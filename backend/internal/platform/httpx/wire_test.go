package httpx_test

import (
	"encoding/json"
	"testing"
	"time"

	"dev-digest-go/backend/internal/platform/httpx"
)

func TestTimeMarshal(t *testing.T) {
	plus2 := time.FixedZone("plus2", 2*3600)
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"whole second", time.Date(2026, 10, 21, 9, 15, 0, 0, time.UTC), `"2026-10-21T09:15:00.000Z"`},
		{"milliseconds", time.Date(2026, 10, 21, 9, 15, 0, 123_000_000, time.UTC), `"2026-10-21T09:15:00.123Z"`},
		{"truncates sub-millisecond", time.Date(2026, 10, 21, 9, 15, 0, 123_999_999, time.UTC), `"2026-10-21T09:15:00.123Z"`},
		{"converts zone to UTC", time.Date(2026, 10, 21, 11, 15, 0, 5_000_000, plus2), `"2026-10-21T09:15:00.005Z"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(httpx.NewTime(tc.in))
			if err != nil {
				t.Fatalf("Marshal(%v): %v", tc.in, err)
			}
			if got := string(b); got != tc.want {
				t.Errorf("Marshal(%v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestTimeNullable(t *testing.T) {
	type dto struct {
		At *httpx.Time `json:"at"`
	}
	b, err := json.Marshal(dto{})
	if err != nil {
		t.Fatalf("Marshal(nil time): %v", err)
	}
	if got, want := string(b), `{"at":null}`; got != want {
		t.Errorf("Marshal(nil time) = %s, want %s", got, want)
	}
}

func TestTimeUnmarshal(t *testing.T) {
	var got httpx.Time
	if err := json.Unmarshal([]byte(`"2026-10-21T09:15:00.123Z"`), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if want := time.Date(2026, 10, 21, 9, 15, 0, 123_000_000, time.UTC); !got.Std().Equal(want) {
		t.Errorf("Unmarshal = %v, want %v", got.Std(), want)
	}
	for _, bad := range []string{`123`, `"yesterday"`} {
		var v httpx.Time
		if err := json.Unmarshal([]byte(bad), &v); err == nil {
			t.Errorf("Unmarshal(%s) error = nil, want error", bad)
		}
	}
}
