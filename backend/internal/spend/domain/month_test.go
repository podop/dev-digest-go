package domain_test

import (
	"testing"
	"time"

	"dev-digest-go/backend/internal/spend/domain"
)

func TestMonthStart(t *testing.T) {
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"mid month", time.Date(2026, 10, 24, 13, 5, 0, 0, time.UTC), oct},
		{"first instant belongs to the new month (EC5)", oct, oct},
		{"last millisecond belongs to the old month (EC5)", time.Date(2026, 10, 31, 23, 59, 59, 999_000_000, time.UTC), oct},
		{"last nanosecond", time.Date(2026, 10, 31, 23, 59, 59, 999999999, time.UTC), oct},
		{"first instant of next month (EC5)", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{"local zone ahead of UTC", time.Date(2026, 11, 1, 1, 0, 0, 0, time.FixedZone("x", 2*3600)), oct},
		{"local zone behind UTC", time.Date(2026, 10, 31, 20, 0, 0, 0, time.FixedZone("x", -5*3600)), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{"december", time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		{"leap february", time.Date(2028, 2, 29, 23, 0, 0, 0, time.UTC), time.Date(2028, 2, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.MonthStart(tc.now); !got.Equal(tc.want) || got.Location() != time.UTC {
				t.Errorf("MonthStart(%v) = %v, want %v UTC", tc.now, got, tc.want)
			}
		})
	}
}

func TestNextMonthStart(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"mid month", time.Date(2026, 10, 24, 13, 5, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{"last millisecond of the month (EC5)", time.Date(2026, 10, 31, 23, 59, 59, 999_000_000, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		{"first instant of the month", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		{"december rolls the year", time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"31-day month into a shorter one", time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
		{"local zone", time.Date(2026, 11, 1, 1, 0, 0, 0, time.FixedZone("x", 2*3600)), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.NextMonthStart(tc.now); !got.Equal(tc.want) || got.Location() != time.UTC {
				t.Errorf("NextMonthStart(%v) = %v, want %v UTC", tc.now, got, tc.want)
			}
		})
	}
}

// TestMonthWindow_Boundaries pins the half-open window [MonthStart, NextMonthStart) (EC5): the
// instant at 00:00:00.000 on the 1st is in the new month's window, 23:59:59.999 the day before
// in the old one's.
func TestMonthWindow_Boundaries(t *testing.T) {
	newMonth := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	lastOld := newMonth.Add(-time.Millisecond)

	in := func(record, anchor time.Time) bool {
		return !record.Before(domain.MonthStart(anchor)) && record.Before(domain.NextMonthStart(anchor))
	}
	if !in(newMonth, newMonth) || in(newMonth, lastOld) {
		t.Errorf("record at %v: in new window = %v, in old window = %v, want true, false", newMonth, in(newMonth, newMonth), in(newMonth, lastOld))
	}
	if !in(lastOld, lastOld) || in(lastOld, newMonth) {
		t.Errorf("record at %v: in old window = %v, in new window = %v, want true, false", lastOld, in(lastOld, lastOld), in(lastOld, newMonth))
	}
}
