package pst

import (
	"fmt"
	"os"
)

// This file is the read / in-place edit side of the NDB layer, used by Prune.
// Both B-Trees are loaded into memory as 512-byte pages, modified there, and written back page by page.

const (
	headerSize = 564
	nbtEntSize = 32
	bbtEntSize = 24
)

// File is an existing Unicode PST opened for inspection and in-place editing. It reads the NDB layer ([MS-PST] 2.2): the
// Node BTree and Block BTree reached from HEADER.root, and the blocks they locate.
type File struct {
	f       *os.File
	crypt   CryptMethod
	nbt     *btree
	bbt     *btree
	rw      bool
	pending map[uint64][]byte // staged plain block contents (see stage)
	zeroes  [64 << 10]byte
}

// nbtEntry is a decoded NBTENTRY ([MS-PST] 2.2.2.7.7.4).
type nbtEntry struct {
	nid, parent uint32
	data, sub   uint64
}

// openPST opens path. With rw false the file is opened read-only.
func openPST(path string, rw bool) (*File, error) {
	mode := os.O_RDONLY
	if rw {
		mode = os.O_RDWR
	}
	osf, err := os.OpenFile(path, mode, 0)
	if err != nil {
		return nil, err
	}
	f := &File{f: osf, rw: rw}
	if err := f.load(); err != nil {
		osf.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (f *File) close() error { return f.f.Close() }

// load validates the HEADER ([MS-PST] 2.2.2.6: dwMagic, wVer, both CRCs, bCryptMethod) and loads the BBT and NBT from ROOT.
func (f *File) load() error {
	h := make([]byte, headerSize)
	if _, err := f.f.ReadAt(h, 0); err != nil || string(h[:4]) != pstMagic {
		return fmt.Errorf("not a PST file")
	}
	// Unicode PSTs have wVer >= 23 ([MS-PST] 2.2.2.6); 14 and 15 are ANSI. 37 is written by an Outlook that supports Windows
	// Information Protection, whose data MAY be protected (and then cannot be read here). The spec describes no other layout
	// for values above 23, so they are read like 23.
	if v := le.Uint16(h[hdrVerOff:]); v < verUnicode {
		return fmt.Errorf("unsupported PST version %d (only Unicode, wVer >= 23)", v)
	}
	if pstCRC(h[hdrMagicClientOff:hdrCRCPartialEnd]) != le.Uint32(h[hdrCRCPartialOff:]) || pstCRC(h[hdrMagicClientOff:hdrCRCFullEnd]) != le.Uint32(h[hdrCRCFullOff:]) {
		return fmt.Errorf("header CRC mismatch")
	}
	f.crypt = CryptMethod(h[hdrCryptOff])
	if f.crypt > CryptCyclic {
		return fmt.Errorf("unsupported encryption method %#x", h[hdrCryptOff])
	}
	var err error
	if f.bbt, err = f.loadTree(le.Uint64(h[hdrRootOff+rootBBTOff:]), le.Uint64(h[hdrRootOff+rootBBTOff+8:]), ptypeBBT, bbtEntSize); err != nil {
		return fmt.Errorf("BBT: %w", err)
	}
	if f.nbt, err = f.loadTree(le.Uint64(h[hdrRootOff+rootNBTOff:]), le.Uint64(h[hdrRootOff+rootNBTOff+8:]), ptypeNBT, nbtEntSize); err != nil {
		return fmt.Errorf("NBT: %w", err)
	}
	return nil
}

// ---- B-Tree ----
// Pages are BTPAGEs ([MS-PST] 2.2.2.7.7.1). Intermediate entries are BTENTRYs {btkey, BREF} ([MS-PST] 2.2.2.7.7.2), where btkey
// is the smallest key under the child; leaf entries are NBTENTRYs or BBTENTRYs.

type btPage struct {
	ib    uint64
	buf   [pageSize]byte
	level int
	kids  []*btPage // internal pages only, parallel to the entries
	dirty bool
	dead  bool // became empty and was unlinked from its parent
}

func (p *btPage) cnt() int { return int(p.buf[btCntOff]) }

type btree struct {
	ptype byte
	ent   int // leaf entry size
	root  *btPage
	pages []*btPage
}

// loadTree loads the BTree whose root page is at ib with BID bid. ent is its leaf entry size (nbtEntSize or bbtEntSize).
func (f *File) loadTree(bid, ib uint64, ptype byte, ent int) (*btree, error) {
	t := &btree{ptype: ptype, ent: ent}
	root, err := f.loadPage(t, bid, ib, -1)
	if err != nil {
		return nil, err
	}
	t.root = root
	return t, nil
}

// loadPage reads a page, checks its PAGETRAILER ([MS-PST] 2.2.2.7.1: ptype, dwCRC, bid) and, recursively, its children. level is the expected level (-1: unknown, for the root).
func (f *File) loadPage(t *btree, bid, ib uint64, level int) (*btPage, error) {
	p := &btPage{ib: ib}
	if _, err := f.f.ReadAt(p.buf[:], int64(ib)); err != nil {
		return nil, fmt.Errorf("page @%#x: %w", ib, err)
	}
	tr := p.buf[pageTrailerOff:]
	switch {
	case tr[0] != t.ptype || tr[1] != t.ptype:
		return nil, fmt.Errorf("page @%#x: bad page type %#x", ib, tr[0])
	case pstCRC(p.buf[:pageTrailerOff]) != le.Uint32(tr[trailerCRCOff:]):
		return nil, fmt.Errorf("page @%#x: CRC mismatch", ib)
	case le.Uint64(tr[trailerBIDOff:]) != bid:
		return nil, fmt.Errorf("page @%#x: BID %#x, want %#x", ib, le.Uint64(tr[trailerBIDOff:]), bid)
	}
	p.level = int(p.buf[btLevelOff])
	if level >= 0 && p.level != level {
		return nil, fmt.Errorf("page @%#x: level %d, want %d", ib, p.level, level)
	}
	cnt, es := p.cnt(), int(p.buf[btEntSizeOff])
	want := t.ent
	if p.level > 0 {
		want = btIntEntSize
	}
	if es != want || cnt*es > btBodySize {
		return nil, fmt.Errorf("page @%#x: bad entry size/count %d/%d", ib, es, cnt)
	}
	t.pages = append(t.pages, p)
	if p.level == 0 {
		return p, nil
	}
	for i := 0; i < cnt; i++ {
		e := p.buf[btIntEntSize*i:]
		k, err := f.loadPage(t, le.Uint64(e[8:]), le.Uint64(e[16:]), p.level-1)
		if err != nil {
			return nil, err
		}
		p.kids = append(p.kids, k)
	}
	return p, nil
}

// each calls fn for every leaf entry in key order.
func (t *btree) each(fn func(e []byte)) {
	var rec func(p *btPage)
	rec = func(p *btPage) {
		if p.level == 0 {
			for i := 0; i < p.cnt(); i++ {
				fn(p.buf[i*t.ent : (i+1)*t.ent])
			}
			return
		}
		for _, k := range p.kids {
			rec(k)
		}
	}
	rec(t.root)
}

// find returns the leaf page and slot of key (nil if absent). In an intermediate page it follows the last entry whose btkey is
// <= key.
func (t *btree) find(key uint64) (*btPage, int) {
	p := t.root
	for p.level > 0 {
		idx := -1
		for i := 0; i < p.cnt(); i++ {
			if le.Uint64(p.buf[btIntEntSize*i:]) > key {
				break
			}
			idx = i
		}
		if idx < 0 {
			return nil, 0
		}
		p = p.kids[idx]
	}
	for i := 0; i < p.cnt(); i++ {
		if le.Uint64(p.buf[i*t.ent:]) == key {
			return p, i
		}
	}
	return nil, 0
}

// remove deletes the entries whose key is in del and unlinks pages that become empty.
// It reports false (and the tree must be discarded) if that would empty the whole tree.
func (t *btree) remove(del map[uint64]struct{}) bool {
	var rec func(p *btPage) bool // reports whether p is now empty
	rec = func(p *btPage) bool {
		n := p.cnt()
		if p.level == 0 {
			w := 0
			for i := 0; i < n; i++ {
				e := p.buf[i*t.ent : (i+1)*t.ent]
				if _, ok := del[le.Uint64(e)]; ok {
					continue
				}
				copy(p.buf[w*t.ent:], e)
				w++
			}
			if w != n {
				clear(p.buf[w*t.ent : n*t.ent])
				p.buf[btCntOff] = byte(w)
				p.dirty = true
			}
			p.dead = w == 0
			return p.dead
		}
		w := 0
		for i := 0; i < n; i++ {
			if rec(p.kids[i]) {
				continue
			}
			copy(p.buf[btIntEntSize*w:], p.buf[btIntEntSize*i:btIntEntSize*(i+1)])
			p.kids[w] = p.kids[i]
			w++
		}
		if w != n {
			clear(p.buf[btIntEntSize*w : btIntEntSize*n])
			p.kids = p.kids[:w]
			p.buf[btCntOff] = byte(w)
			p.dirty = true
		}
		p.dead = w == 0
		return p.dead
	}
	return !rec(t.root)
}

// flush writes the modified pages back. Pages unlinked by remove are zeroed when zeroDead is set.
func (f *File) flush(t *btree, zeroDead bool) error {
	for _, p := range t.pages {
		switch {
		case p.dead:
			if zeroDead {
				if err := f.zero(p.ib, pageSize); err != nil {
					return err
				}
			}
		case p.dirty:
			le.PutUint32(p.buf[pageTrailerOff+trailerCRCOff:], pstCRC(p.buf[:pageTrailerOff]))
			if _, err := f.f.WriteAt(p.buf[:], int64(p.ib)); err != nil {
				return err
			}
			p.dirty = false
		}
	}
	return nil
}

func (f *File) zero(off, n uint64) error {
	for n > 0 {
		c := min(n, uint64(len(f.zeroes)))
		if _, err := f.f.WriteAt(f.zeroes[:c], int64(off)); err != nil {
			return err
		}
		off += c
		n -= c
	}
	return nil
}

// ---- NBT / BBT lookups ----

func (f *File) nbtEntries() []nbtEntry {
	var out []nbtEntry
	f.nbt.each(func(e []byte) { out = append(out, decodeNBT(e)) })
	return out
}

// decodeNBT decodes an NBTENTRY {nid (8 bytes), bidData, bidSub, nidParent}.
func decodeNBT(e []byte) nbtEntry {
	return nbtEntry{nid: le.Uint32(e), data: le.Uint64(e[8:]), sub: le.Uint64(e[16:]), parent: le.Uint32(e[24:])}
}

func (f *File) node(nid uint32) (nbtEntry, bool) {
	p, i := f.nbt.find(uint64(nid))
	if p == nil {
		return nbtEntry{}, false
	}
	return decodeNBT(p.buf[i*nbtEntSize:]), true
}

// blockRef locates a block: its offset and cb, the data size from its BBTENTRY ([MS-PST] 2.2.2.7.7.3).
type blockRef struct {
	ib uint64
	cb int
}

// total is the on-disk size: data, padding and BLOCKTRAILER, rounded up to a multiple of 64 bytes ([MS-PST] 2.2.2.8.2).
func (r blockRef) total() uint64 {
	return uint64(r.cb+blockTrailerSize+blockAlign-1) &^ (blockAlign - 1)
}

func (f *File) blockRef(bid uint64) (blockRef, error) {
	p, i := f.bbt.find(bid)
	if p == nil {
		return blockRef{}, fmt.Errorf("BID %#x not in BBT", bid)
	}
	e := p.buf[i*bbtEntSize:]
	return blockRef{ib: le.Uint64(e[8:]), cb: int(le.Uint16(e[16:]))}, nil
}

// rawBlock reads and verifies a block (BLOCKTRAILER cb, bid and dwCRC, [MS-PST] 2.2.2.8.1), returning its on-disk (still encrypted) contents.
func (f *File) rawBlock(bid uint64) ([]byte, error) {
	r, err := f.blockRef(bid)
	if err != nil {
		return nil, err
	}
	if r.cb > blockMax {
		return nil, fmt.Errorf("block %#x: cb %d too large", bid, r.cb)
	}
	buf := make([]byte, r.total())
	if _, err := f.f.ReadAt(buf, int64(r.ib)); err != nil {
		return nil, fmt.Errorf("block %#x @%#x: %w", bid, r.ib, err)
	}
	t := buf[len(buf)-blockTrailerSize:]
	switch {
	case int(le.Uint16(t)) != r.cb || le.Uint64(t[trailerBIDOff:]) != bid:
		return nil, fmt.Errorf("block %#x @%#x: bad trailer", bid, r.ib)
	case pstCRC(buf[:r.cb]) != le.Uint32(t[trailerCRCOff:]):
		return nil, fmt.Errorf("block %#x @%#x: CRC mismatch", bid, r.ib)
	}
	return buf[:r.cb], nil
}

// readBlock returns the plain contents of a block (decrypting data blocks).
func (f *File) readBlock(bid uint64) ([]byte, error) {
	if p, ok := f.pending[bid]; ok {
		return append([]byte(nil), p...), nil
	}
	b, err := f.rawBlock(bid)
	if err != nil {
		return nil, err
	}
	if bid&bidInternal == 0 {
		f.crypt.decrypt(b, bid)
	}
	return b, nil
}

// dataBlocks returns the plain data blocks of a data tree: one data block, or those under an XBLOCK / XXBLOCK ([MS-PST] 2.2.2.8.3.2).
func (f *File) dataBlocks(bid uint64) ([][]byte, error) {
	if bid == 0 {
		return nil, nil
	}
	if bid&bidInternal == 0 {
		b, err := f.readBlock(bid)
		return [][]byte{b}, err
	}
	b, err := f.readBlock(bid)
	if err != nil {
		return nil, err
	}
	kids, err := xblockKids(bid, b)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, k := range kids {
		bs, err := f.dataBlocks(k)
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return out, nil
}

// xblockKids returns the BIDs an XBLOCK / XXBLOCK refers to ([MS-PST] 2.2.2.8.3.2).
func xblockKids(bid uint64, b []byte) ([]uint64, error) {
	if len(b) < 8 || b[0] != btypeXBlock {
		return nil, fmt.Errorf("block %#x: not an XBLOCK", bid)
	}
	n := int(le.Uint16(b[2:]))
	if internalHdrSize+bidSize*n > len(b) {
		return nil, fmt.Errorf("block %#x: truncated XBLOCK", bid)
	}
	kids := make([]uint64, n)
	for i := range kids {
		kids[i] = le.Uint64(b[8+8*i:])
	}
	return kids, nil
}

// walkData calls visit for every block of a data tree. Only internal blocks are read.
func (f *File) walkData(bid uint64, visit func(uint64)) error {
	if bid == 0 {
		return nil
	}
	visit(bid)
	if bid&bidInternal == 0 {
		return nil
	}
	b, err := f.rawBlock(bid)
	if err != nil {
		return err
	}
	kids, err := xblockKids(bid, b)
	if err != nil {
		return err
	}
	for _, k := range kids {
		if err := f.walkData(k, visit); err != nil {
			return err
		}
	}
	return nil
}

// walkSub calls visit for every block of a subnode tree, including the data and subnode trees of the subnodes.
func (f *File) walkSub(bid uint64, visit func(uint64)) error {
	if bid == 0 {
		return nil
	}
	visit(bid)
	b, err := f.rawBlock(bid)
	if err != nil {
		return err
	}
	if len(b) < 8 || b[0] != btypeSLBlock {
		return fmt.Errorf("block %#x: not an SLBLOCK/SIBLOCK", bid)
	}
	n := int(le.Uint16(b[2:]))
	if b[1] == levelSLBlock {
		if internalHdrSize+slEntSize*n > len(b) {
			return fmt.Errorf("block %#x: truncated SLBLOCK", bid)
		}
		for i := 0; i < n; i++ {
			e := b[8+24*i:]
			if err := f.walkData(le.Uint64(e[8:]), visit); err != nil {
				return err
			}
			if err := f.walkSub(le.Uint64(e[16:]), visit); err != nil {
				return err
			}
		}
		return nil
	}
	if 8+16*n > len(b) {
		return fmt.Errorf("block %#x: truncated SIBLOCK", bid)
	}
	for i := 0; i < n; i++ {
		if err := f.walkSub(le.Uint64(b[16+16*i:]), visit); err != nil {
			return err
		}
	}
	return nil
}

// walkNode visits the BIDs of every block of a node: its data tree and its subnode tree.
func (f *File) walkNode(e nbtEntry, visit func(uint64)) error {
	if err := f.walkData(e.data, visit); err != nil {
		return err
	}
	return f.walkSub(e.sub, visit)
}

// writeBlock rewrites an existing block in place with new plain contents (cb may shrink) and updates its BBT entry.
// The BID, and so the key for encryption, stays the same ([MS-PST] 5.2). The BLOCKTRAILER gets the new cb and dwCRC;
// wSig depends only on the offset and BID ([MS-PST] 5.5), which do not change.
func (f *File) writeBlock(bid uint64, data []byte) error {
	r, err := f.blockRef(bid)
	if err != nil {
		return err
	}
	if len(data) > blockMax {
		return fmt.Errorf("block %#x: cannot rewrite", bid)
	}
	n := blockRef{r.ib, len(data)}
	buf := make([]byte, n.total())
	copy(buf, data)
	if bid&bidInternal == 0 {
		f.crypt.encrypt(buf[:len(data)], bid)
	}
	t := buf[len(buf)-blockTrailerSize:]
	le.PutUint16(t[0:], uint16(len(data)))
	le.PutUint16(t[2:], sig(r.ib, bid))
	le.PutUint32(t[4:], pstCRC(buf[:len(data)]))
	le.PutUint64(t[8:], bid)
	if _, err := f.f.WriteAt(buf, int64(r.ib)); err != nil {
		return err
	}
	if old := r.total(); old > n.total() {
		if err := f.zero(r.ib+n.total(), old-n.total()); err != nil {
			return err
		}
	}
	p, i := f.bbt.find(bid)
	le.PutUint16(p.buf[i*bbtEntSize+16:], uint16(len(data)))
	p.dirty = true
	return nil
}
