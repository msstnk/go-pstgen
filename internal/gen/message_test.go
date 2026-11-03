package gen

import (
	"math/rand"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/msstnk/go-pstgen/internal/config"
)

func testCorpus() *corpus {
	cfg := config.Default()
	cfg.MaxSizeKB = 64
	return newCorpus(rand.New(rand.NewSource(1)), cfg)
}

func TestBuildMessageContent(t *testing.T) {
	c := testCorpus()
	rng := rand.New(rand.NewSource(2))
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for _, p := range []planned{
		{ts: ts, body: 4000},
		{ts: ts, body: 4000, attach: 30000},
		{ts: ts, body: 2000, dur: time.Hour, created: ts.Add(-48 * time.Hour)},
	} {
		m := buildMessage(rng, c, p)
		if m.Subject == "" || !strings.HasSuffix(m.Subject, ".") {
			t.Errorf("subject %q is not a sentence", m.Subject)
		}
		if n := utf8.RuneCountInString(m.Body); n < p.body/2 || n > p.body/2+200 {
			t.Errorf("body has %d chars, want about %d", n, p.body/2)
		}
		if !strings.Contains(m.Sender.Addr, "@") || len(m.To) == 0 || len(m.Cc) == 0 {
			t.Errorf("sender %+v, to %d, cc %d", m.Sender, len(m.To), len(m.Cc))
		}
		domain := m.Sender.Addr[strings.IndexByte(m.Sender.Addr, '@')+1:]
		if !strings.HasPrefix(m.MessageID, "<") || !strings.HasSuffix(m.MessageID, "@"+domain+">") {
			t.Errorf("message id %q does not match sender domain %q", m.MessageID, domain)
		}
		for _, h := range []string{"Message-ID: " + m.MessageID, "From: " + m.Sender.Name + " <" + m.Sender.Addr + ">", "Subject: " + m.Subject} {
			if !strings.Contains(m.Headers, h+"\r\n") {
				t.Errorf("headers lack %q:\n%s", h, m.Headers)
			}
		}
		if len(m.Attachments) != btoi(p.attach > 0) || (p.attach > 0 && len(m.Attachments[0].Data) != p.attach) {
			t.Errorf("attachments %d, want size %d", len(m.Attachments), p.attach)
		}
		if p.dur > 0 {
			a := m.Appt
			if a == nil || !a.Start.Equal(p.ts) || a.End.Sub(a.Start) != p.dur || a.Reply.Before(p.created) || a.Reply.After(p.ts) || a.Location == "" {
				t.Errorf("bad appointment %+v", a)
			}
			if !m.Submit.Equal(p.created) {
				t.Errorf("a meeting is sent when created: submit %v, want %v", m.Submit, p.created)
			}
		} else if m.Appt != nil || !m.Submit.Equal(p.ts) || !m.Delivery.Equal(p.ts) {
			t.Errorf("plain message: appt %v, submit %v, delivery %v", m.Appt, m.Submit, m.Delivery)
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestCorpusPools(t *testing.T) {
	c := testCorpus()
	rng := rand.New(rand.NewSource(3))
	for range 200 {
		n := 1 + rng.Intn(5000)
		if b := c.body(rng, n); len(b) < n || !strings.HasSuffix(b, ".") {
			t.Fatalf("body(%d) = %d bytes ending %q", n, len(b), b[max(0, len(b)-1):])
		}
		a := c.attachData(rng, 1+rng.Intn(c.attLen))
		for _, ch := range a {
			if ch >= 0x80 || (ch < 0x20 && ch != '\n') {
				t.Fatalf("attachment byte %#x is not in the ASCII alphabet", ch)
			}
		}
	}
}

func TestBuildMessageReproducible(t *testing.T) {
	c := testCorpus()
	p := planned{ts: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC), body: 3000, attach: 5000}
	a, b := buildMessage(rand.New(rand.NewSource(9)), c, p), buildMessage(rand.New(rand.NewSource(9)), c, p)
	if a.Subject != b.Subject || a.Body != b.Body || a.MessageID != b.MessageID || a.Headers != b.Headers {
		t.Error("the same seed must give the same message")
	}
}
