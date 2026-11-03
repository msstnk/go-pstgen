package pst

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf16"
)

// PidTagMessageSize and PidTagAttachSize are the total length of the data blocks of the node and its subnodes (what ScanPST
// recomputes), and the contents-table row agrees.
func TestMessageSizeIsDataBlockTotal(t *testing.T) {
	p := buildPrunable(t, CryptPermute)
	f, err := openPST(p, false)
	if err != nil {
		t.Fatal(err)
	}
	checked, withAttach := 0, 0
	for _, e := range f.nbtEntries() {
		if e.nid&nidTypeMask != nidMessage {
			continue
		}
		var sum int64
		if err := f.walkNode(e, func(b uint64) {
			if b&bidInternal == 0 {
				r, err := f.blockRef(b)
				if err != nil {
					t.Fatal(err)
				}
				sum += int64(r.cb)
			}
		}); err != nil {
			t.Fatal(err)
		}
		h, err := f.heapNode(e.nid)
		if err != nil {
			t.Fatal(err)
		}
		_, v, ok, err := h.pcValue(0x0E08)
		if err != nil || !ok {
			t.Fatalf("message %#x: no PidTagMessageSize: %v", e.nid, err)
		}
		if int64(v) != sum {
			t.Errorf("message %#x: PidTagMessageSize %d, data blocks total %d", e.nid, v, sum)
		}
		if _, ha, _, _ := h.pcValue(0x0E1B); ha != 0 {
			withAttach++
		}
		checked++
	}
	if checked == 0 || withAttach == 0 {
		t.Fatalf("checked %d messages, %d with attachments", checked, withAttach)
	}
}

func TestBodyPrefixAndConversationID(t *testing.T) {
	long := strings.Repeat("a", 495) + " bcd"
	if got := bodyPrefix(long); len(got) != 495 || strings.HasSuffix(got, " ") {
		t.Errorf("long body: prefix is %d units, want 495 (496 cut at a space, trailing space trimmed)", len(got))
	}
	if got := bodyPrefix("short\n\n"); got != "short" {
		t.Errorf("short body: %q", got)
	}
	if got := bodyPrefix(strings.Repeat("x", 495) + "📧"); len(utf16.Encode([]rune(got))) != 495 {
		t.Errorf("a surrogate pair must not be split: %d units", len(utf16.Encode([]rune(got))))
	}
	if bodyPrefix("") != "" || conversationID("") != nil {
		t.Error("empty body / subject must give no value")
	}
	if a, b := conversationID("Hello"), conversationID("HELLO"); len(a) != 16 || !bytes.Equal(a, b) {
		t.Error("the conversation ID is 16 bytes and ignores case")
	}
}
