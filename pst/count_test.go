package pst

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCountItems(t *testing.T) {
	p := buildPrunable(t, CryptPermute)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CountItems(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []FolderCount{
		{"Inbox", 100},
		{"Recoverable Items", 1},
		{"Recoverable Items/Deletions", 120},
		{"Recoverable Items/DiscoveryHolds", 1},
		{"Recoverable Items/DiscoveryHolds/Deep", 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("CountItems modified the file")
	}
}

// A parent's count excludes its subfolders, and empty folders are left out.
func TestCountItemsNested(t *testing.T) {
	p := t.TempDir() + "/n.pst"
	s, err := Create(p, "test@example.com", []byte("0123456789abcdef"), 1<<20, CryptNone)
	if err != nil {
		t.Fatal(err)
	}
	inbox := s.AddFolder(s.Top, "Inbox")
	fav := s.AddFolder(inbox, "MyFav")
	s.AddFolder(s.Top, "Empty")
	for i := 0; i < 3; i++ {
		s.AddMessage(fav, testMessage(i))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := CountItems(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := []FolderCount{{"Inbox/MyFav", 3}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestCountItemsErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := CountItems(filepath.Join(dir, "missing.pst")); err == nil {
		t.Error("missing file: expected error")
	}
	bad := filepath.Join(dir, "bad.pst")
	if err := os.WriteFile(bad, bytes.Repeat([]byte("x"), 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CountItems(bad); err == nil {
		t.Error("non-PST file: expected error")
	}
}
