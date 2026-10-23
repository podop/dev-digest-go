package domain_test

import (
	"testing"
	"time"
	_ "time/tzdata" // the zone database must not depend on the host

	"dev-digest-go/backend/internal/leaderboard/domain"
)

func windowTestZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("time.LoadLocation(%q) error = %v", name, err)
	}
	return loc
}

func windowTestUTC(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", s, err)
	}
	return ts.UTC()
}

// rfc renders an instant in its own zone with the offset, as the API's period.* fields do.
func rfc(ts time.Time) string { return ts.Format(time.RFC3339) }

func TestResolveWindow(t *testing.T) {
	kyiv := windowTestZone(t, "Europe/Kyiv")
	tests := []struct {
		name                       string
		key                        domain.PeriodKey
		loc                        *time.Location
		now                        string
		from, to, prevFrom, prevTo string
		days                       int
		wantKey                    domain.PeriodKey
	}{
		{
			// AC6: Thursday, DST ends on Sunday 2026-10-25 inside the window.
			name: "AC6 week Kyiv", key: domain.PeriodWeek, loc: kyiv, now: "2026-10-22T12:00:00Z",
			from: "2026-10-19T00:00:00+03:00", to: "2026-10-26T00:00:00+02:00",
			prevFrom: "2026-10-12T00:00:00+03:00", prevTo: "2026-10-19T00:00:00+03:00", days: 7, wantKey: domain.PeriodWeek,
		},
		{
			name: "last_week Kyiv", key: domain.PeriodLastWeek, loc: kyiv, now: "2026-10-22T12:00:00Z",
			from: "2026-10-12T00:00:00+03:00", to: "2026-10-19T00:00:00+03:00",
			prevFrom: "2026-10-05T00:00:00+03:00", prevTo: "2026-10-12T00:00:00+03:00", days: 7, wantKey: domain.PeriodLastWeek,
		},
		{
			name: "30d Kyiv", key: domain.Period30d, loc: kyiv, now: "2026-10-22T12:00:00Z",
			from: "2026-09-23T00:00:00+03:00", to: "2026-10-23T00:00:00+03:00",
			prevFrom: "2026-08-24T00:00:00+03:00", prevTo: "2026-09-23T00:00:00+03:00", days: 30, wantKey: domain.Period30d,
		},
		{
			name: "week UTC on a Monday midnight is that Monday", key: domain.PeriodWeek, loc: time.UTC, now: "2026-10-19T00:00:00Z",
			from: "2026-10-19T00:00:00Z", to: "2026-10-26T00:00:00Z",
			prevFrom: "2026-10-12T00:00:00Z", prevTo: "2026-10-19T00:00:00Z", days: 7, wantKey: domain.PeriodWeek,
		},
		{
			name: "week UTC one nanosecond before Monday is still the old week", key: domain.PeriodWeek, loc: time.UTC,
			now:  "2026-10-18T23:59:59Z",
			from: "2026-10-12T00:00:00Z", to: "2026-10-19T00:00:00Z",
			prevFrom: "2026-10-05T00:00:00Z", prevTo: "2026-10-12T00:00:00Z", days: 7, wantKey: domain.PeriodWeek,
		},
		{
			name: "week on Sunday", key: domain.PeriodWeek, loc: time.UTC, now: "2026-10-25T10:00:00Z",
			from: "2026-10-19T00:00:00Z", to: "2026-10-26T00:00:00Z",
			prevFrom: "2026-10-12T00:00:00Z", prevTo: "2026-10-19T00:00:00Z", days: 7, wantKey: domain.PeriodWeek,
		},
		{
			// 22:30Z is already Friday 01:30 in Kyiv: the local day decides, not the UTC day.
			name: "local date differs from UTC date", key: domain.Period30d, loc: kyiv, now: "2026-10-22T22:30:00Z",
			from: "2026-09-24T00:00:00+03:00", to: "2026-10-24T00:00:00+03:00",
			prevFrom: "2026-08-25T00:00:00+03:00", prevTo: "2026-09-24T00:00:00+03:00", days: 30, wantKey: domain.Period30d,
		},
		{
			name: "week in a zone behind UTC", key: domain.PeriodWeek, loc: windowTestZone(t, "America/Los_Angeles"),
			now:  "2026-10-19T03:00:00Z", // still Sunday 20:00 in Los Angeles
			from: "2026-10-12T00:00:00-07:00", to: "2026-10-19T00:00:00-07:00",
			prevFrom: "2026-10-05T00:00:00-07:00", prevTo: "2026-10-12T00:00:00-07:00", days: 7, wantKey: domain.PeriodWeek,
		},
		{
			name: "previous window of a week keeps the calendar days across DST", key: domain.PeriodWeek, loc: kyiv, now: "2026-10-28T09:00:00Z",
			from: "2026-10-26T00:00:00+02:00", to: "2026-11-02T00:00:00+02:00",
			prevFrom: "2026-10-19T00:00:00+03:00", prevTo: "2026-10-26T00:00:00+02:00", days: 7, wantKey: domain.PeriodWeek,
		},
		{
			name: "nil location is UTC", key: domain.PeriodWeek, loc: nil, now: "2026-10-22T12:00:00Z",
			from: "2026-10-19T00:00:00Z", to: "2026-10-26T00:00:00Z",
			prevFrom: "2026-10-12T00:00:00Z", prevTo: "2026-10-19T00:00:00Z", days: 7, wantKey: domain.PeriodWeek,
		},
		{
			name: "unknown key resolves as week", key: domain.PeriodKey("x"), loc: time.UTC, now: "2026-10-22T12:00:00Z",
			from: "2026-10-19T00:00:00Z", to: "2026-10-26T00:00:00Z",
			prevFrom: "2026-10-12T00:00:00Z", prevTo: "2026-10-19T00:00:00Z", days: 7, wantKey: domain.PeriodWeek,
		},
		{
			name: "year boundary", key: domain.PeriodWeek, loc: time.UTC, now: "2027-01-01T08:00:00Z",
			from: "2026-12-28T00:00:00Z", to: "2027-01-04T00:00:00Z",
			prevFrom: "2026-12-21T00:00:00Z", prevTo: "2026-12-28T00:00:00Z", days: 7, wantKey: domain.PeriodWeek,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := windowTestUTC(t, tc.now)
			w := domain.ResolveWindow(tc.key, tc.loc, now)
			got := [4]string{rfc(w.From), rfc(w.To), rfc(w.PrevFrom), rfc(w.PrevTo)}
			want := [4]string{tc.from, tc.to, tc.prevFrom, tc.prevTo}
			if got != want {
				t.Errorf("ResolveWindow(%s, %v, %s) from/to/prevFrom/prevTo = %v, want %v", tc.key, tc.loc, tc.now, got, want)
			}
			if w.Days != tc.days || w.Key != tc.wantKey {
				t.Errorf("ResolveWindow(%s, %v, %s) Days/Key = %d/%s, want %d/%s", tc.key, tc.loc, tc.now, w.Days, w.Key, tc.days, tc.wantKey)
			}
			if !w.PrevTo.Equal(w.From) {
				t.Errorf("ResolveWindow PrevTo = %s, want it equal to From %s", rfc(w.PrevTo), rfc(w.From))
			}
			if w.Prev().To != w.PrevTo || w.Cur().From != w.From {
				t.Errorf("Cur()/Prev() do not mirror the window fields: %+v", w)
			}
		})
	}
}

func TestResolveWindow_DSTDaysKeepTheirLength(t *testing.T) {
	kyiv := windowTestZone(t, "Europe/Kyiv")
	tests := []struct {
		name     string
		key      domain.PeriodKey
		now      string
		wantSpan time.Duration
		wantPrev time.Duration
	}{
		// 2026-03-29 has 23 h (spring forward), 2026-10-25 has 25 h (fall back).
		{"week with a 23 h day", domain.PeriodWeek, "2026-03-25T10:00:00Z", 7*24*time.Hour - time.Hour, 7 * 24 * time.Hour},
		{"week with a 25 h day", domain.PeriodWeek, "2026-10-22T10:00:00Z", 7*24*time.Hour + time.Hour, 7 * 24 * time.Hour},
		{"week after the 25 h day has a 25 h previous week", domain.PeriodWeek, "2026-10-28T10:00:00Z", 7 * 24 * time.Hour, 7*24*time.Hour + time.Hour},
		{"last_week with a 23 h day", domain.PeriodLastWeek, "2026-04-02T10:00:00Z", 7*24*time.Hour - time.Hour, 7 * 24 * time.Hour},
		{"30d across the 23 h day", domain.Period30d, "2026-04-05T10:00:00Z", 30*24*time.Hour - time.Hour, 30 * 24 * time.Hour},
		{"30d across the 25 h day", domain.Period30d, "2026-10-30T10:00:00Z", 30*24*time.Hour + time.Hour, 30 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := domain.ResolveWindow(tc.key, kyiv, windowTestUTC(t, tc.now))
			if got := w.To.Sub(w.From); got != tc.wantSpan {
				t.Errorf("ResolveWindow(%s, Kyiv, %s) span = %v, want %v", tc.key, tc.now, got, tc.wantSpan)
			}
			if got := w.PrevTo.Sub(w.PrevFrom); got != tc.wantPrev {
				t.Errorf("ResolveWindow(%s, Kyiv, %s) previous span = %v, want %v", tc.key, tc.now, got, tc.wantPrev)
			}
		})
	}
}

func TestWindowDayIndex_HalfOpenBounds(t *testing.T) {
	kyiv := windowTestZone(t, "Europe/Kyiv")
	for _, key := range []domain.PeriodKey{domain.PeriodWeek, domain.PeriodLastWeek, domain.Period30d} {
		t.Run(string(key), func(t *testing.T) {
			w := domain.ResolveWindow(key, kyiv, windowTestUTC(t, "2026-10-22T12:00:00Z"))
			tests := []struct {
				name string
				at   time.Time
				want int
			}{
				{"one ns before from", w.From.Add(-time.Nanosecond), -1},
				{"exactly from (EC4: belongs to this window)", w.From, 0},
				{"exactly from, in UTC", w.From.UTC(), 0},
				{"last ns before to", w.To.Add(-time.Nanosecond), w.Days - 1},
				{"exactly to (EC4: belongs to the next window)", w.To, -1},
				{"to in UTC", w.To.UTC(), -1},
				{"far before", w.From.AddDate(-1, 0, 0), -1},
				{"far after", w.To.AddDate(1, 0, 0), -1},
			}
			for _, tc := range tests {
				if got := w.DayIndex(tc.at); got != tc.want {
					t.Errorf("%s window DayIndex(%s) [%s] = %d, want %d", key, rfc(tc.at), tc.name, got, tc.want)
				}
			}
			if !w.Cur().Contains(w.From) || w.Cur().Contains(w.To) || !w.Prev().Contains(w.PrevFrom) || w.Prev().Contains(w.From) {
				t.Errorf("%s window spans are not half-open: cur %+v prev %+v", key, w.Cur(), w.Prev())
			}
		})
	}
}

func TestWindowDayIndex_DSTDays(t *testing.T) {
	kyiv := windowTestZone(t, "Europe/Kyiv")
	tests := []struct {
		name string
		key  domain.PeriodKey
		now  string
		at   string
		want int
	}{
		// Week 2026-10-19 .. 10-26; Sunday 10-25 is the 25 h day (local midnight 21:00Z .. 22:00Z next day).
		{"Sunday starts at 21:00Z", domain.PeriodWeek, "2026-10-22T12:00:00Z", "2026-10-24T21:00:00Z", 6},
		{"Saturday ends one second earlier", domain.PeriodWeek, "2026-10-22T12:00:00Z", "2026-10-24T20:59:59Z", 5},
		{"first 03:30 of the repeated hour", domain.PeriodWeek, "2026-10-22T12:00:00Z", "2026-10-25T00:30:00Z", 6},
		{"second 03:30 of the repeated hour", domain.PeriodWeek, "2026-10-22T12:00:00Z", "2026-10-25T01:30:00Z", 6},
		{"last second of the 25 h day", domain.PeriodWeek, "2026-10-22T12:00:00Z", "2026-10-25T21:59:59Z", 6},
		{"first second after the window", domain.PeriodWeek, "2026-10-22T12:00:00Z", "2026-10-25T22:00:00Z", -1},
		{"Monday", domain.PeriodWeek, "2026-10-22T12:00:00Z", "2026-10-19T12:00:00+03:00", 0},
		// Week 2026-03-23 .. 03-30; Sunday 03-29 is the 23 h day (local midnight 22:00Z .. 21:00Z next day).
		{"Saturday before the 23 h day", domain.PeriodWeek, "2026-03-25T10:00:00Z", "2026-03-28T21:59:59Z", 5},
		{"23 h Sunday starts at 22:00Z", domain.PeriodWeek, "2026-03-25T10:00:00Z", "2026-03-28T22:00:00Z", 6},
		{"last second before the gap", domain.PeriodWeek, "2026-03-25T10:00:00Z", "2026-03-29T00:59:59Z", 6},
		{"first second after the gap", domain.PeriodWeek, "2026-03-25T10:00:00Z", "2026-03-29T01:00:00Z", 6},
		{"last second of the 23 h day", domain.PeriodWeek, "2026-03-25T10:00:00Z", "2026-03-29T20:59:59Z", 6},
		{"after the 23 h week", domain.PeriodWeek, "2026-03-25T10:00:00Z", "2026-03-29T21:00:00Z", -1},
		// 30d window across the 23 h day: every calendar day counts once.
		{"30d first day", domain.Period30d, "2026-04-05T10:00:00Z", "2026-03-07T00:00:00+02:00", 0},
		{"30d day before the 23 h day", domain.Period30d, "2026-04-05T10:00:00Z", "2026-03-28T12:00:00+02:00", 21},
		{"30d the 23 h day", domain.Period30d, "2026-04-05T10:00:00Z", "2026-03-29T12:00:00+03:00", 22},
		{"30d last day", domain.Period30d, "2026-04-05T10:00:00Z", "2026-04-05T23:59:59+03:00", 29},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := domain.ResolveWindow(tc.key, kyiv, windowTestUTC(t, tc.now))
			at, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatalf("time.Parse(%q) error = %v", tc.at, err)
			}
			if got := w.DayIndex(at); got != tc.want {
				t.Errorf("%s window of %s DayIndex(%s) = %d, want %d", tc.key, tc.now, tc.at, got, tc.want)
			}
		})
	}
}

func TestWindowDayIndex_EveryDayOnce(t *testing.T) {
	kyiv := windowTestZone(t, "Europe/Kyiv")
	for _, now := range []string{"2026-03-25T10:00:00Z", "2026-10-22T10:00:00Z", "2026-04-05T10:00:00Z"} {
		for _, key := range []domain.PeriodKey{domain.PeriodWeek, domain.PeriodLastWeek, domain.Period30d} {
			w := domain.ResolveWindow(key, kyiv, windowTestUTC(t, now))
			seen := make([]int, w.Days)
			prev := 0
			for at := w.From; at.Before(w.To); at = at.Add(15 * time.Minute) {
				i := w.DayIndex(at)
				if i < 0 || i >= w.Days {
					t.Fatalf("%s window of %s DayIndex(%s) = %d, want 0..%d", key, now, rfc(at), i, w.Days-1)
				}
				if i < prev || i > prev+1 {
					t.Fatalf("%s window of %s DayIndex(%s) = %d after %d, want non-decreasing steps of 1", key, now, rfc(at), i, prev)
				}
				prev = i
				seen[i]++
			}
			for i, n := range seen {
				if n == 0 {
					t.Errorf("%s window of %s: day %d never seen", key, now, i)
				}
			}
		}
	}
}
