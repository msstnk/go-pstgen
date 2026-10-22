package pst

import (
	"sort"
	"unicode/utf16"
)

// MAPI property types
const (
	PtShort   = 0x0002
	PtLong    = 0x0003
	PtFloat   = 0x0004
	PtDouble  = 0x0005
	PtBool    = 0x000B
	PtI8      = 0x0014
	PtUnicode = 0x001F
	PtTime    = 0x0040
	PtBinary  = 0x0102
)

const maxHeapItem = 3580

// Prop is one property of a PC. Data is the raw value bytes (fixed-length values are LE, strings are UTF-16LE).
type Prop struct {
	ID, Type uint16
	Data     []byte
}

func PInt32(id uint16, v int32) Prop {
	b := make([]byte, 4)
	le.PutUint32(b, uint32(v))
	return Prop{id, PtLong, b}
}

func PBool(id uint16, v bool) Prop {
	b := []byte{0}
	if v {
		b[0] = 1
	}
	return Prop{id, PtBool, b}
}

func PTime(id uint16, ft int64) Prop {
	b := make([]byte, 8)
	le.PutUint64(b, uint64(ft))
	return Prop{id, PtTime, b}
}

func PStr(id uint16, s string) Prop { return Prop{id, PtUnicode, UTF16LE(s)} }

func PBin(id uint16, b []byte) Prop { return Prop{id, PtBinary, b} }

// UTF16LE converts a string to UTF-16LE (with a fast path for ASCII).
func UTF16LE(s string) []byte {
	if s == "" {
		return nil
	}
	b := make([]byte, 2*len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return utf16Slow(s)
		}
		b[2*i] = s[i]
	}
	return b
}

func utf16Slow(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		le.PutUint16(b[2*i:], c)
	}
	return b
}

// ---- subnode context ----

// subCtx collects the subnodes of one node.
type subCtx struct {
	w    *Writer
	subs []Sub
	ltp  uint32
}

func newSubCtx(w *Writer) *subCtx { return &subCtx{w: w, ltp: 0x401} }

func (c *subCtx) nextLTP() uint32 {
	n := c.ltp<<5 | 0x1F
	c.ltp++
	return n
}

func (c *subCtx) putChunks(chunks [][]byte) uint32 {
	nid := c.nextLTP()
	c.subs = append(c.subs, Sub{NID: nid, Data: c.w.putChunks(chunks)})
	return nid
}

func (c *subCtx) putData(b []byte) uint32 { return c.putChunks(split(b)) }

// addNode registers a child node (table/attachment) that can have its own subnodes.
func (c *subCtx) addNode(nid uint32, chunks [][]byte, inner *subCtx) {
	s := Sub{NID: nid, Data: c.w.putChunks(chunks)}
	if inner != nil {
		s.Sub = c.w.putSubs(inner.subs)
	}
	c.subs = append(c.subs, s)
}

// ---- Heap-on-Node ----

type hblock struct {
	buf  []byte
	offs []uint16
}

type heap struct {
	sig    byte
	blocks []*hblock
}

func newHeap(sig byte) *heap {
	h := &heap{sig: sig}
	h.newBlock()
	return h
}

func (h *heap) newBlock() *hblock {
	i := len(h.blocks)
	hdr := 2
	switch {
	case i == 0:
		hdr = 12
	case i%128 == 8:
		hdr = 66
	}
	b := &hblock{buf: make([]byte, hdr, blockMax)}
	h.blocks = append(h.blocks, b)
	return b
}

// allocBuf allocates an n-byte heap item and returns its HID and a slice to write into.
func (h *heap) allocBuf(n int) (uint32, []byte) {
	if n > maxHeapItem {
		panic("heap item too large")
	}
	sz := n // value length is determined by the heap item length, so do not pad
	b := h.blocks[len(h.blocks)-1]
	if len(b.buf)+sz+1+4+2*(len(b.offs)+2) > blockMax {
		b = h.newBlock()
	}
	start := len(b.buf)
	b.buf = b.buf[:start+sz]
	b.offs = append(b.offs, uint16(start))
	hid := uint32(len(h.blocks)-1)<<16 | uint32(len(b.offs))<<5
	return hid, b.buf[start : start+n]
}

func fillLevel(free int) byte {
	switch {
	case free >= 3584:
		return 0
	case free < 8:
		return 15
	}
	l := 14 - free*14/3584
	return byte(max(1, min(14, l)))
}

// finish finalizes the HNPAGEMAP and header and returns the NDB block sequence.
func (h *heap) finish(userRoot uint32) [][]byte {
	levels := make([]byte, len(h.blocks))
	out := make([][]byte, len(h.blocks))
	for i, b := range h.blocks {
		end := len(b.buf)
		n := len(b.offs)
		pm := (len(b.buf) + 1) &^ 1
		b.buf = b.buf[:pm+4+2*(n+1)]
		le.PutUint16(b.buf[pm:], uint16(n))
		for k, o := range b.offs {
			le.PutUint16(b.buf[pm+4+2*k:], o)
		}
		le.PutUint16(b.buf[pm+4+2*n:], uint16(end))
		le.PutUint16(b.buf, uint16(pm))
		if i < len(h.blocks)-1 {
			levels[i] = 15 // every block but the last is zero-padded to blockMax by putChunks
		} else {
			levels[i] = fillLevel(blockMax - len(b.buf))
		}
		out[i] = b.buf
	}
	b0 := h.blocks[0].buf
	b0[2], b0[3] = 0xEC, h.sig
	le.PutUint32(b0[4:], userRoot)
	for i := 0; i < len(levels) && i < 8; i++ {
		b0[8+i/2] |= levels[i] << (4 * (i % 2))
	}
	for bi := 8; bi < len(h.blocks); bi += 128 {
		bb := h.blocks[bi].buf
		for k := 0; k < 128 && bi+k < len(levels); k++ {
			bb[2+k/2] |= levels[bi+k] << (4 * (k % 2))
		}
	}
	return out
}

// bth builds a BTH from recs (concatenated fixed-length records in ascending key order) and fills hdr (BTHHEADER).
func (h *heap) bth(hdr []byte, cbKey, cbEnt int, recs []byte) {
	rs := cbKey + cbEnt
	n := len(recs) / rs
	hdr[0], hdr[1], hdr[2], hdr[3] = 0xB5, byte(cbKey), byte(cbEnt), 0
	le.PutUint32(hdr[4:], 0)
	if n == 0 {
		return
	}
	type node struct {
		hid uint32
		key []byte
	}
	var cur []node
	per := maxHeapItem / rs
	for i := 0; i < n; i += per {
		j := min(n, i+per)
		hid, b := h.allocBuf((j - i) * rs)
		copy(b, recs[i*rs:j*rs])
		cur = append(cur, node{hid, recs[i*rs : i*rs+cbKey]})
	}
	ir := cbKey + 4
	iper := maxHeapItem / ir
	for len(cur) > 1 {
		hdr[3]++
		var next []node
		for i := 0; i < len(cur); i += iper {
			j := min(len(cur), i+iper)
			hid, b := h.allocBuf((j - i) * ir)
			for k, c := range cur[i:j] {
				copy(b[k*ir:], c.key)
				le.PutUint32(b[k*ir+cbKey:], c.hid)
			}
			next = append(next, node{hid, cur[i].key})
		}
		cur = next
	}
	le.PutUint32(hdr[4:], cur[0].hid)
}

// ---- Property Context ----

func inlineType(t uint16) bool {
	switch t {
	case PtShort, PtLong, PtFloat, PtBool, 0x000A, 0x0001:
		return true
	}
	return false
}

// buildPC returns the NDB block sequence of a Property Context. Large values are moved out to subnodes of sc.
func buildPC(props []Prop, sc *subCtx) [][]byte {
	sort.Slice(props, func(i, j int) bool { return props[i].ID < props[j].ID })
	h := newHeap(0xBC)
	hdrHid, hdr := h.allocBuf(8)
	recs := make([]byte, 8*len(props))
	for i, p := range props {
		r := recs[8*i:]
		le.PutUint16(r, p.ID)
		le.PutUint16(r[2:], p.Type)
		var v uint32
		switch {
		case inlineType(p.Type):
			var t [4]byte
			copy(t[:], p.Data)
			v = le.Uint32(t[:])
		case len(p.Data) == 0:
		case len(p.Data) <= maxHeapItem:
			hid, b := h.allocBuf(len(p.Data))
			copy(b, p.Data)
			v = hid
		default:
			v = sc.putData(p.Data)
		}
		le.PutUint32(r[4:], v)
	}
	h.bth(hdr, 2, 6, recs)
	return h.finish(hdrHid)
}

// ---- Table Context ----

// ColDef is a table column definition (the RowID/RowVer columns are added automatically).
type ColDef struct{ ID, Type uint16 }

// Cell is one cell value in a row (omit it for columns without a value).
type Cell struct {
	ID uint16
	V  []byte
}

type Row struct {
	ID    uint32
	Cells []Cell
}

func cellSize(t uint16) int {
	switch t {
	case PtI8, PtTime, PtDouble, 0x0006:
		return 8
	case PtShort:
		return 2
	case PtBool:
		return 1
	}
	return 4
}

func isVar(t uint16) bool {
	switch t {
	case PtLong, PtFloat, 0x000A, PtI8, PtTime, PtDouble, 0x0006, PtShort, PtBool:
		return false
	}
	return true
}

const (
	tagRowID  = 0x67F2
	tagRowVer = 0x67F3
)

// buildTC builds a Table Context. cols is in iBit order (iBit 0/1 are fixed as RowID/RowVer).
// TCOLDESC entries are sorted by tag ascending; in-row offsets are assigned in iBit order: 4/8-byte groups, then 2, then 1-byte groups, then CEB.
func buildTC(cols []ColDef, rows []Row, sc *subCtx) [][]byte {
	type col struct {
		ColDef
		src  int // position in cols (-1: RowID, -2: RowVer)
		ib   int
		size int
		bit  int
	}
	byBit := make([]col, 0, len(cols)+2)
	byBit = append(byBit,
		col{ColDef: ColDef{tagRowID, PtLong}, src: -1, size: 4, bit: 0},
		col{ColDef: ColDef{tagRowVer, PtLong}, src: -2, size: 4, bit: 1})
	for i, c := range cols {
		byBit = append(byBit, col{ColDef: c, src: i, size: cellSize(c.Type), bit: i + 2})
	}
	off := 8
	var rgib [4]int
	for pass := 0; pass < 3; pass++ {
		for i := range byBit {
			c := &byBit[i]
			if c.src < 0 {
				c.ib = 4 * (-c.src - 1)
				continue
			}
			if (pass == 0 && c.size >= 4) || (pass == 1 && c.size == 2) || (pass == 2 && c.size == 1) {
				c.ib = off
				off += c.size
			}
		}
		rgib[pass] = off
	}
	nc := len(byBit)
	rowSize := off + (nc+7)/8
	rgib[3] = rowSize

	all := append([]col(nil), byBit...)
	sort.Slice(all, func(i, j int) bool {
		return uint32(all[i].ID)<<16|uint32(all[i].Type) < uint32(all[j].ID)<<16|uint32(all[j].Type)
	})

	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	h := newHeap(0x7C)
	infoHid, info := h.allocBuf(22 + 8*nc)
	idxHid, idxHdr := h.allocBuf(8)

	rowBuf := make([]byte, len(rows)*rowSize)
	for ri, r := range rows {
		rb := rowBuf[ri*rowSize : (ri+1)*rowSize]
		ceb := rb[off:]
		le.PutUint32(rb[0:], r.ID)
		le.PutUint32(rb[4:], uint32(ri+1))
		ceb[0] |= 0xC0
		for i := 2; i < nc; i++ {
			c := &byBit[i]
			var cell []byte
			for _, x := range r.Cells {
				if x.ID == c.ID {
					cell = x.V
					break
				}
			}
			if cell == nil {
				continue
			}
			ceb[c.bit/8] |= 0x80 >> (c.bit % 8)
			if !isVar(c.Type) {
				copy(rb[c.ib:c.ib+c.size], cell)
				continue
			}
			var v uint32
			switch {
			case len(cell) == 0:
			case len(cell) <= maxHeapItem:
				hid, b := h.allocBuf(len(cell))
				copy(b, cell)
				v = hid
			default:
				v = sc.putData(cell)
			}
			le.PutUint32(rb[c.ib:], v)
		}
	}

	idx := make([]byte, 8*len(rows))
	for i, r := range rows {
		le.PutUint32(idx[8*i:], r.ID)
		le.PutUint32(idx[8*i+4:], uint32(i))
	}
	h.bth(idxHdr, 4, 4, idx)

	var rowsHnid uint32
	switch {
	case len(rowBuf) == 0:
	case len(rowBuf) <= maxHeapItem:
		hid, b := h.allocBuf(len(rowBuf))
		copy(b, rowBuf)
		rowsHnid = hid
	default:
		per := blockMax / rowSize * rowSize
		var chunks [][]byte
		for p := 0; p < len(rowBuf); p += per {
			chunks = append(chunks, rowBuf[p:min(len(rowBuf), p+per)])
		}
		rowsHnid = sc.putChunks(chunks)
	}

	info[0], info[1] = 0x7C, byte(nc)
	for i, v := range rgib {
		le.PutUint16(info[2+2*i:], uint16(v))
	}
	le.PutUint32(info[10:], idxHid)
	le.PutUint32(info[14:], rowsHnid)
	le.PutUint32(info[18:], 0)
	for i := range all {
		c := &all[i]
		d := info[22+8*i:]
		le.PutUint32(d, uint32(c.ID)<<16|uint32(c.Type))
		le.PutUint16(d[4:], uint16(c.ib))
		d[6] = byte(c.size)
		d[7] = byte(c.bit)
	}
	return h.finish(infoHid)
}
