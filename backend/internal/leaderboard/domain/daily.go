package domain

import "time"

const aggDayLayout = "2006-01-02"

// Activity returns the activity chart of the window (spec FR16, D22). Its points are the days of
// the window in its zone for week and last_week (win.Days points, also on DST days, EC5;
// GranularityDay) and, for 30d, one point per Monday-started week that intersects the window,
// clipped to it (GranularityWeek). Each point counts the PRs opened and merged in it (from
// pulls: every PR of the filter, a nil instant counts nowhere) and the snapshot CRITICAL
// findings of the rows' reviews, bucketed by the day of their review. Instants outside
// [From, To) are ignored (EC4), so for the rows of the window the critical sum equals the
// critical-found KPI.
func Activity(win Window, rows []Row, pulls []PullFact) ActivityChart {
	loc := win.Location
	if loc == nil {
		loc = time.UTC
	}
	start := win.From.In(loc)
	weekly := win.Key == Period30d

	var points []ActivityPoint
	index := make(map[string]int, win.Days) // local date -> point
	for i := 0; i < win.Days; i++ {
		day := time.Date(start.Year(), start.Month(), start.Day()+i, 0, 0, 0, 0, loc)
		date := day.Format(aggDayLayout)
		if !weekly || i == 0 || day.Weekday() == time.Monday {
			points = append(points, ActivityPoint{From: date})
		}
		last := len(points) - 1
		points[last].To = date
		index[date] = last
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
				points[i].Opened++
			}
		}
		if p.MergedAt != nil {
			if i, ok := at(*p.MergedAt); ok {
				points[i].Merged++
			}
		}
	}
	for _, r := range rows {
		for _, rv := range r.Reviews {
			if i, ok := at(rv.CreatedAt); ok {
				points[i].Critical += rv.Findings.Critical
			}
		}
	}
	granularity := GranularityDay
	if weekly {
		granularity = GranularityWeek
	}
	return ActivityChart{Granularity: granularity, Points: points}
}
