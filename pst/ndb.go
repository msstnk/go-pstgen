// Package pst writes Unicode-format (wVer 23) MS-PST files in append-only fashion.
package pst

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"sort"
)

const (
	blockMax = 8176 // 8192 - BLOCKTRAILER(16)
	pageSize = 512
	firstMap = 0x4400  // first AMap
	mapSpan  = 0x3E000 // bytes covered by one AMap (253,952)
	xbMax    = (blockMax - 8) / 8

	// FMap / FPMap are obsolete but must exist at fixed positions ([MS-PST] 2.2.2.7.4, 2.2.2.7.6).
	// The header's rgbFM / rgbFP cover the FMaps for the first 128 AMaps and the FPMaps for 1024 PMaps.
	fmapFirst  = 128         // AMap index that carries the first FMap
	fmapEvery  = 496         // number of AMaps covered by one FMap
	fpmapFirst = 128 * 8 * 8 // AMap index that carries the first FPMap
	fpmapEvery = 496 * 8 * 8 // number of AMaps covered by one FPMap
)

var le = binary.LittleEndian

// pstCRC is the MS-PST CRC-32 with initial value 0 and no final inversion.
func pstCRC(b []byte) uint32 { return ^crc32.Update(0xFFFFFFFF, crc32.IEEETable, b) }

func sig(ib, bid uint64) uint16 {
	v := uint32(ib ^ bid)
	return uint16(v>>16) ^ uint16(v)
}

type bbtEnt struct {
	bid, ib uint64
	cb      uint16
}

type nbtEnt struct {
	nid, parent uint32
	data, sub   uint64
}

// Sub is one entry of the subnode B-Tree.
type Sub struct {
	NID       uint32
	Data, Sub uint64
}

// Writer is the NDB layer. Blocks are always appended to the end of the file, and B-Trees are built bottom-up in Finish.
type Writer struct {
	f              *os.File
	bw             *bufio.Writer
	off            uint64
	nextMap        int
	amaps, pmaps   []uint64
	fmaps, fpmaps  []uint64
	blkIdx, pgNext uint64
	bbt            []bbtEnt
	nbt            []nbtEnt
	nextIdx        [32]uint32
	scratch        []byte
	crypt          CryptMethod
}

var zeroBuf [pageSize * 2]byte

func NewWriter(path string, bufSize int, crypt CryptMethod) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	w := &Writer{f: f, bw: bufio.NewWriterSize(f, bufSize), blkIdx: 1, crypt: crypt}
	for i := range w.nextIdx {
		w.nextIdx[i] = 0x401
	}
	w.zeros(firstMap) // header area (written back in Finish)
	return w, nil
}

// NewNID hands out an unused NID of the given type.
func (w *Writer) NewNID(t uint32) uint32 {
	i := w.nextIdx[t]
	w.nextIdx[t]++
	return i<<5 | t
}

func (w *Writer) writeRaw(b []byte) {
	w.bw.Write(b)
	w.off += uint64(len(b))
}

func (w *Writer) zeros(n uint64) {
	for n > 0 {
		c := uint64(len(zeroBuf))
		if c > n {
			c = n
		}
		w.writeRaw(zeroBuf[:c])
		n -= c
	}
}

// emitMap reserves an AMap plus the PMap / FMap / FPMap that belong at that position, back to back (contents are written in Finish).
func (w *Writer) emitMap() {
	i := w.nextMap
	w.amaps = append(w.amaps, w.off)
	w.zeros(pageSize)
	if i%8 == 0 {
		w.pmaps = append(w.pmaps, w.off)
		w.zeros(pageSize)
	}
	if i >= fmapFirst && (i-fmapFirst)%fmapEvery == 0 {
		w.fmaps = append(w.fmaps, w.off)
		w.zeros(pageSize)
	}
	if i >= fpmapFirst && (i-fpmapFirst)%fpmapEvery == 0 {
		w.fpmaps = append(w.fpmaps, w.off)
		w.zeros(pageSize)
	}
	w.nextMap++
}

// alloc allocates n bytes at align boundaries, inserting padding if needed so allocations do not straddle AMap/PMap positions.
func (w *Writer) alloc(n, align uint64) uint64 {
	for {
		mapPos := firstMap + uint64(w.nextMap)*mapSpan
		start := (w.off + align - 1) / align * align
		switch {
		case start == mapPos:
			w.zeros(start - w.off)
			w.emitMap()
		case start+n <= mapPos:
			w.zeros(start - w.off)
			return start
		default:
			w.zeros(mapPos - w.off)
			w.emitMap()
		}
	}
}

func (w *Writer) putBlock(data []byte, internal bool) uint64 {
	return w.putBlockN(data, len(data), internal)
}

// putBlockN writes a block with data zero-padded up to cb bytes.
func (w *Writer) putBlockN(data []byte, cb int, internal bool) uint64 {
	if cb > blockMax || len(data) > cb {
		panic("block too large")
	}
	total := uint64((cb + 16 + 63) &^ 63)
	ib := w.alloc(total, 64)
	bid := w.blkIdx << 2
	if internal {
		bid |= 2
	}
	w.blkIdx++
	if uint64(cap(w.scratch)) < total {
		w.scratch = make([]byte, total)
	}
	b := w.scratch[:total]
	n := copy(b, data)
	clear(b[n:])
	if !internal {
		w.crypt.encrypt(b[:cb], bid)
	}
	t := b[total-16:]
	le.PutUint16(t[0:], uint16(cb))
	le.PutUint16(t[2:], sig(ib, bid))
	le.PutUint32(t[4:], pstCRC(b[:cb])) // CRC is computed over the on-disk (post-encryption) bytes
	le.PutUint64(t[8:], bid)
	w.writeRaw(b)
	w.bbt = append(w.bbt, bbtEnt{bid, ib, uint16(cb)})
	return bid
}

func (w *Writer) putX(level byte, ids []uint64, total int) uint64 {
	b := make([]byte, 8+8*len(ids))
	b[0], b[1] = 0x01, level
	le.PutUint16(b[2:], uint16(len(ids)))
	le.PutUint32(b[4:], uint32(total))
	for i, id := range ids {
		le.PutUint64(b[8+8*i:], id)
	}
	return w.putBlock(b, true)
}

// putChunks writes a sequence of data blocks and returns the BID: the block itself for one, or one bundled by XBLOCK/XXBLOCK for several.
// Outlook locates positions in the data tree as "block number x blockMax", so every block but the last is zero-padded to blockMax.
func (w *Writer) putChunks(chunks [][]byte) uint64 {
	switch len(chunks) {
	case 0:
		return 0
	case 1:
		return w.putBlock(chunks[0], false)
	}
	ids := make([]uint64, len(chunks))
	total := 0
	last := len(chunks) - 1
	for i, c := range chunks {
		cb := blockMax
		if i == last {
			cb = len(c)
		}
		ids[i] = w.putBlockN(c, cb, false)
		total += cb
	}
	if len(ids) <= xbMax {
		return w.putX(1, ids, total)
	}
	var xids []uint64
	for i := 0; i < len(ids); i += xbMax {
		j := min(len(ids), i+xbMax)
		sub := (j - i) * blockMax
		if j == len(ids) {
			sub += len(chunks[last]) - blockMax
		}
		xids = append(xids, w.putX(1, ids[i:j], sub))
	}
	if len(xids) > xbMax {
		panic("node data too large")
	}
	return w.putX(2, xids, total)
}

func split(b []byte) [][]byte {
	var out [][]byte
	for len(b) > blockMax {
		out = append(out, b[:blockMax])
		b = b[blockMax:]
	}
	if len(b) > 0 || out == nil {
		out = append(out, b)
	}
	return out
}

// putSubs writes the subnode B-Tree (SLBLOCK / SIBLOCK).
func (w *Writer) putSubs(subs []Sub) uint64 {
	if len(subs) == 0 {
		return 0
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].NID < subs[j].NID })
	const per = (blockMax - 8) / 24
	type ent struct{ nid, bid uint64 }
	var leaves []ent
	for i := 0; i < len(subs); i += per {
		j := min(len(subs), i+per)
		b := make([]byte, 8+24*(j-i))
		b[0], b[1] = 0x02, 0
		le.PutUint16(b[2:], uint16(j-i))
		for k, s := range subs[i:j] {
			o := 8 + 24*k
			le.PutUint64(b[o:], uint64(s.NID))
			le.PutUint64(b[o+8:], s.Data)
			le.PutUint64(b[o+16:], s.Sub)
		}
		leaves = append(leaves, ent{uint64(subs[i].NID), w.putBlock(b, true)})
	}
	if len(leaves) == 1 {
		return leaves[0].bid
	}
	const iper = (blockMax - 8) / 16
	if len(leaves) > iper {
		panic("too many subnodes")
	}
	b := make([]byte, 8+16*len(leaves))
	b[0], b[1] = 0x02, 1
	le.PutUint16(b[2:], uint16(len(leaves)))
	for k, e := range leaves {
		le.PutUint64(b[8+16*k:], e.nid)
		le.PutUint64(b[16+16*k:], e.bid)
	}
	return w.putBlock(b, true)
}

// PutNode writes a node's data and subnodes and registers it in the NBT.
func (w *Writer) PutNode(nid, parent uint32, chunks [][]byte, subs []Sub) {
	data := w.putChunks(chunks)
	sub := w.putSubs(subs)
	w.nbt = append(w.nbt, nbtEnt{nid: nid, parent: parent, data: data, sub: sub})
}

type btNode struct{ key, bid, ib uint64 }

func (w *Writer) putPage(ptype byte, page []byte, cnt, cmax, cbEnt, level int) (bid, ib uint64) {
	ib = w.alloc(pageSize, pageSize)
	bid = w.pgNext
	w.pgNext++
	page[488], page[489], page[490], page[491] = byte(cnt), byte(cmax), byte(cbEnt), byte(level)
	t := page[496:]
	t[0], t[1] = ptype, ptype
	le.PutUint16(t[2:], sig(ib, bid))
	le.PutUint32(t[4:], pstCRC(page[:496]))
	le.PutUint64(t[8:], bid)
	w.writeRaw(page)
	return
}

func (w *Writer) buildBT(ptype byte, n, entSize, maxEnt int, enc func(i int, dst []byte), key func(i int) uint64) btNode {
	var cur []btNode
	for i := 0; i < n || i == 0; i += maxEnt {
		j := min(n, i+maxEnt)
		page := make([]byte, pageSize)
		for k := i; k < j; k++ {
			enc(k, page[(k-i)*entSize:])
		}
		var kk uint64
		if n > 0 {
			kk = key(i)
		}
		bid, ib := w.putPage(ptype, page, j-i, maxEnt, entSize, 0)
		cur = append(cur, btNode{kk, bid, ib})
	}
	for level := 1; len(cur) > 1; level++ {
		var next []btNode
		for i := 0; i < len(cur); i += 20 {
			j := min(len(cur), i+20)
			page := make([]byte, pageSize)
			for k, c := range cur[i:j] {
				le.PutUint64(page[24*k:], c.key)
				le.PutUint64(page[24*k+8:], c.bid)
				le.PutUint64(page[24*k+16:], c.ib)
			}
			bid, ib := w.putPage(ptype, page, j-i, 20, 24, level)
			next = append(next, btNode{cur[i].key, bid, ib})
		}
		cur = next
	}
	return cur[0]
}

// Finish writes the NBT/BBT, AMap/PMap and header, and closes the file.
func (w *Writer) Finish() error {
	// BIDs of BT pages are a separate series from block BIDs (sequential numbers above the blocks, following Outlook-created files)
	w.pgNext = w.blkIdx<<2 + 0x10
	dlistBid := w.pgNext
	w.pgNext++
	sort.Slice(w.nbt, func(i, j int) bool { return w.nbt[i].nid < w.nbt[j].nid })
	nbtRoot := w.buildBT(0x81, len(w.nbt), 32, 15, func(i int, d []byte) {
		e := w.nbt[i]
		le.PutUint64(d[0:], uint64(e.nid))
		le.PutUint64(d[8:], e.data)
		le.PutUint64(d[16:], e.sub)
		le.PutUint32(d[24:], e.parent)
	}, func(i int) uint64 { return uint64(w.nbt[i].nid) })

	sort.Slice(w.bbt, func(i, j int) bool { return w.bbt[i].bid < w.bbt[j].bid })
	bbtRoot := w.buildBT(0x80, len(w.bbt), 24, 20, func(i int, d []byte) {
		e := w.bbt[i]
		le.PutUint64(d[0:], e.bid)
		le.PutUint64(d[8:], e.ib)
		le.PutUint16(d[16:], e.cb)
		le.PutUint16(d[18:], 2) // cRef
	}, func(i int) uint64 { return w.bbt[i].bid })

	eof := w.off
	if err := w.bw.Flush(); err != nil {
		return err
	}

	var amapFree uint64
	for _, pos := range w.amaps {
		used := min(eof-pos, mapSpan)
		units := (used + 63) / 64
		amapFree += mapSpan - used
		page := make([]byte, pageSize)
		for u := uint64(0); u < units; u++ {
			page[u/8] |= 0x80 >> (u % 8)
		}
		page[496], page[497] = 0x84, 0x84
		le.PutUint32(page[500:], pstCRC(page[:496]))
		le.PutUint64(page[504:], pos)
		if _, err := w.f.WriteAt(page, int64(pos)); err != nil {
			return err
		}
	}
	// PMap / FMap / FPMap are obsolete. Fill them with all 0xFF bits, following Outlook-created files.
	for _, m := range []struct {
		ptype byte
		pos   []uint64
	}{{0x83, w.pmaps}, {0x82, w.fmaps}, {0x85, w.fpmaps}} {
		for _, pos := range m.pos {
			page := make([]byte, pageSize)
			for i := 0; i < 496; i++ {
				page[i] = 0xFF
			}
			page[496], page[497] = m.ptype, m.ptype
			le.PutUint32(page[500:], pstCRC(page[:496]))
			le.PutUint64(page[504:], pos)
			if _, err := w.f.WriteAt(page, int64(pos)); err != nil {
				return err
			}
		}
	}

	// DList page (0x4200): empty body, trailer only
	dl := make([]byte, pageSize)
	dl[496], dl[497] = 0x86, 0x86
	le.PutUint16(dl[498:], sig(0x4200, dlistBid))
	le.PutUint64(dl[504:], dlistBid)
	if _, err := w.f.WriteAt(dl, 0x4200); err != nil {
		return err
	}

	h := make([]byte, 564)
	copy(h[0:], "!BDN")
	le.PutUint16(h[8:], 0x4D53)
	le.PutUint16(h[10:], 23)
	le.PutUint16(h[12:], 19)
	h[14], h[15] = 1, 1
	le.PutUint64(h[32:], w.pgNext)
	le.PutUint32(h[40:], 1)
	for t := uint32(0); t < 32; t++ {
		le.PutUint32(h[44+4*t:], w.nextIdx[t])
	}
	le.PutUint64(h[184:], eof)
	le.PutUint64(h[192:], w.amaps[len(w.amaps)-1])
	le.PutUint64(h[200:], amapFree)
	le.PutUint64(h[208:], 0)
	le.PutUint64(h[216:], nbtRoot.bid)
	le.PutUint64(h[224:], nbtRoot.ib)
	le.PutUint64(h[232:], bbtRoot.bid)
	le.PutUint64(h[240:], bbtRoot.ib)
	h[248] = 2 // VALID_AMAP2
	for i := 256; i < 512; i++ {
		h[i] = 0xFF // rgbFM / rgbFP (obsolete area)
	}
	h[512] = 0x80 // bSentinel
	h[513] = byte(w.crypt)
	le.PutUint64(h[516:], w.blkIdx<<2)
	le.PutUint32(h[524:], pstCRC(h[8:524]))
	le.PutUint32(h[4:], pstCRC(h[8:479]))
	if _, err := w.f.WriteAt(h, 0); err != nil {
		return err
	}
	if err := w.f.Truncate(int64(eof)); err != nil {
		return err
	}
	return w.f.Close()
}

// Abort closes the file on error.
func (w *Writer) Abort() { w.f.Close() }

func (w *Writer) String() string { return fmt.Sprintf("pst.Writer{off=%d}", w.off) }
