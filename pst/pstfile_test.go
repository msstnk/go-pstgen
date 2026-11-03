package pst

import (
	"path/filepath"
	"testing"
)

// writeSelfRef writes a minimal PST with one node whose data tree (internal) or subnode tree (sub) is a block listing itself.
func writeSelfRef(t *testing.T, sub bool) (*File, nbtEntry) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cyc.pst")
	w, err := NewWriter(p, 1<<16, CryptNone)
	if err != nil {
		t.Fatal(err)
	}
	bid := w.blkIdx<<bidIndexShift | bidInternal
	b := make([]byte, internalHdrSize+bidSize)
	if sub {
		b = make([]byte, internalHdrSize+siEntSize)
		b[0], b[1] = btypeSLBlock, levelSIBlock
		le.PutUint16(b[2:], 1)
		le.PutUint64(b[internalHdrSize+bidSize:], bid) // one SIENTRY: key 0, bid itself
	} else {
		b[0], b[1] = btypeXBlock, levelXXBlock
		le.PutUint16(b[2:], 1)
		le.PutUint64(b[internalHdrSize:], bid)
	}
	if got := w.putBlock(b, true); got != bid {
		t.Fatalf("bid %#x, want %#x", got, bid)
	}
	e := nbtEnt{nid: 0x21, data: bid}
	if sub {
		e = nbtEnt{nid: 0x21, sub: bid}
	}
	w.nbt = append(w.nbt, e)
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	f, err := openPST(p, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.close() })
	n, ok := f.node(0x21)
	if !ok {
		t.Fatal("node not found")
	}
	return f, n
}

func TestCyclicBlockTreesAreRejected(t *testing.T) {
	f, e := writeSelfRef(t, false)
	if _, err := f.dataBlocks(e.data); err == nil {
		t.Error("dataBlocks: expected error for a self-referencing XXBLOCK")
	}
	if err := f.walkNode(e, func(uint64) {}); err == nil {
		t.Error("walkNode: expected error for a self-referencing XXBLOCK")
	}
	f, e = writeSelfRef(t, true)
	if err := f.walkNode(e, func(uint64) {}); err == nil {
		t.Error("walkNode: expected error for a self-referencing SIBLOCK")
	}
	if _, _, err := f.subnode(e.sub, 0x40); err == nil {
		t.Error("subnode: expected error for a self-referencing SIBLOCK")
	}
}

// tamperedHierarchy opens a copy of the sample file read-write and returns the hierarchy table of the IPM subtree
// (rows are the top-level folders, stored in the heap) with the RowID of its first row.
func tamperedHierarchy(t *testing.T) (f *File, e nbtEntry, tab *tableInfo, rowID uint32) {
	t.Helper()
	f, err := openPST(buildPrunable(t, CryptNone), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.close() })
	e, ok := f.node(0x8022&^nidTypeMask | nidHierarchy)
	if !ok {
		t.Fatal("hierarchy table not found")
	}
	if tab, err = f.loadTable(e); err != nil || tab.root == nil {
		t.Fatalf("loadTable: %v", err)
	}
	tab.root.leaves(func(l *bthNode) {
		if rowID == 0 {
			rowID = le.Uint32(l.recs)
		}
	})
	if tab.rowsHnid&nidTypeMask != 0 {
		t.Fatalf("expected the row matrix in the heap, got HNID %#x", tab.rowsHnid)
	}
	return f, e, tab, rowID
}

// A TC whose TCINFO is inconsistent with its heap and passes every CRC must give an error, not a panic.
func TestClearTableCellRejectsBadLayout(t *testing.T) {
	// the sanity case: the untouched table works
	f, e, _, id := tamperedHierarchy(t)
	if err := f.clearTableCell(e, id, pidContentCount, PtLong); err != nil {
		t.Fatalf("untouched table: %v", err)
	}
	for name, tamper := range map[string]func(tab *tableInfo){
		"row matrix on a missing heap page": func(tab *tableInfo) {
			le.PutUint32(tab.info[tcRowsOff:], 7<<16|1<<hidIdxShift)
		},
		"row size beyond the matrix": func(tab *tableInfo) {
			le.PutUint16(tab.info[tcRgibOff+2*tciBM:], blockMax-1)
		},
		"column beyond the row": func(tab *tableInfo) {
			for i := 0; i < int(tab.info[tcColsOff]); i++ {
				d := tab.info[tcInfoHdrSize+tcColDescSize*i:]
				if le.Uint32(d) == uint32(pidContentCount)<<16|uint32(PtLong) {
					le.PutUint16(d[4:], 0xFFF0)
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, e, tab, id := tamperedHierarchy(t)
			tamper(tab)
			f.saveHeap(tab.h)
			if err := f.clearTableCell(e, id, pidContentCount, PtLong); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// Prune zero-fills blocks at the position and length the BBT gives, so a BBT cb above the block limit has to be refused.
func TestBlockRefRejectsOversizedCB(t *testing.T) {
	f, err := openPST(buildPrunable(t, CryptNone), false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	var bid uint64
	f.bbt.each(func(e []byte) {
		if bid == 0 {
			bid = le.Uint64(e)
		}
	})
	if _, err := f.blockRef(bid); err != nil {
		t.Fatalf("untouched entry: %v", err)
	}
	p, i := f.bbt.find(bid)
	le.PutUint16(p.buf[i*bbtEntSize+16:], blockMax+1)
	if _, err := f.blockRef(bid); err == nil {
		t.Error("blockRef accepted cb > blockMax")
	}
	if _, err := f.rawBlock(bid); err == nil {
		t.Error("rawBlock accepted cb > blockMax")
	}
}
