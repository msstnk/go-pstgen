package pst

import (
	"sort"
	"unicode/utf16"
)

// MAPI property types ([MS-OXCDATA] 2.11.1)
const (
	PtNull     = 0x0001 // PtypNull
	PtShort    = 0x0002
	PtLong     = 0x0003
	PtFloat    = 0x0004
	PtDouble   = 0x0005
	PtCurrency = 0x0006 // PtypCurrency
	PtError    = 0x000A // PtypErrorCode
	PtBool     = 0x000B
	PtI8       = 0x0014
	PtUnicode  = 0x001F
	PtTime     = 0x0040
	PtBinary   = 0x0102
)

// Heap-on-Node (HN, [MS-PST] 2.3.1). An HN is a sequence of data blocks, each holding heap items followed by
// an HNPAGEMAP; the first block starts with an HNHDR, others with an HNPAGEHDR or HNBITMAPHDR.
const (
	// maxHeapItem is the largest heap allocation (a value bigger than this is stored in a subnode,
	// [MS-PST] 2.3.3.3: "HID (<= 3580 bytes)" vs "NID (subnode, > 3580 bytes)").
	maxHeapItem = 3580

	hnSig = 0xEC // HNHDR.bSig

	// HNHDR { ibHnpm u16, bSig u8, bClientSig u8, hidUserRoot u32, rgbFillLevel [4]byte } = 12 bytes ([MS-PST] 2.3.1.2).
	hnHdrSize         = 12
	hnPageHdrSize     = 2   // HNPAGEHDR { ibHnpm u16 } ([MS-PST] 2.3.1.3)
	hnBitmapHdrSize   = 66  // HNBITMAPHDR { ibHnpm u16, rgbFillLevel [64]byte } ([MS-PST] 2.3.1.4)
	hnFillLevelOff    = 8   // HNHDR.rgbFillLevel: eight 4-bit fill levels for data blocks 0..7
	hnFillLevelBlocks = 8   // blocks covered by HNHDR.rgbFillLevel
	hnBitmapOff       = 2   // HNBITMAPHDR.rgbFillLevel: 128 4-bit fill levels
	hnBitmapEvery     = 128 // an HNBITMAPHDR sits in block 8 and then every 128 blocks (8, 136, 264, ...)
	hnBitmapFirst     = 8

	// bClientSig ([MS-PST] 2.3.1.2)
	clientSigTC  = 0x7C // Table Context
	clientSigBTH = 0xB5 // BTree-on-Heap; also BTHHEADER.bType ([MS-PST] 2.3.2.1)
	clientSigPC  = 0xBC // Property Context

	// Fill level ([MS-PST] 2.3.1.2): FILL_LEVEL_EMPTY (>= 3584 bytes free) .. FILL_LEVEL_FULL (< 8 bytes free).
	fillEmptyFree = 3584
	fillFullFree  = 8
	fillLevelFull = 0xF
)

// Prop is one property of a PC ([MS-PST] 2.3.3 Property Context). Data is the raw value bytes (fixed-length values are LE, strings are UTF-16LE).
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

// subCtx collects the subnodes of one node ([MS-PST] 2.2.2.8.3.3 Subnode BTree). A PC value or a TC row matrix that does not
// fit a heap item is stored here and referenced by its local NID ([MS-PST] 2.3.3.2 HNID).
type subCtx struct {
	w    *Writer
	subs []Sub
	ltp  uint32
}

func newSubCtx(w *Writer) *subCtx { return &subCtx{w: w, ltp: firstSubIdx} }

func (c *subCtx) nextLTP() uint32 {
	n := c.ltp<<nidTypeBits | nidLTP
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

// heap builds a Heap-on-Node ([MS-PST] 2.3.1): a series of heap items, each data block ending with an HNPAGEMAP.
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
	hdr := hnPageHdrSize
	switch {
	case i == 0:
		hdr = hnHdrSize
	case i%hnBitmapEvery == hnBitmapFirst:
		hdr = hnBitmapHdrSize
	}
	b := &hblock{buf: make([]byte, hdr, blockMax)}
	h.blocks = append(h.blocks, b)
	return b
}

// allocBuf allocates an n-byte heap item and returns its HID ([MS-PST] 2.3.1.1) and a slice to write into.
// A new block is started when the HNPAGEMAP ([MS-PST] 2.3.1.5) would no longer fit after the item: a pad byte (2-byte alignment),
// cAlloc and cFree (4 bytes) and cAlloc+1 offsets of 2 bytes each.
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
	hid := uint32(len(h.blocks)-1)<<16 | uint32(len(b.offs))<<hidIdxShift
	return hid, b.buf[start : start+n]
}

// fillLevelMin[l] is the least free space (bytes) of FILL_LEVEL l ([MS-PST] 2.3.1.2); less than fillFullFree is FILL_LEVEL_FULL.
var fillLevelMin = [...]int{fillEmptyFree, 2560, 2048, 1792, 1536, 1280, 1024, 768, 512, 256, 128, 64, 32, 16, fillFullFree}

func fillLevel(free int) byte {
	for l, m := range fillLevelMin {
		if free >= m {
			return byte(l)
		}
	}
	return fillLevelFull
}

// finish finalizes the HNPAGEMAP ([MS-PST] 2.3.1.5) and header and returns the NDB block sequence.
// userRoot is HNHDR.hidUserRoot: the BTHHEADER of a PC or the TCINFO of a TC.
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
			levels[i] = fillLevelFull // every block but the last is zero-padded to blockMax by putChunks
		} else {
			levels[i] = fillLevel(blockMax - len(b.buf))
		}
		out[i] = b.buf
	}
	b0 := h.blocks[0].buf
	b0[2], b0[3] = hnSig, h.sig
	le.PutUint32(b0[4:], userRoot)
	// Each fill level is a 4-bit value; the even block of a pair takes the low nibble.
	for i := 0; i < len(levels) && i < hnFillLevelBlocks; i++ {
		b0[hnFillLevelOff+i/2] |= levels[i] << (4 * (i % 2))
	}
	for bi := hnBitmapFirst; bi < len(h.blocks); bi += hnBitmapEvery {
		bb := h.blocks[bi].buf
		for k := 0; k < hnBitmapEvery && bi+k < len(levels); k++ {
			bb[hnBitmapOff+k/2] |= levels[bi+k] << (4 * (k % 2))
		}
	}
	return out
}

// BTHHEADER { bType u8, cbKey u8, cbEnt u8, bIdxLevels u8, hidRoot u32 } = 8 bytes ([MS-PST] 2.3.2.1).
// Intermediate records are (key, HID of the child) pairs ([MS-PST] 2.3.2.2).
const (
	bthHdrSize   = 8
	bthIdxLevels = 3 // BTHHEADER.bIdxLevels offset
	bthRootOff   = 4 // BTHHEADER.hidRoot offset
	bthHIDSize   = 4 // size of the child HID in an intermediate record
)

// bth builds a BTree-on-Heap ([MS-PST] 2.3.2) from recs (concatenated fixed-length records in ascending key order) and fills hdr (BTHHEADER).
func (h *heap) bth(hdr []byte, cbKey, cbEnt int, recs []byte) {
	rs := cbKey + cbEnt
	n := len(recs) / rs
	hdr[0], hdr[1], hdr[2], hdr[bthIdxLevels] = clientSigBTH, byte(cbKey), byte(cbEnt), 0
	le.PutUint32(hdr[bthRootOff:], 0)
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
	ir := cbKey + bthHIDSize
	iper := maxHeapItem / ir
	for len(cur) > 1 {
		hdr[bthIdxLevels]++
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
	le.PutUint32(hdr[bthRootOff:], cur[0].hid)
}

// ---- Property Context ----

// inlineType reports whether a PC value of type t always fits the 4-byte dwValueHnid ([MS-PST] 2.3.3.3).
func inlineType(t uint16) bool {
	switch t {
	case PtShort, PtLong, PtFloat, PtBool, PtError, PtNull:
		return true
	}
	return false
}

// PC BTH record { wPropId u16, wPropType u16, dwValueHnid u32 }: cbKey 2, cbEnt 6 ([MS-PST] 2.3.3.3).
// A value of at most 4 bytes is stored in dwValueHnid itself; a larger one is an HID (<= maxHeapItem) or a subnode NID.
const (
	pcKeySize = 2
	pcEntSize = 6
	pcRecSize = pcKeySize + pcEntSize
)

// buildPC returns the NDB block sequence of a Property Context ([MS-PST] 2.3.3). Large values are moved out to subnodes of sc.
func buildPC(props []Prop, sc *subCtx) [][]byte {
	sort.Slice(props, func(i, j int) bool { return props[i].ID < props[j].ID })
	h := newHeap(clientSigPC)
	hdrHid, hdr := h.allocBuf(bthHdrSize)
	recs := make([]byte, pcRecSize*len(props))
	for i, p := range props {
		r := recs[pcRecSize*i:]
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
	h.bth(hdr, pcKeySize, pcEntSize, recs)
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

// cellSize is the size of a column's slot in a TC row: the value itself for fixed types up to 8 bytes, otherwise a 4-byte HNID
// ([MS-PST] 2.3.4.4.2 Variable-sized Data).
func cellSize(t uint16) int {
	switch t {
	case PtI8, PtTime, PtDouble, PtCurrency:
		return 8
	case PtShort:
		return 2
	case PtBool:
		return 1
	}
	return 4
}

// isVar reports whether a value of type t is stored out of the row and referenced by an HNID ([MS-PST] 2.3.4.4.2).
func isVar(t uint16) bool {
	switch t {
	case PtLong, PtFloat, PtError, PtI8, PtTime, PtDouble, PtCurrency, PtShort, PtBool:
		return false
	}
	return true
}

const (
	tagRowID  = 0x67F2 // PidTagLtpRowId
	tagRowVer = 0x67F3 // PidTagLtpRowVer
)

// Table Context ([MS-PST] 2.3.4).
//
// TCINFO { bType u8, cCols u8, rgib [4]u16, hidRowIndex u32, hnidRows u32, hidIndex u32, rgTCOLDESC } ([MS-PST] 2.3.4.1)
// followed by cCols TCOLDESC { tag u32, ibData u16, cbData u8, iBit u8 } ([MS-PST] 2.3.4.2).
const (
	tcInfoHdrSize = 22 // TCINFO without rgTCOLDESC
	tcColDescSize = 8
	tcColsOff     = 1  // cCols
	tcRgibOff     = 2  // rgib[TCI_4b, TCI_2b, TCI_1b, TCI_bm]: end offsets of the 4/8-byte, 2-byte, 1-byte groups and of the CEB
	tcRowIndexOff = 10 // hidRowIndex: HID of the RowIndex BTH
	tcRowsOff     = 14 // hnidRows: HNID of the Row Matrix (0 if no rows)
	tcHidIndexOff = 18 // hidIndex: deprecated, must be 0
	tciGroups     = 3  // TCI_4b, TCI_2b, TCI_1b (TCI_bm is the row size)
	tciBM         = 3  // index of TCI_bm in rgib

	// Row Data Format ([MS-PST] 2.3.4.4.1): dwRowID (PidTagLtpRowId, ibData 0, iBit 0) and dwRowVer
	// (PidTagLtpRowVer, ibData 4, iBit 1) always come first, so other columns start at the 8th byte and iBit 2.
	rowIDOff     = 0
	rowVerOff    = 4
	rowFixedSize = 8
	firstUserBit = 2

	// RowIndex BTH ([MS-PST] 2.3.4.3): (dwRowID, dwRowIndex), both 4 bytes.
	rowIdxKeySize = 4
	rowIdxEntSize = 4
	rowIdxRecSize = rowIdxKeySize + rowIdxEntSize

	cebFixedBits = 0xC0 // CEB bits 0 and 1 (RowID, RowVer) are always set; bit n is bit 7-n%8 of CEB byte n/8
)

// buildTC builds a Table Context ([MS-PST] 2.3.4). cols is in iBit order (iBit 0/1 are fixed as RowID/RowVer).
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
		byBit = append(byBit, col{ColDef: c, src: i, size: cellSize(c.Type), bit: i + firstUserBit})
	}
	off := rowFixedSize
	var rgib [4]int
	for pass := 0; pass < tciGroups; pass++ {
		for i := range byBit {
			c := &byBit[i]
			if c.src < 0 {
				c.ib = rowVerOff * (-c.src - 1) // RowID at 0, RowVer at 4
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
	rgib[tciBM] = rowSize

	all := append([]col(nil), byBit...)
	sort.Slice(all, func(i, j int) bool {
		return uint32(all[i].ID)<<16|uint32(all[i].Type) < uint32(all[j].ID)<<16|uint32(all[j].Type)
	})

	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	h := newHeap(clientSigTC)
	infoHid, info := h.allocBuf(tcInfoHdrSize + tcColDescSize*nc)
	idxHid, idxHdr := h.allocBuf(bthHdrSize)

	rowBuf := make([]byte, len(rows)*rowSize)
	for ri, r := range rows {
		rb := rowBuf[ri*rowSize : (ri+1)*rowSize]
		ceb := rb[off:]
		le.PutUint32(rb[rowIDOff:], r.ID)
		le.PutUint32(rb[rowVerOff:], uint32(ri+1))
		ceb[0] |= cebFixedBits
		for i := firstUserBit; i < nc; i++ {
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

	idx := make([]byte, rowIdxRecSize*len(rows))
	for i, r := range rows {
		le.PutUint32(idx[rowIdxRecSize*i:], r.ID)
		le.PutUint32(idx[rowIdxRecSize*i+rowIdxKeySize:], uint32(i)) // dwRowIndex
	}
	h.bth(idxHdr, rowIdxKeySize, rowIdxEntSize, idx)

	var rowsHnid uint32
	switch {
	case len(rowBuf) == 0:
	case len(rowBuf) <= maxHeapItem:
		hid, b := h.allocBuf(len(rowBuf))
		copy(b, rowBuf)
		rowsHnid = hid
	default:
		per := blockMax / rowSize * rowSize // each data block holds whole rows only ([MS-PST] 2.3.4.4 Row Matrix)
		var chunks [][]byte
		for p := 0; p < len(rowBuf); p += per {
			chunks = append(chunks, rowBuf[p:min(len(rowBuf), p+per)])
		}
		rowsHnid = sc.putChunks(chunks)
	}

	info[0], info[tcColsOff] = clientSigTC, byte(nc)
	for i, v := range rgib {
		le.PutUint16(info[tcRgibOff+2*i:], uint16(v))
	}
	le.PutUint32(info[tcRowIndexOff:], idxHid)
	le.PutUint32(info[tcRowsOff:], rowsHnid)
	le.PutUint32(info[tcHidIndexOff:], 0)
	for i := range all {
		c := &all[i]
		d := info[tcInfoHdrSize+tcColDescSize*i:]
		le.PutUint32(d, uint32(c.ID)<<16|uint32(c.Type))
		le.PutUint16(d[4:], uint16(c.ib))
		d[6] = byte(c.size)
		d[7] = byte(c.bit)
	}
	return h.finish(infoHid)
}
