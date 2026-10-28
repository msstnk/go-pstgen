package gen

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/msstnk/go-pstgen/internal/config"
	"github.com/msstnk/go-pstgen/pst"
)

var domains = []string{
	"example.com", "example.net", "example.org", "contoso.com",
	"corp.test", "mail.test", "mail.invalid", "corp.example",
}

const wordPoolSize = 200000

// letterFallback is used only when a word start state has no data (never with a real dictionary).
var letterFallback = []int{
	817, 149, 278, 425, 1270, 223, 202, 609, 697, 15, 77, 403, 241,
	675, 751, 193, 10, 599, 633, 906, 276, 98, 236, 15, 197, 7,
}

// trigram_table.go is generated from a word list; see gentrigram.go to regenerate it.

// wordLenWeights is the frequency of word lengths (1-15 letters) in English text (per mille, approximate).
var wordLenWeights = []int{30, 170, 200, 160, 110, 85, 75, 55, 40, 30, 20, 12, 7, 4, 2}

// trigramCum is trigramCounts as cumulative tables; states that never occur are nil.
var trigramCum = func() (t [27 * 27][]int) {
	for s, row := range trigramCounts {
		w := make([]int, len(row))
		for i, v := range row {
			w[i] = int(v)
		}
		if cum := cumulative(w); cum[len(cum)-1] > 0 {
			t[s] = cum
		}
	}
	return
}()

// weightedPick picks one entry using a cumulative weight table.
func weightedPick(rng *rand.Rand, cum []int) int {
	return sort.SearchInts(cum, rng.Intn(cum[len(cum)-1])+1)
}

func cumulative(w []int) []int {
	cum := make([]int, len(w))
	acc := 0
	for i, v := range w {
		acc += v
		cum[i] = acc
	}
	return cum
}

// randomWords generates n pseudo-words. The length follows wordLenWeights, and each letter is sampled from the two
// letters before it (a trigram model of English). The last letter is chosen among those that can end a word.
func randomWords(rng *rand.Rand, n int) []string {
	const pad = 26
	lenCum := cumulative(wordLenWeights)
	words := make([]string, n)
	buf := make([]byte, len(wordLenWeights))
	for i := range words {
		l := weightedPick(rng, lenCum) + 1
		a, b, j := pad, pad, 0
		for ; j < l; j++ {
			cum := trigramCum[a*27+b]
			var c int
			if j == l-1 {
				c = pickLast(rng, a, b)
			} else if cum != nil && cum[pad-1] > 0 {
				c = weightedPick(rng, cum[:pad]) // anything but the end of word
			} else {
				break // the word cannot be continued from here
			}
			buf[j] = byte('a' + c)
			a, b = b, c
		}
		words[i] = string(buf[:j])
	}
	return words
}

// pickLast picks a final letter after a, b, weighting each candidate by how often it follows a, b and then ends a word.
func pickLast(rng *rand.Rand, a, b int) int {
	const pad = 26
	row := trigramCounts[a*27+b]
	w := make([]int, pad)
	total := 0
	for c := range w {
		w[c] = int(row[c]) * int(trigramCounts[b*27+c][pad])
		total += w[c]
	}
	if total == 0 { // no known ending: fall back to any letter that can follow
		for c := range w {
			w[c] = int(row[c])
			total += w[c]
		}
	}
	if total == 0 {
		return weightedPick(rng, cumulative(letterFallback))
	}
	return weightedPick(rng, cumulative(w))
}

// corpus is the shared (read-only) text/attachment pool, built once at startup.
type corpus struct {
	words  []string
	text   string // sentence pool (doubled to allow reading past the end)
	starts []int  // sentence start offsets
	attach []byte // random ASCII for attachments (doubled)
	attLen int
}

func newCorpus(rng *rand.Rand, cfg config.Config) *corpus {
	c := &corpus{words: randomWords(rng, wordPoolSize)}
	maxBytes := cfg.MaxSizeKB * 1024
	poolChars := max(maxBytes/2*6/5, 4<<20)
	var sb strings.Builder
	sb.Grow(poolChars*2 + 256)
	count, every := 0, 3+rng.Intn(3)
	for sb.Len() < poolChars {
		c.starts = append(c.starts, sb.Len())
		sb.WriteString(c.sentence(rng, 5+rng.Intn(10)))
		count++
		if count%every == 0 {
			sb.WriteByte('\n')
			every = 3 + rng.Intn(3)
		} else {
			sb.WriteByte(' ')
		}
	}
	one := sb.String()
	c.text = one + one

	const alpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 \n"
	c.attLen = max(maxBytes, 1<<20)
	buf := make([]byte, c.attLen*2)
	rng.Read(buf[:c.attLen])
	for i := 0; i < c.attLen; i++ {
		buf[i] = alpha[int(buf[i])%len(alpha)]
	}
	copy(buf[c.attLen:], buf[:c.attLen])
	c.attach = buf
	return c
}

func (c *corpus) sentence(rng *rand.Rand, n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		w := c.words[rng.Intn(len(c.words))]
		if i == 0 {
			sb.WriteString(strings.ToUpper(w[:1]))
			sb.WriteString(w[1:])
		} else {
			sb.WriteByte(' ')
			sb.WriteString(w)
		}
	}
	sb.WriteByte('.')
	return sb.String()
}

// body cuts out about n characters starting at a sentence boundary (zero-copy).
func (c *corpus) body(rng *rand.Rand, n int) string {
	start := c.starts[rng.Intn(len(c.starts))]
	end := strings.IndexByte(c.text[min(start+n, len(c.text)):], '.')
	if end < 0 {
		return c.text[start:]
	}
	return c.text[start : min(start+n, len(c.text))+end+1]
}

func (c *corpus) attachData(rng *rand.Rand, n int) []byte {
	n = min(n, c.attLen)
	off := rng.Intn(c.attLen)
	return c.attach[off : off+n]
}

func (c *corpus) person(rng *rand.Rand) pst.Person {
	f, l := c.words[rng.Intn(len(c.words))], c.words[rng.Intn(len(c.words))]
	title := func(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
	return pst.Person{
		Name: title(f) + " " + title(l),
		Addr: fmt.Sprintf("%s.%s%d@%s", f, l, 1+rng.Intn(99), domains[rng.Intn(len(domains))]),
	}
}

func (c *corpus) people(rng *rand.Rand) []pst.Person {
	n := 1 + rng.Intn(5)
	ps := make([]pst.Person, n)
	for i := range ps {
		ps[i] = c.person(rng)
	}
	return ps
}
