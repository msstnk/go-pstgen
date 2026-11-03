package pst

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildSmall writes a small PST (a few folders, a nested one, messages with an attachment) for corruption tests.
func buildSmall(t *testing.T, cm CryptMethod) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "small.pst")
	s, err := Create(p, "test@example.com", []byte("0123456789abcdef"), 1<<20, cm)
	if err != nil {
		t.Fatal(err)
	}
	inbox := s.AddFolder(s.Top, "Inbox")
	ri := s.AddFolder(s.Root, "Recoverable Items")
	del := s.AddPlainFolder(ri, "Deletions")
	for i := 0; i < 5; i++ {
		s.AddMessage(inbox, testMessage(i))
	}
	m := testMessage(9)
	m.Attachments = []Attachment{{Name: "a.bin", Data: make([]byte, 3000)}}
	s.AddMessage(del, m)
	cal := s.AddCalendarFolder(s.Top, "Calendar")
	mm := testMessage(10)
	mm.Appt = &Appointment{Start: mm.Submit, End: mm.Submit.Add(time.Hour), Location: "room"}
	s.AddMessage(cal, mm)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// flipStride is the step between the offsets that the byte-flip tests try, so that about target of them are tried
// (a quarter of that with -short).
func flipStride(size, target int) int {
	if testing.Short() {
		target /= 4
	}
	return max(size/target, 1)
}

// readAll runs the read paths on data: CountItems and a dry-run Prune. It reports a panic as an error string.
func readAll(dir string, data []byte) (panicked any, errs [2]error) {
	p := filepath.Join(dir, "c.pst")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return nil, [2]error{err, err}
	}
	defer func() { panicked = recover() }()
	_, errs[0] = CountItems(p)
	_, errs[1] = Prune(p, PruneOptions{Paths: []string{"Recoverable Items/*", "Inbox"}, DryRun: true})
	return
}

// Damage to the header or a cut-off file is reported as an error by every read path.
func TestCorruptHeader(t *testing.T) {
	good := buildSmall(t, CryptPermute)
	mut := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	cases := map[string][]byte{
		"empty":            {},
		"short":            good[:100],
		"header only":      good[:headerSize],
		"cut in half":      good[:len(good)/2],
		"cut last page":    good[:len(good)-pageSize],
		"bad magic":        mut(func(b []byte) { b[0] ^= 0xFF }),
		"ANSI version":     mut(func(b []byte) { le.PutUint16(b[hdrVerOff:], 15) }),
		"header CRC":       mut(func(b []byte) { b[hdrNIDOff] ^= 1 }),
		"partial CRC only": mut(func(b []byte) { le.PutUint32(b[hdrCRCPartialOff:], 0) }),
		"unknown crypt":    mut(func(b []byte) { b[hdrCryptOff] = 9 }),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			pan, errs := readAll(t.TempDir(), data)
			if pan != nil {
				t.Fatalf("panic: %v", pan)
			}
			if errs[0] == nil {
				t.Error("CountItems accepted the file")
			}
			if errs[1] == nil {
				t.Error("Prune accepted the file")
			}
		})
	}
}

// Flipping any single byte of a valid PST must never panic or hang the readers: they return an error or, when the byte
// is unused or covered by nothing they check, succeed. Every header byte (with one encryption method) and a stride over the rest are tried.
func TestCorruptByteFlips(t *testing.T) {
	for name, cm := range map[string]CryptMethod{"none": CryptNone, "permute": CryptPermute, "cyclic": CryptCyclic} {
		t.Run(name, func(t *testing.T) {
			good := buildSmall(t, cm)
			stride := flipStride(len(good), 400)
			hdrStride := 1 // every header byte once (the header does not depend on the encryption method, except bCryptMethod)
			if cm != CryptPermute {
				hdrStride = 8
			}
			dir := t.TempDir()
			bad := make([]byte, len(good))
			done := make(chan struct{})
			go func() {
				defer close(done)
				for off := 0; off < len(good); off++ {
					if (off >= headerSize && off%stride != 0) || (off < headerSize && off%hdrStride != 0) {
						continue
					}
					copy(bad, good)
					bad[off] ^= 0xFF
					if pan, _ := readAll(dir, bad); pan != nil {
						t.Errorf("flip at offset %d (%#x): panic: %v", off, off, pan)
					}
				}
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Minute):
				t.Fatal("timed out: a flipped byte probably makes a reader loop forever")
			}
		})
	}
}

// A mutation target: one byte of a structure the parsers rely on. It lives either in a page of the NBT/BBT (page != 0 is its
// file offset) or in a block (bid), at off within the page or the plain (decrypted) contents of the block.
type mutTarget struct {
	page, bid uint64
	off       int
	what      string
}

// structuralTargets lists the bytes whose damage the parsers have to notice: the headers and entries of the B-tree pages,
// the headers of XBLOCKs and subnode blocks, and in every Heap-on-Node the HNHDR, the HNPAGEMAP, the BTHHEADER, the
// TCINFO and the first bytes of the other allocations (BTH records, column descriptors, ...).
// Only the nodes that CountItems and Prune parse are considered (folders and their hierarchy, contents and associated
// contents tables): damage in a message or a recipient table is invisible to them by design.
func structuralTargets(t *testing.T, f *File) []mutTarget {
	t.Helper()
	var out []mutTarget
	add := func(tg mutTarget, from, to int) {
		for o := from; o < to; o++ {
			tg.off = o
			out = append(out, tg)
		}
	}
	for name, tree := range map[string]*btree{"NBT page": f.nbt, "BBT page": f.bbt} {
		for _, p := range tree.pages {
			add(mutTarget{page: p.ib, what: name + " entries"}, 0, 32)
			add(mutTarget{page: p.ib, what: name + " cEnt/cEntMax/cbEnt/cLevel"}, btCntOff, btLevelOff+1)
		}
	}
	read := map[uint64]bool{} // blocks of the nodes the readers parse
	for _, e := range f.nbtEntries() {
		switch e.nid & nidTypeMask {
		case nidFolder, nidHierarchy, nidContents, nidAssoc, nidSearchContents:
			if err := f.walkNode(e, func(bid uint64) { read[bid] = true }); err != nil {
				t.Fatal(err)
			}
		}
	}
	seen := map[uint64]bool{}
	f.bbt.each(func(e []byte) {
		bid := le.Uint64(e)
		if seen[bid] || !read[bid] {
			return
		}
		seen[bid] = true
		b, err := f.readBlock(bid)
		if err != nil {
			t.Fatal(err)
		}
		if bid&bidInternal != 0 {
			add(mutTarget{bid: bid, what: "internal block"}, 0, min(32, len(b)))
			return
		}
		if len(b) < hnHdrSize || b[2] != hnSig {
			return // not the first block of a heap
		}
		add(mutTarget{bid: bid, what: "HNHDR"}, 0, hnHdrSize)
		pm := int(le.Uint16(b))
		if pm+4 > len(b) {
			t.Fatalf("block %#x: HNPAGEMAP at %d beyond %d", bid, pm, len(b))
		}
		n := int(le.Uint16(b[pm:]))
		add(mutTarget{bid: bid, what: "HNPAGEMAP"}, pm, min(pm+4+2*(n+1), len(b), pm+4+2*10))
		root := le.Uint32(b[4:])
		for i := 1; i <= min(n, 8); i++ {
			hid := uint32(i) << hidIdxShift
			s, e, err := hnRange(b, hid)
			if err != nil {
				t.Fatal(err)
			}
			width, what := 8, "heap allocation"
			if hid == root {
				width, what = 24, "BTHHEADER/TCINFO"
			}
			add(mutTarget{bid: bid, what: what}, s, min(s+width, e))
		}
	})
	return out
}

// mutate sets one byte of the file at path as tg says and fixes the checksums up, so the integrity checks pass and the
// parsers see the damage. It reports false when the byte already has that value.
func mutate(path string, tg mutTarget, v byte) (bool, error) {
	f, err := openPST(path, true)
	if err != nil {
		return false, err
	}
	defer f.close()
	if tg.page != 0 {
		for _, tree := range []*btree{f.nbt, f.bbt} {
			for _, p := range tree.pages {
				if p.ib == tg.page {
					if p.buf[tg.off] == v {
						return false, nil
					}
					p.buf[tg.off], p.dirty = v, true
					return true, f.flush(tree, false)
				}
			}
		}
		return false, fmt.Errorf("page @%#x not found", tg.page)
	}
	b, err := f.readBlock(tg.bid)
	if err != nil {
		return false, err
	}
	if b[tg.off] == v {
		return false, nil
	}
	b[tg.off] = v
	if err := f.writeBlock(tg.bid, b); err != nil {
		return false, err
	}
	return true, f.flush(f.bbt, false)
}

// readEverything runs the package's own parsers over every node of the file: the block trees, the folder properties, and
// the Table Contexts (heap, row index and row matrix) of the tables Prune edits. It returns the first error.
func readEverything(path string) error {
	f, err := openPST(path, false)
	if err != nil {
		return err
	}
	defer f.close()
	var first error
	note := func(err error) {
		if first == nil {
			first = err
		}
	}
	for _, e := range f.nbtEntries() {
		note(f.walkNode(e, func(uint64) {}))
		switch e.nid & nidTypeMask {
		case nidFolder:
			_, err := f.folderName(e.nid)
			note(err)
		case nidHierarchy, nidContents, nidAssoc, nidSearchContents:
			tab, err := f.loadTable(e)
			note(err)
			if err == nil && tab.rowsHnid&nidTypeMask == nidLTP {
				_, err := f.loadMatrix(e, tab)
				note(err)
			}
		}
	}
	return first
}

// Damage to the structures inside a valid, checksummed PST (heap headers, BTH, TCINFO, page maps, B-tree pages...) must never
// make a reader panic or hang. Whenever Prune reports an error it has to have left the file exactly as it was, so a damaged
// file is never half-pruned. Each damaged byte is set to 0x00 and to 0xFF, with all CRCs recomputed.
//
// It tries every target (about 3000 bytes, 30 s for the three encryption methods), so it only runs when PST_CORRUPT_FULL is
// set (make test-full): run it after changing a parser and before a release. A sample of it added only 0.3 points of coverage
// to the regular run, which the targeted tests of each parser already give.
func TestCorruptStructure(t *testing.T) {
	if os.Getenv("PST_CORRUPT_FULL") == "" {
		t.Skip("set PST_CORRUPT_FULL=1 (make test-full) to run it")
	}
	for name, cm := range map[string]CryptMethod{"none": CryptNone, "permute": CryptPermute, "cyclic": CryptCyclic} {
		t.Run(name, func(t *testing.T) {
			good := buildSmall(t, cm)
			dir := t.TempDir()
			path := filepath.Join(dir, "c.pst")
			if err := os.WriteFile(path, good, 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := openPST(path, false)
			if err != nil {
				t.Fatal(err)
			}
			targets := structuralTargets(t, f)
			f.close()
			opt := PruneOptions{Paths: []string{"Recoverable Items/*", "Inbox"}}

			var tried, rejected int
			done := make(chan struct{})
			go func() {
				defer close(done)
				for _, tg := range targets {
					for _, v := range []byte{0x00, 0xFF} {
						if err := os.WriteFile(path, good, 0o644); err != nil {
							t.Error(err)
							return
						}
						changed, err := mutate(path, tg, v)
						if err != nil {
							t.Errorf("%+v: mutate: %v", tg, err)
							return
						}
						if !changed {
							continue
						}
						tried++
						damaged, _ := os.ReadFile(path)
						func() {
							defer func() {
								if r := recover(); r != nil {
									t.Errorf("%s byte %d (block %#x page %#x) = %#x: panic: %v", tg.what, tg.off, tg.bid, tg.page, v, r)
								}
							}()
							e0 := readEverything(path)
							_, e1 := CountItems(path)
							_, e2 := Prune(path, PruneOptions{Paths: opt.Paths, DryRun: true})
							_, e3 := Prune(path, opt)
							if e0 != nil || e1 != nil || e2 != nil || e3 != nil {
								rejected++
							}
							if e3 != nil {
								if after, _ := os.ReadFile(path); !bytes.Equal(after, damaged) {
									t.Errorf("%s byte %d (block %#x page %#x) = %#x: Prune failed (%v) but modified the file", tg.what, tg.off, tg.bid, tg.page, v, e3)
								}
							}
						}()
					}
				}
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Minute):
				t.Fatal("timed out: a damaged structure probably makes a reader loop forever")
			}
			t.Logf("%d targets, %d mutations tried, %d noticed by a reader", len(targets), tried, rejected)
			if rejected < tried/5 {
				t.Errorf("only %d of %d mutations were noticed: the targets probably miss the structures", rejected, tried)
			}
		})
	}
}
