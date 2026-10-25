package domain_test

import (
	"math"
	"testing"
	"time"

	"dev-digest-go/backend/internal/spend/domain"
)

func micros(v int64) *domain.Micros { m := domain.Micros(v); return &m }

func TestStatus(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		status        domain.Status
		wantReached   bool
		wantRemaining domain.Micros
		wantHasCap    bool
		wantMessage   string
	}{
		{"no cap", domain.Status{MonthStart: start, Spent: 5_000_000}, false, 0, false, ""},
		{"no cap and no spend", domain.Status{MonthStart: start}, false, 0, false, ""},
		{"under cap", domain.Status{MonthStart: start, Spent: 18_400_000, Cap: micros(50_000_000)}, false, 31_600_000, true,
			"Monthly spend cap of $50.00 reached: $18.40 used since 2026-10-01 (UTC)."},
		{"one micro under cap", domain.Status{MonthStart: start, Spent: 49_999_999, Cap: micros(50_000_000)}, false, 1, true,
			"Monthly spend cap of $50.00 reached: $50.00 used since 2026-10-01 (UTC)."},
		{"at cap", domain.Status{MonthStart: start, Spent: 50_000_000, Cap: micros(50_000_000)}, true, 0, true,
			"Monthly spend cap of $50.00 reached: $50.00 used since 2026-10-01 (UTC)."},
		{"over cap", domain.Status{MonthStart: start, Spent: 50_120_000, Cap: micros(50_000_000)}, true, 0, true,
			"Monthly spend cap of $50.00 reached: $50.12 used since 2026-10-01 (UTC)."},
		{"zero cap blocks from the start (FR11)", domain.Status{MonthStart: start, Cap: micros(0)}, true, 0, true,
			"Monthly spend cap of $0.00 reached: $0.00 used since 2026-10-01 (UTC)."},
		{"cap lowered below the spend (EC7)", domain.Status{MonthStart: start, Spent: 30_000_000, Cap: micros(10_000_000)}, true, 0, true,
			"Monthly spend cap of $10.00 reached: $30.00 used since 2026-10-01 (UTC)."},
		{"maximum cap (FR11)", domain.Status{MonthStart: start, Spent: 1, Cap: micros(1_000_000_000_000)}, false, 999_999_999_999, true,
			"Monthly spend cap of $1000000.00 reached: $0.00 used since 2026-10-01 (UTC)."},
		{"rounds half up", domain.Status{MonthStart: start, Spent: 1_005_000, Cap: micros(1_000_000)}, true, 0, true,
			"Monthly spend cap of $1.00 reached: $1.01 used since 2026-10-01 (UTC)."},
		{"rounds down below half", domain.Status{MonthStart: start, Spent: 1_004_999, Cap: micros(1_000_000)}, true, 0, true,
			"Monthly spend cap of $1.00 reached: $1.00 used since 2026-10-01 (UTC)."},
		{"sub-cent spend shows zero cents", domain.Status{MonthStart: start, Spent: 4_999, Cap: micros(0)}, true, 0, true,
			"Monthly spend cap of $0.00 reached: $0.00 used since 2026-10-01 (UTC)."},
		{"half a cent shows one cent", domain.Status{MonthStart: start, Spent: 5_000, Cap: micros(0)}, true, 0, true,
			"Monthly spend cap of $0.00 reached: $0.01 used since 2026-10-01 (UTC)."},
		{"saturated total (bigint maximum) does not overflow", domain.Status{MonthStart: start, Spent: math.MaxInt64, Cap: micros(1_000_000_000)}, true, 0, true,
			"Monthly spend cap of $1000.00 reached: $9223372036854.78 used since 2026-10-01 (UTC)."},
		{"rounding up carries into the dollars", domain.Status{MonthStart: start, Spent: 49_999_999, Cap: micros(0)}, true, 0, true,
			"Monthly spend cap of $0.00 reached: $50.00 used since 2026-10-01 (UTC)."},
		{"month start in another zone prints the UTC date", domain.Status{
			MonthStart: time.Date(2026, 10, 1, 2, 0, 0, 0, time.FixedZone("x", 2*3600)), Spent: 1_000_000, Cap: micros(1_000_000)}, true, 0, true,
			"Monthly spend cap of $1.00 reached: $1.00 used since 2026-10-01 (UTC)."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Reached(); got != tc.wantReached {
				t.Errorf("Reached() = %v, want %v", got, tc.wantReached)
			}
			got, ok := tc.status.Remaining()
			if got != tc.wantRemaining || ok != tc.wantHasCap {
				t.Errorf("Remaining() = (%d, %v), want (%d, %v)", got, ok, tc.wantRemaining, tc.wantHasCap)
			}
			if got := tc.status.Message(); got != tc.wantMessage {
				t.Errorf("Message() = %q, want %q", got, tc.wantMessage)
			}
		})
	}
}

// TestStatus_MessageSpecExample pins the message text of spec section 7 (FR13).
func TestStatus_MessageSpecExample(t *testing.T) {
	s := domain.Status{
		MonthStart: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Spent:      50_120_000,
		Cap:        micros(50_000_000),
	}
	const want = "Monthly spend cap of $50.00 reached: $50.12 used since 2026-10-01 (UTC)."
	if got := s.Message(); got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}
}

// TestStatus_Unpriced: the count of unknown-cost entries rides on the status; it neither changes
// whether the cap is reached nor the message (FR7, FR13).
func TestStatus_Unpriced(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	base := domain.Status{MonthStart: start, Spent: 18_400_000, Cap: micros(50_000_000)}
	withUnpriced := base
	withUnpriced.Unpriced = 3

	if withUnpriced.Unpriced != 3 {
		t.Errorf("Unpriced = %d, want 3", withUnpriced.Unpriced)
	}
	if base.Unpriced != 0 {
		t.Errorf("zero value Unpriced = %d, want 0", base.Unpriced)
	}
	if got, want := withUnpriced.Reached(), base.Reached(); got != want {
		t.Errorf("Reached() with Unpriced = %v, want %v", got, want)
	}
	if got, want := withUnpriced.Message(), base.Message(); got != want {
		t.Errorf("Message() with Unpriced = %q, want %q", got, want)
	}
	gotRem, gotOK := withUnpriced.Remaining()
	wantRem, wantOK := base.Remaining()
	if gotRem != wantRem || gotOK != wantOK {
		t.Errorf("Remaining() with Unpriced = (%d, %v), want (%d, %v)", gotRem, gotOK, wantRem, wantOK)
	}
}
