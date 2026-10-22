package pst

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMapPages verifies that AMap / PMap / FMap sit at their fixed positions in a file extending past AMap 128.
func TestMapPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.pst")
	w, err := NewWriter(path, 1<<20, CryptNone)
	if err != nil {
		t.Fatal(err)
	}
	for w.nextMap <= fmapFirst+1 {
		ib := w.alloc(8192, 64)
		w.zeros(ib + 8192 - w.off)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	f, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint64]byte{}
	for i := uint64(0); i <= fmapFirst+1; i++ {
		pos := firstMap + i*mapSpan
		want[pos] = 0x84
		next := pos + pageSize
		if i%8 == 0 {
			want[next] = 0x83
			next += pageSize
		}
		if i == fmapFirst {
			want[next] = 0x82
		}
	}
	for pos, ptype := range want {
		p := f[pos : pos+pageSize]
		if p[496] != ptype || p[497] != ptype || le.Uint64(p[504:]) != pos || le.Uint32(p[500:]) != pstCRC(p[:496]) {
			t.Errorf("page @%#x: ptype %#x bid %#x, want ptype %#x", pos, p[496], le.Uint64(p[504:]), ptype)
		}
	}
	if len(w.fmaps) != 1 {
		t.Errorf("fmaps = %d, want 1", len(w.fmaps))
	}
}

// TestChunkPadding verifies that in a multi-block data tree every block but the last is padded to blockMax.
func TestChunkPadding(t *testing.T) {
	w, err := NewWriter(filepath.Join(t.TempDir(), "p.pst"), 1<<20, CryptNone)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort()
	sizes := []int{8064, 100, 4716}
	chunks := make([][]byte, len(sizes))
	for i, n := range sizes {
		chunks[i] = make([]byte, n)
	}
	w.putChunks(chunks)
	got := w.bbt[:len(sizes)]
	for i, e := range got {
		want := blockMax
		if i == len(sizes)-1 {
			want = sizes[i]
		}
		if int(e.cb) != want {
			t.Errorf("block %d cb = %d, want %d", i, e.cb, want)
		}
	}
	if x := w.bbt[len(sizes)]; x.bid&2 == 0 {
		t.Fatalf("expected XBLOCK, got bid %#x", x.bid)
	}
}
