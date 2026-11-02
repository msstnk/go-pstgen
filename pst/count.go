package pst

import (
	"fmt"
	"sort"
	"strings"
)

// FolderCount is the number of messages directly in one folder.
type FolderCount struct {
	Path  string // display names joined with "/"; relative to the IPM subtree ("Top of Personal Folders") when below it
	Count int
}

// CountItems returns, for every folder holding at least one message, the number of messages directly in it (subfolders are
// not included, so nothing is counted twice). Folder-associated messages (FAI) are not counted. The file is opened read-only.
// Folders are returned in hierarchy order: depth first, siblings sorted by name.
func CountItems(filePath string) ([]FolderCount, error) {
	f, err := openPST(filePath, false)
	if err != nil {
		return nil, err
	}
	defer f.close()

	subs := map[uint32][]uint32{} // folder -> subfolders
	msgs := map[uint32]int{}      // folder -> messages
	hasRoot := false
	for _, e := range f.nbtEntries() {
		if e.nid == rootNID {
			hasRoot = true
		}
		if e.parent == 0 || e.parent == e.nid {
			continue
		}
		switch e.nid & 0x1F {
		case nidFolder:
			subs[e.parent] = append(subs[e.parent], e.nid)
		case nidMessage:
			msgs[e.parent]++
		}
	}
	if !hasRoot {
		return nil, fmt.Errorf("%s: root folder not found", filePath)
	}
	ipm := f.ipmSubtree()

	names := map[uint32]string{}
	name := func(nid uint32) string {
		n, ok := names[nid]
		if !ok {
			n, _ = f.folderName(nid)
			// keep the output one record per line
			n = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(n)
			names[nid] = n
		}
		return n
	}

	var out []FolderCount
	add := func(nid uint32, path string) {
		if n := msgs[nid]; n > 0 {
			out = append(out, FolderCount{Path: path, Count: n})
		}
	}
	var walk func(nid uint32, path string)
	walk = func(nid uint32, path string) {
		add(nid, path)
		walkKids(nid, path, subs, name, walk)
	}
	// Folders below the IPM subtree are named relative to it; the rest (e.g. Recoverable Items) from the root.
	if ipm != 0 {
		add(ipm, name(ipm))
		walkKids(ipm, "", subs, name, walk)
	}
	for _, k := range sortedKids(subs[rootNID], name) {
		if k != ipm {
			walk(k, name(k))
		}
	}
	return out, nil
}

func sortedKids(kids []uint32, name func(uint32) string) []uint32 {
	sort.Slice(kids, func(i, j int) bool {
		a, b := strings.ToLower(name(kids[i])), strings.ToLower(name(kids[j]))
		if a != b {
			return a < b
		}
		return kids[i] < kids[j]
	})
	return kids
}

func walkKids(nid uint32, path string, subs map[uint32][]uint32, name func(uint32) string, walk func(uint32, string)) {
	for _, k := range sortedKids(subs[nid], name) {
		if path == "" {
			walk(k, name(k))
		} else {
			walk(k, path+"/"+name(k))
		}
	}
}
