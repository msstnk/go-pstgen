package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/msstnk/go-pstgen/pst"
)

func runCmd(args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

// buildPST writes dir/name with Inbox (3 messages) and Recoverable Items/Deletions (2 messages).
func buildPST(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	s, err := pst.Create(p, "test@example.com", []byte("0123456789abcdef"), 1<<20, pst.CryptPermute)
	if err != nil {
		t.Fatal(err)
	}
	msg := func(i int) *pst.Message {
		ts := time.Date(2026, 1, 1, i, 0, 0, 0, time.UTC)
		return &pst.Message{Subject: "m", Body: "body", Sender: pst.Person{Name: "S", Addr: "s@example.com"},
			To: []pst.Person{{Name: "T", Addr: "t@example.com"}}, Submit: ts, Delivery: ts, MessageID: "<1@x>"}
	}
	inbox := s.AddFolder(s.Top, "Inbox")
	ri := s.AddFolder(s.Root, "Recoverable Items")
	del := s.AddFolder(ri, "Deletions")
	for i := 0; i < 3; i++ {
		s.AddMessage(inbox, msg(i))
	}
	for i := 0; i < 2; i++ {
		s.AddMessage(del, msg(i))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestListFlag(t *testing.T) {
	var l listFlag
	l.Set("a")
	l.Set("b")
	if !reflect.DeepEqual([]string(l), []string{"a", "b"}) || l.String() != "a,b" {
		t.Errorf("got %v %q", l, l.String())
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := runCmd("--version")
	if code != 0 || !strings.HasPrefix(out, "go-pstprune "+version) {
		t.Errorf("code=%d out=%q", code, out)
	}
}

// Without both a file and a path (or --count-items) nothing is touched and the status is 2.
func TestUsageErrors(t *testing.T) {
	p := buildPST(t, t.TempDir(), "a.pst")
	before := readFile(t, p)
	for name, args := range map[string][]string{
		"no arguments":      {},
		"file without path": {"--file", p},
		"path without file": {"--path", "Recoverable Items"},
		"unknown flag":      {"--nope"},
		"bad glob":          {"--file", "[", "--path", "x"},
	} {
		if code, _, errOut := runCmd(args...); code != 2 || errOut == "" {
			t.Errorf("%s: code=%d stderr=%q", name, code, errOut)
		}
	}
	if code, _, errOut := runCmd("-h"); code != 0 || !strings.Contains(errOut, "Usage:") {
		t.Errorf("-h: code=%d stderr=%q", code, errOut)
	}
	if !bytes.Equal(before, readFile(t, p)) {
		t.Error("a usage error modified the file")
	}
}

func TestDryRunLeavesFileAlone(t *testing.T) {
	p := buildPST(t, t.TempDir(), "a.pst")
	before := readFile(t, p)
	code, out, errOut := runCmd("--dry-run", "--file", p, "--path", "Recoverable Items")
	if code != 0 || !strings.Contains(out, "would prune 1 folder(s)") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if !bytes.Equal(before, readFile(t, p)) {
		t.Error("--dry-run modified the file")
	}
}

func TestPrune(t *testing.T) {
	p := buildPST(t, t.TempDir(), "a.pst")
	code, out, errOut := runCmd("--file", p, "--path", "Recoverable Items")
	if code != 0 || !strings.Contains(out, "pruned 1 folder(s)") || !strings.Contains(out, "zero-filled") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	got, err := pst.CountItems(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := []pst.FolderCount{{Path: "Inbox", Count: 3}}; !reflect.DeepEqual(got, want) {
		t.Errorf("after prune: %+v", got)
	}
}

func TestPruneNoFill(t *testing.T) {
	p := buildPST(t, t.TempDir(), "a.pst")
	code, out, _ := runCmd("--no-fill", "--file", p, "--path", "Recoverable Items")
	if code != 0 || strings.Contains(out, "zero-filled") {
		t.Errorf("code=%d stdout=%q", code, out)
	}
}

func TestNoMatchIsSkippedNotFailed(t *testing.T) {
	p := buildPST(t, t.TempDir(), "a.pst")
	before := readFile(t, p)
	code, out, _ := runCmd("--file", p, "--path", "Nonexistent")
	if code != 0 || !strings.Contains(out, "no matching folder, skipped") {
		t.Errorf("code=%d stdout=%q", code, out)
	}
	if !bytes.Equal(before, readFile(t, p)) {
		t.Error("a skipped file was modified")
	}
}

// Files after the first arrive as positional arguments when the shell expands a glob; a glob given to --file is expanded here.
// One bad file makes the status 1 but does not stop the others.
func TestMultipleFilesAndFailures(t *testing.T) {
	dir := t.TempDir()
	a := buildPST(t, dir, "a.pst")
	b := buildPST(t, dir, "b.pst")
	bad := filepath.Join(dir, "bad.pst")
	os.WriteFile(bad, bytes.Repeat([]byte("x"), 1024), 0o644)
	missing := filepath.Join(dir, "missing.pst")

	code, out, errOut := runCmd("--file", a, "--path", "Recoverable Items", bad, b, missing)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "bad.pst") || !strings.Contains(errOut, "missing.pst") {
		t.Errorf("stderr = %q", errOut)
	}
	for _, p := range []string{a, b} {
		if !strings.Contains(out, p+": pruned") {
			t.Errorf("%s was not pruned: %q", p, out)
		}
	}

	c := buildPST(t, dir, "c.pst")
	code, out, _ = runCmd("--file", filepath.Join(dir, "c*.pst"), "--path", "Recoverable Items")
	if code != 0 || !strings.Contains(out, c+": pruned") {
		t.Errorf("glob: code=%d stdout=%q", code, out)
	}
}

func TestCountItems(t *testing.T) {
	dir := t.TempDir()
	a := buildPST(t, dir, "a.pst")
	before := readFile(t, a)
	bad := filepath.Join(dir, "bad.pst")
	os.WriteFile(bad, []byte("nope"), 0o644)

	// --path is ignored, and a file that cannot be read gives status 1 without hiding the others.
	code, out, errOut := runCmd("--count-items", "--path", "ignored", "--file", a, bad)
	want := a + "\tInbox\t3\n" + a + "\tRecoverable Items/Deletions\t2\n"
	if code != 1 || out != want || !strings.Contains(errOut, "bad.pst") {
		t.Errorf("code=%d\nstdout=%q\nwant   %q\nstderr=%q", code, out, want, errOut)
	}
	if !bytes.Equal(before, readFile(t, a)) {
		t.Error("--count-items modified the file")
	}
	if code, _, _ := runCmd("--count-items", "--file", a); code != 0 {
		t.Errorf("--count-items without --path: code=%d", code)
	}
}
