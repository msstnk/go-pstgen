package gen

import (
	"math"
	"math/rand"
	"slices"
	"sort"
	"time"

	"github.com/msstnk/go-pstgen/internal/config"
	"github.com/msstnk/go-pstgen/pst"
)

// Purview names the Recoverable Items root with a hyphen, and imports it as a system folder only when the name matches.
const recoverableRoot = "Recoverable-Items"

var folderNames = []string{"Inbox", "Sent Items", "Deleted Items", recoverableRoot, "Deletions", "Purges", "Versions", "DiscoveryHolds", "SubstrateHolds"}

// holdFolders exist only in mailboxes with a hold (litigation hold, eDiscovery hold or retention policy) in effect.
var holdFolders = []string{"Purges", "Versions", "DiscoveryHolds", "SubstrateHolds"}

// folderParents maps a folder to its parent's name. Folders not listed are created under the top folder.
// A parent must appear before its children in folderNames.
var folderParents = map[string]string{
	"Deletions": recoverableRoot, "Purges": recoverableRoot, "Versions": recoverableRoot, "DiscoveryHolds": recoverableRoot, "SubstrateHolds": recoverableRoot,
}

// Recoverable-Items itself holds no messages in Exchange; its subfolders do.
var folderWeights = []int{38, 24, 15, 0, 8, 5, 4, 3, 3}

type planned struct {
	ts     time.Time
	body   int // body size in bytes (before UTF-16 conversion)
	attach int // attachment size in bytes; 0 = no attachment
}

// size is the value of PidTagMessageSize.
func (p planned) size() int {
	if p.attach > 0 {
		return pst.MessageBaseSize + p.body + p.attach + pst.AttachmentSizeOverhead
	}
	return pst.MessageBaseSize + p.body
}

// logNormal samples a log-normal value with the given mean (not median) and log variance.
func logNormal(rng *rand.Rand, mean, variance float64) float64 {
	return math.Exp(math.Log(mean) - variance/2 + math.Sqrt(variance)*rng.NormFloat64())
}

// sampleSizes draws the body size, and with probability attachment_ratio an attachment size, both log-normal.
// The total is clamped to [min_size_kb, max_size_kb] by trimming the attachment first, then growing or trimming the body.
func sampleSizes(rng *rand.Rand, cfg config.Config) (body, attach int) {
	bm, am := cfg.MeanBodyAttach()
	body = max(int(logNormal(rng, bm, cfg.BodyLogVariance)), 1)
	if am > 0 && rng.Float64() < cfg.AttachmentRatio {
		attach = max(int(logNormal(rng, am, cfg.LogVariance)), 1)
	}
	lo, hi := cfg.MinSizeKB*1024, cfg.MaxSizeKB*1024
	if over := (planned{body: body, attach: attach}).size() - hi; over > 0 {
		cut := min(over, max(attach-1, 0))
		attach -= cut
		body = max(body-(over-cut), 1)
	}
	if short := lo - (planned{body: body, attach: attach}).size(); short > 0 {
		body += short
	}
	return body, attach
}

// planMessages generates metadata only and sorts it by time ascending within each folder.
func planMessages(rng *rand.Rand, cfg config.Config, target int64, holds bool) ([][]planned, int, int64) {
	weights := slices.Clone(folderWeights)
	if !holds {
		for _, name := range holdFolders {
			weights[slices.Index(folderNames, name)] = 0
		}
	}
	sum := 0
	for _, w := range weights {
		sum += w
	}
	plan := make([][]planned, len(folderNames))
	var total int64
	n := 0
	for (cfg.TotalMessages > 0 && n < cfg.TotalMessages) || (cfg.TotalMessages <= 0 && total < target) {
		body, attach := sampleSizes(rng, cfg)
		r := rng.Intn(sum)
		fi := 0
		for acc := weights[0]; r >= acc; acc += weights[fi] {
			fi++
		}
		ts := cfg.End.Add(-time.Duration(rng.Float64() * float64(cfg.RangeDays) * 86400 * float64(time.Second)))
		p := planned{ts, body, attach}
		plan[fi] = append(plan[fi], p)
		total += int64(p.size())
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
