package gen

import (
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/msstnk/go-pstgen/internal/config"
)

func TestPlanMessagesDateRange(t *testing.T) {
	end := time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC)
	for _, days := range []int{1, 30, 365} {
		cfg := config.Default()
		cfg.TotalMessages = 2000
		cfg.End = end
		cfg.RangeDays = days
		start := end.Add(-time.Duration(days) * 24 * time.Hour)
		plan, n, _ := planMessages(rand.New(rand.NewSource(1)), cfg, 0, true)
		if n != 2000 {
			t.Fatalf("n = %d", n)
		}
		var minTS, maxTS time.Time
		for _, l := range plan {
			for i, p := range l {
				if p.ts.Before(start) || p.ts.After(end) {
					t.Fatalf("days=%d: %v outside [%v, %v]", days, p.ts, start, end)
				}
				if i > 0 && p.ts.Before(l[i-1].ts) {
					t.Fatalf("days=%d: folder not sorted", days)
				}
				if minTS.IsZero() || p.ts.Before(minTS) {
					minTS = p.ts
				}
				if p.ts.After(maxTS) {
					maxTS = p.ts
				}
			}
		}
		// Samples should cover most of the range, not collapse into a corner
		if span := maxTS.Sub(minTS); span < end.Sub(start)*9/10 {
			t.Errorf("days=%d: span %v covers too little of the range", days, span)
		}
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"user@example.com", "user@example.com"},
		{`a\b/c:d*e?f"g<h>i|j`, "a_b_c_d_e_f_g_h_i_j"},
		{"  name. . ", "name"},
		{"a\x01b", "a_b"},
		{"", "_"},
		{"...", "_"},
	}
	for _, tt := range tests {
		if got := sanitize(tt.in); got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestOutPath(t *testing.T) {
	cfg := config.Config{OutputDir: "out"}
	for part, want := range map[int]string{
		1: filepath.Join("out", "u@x.com.pst"),
		2: filepath.Join("out", "u@x.com-2.pst"),
		3: filepath.Join("out", "u@x.com-3.pst"),
	} {
		if got := outPath(cfg, "u@x.com", part); got != want {
			t.Errorf("part %d: %q, want %q", part, got, want)
		}
	}
}

func TestPlanMessagesReproducible(t *testing.T) {
	cfg := config.Default()
	cfg.TotalMessages = 500
	cfg.End = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	a, _, ta := planMessages(rand.New(rand.NewSource(42)), cfg, 0, true)
	b, _, tb := planMessages(rand.New(rand.NewSource(42)), cfg, 0, true)
	c, _, _ := planMessages(rand.New(rand.NewSource(43)), cfg, 0, true)
	if ta != tb || !reflect.DeepEqual(a, b) {
		t.Error("same seed and end_date produced different plans")
	}
	if reflect.DeepEqual(a, c) {
		t.Error("different seeds produced identical plans")
	}
}

func TestPlanMessagesCount(t *testing.T) {
	cfg := config.Default()
	cfg.End = time.Now().UTC()

	cfg.TotalMessages = 123
	plan, n, total := planMessages(rand.New(rand.NewSource(1)), cfg, 1, true)
	got := 0
	var sum int64
	for _, l := range plan {
		got += len(l)
		for _, p := range l {
			sum += int64(p.size())
		}
	}
	if n != 123 || got != 123 || sum != total {
		t.Errorf("n=%d got=%d sum=%d total=%d, want 123 messages", n, got, sum, total)
	}

	cfg.TotalMessages = 0
	target := int64(50 << 20)
	_, _, total = planMessages(rand.New(rand.NewSource(1)), cfg, target, true)
	if total < target || total >= target+int64(cfg.MaxSizeKB)*1024 {
		t.Errorf("total = %d, want in [%d, target+max message)", total, target)
	}
}

func TestPlanMessagesFolders(t *testing.T) {
	cfg := config.Default()
	cfg.TotalMessages = 20000
	cfg.End = time.Now().UTC()

	plan, _, _ := planMessages(rand.New(rand.NewSource(1)), cfg, 0, false)
	for _, name := range holdFolders {
		if n := len(plan[slices.Index(folderNames, name)]); n != 0 {
			t.Errorf("%s has %d messages without holds, want 0", name, n)
		}
	}

	plan, _, _ = planMessages(rand.New(rand.NewSource(1)), cfg, 0, true)
	for i, w := range folderWeights {
		want := float64(w) / 100
		got := float64(len(plan[i])) / float64(cfg.TotalMessages)
		if math.Abs(got-want) > 0.02 {
			t.Errorf("%s: share %.3f, want about %.2f", folderNames[i], got, want)
		}
	}
}

func TestSampleSizesBounds(t *testing.T) {
	cfg := config.Default()
	cfg.LogVariance, cfg.BodyLogVariance = 3, 3 // wide enough to hit both clamps
	cfg.MinSizeKB = 10
	rng := rand.New(rand.NewSource(1))
	lo, hi := cfg.MinSizeKB*1024, cfg.MaxSizeKB*1024
	for i := 0; i < 20000; i++ {
		body, attach := sampleSizes(rng, cfg)
		if s := (planned{body: body, attach: attach}).size(); s < lo || s > hi || body < 1 {
			t.Fatalf("body %d attach %d: size %d outside [%d, %d]", body, attach, s, lo, hi)
		}
	}
}

func TestSampleSizesMeans(t *testing.T) {
	for _, ratio := range []float64{0, 0.1, 0.3, 1} {
		cfg := config.Default()
		cfg.AttachmentRatio = ratio
		cfg.MeanSizeKB = 100
		cfg.MaxSizeKB = 1 << 20 // avoid clamping bias
		rng := rand.New(rand.NewSource(1))
		const n = 400000
		var sum float64
		var withAttach int
		for i := 0; i < n; i++ {
			body, attach := sampleSizes(rng, cfg)
			sum += float64((planned{body: body, attach: attach}).size())
			if attach > 0 {
				withAttach++
			}
		}
		if mean := sum / n / 1024; math.Abs(mean-100) > 3 {
			t.Errorf("ratio %.1f: mean size %.1f KB, want about 100", ratio, mean)
		}
		if got := float64(withAttach) / n; math.Abs(got-ratio) > 0.01 {
			t.Errorf("ratio %.1f: attachment share %.3f", ratio, got)
		}
	}
}

func TestCheckDuplicates(t *testing.T) {
	ok := [][]string{nil, {"a@x.com"}, {"a@x.com", "b@x.com"}}
	for _, l := range ok {
		if err := checkDuplicates(l); err != nil {
			t.Errorf("%v: unexpected error %v", l, err)
		}
	}
	dup := [][]string{
		{"a@x.com", "a@x.com"},
		{"a@x.com", "A@X.COM"},
		{"a:b", "a?b"}, // both sanitize to a_b
	}
	for _, l := range dup {
		if err := checkDuplicates(l); err == nil {
			t.Errorf("%v: expected duplicate error", l)
		}
	}
}
