package domain

import "time"

const aggDayLayout = "2006-01-02"

// Daily returns one Day per calendar day of the window in its zone (spec FR16, win.Days points,
// also on DST days, EC5): PRs opened and merged from pulls (every PR of the filter, a nil
// instant counts nowhere) and the snapshot CRITICAL findings of the rows' reviews, bucketed by
// the day of their review. Instants outside [From, To) are ignored (EC4), so for the rows of the
// window the critical sum equals the critical-found KPI.
func Daily(win Window, rows []Row, pulls []PullFact) []Day {
	loc := win.Location
	if loc == nil {
		loc = time.UTC
	}
	start := win.From.In(loc)
	days := make([]Day, win.Days)
	index := make(map[string]int, win.Days)
	for i := range days {
		date := time.Date(start.Year(), start.Month(), start.Day()+i, 0, 0, 0, 0, loc).Format(aggDayLayout)
		days[i].Date = date
		index[date] = i
	}
	at := func(t time.Time) (int, bool) {
		if t.Before(win.From) || !t.Before(win.To) {
			return 0, false
		}
		i, ok := index[t.In(loc).Format(aggDayLayout)]
		return i, ok
	}

	for _, p := range pulls {
		if p.OpenedAt != nil {
			if i, ok := at(*p.OpenedAt); ok {
				days[i].Opened++
			}
		}
		if p.MergedAt != nil {
			if i, ok := at(*p.MergedAt); ok {
				days[i].Merged++
			}
		}
	}
	for _, r := range rows {
		for _, rv := range r.Reviews {
			if i, ok := at(rv.CreatedAt); ok {
				days[i].Critical += rv.Findings.Critical
			}
		}
	}
	return days
}
