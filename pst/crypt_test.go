package pst

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// First 32 bytes of mpbbI (the inverse of mpbbR, [MS-PST] 5.1).
var refI = []byte{
	0x47, 0xf1, 0xb4, 0xe6, 0x0b, 0x6a, 0x72, 0x48, 0x85, 0x4e, 0x9e, 0xeb, 0xe2, 0xf8, 0x94, 0x53,
	0xe0, 0xbb, 0xa0, 0x02, 0xe8, 0x5a, 0x09, 0xab, 0xdb, 0xe3, 0xba, 0xc6, 0x7c, 0xc3, 0x10, 0xdd,
}

func TestCryptTables(t *testing.T) {
	if !bytes.Equal(mpbbI[:len(refI)], refI) {
		t.Fatalf("mpbbI mismatch: % x", mpbbI[:len(refI)])
	}
	for i := 0; i < 256; i++ {
		if mpbbS[mpbbS[i]] != byte(i) {
			t.Fatalf("mpbbS not an involution at %d", i)
		}
	}
}

func TestCryptRoundTrip(t *testing.T) {
	src := make([]byte, 1000)
	for i := range src {
		src[i] = byte(i * 7)
	}
	for _, m := range []CryptMethod{CryptNone, CryptPermute, CryptCyclic} {
		b := append([]byte(nil), src...)
		m.encrypt(b, 0x1234_5678_9ABC)
		if m != CryptNone && bytes.Equal(b, src) {
			t.Fatalf("method %d: not encrypted", m)
		}
		m.decrypt(b, 0x1234_5678_9ABC)
		if !bytes.Equal(b, src) {
			t.Fatalf("method %d: round trip failed", m)
		}
	}
}

// TestEncryptedFile writes an encrypted PST, walks the BBT, and verifies the CRC of every block and
// the HN signature (message store PC) after decryption.
func TestEncryptedFile(t *testing.T) {
	for _, m := range []CryptMethod{CryptNone, CryptPermute, CryptCyclic} {
		path := filepath.Join(t.TempDir(), "t.pst")
		s, err := Create(path, "test", []byte("0123456789abcdef"), 1<<16, m)
		if err != nil {
			t.Fatal(err)
		}
		s.AddMessage(s.Deleted, &Message{
			Subject: "hello", Body: "body text", Sender: Person{"a", "a@example.com"},
			To: []Person{{"b", "b@example.com"}}, Submit: time.Now(), Delivery: time.Now(),
			Attachments: []Attachment{{"x.bin", bytes.Repeat([]byte("A"), 20000)}},
		})
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		f, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if CryptMethod(f[513]) != m {
			t.Fatalf("bCryptMethod = %d, want %d", f[513], m)
		}
		blocks := map[uint64][]byte{}
		var walk func(ib uint64, ptype byte, fn func(e []byte))
		walk = func(ib uint64, ptype byte, fn func(e []byte)) {
			p := f[ib : ib+pageSize]
			cnt, cb, lvl := int(p[488]), int(p[490]), p[491]
			for i := 0; i < cnt; i++ {
				e := p[i*cb : (i+1)*cb]
				if lvl > 0 {
					walk(le.Uint64(e[16:]), ptype, fn)
				} else {
					fn(e)
				}
			}
		}
		walk(le.Uint64(f[240:]), 0x80, func(e []byte) {
			bid, ib, n := le.Uint64(e), le.Uint64(e[8:]), uint64(le.Uint16(e[16:]))
			total := (n + 16 + 63) &^ 63
			tr := f[ib+total-16 : ib+total]
			data := f[ib : ib+n]
			if le.Uint64(tr[8:]) != bid || le.Uint32(tr[4:]) != pstCRC(data) {
				t.Fatalf("method %d: bad trailer for bid %#x", m, bid)
			}
			blocks[bid] = data
		})
		var storeBid uint64
		walk(le.Uint64(f[224:]), 0x81, func(e []byte) {
			if le.Uint64(e) == nidMessageStore {
				storeBid = le.Uint64(e[8:])
			}
		})
		d := append([]byte(nil), blocks[storeBid]...)
		m.decrypt(d, storeBid)
		if d[2] != 0xEC || d[3] != 0xBC {
			t.Fatalf("method %d: message store PC not readable after decrypt: % x", m, d[:4])
		}
		if m != CryptNone && blocks[storeBid][2] == 0xEC {
			t.Fatalf("method %d: block stored in plaintext", m)
		}
	}
}
