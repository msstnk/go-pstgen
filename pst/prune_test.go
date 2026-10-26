package pst

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildPrunable writes a PST with Inbox and a Purview-like Recoverable Items tree.
func buildPrunable(t *testing.T, cm CryptMethod) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "p.pst")
	s, err := Create(p, "test@example.com", []byte("0123456789abcdef"), 1<<20, cm)
	if err != nil {
		t.Fatal(err)
	}
	inbox := s.AddFolder(s.Top, "Inbox")
	ri := s.AddFolder(s.Root, "Recoverable Items")
	del := s.AddFolder(ri, "Deletions")
	holds := s.AddFolder(ri, "DiscoveryHolds")
	deep := s.AddFolder(holds, "Deep")
	for i := 0; i < 100; i++ {
		s.AddMessage(inbox, testMessage(i))
	}
	for i := 0; i < 120; i++ {
		s.AddMessage(del, testMessage(i))
	}
	big := testMessage(1)
	big.Attachments = []Attachment{{Name: "big.bin", Data: make([]byte, xbMax*blockMax+12345)}}
	s.AddMessage(holds, big)
	s.AddMessage(deep, testMessage(7))
	s.AddMessage(ri, testMessage(3)) // directly in Recoverable Items: also matched by the trailing "*"
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrune(t *testing.T) {
	for name, cm := range map[string]CryptMethod{"none": CryptNone, "permute": CryptPermute, "cyclic": CryptCyclic} {
		for _, noFill := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				p := buildPrunable(t, cm)
				before, err := openPST(p, false)
				if err != nil {
					t.Fatal(err)
				}
				n0 := len(before.nbtEntries())
				blocks := map[uint64]blockRef{}
				before.bbt.each(func(e []byte) {
					blocks[le.Uint64(e)] = blockRef{le.Uint64(e[8:]), int(le.Uint16(e[16:]))}
				})
				before.close()

				res, err := Prune(p, PruneOptions{Paths: []string{"recoverable items/*"}, NoFill: noFill})
				if err != nil {
					t.Fatal(err)
				}
				if len(res.Folders) != 2 || res.Items != 1 || res.Rows != 3 || res.Shared != 0 {
					t.Errorf("unexpected result %+v", res)
				}
				if noFill != (res.Blocks == 0) {
					t.Errorf("blocks zero-filled: %d with noFill=%v", res.Blocks, noFill)
				}

				f, err := openPST(p, false)
				if err != nil {
					t.Fatal(err)
				}
				defer f.close()
				if len(f.nbtEntries()) != n0-res.Nodes {
					t.Errorf("NBT has %d entries, want %d", len(f.nbtEntries()), n0-res.Nodes)
				}
				// every retained node must still be fully readable
				for _, e := range f.nbtEntries() {
					if err := f.walkNode(e, func(uint64) {}); err != nil {
						t.Errorf("node %#x: %v", e.nid, err)
					}
					if _, err := f.dataBlocks(e.data); err != nil {
						t.Errorf("node %#x: %v", e.nid, err)
					}
				}
				// Recoverable Items stays, its subfolders are gone, and so are their rows in its hierarchy table
				again, err := Prune(p, PruneOptions{Paths: []string{"Recoverable Items/*"}, DryRun: true})
				if !errors.Is(err, ErrNoMatch) {
					t.Errorf("second run: %v %+v", err, again)
				}
				if r, err := Prune(p, PruneOptions{Paths: []string{"Recoverable Items"}, DryRun: true}); err != nil || r.Rows != 1 || r.Nodes == 0 {
					t.Errorf("Recoverable Items itself: %v %+v", err, r)
				}
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				left := map[uint64]bool{}
				f.bbt.each(func(e []byte) { left[le.Uint64(e)] = true })
				zeroed := 0
				for bid, r := range blocks {
					if left[bid] {
						continue
					}
					zeroed++
					for _, c := range raw[r.ib : r.ib+r.total()] {
						if c != 0 {
							t.Fatalf("block %#x is not zero-filled", bid)
						}
					}
				}
				if zeroed != res.Blocks {
					t.Errorf("%d blocks dropped from the BBT, result says %d", zeroed, res.Blocks)
				}
				if !noFill {
					checkFile(t, p) // a pruned file must stay consistent for the strict verifier
				}
			})
		}
	}
}

func TestPruneGuards(t *testing.T) {
	p := buildPrunable(t, CryptPermute)
	if _, err := Prune(p, PruneOptions{Paths: []string{"Top of Personal Folders"}, DryRun: true}); err == nil {
		t.Error("pruning the IPM subtree must be refused")
	}
	if _, err := Prune(p, PruneOptions{Paths: []string{"Nope/*"}, DryRun: true}); !errors.Is(err, ErrNoMatch) {
		t.Errorf("got %v", err)
	}
	// IPM-relative path
	if r, err := Prune(p, PruneOptions{Paths: []string{"inbox"}, DryRun: true}); err != nil || len(r.Folders) != 1 {
		t.Errorf("%v %+v", err, r)
	}
}

// TestPruneLargeHierarchy covers hierarchy tables whose row matrix lives in a subnode (single block and XBLOCK), including emptying it.
func TestPruneLargeHierarchy(t *testing.T) {
	for _, tc := range []struct {
		kids    int
		pattern string
		rows    int
	}{
		{40, "bulk/c00*", 10}, // matrix in one subnode block
		{300, "bulk/c0*", 100},
		{300, "bulk/c1*", 100},
		{300, "bulk/*", 300}, // emptied
		{300, "bulk/c299", 1},
		{1000, "bulk/c1*", 100}, // multi-block heap, multi-level row index
		{1000, "bulk/*", 1000},
		{1000, "bulk/c9*", 100},
	} {
		p := filepath.Join(t.TempDir(), "h.pst")
		s, err := Create(p, "test@example.com", []byte("0123456789abcdef"), 1<<20, CryptPermute)
		if err != nil {
			t.Fatal(err)
		}
		bulk := s.AddFolder(s.Top, "Bulk")
		for i := 0; i < tc.kids; i++ {
			c := s.AddFolder(bulk, fmt.Sprintf("c%03d", i))
			if i < 50 {
				s.AddMessage(c, testMessage(i))
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		res, err := Prune(p, PruneOptions{Paths: []string{tc.pattern}})
		if err != nil || res.Rows != tc.rows {
			t.Fatalf("%d kids, %s: %v %+v", tc.kids, tc.pattern, err, res)
		}
		checkFile(t, p)
		f, err := openPST(p, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range f.nbtEntries() {
			if err := f.walkNode(e, func(uint64) {}); err != nil {
				t.Errorf("node %#x: %v", e.nid, err)
			}
		}
		// the parent keeps hasSubfolders exactly while it has subfolders left
		for _, e := range f.nbtEntries() {
			if e.nid&0x1F != nidFolder {
				continue
			}
			if n, _ := f.folderName(e.nid); n != "Bulk" {
				continue
			}
			h, err := f.heapNode(e.nid)
			if err != nil {
				t.Fatal(err)
			}
			_, v, _, _ := h.pcValue(0x360A)
			if want := tc.rows != tc.kids; (v&0xFF != 0) != want {
				t.Errorf("%d kids, %s: hasSubfolders = %d, want %v", tc.kids, tc.pattern, v&0xFF, want)
			}
		}
		f.close()
	}
}
