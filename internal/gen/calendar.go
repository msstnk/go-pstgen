package gen

import (
	"math/rand"
	"time"

	"github.com/msstnk/go-pstgen/internal/config"
)

const calendarFolder = "Calendar"

// Meetings start on the half hour from 01:00 to 23:00 UTC and end by midnight.
const (
	fullDay   = 24 * time.Hour
	slot      = 30 * time.Minute
	firstSlot = 2  // 01:00
	lastSlot  = 46 // 23:00
)

// meetingLengths weights the meeting lengths 30 min : 60 min : 120 min = 3 : 6.5 : 0.5.
var meetingLengths = []struct {
	d time.Duration
	w float64
}{{30 * time.Minute, 3}, {60 * time.Minute, 6.5}, {120 * time.Minute, 0.5}}

// sampleMeeting draws a meeting start within [end_date - range_days, end_date + calendar_days_ahead], its length,
// and the time it was created (before it starts, and never after end_date).
func sampleMeeting(rng *rand.Rand, cfg config.Config) (start time.Time, dur time.Duration, created time.Time) {
	lo := cfg.End.Add(-time.Duration(cfg.RangeDays) * fullDay)
	hi := cfg.End.Add(time.Duration(cfg.CalendarDaysAhead) * fullDay)
	day0 := lo.Truncate(fullDay)
	days := int(hi.Truncate(fullDay).Sub(day0)/(fullDay)) + 1

	r := rng.Float64() * 10
	dur = meetingLengths[len(meetingLengths)-1].d
	for _, l := range meetingLengths {
		if r < l.w {
			dur = l.d
			break
		}
		r -= l.w
	}

	// The slots of a day that keep the meeting inside [lo, hi] and inside that day. Only the first and last
	// day can be cut short, or even left empty, so trim those and every remaining day has a valid slot.
	slotsPerDay := int(fullDay / slot)
	slots := func(day time.Time) (a, b int) {
		a, b = firstSlot, min(lastSlot, slotsPerDay-int(dur/slot))
		if d := lo.Sub(day); d > 0 {
			a = max(a, int((d+slot-1)/slot))
		}
		if d := hi.Sub(day); d < 0 {
			b = -1
		} else {
			b = min(b, int(d/slot))
		}
		return a, b
	}
	for a, b := slots(day0); a > b; a, b = slots(day0) {
		day0 = day0.Add(fullDay)
		days--
	}
	for a, b := slots(day0.Add(time.Duration(days-1) * fullDay)); a > b; a, b = slots(day0.Add(time.Duration(days-1) * fullDay)) {
		days--
	}
	day := day0.Add(time.Duration(rng.Intn(days)) * fullDay)
	a, b := slots(day)
	start = day.Add(time.Duration(a+rng.Intn(b-a+1)) * slot)
	ref := start
	if ref.After(cfg.End) {
		ref = cfg.End
	}
	created = ref.Add(-time.Hour - time.Duration(rng.Float64()*float64(14*fullDay-time.Hour)))
	return start, dur, created
}
