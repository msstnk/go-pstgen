package gen

import (
	"math"
	"math/rand"
	"slices"
	"sort"
	"time"

	"github.com/msstnk/go-pstgen/internal/config"
)

var folderNames = []string{"Inbox", "Sent Items", "Deleted Items", "Recoverable Items", "Legal Holds"}

// folderParents maps a folder to its parent's name. Folders not listed are created under the top folder.
// A parent must appear before its children in folderNames.
var folderParents = map[string]string{"Legal Holds": "Recoverable Items"}

var folderWeights = []int{40, 25, 15, 10, 10}

type planned struct {
	ts        time.Time
	size      int
	hasAttach bool
}

func sampleSize(rng *rand.Rand, cfg config.Config) int {
	mu := math.Log(float64(cfg.MeanSizeKB*1024)) - cfg.LogVariance/2
	s := math.Exp(mu + math.Sqrt(cfg.LogVariance)*rng.NormFloat64())
	return int(math.Min(math.Max(s, float64(cfg.MinSizeKB*1024)), float64(cfg.MaxSizeKB*1024)))
}

// planMessages generates metadata only and sorts it by time ascending within each folder.
func planMessages(rng *rand.Rand, cfg config.Config, target int64, legalHolds bool) ([][]planned, int, int64) {
	weights := slices.Clone(folderWeights)
	if !legalHolds {
		weights[slices.Index(folderNames, "Legal Holds")] = 0
	}
	sum := 0
	for _, w := range weights {
		sum += w
	}
	plan := make([][]planned, len(folderNames))
	var total int64
	n := 0
	for (cfg.TotalMessages > 0 && n < cfg.TotalMessages) || (cfg.TotalMessages <= 0 && total < target) {
		size := sampleSize(rng, cfg)
		r := rng.Intn(sum)
		fi := 0
		for acc := weights[0]; r >= acc; acc += weights[fi] {
			fi++
		}
		ts := cfg.End.Add(-time.Duration(rng.Float64() * float64(cfg.RangeDays) * 86400 * float64(time.Second)))
		plan[fi] = append(plan[fi], planned{ts, size, rng.Float64() < cfg.AttachmentRatio})
		total += int64(size)
		n++
	}
	for _, l := range plan {
		sortPlanned(l)
	}
	return plan, n, total
}

func sortPlanned(l []planned) {
	sort.Slice(l, func(i, j int) bool { return l[i].ts.Before(l[j].ts) })
}
