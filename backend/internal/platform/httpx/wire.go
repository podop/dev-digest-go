package httpx

import (
	"fmt"
	"time"
)

// TimeLayout is the wire format of every timestamp: UTC, exactly 3 fractional digits, "Z".
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Time is a timestamp that marshals as 2026-10-21T09:15:00.000Z. Use *Time for nullable fields.
type Time time.Time

// NewTime converts t to the wire type.
func NewTime(t time.Time) Time { return Time(t) }

// Std returns the timestamp as a time.Time.
func (t Time) Std() time.Time { return time.Time(t) }

// MarshalJSON implements json.Marshaler.
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).UTC().Format(TimeLayout) + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler; it accepts any RFC 3339 timestamp.
func (t *Time) UnmarshalJSON(b []byte) error {
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return fmt.Errorf("httpx.Time: %s is not a JSON string", b)
	}
	parsed, err := time.Parse(time.RFC3339Nano, string(b[1:len(b)-1]))
	if err != nil {
		return fmt.Errorf("httpx.Time: %w", err)
	}
	*t = Time(parsed.UTC())
	return nil
}
