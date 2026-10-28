package config

import (
	"github.com/msstnk/go-pstgen/pst"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigDateDefaults(t *testing.T) {
	before := time.Now().UTC()
	cfg, err := Load(writeConfig(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RangeDays != 365 {
		t.Errorf("RangeDays = %d, want 365", cfg.RangeDays)
	}
	if cfg.End.Before(before) || cfg.End.After(time.Now().UTC()) {
		t.Errorf("end = %v, want the current time", cfg.End)
	}
}

func TestLoadConfigEndDate(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
	}{
		{"2026-03-31", time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC)},
		{"2026-03-31T12:00:00Z", time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)},
		{"2026-03-31T21:00:00+09:00", time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		cfg, err := Load(writeConfig(t, `{"end_date":"`+tt.in+`"}`))
		if err != nil {
			t.Errorf("%s: %v", tt.in, err)
			continue
		}
		if !cfg.End.Equal(tt.want) {
			t.Errorf("%s: end = %v, want %v", tt.in, cfg.End, tt.want)
		}
	}
}

func TestLoadConfigInvalidDates(t *testing.T) {
	for _, body := range []string{
		`{"end_date":"yesterday"}`,
		`{"end_date":"2026/03/31"}`,
		`{"range_days":0}`,
		`{"range_days":-5}`,
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: expected error", body)
		} else if !strings.Contains(err.Error(), "end_date") && !strings.Contains(err.Error(), "range_days") {
			t.Errorf("%s: unexpected error %v", body, err)
		}
	}
}

func TestLoadConfigValidation(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{"unknown key", `{"nope":1}`},
		{"invalid JSON", `{`},
		{"min above max", `{"min_size_kb":100,"max_size_kb":10}`},
		{"min below 1", `{"min_size_kb":0}`},
		{"holds above 1", `{"holds_ratio":1.5}`},
		{"holds below 0", `{"holds_ratio":-0.1}`},
		{"log variance zero", `{"mailbox_log_variance":0}`},
		{"log variance negative", `{"mailbox_log_variance":-0.5}`},
		{"bad crypt", `{"crypt_method":"rot13"}`},
	}
	for _, tt := range tests {
		if _, err := Load(writeConfig(t, tt.body)); err == nil {
			t.Errorf("%s: expected error", tt.name)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing file: expected error")
	}
}

func TestLoadConfigBOMAndWorkers(t *testing.T) {
	cfg, err := Load(writeConfig(t, "\xef\xbb\xbf"+`{"workers":0,"crypt_method":"cyclic"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers != 1 {
		t.Errorf("Workers = %d, want 1", cfg.Workers)
	}
}

func TestLoadConfigMailboxLogVariance(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{}`))
	if err != nil || cfg.MailboxLogVariance != nil {
		t.Fatalf("default: err=%v, log variance=%v, want nil", err, cfg.MailboxLogVariance)
	}
	cfg, err = Load(writeConfig(t, `{"mailbox_log_variance":null}`))
	if err != nil || cfg.MailboxLogVariance != nil {
		t.Fatalf("null: err=%v, log variance=%v, want nil", err, cfg.MailboxLogVariance)
	}
	cfg, err = Load(writeConfig(t, `{"mailbox_log_variance":1.2}`))
	if err != nil || cfg.MailboxLogVariance == nil || *cfg.MailboxLogVariance != 1.2 {
		t.Fatalf("set: err=%v, log variance=%v, want 1.2", err, cfg.MailboxLogVariance)
	}
}

func TestMeanBodyAttach(t *testing.T) {
	cfg := Default()
	cfg.MeanSizeKB, cfg.AttachmentRatio = 100, 0.1
	b, a := cfg.MeanBodyAttach()
	if b != 10*1024 || pst.MessageBaseSize+b+0.1*(a+pst.AttachmentSizeOverhead) != 100*1024 {
		t.Errorf("body %v attach %v does not add up to the mean", b, a)
	}
	cfg.MeanSizeKB = 8
	cfg.AttachmentRatio = 0
	if b, a := cfg.MeanBodyAttach(); a != 0 || b != float64(8*1024-pst.MessageBaseSize) {
		t.Errorf("ratio 0: body %v attach %v", b, a)
	}
}

func TestLoadConfigAttachmentRatio(t *testing.T) {
	if _, err := Load(writeConfig(t, `{"mean_size_kb": 20, "body_mean_kb": 20}`)); err == nil {
		t.Error("body_mean_kb >= mean_size_kb: expected error")
	}
	if _, err := Load(writeConfig(t, `{"mean_size_kb": 8, "attachment_ratio": 0}`)); err != nil {
		t.Errorf("ratio 0 ignores body_mean_kb: %v", err)
	}
	if _, err := Load(writeConfig(t, `{"attachment_ratio": 1.5}`)); err == nil {
		t.Error("attachment_ratio > 1: expected error")
	}
}

func TestWriteTemplate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := WriteTemplate(p); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("template does not load: %v", err)
	}
	if len(cfg.Mailboxes) != 24 || cfg.MailboxMeanMB != 100 || cfg.Workers != 8 {
		t.Errorf("unexpected template: %+v", cfg)
	}
	if err := WriteTemplate(p); err == nil {
		t.Error("second write: expected error, not overwriting")
	}
}
