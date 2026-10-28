// Package config loads and validates config.json.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/msstnk/go-pstgen/pst"
)

// Config holds the contents of config.json.
type Config struct {
	OutputDir       string   `json:"output_dir"`
	Mailboxes       []string `json:"mailboxes"`
	MailboxMeanMB   float64  `json:"mailbox_mean_mb"`
	MailboxStddevMB float64  `json:"mailbox_stddev_mb"`
	// MailboxLogVariance, when set, makes mailbox sizes log-normal with this log variance and mailbox_stddev_mb is ignored; nil = normal distribution
	MailboxLogVariance *float64 `json:"mailbox_log_variance"`
	Workers            int      `json:"workers"`
	TotalMessages      int      `json:"total_messages"`
	MinSizeKB          int      `json:"min_size_kb"`
	MaxSizeKB          int      `json:"max_size_kb"`
	MeanSizeKB         int      `json:"mean_size_kb"`
	LogVariance        float64  `json:"log_variance"`
	AttachmentRatio    float64  `json:"attachment_ratio"`
	Seed               *int64   `json:"seed"`
	CryptMethod        string   `json:"crypt_method"`  // none / permute (default) / cyclic
	SplitSizeMB        float64  `json:"split_size_mb"` // values <= 0 disable splitting
	HoldsRatio         float64  `json:"holds_ratio"`   // fraction of mailboxes that have the hold folders (Purges, Versions, DiscoveryHolds, SubstrateHolds)
	EndDate            string   `json:"end_date"`      // latest message date: RFC 3339 or YYYY-MM-DD (end of that day, UTC); empty = run time
	RangeDays          int      `json:"range_days"`    // messages are dated within this many days before end_date

	Crypt pst.CryptMethod `json:"-"`
	End   time.Time       `json:"-"`
}

// Default returns the configuration used for keys omitted from config.json.
func Default() Config {
	return Config{
		OutputDir: "./output", Mailboxes: []string{"user01@example.com"},
		MailboxMeanMB: 1000, MailboxStddevMB: 200, Workers: runtime.NumCPU(),
		MinSizeKB: 10, MaxSizeKB: 10240, MeanSizeKB: 150, LogVariance: 0.5, AttachmentRatio: 0.3,
		SplitSizeMB: 10240, HoldsRatio: 1.0, RangeDays: 365,
	}
}

// Load reads and validates a config file on top of the defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config not found: %w", err)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.MinSizeKB < 1 || cfg.MaxSizeKB < cfg.MinSizeKB {
		return cfg, fmt.Errorf("invalid min_size_kb/max_size_kb")
	}
	if cfg.MailboxLogVariance != nil && *cfg.MailboxLogVariance <= 0 {
		return cfg, fmt.Errorf("invalid mailbox_log_variance (must be greater than 0)")
	}
	if cfg.HoldsRatio < 0 || cfg.HoldsRatio > 1 {
		return cfg, fmt.Errorf("invalid holds_ratio (must be between 0 and 1)")
	}
	if cfg.RangeDays < 1 {
		return cfg, fmt.Errorf("invalid range_days (must be at least 1)")
	}
	if cfg.EndDate == "" {
		cfg.End = time.Now().UTC()
	} else if t, err := time.Parse(time.RFC3339, cfg.EndDate); err == nil {
		cfg.End = t.UTC()
	} else if t, err := time.Parse("2006-01-02", cfg.EndDate); err == nil {
		cfg.End = t.Add(24*time.Hour - time.Second)
	} else {
		return cfg, fmt.Errorf("invalid end_date %q (RFC 3339 or YYYY-MM-DD)", cfg.EndDate)
	}
	var ok bool
	if cfg.Crypt, ok = pst.ParseCryptMethod(cfg.CryptMethod); !ok {
		return cfg, fmt.Errorf("invalid crypt_method %q (none / permute / cyclic)", cfg.CryptMethod)
	}
	return cfg, nil
}
