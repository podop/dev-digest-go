package domain

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrInvalidWindow is returned when a period query cannot be resolved to a window: a custom
// period without its instants, an unknown key, or an instant JavaScript cannot represent. Node
// threw there too, which its error handler rendered as a 500.
var ErrInvalidWindow = errors.New("performance: invalid period window")

const (
	msPerHour = int64(time.Hour / time.Millisecond)
	msPerDay  = 24 * msPerHour
)

// maxTimeMs is the largest absolute time value of a JavaScript Date.
const maxTimeMs = 8_640_000_000_000_000

// ResolveWindow resolves the query against the clock: presets are [now - hours, now] with both
// ends inclusive, custom ranges [from, to). All instants are whole milliseconds, like a
// JavaScript Date.
func ResolveWindow(q PeriodQuery, now time.Time) (PeriodWindow, error) {
	switch q.Key {
	case PeriodCustom:
		if q.From == nil || q.To == nil {
			return PeriodWindow{}, fmt.Errorf("custom period needs from and to: %w", ErrInvalidWindow)
		}
		return PeriodWindow{Key: PeriodCustom, From: msTime(q.From.UnixMilli()), To: msTime(q.To.UnixMilli())}, nil
	case Period1d, Period30d:
		hours := int64(Preset1dHours)
		if q.Key == Period30d {
			hours = Preset30dHours
		}
		to := now.UnixMilli()
		return PeriodWindow{Key: q.Key, From: msTime(to - hours*msPerHour), To: msTime(to), ToInclusive: true}, nil
	}
	return PeriodWindow{}, fmt.Errorf("period %q: %w", q.Key, ErrInvalidWindow)
}

// Previous is the equally long window right before w, end exclusive; it never overlaps w.
func (w PeriodWindow) Previous() PeriodWindow {
	span := w.spanMs()
	return PeriodWindow{Key: w.Key, From: msTime(w.From.UnixMilli() - span), To: w.From}
}

// Info is the window as the responses echo it.
func (w PeriodWindow) Info() PeriodInfo { return PeriodInfo{Key: w.Key, From: w.From, To: w.To} }

// Contains reports whether t lies in the window.
func (w PeriodWindow) Contains(t time.Time) bool {
	if t.Before(w.From) {
		return false
	}
	if w.ToInclusive {
		return !t.After(w.To)
	}
	return t.Before(w.To)
}

func (w PeriodWindow) spanMs() int64 { return w.To.UnixMilli() - w.From.UnixMilli() }

func msTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// ISOMillis renders t as JavaScript's Date.prototype.toISOString does: UTC with milliseconds,
// and years outside 0000..9999 as a signed six-digit year.
func ISOMillis(t time.Time) string {
	t = t.UTC()
	if y := t.Year(); y < 0 || y > 9999 {
		sign := '+'
		if y < 0 {
			sign, y = '-', -y
		}
		return fmt.Sprintf("%c%06d", sign, y) + t.Format("-01-02T15:04:05.000Z")
	}
	return t.Format("2006-01-02T15:04:05.000Z")
}

// sliceSpec is an equal-width cut of a window starting at its from instant.
type sliceSpec struct {
	from  int64
	width float64
	count int
}

func newSlices(win PeriodWindow, count int) sliceSpec {
	from := win.From.UnixMilli()
	return sliceSpec{from: from, width: float64(win.To.UnixMilli()-from) / float64(count), count: count}
}

// index is the slice an instant falls in, clamped to the first and last slice: the end of an
// inclusive preset belongs to the last one.
func (s sliceSpec) index(at time.Time) int {
	i := math.Floor(float64(at.UnixMilli()-s.from) / s.width)
	return int(math.Min(math.Max(i, 0), float64(s.count-1)))
}

// start is the ISO label of slice i: from + i*width, truncated to the millisecond.
func (s sliceSpec) start(i int) string {
	// The explicit conversion rounds the product before the addition: no fused multiply-add.
	step := float64(float64(i) * s.width)
	return ISOMillis(msTime(int64(math.Trunc(float64(s.from) + step))))
}
