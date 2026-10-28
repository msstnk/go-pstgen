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
	headers := "Message-ID: " + msgID + "\r\n" +
		"Date: " + p.ts.Format(time.RFC1123Z) + "\r\n" +
		"From: " + fmtP([]pst.Person{sender}) + "\r\n" +
		"To: " + fmtP(to) + "\r\n" +
		"Cc: " + fmtP(cc) + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\nX-Mailer: Microsoft Outlook 16.0\r\nX-Priority: 3 (Normal)\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n"

	m := &pst.Message{
		Subject: subject, Body: body, Sender: sender, To: to, Cc: cc,
		Submit: p.ts, Delivery: p.ts, MessageID: msgID, Headers: headers,
	}
	if attachBytes > 0 {
		const lc = "abcdefghijklmnopqrstuvwxyz0123456789"
		name := make([]byte, 12)
		for i := range name {
			name[i] = lc[rng.Intn(len(lc))]
		}
		m.Attachments = []pst.Attachment{{Name: string(name) + ".attach", Data: c.attachData(rng, attachBytes)}}
	}
	return m
}
