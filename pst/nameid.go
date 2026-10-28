package pst

import (
	"encoding/hex"
	"sort"
)

// Property IDs assigned to named properties. The first ones are in the NameToIDMap of the empty PST (nameid_base.go);
// the ones from propLocation are appended by nameToIDNode, so the IDs must stay in this order.
const (
	// PSETID_Appointment
	propBusyStatus  = 0x8000 // PidLidBusyStatus 0x8205
	propRecurring   = 0x8001 // PidLidRecurring 0x8223
	propApptStart   = 0x8004 // PidLidAppointmentStartWhole 0x820D
	propApptEnd     = 0x8005 // PidLidAppointmentEndWhole 0x820E
	propClipStart   = 0x8006 // PidLidClipStart 0x8235
	propClipEnd     = 0x8007 // PidLidClipEnd 0x8236
	propReminderSet = 0x8017 // PidLidReminderSet 0x8503 (PSETID_Common)

	propLocation       = 0x8023 // PidLidLocation 0x8208
	propApptSubType    = 0x8024 // PidLidAppointmentSubType 0x8215
	propApptStateFlags = 0x8025 // PidLidAppointmentStateFlags 0x8217
	propReminderDelta  = 0x8026 // PidLidReminderDelta 0x8501 (PSETID_Common)
	propCommonStart    = 0x8027 // PidLidCommonStart 0x8516 (PSETID_Common)
	propCommonEnd      = 0x8028 // PidLidCommonEnd 0x8517 (PSETID_Common)
	propApptDuration   = 0x8029 // PidLidAppointmentDuration 0x8213
	propResponseStatus = 0x802A // PidLidResponseStatus 0x8218
)

// Indexes into the GUID stream of the empty PST (the wGuid field of an entry is index+3 shifted left by one).
const (
	nameGUIDAppointment = 0 // 00062002-0000-0000-C000-000000000046
	nameGUIDCommon      = 2 // 00062008-0000-0000-C000-000000000046
)

// namedProp is a numeric named property added to the empty PST's NameToIDMap.
type namedProp struct {
	lid  uint32
	guid int
	id   uint16
}

var addedNames = []namedProp{
	{0x8208, nameGUIDAppointment, propLocation},
	{0x8215, nameGUIDAppointment, propApptSubType},
	{0x8217, nameGUIDAppointment, propApptStateFlags},
	{0x8501, nameGUIDCommon, propReminderDelta},
	{0x8516, nameGUIDCommon, propCommonStart},
	{0x8517, nameGUIDCommon, propCommonEnd},
	{0x8213, nameGUIDAppointment, propApptDuration},
	{0x8218, nameGUIDAppointment, propResponseStatus},
}

// nameToIDNode builds the NameToIDMap node: the empty PST's map plus addedNames.
func nameToIDNode(sc *subCtx) [][]byte {
	entries := mustHex(nameIDEntries)
	buckets := map[uint16][]byte{}
	var tags []uint16
	for _, b := range nameIDHash {
		buckets[b.tag] = mustHex(b.data)
		tags = append(tags, b.tag)
	}
	for _, n := range addedNames {
		wGuid := uint16(n.guid+3) << 1
		var rec [8]byte
		le.PutUint32(rec[0:], n.lid)
		le.PutUint16(rec[4:], wGuid)
		le.PutUint16(rec[6:], n.id-0x8000)
		entries = append(entries, rec[:]...)
		tag := uint16(0x1000 + (n.lid^uint32(wGuid))%nameIDBuckets)
		if _, ok := buckets[tag]; !ok {
			tags = append(tags, tag)
		}
		buckets[tag] = append(buckets[tag], rec[:]...)
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i] < tags[j] })

	props := []Prop{
		PInt32(0x0001, nameIDBuckets),
		PBin(0x0002, mustHex(nameIDGUIDs)), PBin(0x0003, entries), PBin(0x0004, mustHex(nameIDStrings)),
	}
	for _, t := range tags {
		props = append(props, PBin(t, buckets[t]))
	}
	return buildPC(props, sc)
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
