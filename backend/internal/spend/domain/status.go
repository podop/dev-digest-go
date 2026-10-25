package domain

import (
	"fmt"
	"time"
)

// Status is the spend of one calendar month against the workspace's cap.
type Status struct {
	// MonthStart is the first instant of the month, UTC.
	MonthStart time.Time
	// Spent is the month's spend so far.
	Spent Micros
	// Cap is the monthly cap; nil means no cap.
	Cap *Micros
	// Unpriced is the number of this month's ledger entries whose cost is unknown: they are
	// not part of Spent (FR7, FR13).
	Unpriced int
}

// Reached reports whether new work must be refused: a cap is set and the spend is at or over it.
// A cap of 0 is reached from the start (FR11), and a cap lowered below the spend stays reached
// (EC7).
func (s Status) Reached() bool { return s.Cap != nil && s.Spent >= *s.Cap }

// Remaining is what may still be spent this month, never negative; ok is false without a cap.
func (s Status) Remaining() (remaining Micros, ok bool) {
	if s.Cap == nil {
		return 0, false
	}
	return max(*s.Cap-s.Spent, 0), true
}

// Message is the client-facing text of a refused request, for example
// "Monthly spend cap of $50.00 reached: $50.12 used since 2026-10-01 (UTC).". Amounts show two
// decimals, rounded half up. Without a cap it is empty.
func (s Status) Message() string {
	if s.Cap == nil {
		return ""
	}
	return fmt.Sprintf("Monthly spend cap of %s reached: %s used since %s (UTC).",
		dollars(*s.Cap), dollars(s.Spent), s.MonthStart.UTC().Format(time.DateOnly))
}

// dollars formats m with a dollar sign and two decimals, rounding half up in integer maths.
func dollars(m Micros) string {
	sign := ""
	if m < 0 {
		sign, m = "-", -m
	}
	cents := (int64(m) + microsPerUSD/200) / (microsPerUSD / 100)
	return fmt.Sprintf("%s$%d.%02d", sign, cents/100, cents%100)
}
