package domain

import "time"

// MonthStart returns the first instant (00:00 UTC on the 1st) of the calendar month, in UTC,
// that contains now. The spend of a month is the spend in [MonthStart, NextMonthStart): a record
// at exactly 00:00:00.000 UTC on the 1st belongs to the new month (EC5).
func MonthStart(now time.Time) time.Time {
	y, m, _ := now.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}

// NextMonthStart returns the first instant of the month after the one containing now: the
// exclusive end of that month's spend window.
func NextMonthStart(now time.Time) time.Time {
	return MonthStart(now).AddDate(0, 1, 0)
}
