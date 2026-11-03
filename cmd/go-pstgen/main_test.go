package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCmd(args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

// writeConfig writes a config that generates one small mailbox into dir/out and returns its path.
func writeConfig(t *testing.T, dir string, extra map[string]any) string {
	t.Helper()
	cfg := map[string]any{
		"output_dir": filepath.Join(dir, "out"), "mailboxes": []string{"a@example.com"},
		"mailbox_mean_mb": 0.3, "mailbox_stddev_mb": 0, "workers": 1, "seed": 1,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVersion(t *testing.T) {
	code, out, _ := runCmd("--version")
	if code != 0 || !strings.HasPrefix(out, "go-pstgen "+version) {
		t.Errorf("code=%d out=%q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	if code, _, errOut := runCmd("--nope"); code != 2 || !strings.Contains(errOut, "nope") {
		t.Errorf("unknown flag: code=%d stderr=%q", code, errOut)
	}
	code, _, errOut := runCmd("-h")
	if code != 0 || !strings.Contains(errOut, "Usage:") {
		t.Errorf("-h: code=%d stderr=%q", code, errOut)
	}
}

func TestRunGenerates(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, nil)
	sum := filepath.Join(dir, "summary.tsv")
	code, out, errOut := runCmd("--config", cfg, "--summary", sum)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "wrote "+sum) {
		t.Errorf("stdout = %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "a@example.com.pst")); err != nil {
		t.Error(err)
	}
	tsv, err := os.ReadFile(sum)
	if err != nil || !strings.Contains(string(tsv), "a@example.com.pst\t") {
		t.Errorf("summary: %v %q", err, tsv)
	}

	// The summary exists now: the next run must fail before generating anything.
	before, _ := os.ReadDir(filepath.Join(dir, "out"))
	code, _, errOut = runCmd("--config", cfg, "--summary", sum)
	after, _ := os.ReadDir(filepath.Join(dir, "out"))
	if code != 1 || !strings.Contains(errOut, "already exists") || len(before) != len(after) {
		t.Errorf("existing summary: code=%d stderr=%q", code, errOut)
	}
}

func TestRunNoSummary(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runCmd("--config", writeConfig(t, dir, nil), "--summary", "")
	if code != 0 || strings.Contains(out, "wrote") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestRunConfigErrors(t *testing.T) {
	dir := t.TempDir()
	if code, _, errOut := runCmd("--config", filepath.Join(dir, "missing.json")); code != 1 || errOut == "" {
		t.Errorf("missing config: code=%d stderr=%q", code, errOut)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"mailboxes": 1`), 0o644)
	if code, _, errOut := runCmd("--config", bad); code != 1 || errOut == "" {
		t.Errorf("malformed config: code=%d stderr=%q", code, errOut)
	}
	// Duplicate mailboxes are rejected by Run: reported, exit status 1, nothing summarized.
	dup := writeConfig(t, dir, map[string]any{"mailboxes": []string{"a@example.com", "a@example.com"}})
	sum := filepath.Join(dir, "s.tsv")
	if code, _, errOut := runCmd("--config", dup, "--summary", sum); code != 1 || errOut == "" {
		t.Errorf("duplicate mailboxes: code=%d stderr=%q", code, errOut)
	}
	if _, err := os.Stat(sum); err == nil {
		t.Error("summary written although Run failed")
	}
}

// --init writes config.json in the current directory and refuses to overwrite it.
func TestInit(t *testing.T) {
	t.Chdir(t.TempDir())
	if code, out, errOut := runCmd("--init"); code != 0 || !strings.Contains(out, "wrote config.json") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if _, err := os.Stat("config.json"); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCmd("--init"); code != 1 || !strings.Contains(errOut, "already exists") {
		t.Errorf("second --init: code=%d stderr=%q", code, errOut)
	}
}
