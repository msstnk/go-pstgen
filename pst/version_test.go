package pst

import (
	"os"
	"testing"
)

// patchVersion sets HEADER.wVer of the PST at path and fixes both header CRCs ([MS-PST] 2.2.2.6).
func patchVersion(t *testing.T, path string, v uint16) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := make([]byte, headerSize)
	if _, err := f.ReadAt(h, 0); err != nil {
		t.Fatal(err)
	}
	le.PutUint16(h[hdrVerOff:], v)
	le.PutUint32(h[hdrCRCFullOff:], pstCRC(h[hdrMagicClientOff:hdrCRCFullEnd]))
	le.PutUint32(h[hdrCRCPartialOff:], pstCRC(h[hdrMagicClientOff:hdrCRCPartialEnd]))
	if _, err := f.WriteAt(h, 0); err != nil {
		t.Fatal(err)
	}
}

// TestOpenVersions: Unicode files have wVer >= 23 ([MS-PST] 2.2.2.6); 14 and 15 are ANSI and 22 is below the Unicode range.
func TestOpenVersions(t *testing.T) {
	for _, tc := range []struct {
		v  uint16
		ok bool
	}{{14, false}, {15, false}, {22, false}, {23, true}, {36, true}, {37, true}} {
		p := buildPrunable(t, CryptNone)
		patchVersion(t, p, tc.v)
		f, err := openPST(p, false)
		if (err == nil) != tc.ok {
			t.Errorf("wVer %d: openPST error = %v, want ok = %v", tc.v, err, tc.ok)
		}
		if err == nil {
			f.close()
			if _, err := CountItems(p); err != nil {
				t.Errorf("wVer %d: CountItems: %v", tc.v, err)
			}
		}
	}
}
