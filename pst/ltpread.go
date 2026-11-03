package pst

import (
	"fmt"
	"unicode/utf16"
)

// Minimal LTP reader (Heap-on-Node, PC lookup) and the table row remover used by Prune. See [MS-PST] 2.3 (LTP layer).

// hnRange returns the [start, end) range of heap allocation hid in the heap block b: the HID's hidIndex selects an entry of the
// HNPAGEMAP's rgibAlloc, which holds the start offsets, so allocation n ends where n+1 starts ([MS-PST] 2.3.1.1, 2.3.1.5).
func hnRange(b []byte, hid uint32) (start, end int, err error) {
	idx := int(hid>>hidIdxShift) & hidIdxMask
	if hid&hidTypeMask != 0 || idx == 0 || len(b) < 2 {
		return 0, 0, fmt.Errorf("bad HID %#x", hid)
	}
	pm := int(le.Uint16(b))
	if pm+4 > len(b) {
		return 0, 0, fmt.Errorf("bad HNPAGEMAP offset %#x", pm)
	}
	n := int(le.Uint16(b[pm:]))
	if idx > n || pm+4+2*(n+1) > len(b) {
		return 0, 0, fmt.Errorf("HID %#x out of range", hid)
	}
	start = int(le.Uint16(b[pm+4+2*(idx-1):]))
	end = int(le.Uint16(b[pm+4+2*idx:]))
	if start > end || end > pm {
		return 0, 0, fmt.Errorf("HID %#x has a bad allocation range", hid)
	}
	return start, end, nil
}

// heapNode is the decrypted data blocks of a node that holds a Heap-on-Node ([MS-PST] 2.3.1). The page of an HID is its
// hidBlockIndex, the index of the data block in the node's data tree.
type heapNode [][]byte

// allocRange returns the heap page of hid and the [start, end) range of its allocation within that page.
func (h heapNode) allocRange(hid uint32) (page, start, end int, err error) {
	page = int(hid >> 16)
	if page >= len(h) {
		return 0, 0, 0, fmt.Errorf("HID %#x: no such heap page", hid)
	}
	start, end, err = hnRange(h[page], hid)
	return page, start, end, err
}

func (h heapNode) alloc(hid uint32) ([]byte, error) {
	page, s, e, err := h.allocRange(hid)
	if err != nil {
		return nil, err
	}
	return h[page][s:e], nil
}

// pcLocate finds a property of a Property Context ([MS-PST] 2.3.3: the BTH under the HNHDR's hidUserRoot, keyed by property ID) and returns the heap page and offset of its 8-byte BTH record
// (tag, type, 4-byte value / HNID).
func (h heapNode) pcLocate(tag uint16) (page, off int, ok bool, err error) {
	if len(h) == 0 || len(h[0]) < hnHdrSize || h[0][2] != hnSig || h[0][3] != clientSigPC {
		return 0, 0, false, fmt.Errorf("not a Property Context")
	}
	hdr, err := h.alloc(le.Uint32(h[0][4:]))
	if err != nil {
		return 0, 0, false, err
	}
	if len(hdr) < bthHdrSize || hdr[0] != clientSigBTH || hdr[1] != pcKeySize || hdr[2] != pcEntSize {
		return 0, 0, false, fmt.Errorf("bad BTHHEADER")
	}
	hid, levels := le.Uint32(hdr[4:]), int(hdr[3])
	for ; hid != 0; levels-- {
		pg, s, e, err := h.allocRange(hid)
		if err != nil {
			return 0, 0, false, err
		}
		a := h[pg][s:e]
		if levels > 0 {
			next := uint32(0)
			for i := 0; i+6 <= len(a) && le.Uint16(a[i:]) <= tag; i += 6 {
				next = le.Uint32(a[i+2:])
			}
			hid = next
			continue
		}
		for i := 0; i+8 <= len(a); i += 8 {
			if le.Uint16(a[i:]) == tag {
				return pg, s + i, true, nil
			}
		}
		break
	}
	return 0, 0, false, nil
}

// pcValue looks up a property of a Property Context: its type and the 4-byte value / HNID.
func (h heapNode) pcValue(tag uint16) (typ uint16, val uint32, ok bool, err error) {
	pg, off, ok, err := h.pcLocate(tag)
	if err != nil || !ok {
		return 0, 0, false, err
	}
	return le.Uint16(h[pg][off+2:]), le.Uint32(h[pg][off+4:]), true, nil
}

// pcBytes returns the value of a PC property stored in the heap (HID); values in subnodes are not supported.
func (h heapNode) pcBytes(tag uint16) ([]byte, bool, error) {
	_, v, ok, err := h.pcValue(tag)
	if err != nil || !ok || v == 0 {
		return nil, false, err
	}
	if v&hidTypeMask != 0 {
		return nil, false, fmt.Errorf("property %#04x is stored in a subnode", tag)
	}
	b, err := h.alloc(v)
	return b, err == nil, err
}

func (f *File) heapNode(nid uint32) (heapNode, error) {
	e, ok := f.node(nid)
	if !ok {
		return nil, fmt.Errorf("node %#x not found", nid)
	}
	bs, err := f.dataBlocks(e.data)
	if err != nil {
		return nil, fmt.Errorf("node %#x: %w", nid, err)
	}
	return heapNode(bs), nil
}

// folderName returns PidTagDisplayName of a folder: a PtypString value, UTF-16LE ([MS-PST] 2.3.3.3, [MS-OXCDATA] 2.11.1).
func (f *File) folderName(nid uint32) (string, error) {
	h, err := f.heapNode(nid)
	if err != nil {
		return "", err
	}
	b, ok, err := h.pcBytes(pidDisplayName)
	if err != nil || !ok {
		return "", err
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u)), nil
}

// hnReplace returns a copy of the heap block b with allocation hid replaced by data (which may differ in length).
// Later allocations are shifted and the rgibAlloc offsets of the HNPAGEMAP updated ([MS-PST] 2.3.1.5); HIDs are indexes, so nothing else
// needs fixing. An allocation emptied to zero length counts as freed (cFree).
func hnReplace(b []byte, hid uint32, data []byte) ([]byte, error) {
	start, end, err := hnRange(b, hid)
	if err != nil {
		return nil, err
	}
	idx := int(hid>>hidIdxShift) & hidIdxMask
	pm := int(le.Uint16(b))
	n := int(le.Uint16(b[pm:]))
	delta := len(data) - (end - start)
	last := int(le.Uint16(b[pm+4+2*n:])) // end of the last allocation; pm-last is 0 or 1 pad byte
	newLast := last + delta
	npm := (newLast + 1) &^ 1 // the HNPAGEMAP stays 2-byte aligned, as the builder lays it out
	out := make([]byte, 0, npm+len(b)-pm)
	out = append(out, b[:start]...)
	out = append(out, data...)
	out = append(out, b[end:last]...)
	for len(out) < npm {
		out = append(out, 0)
	}
	out = append(out, b[pm:]...)
	le.PutUint16(out, uint16(npm))
	for j := idx; j <= n; j++ {
		o := npm + 4 + 2*j
		le.PutUint16(out[o:], uint16(int(le.Uint16(out[o:]))+delta))
	}
	if len(data) == 0 && end > start { // the allocation is now free
		le.PutUint16(out[npm+2:], le.Uint16(out[npm+2:])+1)
	}
	return out, nil
}

// subnode looks up nid in a subnode tree (SLBLOCK / SIBLOCK, [MS-PST] 2.2.2.8.3.3) and returns its data BID.
func (f *File) subnode(sub uint64, nid uint32) (uint64, bool, error) {
	for depth := 0; sub != 0; depth++ {
		if depth > maxTreeDepth {
			return 0, false, errTreeDepth(sub)
		}
		b, err := f.rawBlock(sub)
		if err != nil {
			return 0, false, err
		}
		if len(b) < 8 || b[0] != btypeSLBlock {
			return 0, false, fmt.Errorf("block %#x: not an SLBLOCK/SIBLOCK", sub)
		}
		n := int(le.Uint16(b[2:]))
		if b[1] == levelSLBlock {
			for i := 0; i < n && internalHdrSize+slEntSize*(i+1) <= len(b); i++ {
				if e := b[internalHdrSize+slEntSize*i:]; le.Uint64(e) == uint64(nid) {
					return bidRef(le.Uint64(e[8:])), true, nil
				}
			}
			return 0, false, nil
		}
		next := uint64(0)
		for i := 0; i < n && internalHdrSize+siEntSize*(i+1) <= len(b); i++ {
			if le.Uint64(b[internalHdrSize+siEntSize*i:]) > uint64(nid) {
				break
			}
			next = bidRef(le.Uint64(b[internalHdrSize+siEntSize*i+bidSize:]))
		}
		sub = next
	}
	return 0, false, nil
}
