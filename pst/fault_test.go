package pst

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var errInjected = errors.New("injected failure")

// faultFile wraps a real file and fails the n-th call (1-based) of the chosen operation; n == 0 disables it.
type faultFile struct {
	*os.File
	op    string // "write", "writeat", "truncate" or "close"
	n     int
	calls map[string]int
}

func (f *faultFile) hit(op string) bool {
	f.calls[op]++
	return f.op == op && f.calls[op] == f.n
}

func (f *faultFile) Write(b []byte) (int, error) {
	if f.hit("write") {
		return 0, errInjected
	}
	return f.File.Write(b)
}

func (f *faultFile) WriteAt(b []byte, off int64) (int, error) {
	if f.hit("writeat") {
		return 0, errInjected
	}
	return f.File.WriteAt(b, off)
}

func (f *faultFile) Truncate(size int64) error {
	if f.hit("truncate") {
		return errInjected
	}
	return f.File.Truncate(size)
}

func (f *faultFile) Close() error {
	err := f.File.Close()
	if f.hit("close") {
		return errInjected
	}
	return err
}

func newFaultStore(t *testing.T, op string, n int) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.pst")
	of, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	ff := &faultFile{File: of, op: op, n: n, calls: map[string]int{}}
	w := newWriter(ff, path, 4096, CryptNone)
	return createOn(w, "test@example.com", []byte("0123456789abcdef")), path
}

// Every failing step of Finish is reported by Store.Close, and Abort then removes the incomplete file.
func TestFinishFailures(t *testing.T) {
	// Writes with a 4 KiB buffer happen on Flush; WriteAt is called per AMap/PMap/FMap page, the DList page and the header.
	// Find out how many WriteAt calls a successful run makes, then fail each of them in turn.
	s, path := newFaultStore(t, "", 0)
	s.AddMessage(s.AddFolder(s.Top, "Inbox"), testMessage(0))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	checkFile(t, path)
	total := s.w.f.(*faultFile).calls["writeat"]
	if total < 3 {
		t.Fatalf("only %d WriteAt calls in a successful Finish", total)
	}

	cases := []struct {
		op string
		n  int
	}{{"write", 1}, {"truncate", 1}, {"close", 1}}
	for n := 1; n <= total; n++ {
		cases = append(cases, struct {
			op string
			n  int
		}{"writeat", n})
	}
	for _, c := range cases {
		s, path := newFaultStore(t, c.op, c.n)
		s.AddMessage(s.AddFolder(s.Top, "Inbox"), testMessage(0))
		err := s.Close()
		if !errors.Is(err, errInjected) {
			t.Errorf("%s #%d: Close = %v, want the injected error", c.op, c.n, err)
		}
		s.Abort()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s #%d: file still exists after Abort: %v", c.op, c.n, err)
		}
	}
}
