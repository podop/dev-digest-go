package domain

import "time"

// periodDays is the days a period preset looks back; PeriodAll has no bound.
var periodDays = map[Period]int{Period7d: 7, Period30d: 30, Period90d: 90}

// ParsePeriod parses the period query value: "7d", "30d", "90d" or "all". ok is false for any other
// text, the empty one included (an absent period is PeriodNone, which callers handle before).
func ParsePeriod(s string) (Period, bool) {
	p := Period(s)
	if _, ok := periodDays[p]; ok || p == PeriodAll {
		return p, true
	}
	return PeriodNone, false
}

// PeriodStart is the start of a period window: now minus N x 24 h. It is nil for PeriodAll and
// PeriodNone (no lower bound).
func PeriodStart(p Period, now time.Time) *time.Time {
	days, ok := periodDays[p]
	if !ok {
		return nil
	}
	t := now.Add(-time.Duration(days) * 24 * time.Hour)
	return &t
}
