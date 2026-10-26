package pst

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ErrNoMatch is returned by Prune when no folder matches any of the paths.
var ErrNoMatch = errors.New("no folder matches")

// PruneOptions controls Prune.
type PruneOptions struct {
	Paths  []string // folder paths; "/" separated, each segment is a case-insensitive glob (path.Match)
	NoFill bool     // keep the detached blocks as they are instead of zero-filling them
	DryRun bool     // analyze only; the file is opened read-only
}

// PruneResult summarizes what Prune did (or, with DryRun, would do).
type PruneResult struct {
	Folders []string // matched folders (the roots of the pruned subtrees)
	Nodes   int      // NBT entries detached
	Items   int      // messages directly in folders matched by a trailing "*"
	Rows    int      // table rows removed (hierarchy, contents and associated contents tables)
	Blocks  int      // blocks zero-filled and dropped from the BBT
	Bytes   int64    // size of those blocks including trailers
	Shared  int      // blocks kept because a retained node still references them
}

var (
	rootNID     = uint32(nidRootFolder)
	tableTypes  = []uint32{nidHierarchy, nidContents, nidAssoc, 0x10}
	errUnpruned = errors.New("refusing to prune")
)

type matched struct {
	nid  uint32
	path string
}

// Prune detaches every folder matching opt.Paths, with all subfolders, messages and their tables, from the NBT of the
// PST at filePath. Unless opt.NoFill is set, the blocks only those nodes used are filled with 0x00 and dropped from the BBT.
// The matched folders are also removed from their parents' hierarchy tables. The file is modified in place.
func Prune(filePath string, opt PruneOptions) (*PruneResult, error) {
	pats, err := splitPaths(opt.Paths)
	if err != nil {
		return nil, err
	}
	f, err := openPST(filePath, !opt.DryRun)
	if err != nil {
		return nil, err
	}
	defer f.close()

	entries := f.nbtEntries()
	byParent := map[uint32][]nbtEntry{}
	parentOf := map[uint32]uint32{}
	for _, e := range entries {
		if e.parent != 0 && e.parent != e.nid {
			byParent[e.parent] = append(byParent[e.parent], e)
		}
		if e.nid&0x1F == nidFolder {
			parentOf[e.nid] = e.parent
		}
	}
	if _, ok := parentOf[rootNID]; !ok {
		return nil, fmt.Errorf("%s: root folder not found", filePath)
	}
	ipm := f.ipmSubtree()

	// ---- resolve paths ----
	names := map[uint32]string{}
	name := func(nid uint32) string {
		n, ok := names[nid]
		if !ok {
			n, _ = f.folderName(nid) // an unreadable name simply never matches
			names[nid] = n
		}
		return n
	}
	found := map[uint32]string{}
	contents := map[uint32]bool{} // folders whose own items go too (a trailing "*" matches items as well as folders)
	for _, segs := range pats {
		for _, base := range []uint32{rootNID, ipm} {
			if base == 0 {
				continue
			}
			cur := []matched{{base, ""}}
			for i, seg := range segs {
				var next []matched
				if i == len(segs)-1 && seg == "*" {
					for _, c := range cur {
						contents[c.nid] = true
					}
				}
				for _, c := range cur {
					for _, e := range byParent[c.nid] {
						if e.nid&0x1F != nidFolder {
							continue
						}
						if ok, _ := path.Match(seg, strings.ToLower(name(e.nid))); ok {
							next = append(next, matched{e.nid, c.path + "/" + name(e.nid)})
						}
					}
				}
				cur = next
			}
			for _, m := range cur {
				if _, dup := found[m.nid]; !dup {
					found[m.nid] = strings.TrimPrefix(m.path, "/")
				}
			}
		}
	}
	var tops []uint32
	for nid := range found {
		if nid == rootNID || nid == ipm {
			return nil, fmt.Errorf("%w %q", errUnpruned, found[nid])
		}
		nested := false
		for p := parentOf[nid]; p != 0 && p != rootNID; p = parentOf[p] {
			if _, ok := found[p]; ok {
				nested = true
				break
			}
		}
		if !nested {
			tops = append(tops, nid)
		}
	}
	sort.Slice(tops, func(i, j int) bool { return found[tops[i]] < found[tops[j]] })
	res := &PruneResult{}
	for _, nid := range tops {
		res.Folders = append(res.Folders, found[nid])
	}

	// ---- everything under the matched folders ----
	gone := map[uint32]nbtEntry{} // detached nodes
	byNID := func(nid uint32) (nbtEntry, bool) { return f.node(nid) }
	var addFolder func(nid uint32)
	addFolder = func(nid uint32) {
		for _, t := range tableTypes {
			if e, ok := byNID(nid&^0x1F | t); ok {
				gone[e.nid] = e
			}
		}
		if e, ok := byNID(nid); ok {
			gone[e.nid] = e
		}
		for _, c := range byParent[nid] {
			if _, dup := gone[c.nid]; dup {
				continue
			}
			gone[c.nid] = c
			if c.nid&0x1F == nidFolder {
				addFolder(c.nid)
			}
		}
	}
	for _, nid := range tops {
		addFolder(nid)
	}

	// The items (messages, FAI) directly in the folders matched by a trailing "*".
	rowsByTable := map[uint32]map[uint32]bool{}
	addRow := func(table, id uint32) {
		if rowsByTable[table] == nil {
			rowsByTable[table] = map[uint32]bool{}
		}
		rowsByTable[table][id] = true
	}
	emptiedItems := map[uint32]bool{}
	for p := range contents {
		if _, ok := gone[p]; ok {
			continue
		}
		for _, c := range byParent[p] {
			if _, ok := gone[c.nid]; ok || c.nid&0x1F == nidFolder || c.nid&0x1F == 0x03 {
				continue
			}
			gone[c.nid] = c
			res.Items++
			emptiedItems[p] = true
			switch c.nid & 0x1F {
			case nidMessage:
				addRow(p&^0x1F|nidContents, c.nid)
			case 0x08: // folder-associated message
				addRow(p&^0x1F|nidAssoc, c.nid)
			}
		}
	}
	if len(tops) == 0 && res.Items == 0 {
		return nil, ErrNoMatch
	}
	res.Nodes = len(gone)

	// ---- table rows of the parents ----
	for _, nid := range tops {
		addRow(parentOf[nid]&^0x1F|nidHierarchy, nid)
	}
	var dropped []uint64
	for t, ids := range rowsByTable {
		e, ok := byNID(t)
		if !ok || e.data == 0 {
			continue
		}
		n, drop, err := f.removeTableRows(e, ids)
		if err != nil {
			return nil, fmt.Errorf("%s: table %#x: %w", filePath, t, err)
		}
		dropped = append(dropped, drop...)
		res.Rows += n
	}
	// Folders that lost all their items have a content count of zero: fix the folder properties and the folder's row
	// in its parent's hierarchy table.
	for p := range emptiedItems {
		for _, tag := range []uint16{0x3602, 0x3603, 0x3617} { // content count, unread count, FAI count
			if err := f.clearPCValue(p, tag); err != nil {
				return nil, fmt.Errorf("%s: folder %#x: %w", filePath, p, err)
			}
			if e, ok := byNID(parentOf[p]&^0x1F | nidHierarchy); ok && e.data != 0 && p != rootNID {
				if err := f.clearTableCell(e, p, tag, PtLong); err != nil {
					return nil, fmt.Errorf("%s: hierarchy table of %#x: %w", filePath, parentOf[p], err)
				}
			}
		}
	}
	// A parent that lost its last subfolder no longer has subfolders: clear the flag in its own properties
	// and in its row of the grandparent's hierarchy table.
	emptied := map[uint32]bool{}
	for _, nid := range tops {
		emptied[parentOf[nid]] = true
	}
	for p := range emptied {
		if _, ok := gone[p]; ok {
			continue
		}
		kept := false
		for _, c := range byParent[p] {
			if _, ok := gone[c.nid]; !ok && c.nid&0x1F == nidFolder {
				kept = true
			}
		}
		if kept {
			continue
		}
		if err := f.clearPCValue(p, 0x360A); err != nil {
			return nil, fmt.Errorf("%s: folder %#x: %w", filePath, p, err)
		}
		if p == rootNID {
			continue
		}
		if e, ok := byNID(parentOf[p]&^0x1F | nidHierarchy); ok && e.data != 0 {
			if err := f.clearTableCell(e, p, 0x360A, PtBool); err != nil {
				return nil, fmt.Errorf("%s: hierarchy table of %#x: %w", filePath, parentOf[p], err)
			}
		}
	}

	// ---- blocks used only by the detached nodes ----
	cand := map[uint64]struct{}{}
	for _, e := range gone {
		if err := f.walkNode(e, func(b uint64) { cand[b] = struct{}{} }); err != nil {
			return nil, fmt.Errorf("%s: node %#x: %w", filePath, e.nid, err)
		}
	}
	for _, e := range entries {
		if _, ok := gone[e.nid]; ok {
			continue
		}
		err := f.walkNode(e, func(b uint64) {
			if _, ok := cand[b]; ok {
				delete(cand, b)
				res.Shared++
			}
		})
		if err != nil {
			return nil, fmt.Errorf("%s: node %#x: %w", filePath, e.nid, err)
		}
	}
	for _, b := range dropped { // matrix blocks released by the table rewrite
		cand[b] = struct{}{}
	}
	var zero []blockRef
	delBlocks := map[uint64]struct{}{}
	if !opt.NoFill {
		for bid := range cand {
			r, err := f.blockRef(bid)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", filePath, err)
			}
			zero = append(zero, r)
			delBlocks[bid] = struct{}{}
			res.Bytes += int64(r.total())
		}
		sort.Slice(zero, func(i, j int) bool { return zero[i].ib < zero[j].ib })
		res.Blocks = len(zero)
	}
	if opt.DryRun {
		return res, nil
	}

	// ---- apply ----
	delNodes := make(map[uint64]struct{}, len(gone))
	for nid := range gone {
		delNodes[uint64(nid)] = struct{}{}
	}
	if !f.nbt.remove(delNodes) || !f.bbt.remove(delBlocks) {
		return nil, fmt.Errorf("%s: pruning would empty a B-Tree", filePath)
	}
	for bid, data := range f.pending {
		if err := f.writeBlock(bid, data); err != nil {
			return nil, fmt.Errorf("%s: %w", filePath, err)
		}
	}
	for _, r := range zero {
		if err := f.zero(r.ib, r.total()); err != nil {
			return nil, fmt.Errorf("%s: %w", filePath, err)
		}
	}
	// BBT first, then NBT last: until the NBT page is rewritten the old tree still resolves.
	if err := f.flush(f.bbt, !opt.NoFill); err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	if err := f.flush(f.nbt, !opt.NoFill); err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	if err := f.f.Sync(); err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	return res, nil
}

// splitPaths splits and lower-cases glob paths and validates the patterns.
func splitPaths(paths []string) ([][]string, error) {
	var out [][]string
	for _, p := range paths {
		var segs []string
		for _, s := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
			if s == "" {
				continue
			}
			s = strings.ToLower(s)
			if _, err := path.Match(s, ""); err != nil {
				return nil, fmt.Errorf("bad path %q: %w", p, err)
			}
			segs = append(segs, s)
		}
		if len(segs) == 0 {
			return nil, fmt.Errorf("empty path %q", p)
		}
		out = append(out, segs)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no path given")
	}
	return out, nil
}

// ipmSubtree returns the NID of the IPM subtree folder ("Top of Personal Folders") from the message store, or 0.
func (f *File) ipmSubtree() uint32 {
	h, err := f.heapNode(nidMessageStore)
	if err != nil {
		return 0
	}
	b, ok, err := h.pcBytes(0x35E0) // PidTagIpmSubTreeEntryId
	if err != nil || !ok || len(b) < 24 {
		return 0
	}
	return le.Uint32(b[20:])
}
