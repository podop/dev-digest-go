package domain

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// ErrInvalidRetention means the value is not one of the accepted retention choices.
var ErrInvalidRetention = errors.New("invalid retention_days")

// RetentionKey is the Settings key of the trace retention.
const RetentionKey = "retention_days"

// DefaultRetentionDays applies when the setting is absent.
const DefaultRetentionDays = 90

// retentionChoices are the accepted day counts (spec FR6); null means keep forever.
var retentionChoices = [...]int{30, 60, 90, 180, 365}

// Retention is how long run traces are kept: Days after the run started, or Forever.
type Retention struct {
	Days    int
	Forever bool
}

// DefaultRetention is the retention of a workspace without the setting.
func DefaultRetention() Retention { return Retention{Days: DefaultRetentionDays} }

// KeepForever is the retention stored as null.
func KeepForever() Retention { return Retention{Forever: true} }

// RetentionChoices returns the accepted day counts, ascending.
func RetentionChoices() []int { return slices.Clone(retentionChoices[:]) }

// ParseRetention accepts exactly 30, 60, 90, 180 and 365 days; anything else is
// ErrInvalidRetention.
func ParseRetention(days int) (Retention, error) {
	for _, c := range retentionChoices {
		if days == c {
			return Retention{Days: days}, nil
		}
	}
	return Retention{}, fmt.Errorf("%d days: %w", days, ErrInvalidRetention)
}

// DecodeRetention reads the stored setting: absent is DefaultRetention, the JSON literal null is
// KeepForever, a plain JSON integer is checked by ParseRetention. A string, a fraction, an
// exponent or any other JSON value is ErrInvalidRetention.
func DecodeRetention(raw []byte, present bool) (Retention, error) {
	if !present {
		return DefaultRetention(), nil
	}
	lit := string(bytes.TrimSpace(raw))
	if lit == "null" {
		return KeepForever(), nil
	}
	days, err := strconv.Atoi(lit)
	if err != nil {
		return Retention{}, fmt.Errorf("stored value is not an integer: %w", ErrInvalidRetention)
	}
	return ParseRetention(days)
}

// Cutoff is the instant before which a run's trace is expired: now minus Days times 24 h. A
// trace of a run that started exactly at the cutoff is kept (spec EC8), so callers delete only
// runs started strictly before it. For Forever there is no cutoff and the zero time is returned,
// before which no run started; callers must check Forever first.
func (r Retention) Cutoff(now time.Time) time.Time {
	if r.Forever {
		return time.Time{}
	}
	return now.Add(-time.Duration(r.Days) * 24 * time.Hour)
}
