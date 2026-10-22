package gen

import (
	"math"
	"math/rand"
	"slices"
	"testing"

	"github.com/msstnk/go-pstgen/internal/config"
)

func TestSampleMailboxSizeLogNormalKeepsMean(t *testing.T) {
	v := 0.8
	cfg := config.Default()
	cfg.MailboxLogVariance = &v
	rng := rand.New(rand.NewSource(1))
	const n = 400000
	var sum float64
	for i := 0; i < n; i++ {
		sum += sampleMailboxSize(rng, cfg)
	}
	want := cfg.MailboxMeanMB * 1024 * 1024
	if got := sum / n; math.Abs(got-want)/want > 0.02 {
		t.Fatalf("mean = %.0f, want ~%.0f", got, want)
	}
}

func TestSampleMailboxSizeNormalDefault(t *testing.T) {
	cfg := config.Default()
	rng := rand.New(rand.NewSource(1))
	const n = 200000
	var sum, sq float64
	for i := 0; i < n; i++ {
		x := sampleMailboxSize(rng, cfg) / 1024 / 1024
		sum += x
		sq += x * x
	}
	mean := sum / n
	sd := math.Sqrt(sq/n - mean*mean)
	if math.Abs(mean-cfg.MailboxMeanMB) > 5 || math.Abs(sd-cfg.MailboxStddevMB) > 5 {
		t.Fatalf("mean=%.1f sd=%.1f, want ~%.0f / ~%.0f", mean, sd, cfg.MailboxMeanMB, cfg.MailboxStddevMB)
	}
}

func TestSampleMailboxSizeLogNormalIgnoresStddev(t *testing.T) {
	v := 1.0
	a, b := config.Default(), config.Default()
	a.MailboxLogVariance, b.MailboxLogVariance = &v, &v
	b.MailboxStddevMB = 999
	r1, r2 := rand.New(rand.NewSource(7)), rand.New(rand.NewSource(7))
	for i := 0; i < 100; i++ {
		if x, y := sampleMailboxSize(r1, a), sampleMailboxSize(r2, b); x != y || x <= 0 {
			t.Fatalf("draw %d: %v vs %v", i, x, y)
		}
	}
}

func TestSampleMailboxSizeLogNormalMedian(t *testing.T) {
	v := 1.0
	cfg := config.Default()
	cfg.MailboxLogVariance = &v
	rng := rand.New(rand.NewSource(3))
	const n = 100001
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = sampleMailboxSize(rng, cfg)
	}
	slices.Sort(xs)
	want := cfg.MailboxMeanMB * 1024 * 1024 * math.Exp(-v/2)
	if got := xs[n/2]; math.Abs(got-want)/want > 0.02 {
		t.Fatalf("median = %.0f, want ~%.0f", got, want)
	}
}

func TestClampMailboxSize(t *testing.T) {
	const mb = 1024 * 1024
	tests := []struct {
		name    string
		t, mean float64
		want    int64
	}{
		{"inside range", 800 * mb, 1000 * mb, 800 * mb},
		{"below 10% of mean", 50 * mb, 1000 * mb, 100 * mb},
		{"negative", -5 * mb, 1000 * mb, 100 * mb},
		{"floor is 1 MB for tiny mean", 0, 5 * mb, 1 * mb},
		{"above 10x mean", 50000 * mb, 1000 * mb, 10000 * mb},
		{"exactly 10x mean", 10000 * mb, 1000 * mb, 10000 * mb},
	}
	for _, tt := range tests {
		if got := clampMailboxSize(tt.t, tt.mean); got != tt.want {
			t.Errorf("%s: got %d, want %d", tt.name, got, tt.want)
		}
	}
}
