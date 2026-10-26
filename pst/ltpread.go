package pst

import (
	"fmt"
	"unicode/utf16"
)

// Minimal LTP reader (Heap-on-Node, PC lookup) and the table row remover used by Prune.

// hnRange returns the [start, end) range of heap allocation hid in the heap block b.
func hnRange(b []byte, hid uint32) (start, end int, err error) {
	idx := int(hid>>5) & 0x7FF
	if hid&0x1F != 0 || idx == 0 || len(b) < 2 {
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

// heapNode is the decrypted data blocks of a node that holds a Heap-on-Node.
type heapNode [][]byte

func (h heapNode) alloc(hid uint32) ([]byte, error) {
	page := int(hid >> 16)
	if page >= len(h) {
		return nil, fmt.Errorf("HID %#x: no such heap page", hid)
	}
	s, e, err := hnRange(h[page], hid)
	if err != nil {
		return nil, err
	}
	return h[page][s:e], nil
}

// pcLocate finds a property of a Property Context and returns the heap page and offset of its 8-byte BTH record
// (tag, type, 4-byte value / HNID).
func (h heapNode) pcLocate(tag uint16) (page, off int, ok bool, err error) {
	if len(h) == 0 || len(h[0]) < 12 || h[0][2] != 0xEC || h[0][3] != 0xBC {
		return 0, 0, false, fmt.Errorf("not a Property Context")
	}
	hdr, err := h.alloc(le.Uint32(h[0][4:]))
	if err != nil {
		return 0, 0, false, err
	}
	if len(hdr) < 8 || hdr[0] != 0xB5 || hdr[1] != 2 || hdr[2] != 6 {
		return 0, 0, false, fmt.Errorf("bad BTHHEADER")
	}
	hid, levels := le.Uint32(hdr[4:]), int(hdr[3])
	for ; hid != 0; levels-- {
		pg := int(hid >> 16)
		if pg >= len(h) {
			return 0, 0, false, fmt.Errorf("HID %#x: no such heap page", hid)
		}
		s, e, err := hnRange(h[pg], hid)
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
	if v&0x1F != 0 {
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

// folderName returns PidTagDisplayName of a folder.
func (f *File) folderName(nid uint32) (string, error) {
	h, err := f.heapNode(nid)
	if err != nil {
		return "", err
	}
	b, ok, err := h.pcBytes(0x3001)
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
// Later allocations are shifted and the HNPAGEMAP updated; HIDs are indexes, so nothing else needs fixing.
func hnReplace(b []byte, hid uint32, data []byte) ([]byte, error) {
	start, end, err := hnRange(b, hid)
	if err != nil {
		return nil, err
	}
	idx := int(hid>>5) & 0x7FF
	pm := int(le.Uint16(b))
	n := int(le.Uint16(b[pm:]))
	delta := len(data) - (end - start)
	out := make([]byte, 0, len(b)+delta)
	out = append(out, b[:start]...)
	out = append(out, data...)
	out = append(out, b[end:]...)
	npm := pm + delta
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

// subnode looks up nid in a subnode tree and returns its data BID.
func (f *File) subnode(sub uint64, nid uint32) (uint64, bool, error) {
	for sub != 0 {
		b, err := f.rawBlock(sub)
		if err != nil {
			return 0, false, err
		}
		if len(b) < 8 || b[0] != 0x02 {
			return 0, false, fmt.Errorf("block %#x: not an SLBLOCK/SIBLOCK", sub)
		}
		n := int(le.Uint16(b[2:]))
		if b[1] == 0 {
			for i := 0; i < n && 8+24*(i+1) <= len(b); i++ {
				if e := b[8+24*i:]; le.Uint64(e) == uint64(nid) {
					return le.Uint64(e[8:]), true, nil
				}
			}
			return 0, false, nil
		}
		next := uint64(0)
		for i := 0; i < n && 8+16*(i+1) <= len(b); i++ {
			if le.Uint64(b[8+16*i:]) > uint64(nid) {
				break
			}
			next = le.Uint64(b[16+16*i:])
		}
		sub = next
	}
	return 0, false, nil
}
