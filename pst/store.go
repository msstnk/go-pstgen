package pst

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf16"
)

// NID types ([MS-PST] 2.2.2.1, NID_TYPE) and the fixed NIDs ([MS-PST] 2.4.1)
const (
	nidFolder         = 0x02 // NID_TYPE_NORMAL_FOLDER
	nidSearchFolder   = 0x03 // NID_TYPE_SEARCH_FOLDER
	nidMessage        = 0x04 // NID_TYPE_NORMAL_MESSAGE
	nidAttachment     = 0x05 // NID_TYPE_ATTACHMENT
	nidAssocMessage   = 0x08 // NID_TYPE_ASSOC_MESSAGE (folder-associated message)
	nidHierarchy      = 0x0D // NID_TYPE_HIERARCHY_TABLE
	nidContents       = 0x0E // NID_TYPE_CONTENTS_TABLE
	nidAssoc          = 0x0F // NID_TYPE_ASSOC_CONTENTS_TABLE
	nidSearchContents = 0x10 // NID_TYPE_SEARCH_CONTENTS_TABLE
	nidLTP            = 0x1F // NID_TYPE_LTP

	nidTypeBits = 5    // an NID is nidIndex<<nidTypeBits | nidType ([MS-PST] 2.2.2.1)
	nidTypeMask = 0x1F // (1<<nidTypeBits)-1
	// Subnode NIDs (attachments, LTP subnodes) are local to their parent node ([MS-PST] 2.2.2.8.3.3), so their index starts at 0x401 as well.
	firstSubIdx = 0x401

	// HEADER.rgnid starting nidIndex per NID type ([MS-PST] 2.2.2.6): the last index allocated in a blank file,
	// so the first NID handed out has index start+1.
	nidStartFolder   = 0x400
	nidStartSearch   = 0x4000
	nidStartMessage  = 0x10000
	nidStartAssocMsg = 0x8000
	nidStartOther    = 0x400

	// HID ([MS-PST] 2.3.1.1): hidType (always 0) in the low 5 bits, then the 11-bit hidIndex, then the 16-bit hidBlockIndex.
	hidTypeMask = 0x1F
	hidIdxShift = 5
	hidIdxMask  = 0x7FF

	nidMessageStore = 0x21  // NID_MESSAGE_STORE
	nidNameToID     = 0x61  // NID_NAME_TO_ID_MAP
	nidRootFolder   = 0x122 // NID_ROOT_FOLDER
	nidRecipTable   = 0x692 // NID_RECIPIENT_TABLE: template of recipient tables ([MS-PST] 2.4.5.3.1); a message's own table is a subnode with this NID
	nidAttachTable  = 0x671 // NID_ATTACHMENT_TABLE: template of attachment tables ([MS-PST] 2.4.6.1.1); likewise used as the subnode NID
)

// Property IDs of the folder and message store properties that the pruner reads or clears (PidTag names, [MS-OXPROPS]).
const (
	pidDisplayName   = 0x3001 // PidTagDisplayName
	pidContentCount  = 0x3602 // PidTagContentCount
	pidContentUnread = 0x3603 // PidTagContentUnreadCount
	pidSubfolders    = 0x360A // PidTagSubfolders
	pidAssocContent  = 0x3617 // PidTagAssociatedContentCount
	pidIpmSubTreeEID = 0x35E0 // PidTagIpmSubTreeEntryId
)

// FileTime returns a FILETIME (100 ns units since 1601-01-01).
func FileTime(t time.Time) int64 { return t.UnixNano()/100 + 116444736000000000 }

type Person struct{ Name, Addr string }

type Attachment struct {
	Name string
	Data []byte
}

// Appointment makes a Message an IPM.Appointment (a meeting organized by Sender, with To/Cc as attendees).
type Appointment struct {
	Start, End time.Time
	Reply      time.Time // when the meeting was accepted
	Location   string
}

// Message is one message. Its contents are not retained after Write.
type Message struct {
	Subject, Body      string
	Sender             Person
	To, Cc             []Person
	Submit, Delivery   time.Time
	MessageID, Headers string
	Attachments        []Attachment
	Appt               *Appointment // non-nil: the message class is IPM.Appointment instead of IPM.Note
}

type contentRow struct {
	nid                  uint32
	convID               []byte // PidTagConversationId, nil if the subject is empty
	class                string
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
	return createOn(w, displayName, key), nil
}

// createOn builds a Store with the fixed folders on an existing Writer.
func createOn(w *Writer, displayName string, key []byte) *Store {
	w.nextIdx[nidFolder] = 0x405 // nidIndex 0x401..0x404 are taken by the fixed folders below (NIDs 0x8022..0x8082)
	s := &Store{w: w, name: displayName}
	copy(s.recKey[:], key)
	s.Root = s.newFolder(nil, nidRootFolder, "", "")
	// The NIDs of the folders below are those of the sample message store in [MS-PST] 3.10 (IPM subtree 0x8022, Finder 0x8042,
	// Deleted Items 0x8062). 0x8082 and 0x2223 are not in the specification; they are the ones Outlook uses.
	s.Top = s.newFolder(s.Root, 0x8022, "Top of Personal Folders", "")
	sr := s.newFolder(s.Root, 0x8042, "Search Root", "")
	sp := s.newFolder(s.Root, 0x2223, "SPAM Search Folder 2", "")
	cv := s.newFolder(s.Root, 0x8082, "IPM_COMMON_VIEWS", "")
	sr.verbatim, sp.verbatim, cv.verbatim = true, true, true
	s.Deleted = s.newFolder(s.Top, 0x8062, "Deleted Items", "")
	s.Deleted.comment = "Deleted Items folder"
	return s
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

// AddCalendarFolder adds an IPF.Appointment folder under the parent.
func (s *Store) AddCalendarFolder(parent *Folder, name string) *Folder {
	return s.newFolder(parent, s.w.NewNID(nidFolder), name, "IPF.Appointment")
}

// AddPlainFolder adds a folder under the parent without PidTagContainerClass, like the Recoverable Items folders in a Purview export.
func (s *Store) AddPlainFolder(parent *Folder, name string) *Folder {
	return s.newFolder(parent, s.w.NewNID(nidFolder), name, "")
}

// entryID builds an EntryID of this store for a node: rgbFlags (4 zero bytes), uid (the PidTagRecordKey) and the NID
// ([MS-PST] 2.4.3.2 Mapping between EntryID and NID).
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

// oneOffEntryID builds a one-off EntryID ([MS-OXCDATA] 2.2.5.1 One-Off EntryID Structure) so Outlook can show the SMTP address:
// Flags (0), the fixed ProviderUID, Version (0) and a 16-bit flags word, then DisplayName, AddressType and EmailAddress as
// null-terminated UTF-16 strings. The flags word is stored big-endian: 0x0080 = U (UTF-16 strings) with M clear (TNEF), written as
// bytes 00 80. Outlook-created entries usually also set M (0x0180).
func oneOffEntryID(p Person) []byte {
	b := []byte{0, 0, 0, 0,
		0x81, 0x2B, 0x1F, 0xA4, 0xBE, 0xA3, 0x10, 0x19, 0x9D, 0x6E, 0x00, 0xDD, 0x01, 0x0F, 0x54, 0x02,
		0x00, 0x00, 0x00, 0x80}
	b = append(b, UTF16LE(p.Name+"\x00")...)
	b = append(b, UTF16LE("SMTP\x00")...)
	return append(b, UTF16LE(p.Addr+"\x00")...)
}

// searchKey is the PidTagSearchKey of a recipient: "SMTP:" and the upper-cased address, null-terminated.
func searchKey(p Person) []byte { return []byte("SMTP:" + strings.ToUpper(p.Addr) + "\x00") }

// messageSearchKey is a message's PidTagSearchKey (16 bytes, unique per message); ScanPST reports messages without one.
func messageSearchKey(nid uint32) []byte {
	b := make([]byte, 16)
	le.PutUint32(b, nid)
	le.PutUint32(b[4:], nid*2654435761)
	le.PutUint64(b[8:], 0x9E3779B97F4A7C15^uint64(nid))
	return b
}

// conversationID is PidTagConversationId (0x3013) of a message without a conversation index: the MD5 of the conversation topic
// (here the subject), at most 255 UTF-16 code units, upper-cased and little-endian ([MS-OXOMSG] 2.2.1.2). Outlook computes the
// same value when it opens the file (checked on every message of a file it had opened); nil if there is no topic.
func conversationID(subject string) []byte {
	u := utf16.Encode([]rune(strings.ToUpper(subject)))
	if len(u) > 255 {
		u = u[:255]
	}
	for i, c := range u {
		if c == 0 {
			u = u[:i]
			break
		}
	}
	if len(u) == 0 {
		return nil
	}
	b := make([]byte, 2*len(u))
	for i, c := range u {
		le.PutUint16(b[2*i:], c)
	}
	sum := md5.Sum(b)
	return sum[:]
}

// bodyPrefix is PidTagPstBodyPrefix (0x6619): the first 496 UTF-16 code units of the body without trailing white space. Outlook
// writes this into its contents tables when it opens a file whose messages do not have it, and ScanPST then reports the rows
// as not matching the messages; the length and trimming are those seen in a file Outlook had opened (no specification found).
func bodyPrefix(body string) string {
	const maxUnits = 496
	n, units := 0, 0
	for i, r := range body {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > maxUnits {
			break
		}
		units += w
		n = i + len(string(r))
	}
	return strings.TrimRight(body[:n], " \t\r\n")
}

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
	start := w.dataBytes // the size of the message is the data written from here on (subnodes, then the PC)

	// Recipient table: a TC whose columns are those of the recipient table template ([MS-PST] 2.4.5.3). PidTagRecipientType is
	// 1 (To) or 2 (Cc), PidTagObjectType 6 (a recipient).
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

	// Attachments: each is an attachment object (a PC, [MS-PST] 2.4.6.2) in a subnode of the message with its own NID, plus a row in
	// the attachment table ([MS-PST] 2.4.6.1) whose row ID is that NID ([MS-PST] 2.4.6.1.3). The data is PidTagAttachDataBinary.
	var size int32 // PidTagMessageSize, filled in when the message PC is laid out
	if len(m.Attachments) > 0 {
		var arows []Row
		for i, a := range m.Attachments {
			anid := uint32(firstSubIdx+i)<<nidTypeBits | nidAttachment
			asc := newSubCtx(w)
			props := []Prop{
				PInt32(0x0E20, 0), PInt32(0x0E21, int32(i)),
				PStr(0x3001, a.Name), PBin(0x3701, a.Data), PStr(0x3704, a.Name),
				PInt32(0x3705, 1), PStr(0x3707, a.Name), PInt32(0x370B, -1),
				PStr(0x370E, "application/octet-stream"), PInt32(0x3714, 0),
				PTime(0x3007, FileTime(m.Submit)), PTime(0x3008, FileTime(m.Submit)),
			}
			// PidTagAttachSize is the sum of the lengths of the attachment's property values (inline values count 4 or 8
			// bytes, strings and the data their length, the heap structure not at all), which is what ScanPST checks.
			var asize int32
			for _, p := range props {
				asize += int32(len(p.Data))
			}
			props[0] = PInt32(0x0E20, asize)
			sc.addNode(anid, buildPC(props, asc), asc)
			arows = append(arows, Row{ID: anid, Cells: []Cell{
				cell(0x0E20, i32(asize)), cell(0x3704, UTF16LE(a.Name)),
				cell(0x3705, i32(1)), cell(0x370B, i32(-1)),
			}})
		}
		asc := newSubCtx(w)
		sc.addNode(nidAttachTable, buildTC(colsAttachment, arows, asc), asc)
	}

	hasAttach := len(m.Attachments) > 0
	flags := int32(0x01) // PidTagMessageFlags: 0x01 = read; 0x10 below = has attachments ([MS-OXCMSG])
	if hasAttach {
		flags |= 0x10
	}
	class := "IPM.Note"
	if m.Appt != nil {
		class = "IPM.Appointment"
	}
	toS, ccS := joinNames(m.To), joinNames(m.Cc)
	sub, dlv := FileTime(m.Submit), FileTime(m.Delivery)
	props := []Prop{
		PInt32(0x0017, 1), PStr(0x001A, class), PInt32(0x0026, 0), PInt32(0x0036, 0),
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
		PBin(0x300B, messageSearchKey(nid)),
	}
	conv := conversationID(m.Subject)
	if conv != nil {
		props = append(props, PBin(0x3013, conv))
	}
	if pre := bodyPrefix(m.Body); pre != "" {
		props = append(props, PStr(0x6619, pre))
	}
	if a := m.Appt; a != nil {
		props = append(props, apptProps(a)...)
	}
	chunks := buildPCSized(props, sc, 0x0E08, func(heap int) int32 {
		size = int32(w.dataBytes-start) + int32(heap)
		return size
	})
	w.PutNode(nid, f.NID, chunks, sc.subs)
	s.rowSize += 128 + 2*int64(len(m.Subject)+len(m.Sender.Name)+len(toS)+len(ccS))
	f.rows = append(f.rows, contentRow{
		nid: nid, convID: conv, class: class, subject: m.Subject, sender: m.Sender.Name, displayTo: toS, displayCc: ccS,
		submit: sub, delivery: dlv, flags: flags, size: size, hasAttach: hasAttach,
	})
}

// writeFolder writes the four nodes of a Folder object ([MS-PST] 2.4.4): the folder PC, then its hierarchy, contents and FAI
// contents tables, which share the folder's nidIndex. Counts in the PC and in the parent's hierarchy row are kept in step
// ([MS-PST] 2.4.4.3).
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

	base := nid &^ nidTypeMask
	if f.verbatim {
		return // hierarchy/content/FAI tables are also emitted from the skeleton
	}
	var hrows []Row
	for _, c := range f.Children {
		cells := []Cell{
			cell(0x3001, UTF16LE(c.Name)), cell(0x3602, i32(int32(len(c.rows)))), cell(0x3603, i32(0)),
			cell(0x360A, bl(len(c.Children) > 0)),
		}
		if c.Class != "" {
			// PidTagContainerClass is duplicated in the parent's hierarchy row ([MS-PST] 2.4.4.3); ScanPST flags the row
			// as not matching the folder, and Outlook can then file calendar items under Mail, when it is missing.
			cells = append(cells, cell(0x3613, UTF16LE(c.Class)))
		}
		hrows = append(hrows, Row{ID: c.NID, Cells: cells})
	}
	hsc := newSubCtx(w)
	hch := buildTC(colsHierarchy, hrows, hsc)
	w.PutNode(base|nidHierarchy, 0, hch, hsc.subs)

	crows := make([]Row, len(f.rows))
	for i, r := range f.rows {
		crows[i] = Row{ID: r.nid, Cells: []Cell{
			cell(0x0017, i32(1)), cell(0x001A, UTF16LE(r.class)), cell(0x0036, i32(0)),
			cell(0x0037, UTF16LE(r.subject)), cell(0x0039, i64(r.submit)), cell(0x0042, UTF16LE(r.sender)), cell(0x0070, UTF16LE(r.subject)),
			cell(0x0E03, UTF16LE(r.displayCc)), cell(0x0E04, UTF16LE(r.displayTo)), cell(0x0E06, i64(r.delivery)),
			cell(0x0E07, i32(r.flags)), cell(0x0E08, i32(r.size)), cell(0x0E17, i32(0)),
			cell(0x3008, i64(r.delivery)),
		}}
		if r.convID != nil {
			crows[i].Cells = append(crows[i].Cells, cell(0x3013, r.convID))
		}
	}
	csc := newSubCtx(w)
	cch := buildTC(colsContents, crows, csc)
	w.PutNode(base|nidContents, 0, cch, csc.subs)
	asc := newSubCtx(w)
	ach := buildTC(colsAssoc, nil, asc)
	w.PutNode(base|nidAssoc, 0, ach, asc.subs)
}

// PidTagMessageSize (0x0E08) is the total length of the data blocks of the node and its subnodes, which is what ScanPST recomputes
// (PidTagAttachSize is the sum of the attachment's property value lengths). MessageBaseSize and AttachmentSizeOverhead are only the planner's model of the overhead (gen/plan.go).
const (
	MessageBaseSize        = 1024
	AttachmentSizeOverhead = 512
)

// EstimatedSize estimates the file size after Close: the bytes already written plus
// a rough allowance for the content tables, B-Tree pages and system nodes added at Close.
func (s *Store) EstimatedSize() int64 {
	w := s.w
	blocks := int64(len(w.bbt)) + s.rowSize/8192
	pages := (int64(len(w.nbt))/nbtMaxEnt + blocks/bbtMaxEnt + 2) * 16 / 15 // add 1/15 for the upper levels
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

	nsc := newSubCtx(w)
	nch := nameToIDNode(nsc)
	w.PutNode(nidNameToID, 0, nch, nsc.subs)

	// 0x0E34 (PidTagReplVersionHistory) is store identification info containing a GUID. Vary the GUID part per store.
	// Message store PC ([MS-PST] 2.4.3): the required properties are PidTagRecordKey 0x0FF9, PidTagDisplayName 0x3001,
	// PidTagIpmSubTreeEntryId 0x35E0, PidTagIpmWastebasketEntryId 0x35E3 and PidTagFinderEntryId 0x35E7 (2.4.3.1).
	var info [24]byte
	le.PutUint32(info[0:], 1)
	for i := range s.recKey {
		info[4+i] = s.recKey[i] ^ 0x5A
	}
	le.PutUint32(info[20:], 1)
	sc := newSubCtx(w)
	props := []Prop{
		PBin(0x0E34, info[:]), PInt32(0x0E38, 0), PBin(0x0FF9, s.recKey[:]), PStr(0x3001, s.name),
		PInt32(0x35DF, 0xC9), // PidTagValidFolderMask
		PBin(0x35E0, s.entryID(s.Top.NID)), PBin(0x35E3, s.entryID(s.Deleted.NID)),
		PBin(0x35E6, s.entryID(0x8082)), PBin(0x35E7, s.entryID(0x8042)),
		// 0x35E6 (-> IPM_COMMON_VIEWS), 0x6633, 0x66FA and 0x66FC are in neither [MS-PST] nor [MS-OXPROPS]; the values are those of
		// Outlook-created stores. 0x67FF is PidTagPstPassword (0 = no password, [MS-PST] 2.4.3.3).
		PBool(0x6633, true), PInt32(0x66FA, 0xE0011), PInt32(0x66FC, 0x18B2B3), PInt32(0x67FF, 0),
	}
	sch := buildPC(props, sc)
	w.PutNode(nidMessageStore, 0, sch, sc.subs)

	return w.Finish()
}

// Abort closes an unfinished file.
func (s *Store) Abort() { s.w.Abort() }

// apptProps are the named properties of a single (non-recurring) meeting organized by the sender and accepted,
// that is shown as busy, with no reminder.
func apptProps(a *Appointment) []Prop {
	start, end := FileTime(a.Start), FileTime(a.End)
	return []Prop{
		PTime(0x0060, start), PTime(0x0061, end), // PidTagStartDate, PidTagEndDate
		PTime(propApptStart, start), PTime(propApptEnd, end),
		PTime(propClipStart, start), PTime(propClipEnd, end),
		PTime(propCommonStart, start), PTime(propCommonEnd, end),
		PInt32(propApptDuration, int32(a.End.Sub(a.Start)/time.Minute)),
		PStr(propLocation, a.Location),
		PInt32(propBusyStatus, 2), // olBusy
		PBool(propRecurring, false), PBool(propApptSubType, false),
		PInt32(propApptStateFlags, 3), // asfMeeting | asfReceived
		PInt32(propResponseStatus, 3), // respAccepted
		PTime(propReplyTime, FileTime(a.Reply)),
		PBool(propReminderSet, false), PInt32(propReminderDelta, 15),
	}
}
