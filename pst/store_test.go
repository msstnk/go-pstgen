package pst

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.pst")
	s, err := Create(path, "test@example.com", []byte("0123456789abcdef"), 1<<20, CryptNone)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

// Plain folders have no container class, may share a name under different parents (or even under one parent), and nest.
func TestAddPlainFolder(t *testing.T) {
	s, path := newTestStore(t)
	ri := s.AddPlainFolder(s.Top, "Recoverable Items")
	del := s.AddPlainFolder(ri, "Deletions")
	deep := s.AddPlainFolder(del, "Deep")
	dupA := s.AddPlainFolder(ri, "Dup")
	dupB := s.AddPlainFolder(ri, "Dup") // same name, same parent
	other := s.AddPlainFolder(del, "Dup")
	inbox := s.AddFolder(s.Top, "Dup") // same name as the plain ones, but with a class

	seen := map[uint32]bool{}
	for _, f := range []*Folder{ri, del, deep, dupA, dupB, other, inbox} {
		if seen[f.NID] {
			t.Errorf("duplicate NID %#x for %q", f.NID, f.Name)
		}
		seen[f.NID] = true
		if f.NID&nidTypeMask != nidFolder {
			t.Errorf("%q: NID %#x is not a folder NID", f.Name, f.NID)
		}
	}
	for _, f := range []*Folder{ri, del, deep, dupA, dupB, other} {
		if f.Class != "" {
			t.Errorf("%q: Class = %q, want none", f.Name, f.Class)
		}
	}
	if inbox.Class != "IPF.Note" {
		t.Errorf("AddFolder Class = %q", inbox.Class)
	}
	if deep.Parent != del || del.Parent != ri || ri.Parent != s.Top {
		t.Error("Parent links are wrong")
	}
	if want := []*Folder{del, dupA, dupB}; !reflect.DeepEqual(ri.Children, want) {
		t.Errorf("Recoverable Items children = %v", ri.Children)
	}

	s.AddMessage(deep, testMessage(0))
	s.AddMessage(dupA, testMessage(1))
	s.AddMessage(dupB, testMessage(2))
	s.AddMessage(dupB, testMessage(3))
	s.AddMessage(other, testMessage(4))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	checkFile(t, path)
	got, err := CountItems(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []FolderCount{
		{"Recoverable Items/Deletions/Deep", 1},
		{"Recoverable Items/Deletions/Dup", 1},
		{"Recoverable Items/Dup", 1},
		{"Recoverable Items/Dup", 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseCryptMethod(t *testing.T) {
	for in, want := range map[string]CryptMethod{
		"none": CryptNone, "": CryptPermute, "permute": CryptPermute, "compressible": CryptPermute,
		"cyclic": CryptCyclic, "high": CryptCyclic,
	} {
		if got, ok := ParseCryptMethod(in); !ok || got != want {
			t.Errorf("ParseCryptMethod(%q) = %v, %v; want %v, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"None", "rot13", " permute"} {
		if _, ok := ParseCryptMethod(in); ok {
			t.Errorf("ParseCryptMethod(%q) accepted", in)
		}
	}
}

func TestFolderCountTSV(t *testing.T) {
	got := FolderCount{"Inbox/MyFav", 3}.TSV("a.pst")
	if want := "a.pst\tInbox/MyFav\t3"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Store.Abort removes the unfinished file.
func TestStoreAbort(t *testing.T) {
	s, path := newTestStore(t)
	s.AddMessage(s.AddFolder(s.Top, "Inbox"), testMessage(0))
	s.Abort()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after Abort: %v", err)
	}
}

// EstimatedSize grows as messages are added and does not fall below the final size.
func TestEstimatedSize(t *testing.T) {
	s, path := newTestStore(t)
	inbox := s.AddFolder(s.Top, "Inbox")
	prev := s.EstimatedSize()
	for i := 0; i < 200; i++ {
		s.AddMessage(inbox, testMessage(i))
		if i%50 == 49 {
			cur := s.EstimatedSize()
			if cur <= prev {
				t.Errorf("EstimatedSize did not grow after %d messages: %d -> %d", i+1, prev, cur)
			}
			prev = cur
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if prev < fi.Size() {
		t.Errorf("estimate %d is below the final size %d", prev, fi.Size())
	}
}
