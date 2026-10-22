package pst

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// pstReader is a minimal PST reader for verification. It applies the same rules to Writer output and Outlook-created files.
type pstReader struct {
	b     []byte
	crypt CryptMethod
	bbt   map[uint64]bbtEnt
	nbt   map[uint32]nbtEnt
	errs  []string
}

func (r *pstReader) errf(format string, a ...any) {
	if len(r.errs) < 1000 {
		r.errs = append(r.errs, fmt.Sprintf(format, a...))
	}
}

func readPST(path string) (*pstReader, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < firstMap+pageSize || string(b[:4]) != "!BDN" {
		return nil, fmt.Errorf("%s: not a PST", path)
	}
	r := &pstReader{b: b, crypt: CryptMethod(b[513]), bbt: map[uint64]bbtEnt{}, nbt: map[uint32]nbtEnt{}}
	if v := le.Uint16(b[10:]); v != 23 {
		r.errf("header: wVer %d, want 23", v)
	}
	if pstCRC(b[8:479]) != le.Uint32(b[4:]) {
		r.errf("header: dwCRCPartial mismatch")
	}
	if pstCRC(b[8:524]) != le.Uint32(b[524:]) {
		r.errf("header: dwCRCFull mismatch")
	}
	if eof := le.Uint64(b[184:]); eof != uint64(len(b)) {
		r.errf("header: ibFileEof %#x, file size %#x", eof, len(b))
	}
	r.checkAMaps()
	r.walk(le.Uint64(b[240:]), 0x80, -1)
	r.walk(le.Uint64(b[224:]), 0x81, -1)
	return r, nil
}

func (r *pstReader) checkAMaps() {
	for pos := uint64(firstMap); pos < uint64(len(r.b)); pos += mapSpan {
		p := r.b[pos : pos+pageSize]
		if p[496] != 0x84 || p[497] != 0x84 || le.Uint64(p[504:]) != pos || pstCRC(p[:496]) != le.Uint32(p[500:]) {
			r.errf("AMap @%#x: bad trailer", pos)
		}
	}
}

// allocated reports whether [ib, ib+n) is allocated in the AMap.
func (r *pstReader) allocated(ib, n uint64) bool {
	for u := ib; u < ib+n; u += 64 {
		pos := firstMap + (u-firstMap)/mapSpan*mapSpan
		bit := (u - pos) / 64
		if r.b[pos+bit/8]&(0x80>>(bit%8)) == 0 {
			return false
		}
	}
	return true
}

func (r *pstReader) walk(ib uint64, ptype byte, level int) {
	if ib < firstMap || ib+pageSize > uint64(len(r.b)) {
		r.errf("page @%#x: out of range", ib)
		return
	}
	p := r.b[ib : ib+pageSize]
	cnt, cmax, cb, lvl := int(p[488]), int(p[489]), int(p[490]), int(p[491])
	if p[496] != ptype || p[497] != ptype {
		r.errf("page @%#x: ptype %#x, want %#x", ib, p[496], ptype)
		return
	}
	if pstCRC(p[:496]) != le.Uint32(p[500:]) {
		r.errf("page @%#x: CRC mismatch", ib)
	}
	if le.Uint16(p[498:]) != sig(ib, le.Uint64(p[504:])) {
		r.errf("page @%#x: wSig mismatch", ib)
	}
	if !r.allocated(ib, pageSize) {
		r.errf("page @%#x: not allocated in AMap", ib)
	}
	if level >= 0 && lvl != level {
		r.errf("page @%#x: cLevel %d, want %d", ib, lvl, level)
	}
	if cb == 0 || cnt > cmax || cnt*cb > 488 {
		r.errf("page @%#x: bad entry layout cEnt=%d cEntMax=%d cbEnt=%d", ib, cnt, cmax, cb)
		return
	}
	var prev uint64
	for i := 0; i < cnt; i++ {
		e := p[i*cb:]
		key := le.Uint64(e)
		if i > 0 && key <= prev {
			r.errf("page @%#x: keys not ascending at %d", ib, i)
		}
		prev = key
		switch {
		case lvl > 0:
			r.walk(le.Uint64(e[16:]), ptype, lvl-1)
		case ptype == 0x80:
			r.bbt[key&^1] = bbtEnt{bid: key &^ 1, ib: le.Uint64(e[8:]), cb: le.Uint16(e[16:])}
		default:
			r.nbt[uint32(key)] = nbtEnt{nid: uint32(key), data: le.Uint64(e[8:]), sub: le.Uint64(e[16:]), parent: le.Uint32(e[24:])}
		}
	}
}

// block verifies a block's trailer and, for an external block, returns its decrypted contents.
func (r *pstReader) block(bid uint64) ([]byte, bool) {
	e, ok := r.bbt[bid&^1]
	if !ok {
		r.errf("bid %#x: not in BBT", bid)
		return nil, false
	}
	total := (uint64(e.cb) + 16 + 63) &^ 63
	if e.ib < firstMap || e.ib+total > uint64(len(r.b)) {
		r.errf("bid %#x: ib %#x out of range", bid, e.ib)
		return nil, false
	}
	if !r.allocated(e.ib, total) {
		r.errf("bid %#x: not allocated in AMap", bid)
	}
	b := r.b[e.ib : e.ib+total]
	t := b[total-16:]
	if le.Uint16(t) != e.cb || le.Uint64(t[8:])&^1 != e.bid {
		r.errf("bid %#x: trailer cb/bid mismatch", bid)
	}
	if le.Uint16(t[2:]) != sig(e.ib, e.bid) {
		r.errf("bid %#x: wSig mismatch", bid)
	}
	d := b[:e.cb]
	if pstCRC(d) != le.Uint32(t[4:]) {
		r.errf("bid %#x: CRC mismatch", bid)
	}
	if bid&2 == 0 && r.crypt != CryptNone {
		d = append([]byte(nil), d...)
		r.crypt.decrypt(d, e.bid)
	}
	return d, true
}

// data expands a data tree and returns the sequence of data blocks.
func (r *pstReader) data(bid uint64, level int) [][]byte {
	if bid == 0 {
		return nil
	}
	d, ok := r.block(bid)
	if !ok {
		return nil
	}
	if bid&2 == 0 {
		if level > 0 {
			r.errf("bid %#x: external block where XBLOCK level %d expected", bid, level)
		}
		return [][]byte{d}
	}
	if len(d) < 8 || d[0] != 0x01 || (level >= 0 && int(d[1]) != level) || d[1] == 0 || d[1] > 2 {
		r.errf("bid %#x: bad XBLOCK header % x", bid, d[:min(len(d), 8)])
		return nil
	}
	n, total := int(le.Uint16(d[2:])), int(le.Uint32(d[4:]))
	if 8+8*n > len(d) {
		r.errf("bid %#x: XBLOCK cEnt %d overflows", bid, n)
		return nil
	}
	var out [][]byte
	for i := 0; i < n; i++ {
		out = append(out, r.data(le.Uint64(d[8+8*i:]), int(d[1])-1)...)
	}
	sum := 0
	for _, c := range out {
		sum += len(c)
	}
	if sum != total {
		r.errf("bid %#x: lcbTotal %d, actual %d", bid, total, sum)
	}
	return out
}

// nodeData returns a node's data block sequence. Outlook locates positions by block number x blockMax, so every block but the last must be full.
func (r *pstReader) nodeData(name string, bid uint64) [][]byte {
	blocks := r.data(bid, -1)
	for i := 0; i < len(blocks)-1; i++ {
		if len(blocks[i]) != blockMax {
			r.errf("%s: block %d/%d is %d bytes, want %d", name, i, len(blocks), len(blocks[i]), blockMax)
		}
	}
	return blocks
}

func (r *pstReader) subs(bid uint64) map[uint32]Sub {
	out := map[uint32]Sub{}
	if bid != 0 {
		r.collectSubs(bid, out)
	}
	return out
}

func (r *pstReader) collectSubs(bid uint64, out map[uint32]Sub) {
	d, ok := r.block(bid)
	if !ok {
		return
	}
	if bid&2 == 0 || len(d) < 8 || d[0] != 0x02 {
		r.errf("bid %#x: not a SLBLOCK/SIBLOCK", bid)
		return
	}
	n := int(le.Uint16(d[2:]))
	switch d[1] {
	case 0:
		if 8+24*n > len(d) {
			r.errf("bid %#x: SLBLOCK overflows", bid)
			return
		}
		for i := 0; i < n; i++ {
			o := 8 + 24*i
			nid := uint32(le.Uint64(d[o:]))
			out[nid] = Sub{NID: nid, Data: le.Uint64(d[o+8:]), Sub: le.Uint64(d[o+16:])}
		}
	case 1:
		if 8+16*n > len(d) {
			r.errf("bid %#x: SIBLOCK overflows", bid)
			return
		}
		for i := 0; i < n; i++ {
			r.collectSubs(le.Uint64(d[16+16*i:]), out)
		}
	default:
		r.errf("bid %#x: bad subnode level %d", bid, d[1])
	}
}

// ---- LTP ----

type hnode struct {
	r      *pstReader
	name   string
	blocks [][]byte
	maps   [][]uint16
	subs   map[uint32]Sub
	client byte
	root   uint32
}

func (r *pstReader) newHN(name string, blocks [][]byte, subs map[uint32]Sub) *hnode {
	h := &hnode{r: r, name: name, blocks: blocks, subs: subs}
	for i, b := range blocks {
		h.maps = append(h.maps, nil)
		if len(b) < 2 {
			r.errf("%s: HN block %d too short", name, i)
			continue
		}
		pm := int(le.Uint16(b))
		if pm+4 > len(b) {
			r.errf("%s: HN block %d ibHnpm %d out of range", name, i, pm)
			continue
		}
		n := int(le.Uint16(b[pm:]))
		end := pm + 4 + 2*(n+1)
		if end > len(b) {
			r.errf("%s: HN block %d HNPAGEMAP overflows", name, i)
			continue
		}
		for _, c := range b[end:] {
			if c != 0 {
				r.errf("%s: HN block %d has non-zero bytes after HNPAGEMAP", name, i)
				break
			}
		}
		offs := make([]uint16, n+1)
		for k := range offs {
			offs[k] = le.Uint16(b[pm+4+2*k:])
		}
		hdr := 2
		switch {
		case i == 0:
			hdr = 12
		case i%128 == 8:
			hdr = 66
		}
		if int(offs[0]) != hdr {
			r.errf("%s: HN block %d first item at %d, want %d", name, i, offs[0], hdr)
		}
		bad := int(offs[n]) > pm
		for k := 1; k <= n; k++ {
			bad = bad || offs[k] < offs[k-1]
		}
		if bad {
			r.errf("%s: HN block %d bad allocation offsets", name, i)
			continue
		}
		h.maps[i] = offs
	}
	if b0 := blocks[0]; len(b0) >= 12 && b0[2] == 0xEC {
		h.client, h.root = b0[3], le.Uint32(b0[4:])
	} else {
		r.errf("%s: bad HNHDR", name)
	}
	return h
}

func (h *hnode) get(hnid uint32) []byte {
	if hnid == 0 {
		return nil
	}
	if hnid&0x1F != 0 {
		s, ok := h.subs[hnid]
		if !ok {
			h.r.errf("%s: subnode %#x missing", h.name, hnid)
			return nil
		}
		var out []byte
		for _, c := range h.r.nodeData(fmt.Sprintf("%s/%#x", h.name, hnid), s.Data) {
			out = append(out, c...)
		}
		return out
	}
	bi, ix := int(hnid>>16), int(hnid>>5&0x7FF)
	if bi >= len(h.maps) || ix < 1 || ix >= len(h.maps[bi]) {
		h.r.errf("%s: bad HID %#x", h.name, hnid)
		return nil
	}
	o := h.maps[bi]
	return h.blocks[bi][o[ix-1]:o[ix]]
}

func keyVal(b []byte) uint64 {
	var v uint64
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// bth returns the leaf records (key + data) of a BTH in key order.
func (h *hnode) bth(hid uint32) (kb, eb int, recs [][]byte) {
	hd := h.get(hid)
	if len(hd) != 8 || hd[0] != 0xB5 {
		h.r.errf("%s: bad BTHHEADER at %#x", h.name, hid)
		return
	}
	kb, eb = int(hd[1]), int(hd[2])
	var walk func(hid uint32, lvl int)
	walk = func(hid uint32, lvl int) {
		d := h.get(hid)
		rs := kb + eb
		if lvl > 0 {
			rs = kb + 4
		}
		if len(d) == 0 || len(d)%rs != 0 {
			h.r.errf("%s: BTH node %#x size %d not a multiple of %d", h.name, hid, len(d), rs)
		}
		for o := 0; o+rs <= len(d); o += rs {
			if lvl == 0 {
				recs = append(recs, d[o:o+rs])
			} else {
				walk(le.Uint32(d[o+kb:]), lvl-1)
			}
		}
	}
	if root := le.Uint32(hd[4:]); root != 0 {
		walk(root, int(hd[3]))
	}
	for i := 1; i < len(recs); i++ {
		if keyVal(recs[i][:kb]) <= keyVal(recs[i-1][:kb]) {
			h.r.errf("%s: BTH keys not ascending", h.name)
			break
		}
	}
	return
}

// checkPC verifies a Property Context and returns its inline values.
func (h *hnode) checkPC() map[uint16]uint32 {
	kb, eb, recs := h.bth(h.root)
	if kb != 2 || eb != 6 {
		h.r.errf("%s: PC BTH key/data size %d/%d", h.name, kb, eb)
		return nil
	}
	inline := map[uint16]uint32{}
	for _, rec := range recs {
		id, typ, v := le.Uint16(rec), le.Uint16(rec[2:]), le.Uint32(rec[4:])
		if inlineType(typ) {
			inline[id] = v
			continue
		}
		d := h.get(v)
		switch {
		case !isVar(typ) && len(d) != cellSize(typ):
			h.r.errf("%s: prop %04x%04x size %d", h.name, id, typ, len(d))
		case typ == PtUnicode && len(d)%2 != 0:
			h.r.errf("%s: prop %04x%04x odd UTF-16 length", h.name, id, typ)
		}
	}
	return inline
}

// checkTC verifies a Table Context and returns its row IDs.
func (h *hnode) checkTC() []uint32 {
	info := h.get(h.root)
	if len(info) < 22 || info[0] != 0x7C || len(info) != 22+8*int(info[1]) {
		h.r.errf("%s: bad TCINFO", h.name)
		return nil
	}
	nc := int(info[1])
	var rgib [4]int
	for i := range rgib {
		rgib[i] = int(le.Uint16(info[2+2*i:]))
	}
	if !(8 <= rgib[0] && rgib[0] <= rgib[1] && rgib[1] <= rgib[2] && rgib[3]-rgib[2] == (nc+7)/8) {
		h.r.errf("%s: bad rgib %v for %d columns", h.name, rgib, nc)
		return nil
	}
	type tcol struct {
		tag          uint32
		ib, cb, ibit int
	}
	cols := make([]tcol, nc)
	for i := range cols {
		d := info[22+8*i:]
		c := tcol{le.Uint32(d), int(le.Uint16(d[4:])), int(d[6]), int(d[7])}
		cols[i] = c
		lo, hi := 0, rgib[0]
		switch c.cb {
		case 2:
			lo, hi = rgib[0], rgib[1]
		case 1:
			lo, hi = rgib[1], rgib[2]
		}
		if c.ib < lo || c.ib+c.cb > hi || c.ibit >= nc || c.cb != cellSize(uint16(c.tag)) {
			h.r.errf("%s: bad TCOLDESC %08x ib=%d cb=%d bit=%d", h.name, c.tag, c.ib, c.cb, c.ibit)
		}
		if i > 0 && c.tag <= cols[i-1].tag {
			h.r.errf("%s: TCOLDESC not sorted at %08x", h.name, c.tag)
		}
	}
	rs := rgib[3]
	var rows [][]byte
	if hn := le.Uint32(info[14:]); hn&0x1F != 0 {
		s, ok := h.subs[hn]
		if !ok {
			h.r.errf("%s: row matrix subnode %#x missing", h.name, hn)
			return nil
		}
		blocks := h.r.nodeData(h.name+"/rows", s.Data)
		per := blockMax / rs
		for i, b := range blocks {
			n := len(b) / rs
			if i < len(blocks)-1 {
				n = min(n, per)
			} else if len(b)%rs != 0 {
				h.r.errf("%s: last row block size %d not a multiple of %d", h.name, len(b), rs)
			}
			for j := 0; j < n; j++ {
				rows = append(rows, b[j*rs:(j+1)*rs])
			}
		}
	} else {
		d := h.get(hn)
		if len(d)%rs != 0 {
			h.r.errf("%s: row matrix size %d not a multiple of %d", h.name, len(d), rs)
		}
		for j := 0; j+rs <= len(d); j += rs {
			rows = append(rows, d[j:j+rs])
		}
	}
	var ids []uint32
	if hid := le.Uint32(info[10:]); hid != 0 {
		kb, eb, idx := h.bth(hid)
		if kb != 4 || eb != 4 {
			h.r.errf("%s: row index key/data size %d/%d", h.name, kb, eb)
			return nil
		}
		if len(idx) != len(rows) {
			h.r.errf("%s: row index has %d entries, row matrix %d rows", h.name, len(idx), len(rows))
		}
		for _, rec := range idx {
			id, i := le.Uint32(rec), int(le.Uint32(rec[4:]))
			if i >= len(rows) || le.Uint32(rows[i]) != id {
				h.r.errf("%s: row index %#x -> %d mismatch", h.name, id, i)
				continue
			}
			ids = append(ids, id)
		}
	} else if len(rows) > 0 {
		h.r.errf("%s: %d rows without row index", h.name, len(rows))
	}
	for _, row := range rows {
		ceb := row[rgib[2]:]
		for _, c := range cols {
			if ceb[c.ibit/8]&(0x80>>(c.ibit%8)) != 0 && isVar(uint16(c.tag)) {
				h.get(le.Uint32(row[c.ib:]))
			}
		}
	}
	return ids
}

type nodeInfo struct {
	client byte
	rowIDs []uint32
	props  map[uint16]uint32
}

func (r *pstReader) checkNode(name string, nid uint32, data, sub uint64, top bool) nodeInfo {
	var ni nodeInfo
	blocks := r.nodeData(name, data)
	subs := r.subs(sub)
	snids := make([]uint32, 0, len(subs))
	for k := range subs {
		snids = append(snids, k)
	}
	sort.Slice(snids, func(i, j int) bool { return snids[i] < snids[j] })
	for _, k := range snids {
		r.checkNode(fmt.Sprintf("%s/%#x", name, k), k, subs[k].Data, subs[k].Sub, false)
	}
	if len(blocks) == 0 {
		return ni
	}
	var mustHN bool
	if top {
		switch nid & 0x1F {
		case nidFolder, nidMessage, nidHierarchy, nidContents, nidAssoc:
			mustHN = true
		}
	} else {
		if nid&0x1F == 0x1F {
			return ni // LTP internal data (values, row matrix) is verified from the referrer
		}
		mustHN = true
	}
	if b := blocks[0]; len(b) < 4 || b[2] != 0xEC {
		if mustHN {
			r.errf("%s: expected Heap-on-Node", name)
		}
		return ni
	}
	h := r.newHN(name, blocks, subs)
	ni.client = h.client
	switch h.client {
	case 0xBC:
		ni.props = h.checkPC()
	case 0x7C:
		ni.rowIDs = h.checkTC()
	}
	return ni
}

// check verifies all nodes and the consistency among folders, hierarchy/content tables and messages.
func (r *pstReader) check() {
	nids := make([]uint32, 0, len(r.nbt))
	for k := range r.nbt {
		nids = append(nids, k)
	}
	sort.Slice(nids, func(i, j int) bool { return nids[i] < nids[j] })
	info := map[uint32]nodeInfo{}
	for _, nid := range nids {
		e := r.nbt[nid]
		info[nid] = r.checkNode(fmt.Sprintf("nid %#x", nid), nid, e.data, e.sub, true)
	}
	for _, nid := range nids {
		if nid&0x1F != nidFolder {
			continue
		}
		base := nid &^ 0x1F
		for _, t := range []uint32{nidHierarchy, nidContents, nidAssoc} {
			if _, ok := r.nbt[base|t]; !ok {
				r.errf("folder %#x: table node %#x missing", nid, base|t)
			}
		}
		for _, id := range info[base|nidHierarchy].rowIDs {
			e, ok := r.nbt[id]
			if t := id & 0x1F; !ok || (t != nidFolder && t != 0x03) || e.parent != nid {
				r.errf("folder %#x: hierarchy row %#x is not a child folder", nid, id)
			}
		}
		msgs := info[base|nidContents].rowIDs
		for _, id := range msgs {
			if e, ok := r.nbt[id]; !ok || id&0x1F != nidMessage || e.parent != nid {
				r.errf("folder %#x: contents row %#x is not a message in this folder", nid, id)
			}
		}
		if n, ok := info[nid].props[0x3602]; ok && int(n) != len(msgs) {
			r.errf("folder %#x: PR_CONTENT_COUNT %d, contents table %d rows", nid, n, len(msgs))
		}
	}
}

func checkFile(t *testing.T, path string) {
	t.Helper()
	r, err := readPST(path)
	if err != nil {
		t.Fatal(err)
	}
	r.check()
	for i, e := range r.errs {
		if i == 30 {
			t.Errorf("... and %d more", len(r.errs)-i)
			break
		}
		t.Error(e)
	}
}

func testMessage(i int) *Message {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
	body := strings.Repeat(fmt.Sprintf("Line %d of the message body. ", i), []int{1, 400, 1500}[i%3])
	m := &Message{
		Subject: fmt.Sprintf("Test message %d 📧", i), Body: body,
		Sender: Person{"Sender", "sender@example.com"},
		To:     []Person{{"To One", "to1@example.com"}, {"To Two", "to2@example.com"}},
		Cc:     []Person{{"Cc", "cc@example.com"}},
		Submit: ts, Delivery: ts, MessageID: fmt.Sprintf("<%d@example.com>", i),
		Headers: "Subject: test\r\n",
	}
	if i%5 == 0 {
		m.Attachments = []Attachment{{Name: "a.bin", Data: make([]byte, 100+i*300)}}
	}
	return m
}

// TestStoreValid verifies a PST generated by Store with every encryption method.
// Includes counts that make the row matrix and HN span multiple blocks, and bodies/attachments that become XBLOCK/XXBLOCK.
func TestStoreValid(t *testing.T) {
	for name, cm := range map[string]CryptMethod{"none": CryptNone, "permute": CryptPermute, "cyclic": CryptCyclic} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.pst")
			s, err := Create(path, "test@example.com", []byte("0123456789abcdef"), 1<<20, cm)
			if err != nil {
				t.Fatal(err)
			}
			inbox := s.AddFolder(s.Top, "Inbox")
			sub := s.AddFolder(inbox, "📁 Subfolder")
			for i := 0; i < 300; i++ {
				s.AddMessage(inbox, testMessage(i))
			}
			big := testMessage(1)
			big.Attachments = []Attachment{{Name: "big.bin", Data: make([]byte, xbMax*blockMax+12345)}}
			s.AddMessage(sub, big)
			s.AddMessage(s.Deleted, testMessage(2))
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			checkFile(t, path)
		})
	}
}

// TestRefFile confirms that the reference PST openable in Outlook satisfies the same verification rules (sanity check of the verifier itself).
func TestRefFile(t *testing.T) {
	path := filepath.Join("..", "ref", "ref_mailbox.pst")
	if _, err := os.Stat(path); err != nil {
		t.Skip("ref PST not found")
	}
	if testing.Short() {
		t.Skip("skipping large ref PST in -short mode")
	}
	checkFile(t, path)
}
