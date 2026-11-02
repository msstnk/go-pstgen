package gen

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/msstnk/go-pstgen/internal/config"
)

// A mailbox split into several PSTs gets rows for each part, and an existing summary file is never overwritten.
func TestRunWritesSummary(t *testing.T) {
	dir := t.TempDir()
	seed := int64(1)
	cfg := config.Default()
	cfg.OutputDir = dir
	cfg.Mailboxes = []string{"a@example.com", "b@example.com"}
	cfg.MailboxMeanMB, cfg.MailboxStddevMB = 3, 0.1
	cfg.Workers = 2
	cfg.SplitSizeMB = 1
	cfg.Seed = &seed

	files, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 {
		t.Fatalf("expected a split mailbox, got %v", files)
	}
	out := filepath.Join(t.TempDir(), "summary.tsv")
	if err := CheckSummaryPath(out); err != nil {
		t.Fatal(err)
	}
	if err := WriteSummary(out, files); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	perFile := map[string]int{}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		cols := strings.Split(line, "\t")
		if len(cols) != 3 {
			t.Fatalf("want 3 columns: %q", line)
		}
		n, err := strconv.Atoi(cols[2])
		if err != nil || n <= 0 {
			t.Fatalf("bad count in %q", line)
		}
		perFile[cols[0]] += n
	}
	for _, f := range files {
		if perFile[f] == 0 {
			t.Errorf("%s missing from summary", f)
		}
	}
	if err := CheckSummaryPath(out); err == nil {
		t.Error("existing summary must be refused")
	}
	if err := WriteSummary(out, files); err == nil {
		t.Error("existing summary must not be overwritten")
	}
}
