package domain_test

import (
	"encoding/json"
	"testing"

	"dev-digest-go/backend/internal/spend/domain"
)

func TestMicrosUSD(t *testing.T) {
	tests := []struct {
		in   domain.Micros
		want float64
	}{{0, 0}, {50_120_000, 50.12}, {1, 0.000001}, {18_400_000, 18.4}, {1_000_000_000_000, 1_000_000}}
	for _, tc := range tests {
		if got := tc.in.USD(); got != tc.want {
			t.Errorf("Micros(%d).USD() = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestMicrosUSD_JSONHasSixDecimals pins A13: amounts reach the wire with at most 6 decimals and
// no exponent, so USD() needs no extra rounding helper.
func TestMicrosUSD_JSONHasSixDecimals(t *testing.T) {
	tests := []struct {
		in   domain.Micros
		want string
	}{
		{0, "0"},
		{1, "0.000001"},
		{50_120_000, "50.12"},
		{18_400_000, "18.4"},
		{123_456_789, "123.456789"},
		{1_000_000_000_000, "1000000"},
		{999_999_999_999, "999999.999999"},
	}
	for _, tc := range tests {
		b, err := json.Marshal(tc.in.USD())
		if err != nil {
			t.Fatalf("Marshal(Micros(%d).USD()) error: %v", tc.in, err)
		}
		if string(b) != tc.want {
			t.Errorf("json.Marshal(Micros(%d).USD()) = %s, want %s", tc.in, b, tc.want)
		}
	}
}
