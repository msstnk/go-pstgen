package pst

import (
	"bytes"
	"path/filepath"
	"testing"
)

// testPC builds a Property Context heap from props (a real one, as written to a PST).
func testPC(t *testing.T, props []Prop) heapNode {
	t.Helper()
	w, err := NewWriter(filepath.Join(t.TempDir(), "pc.pst"), 4096, CryptNone)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort()
	blocks := buildPC(props, newSubCtx(w))
	// copy: tests damage the blocks
	h := make(heapNode, len(blocks))
	for i, b := range blocks {
		h[i] = append([]byte(nil), b...)
	}
	return h
}

func TestPCLookup(t *testing.T) {
	big := bytes.Repeat([]byte{7}, maxHeapItem+1) // too big for the heap: goes to a subnode
	h := testPC(t, []Prop{
		PInt32(0x0E08, 1234), PStr(0x3001, "name"), PBin(0x0FF9, []byte{1, 2, 3}), PBin(0x3701, big), PBin(0x3702, nil),
	})
	if typ, v, ok, err := h.pcValue(0x0E08); err != nil || !ok || typ != PtLong || v != 1234 {
		t.Errorf("int32: %v %v %v %v", typ, v, ok, err)
	}
	if b, ok, err := h.pcBytes(0x0FF9); err != nil || !ok || !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Errorf("binary: %v %v %v", b, ok, err)
	}
	if b, ok, err := h.pcBytes(0x3001); err != nil || !ok || !bytes.Equal(b, UTF16LE("name")) {
		t.Errorf("string: %v %v %v", b, ok, err)
	}
	if _, _, ok, err := h.pcValue(0x9999); ok || err != nil {
		t.Errorf("missing tag: ok=%v err=%v", ok, err)
	}
	if _, ok, err := h.pcBytes(0x3702); ok || err != nil { // empty value: HID 0
		t.Errorf("empty value: ok=%v err=%v", ok, err)
	}
	if _, _, err := h.pcBytes(0x3701); err == nil { // lives in a subnode, which the reader does not follow
		t.Error("subnode value: expected error")
	}
}

// With more than 447 properties the BTH has an intermediate level; every key must still be found, and keys outside the
// range (below the first, above the last, in a gap) must be reported as missing, not as an error.
func TestPCLookupMultiLevel(t *testing.T) {
	var props []Prop
	for i := 0; i < 1000; i++ {
		props = append(props, PInt32(uint16(0x1000+2*i), int32(i)))
	}
	h := testPC(t, props)
	hdr, err := h.alloc(le.Uint32(h[0][4:]))
	if err != nil || hdr[bthIdxLevels] == 0 {
		t.Fatalf("expected an intermediate BTH level, got %v err=%v", hdr, err)
	}
	for i := 0; i < 1000; i++ {
		if _, v, ok, err := h.pcValue(uint16(0x1000 + 2*i)); err != nil || !ok || v != uint32(i) {
			t.Fatalf("tag %#x: v=%d ok=%v err=%v", 0x1000+2*i, v, ok, err)
		}
	}
	for _, tag := range []uint16{0x0001, 0x1001, 0x1000 + 2*500 + 1, 0xFFFF} {
		if _, _, ok, err := h.pcValue(tag); ok || err != nil {
			t.Errorf("tag %#x: ok=%v err=%v", tag, ok, err)
		}
	}
}

// Damaged headers or references are reported as errors by pcLocate and never read out of range.
func TestPCLookupDamaged(t *testing.T) {
	props := []Prop{PInt32(0x0E08, 1), PStr(0x3001, "x")}
	for name, damage := range map[string]func(h heapNode){
		"empty heap":        func(h heapNode) { h[0] = nil },
		"short first block": func(h heapNode) { h[0] = h[0][:hnHdrSize-1] },
		"bad HN signature":  func(h heapNode) { h[0][2] = 0 },
		"not a PC":          func(h heapNode) { h[0][3] = clientSigTC },
		"user root HID":     func(h heapNode) { le.PutUint32(h[0][4:], 0x7FF1<<hidIdxShift) },
		"user root page":    func(h heapNode) { le.PutUint32(h[0][4:], 9<<16|1<<hidIdxShift) },
		"BTH signature": func(h heapNode) {
			hdr, _ := h.alloc(le.Uint32(h[0][4:]))
			hdr[0] = 0
		},
		"BTH key size": func(h heapNode) {
			hdr, _ := h.alloc(le.Uint32(h[0][4:]))
			hdr[1] = 4
		},
		"BTH record size": func(h heapNode) {
			hdr, _ := h.alloc(le.Uint32(h[0][4:]))
			hdr[2] = 8
		},
		"BTH root HID": func(h heapNode) {
			hdr, _ := h.alloc(le.Uint32(h[0][4:]))
			le.PutUint32(hdr[bthRootOff:], 0x1F<<hidIdxShift)
		},
		"HNPAGEMAP offset": func(h heapNode) { le.PutUint16(h[0], uint16(len(h[0]))) },
	} {
		t.Run(name, func(t *testing.T) {
			h := testPC(t, props)
			damage(h)
			if _, _, ok, err := h.pcLocate(0x0E08); ok || err == nil {
				t.Errorf("ok=%v err=%v, want an error", ok, err)
			}
			if _, _, _, err := h.pcValue(0x0E08); err == nil {
				t.Error("pcValue: expected an error")
			}
			if _, _, err := h.pcBytes(0x3001); err == nil {
				t.Error("pcBytes: expected an error")
			}
		})
	}
}

func TestHNRangeRejectsBadHIDs(t *testing.T) {
	h := testPC(t, []Prop{PInt32(0x0E08, 1)})
	b := h[0]
	pm := int(le.Uint16(b))
	n := int(le.Uint16(b[pm:]))
	if n < 1 {
		t.Fatalf("n = %d", n)
	}
	for name, hid := range map[string]uint32{
		"index 0":      0,
		"type bits":    1<<hidIdxShift | 1,
		"index > n":    uint32(n+1) << hidIdxShift,
		"index > mask": 0xFFFF << hidIdxShift,
	} {
		if _, _, err := hnRange(b, hid); err == nil {
			t.Errorf("%s: accepted HID %#x", name, hid)
		}
	}
	if _, _, err := hnRange(nil, 1<<hidIdxShift); err == nil {
		t.Error("nil block accepted")
	}
	if _, _, err := hnRange([]byte{0xFF}, 1<<hidIdxShift); err == nil {
		t.Error("1-byte block accepted")
	}
	// An allocation whose start is after its end, or whose end is past the page map.
	c := append([]byte(nil), b...)
	le.PutUint16(c[pm+4:], 40) // rgibAlloc[0] (start of allocation 1)
	le.PutUint16(c[pm+4+2:], 20)
	if _, _, err := hnRange(c, 1<<hidIdxShift); err == nil {
		t.Error("start > end accepted")
	}
	le.PutUint16(c[pm+4:], 0)
	le.PutUint16(c[pm+4+2:], uint16(pm+1))
	if _, _, err := hnRange(c, 1<<hidIdxShift); err == nil {
		t.Error("end past the page map accepted")
	}
	// An allocation count that would put the page map beyond the block.
	d := append([]byte(nil), b...)
	le.PutUint16(d[pm:], 0xFFFF)
	if _, _, err := hnRange(d, 1<<hidIdxShift); err == nil {
		t.Error("huge allocation count accepted")
	}
}

func TestHNReplace(t *testing.T) {
	h := testPC(t, []Prop{PStr(0x3001, "first"), PStr(0x3002, "second"), PStr(0x3003, "third")})
	_, v, _, _ := h.pcValue(0x3002)
	b := h[0]
	for name, data := range map[string][]byte{"shorter": UTF16LE("2"), "longer": UTF16LE("a much longer second value"), "empty": nil} {
		t.Run(name, func(t *testing.T) {
			got, err := hnReplace(b, v, data)
			if err != nil {
				t.Fatal(err)
			}
			hh := heapNode{got}
			if a, err := hh.alloc(v); err != nil || !bytes.Equal(a, data) {
				t.Errorf("replaced allocation = %q, %v", a, err)
			}
			// The neighbours keep their values.
			for tag, want := range map[uint16]string{0x3001: "first", 0x3003: "third"} {
				if a, ok, err := hh.pcBytes(tag); err != nil || !ok || !bytes.Equal(a, UTF16LE(want)) {
					t.Errorf("tag %#x = %q, %v", tag, a, err)
				}
			}
			pm := int(le.Uint16(got))
			if free := le.Uint16(got[pm+2:]); (len(data) == 0) != (free == 1) {
				t.Errorf("cFree = %d for len(data) = %d", free, len(data))
			}
		})
	}
	if _, err := hnReplace(b, 0x7FF<<hidIdxShift, nil); err == nil {
		t.Error("bad HID accepted")
	}
}
