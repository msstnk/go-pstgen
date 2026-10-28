package gen

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/msstnk/go-pstgen/pst"
)

func buildMessage(rng *rand.Rand, c *corpus, p planned) *pst.Message {
	sender := c.person(rng)
	domain := sender.Addr[strings.IndexByte(sender.Addr, '@')+1:]
	subject := c.sentence(rng, 3+rng.Intn(6))
	to, cc := c.people(rng), c.people(rng)

	// The body is stored as UTF-16, so the character count is half the target byte count
	body := c.body(rng, p.body/2)
	attachBytes := p.attach

	var id [16]byte
	rng.Read(id[:])
	msgID := fmt.Sprintf("<%x-%x-%x-%x-%x@%s>", id[0:4], id[4:6], id[6:8], id[8:10], id[10:], domain)
	fmtP := func(ps []pst.Person) string {
		s := make([]string, len(ps))
		for i, q := range ps {
			s[i] = q.Name + " <" + q.Addr + ">"
		}
		return strings.Join(s, ", ")
	}
	when := p.ts // when the message was sent; a meeting is created before it starts
	if p.dur > 0 {
		when = p.created
	}
	headers := "Message-ID: " + msgID + "\r\n" +
		"Date: " + when.Format(time.RFC1123Z) + "\r\n" +
		"From: " + fmtP([]pst.Person{sender}) + "\r\n" +
		"To: " + fmtP(to) + "\r\n" +
		"Cc: " + fmtP(cc) + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\nX-Mailer: Microsoft Outlook 16.0\r\nX-Priority: 3 (Normal)\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n"

	m := &pst.Message{
		Subject: subject, Body: body, Sender: sender, To: to, Cc: cc,
		Submit: when, Delivery: when, MessageID: msgID, Headers: headers,
	}
	if p.dur > 0 {
		// Accepted within a day of being created, but before the meeting starts
		reply := p.created.Add(time.Duration(rng.Float64() * float64(24*time.Hour)))
		if reply.After(p.ts) {
			reply = p.ts
		}
		m.Appt = &pst.Appointment{Start: p.ts, End: p.ts.Add(p.dur), Reply: reply, Location: randomString(rng, 6+rng.Intn(7))}
	}
	if attachBytes > 0 {
		m.Attachments = []pst.Attachment{{Name: randomString(rng, 12) + ".attach", Data: c.attachData(rng, attachBytes)}}
	}
	return m
}

func randomString(rng *rand.Rand, n int) string {
	const lc = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = lc[rng.Intn(len(lc))]
	}
	return string(b)
}
