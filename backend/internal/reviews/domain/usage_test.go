package domain_test

import (
	"math"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

func TestUsageMeter_StartsAtZeroWithAKnownCost(t *testing.T) {
	m := domain.NewUsageMeter()
	got := m.Snapshot()
	if got.TokensIn != 0 || got.TokensOut != 0 || got.CostUSD == nil || *got.CostUSD != 0 || m.Calls() != 0 {
		t.Errorf("Snapshot() = %+v (cost %v), calls %d, want zero tokens, cost 0, no calls", got, got.CostUSD, m.Calls())
	}
}

func TestUsageMeter_SumsResponses(t *testing.T) {
	m := domain.NewUsageMeter()
	m.Add(100, 10, ptr(0.001))
	m.Add(50, 5, ptr(0.0005))
	got := m.Snapshot()
	if got.TokensIn != 150 || got.TokensOut != 15 || m.Calls() != 2 {
		t.Errorf("Snapshot() tokens = %d/%d, calls %d, want 150/15, 2", got.TokensIn, got.TokensOut, m.Calls())
	}
	if got.CostUSD == nil || math.Abs(*got.CostUSD-0.0015) > 1e-12 {
		t.Errorf("Snapshot().CostUSD = %v, want 0.0015", got.CostUSD)
	}
}

func TestUsageMeter_OneUnpricedResponseMakesTheCostUnknownForGood(t *testing.T) {
	m := domain.NewUsageMeter()
	m.Add(1, 1, ptr(0.1))
	m.Add(1, 1, nil)
	m.Add(1, 1, ptr(0.1))
	got := m.Snapshot()
	if got.CostUSD != nil {
		t.Errorf("Snapshot().CostUSD = %v, want nil", *got.CostUSD)
	}
	if got.TokensIn != 3 || got.TokensOut != 3 {
		t.Errorf("Snapshot() tokens = %d/%d, want 3/3", got.TokensIn, got.TokensOut)
	}
}

func TestUsageMeter_SnapshotIsACopy(t *testing.T) {
	m := domain.NewUsageMeter()
	m.Add(1, 1, ptr(0.5))
	s := m.Snapshot()
	*s.CostUSD = 99
	if got := *m.Snapshot().CostUSD; got != 0.5 {
		t.Errorf("cost after mutating a snapshot = %v, want 0.5", got)
	}
}

func TestUsageMeter_ConcurrentAdds(t *testing.T) {
	m := domain.NewUsageMeter()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			m.Add(2, 1, ptr(0.25))
			_ = m.Snapshot()
		})
	}
	wg.Wait()
	got := m.Snapshot()
	if got.TokensIn != 100 || got.TokensOut != 50 || m.Calls() != 50 || got.CostUSD == nil || *got.CostUSD != 12.5 {
		t.Errorf("Snapshot() = %+v, calls %d, want 100/50, 50 calls, cost 12.5", got, m.Calls())
	}
}

func TestNoUsage(t *testing.T) {
	want := reviewer.Usage{CostUSD: ptr(0.0)}
	if diff := cmp.Diff(want, domain.NoUsage()); diff != "" {
		t.Errorf("NoUsage() mismatch (-want +got):\n%s", diff)
	}
}

func TestEstimateTokens(t *testing.T) {
	emoji := string(rune(0x1F600)) // two UTF-16 units
	tests := []struct {
		in        string
		wantUnits int
		wantToks  int
	}{
		{"", 0, 0},
		{"abc", 3, 1},
		{"abcd", 4, 1},
		{"abcde", 5, 2},
		{emoji, 2, 1},
		{emoji + emoji + "a", 5, 2},
		{"é中", 2, 1},
	}
	for _, tc := range tests {
		if got := domain.UTF16Units(tc.in); got != tc.wantUnits {
			t.Errorf("UTF16Units(%q) = %d, want %d", tc.in, got, tc.wantUnits)
		}
		if got := domain.EstimateTokens(tc.in); got != tc.wantToks {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.in, got, tc.wantToks)
		}
	}
}

func TestCompareUTF16(t *testing.T) {
	emoji := string(rune(0x1F600))  // D83D DE00: sorts before U+E000..U+FFFF
	private := string(rune(0xE000)) // UTF-8 byte order puts it before the emoji
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"equal", "a/b.go", "a/b.go", 0},
		{"shorter prefix first", "a", "ab", -1},
		{"longer prefix last", "ab", "a", 1},
		{"plain ascii", "a.go", "b.go", -1},
		{"supplementary before U+E000 in UTF-16", emoji, private, -1},
		{"U+E000 after supplementary", private, emoji, 1},
		{"empty first", "", "a", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CompareUTF16(tc.a, tc.b); got != tc.want {
				t.Errorf("CompareUTF16(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
