package pst

import (
	"encoding/hex"
	"strings"
	"time"
)

// NID types
const (
	nidInternal   = 0x01
	nidFolder     = 0x02
	nidMessage    = 0x04
	nidAttachment = 0x05
	nidHierarchy  = 0x0D
	nidContents   = 0x0E
	nidAssoc      = 0x0F
	nidAttachTbl  = 0x11
	nidRecipTbl   = 0x12

	nidMessageStore = 0x21
	nidNameToID     = 0x61
	nidRootFolder   = 0x122
	nidRecipTable   = 0x692
	nidAttachTable  = 0x671
)

// FileTime returns a FILETIME (100 ns units since 1601-01-01).
func FileTime(t time.Time) int64 { return t.UnixNano()/100 + 116444736000000000 }

type Person struct{ Name, Addr string }

type Attachment struct {
	Name string
	Data []byte
}

// Message is one message. Its contents are not retained after Write.
type Message struct {
	Subject, Body      string
	Sender             Person
	To, Cc             []Person
	Submit, Delivery   time.Time
	MessageID, Headers string
	Attachments        []Attachment
}

type contentRow struct {
	nid                  uint32
	subject, sender      string
	displayTo, displayCc string
	submit, delivery     int64
	flags, size          int32
	hasAttach            bool
}

// Folder is a folder. It holds only the metadata of message rows.
type Folder struct {
	NID      uint32
	Name     string
	Class    string
	Parent   *Folder
	Children []*Folder
	rows     []contentRow
	comment  string
	verbatim bool // node body already emitted from the skeleton (only the hierarchy table row is generated)
}

// Store is a whole PST (message store + folder hierarchy).
type Store struct {
	w       *Writer
	name    string
	recKey  [16]byte
	Root    *Folder
	Top     *Folder // Top of Personal Folders
	Deleted *Folder
	folders []*Folder
	rowSize int64 // approximate bytes of content table rows written at Close
}

// Create creates a new PST. key is the store's unique key (truncated/zero-padded to 16 bytes); crypt is the data block encryption method.
// It has the same fixed folders as an empty PST made by Outlook (Top, Search Root, SPAM Search Folder, Common Views, Deleted Items).
func Create(path, displayName string, key []byte, bufSize int, crypt CryptMethod) (*Store, error) {
	w, err := NewWriter(path, bufSize, crypt)
	if err != nil {
		return nil, err
	}
	w.nextIdx[nidFolder] = 0x405 // 0x8022..0x8082 are for the fixed folders
	s := &Store{w: w, name: displayName}
	copy(s.recKey[:], key)
	s.Root = s.newFolder(nil, nidRootFolder, "", "")
	s.Top = s.newFolder(s.Root, 0x8022, "Top of Personal Folders", "")
	sr := s.newFolder(s.Root, 0x8042, "Search Root", "")
	sp := s.newFolder(s.Root, 0x2223, "SPAM Search Folder 2", "")
	cv := s.newFolder(s.Root, 0x8082, "IPM_COMMON_VIEWS", "")
	sr.verbatim, sp.verbatim, cv.verbatim = true, true, true
	s.Deleted = s.newFolder(s.Top, 0x8062, "Deleted Items", "")
	s.Deleted.comment = "Deleted Items folder"
	return s, nil
}

func (s *Store) newFolder(parent *Folder, nid uint32, name, class string) *Folder {
	f := &Folder{NID: nid, Name: name, Class: class, Parent: parent}
	if parent != nil {
		parent.Children = append(parent.Children, f)
	}
	s.folders = append(s.folders, f)
	return f
}

// AddFolder adds an IPF.Note folder under the parent.
func (s *Store) AddFolder(parent *Folder, name string) *Folder {
	return s.newFolder(parent, s.w.NewNID(nidFolder), name, "IPF.Note")
}

func (s *Store) entryID(nid uint32) []byte {
	b := make([]byte, 24)
	copy(b[4:], s.recKey[:])
	le.PutUint32(b[20:], nid)
	return b
}

func joinNames(ps []Person) string {
	n := make([]string, len(ps))
	for i, p := range ps {
		n[i] = p.Name
	}
	return strings.Join(n, "; ")
}

// oneOffEntryID builds a one-off EntryID (MS-OXCDATA 2.2.5.1) so Outlook can show the SMTP address.
func oneOffEntryID(p Person) []byte {
	b := []byte{0, 0, 0, 0,
		0x81, 0x2B, 0x1F, 0xA4, 0xBE, 0xA3, 0x10, 0x19, 0x9D, 0x6E, 0x00, 0xDD, 0x01, 0x0F, 0x54, 0x02,
		0x00, 0x00, 0x00, 0x80}
	b = append(b, UTF16LE(p.Name+"\x00")...)
	b = append(b, UTF16LE("SMTP\x00")...)
	return append(b, UTF16LE(p.Addr+"\x00")...)
}

func searchKey(p Person) []byte { return []byte("SMTP:" + strings.ToUpper(p.Addr) + "\x00") }

func i32(v int32) []byte {
	b := make([]byte, 4)
	le.PutUint32(b, uint32(v))
	return b
}

func i64(v int64) []byte {
	b := make([]byte, 8)
	le.PutUint64(b, uint64(v))
	return b
}

func bl(v bool) []byte {
	if v {
		return []byte{1}
	}
	return []byte{0}
}

func cell(id uint16, v []byte) Cell { return Cell{id, v} }

// AddMessage writes a message (body and attachments are not retained after writing).
func (s *Store) AddMessage(f *Folder, m *Message) {
	w := s.w
	nid := w.NewNID(nidMessage)
	sc := newSubCtx(w)

	// recipient table
	var rrows []Row
	for i, p := range append(append([]Person{}, m.To...), m.Cc...) {
		typ := int32(1)
		if i >= len(m.To) {
			typ = 2
		}
		rrows = append(rrows, Row{ID: uint32(i + 1), Cells: []Cell{
			cell(0x0C15, i32(typ)), cell(0x0E0F, bl(true)), cell(0x0FFE, i32(6)), cell(0x3001, UTF16LE(p.Name)),
			cell(0x3002, UTF16LE("SMTP")), cell(0x3003, UTF16LE(p.Addr)), cell(0x3900, i32(0)),
			cell(0x0FFF, oneOffEntryID(p)), cell(0x300B, searchKey(p)), cell(0x0FF9, searchKey(p)),
			cell(0x39FF, UTF16LE(p.Name)), cell(0x3A40, bl(false)),
		}})
	}
	rsc := newSubCtx(w)
	sc.addNode(nidRecipTable, buildTC(colsRecipient, rrows, rsc), rsc)

	// attachments
	size := int32(len(m.Body)*2 + 1024)
	if len(m.Attachments) > 0 {
		var arows []Row
		for i, a := range m.Attachments {
			anid := uint32(0x401+i)<<5 | nidAttachment
			asc := newSubCtx(w)
			props := []Prop{
				PInt32(0x0E20, int32(len(a.Data))), PInt32(0x0E21, int32(i)),
				PStr(0x3001, a.Name), PBin(0x3701, a.Data), PStr(0x3704, a.Name),
				PInt32(0x3705, 1), PStr(0x3707, a.Name), PInt32(0x370B, -1),
				PStr(0x370E, "application/octet-stream"), PInt32(0x3714, 0),
				PTime(0x3007, FileTime(m.Submit)), PTime(0x3008, FileTime(m.Submit)),
			}
			sc.addNode(anid, buildPC(props, asc), asc)
			arows = append(arows, Row{ID: anid, Cells: []Cell{
				cell(0x0E20, i32(int32(len(a.Data)))), cell(0x3704, UTF16LE(a.Name)),
				cell(0x3705, i32(1)), cell(0x370B, i32(-1)),
			}})
			size += int32(len(a.Data)) + 512
		}
		asc := newSubCtx(w)
		sc.addNode(nidAttachTable, buildTC(colsAttachment, arows, asc), asc)
	}

	hasAttach := len(m.Attachments) > 0
	flags := int32(0x01)
	if hasAttach {
		flags |= 0x10
	}
	toS, ccS := joinNames(m.To), joinNames(m.Cc)
	sub, dlv := FileTime(m.Submit), FileTime(m.Delivery)
	props := []Prop{
		PInt32(0x0017, 1), PStr(0x001A, "IPM.Note"), PInt32(0x0026, 0), PInt32(0x0036, 0),
		PStr(0x0037, m.Subject), PTime(0x0039, sub),
		PStr(0x0042, m.Sender.Name), PStr(0x0064, "SMTP"), PStr(0x0065, m.Sender.Addr),
		PStr(0x0070, m.Subject), PStr(0x007D, m.Headers),
		PStr(0x0C1A, m.Sender.Name), PStr(0x0C1E, "SMTP"), PStr(0x0C1F, m.Sender.Addr),
		PBin(0x0041, oneOffEntryID(m.Sender)), PBin(0x003B, searchKey(m.Sender)),
		PBin(0x0C19, oneOffEntryID(m.Sender)), PBin(0x0C1D, searchKey(m.Sender)),
		PStr(0x0E03, ccS), PStr(0x0E04, toS), PTime(0x0E06, dlv),
		PInt32(0x0E07, flags), PInt32(0x0E08, size), PInt32(0x0E17, 0), PBool(0x0E1B, hasAttach),
		PStr(0x0E1D, m.Subject), PStr(0x1000, m.Body), PStr(0x1035, m.MessageID),
		PTime(0x3007, sub), PTime(0x3008, dlv),
	}
	chunks := buildPC(props, sc)
	w.PutNode(nid, f.NID, chunks, sc.subs)
	s.rowSize += 128 + 2*int64(len(m.Subject)+len(m.Sender.Name)+len(toS)+len(ccS))
	f.rows = append(f.rows, contentRow{
		nid: nid, subject: m.Subject, sender: m.Sender.Name, displayTo: toS, displayCc: ccS,
		submit: sub, delivery: dlv, flags: flags, size: size, hasAttach: hasAttach,
	})
}

func (s *Store) writeFolder(f *Folder) {
	w := s.w
	nid := f.NID
	if !f.verbatim {
		parent := uint32(0)
		switch {
		case f.Parent != nil:
			parent = f.Parent.NID
		case nid == nidRootFolder:
			parent = nid
		}
		props := []Prop{
			PStr(0x3001, f.Name), PInt32(0x3602, int32(len(f.rows))),
			PInt32(0x3603, 0), PBool(0x360A, len(f.Children) > 0),
		}
		if f.comment != "" {
			props = append(props, PStr(0x3004, f.comment))
		}
		if f.Class != "" {
			props = append(props, PStr(0x3613, f.Class))
		}
		sc := newSubCtx(w)
		ch := buildPC(props, sc)
		w.PutNode(nid, parent, ch, sc.subs)
	}

	base := nid &^ 0x1F
	if f.verbatim {
		return // hierarchy/content/FAI tables are also emitted from the skeleton
	}
	var hrows []Row
	for _, c := range f.Children {
		hrows = append(hrows, Row{ID: c.NID, Cells: []Cell{
			cell(0x3001, UTF16LE(c.Name)), cell(0x3602, i32(int32(len(c.rows)))), cell(0x3603, i32(0)),
			cell(0x360A, bl(len(c.Children) > 0)),
		}})
	}
	hsc := newSubCtx(w)
	hch := buildTC(colsHierarchy, hrows, hsc)
	w.PutNode(base|nidHierarchy, 0, hch, hsc.subs)

	crows := make([]Row, len(f.rows))
	for i, r := range f.rows {
		crows[i] = Row{ID: r.nid, Cells: []Cell{
			cell(0x0017, i32(1)), cell(0x001A, UTF16LE("IPM.Note")), cell(0x0036, i32(0)),
			cell(0x0037, UTF16LE(r.subject)), cell(0x0039, i64(r.submit)), cell(0x0042, UTF16LE(r.sender)),
			cell(0x0E03, UTF16LE(r.displayCc)), cell(0x0E04, UTF16LE(r.displayTo)), cell(0x0E06, i64(r.delivery)),
			cell(0x0E07, i32(r.flags)), cell(0x0E08, i32(r.size)), cell(0x0E17, i32(0)),
			cell(0x3008, i64(r.delivery)),
		}}
	}
	csc := newSubCtx(w)
	cch := buildTC(colsContents, crows, csc)
	w.PutNode(base|nidContents, 0, cch, csc.subs)
	asc := newSubCtx(w)
	ach := buildTC(colsAssoc, nil, asc)
	w.PutNode(base|nidAssoc, 0, ach, asc.subs)
}

// EstimatedSize estimates the file size after Close: the bytes already written plus
// a rough allowance for the content tables, B-Tree pages and system nodes added at Close.
func (s *Store) EstimatedSize() int64 {
	w := s.w
	blocks := int64(len(w.bbt)) + s.rowSize/8192
	pages := (int64(len(w.nbt))/15 + blocks/20 + 2) * 16 / 15 // add 1/15 for the upper levels
	return int64(w.off) + s.rowSize + pages*pageSize + 64<<10
}

// Close writes the folders, store and system nodes, and finalizes the file.
func (s *Store) Close() error {
	w := s.w
	for _, f := range s.folders {
		s.writeFolder(f)
	}
	for _, n := range skeleton {
		chunks := make([][]byte, len(n.data))
		for i, h := range n.data {
			b, err := hex.DecodeString(h)
			if err != nil {
				return err
			}
			chunks[i] = b
		}
		w.PutNode(n.nid, n.parent, chunks, nil)
	}

	// 0x0E34 is store identification info containing a GUID. Vary the GUID part per store.
	var info [24]byte
	le.PutUint32(info[0:], 1)
	for i := range s.recKey {
		info[4+i] = s.recKey[i] ^ 0x5A
	}
	le.PutUint32(info[20:], 1)
	sc := newSubCtx(w)
	props := []Prop{
		PBin(0x0E34, info[:]), PInt32(0x0E38, 0), PBin(0x0FF9, s.recKey[:]), PStr(0x3001, s.name),
		PInt32(0x35DF, 0xC9),
		PBin(0x35E0, s.entryID(s.Top.NID)), PBin(0x35E3, s.entryID(s.Deleted.NID)),
		PBin(0x35E6, s.entryID(0x8082)), PBin(0x35E7, s.entryID(0x8042)),
		PBool(0x6633, true), PInt32(0x66FA, 0xE0011), PInt32(0x66FC, 0x18B2B3), PInt32(0x67FF, 0),
	}
	sch := buildPC(props, sc)
	w.PutNode(nidMessageStore, 0, sch, sc.subs)

	return w.Finish()
}

// Abort closes an unfinished file.
func (s *Store) Abort() { s.w.Abort() }
