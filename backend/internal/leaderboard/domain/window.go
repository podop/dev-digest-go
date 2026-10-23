package domain

import "time"

// ResolveWindow resolves a period in the viewer's zone (spec FR7). All bounds are local
// midnights of loc, built from calendar dates, so a DST day keeps its 23 or 25 hours and
// still counts once:
//
//	week      [Monday 00:00 of the week of now, next Monday 00:00)
//	last_week [the Monday before, this Monday)
//	30d       [00:00 of today - 29 days, 00:00 of tomorrow)
//
// The previous window is the same number of calendar days directly before From (PrevTo ==
// From). An unknown key resolves as week; a nil loc as UTC. In a zone whose local midnight
// does not exist on a day (a DST gap at 00:00) time.Date picks one of the two valid instants.
func ResolveWindow(key PeriodKey, loc *time.Location, now time.Time) Window {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := now.In(loc).Date()
	midnight := func(offsetDays int) time.Time { return time.Date(y, m, d+offsetDays, 0, 0, 0, 0, loc) }
	sinceMonday := (int(now.In(loc).Weekday()) + 6) % 7

	var startDay, days int
	switch key {
	case PeriodLastWeek:
		startDay, days = -sinceMonday-7, 7
	case Period30d:
		startDay, days = -29, 30
	default:
		key = PeriodWeek
		startDay, days = -sinceMonday, 7
	}
	from := midnight(startDay)
	return Window{
		Key:      key,
		Location: loc,
		From:     from,
		To:       midnight(startDay + days),
		PrevFrom: midnight(startDay - days),
		PrevTo:   from,
		Days:     days,
	}
}

// DayIndex is the zero-based calendar day of t inside the window, counted in the window's zone
// from the day of From, or -1 when t lies outside [From, To). From is index 0 and the last
// instant before To is Days-1.
func (w Window) DayIndex(t time.Time) int {
	if !w.Cur().Contains(t) {
		return -1
	}
	loc := w.Location
	if loc == nil {
		loc = time.UTC
	}
	return civilDays(w.From.In(loc), t.In(loc))
}

// civilDays is the number of calendar days from a's date to b's date (both in the same zone).
func civilDays(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	da := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	db := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da) / (24 * time.Hour))
}
