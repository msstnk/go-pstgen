package gen

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/msstnk/go-pstgen/internal/config"
	"github.com/msstnk/go-pstgen/pst"
)

const (
	bytesPerMB = 1024 * 1024

	progressEvery = 1000 // messages between progress lines

	// A sampled mailbox size is clamped to [max(minMailboxFrac x mean, 1 MB), maxMailboxFactor x mean].
	minMailboxFrac   = 0.1
	maxMailboxFactor = 10
)

// generate writes one mailbox (split across several PST files if needed) and returns the paths of the files written.
func generate(cfg config.Config, c *corpus, j job) ([]string, error) {
	rng := rand.New(rand.NewSource(j.seed))
	tag := "[" + j.user + "]"
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return nil, err
	}

	plan, n, total := planMessages(rng, cfg, j.target, j.holds)
	fmt.Printf("%s planned %d messages, ~%d MB\n", tag, n, total/1024/1024)

	var key [16]byte
	rng.Read(key[:])
	splitBytes := int64(cfg.SplitSizeMB * bytesPerMB)

	// Create the same folder layout for each split file
	var (
		s       *pst.Store
		path    string
		part    int
		inPart  int
		folders map[string]*pst.Folder
		written []string
	)
	open := func() error {
		part++
		inPart = 0
		path = outPath(cfg, j.user, part)
		var err error
		if s, err = pst.Create(path, j.user, key[:], 4<<20, cfg.Crypt); err != nil {
			return err
		}
		folders = map[string]*pst.Folder{}
		for _, name := range folderNames {
			if !j.holds && slices.Contains(holdFolders, name) {
				continue
			}
			parent, hasParent := folderParents[name]
			switch {
			case name == "Deleted Items":
				folders[name] = s.Deleted
			case hasParent:
				folders[name] = s.AddPlainFolder(folders[parent], name)
			case name == calendarFolder:
				folders[name] = s.AddCalendarFolder(s.Top, name)
			case name == recoverableRoot:
				folders[name] = s.AddPlainFolder(s.Top, name)
			default:
				folders[name] = s.AddFolder(s.Top, name)
			}
		}
		return nil
	}
	finish := func() error {
		if err := s.Close(); err != nil {
			s.Abort()
			return err
		}
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		fmt.Printf("%s done: %s (%d MB, %d messages)\n", tag, path, st.Size()/1024/1024, inPart)
		written = append(written, path)
		return nil
	}

	if err := open(); err != nil {
		return nil, err
	}
	done := 0
	for fi, name := range folderNames {
		for _, p := range plan[fi] {
			// If adding this would exceed the split size, move to the next file (the first message is always kept)
			if splitBytes > 0 && inPart > 0 && s.EstimatedSize()+int64(p.size()) > splitBytes {
				if err := finish(); err != nil {
					return nil, err
				}
				if err := open(); err != nil {
					return nil, err
				}
			}
			s.AddMessage(folders[name], buildMessage(rng, c, p))
			inPart++
			done++
			if done%progressEvery == 0 {
				fmt.Printf("%s  %d/%d\n", tag, done, n)
			}
		}
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return written, nil
}

// sanitize strips Windows-reserved characters and trailing dots/spaces so the result is usable as a file name.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.TrimRight(s, ". ")
	if s == "" {
		s = "_"
	}
	return s
}

// checkDuplicates rejects mailboxes whose output file names collide (case-insensitively, after sanitizing).
func checkDuplicates(mailboxes []string) error {
	seen := map[string]bool{}
	for _, u := range mailboxes {
		d := strings.ToLower(sanitize(u))
		if seen[d] {
			return fmt.Errorf("duplicate mailbox in list: %s", u)
		}
		seen[d] = true
	}
	return nil
}

// outPath returns the path of the part-th (1-based) split file.
func outPath(cfg config.Config, user string, part int) string {
	name := sanitize(user)
	if part > 1 {
		name += fmt.Sprintf("-%d", part)
	}
	return filepath.Join(cfg.OutputDir, name+".pst")
}

type job struct {
	user   string
	seed   int64
	target int64
	holds  bool
}

// Run generates every mailbox in cfg using a pool of cfg.Workers workers.
// It returns the paths of the PST files written (in mailbox order, then split order) and an error if the mailbox list
// is invalid or any mailbox failed. The files of a mailbox that failed are not listed.
func Run(cfg config.Config) ([]string, error) {
	if len(cfg.Mailboxes) == 0 {
		return nil, fmt.Errorf("mailboxes is empty")
	}
	if err := checkDuplicates(cfg.Mailboxes); err != nil {
		return nil, err
	}

	seed := time.Now().UnixNano()
	if cfg.Seed != nil {
		seed = *cfg.Seed
	}
	master := rand.New(rand.NewSource(seed))
	corp := newCorpus(master, cfg)

	// Decide the target size and seed up front (reproducible via seed regardless of parallel execution order)
	jobs := make([]job, len(cfg.Mailboxes))
	for i, u := range cfg.Mailboxes {
		mean := cfg.MailboxMeanMB * bytesPerMB
		t := sampleMailboxSize(master, cfg)
		j := job{user: u, seed: master.Int63(), target: clampMailboxSize(t, mean)}
		// No random draw when the ratio is 1 so existing seeded output is unchanged
		j.holds = cfg.HoldsRatio >= 1 || master.Float64() < cfg.HoldsRatio
		jobs[i] = j
	}
	workers := max(1, min(cfg.Workers, len(jobs)))
	fmt.Printf("%d mailboxes, %d workers\n", len(jobs), workers)

	ch := make(chan int)
	written := make([][]string, len(jobs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	completed := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				j := jobs[i]
				files, err := generate(cfg, corp, j)
				if err != nil {
					fmt.Printf("[%s] FAILED: %v\n", j.user, err)
					continue
				}
				written[i] = files
				mu.Lock()
				completed++
				mu.Unlock()
			}
		}()
	}
	for i := range jobs {
		ch <- i
	}
	close(ch)
	wg.Wait()
	fmt.Printf("completed %d/%d mailboxes\n", completed, len(jobs))
	var files []string
	for _, w := range written {
		files = append(files, w...)
	}
	if completed != len(jobs) {
		return files, fmt.Errorf("%d of %d mailboxes failed", len(jobs)-completed, len(jobs))
	}
	return files, nil
}

// sampleMailboxSize draws a target mailbox size in bytes: normal by default, or log-normal
// (long right tail, same mean) when mailbox_log_variance is set.
func sampleMailboxSize(rng *rand.Rand, cfg config.Config) float64 {
	mean := cfg.MailboxMeanMB * bytesPerMB
	z := rng.NormFloat64()
	if cfg.MailboxLogVariance == nil {
		return z*cfg.MailboxStddevMB*bytesPerMB + mean
	}
	v := *cfg.MailboxLogVariance
	return math.Exp(math.Log(mean) - v/2 + math.Sqrt(v)*z)
}

// clampMailboxSize limits a sampled size (see minMailboxFrac, maxMailboxFactor) and returns bytes.
func clampMailboxSize(t, mean float64) int64 {
	return int64(min(max(t, max(mean*minMailboxFrac, bytesPerMB)), mean*maxMailboxFactor))
}
