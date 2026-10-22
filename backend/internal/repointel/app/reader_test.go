package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// readerStore is a store holding an indexed repo: states, ranks, symbols, edges and a map.
func readerStore() *fakeStore {
	s := newFakeStore()
	s.states[repoID] = domain.IndexState{RepoID: repoID, LastIndexedSHA: "sha1", IndexerVersion: 2, Status: domain.StatusFull}
	s.data.ranks = []domain.FileRankRow{
		{Path: "src/core.ts", Rank: 0.5, Percentile: 100},
		{Path: "src/core.test.ts", Rank: 0.4, Percentile: 80},
		{Path: "src/util.ts", Rank: 0.3, Percentile: 60},
		{Path: "src/api/handler.ts", Rank: 0.2, Percentile: 40},
		{Path: "src/leaf.ts", Rank: 0.1, Percentile: 20},
	}
	s.data.symbols = []domain.SymbolRow{{Path: "src/core.ts", Name: "core", Kind: "function", Line: 3, EndLine: 9, Exported: true, Signature: "function core()"}}
	s.data.edges = []domain.Edge{{From: "src/core.ts", To: "src/util.ts"}, {From: "src/util.ts", To: "src/leaf.ts"}}
	s.data.maps = []domain.RepoMap{{CommitSHA: "sha1", Budget: 1500, Text: "the map", Tokens: 2}}
	return s
}

func TestReader_RepoMap(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		budget  int
		setup   func(s *fakeStore)
		want    domain.RepoMapResult
	}{
		{name: "cached map", enabled: true, want: domain.RepoMapResult{Text: "the map", Tokens: 2, Cached: true}},
		{name: "explicit default budget", enabled: true, budget: 1500, want: domain.RepoMapResult{Text: "the map", Tokens: 2, Cached: true}},
		{name: "flag off", want: domain.RepoMapResult{Degraded: true, Reason: domain.ReasonFlagOff}},
		{name: "other budget misses", enabled: true, budget: 2000, want: domain.RepoMapResult{Degraded: true, Reason: domain.ReasonNoData}},
		{name: "no state row", enabled: true, setup: func(s *fakeStore) { delete(s.states, repoID) }, want: domain.RepoMapResult{Degraded: true, Reason: domain.ReasonNoData}},
		{name: "empty sha", enabled: true, setup: func(s *fakeStore) {
			st := s.states[repoID]
			st.LastIndexedSHA = ""
			s.states[repoID] = st
		}, want: domain.RepoMapResult{Degraded: true, Reason: domain.ReasonNoData}},
		{name: "map of another commit", enabled: true, setup: func(s *fakeStore) { s.data.maps[0].CommitSHA = "sha0" }, want: domain.RepoMapResult{Degraded: true, Reason: domain.ReasonNoData}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := readerStore()
			if tc.setup != nil {
				tc.setup(s)
			}
			got, err := app.NewReader(s, tc.enabled).RepoMap(t.Context(), repoID, tc.budget)
			if err != nil {
				t.Fatalf("RepoMap() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RepoMap(%d) mismatch (-want +got):\n%s", tc.budget, diff)
			}
		})
	}
}

func TestReader_ReadsWithTheFlagOn(t *testing.T) {
	r := app.NewReader(readerStore(), true)
	ctx := t.Context()

	ranks, err := r.FileRank(ctx, repoID, []string{"src/core.ts", "src/missing.ts"})
	if err != nil || len(ranks) != 1 || ranks[0].Percentile != 100 {
		t.Errorf("FileRank() = %v, %v, want only src/core.ts at 100", ranks, err)
	}
	syms, err := r.SymbolsInFiles(ctx, repoID, []string{"src/core.ts"})
	if err != nil || len(syms) != 1 || syms[0].Name != "core" || syms[0].Signature == nil || *syms[0].Signature != "function core()" {
		t.Errorf("SymbolsInFiles() = %+v, %v, want the core symbol", syms, err)
	}
	top, err := r.TopFilesByRank(ctx, repoID, 3, []string{"util"})
	if err != nil {
		t.Fatalf("TopFilesByRank() error = %v", err)
	}
	// core.test.ts is junk, util.ts is excluded.
	if diff := cmp.Diff([]string{"src/core.ts", "src/api/handler.ts", "src/leaf.ts"}, top); diff != "" {
		t.Errorf("TopFilesByRank(3, [util]) mismatch (-want +got):\n%s", diff)
	}
	samples, err := r.ConventionSamples(ctx, repoID, 2)
	if err != nil || cmp.Diff([]string{"src/core.ts", "src/util.ts"}, samples) != "" {
		t.Errorf("ConventionSamples(2) = %v, %v, want core and util", samples, err)
	}
	chains, err := r.CriticalPaths(ctx, repoID)
	if err != nil {
		t.Fatalf("CriticalPaths() error = %v", err)
	}
	if len(chains) == 0 || cmp.Diff([]string{"src/core.ts", "src/util.ts", "src/leaf.ts"}, chains[0]) != "" {
		t.Errorf("CriticalPaths() = %v, want the chain core -> util -> leaf first", chains)
	}
	all, err := r.ListIndexedFiles(ctx, repoID)
	if err != nil || len(all) != 5 || all[0] != "src/core.ts" || all[1] != "src/core.test.ts" {
		t.Errorf("ListIndexedFiles() = %v, %v, want all 5 paths, best first, unfiltered", all, err)
	}
}

func TestReader_NeverFailsForAMissingIndexOrTheFlagOff(t *testing.T) {
	tests := []struct {
		name  string
		store *fakeStore
		on    bool
	}{
		{name: "flag off", store: readerStore()},
		{name: "no index", store: newFakeStore(), on: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := app.NewReader(tc.store, tc.on)
			ctx := t.Context()
			ranks, e1 := r.FileRank(ctx, repoID, []string{"a.ts"})
			syms, e2 := r.SymbolsInFiles(ctx, repoID, []string{"a.ts"})
			top, e3 := r.TopFilesByRank(ctx, repoID, 5, nil)
			samples, e4 := r.ConventionSamples(ctx, repoID, 12)
			chains, e5 := r.CriticalPaths(ctx, repoID)
			files, e6 := r.ListIndexedFiles(ctx, repoID)
			if err := errors.Join(e1, e2, e3, e4, e5, e6); err != nil {
				t.Fatalf("a read failed: %v", err)
			}
			if len(ranks)+len(syms)+len(top)+len(samples)+len(chains)+len(files) != 0 {
				t.Errorf("results = %v %v %v %v %v %v, want all empty", ranks, syms, top, samples, chains, files)
			}
			for name, s := range map[string]bool{"ranks": ranks == nil, "syms": syms == nil, "top": top == nil,
				"samples": samples == nil, "chains": chains == nil, "files": files == nil} {
				if s {
					t.Errorf("%s is nil, want an empty non-nil slice (encodes as [])", name)
				}
			}
		})
	}
}

func TestReader_EmptyInputsAndNonPositiveCounts(t *testing.T) {
	r := app.NewReader(readerStore(), true)
	if got, err := r.FileRank(t.Context(), repoID, nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("FileRank(nil) = %v, %v, want an empty slice", got, err)
	}
	if got, err := r.SymbolsInFiles(t.Context(), repoID, nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("SymbolsInFiles(nil) = %v, %v, want an empty slice", got, err)
	}
	for _, n := range []int{0, -3} {
		if got, err := r.TopFilesByRank(t.Context(), repoID, n, nil); err != nil || got == nil || len(got) != 0 {
			t.Errorf("TopFilesByRank(%d) = %v, %v, want an empty slice", n, got, err)
		}
	}
}

func TestReader_DatabaseErrorsAreReturned(t *testing.T) {
	boom := errors.New("connection reset")
	for _, method := range []string{"GetState", "FileRanks", "SymbolsInFiles", "RankedPaths", "Edges"} {
		s := readerStore()
		s.hooks[method] = func(context.Context) error { return boom }
		r := app.NewReader(s, true)
		ctx := t.Context()
		_, e1 := r.RepoMap(ctx, repoID, 0)
		_, e2 := r.FileRank(ctx, repoID, []string{"a"})
		_, e3 := r.SymbolsInFiles(ctx, repoID, []string{"a"})
		_, e4 := r.TopFilesByRank(ctx, repoID, 5, nil)
		_, e5 := r.CriticalPaths(ctx, repoID)
		_, e6 := r.ListIndexedFiles(ctx, repoID)
		if err := errors.Join(e1, e2, e3, e4, e5, e6); !errors.Is(err, boom) {
			t.Errorf("hook %s: joined errors = %v, want the database error among them", method, err)
		}
	}
}
