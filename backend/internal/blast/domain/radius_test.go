package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/blast/domain"
)

func TestBuildRadius_MappingCorpus(t *testing.T) {
	res := domain.PortResult{
		ChangedSymbols: []domain.ChangedSymbol{
			{Name: "charge", File: "src/pay.ts", Kind: "function"},
			{Name: "refund", File: "src/pay.ts", Kind: "function"},
			{Name: "unused", File: "src/pay.ts", Kind: "const"},
		},
		Callers: []domain.Caller{
			// a reference inside the file that declares the symbol is dropped
			{File: "src/pay.ts", Symbol: "refund", ViaSymbol: "charge", Line: 5, Rank: 9},
			// the same name referenced from another file is kept
			{File: "src/api.ts", Symbol: "postCharge", ViaSymbol: "charge", Line: 12, Rank: 2},
			// duplicate (symbol, file, line): the first one (rank 2) is kept
			{File: "src/api.ts", Symbol: "postCharge", ViaSymbol: "charge", Line: 12, Rank: 8},
			{File: "src/cron.ts", Symbol: "nightly", ViaSymbol: "charge", Line: 3, Rank: 2},
			{File: "src/Api.ts", Symbol: "postCharge", ViaSymbol: "charge", Line: 20, Rank: 2},
			// refund: one caller, higher rank than charge's callers
			{File: "src/api.ts", Symbol: "postRefund", ViaSymbol: "refund", Line: 30, Rank: 5},
			// the same caller reaches a second symbol: counted once in the summary
			{File: "src/api.ts", Symbol: "postCharge", ViaSymbol: "refund", Line: 12, Rank: 1},
		},
		FactsByFile: map[string]domain.Facts{
			"src/api.ts":  {Endpoints: []string{"POST /charge", "POST /refund"}},
			"src/cron.ts": {Endpoints: []string{"POST /charge"}, Crons: []string{"0 0 * * *"}},
		},
	}
	got := domain.BuildRadius(res)
	want := domain.BlastRadius{
		ChangedSymbols: res.ChangedSymbols,
		Downstream: []domain.Downstream{
			{
				Symbol:            "refund",
				Callers:           []domain.CallerRef{{Name: "postRefund", File: "src/api.ts", Line: 30}, {Name: "postCharge", File: "src/api.ts", Line: 12}},
				EndpointsAffected: []string{"POST /charge", "POST /refund"},
				CronsAffected:     []string{},
			},
			{
				Symbol: "charge",
				// rank 2 for all three: name ascending (nightly after postCharge), then file
				// (English collation: "src/api.ts" before "src/Api.ts"), then line.
				Callers: []domain.CallerRef{
					{Name: "nightly", File: "src/cron.ts", Line: 3},
					{Name: "postCharge", File: "src/api.ts", Line: 12},
					{Name: "postCharge", File: "src/Api.ts", Line: 20},
				},
				EndpointsAffected: []string{"POST /charge", "POST /refund"},
				CronsAffected:     []string{"0 0 * * *"},
			},
		},
		Summary: "3 changed symbols · 4 callers · 2 endpoints · 1 cron",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildRadius() mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildRadius_EmptyAndDegradedAnswers(t *testing.T) {
	tests := []struct {
		name string
		in   domain.PortResult
		want domain.BlastRadius
	}{
		{
			name: "no data",
			in:   domain.PortResult{Degraded: true, Reason: domain.ReasonNoData},
			want: domain.BlastRadius{
				ChangedSymbols: []domain.ChangedSymbol{}, Downstream: []domain.Downstream{},
				Summary: "0 changed symbols · 0 callers · 0 endpoints · 0 crons", Degraded: true, Reason: domain.ReasonNoData,
			},
		},
		{
			name: "complete answer without callers",
			in:   domain.PortResult{ChangedSymbols: []domain.ChangedSymbol{{Name: "f", File: "a.ts", Kind: "function"}}},
			want: domain.BlastRadius{
				ChangedSymbols: []domain.ChangedSymbol{{Name: "f", File: "a.ts", Kind: "function"}}, Downstream: []domain.Downstream{},
				Summary: "1 changed symbol · 0 callers · 0 endpoints · 0 crons",
			},
		},
		{
			name: "clone scan callers carry no facts and rank 0",
			in: domain.PortResult{
				ChangedSymbols: []domain.ChangedSymbol{{Name: "f", File: "a.ts", Kind: "function"}},
				Callers: []domain.Caller{
					{File: "z.ts", Symbol: "z", ViaSymbol: "f", Line: 1},
					{File: "b.ts", Symbol: "b", ViaSymbol: "f", Line: 2},
				},
				Degraded: true, Reason: domain.ReasonIndexPartial,
			},
			want: domain.BlastRadius{
				ChangedSymbols: []domain.ChangedSymbol{{Name: "f", File: "a.ts", Kind: "function"}},
				Downstream: []domain.Downstream{{
					Symbol:            "f",
					Callers:           []domain.CallerRef{{Name: "b", File: "b.ts", Line: 2}, {Name: "z", File: "z.ts", Line: 1}},
					EndpointsAffected: []string{}, CronsAffected: []string{},
				}},
				Summary:  "1 changed symbol · 2 callers · 0 endpoints · 0 crons",
				Degraded: true, Reason: domain.ReasonIndexPartial,
			},
		},
		{
			name: "every caller is in a declaring file",
			in: domain.PortResult{
				ChangedSymbols: []domain.ChangedSymbol{{Name: "f", File: "a.ts", Kind: "function"}},
				Callers:        []domain.Caller{{File: "a.ts", Symbol: "g", ViaSymbol: "f", Line: 1, Rank: 1}},
			},
			want: domain.BlastRadius{
				ChangedSymbols: []domain.ChangedSymbol{{Name: "f", File: "a.ts", Kind: "function"}}, Downstream: []domain.Downstream{},
				Summary: "1 changed symbol · 0 callers · 0 endpoints · 0 crons",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.BuildRadius(tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BuildRadius() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildRadius_GroupOrder(t *testing.T) {
	tests := []struct {
		name    string
		callers []domain.Caller
		want    []string
	}{
		{
			name: "highest rank first",
			callers: []domain.Caller{
				{File: "x.ts", Symbol: "s", ViaSymbol: "low", Line: 1, Rank: 1},
				{File: "x.ts", Symbol: "s", ViaSymbol: "high", Line: 1, Rank: 3},
				{File: "y.ts", Symbol: "s", ViaSymbol: "low", Line: 1, Rank: 2},
			},
			want: []string{"high", "low"},
		},
		{
			name: "a group ranks by its best caller",
			callers: []domain.Caller{
				{File: "x.ts", Symbol: "s", ViaSymbol: "a", Line: 1, Rank: 1},
				{File: "y.ts", Symbol: "s", ViaSymbol: "b", Line: 1, Rank: 5},
				{File: "z.ts", Symbol: "s", ViaSymbol: "a", Line: 1, Rank: 9},
			},
			want: []string{"a", "b"},
		},
		{
			name: "equal rank: English collation of the symbol",
			callers: []domain.Caller{
				{File: "x.ts", Symbol: "s", ViaSymbol: "b", Line: 1, Rank: 1},
				{File: "x.ts", Symbol: "s", ViaSymbol: "B", Line: 2, Rank: 1},
				{File: "x.ts", Symbol: "s", ViaSymbol: "a", Line: 3, Rank: 1},
				{File: "x.ts", Symbol: "s", ViaSymbol: "_z", Line: 4, Rank: 1},
				{File: "x.ts", Symbol: "s", ViaSymbol: "é", Line: 5, Rank: 1},
			},
			want: []string{"_z", "a", "b", "B", "é"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.BuildRadius(domain.PortResult{Callers: tc.callers})
			var order []string
			for _, d := range got.Downstream {
				order = append(order, d.Symbol)
			}
			if diff := cmp.Diff(tc.want, order); diff != "" {
				t.Errorf("BuildRadius() group order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSummary_Plurals(t *testing.T) {
	one := []domain.Downstream{{
		Symbol:            "f",
		Callers:           []domain.CallerRef{{Name: "g", File: "a.ts", Line: 1}},
		EndpointsAffected: []string{"GET /a"},
		CronsAffected:     []string{"* * * * *"},
	}}
	tests := []struct {
		name    string
		changed int
		down    []domain.Downstream
		want    string
	}{
		{"all singular", 1, one, "1 changed symbol · 1 caller · 1 endpoint · 1 cron"},
		{"all zero", 0, nil, "0 changed symbols · 0 callers · 0 endpoints · 0 crons"},
		{
			"counts are distinct across groups", 12,
			[]domain.Downstream{
				{Symbol: "f", Callers: []domain.CallerRef{{Name: "g", File: "a.ts", Line: 1}, {Name: "h", File: "a.ts", Line: 2}}, EndpointsAffected: []string{"GET /a", "GET /b"}},
				{Symbol: "k", Callers: []domain.CallerRef{{Name: "g", File: "a.ts", Line: 1}}, EndpointsAffected: []string{"GET /a"}, CronsAffected: []string{"x", "y"}},
			},
			"12 changed symbols · 2 callers · 2 endpoints · 2 crons",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.Summary(tc.changed, tc.down); got != tc.want {
				t.Errorf("Summary(%d, ...) = %q, want %q", tc.changed, got, tc.want)
			}
		})
	}
}
