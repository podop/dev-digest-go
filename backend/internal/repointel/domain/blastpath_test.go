package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

func TestUsableIndexFlags(t *testing.T) {
	tests := []struct {
		name         string
		st           domain.IndexState
		wantDegraded bool
		wantReason   domain.DegradedReason
	}{
		{name: "full", st: domain.IndexState{Status: domain.StatusFull}},
		{name: "partial", st: domain.IndexState{Status: domain.StatusPartial}, wantDegraded: true, wantReason: domain.ReasonIndexPartial},
		{name: "bounded", st: domain.IndexState{Status: domain.StatusFull, Stats: map[string]any{"bounded": float64(3)}}, wantDegraded: true, wantReason: domain.ReasonRepoTooLarge},
		{name: "bounded wins over partial", st: domain.IndexState{Status: domain.StatusPartial, Stats: map[string]any{"bounded": float64(1)}}, wantDegraded: true, wantReason: domain.ReasonRepoTooLarge},
		{name: "bounded zero", st: domain.IndexState{Status: domain.StatusFull, Stats: map[string]any{"bounded": float64(0)}}},
		{name: "bounded not a number", st: domain.IndexState{Status: domain.StatusFull, Stats: map[string]any{"bounded": "7"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			degraded, reason := domain.UsableIndexFlags(tc.st)
			if degraded != tc.wantDegraded || reason != tc.wantReason {
				t.Errorf("UsableIndexFlags(%+v) = %v, %q, want %v, %q", tc.st, degraded, reason, tc.wantDegraded, tc.wantReason)
			}
		})
	}
}

func TestUnusableIndexReason(t *testing.T) {
	tests := []struct {
		name string
		st   domain.IndexState
		want domain.DegradedReason
	}{
		{name: "failed", st: domain.IndexState{Status: domain.StatusFailed, Stats: map[string]any{"degradedReason": "no_data"}}, want: domain.ReasonIndexFailed},
		{name: "degraded with a reason", st: domain.IndexState{Status: domain.StatusDegraded, Stats: map[string]any{"degradedReason": "repo_too_large"}}, want: domain.ReasonRepoTooLarge},
		{name: "degraded without a reason", st: domain.IndexState{Status: domain.StatusDegraded}, want: domain.ReasonIndexFailed},
		{name: "degraded with an unknown reason", st: domain.IndexState{Status: domain.StatusDegraded, Stats: map[string]any{"degradedReason": "boom"}}, want: domain.ReasonIndexFailed},
		{name: "unknown status", st: domain.IndexState{Status: "weird"}, want: domain.ReasonNoData},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.UnusableIndexReason(tc.st); got != tc.want {
				t.Errorf("UnusableIndexReason(%+v) = %q, want %q", tc.st, got, tc.want)
			}
		})
	}
	if !domain.UsableIndex(domain.IndexState{Status: domain.StatusPartial}) || domain.UsableIndex(domain.IndexState{Status: domain.StatusDegraded}) {
		t.Error("UsableIndex() must accept full and partial and nothing else")
	}
}

func TestEnclosing(t *testing.T) {
	e := domain.NewEnclosing([]domain.LineName{
		{Line: 10, Name: "second"}, {Line: 1, Name: "first"}, {Line: 10, Name: "twin"}, {Line: 5, Name: "Cls.method"}, {Line: 12, Name: "later"},
	})
	tests := []struct {
		line   int
		want   string
		wantOK bool
	}{
		{line: 0}, {line: 1, want: "first", wantOK: true}, {line: 9, want: "first", wantOK: true},
		{line: 10, want: "second", wantOK: true}, {line: 11, want: "second", wantOK: true}, {line: 12, want: "later", wantOK: true},
		{line: 999, want: "later", wantOK: true},
	}
	for _, tc := range tests {
		got, ok := e.At(tc.line)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("At(%d) = %q, %v, want %q, %v", tc.line, got, ok, tc.want, tc.wantOK)
		}
	}
	if got := domain.CallerLabel(e, "src/deep/file.ts", 0); got != "file.ts" {
		t.Errorf("CallerLabel(no enclosing) = %q, want the base name file.ts", got)
	}
	if got := domain.CallerLabel(domain.NewEnclosing(nil), "top.ts", 3); got != "top.ts" {
		t.Errorf("CallerLabel(no symbols, top.ts) = %q, want top.ts", got)
	}
	if got := domain.CallerLabel(domain.NewEnclosing(nil), "dir/", 3); got != "" {
		t.Errorf("CallerLabel(dir/) = %q, want the empty last segment like Node's split('/').pop()", got)
	}
}

func TestChangedSymbols(t *testing.T) {
	rows := []domain.SymbolInfo{
		{File: "a.ts", Name: "foo", Kind: "function"}, {File: "a.ts", Name: "Cls", Kind: "class"}, {File: "a.ts", Name: "Cls.run", Kind: "method"},
		{File: "a.ts", Name: "run", Kind: "method"}, {File: "a.ts", Name: "foo", Kind: "type"},
		{File: "b.ts", Name: "foo", Kind: "function"},
	}
	symbols, names := domain.ChangedSymbols(rows)
	wantSyms := []domain.BlastSymbol{
		{File: "a.ts", Name: "foo", Kind: "function"}, {File: "a.ts", Name: "Cls", Kind: "class"}, {File: "a.ts", Name: "run", Kind: "method"},
		{File: "b.ts", Name: "foo", Kind: "function"},
	}
	if diff := cmp.Diff(wantSyms, symbols); diff != "" {
		t.Errorf("ChangedSymbols() symbols mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"foo", "Cls", "run"}, names); diff != "" {
		t.Errorf("ChangedSymbols() names mismatch (-want +got):\n%s", diff)
	}
}

func TestAssembleCallers(t *testing.T) {
	byFile := map[string][]domain.LineName{
		"s/a.ts": {{Line: 1, Name: "alpha"}, {Line: 10, Name: "beta"}, {Line: 10, Name: "Cls.m"}},
		"s/b.ts": {{Line: 3, Name: "gamma"}},
	}
	t.Run("labels, dedupe and order", func(t *testing.T) {
		rows := []domain.ResolvedCaller{
			{File: "s/a.ts", ToSymbol: "x", Line: 4, Rank: 0.9},  // alpha
			{File: "s/a.ts", ToSymbol: "x", Line: 6, Rank: 0.9},  // alpha again: dropped
			{File: "s/a.ts", ToSymbol: "y", Line: 6, Rank: 0.9},  // alpha via y: kept
			{File: "s/a.ts", ToSymbol: "x", Line: 11, Rank: 0.9}, // beta
			{File: "s/b.ts", ToSymbol: "x", Line: 1, Rank: 0.5},  // no enclosing: base name
			{File: "s/c.ts", ToSymbol: "x", Line: 1, Rank: 0.7},  // no symbol rows: base name
		}
		want := []domain.BlastCaller{
			{File: "s/a.ts", Symbol: "alpha", ViaSymbol: "x", Line: 4, Rank: 0.9},
			{File: "s/a.ts", Symbol: "alpha", ViaSymbol: "y", Line: 6, Rank: 0.9},
			{File: "s/a.ts", Symbol: "beta", ViaSymbol: "x", Line: 11, Rank: 0.9},
			{File: "s/c.ts", Symbol: "c.ts", ViaSymbol: "x", Line: 1, Rank: 0.7},
			{File: "s/b.ts", Symbol: "b.ts", ViaSymbol: "x", Line: 1, Rank: 0.5},
		}
		if diff := cmp.Diff(want, domain.AssembleCallers(rows, byFile)); diff != "" {
			t.Errorf("AssembleCallers() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("at most 20 per changed symbol", func(t *testing.T) {
		var rows []domain.ResolvedCaller
		for i := range 30 {
			rows = append(rows, domain.ResolvedCaller{File: "f" + string(rune('a'+i)) + ".ts", ToSymbol: "x", Line: 1, Rank: float64(100 - i)})
		}
		rows = append(rows, domain.ResolvedCaller{File: "other.ts", ToSymbol: "y", Line: 1, Rank: 1})
		got := domain.AssembleCallers(rows, nil)
		perVia := map[string]int{}
		for _, c := range got {
			perVia[c.ViaSymbol]++
		}
		if perVia["x"] != domain.MaxCallersPerSymbol || perVia["y"] != 1 {
			t.Errorf("AssembleCallers() kept %v rows per symbol, want x: 20, y: 1", perVia)
		}
		if got[0].File != "fa.ts" || got[19].File != "ft.ts" {
			t.Errorf("AssembleCallers() kept %s..%s of x, want the 20 best-ranked fa.ts..ft.ts", got[0].File, got[19].File)
		}
	})
	t.Run("files of callers", func(t *testing.T) {
		got := domain.CallerFiles([]domain.BlastCaller{{File: "b"}, {File: "a"}, {File: "b"}})
		if diff := cmp.Diff([]string{"b", "a"}, got); diff != "" {
			t.Errorf("CallerFiles() mismatch (-want +got):\n%s", diff)
		}
	})
}
