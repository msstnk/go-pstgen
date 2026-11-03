package pst

import (
	"fmt"
	"path/filepath"
	"testing"
)

// bulkTable writes a PST whose folder "Bulk" has kids subfolders and opens it read-write. It returns the hierarchy table
// node of Bulk and the NID of its first subfolder (which holds one message, so its content count is not zero).
// With 3 kids the row matrix is in the heap, with 100 one subnode block, with 300 an XBLOCK of blocks; with 1000 the heap itself also spans several blocks.
func bulkTable(t *testing.T, kids int) (f *File, e nbtEntry, first uint32) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "b.pst")
	s, err := Create(p, "test@example.com", []byte("0123456789abcdef"), 1<<20, CryptNone)
	if err != nil {
		t.Fatal(err)
	}
	bulk := s.AddFolder(s.Top, "Bulk")
	for i := 0; i < kids; i++ {
		c := s.AddFolder(bulk, fmt.Sprintf("c%03d", i))
		if i == 0 {
			first = c.NID
			s.AddMessage(c, testMessage(0))
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if f, err = openPST(p, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.close() })
	e, ok := f.node(bulk.NID&^nidTypeMask | nidHierarchy)
	if !ok {
		t.Fatal("hierarchy table not found")
	}
	return f, e, first
}

// editBlock changes a block through the staging area, as an edit would, so that later reads see the damage.
func editBlock(t *testing.T, f *File, bid uint64, fn func(b []byte) []byte) {
	t.Helper()
	b, err := f.readBlock(bid)
	if err != nil {
		t.Fatal(err)
	}
	f.stage(bid, fn(b))
}

// Damage inside the heap of a single-block table is reported by every table operation; none of them panics.
func TestTableEditRejectsDamagedHeap(t *testing.T) {
	infoPos := func(pg []byte) int {
		s, _, err := hnRange(pg, le.Uint32(pg[4:]))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for name, damage := range map[string]func(pg []byte){
		"HN signature":     func(pg []byte) { pg[2] = 0 },
		"not a TC":         func(pg []byte) { pg[3] = clientSigPC },
		"TCINFO HID":       func(pg []byte) { le.PutUint32(pg[4:], 0x7FF<<hidIdxShift) },
		"TCINFO signature": func(pg []byte) { pg[infoPos(pg)] = 0 },
		"row size 0":       func(pg []byte) { le.PutUint16(pg[infoPos(pg)+tcRgibOff+2*tciBM:], 0) },
		"row size > block": func(pg []byte) { le.PutUint16(pg[infoPos(pg)+tcRgibOff+2*tciBM:], blockMax+1) },
		"row index HID":    func(pg []byte) { le.PutUint32(pg[infoPos(pg)+tcRowIndexOff:], 0x7FF<<hidIdxShift) },
		"row index BTH": func(pg []byte) {
			s, _, _ := hnRange(pg, le.Uint32(pg[infoPos(pg)+tcRowIndexOff:]))
			pg[s] = 0
		},
		"row index root": func(pg []byte) {
			s, _, _ := hnRange(pg, le.Uint32(pg[infoPos(pg)+tcRowIndexOff:]))
			le.PutUint32(pg[s+4:], 0x7FF<<hidIdxShift)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, e, first := bulkTable(t, 100)
			editBlock(t, f, e.data, func(b []byte) []byte { damage(b); return b })
			if _, err := f.loadTable(e); err == nil {
				t.Error("loadTable accepted the damage")
			}
			if _, _, err := f.removeTableRows(e, map[uint32]bool{first: true}); err == nil {
				t.Error("removeTableRows accepted the damage")
			}
			if err := f.clearTableCell(e, first, pidContentCount, PtLong); err == nil {
				t.Error("clearTableCell accepted the damage")
			}
		})
	}
	// A heap block that cannot be read at all.
	f, e, first := bulkTable(t, 100)
	gone := e
	gone.data = 0x7FFF0
	if _, err := f.loadTable(gone); err == nil {
		t.Error("loadTable: missing heap block accepted")
	}
	if err := f.clearTableCell(gone, first, pidContentCount, PtLong); err == nil {
		t.Error("clearTableCell: missing heap block accepted")
	}
}

// A row matrix stored in a subnode is checked before it is used.
func TestLoadMatrixRejectsDamage(t *testing.T) {
	f, e, _ := bulkTable(t, 100)
	tab, err := f.loadTable(e)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := f.loadMatrix(e, tab); err != nil || len(m.kids) != 1 || m.xbid != 0 {
		t.Fatalf("untouched table: %+v, %v", m, err)
	}
	for name, mod := range map[string]func(tt *tableInfo, ee *nbtEntry){
		"HNID is neither HID nor subnode": func(tt *tableInfo, _ *nbtEntry) { tt.rowsHnid = nidMessage | 1<<nidTypeBits },
		"subnode is missing":              func(tt *tableInfo, _ *nbtEntry) { tt.rowsHnid = nidLTP | 0x7777<<nidTypeBits },
		"node without subnodes":           func(_ *tableInfo, ee *nbtEntry) { ee.sub = 0 },
		"block count mismatch (rows)":     func(tt *tableInfo, _ *nbtEntry) { tt.n += blockMax },
		"block count mismatch (size)":     func(tt *tableInfo, _ *nbtEntry) { tt.rowSize = blockMax },
	} {
		t.Run(name, func(t *testing.T) {
			tt, ee := *tab, e
			mod(&tt, &ee)
			if _, err := f.loadMatrix(ee, &tt); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// With 300 subfolders the matrix is an XBLOCK; with 1000 the heap is too.
func TestTableEditRejectsDamagedXBlocks(t *testing.T) {
	t.Run("matrix", func(t *testing.T) {
		f, e, first := bulkTable(t, 300)
		tab, err := f.loadTable(e)
		if err != nil {
			t.Fatal(err)
		}
		dbid, ok, err := f.subnode(e.sub, tab.rowsHnid)
		if err != nil || !ok || dbid&bidInternal == 0 {
			t.Fatalf("expected an XBLOCK matrix: %#x %v %v", dbid, ok, err)
		}
		if m, err := f.loadMatrix(e, tab); err != nil || len(m.kids) < 2 || m.xbid != dbid {
			t.Fatalf("untouched: %+v %v", m, err)
		}
		for name, damage := range map[string]func(b []byte) []byte{
			"two levels":        func(b []byte) []byte { b[1] = 2; return b },
			"not an XBLOCK":     func(b []byte) []byte { b[0] = 0; return b },
			"truncated":         func(b []byte) []byte { le.PutUint16(b[2:], 0xFFFF); return b },
			"one child missing": func(b []byte) []byte { le.PutUint16(b[2:], le.Uint16(b[2:])-1); return b },
			"too short":         func(b []byte) []byte { return b[:1] },
		} {
			t.Run(name, func(t *testing.T) {
				f, e, first := bulkTable(t, 300)
				editBlock(t, f, dbid, damage)
				tab, err := f.loadTable(e)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.loadMatrix(e, tab); err == nil {
					t.Error("loadMatrix accepted the damage")
				}
				if _, _, err := f.removeTableRows(e, map[uint32]bool{first: true}); err == nil {
					t.Error("removeTableRows accepted the damage")
				}
				if err := f.clearTableCell(e, first, pidContentCount, PtLong); err == nil {
					t.Error("clearTableCell accepted the damage")
				}
			})
		}
		_ = first
	})
	t.Run("heap", func(t *testing.T) {
		for name, damage := range map[string]func(b []byte) []byte{
			"two levels": func(b []byte) []byte { b[1] = 2; return b },
			"truncated":  func(b []byte) []byte { le.PutUint16(b[2:], 0xFFFF); return b },
			"too short":  func(b []byte) []byte { return b[:1] },
		} {
			t.Run(name, func(t *testing.T) {
				f, e, first := bulkTable(t, 1000)
				if e.data&bidInternal == 0 {
					t.Fatalf("expected an XBLOCK heap, got block %#x", e.data)
				}
				if _, err := f.loadHeap(e.data); err != nil {
					t.Fatal(err)
				}
				editBlock(t, f, e.data, damage)
				if _, err := f.loadHeap(e.data); err == nil {
					t.Error("loadHeap accepted the damage")
				}
				if _, _, err := f.removeTableRows(e, map[uint32]bool{first: true}); err == nil {
					t.Error("removeTableRows accepted the damage")
				}
			})
		}
	})
}

// removeTableRows refuses a table whose index or matrix disagrees with itself, and leaves nothing staged.
func TestRemoveTableRowsRejectsInconsistentTables(t *testing.T) {
	t.Run("row index beyond the rows", func(t *testing.T) {
		f, e, first := bulkTable(t, 100)
		tab, err := f.loadTable(e)
		if err != nil {
			t.Fatal(err)
		}
		editBlock(t, f, e.data, func(pg []byte) []byte {
			s, _, err := hnRange(pg, tab.root.hid)
			if err != nil {
				t.Fatal(err)
			}
			le.PutUint32(pg[s+4:], 9999) // the row index (second field) of the first record
			return pg
		})
		if _, _, err := f.removeTableRows(e, map[uint32]bool{first: true}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("matrix block too short", func(t *testing.T) {
		f, e, first := bulkTable(t, 100)
		tab, _ := f.loadTable(e)
		m, err := f.loadMatrix(e, tab)
		if err != nil {
			t.Fatal(err)
		}
		editBlock(t, f, m.kids[0], func(b []byte) []byte { return b[:1] })
		if _, _, err := f.removeTableRows(e, map[uint32]bool{first: true}); err == nil {
			t.Error("removeTableRows accepted a short matrix block")
		}
		if err := f.clearTableCell(e, first, pidContentCount, PtLong); err == nil {
			t.Error("clearTableCell accepted a short matrix block")
		}
	})
	t.Run("heap matrix of the wrong size", func(t *testing.T) {
		f, e, first := bulkTable(t, 3) // few rows: the matrix stays in the heap
		tab, err := f.loadTable(e)
		if err != nil || tab.rowsHnid&nidTypeMask != 0 {
			t.Fatalf("expected a heap matrix: %v %#x", err, tab.rowsHnid)
		}
		editBlock(t, f, e.data, func(pg []byte) []byte {
			s, _, _ := hnRange(pg, tab.infoHid)
			p := pg[s+tcRgibOff+2*tciBM:]
			le.PutUint16(p, le.Uint16(p)+1)
			return pg
		})
		if _, _, err := f.removeTableRows(e, map[uint32]bool{first: true}); err == nil {
			t.Error("expected an error")
		}
	})
}

// clearTableCell on a subnode matrix zeroes exactly the cell, and refuses a column that lies outside the row.
func TestClearTableCellInSubnodeMatrix(t *testing.T) {
	f, e, first := bulkTable(t, 100)
	tab, err := f.loadTable(e)
	if err != nil || tab.rowsHnid&nidTypeMask != nidLTP {
		t.Fatalf("expected a subnode matrix: %v %#x", err, tab.rowsHnid)
	}
	m, err := f.loadMatrix(e, tab)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.readBlock(m.kids[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := f.clearTableCell(e, first, pidContentCount, PtLong); err != nil {
		t.Fatal(err)
	}
	after, err := f.readBlock(m.kids[0])
	if err != nil {
		t.Fatal(err)
	}
	var diff []int
	for i := range before {
		if before[i] != after[i] {
			diff = append(diff, i)
			if after[i] != 0 {
				t.Errorf("byte %d became %#x, not 0", i, after[i])
			}
		}
	}
	if len(diff) == 0 || len(diff) > 4 {
		t.Errorf("%d bytes changed (%v), want 1..4", len(diff), diff)
	}
	// A row that is not there, and a column the table does not have, are left alone without an error.
	if err := f.clearTableCell(e, 0xFFFFF1, pidContentCount, PtLong); err != nil {
		t.Errorf("missing row: %v", err)
	}
	if err := f.clearTableCell(e, first, 0x7777, PtLong); err != nil {
		t.Errorf("missing column: %v", err)
	}

	// A column that lies outside the row is an error, not a read or write out of range.
	f2, e2, first2 := bulkTable(t, 100)
	editBlock(t, f2, e2.data, func(pg []byte) []byte {
		tab, err := f2.loadTable(e2)
		if err != nil {
			t.Fatal(err)
		}
		s, _, _ := hnRange(pg, tab.infoHid)
		for i := 0; i < int(pg[s+1]); i++ {
			d := pg[s+tcInfoHdrSize+tcColDescSize*i:]
			if le.Uint32(d) == uint32(pidContentCount)<<16|uint32(PtLong) {
				le.PutUint16(d[4:], 0xFFF0)
			}
		}
		return pg
	})
	if err := f2.clearTableCell(e2, first2, pidContentCount, PtLong); err == nil {
		t.Error("column beyond the row accepted")
	}
}
