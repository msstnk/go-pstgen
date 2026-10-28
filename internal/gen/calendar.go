package gen

import (
	"math/rand"
	"time"

	"github.com/msstnk/go-pstgen/internal/config"
)

const calendarFolder = "Calendar"

// Meetings start on the half hour from 01:00 to 23:00 UTC and end by midnight.
const (
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
	lo := cfg.End.Add(-time.Duration(cfg.RangeDays) * 24 * time.Hour)
	hi := cfg.End.Add(time.Duration(cfg.CalendarDaysAhead) * 24 * time.Hour)
	day0 := lo.Truncate(24 * time.Hour)
	days := int(hi.Truncate(24*time.Hour).Sub(day0)/(24*time.Hour)) + 1

	for range 1000 {
		r := rng.Float64() * 10
		dur = meetingLengths[len(meetingLengths)-1].d
		for _, l := range meetingLengths {
			if r < l.w {
				dur = l.d
				break
			}
			r -= l.w
		}
		day := day0.Add(time.Duration(rng.Intn(days)) * 24 * time.Hour)
		start = day.Add(time.Duration(firstSlot+rng.Intn(lastSlot-firstSlot+1)) * slot)
		if start.Before(lo) || start.After(hi) || start.Add(dur).After(day.Add(24*time.Hour)) {
			continue
		}
		break
	}
	ref := start
	if ref.After(cfg.End) {
		ref = cfg.End
	}
	created = ref.Add(-time.Hour - time.Duration(rng.Float64()*float64(14*24*time.Hour-time.Hour)))
	return start, dur, created
}
