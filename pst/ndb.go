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

// Constants below are for the Unicode format. "[MS-PST] x.y.z" refers to section numbers of the
// Outlook Personal Folders (.pst) File Format specification (NDB layer: 2.2, LTP layer: 2.3).
const (
	// Block ([MS-PST] 2.2.2.8): data, then padding up to a 64-byte boundary that ends with a BLOCKTRAILER.
	// A block holds at most blockMax bytes of data (8 KiB minus the trailer).
	blockTrailerSize = 16 // BLOCKTRAILER ([MS-PST] 2.2.2.8.1)
	blockAlign       = 64
	blockMax         = 8192 - blockTrailerSize

	// BLOCKTRAILER { cb u16, wSig u16, dwCRC u32, bid u64 } and PAGETRAILER { ptype u8, ptypeRepeat u8,
	// wSig u16, dwCRC u32, bid u64 } ([MS-PST] 2.2.2.7.1) share these offsets from the start of the trailer.
	trailerSigOff = 2
	trailerCRCOff = 4
	trailerBIDOff = 8

	// BID ([MS-PST] 2.2.2.2): bit 1 (i) marks an internal block (XBLOCK, XXBLOCK, SLBLOCK, SIBLOCK);
	// bits 0 are reserved, bidIndex starts at bit 2, so successive block BIDs differ by 4.
	bidInternal   = 0x2
	bidIndexShift = 2
	bidSize       = 8

	// Block BTree internal blocks ([MS-PST] 2.2.2.8.3.2 / 2.2.2.8.3.3) start with an 8-byte header:
	// btype u8, cLevel u8, cEnt u16, then lcbTotal u32 (XBLOCK / XXBLOCK) or dwPadding u32 (SLBLOCK / SIBLOCK).
	internalHdrSize = 8
	btypeXBlock     = 0x01 // XBLOCK / XXBLOCK ([MS-PST] 2.2.2.8.3.2.1, 2.2.2.8.3.2.2)
	btypeSLBlock    = 0x02 // SLBLOCK / SIBLOCK ([MS-PST] 2.2.2.8.3.3.1.2, 2.2.2.8.3.3.2.2)
	levelXBlock     = 1    // cLevel of an XBLOCK (references data blocks)
	levelXXBlock    = 2    // cLevel of an XXBLOCK (references XBLOCKs)
	levelSLBlock    = 0    // cLevel of an SLBLOCK (leaf: SLENTRYs)
	levelSIBlock    = 1    // cLevel of an SIBLOCK (intermediate: SIENTRYs)
	xbMax           = (blockMax - internalHdrSize) / bidSize

	// PAGETRAILER / BTPAGE pages ([MS-PST] 2.2.2.7).
	pageSize = 512
	firstMap = 0x4400 // first AMap

	// AMap ([MS-PST] 2.2.2.7.2): each of its 496 bytes of bits covers amapUnit bytes of the file per bit.
	amapUnit = 64
	mapSpan  = (pageSize - 16) * 8 * amapUnit // bytes covered by one AMap: 0x3E000 (253,952)

	// Page layout: [entries (btBodySize)] [cEnt cEntMax cbEnt cLevel] [padding] [PAGETRAILER at pageTrailerOff]
	pageTrailerOff = pageSize - 16 // 496; BTPAGE.rgentries is 488 bytes, then cEnt/cEntMax/cbEnt/cLevel and a 4-byte dwPadding ([MS-PST] 2.2.2.7.7.1)
	btBodySize     = 488           // bytes available for BT entries
	btCntOff       = 488           // cEnt, cEntMax, cbEnt, cLevel follow the entries
	btCmaxOff      = 489
	btEntSizeOff   = 490
	btLevelOff     = 491
	btIntEntSize   = 24                        // BTENTRY (key, BREF)
	btIntMaxEnt    = btBodySize / btIntEntSize // fits 20
	bbtMaxEnt      = btBodySize / bbtEntSize   // 20
	nbtMaxEnt      = btBodySize / nbtEntSize   // 15
	slEntSize      = 24                        // SLENTRY (nid, bidData, bidSub)
	siEntSize      = 16                        // SIENTRY (nid, bid)

	// PAGETRAILER.ptype ([MS-PST] 2.2.2.7.1)
	ptypeBBT   = 0x80
	ptypeNBT   = 0x81
	ptypeFMap  = 0x82
	ptypePMap  = 0x83
	ptypeAMap  = 0x84
	ptypeFPMap = 0x85
	ptypeDList = 0x86

	// DList page ([MS-PST] 2.2.2.7.4 Density List): at most one, at absolute offset 0x4200 (one page before the first AMap).
	dlistPos = firstMap - pageSize

	// An AMap page's bid equals its file offset; BT page BIDs are a separate counter (HEADER.bidNextP, [MS-PST] 2.2.2.6)
	// that is not tied to the block BIDs. pageBIDGap is where this writer starts it above the block BIDs.
	pageBIDGap = 0x10

	// NBTENTRY / BBTENTRY fields ([MS-PST] 2.2.2.7.7.3, 2.2.2.7.7.4). A block's cRef counts the BBTENTRY plus
	// the one NBTENTRY.bidData / bidSub / SLENTRY / XBLOCK slot that references it ([MS-PST] 2.2.2.7.7.3.1).
	bbtRefCount = 2

	// FMap / FPMap are obsolete but must exist at fixed positions ([MS-PST] 2.2.2.7.5, 2.2.2.7.6).
	// The header's rgbFM / rgbFP cover the FMaps for the first 128 AMaps and the FPMaps for 1024 PMaps.
	fmapFirst  = 128         // AMap index that carries the first FMap
	fmapEvery  = 496         // number of AMaps covered by one FMap
	fpmapFirst = 128 * 8 * 8 // AMap index that carries the first FPMap
	fpmapEvery = 496 * 8 * 8 // number of AMaps covered by one FPMap
)

// HEADER ([MS-PST] 2.2.2.6): offsets of the fields written by Finish. headerSize (564) is the whole structure.
const (
	pstMagic          = "!BDN" // dwMagic
	hdrCRCPartialOff  = 4      // dwCRCPartial: CRC of the 471 bytes from hdrMagicClientOff
	hdrMagicClientOff = 8      // wMagicClient (0x4D53, "SM"); the CRC ranges start here
	hdrVerOff         = 10     // wVer
	hdrVerClientOff   = 12     // wVerClient
	hdrPlatformOff    = 14     // bPlatformCreate, bPlatformAccess
	hdrBidNextPOff    = 32     // bidNextP
	hdrUniqueOff      = 40     // dwUnique
	hdrNIDOff         = 44     // rgnid[nidTypeCount]: last-allocated nidIndex per NID type
	hdrRootOff        = 180    // ROOT, 72 bytes ([MS-PST] 2.2.2.5)
	hdrFMOff          = 256    // rgbFM + rgbFP (2 x 128 bytes, deprecated, filled with 0xFF)
	hdrSentinelOff    = 512    // bSentinel
	hdrCryptOff       = 513    // bCryptMethod
	hdrBidNextBOff    = 516    // bidNextB
	hdrCRCFullOff     = 524    // dwCRCFull: CRC of the 516 bytes from hdrMagicClientOff
	hdrCRCPartialEnd  = hdrMagicClientOff + 471
	hdrCRCFullEnd     = hdrMagicClientOff + 516

	magicClient     = 0x4D53 // wMagicClient
	verUnicode      = 23     // wVer: Unicode PST (>= 23); the value the writer emits and the least the reader accepts
	verClient       = 19     // wVerClient
	platformWindows = 1      // bPlatformCreate / bPlatformAccess
	sentinel        = 0x80   // bSentinel
	nidTypeCount    = 32     // NID_TYPE is a 5-bit value

	// ROOT field offsets, relative to hdrRootOff.
	rootEofOff       = 4  // ibFileEof
	rootAMapLastOff  = 12 // ibAMapLast
	rootAMapFreeOff  = 20 // cbAMapFree
	rootPMapFreeOff  = 28 // cbPMapFree (deprecated, 0)
	rootNBTOff       = 36 // BREFNBT {bid, ib}
	rootBBTOff       = 52 // BREFBBT {bid, ib}
	rootAMapValidOff = 68 // fAMapValid
	validAMap2       = 2  // VALID_AMAP2
)

var le = binary.LittleEndian

// pstCRC is the CRC-32 of [MS-PST] 5.3 (CRC Calculation): seed 0 and no final inversion. It covers a block's data
// (without padding and trailer), a page up to its PAGETRAILER, and the two HEADER ranges.
func pstCRC(b []byte) uint32 { return ^crc32.Update(0xFFFFFFFF, crc32.IEEETable, b) }

// sig is wSig of a BLOCKTRAILER / PAGETRAILER ([MS-PST] 5.5 Block Signature): ib XOR bid, then the high and low 16 bits XORed.
// Pages of type BBT, NBT and DList carry it; AMap, PMap, FMap and FPMap pages have wSig 0 ([MS-PST] 2.2.2.7.1).
func sig(ib, bid uint64) uint16 {
	v := uint32(ib ^ bid)
	return uint16(v>>16) ^ uint16(v)
}

// bbtEnt is a BBTENTRY (leaf of the Block BTree, [MS-PST] 2.2.2.7.7.3): bid, its file offset, and cb (data size).
type bbtEnt struct {
	bid, ib uint64
	cb      uint16
}

// nbtEnt is an NBTENTRY (leaf of the Node BTree, [MS-PST] 2.2.2.7.7.4): a node's data tree (bidData), subnode tree (bidSub)
// and parent NID ([MS-PST] 2.2.2.7.7.4.1 Parent NID).
type nbtEnt struct {
	nid, parent uint32
	data, sub   uint64
}

// Sub is one entry of the subnode BTree: an SLENTRY ([MS-PST] 2.2.2.8.3.3.1.1).
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
	nextIdx        [nidTypeCount]uint32
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
		w.nextIdx[i] = nidStart(uint32(i)) + 1
	}
	w.zeros(firstMap) // header area (written back in Finish)
	return w, nil
}

// nidStart returns HEADER.rgnid's initial value (the last index allocated in a blank file) for NID type t.
func nidStart(t uint32) uint32 {
	switch t {
	case nidFolder:
		return nidStartFolder
	case nidSearchFolder:
		return nidStartSearch
	case nidMessage:
		return nidStartMessage
	case nidAssocMessage:
		return nidStartAssocMsg
	}
	return nidStartOther
}

// NewNID hands out an unused NID of the given type.
func (w *Writer) NewNID(t uint32) uint32 {
	i := w.nextIdx[t]
	w.nextIdx[t]++
	return i<<nidTypeBits | t
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
// An AMap appears every mapSpan bytes ([MS-PST] 2.2.2.7.2) and a PMap with every eighth AMap ([MS-PST] 2.2.2.7.3). FMaps and FPMaps
// are obsolete, but their positions must be kept ([MS-PST] 2.2.2.7.5, 2.2.2.7.6).
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

// putBlock appends one block ([MS-PST] 2.2.2.8.2 Anatomy of a Block) and registers it in the BBT. Only the data of a non-internal
// block is encoded ([MS-PST] 2.2.2.8.3.1.1); internal blocks (XBLOCK, XXBLOCK, SLBLOCK, SIBLOCK) are written as is.
func (w *Writer) putBlock(data []byte, internal bool) uint64 {
	return w.putBlockN(data, len(data), internal)
}

// putBlockN writes a block with data zero-padded up to cb bytes.
func (w *Writer) putBlockN(data []byte, cb int, internal bool) uint64 {
	if cb > blockMax || len(data) > cb {
		panic("block too large")
	}
	total := uint64((cb + blockTrailerSize + blockAlign - 1) &^ (blockAlign - 1))
	ib := w.alloc(total, blockAlign)
	bid := w.blkIdx << bidIndexShift
	if internal {
		bid |= bidInternal
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
	t := b[total-blockTrailerSize:]
	le.PutUint16(t[0:], uint16(cb))
	le.PutUint16(t[trailerSigOff:], sig(ib, bid))
	le.PutUint32(t[trailerCRCOff:], pstCRC(b[:cb])) // CRC is computed over the on-disk (post-encryption) bytes
	le.PutUint64(t[trailerBIDOff:], bid)
	w.writeRaw(b)
	w.bbt = append(w.bbt, bbtEnt{bid, ib, uint16(cb)})
	return bid
}

// putX writes an XBLOCK (level 1, referencing data blocks) or an XXBLOCK (level 2, referencing XBLOCKs); total is its lcbTotal,
// the bytes of external data below it ([MS-PST] 2.2.2.8.3.2.1, 2.2.2.8.3.2.2).
func (w *Writer) putX(level byte, ids []uint64, total int) uint64 {
	b := make([]byte, internalHdrSize+bidSize*len(ids))
	b[0], b[1] = btypeXBlock, level
	le.PutUint16(b[2:], uint16(len(ids)))
	le.PutUint32(b[4:], uint32(total)) // lcbTotal
	for i, id := range ids {
		le.PutUint64(b[internalHdrSize+bidSize*i:], id)
	}
	return w.putBlock(b, true)
}

// putChunks writes a data tree ([MS-PST] 2.2.2.8.3.2) and returns the BID: the block itself for one, or one bundled by XBLOCK/XXBLOCK for several.
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
		return w.putX(levelXBlock, ids, total)
	}
	var xids []uint64
	for i := 0; i < len(ids); i += xbMax {
		j := min(len(ids), i+xbMax)
		sub := (j - i) * blockMax
		if j == len(ids) {
			sub += len(chunks[last]) - blockMax
		}
		xids = append(xids, w.putX(levelXBlock, ids[i:j], sub))
	}
	if len(xids) > xbMax {
		panic("node data too large")
	}
	return w.putX(levelXXBlock, xids, total)
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

// putSubs writes the subnode BTree ([MS-PST] 2.2.2.8.3.3): SLBLOCKs, plus one SIBLOCK above them if there are several.
func (w *Writer) putSubs(subs []Sub) uint64 {
	if len(subs) == 0 {
		return 0
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].NID < subs[j].NID })
	const per = (blockMax - internalHdrSize) / slEntSize
	type ent struct{ nid, bid uint64 }
	var leaves []ent
	for i := 0; i < len(subs); i += per {
		j := min(len(subs), i+per)
		b := make([]byte, internalHdrSize+slEntSize*(j-i))
		b[0], b[1] = btypeSLBlock, levelSLBlock
		le.PutUint16(b[2:], uint16(j-i))
		for k, s := range subs[i:j] {
			o := internalHdrSize + slEntSize*k
			le.PutUint64(b[o:], uint64(s.NID))
			le.PutUint64(b[o+8:], s.Data)
			le.PutUint64(b[o+16:], s.Sub)
		}
		leaves = append(leaves, ent{uint64(subs[i].NID), w.putBlock(b, true)})
	}
	if len(leaves) == 1 {
		return leaves[0].bid
	}
	const iper = (blockMax - internalHdrSize) / siEntSize
	if len(leaves) > iper {
		panic("too many subnodes")
	}
	b := make([]byte, internalHdrSize+siEntSize*len(leaves))
	b[0], b[1] = btypeSLBlock, levelSIBlock
	le.PutUint16(b[2:], uint16(len(leaves)))
	for k, e := range leaves {
		le.PutUint64(b[internalHdrSize+siEntSize*k:], e.nid)
		le.PutUint64(b[internalHdrSize+siEntSize*k+8:], e.bid)
	}
	return w.putBlock(b, true)
}

// PutNode writes a node's data and subnodes and registers it in the NBT ([MS-PST] 2.2.2.7.7.4).
func (w *Writer) PutNode(nid, parent uint32, chunks [][]byte, subs []Sub) {
	data := w.putChunks(chunks)
	sub := w.putSubs(subs)
	w.nbt = append(w.nbt, nbtEnt{nid: nid, parent: parent, data: data, sub: sub})
}

type btNode struct{ key, bid, ib uint64 }

// putPage writes one BTPAGE ([MS-PST] 2.2.2.7.7.1) of the given ptype. Its BID comes from the bidNextP counter, not from the block BIDs.
func (w *Writer) putPage(ptype byte, page []byte, cnt, cmax, cbEnt, level int) (bid, ib uint64) {
	ib = w.alloc(pageSize, pageSize)
	bid = w.pgNext
	w.pgNext++
	page[btCntOff], page[btCmaxOff], page[btEntSizeOff], page[btLevelOff] = byte(cnt), byte(cmax), byte(cbEnt), byte(level)
	t := page[pageTrailerOff:]
	t[0], t[1] = ptype, ptype
	le.PutUint16(t[trailerSigOff:], sig(ib, bid))
	le.PutUint32(t[trailerCRCOff:], pstCRC(page[:pageTrailerOff]))
	le.PutUint64(t[trailerBIDOff:], bid)
	w.writeRaw(page)
	return
}

// buildBT builds a BTree bottom-up: leaf pages of n entries, then BTENTRY pages ([MS-PST] 2.2.2.7.7.2) level by level until one root is left.
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
		for i := 0; i < len(cur); i += btIntMaxEnt {
			j := min(len(cur), i+btIntMaxEnt)
			page := make([]byte, pageSize)
			for k, c := range cur[i:j] {
				le.PutUint64(page[btIntEntSize*k:], c.key)
				le.PutUint64(page[btIntEntSize*k+8:], c.bid)
				le.PutUint64(page[btIntEntSize*k+16:], c.ib)
			}
			bid, ib := w.putPage(ptype, page, j-i, btIntMaxEnt, btIntEntSize, level)
			next = append(next, btNode{cur[i].key, bid, ib})
		}
		cur = next
	}
	return cur[0]
}

// Finish writes the NBT/BBT, AMap/PMap and header, and closes the file.
// Layout: [MS-PST] 2.2.2.7 (pages), 2.2.2.6 (HEADER), 2.2.2.5 (ROOT).
func (w *Writer) Finish() error {
	// BIDs of BT pages are a separate series from block BIDs (sequential numbers above the blocks, following Outlook-created files)
	w.pgNext = w.blkIdx<<bidIndexShift + pageBIDGap
	dlistBid := w.pgNext
	w.pgNext++
	sort.Slice(w.nbt, func(i, j int) bool { return w.nbt[i].nid < w.nbt[j].nid })
	nbtRoot := w.buildBT(ptypeNBT, len(w.nbt), nbtEntSize, nbtMaxEnt, func(i int, d []byte) {
		e := w.nbt[i]
		le.PutUint64(d[0:], uint64(e.nid))
		le.PutUint64(d[8:], e.data)
		le.PutUint64(d[16:], e.sub)
		le.PutUint32(d[24:], e.parent)
	}, func(i int) uint64 { return uint64(w.nbt[i].nid) })

	sort.Slice(w.bbt, func(i, j int) bool { return w.bbt[i].bid < w.bbt[j].bid })
	bbtRoot := w.buildBT(ptypeBBT, len(w.bbt), bbtEntSize, bbtMaxEnt, func(i int, d []byte) {
		e := w.bbt[i]
		le.PutUint64(d[0:], e.bid)
		le.PutUint64(d[8:], e.ib)
		le.PutUint16(d[16:], e.cb)
		le.PutUint16(d[18:], bbtRefCount) // cRef
	}, func(i int) uint64 { return w.bbt[i].bid })

	eof := w.off
	if err := w.bw.Flush(); err != nil {
		return err
	}

	// AMaps ([MS-PST] 2.2.2.7.2.1 AMAPPAGE): one bit per 64-byte unit, 1 = allocated. The tail of the last AMap beyond EOF counts as free.
	var amapFree uint64
	for _, pos := range w.amaps {
		used := min(eof-pos, mapSpan)
		units := (used + amapUnit - 1) / amapUnit
		amapFree += mapSpan - used
		page := make([]byte, pageSize)
		for u := uint64(0); u < units; u++ {
			page[u/8] |= 0x80 >> (u % 8) // bit n (MSB first) = 64-byte unit n is allocated
		}
		page[pageTrailerOff], page[pageTrailerOff+1] = ptypeAMap, ptypeAMap
		le.PutUint32(page[pageTrailerOff+trailerCRCOff:], pstCRC(page[:pageTrailerOff]))
		le.PutUint64(page[pageTrailerOff+trailerBIDOff:], pos) // bid of an AMap = its file offset
		if _, err := w.f.WriteAt(page, int64(pos)); err != nil {
			return err
		}
	}
	// PMap / FMap / FPMap are obsolete but must be present with valid CRCs ([MS-PST] 2.2.2.7.3, 2.2.2.7.5, 2.2.2.7.6); readers
	// SHOULD NOT use them (the DList is used instead). All bytes are 0xFF, as in the Outlook-created reference PST (PMap, and the FMap
	// at AMap 128). FMap bytes are nominally the longest run of free bits of the corresponding AMap (2.2.2.7.5.1), which 0xFF does not
	// express. The FPMap is not covered by any reference file (it first appears at AMap 8192, about 2 GB).
	for _, m := range []struct {
		ptype byte
		pos   []uint64
	}{{ptypePMap, w.pmaps}, {ptypeFMap, w.fmaps}, {ptypeFPMap, w.fpmaps}} {
		for _, pos := range m.pos {
			page := make([]byte, pageSize)
			for i := 0; i < pageTrailerOff; i++ {
				page[i] = 0xFF
			}
			page[pageTrailerOff], page[pageTrailerOff+1] = m.ptype, m.ptype
			le.PutUint32(page[pageTrailerOff+trailerCRCOff:], pstCRC(page[:pageTrailerOff]))
			le.PutUint64(page[pageTrailerOff+trailerBIDOff:], pos)
			if _, err := w.f.WriteAt(page, int64(pos)); err != nil {
				return err
			}
		}
	}

	// DList page at dlistPos ([MS-PST] 2.2.2.7.4.2 DLISTPAGE): empty body, trailer only (the DList is optional, [MS-PST] 2.2.2.7.4).
	dl := make([]byte, pageSize)
	dl[pageTrailerOff], dl[pageTrailerOff+1] = ptypeDList, ptypeDList
	le.PutUint16(dl[pageTrailerOff+trailerSigOff:], sig(dlistPos, dlistBid))
	le.PutUint64(dl[pageTrailerOff+trailerBIDOff:], dlistBid)
	if _, err := w.f.WriteAt(dl, dlistPos); err != nil {
		return err
	}

	h := make([]byte, headerSize)
	copy(h, pstMagic)
	le.PutUint16(h[hdrMagicClientOff:], magicClient)
	le.PutUint16(h[hdrVerOff:], verUnicode)
	le.PutUint16(h[hdrVerClientOff:], verClient)
	h[hdrPlatformOff], h[hdrPlatformOff+1] = platformWindows, platformWindows
	le.PutUint64(h[hdrBidNextPOff:], w.pgNext)
	le.PutUint32(h[hdrUniqueOff:], 1)
	for t := range nidTypeCount {
		le.PutUint32(h[hdrNIDOff+4*t:], w.nextIdx[t]-1) // rgnid holds the last allocated index
	}
	root := h[hdrRootOff:] // ROOT ([MS-PST] 2.2.2.5): EOF, last AMap, free space, and the BREFs of the NBT / BBT roots
	le.PutUint64(root[rootEofOff:], eof)
	le.PutUint64(root[rootAMapLastOff:], w.amaps[len(w.amaps)-1])
	le.PutUint64(root[rootAMapFreeOff:], amapFree)
	le.PutUint64(root[rootPMapFreeOff:], 0)
	le.PutUint64(root[rootNBTOff:], nbtRoot.bid)
	le.PutUint64(root[rootNBTOff+8:], nbtRoot.ib)
	le.PutUint64(root[rootBBTOff:], bbtRoot.bid)
	le.PutUint64(root[rootBBTOff+8:], bbtRoot.ib)
	root[rootAMapValidOff] = validAMap2
	for i := hdrFMOff; i < hdrSentinelOff; i++ {
		h[i] = 0xFF // rgbFM / rgbFP: deprecated, MUST be filled with 0xFF ([MS-PST] 2.2.2.6)
	}
	h[hdrSentinelOff] = sentinel
	h[hdrCryptOff] = byte(w.crypt)
	le.PutUint64(h[hdrBidNextBOff:], w.blkIdx<<bidIndexShift)
	// dwCRCFull covers 516 bytes and dwCRCPartial 471 bytes, both starting at wMagicClient ([MS-PST] 2.2.2.6)
	le.PutUint32(h[hdrCRCFullOff:], pstCRC(h[hdrMagicClientOff:hdrCRCFullEnd]))
	le.PutUint32(h[hdrCRCPartialOff:], pstCRC(h[hdrMagicClientOff:hdrCRCPartialEnd]))
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
