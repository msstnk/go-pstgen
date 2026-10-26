package pst

import (
	"bytes"
	"fmt"
)

// In-place edits of folder nodes (hierarchy table rows, hasSubfolders flags) used by Prune.
// Edits are staged in File.pending (plain block contents by BID) so that several edits of one table compose;
// Prune writes the staged blocks at the end.

// stage records new plain contents for a block.
func (f *File) stage(bid uint64, data []byte) {
	if f.pending == nil {
		f.pending = map[uint64][]byte{}
	}
	f.pending[bid] = data
}

// heapPages is the heap of a node: one block, or the data blocks of a single-level XBLOCK.
type heapPages struct {
	xbid  uint64
	bids  []uint64
	orig  [][]byte
	pages [][]byte // modified in place, never aliasing orig
}

func (f *File) loadHeap(data uint64) (*heapPages, error) {
	h := &heapPages{}
	if data&2 == 0 {
		h.bids = []uint64{data}
	} else {
		xb, err := f.readBlock(data)
		if err != nil {
			return nil, err
		}
		if len(xb) < 2 || xb[1] != 1 {
			return nil, fmt.Errorf("multi-level heap is not supported")
		}
		if h.bids, err = xblockKids(data, xb); err != nil {
			return nil, err
		}
		h.xbid = data
	}
	for _, b := range h.bids {
		p, err := f.readBlock(b)
		if err != nil {
			return nil, err
		}
		h.orig = append(h.orig, p)
		h.pages = append(h.pages, append([]byte(nil), p...))
	}
	return h, nil
}

func (h *heapPages) replace(hid uint32, data []byte) error {
	pg := int(hid >> 16)
	if pg >= len(h.pages) {
		return fmt.Errorf("HID %#x: no such heap page", hid)
	}
	p, err := hnReplace(h.pages[pg], hid, data)
	h.pages[pg] = p
	return err
}

// save stages the pages that changed. Every page but the last keeps its length (zero-padded), as Outlook finds
// page i at offset i*blockMax; the XBLOCK total is updated.
func (f *File) saveHeap(h *heapPages) {
	last := len(h.pages) - 1
	if last < 8 && last >= 0 { // fill level of the last page (the earlier ones stay full)
		sh := uint(4 * (last % 2))
		h.pages[0][8+last/2] = h.pages[0][8+last/2]&^(0xF<<sh) | fillLevel(blockMax-len(h.pages[last]))<<sh
	}
	total, changed := 0, false
	for j, p := range h.pages {
		if j < last && len(p) < len(h.orig[j]) {
			p = append(p, make([]byte, len(h.orig[j])-len(p))...)
		}
		total += len(p)
		if !bytes.Equal(p, h.orig[j]) {
			f.stage(h.bids[j], p)
			changed = true
		}
	}
	if h.xbid != 0 && changed {
		xb, _ := f.readBlock(h.xbid)
		le.PutUint32(xb[4:], uint32(total))
		f.stage(h.xbid, xb)
	}
}

// clearPCValue zeroes an inline (boolean / integer) property of a folder's Property Context.
func (f *File) clearPCValue(nid uint32, tag uint16) error {
	e, ok := f.node(nid)
	if !ok || e.data == 0 {
		return nil
	}
	h, err := f.loadHeap(e.data)
	if err != nil {
		return err
	}
	pg, off, ok, err := heapNode(h.pages).pcLocate(tag)
	if err != nil || !ok {
		return err
	}
	if le.Uint32(h.pages[pg][off+4:]) == 0 {
		return nil
	}
	le.PutUint32(h.pages[pg][off+4:], 0)
	f.saveHeap(h)
	return nil
}

// bthNode is a node of the single-key-size BTH (4-byte keys and values) used by the table row index.
type bthNode struct {
	hid  uint32
	kids []*bthNode // nil for leaves
	recs []byte     // leaf records
}

func loadBTH(h heapNode, hid uint32, levels int) (*bthNode, error) {
	a, err := h.alloc(hid)
	if err != nil {
		return nil, err
	}
	n := &bthNode{hid: hid, recs: a}
	if levels == 0 {
		return n, nil
	}
	n.recs = nil
	for i := 0; i+8 <= len(a); i += 8 {
		k, err := loadBTH(h, le.Uint32(a[i+4:]), levels-1)
		if err != nil {
			return nil, err
		}
		n.kids = append(n.kids, k)
	}
	return n, nil
}

func (n *bthNode) leaves(fn func(*bthNode)) {
	if n.kids == nil {
		fn(n)
		return
	}
	for _, k := range n.kids {
		k.leaves(fn)
	}
}

// tableInfo is the parsed head of a Table Context.
type tableInfo struct {
	h        *heapPages
	infoHid  uint32
	info     []byte
	rowSize  int
	idxHid   uint32
	rowsHnid uint32
	root     *bthNode // nil for an empty table
	n        int
}

func (f *File) loadTable(e nbtEntry) (*tableInfo, error) {
	h, err := f.loadHeap(e.data)
	if err != nil {
		return nil, err
	}
	hn := heapNode(h.pages)
	b := h.pages[0]
	if len(b) < 12 || b[2] != 0xEC || b[3] != 0x7C {
		return nil, fmt.Errorf("not a Table Context")
	}
	t := &tableInfo{h: h, infoHid: le.Uint32(b[4:])}
	if t.info, err = hn.alloc(t.infoHid); err != nil || len(t.info) < 22 || t.info[0] != 0x7C {
		return nil, fmt.Errorf("bad TCINFO")
	}
	t.rowSize = int(le.Uint16(t.info[8:]))
	if t.rowSize == 0 || t.rowSize > blockMax {
		return nil, fmt.Errorf("bad row size %d", t.rowSize)
	}
	t.idxHid, t.rowsHnid = le.Uint32(t.info[10:]), le.Uint32(t.info[14:])
	bth, err := hn.alloc(t.idxHid)
	if err != nil || len(bth) < 8 || bth[0] != 0xB5 || bth[1] != 4 || bth[2] != 4 {
		return nil, fmt.Errorf("bad row index BTH")
	}
	if root := le.Uint32(bth[4:]); root != 0 {
		if t.root, err = loadBTH(hn, root, int(bth[3])); err != nil {
			return nil, err
		}
		t.root.leaves(func(l *bthNode) { t.n += len(l.recs) / 8 })
	}
	return t, nil
}

// matrix describes a row matrix stored in a subnode.
type matrix struct {
	kids []uint64 // data blocks
	xbid uint64   // XBLOCK bundling them, or 0
}

func (f *File) loadMatrix(e nbtEntry, t *tableInfo) (*matrix, error) {
	if t.rowsHnid&0x1F != 0x1F {
		return nil, fmt.Errorf("row matrix HNID %#x is neither an HID nor an LTP subnode", t.rowsHnid)
	}
	dbid, ok, err := f.subnode(e.sub, t.rowsHnid)
	if err != nil || !ok {
		return nil, fmt.Errorf("row matrix subnode %#x not found: %v", t.rowsHnid, err)
	}
	m := &matrix{kids: []uint64{dbid}}
	if dbid&2 != 0 {
		m.xbid = dbid
		xb, err := f.readBlock(dbid)
		if err != nil {
			return nil, err
		}
		if len(xb) < 2 || xb[1] != 1 {
			return nil, fmt.Errorf("multi-level row matrix is not supported")
		}
		if m.kids, err = xblockKids(dbid, xb); err != nil {
			return nil, err
		}
	}
	per := blockMax / t.rowSize
	if len(m.kids) != (t.n+per-1)/per {
		return nil, fmt.Errorf("row matrix has %d blocks for %d rows of %d bytes", len(m.kids), t.n, t.rowSize)
	}
	return m, nil
}

// removeTableRows removes the rows whose RowID is in ids from the Table Context of node e (a hierarchy table).
// The heap may span several blocks and the row index may have several levels; the row matrix may be in the heap or in
// a subnode (single block or one XBLOCK). The edit is staged; the BIDs of blocks that are no longer used are returned.
func (f *File) removeTableRows(e nbtEntry, ids map[uint32]bool) (removed int, drop []uint64, err error) {
	t, err := f.loadTable(e)
	if err != nil || t.root == nil {
		return 0, nil, err
	}
	n := t.n
	del := make([]bool, n)
	t.root.leaves(func(l *bthNode) {
		for i := 0; i+8 <= len(l.recs); i += 8 {
			if idx := int(le.Uint32(l.recs[i+4:])); idx < n {
				if ids[le.Uint32(l.recs[i:])] && !del[idx] {
					del[idx] = true
					removed++
				}
			} else {
				err = fmt.Errorf("row index %d out of range", idx)
			}
		}
	})
	if err != nil || removed == 0 {
		return 0, nil, err
	}
	left := n - removed
	rank := make([]int, n)
	for i, k := 0, 0; i < n; i++ {
		if !del[i] {
			rank[i] = k
			k++
		}
	}

	// the row matrix
	rowSize, per := t.rowSize, blockMax/t.rowSize
	hn := heapNode(t.h.pages)
	var rows []byte
	var mx *matrix
	var kidData [][]byte
	if t.rowsHnid&0x1F == 0 {
		if rows, err = hn.alloc(t.rowsHnid); err != nil {
			return 0, nil, err
		}
		if len(rows) != n*rowSize {
			return 0, nil, fmt.Errorf("row matrix size %d does not match %d rows of %d bytes", len(rows), n, rowSize)
		}
	} else {
		if mx, err = f.loadMatrix(e, t); err != nil {
			return 0, nil, err
		}
		for j, k := range mx.kids {
			d, err := f.readBlock(k)
			if err != nil {
				return 0, nil, err
			}
			c := min(per, n-j*per) * rowSize
			if len(d) < c {
				return 0, nil, fmt.Errorf("row matrix block %#x is short", k)
			}
			kidData = append(kidData, d)
			rows = append(rows, d[:c]...)
		}
	}
	newRows := make([]byte, 0, left*rowSize)
	for i := 0; i < n; i++ {
		if !del[i] {
			newRows = append(newRows, rows[i*rowSize:(i+1)*rowSize]...)
		}
	}

	// the row index: rewrite the leaves, then drop emptied nodes from their parents
	var rewrite func(nd *bthNode) (bool, error)
	rewrite = func(nd *bthNode) (bool, error) {
		var out []byte
		if nd.kids == nil {
			for i := 0; i+8 <= len(nd.recs); i += 8 {
				if idx := le.Uint32(nd.recs[i+4:]); !del[idx] {
					out = le.AppendUint32(out, le.Uint32(nd.recs[i:]))
					out = le.AppendUint32(out, uint32(rank[idx]))
				}
			}
		} else {
			a, err := hn.alloc(nd.hid)
			if err != nil {
				return false, err
			}
			a = append([]byte(nil), a...) // the child rewrites below move the page contents
			for i, k := range nd.kids {
				empty, err := rewrite(k)
				if err != nil {
					return false, err
				}
				if !empty {
					out = append(out, a[8*i:8*i+8]...)
				}
			}
		}
		return len(out) == 0, t.h.replace(nd.hid, out)
	}
	if _, err = rewrite(t.root); err != nil {
		return 0, nil, err
	}
	if left == 0 { // an empty table has no row matrix and no BTH root
		s, _, _ := hnRange(t.h.pages[t.infoHid>>16], t.infoHid)
		le.PutUint32(t.h.pages[t.infoHid>>16][s+14:], 0)
		s, _, _ = hnRange(t.h.pages[t.idxHid>>16], t.idxHid)
		le.PutUint32(t.h.pages[t.idxHid>>16][s+4:], 0)
	}
	if mx == nil {
		if err = t.h.replace(t.rowsHnid, newRows); err != nil {
			return 0, nil, err
		}
	} else {
		m := max(1, (left+per-1)/per) // an emptied matrix keeps one empty block, so the subnode stays valid
		total := 0
		for j := 0; j < m; j++ {
			var d []byte
			if j < m-1 { // every block but the last keeps its (padded) length
				d = make([]byte, len(kidData[j]))
				copy(d, newRows[j*per*rowSize:])
			} else {
				d = newRows[min(len(newRows), j*per*rowSize):]
			}
			f.stage(mx.kids[j], d)
			total += len(d)
		}
		drop = mx.kids[m:]
		if mx.xbid != 0 {
			xb := make([]byte, 8+8*m)
			xb[0], xb[1] = 0x01, 0x01
			le.PutUint16(xb[2:], uint16(m))
			le.PutUint32(xb[4:], uint32(total))
			for j := 0; j < m; j++ {
				le.PutUint64(xb[8+8*j:], mx.kids[j])
			}
			f.stage(mx.xbid, xb)
		}
	}
	f.saveHeap(t.h)
	return removed, drop, nil
}

// clearTableCell zeroes a fixed-size cell (column tag, type typ) of the row with the given RowID, if there is one.
func (f *File) clearTableCell(e nbtEntry, rowID uint32, tag, typ uint16) error {
	t, err := f.loadTable(e)
	if err != nil || t.root == nil {
		return err
	}
	ibData, cb, ibit := -1, 0, 0
	for i := 0; i < int(t.info[1]) && 22+8*(i+1) <= len(t.info); i++ {
		d := t.info[22+8*i:]
		if le.Uint32(d) == uint32(tag)<<16|uint32(typ) {
			ibData, cb, ibit = int(le.Uint16(d[4:])), int(d[6]), int(d[7])
		}
	}
	if ibData < 0 {
		return nil
	}
	idx := -1
	t.root.leaves(func(l *bthNode) {
		for i := 0; i+8 <= len(l.recs); i += 8 {
			if le.Uint32(l.recs[i:]) == rowID {
				idx = int(le.Uint32(l.recs[i+4:]))
			}
		}
	})
	if idx < 0 || idx >= t.n {
		return nil
	}
	cebOff := int(le.Uint16(t.info[6:])) + ibit/8 // TCI_1b ends where the existence bitmap starts
	exists := func(row []byte) bool { return row[cebOff]&(0x80>>(ibit%8)) != 0 }
	if t.rowsHnid&0x1F == 0 {
		pg := int(t.rowsHnid >> 16)
		s, _, err := hnRange(t.h.pages[pg], t.rowsHnid)
		if err != nil {
			return err
		}
		row := t.h.pages[pg][s+idx*t.rowSize : s+(idx+1)*t.rowSize]
		if exists(row) && clearBytes(row[ibData:ibData+cb]) {
			f.saveHeap(t.h)
		}
		return nil
	}
	m, err := f.loadMatrix(e, t)
	if err != nil {
		return err
	}
	per := blockMax / t.rowSize
	d, err := f.readBlock(m.kids[idx/per])
	if err != nil {
		return err
	}
	row := d[(idx%per)*t.rowSize : (idx%per+1)*t.rowSize]
	if exists(row) && clearBytes(row[ibData:ibData+cb]) {
		f.stage(m.kids[idx/per], d)
	}
	return nil
}

// clearBytes zeroes b and reports whether it was not zero already.
func clearBytes(b []byte) bool {
	was := false
	for i, c := range b {
		was = was || c != 0
		b[i] = 0
	}
	return was
}
