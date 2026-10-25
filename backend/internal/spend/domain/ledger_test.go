package domain_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"dev-digest-go/backend/internal/spend/domain"
)

func usd(v float64) *float64 { return &v }

func TestParseKind(t *testing.T) {
	valid := []domain.Kind{
		domain.KindReviewRun, domain.KindReviewIntent, domain.KindIntentRefresh, domain.KindBrief,
		domain.KindOnboarding, domain.KindConventionsScan, domain.KindEvalCase, domain.KindCIRun,
	}
	if len(valid) != 8 {
		t.Fatalf("kinds = %d, want 8 (FR1, FR6)", len(valid))
	}
	for _, k := range valid {
		t.Run(string(k), func(t *testing.T) {
			got, err := domain.ParseKind(string(k))
			if err != nil || got != k {
				t.Errorf("ParseKind(%q) = (%q, %v), want (%q, nil)", k, got, err, k)
			}
		})
	}
	for _, in := range []string{"", "Review_Run", "review", "review_run ", "agent_run", "intent"} {
		t.Run("invalid "+in, func(t *testing.T) {
			got, err := domain.ParseKind(in)
			if !errors.Is(err, domain.ErrInvalidKind) || got != "" {
				t.Errorf("ParseKind(%q) = (%q, %v), want (\"\", ErrInvalidKind)", in, got, err)
			}
		})
	}
}

func TestParseOrigin(t *testing.T) {
	for _, o := range []domain.Origin{domain.OriginLive, domain.OriginCIIngest, domain.OriginBackfill} {
		got, err := domain.ParseOrigin(string(o))
		if err != nil || got != o {
			t.Errorf("ParseOrigin(%q) = (%q, %v), want (%q, nil)", o, got, err, o)
		}
	}
	for _, in := range []string{"", "Live", "ci", "import"} {
		if got, err := domain.ParseOrigin(in); !errors.Is(err, domain.ErrInvalidOrigin) || got != "" {
			t.Errorf("ParseOrigin(%q) = (%q, %v), want (\"\", ErrInvalidOrigin)", in, got, err)
		}
	}
}

func TestCIDedupeKey(t *testing.T) {
	tests := []struct {
		run     int64
		attempt int
		want    string
	}{
		{123456789, 1, "ci:123456789:1"},
		{123456789, 2, "ci:123456789:2"},
		{9_000_000_000_000, 10, "ci:9000000000000:10"},
	}
	for _, tc := range tests {
		if got := domain.CIDedupeKey(tc.run, tc.attempt); got != tc.want {
			t.Errorf("CIDedupeKey(%d, %d) = %q, want %q", tc.run, tc.attempt, got, tc.want)
		}
	}
	if domain.CIDedupeKey(1, 12) == domain.CIDedupeKey(11, 2) {
		t.Error("CIDedupeKey(1, 12) equals CIDedupeKey(11, 2): keys collide")
	}
}

// TestCostFromUSD covers FR3 (6 decimals, unknown on bad input), EC21 and D15 (half away from
// zero).
func TestCostFromUSD(t *testing.T) {
	tests := []struct {
		name string
		in   *float64
		want int64 // -1 = unknown (nil)
	}{
		{"nil is unknown", nil, -1},
		{"NaN is unknown (EC21)", usd(math.NaN()), -1},
		{"+Inf is unknown", usd(math.Inf(1)), -1},
		{"-Inf is unknown", usd(math.Inf(-1)), -1},
		{"negative is unknown (EC21)", usd(-0.01), -1},
		{"tiny negative is unknown", usd(-1e-12), -1},
		{"1e12 is unknown", usd(1e12), -1},
		{"above 1e12 is unknown", usd(5e15), -1},
		{"MaxFloat64 is unknown", usd(math.MaxFloat64), -1},
		{"zero is a known zero", usd(0), 0},
		{"negative zero is a known zero", usd(math.Copysign(0, -1)), 0},
		{"one micro", usd(0.000001), 1},
		{"typical cost", usd(0.0123), 12_300},
		{"whole dollars", usd(50), 50_000_000},
		{"sum artefact 0.1+0.2", usd(0.1 + 0.2), 300_000},
		{"half micro rounds up", usd(0.0000005), 1},
		{"just below half micro rounds down", usd(0.0000004999), 0},
		{"1.2345675 rounds up", usd(1.2345675), 1_234_568},
		{"1.2345674 rounds down", usd(1.2345674), 1_234_567},
		{"seven decimals above half", usd(0.0000015), 2},
		{"carry into the whole part", usd(0.9999995), 1_000_000},
		{"tiny is zero", usd(1e-9), 0},
		{"largest accepted", usd(math.Nextafter(1e12, 0)), 999_999_999_999_999_900},
		{"rounding at the bound stays accepted", usd(999_999_999_999.5), 999_999_999_999_500_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.CostFromUSD(tc.in)
			if tc.want < 0 {
				if got != nil {
					t.Errorf("CostFromUSD(%v) = %d, want nil", *tc.in, *got)
				}
				return
			}
			if got == nil || int64(*got) != tc.want {
				t.Errorf("CostFromUSD(%v) = %v, want %d", *tc.in, got, tc.want)
			}
		})
	}
}

// TestCostFromUSD_RoundTrip: every whole-micro amount survives USD() -> CostFromUSD, the path a
// priced cost takes from the call site into the ledger (NFR5).
func TestCostFromUSD_RoundTrip(t *testing.T) {
	for _, m := range []domain.Micros{0, 1, 2, 3, 7, 999_999, 1_000_000, 1_234_567, 50_120_000, 123_456_789_012, 999_999_999_999_999_999} {
		f := m.USD()
		got := domain.CostFromUSD(&f)
		// float64 holds ~15-17 significant digits; beyond that the micro digit is lost in the float.
		if m < 1_000_000_000_000_000 && (got == nil || *got != m) {
			t.Errorf("CostFromUSD(Micros(%d).USD()) = %v, want %d", m, got, m)
		}
	}
}

// TestCostFromUSD_SumIsExact: costs are summed as integers, so many small costs add up with no
// drift (NFR5).
func TestCostFromUSD_SumIsExact(t *testing.T) {
	var sum domain.Micros
	for range 10_000 {
		c := 0.0001
		sum += *domain.CostFromUSD(&c)
	}
	if want := domain.Micros(1_000_000); sum != want {
		t.Errorf("sum of 10000 x $0.0001 = %d micros, want %d", sum, want)
	}
}

// TestMonthBoundary_Entry: an entry's month is the UTC month of its time; 00:00:00.000 on the 1st
// is the new month, 23:59:59.999 on the last day the old one (EC11).
func TestMonthBoundary_Entry(t *testing.T) {
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	plus2 := time.FixedZone("plus2", 2*3600)
	tests := []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"first instant of November", nov, nov},
		{"last millisecond of October", nov.Add(-time.Millisecond), oct},
		{"first instant of October", oct, oct},
		{"local time already on the 1st, UTC still on the last day", time.Date(2026, 11, 1, 1, 59, 59, 999e6, plus2), oct},
		{"local time on the 1st, UTC on the 1st", time.Date(2026, 11, 1, 2, 0, 0, 0, plus2), nov},
	}
	for _, tc := range tests {
		e := domain.Entry{OccurredAt: tc.at}
		if got := domain.MonthStart(e.OccurredAt); !got.Equal(tc.want) {
			t.Errorf("%s: MonthStart(%v) = %v, want %v", tc.name, tc.at, got, tc.want)
		}
	}
}
