package integration_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

// AC3061: the read port over an indexed fixture answers section 7.3 with the fixture's values.
//
// The fixture's import graph: src/a, b, c and the test import src/core; src/c imports src/a;
// src/server imports src/b. So core ranks first, b (fed by the entry point server) above a (fed
// by c, which also feeds core), and the files nothing imports tie below them in path order.
func TestReader_AnswersSection73(t *testing.T) {
	e := newEnv(t)
	e.mustFull()
	ctx := t.Context()

	t.Run("ListIndexedFiles", func(t *testing.T) {
		got, err := e.reader.ListIndexedFiles(ctx, e.repo)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			"src/core.ts", "src/b.ts", "src/a.ts",
			"db/migrations/01.ts", "src/c.ts", "src/core.test.ts", "src/server.ts", "src/types.d.ts",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("ListIndexedFiles() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("TopFilesByRank skips junk and the excluded", func(t *testing.T) {
		got, err := e.reader.TopFilesByRank(ctx, e.repo, 5, nil)
		if err != nil {
			t.Fatal(err)
		}
		// The test, the declaration and the migration are junk (section 7.3).
		if diff := cmp.Diff([]string{"src/core.ts", "src/b.ts", "src/a.ts", "src/c.ts", "src/server.ts"}, got); diff != "" {
			t.Errorf("TopFilesByRank(5, nil) mismatch (-want +got):\n%s", diff)
		}
		got, err = e.reader.TopFilesByRank(ctx, e.repo, 3, []string{"src/b.", "core"})
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"src/a.ts", "src/c.ts", "src/server.ts"}, got); diff != "" {
			t.Errorf("TopFilesByRank(3, [src/b., core]) mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("ConventionSamples", func(t *testing.T) {
		got, err := e.reader.ConventionSamples(ctx, e.repo, 3)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"src/core.ts", "src/b.ts", "src/a.ts"}, got); diff != "" {
			t.Errorf("ConventionSamples(3) mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("FileRank", func(t *testing.T) {
		got, err := e.reader.FileRank(ctx, e.repo, []string{"src/core.ts", "missing.ts"})
		if err != nil {
			t.Fatal(err)
		}
		// The best of 8 files has percentile 100; a path without a row is left out.
		if diff := cmp.Diff([]domain.PathPercentile{{Path: "src/core.ts", Percentile: 100}}, got); diff != "" {
			t.Errorf("FileRank() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("SymbolsInFiles", func(t *testing.T) {
		got, err := e.reader.SymbolsInFiles(ctx, e.repo, []string{"src/c.ts", "src/core.ts"})
		if err != nil {
			t.Fatal(err)
		}
		sig := func(s string) *string { return &s }
		want := []domain.SymbolInfo{
			{File: "src/c.ts", Name: "runC", Kind: "function", Exported: true, StartLine: 4, EndLine: 6, Signature: sig("function runC(): number")},
			{File: "src/core.ts", Name: "core", Kind: "function", Exported: true, StartLine: 1, EndLine: 3, Signature: sig("function core(): number")},
			{File: "src/core.ts", Name: "Store", Kind: "class", Exported: true, StartLine: 5, EndLine: 5, Signature: sig("class Store")},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("SymbolsInFiles() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("CriticalPaths", func(t *testing.T) {
		got, err := e.reader.CriticalPaths(ctx, e.repo)
		if err != nil {
			t.Fatal(err)
		}
		// The five roots are core, b, a, the migration and c (nothing imports the last two, so
		// they tie in path order); a chain follows the best-ranked import, and core, which every
		// importer prefers, has none. A chain shorter than two files (core, the migration) is
		// dropped.
		want := [][]string{
			{"src/b.ts", "src/core.ts"},
			{"src/a.ts", "src/core.ts"},
			{"src/c.ts", "src/core.ts"},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("CriticalPaths() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("RepoMap", func(t *testing.T) {
		got, err := e.reader.RepoMap(ctx, e.repo, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Cached || got.Degraded || got.Tokens <= 0 || got.Tokens > domain.DefaultTokenBudget {
			t.Errorf("RepoMap() = %+v, want a cached, non-degraded map within the budget", got)
		}
		if !strings.HasPrefix(got.Text, mapHeader+"src/core.ts:\n  function core(): number\n") {
			t.Errorf("RepoMap().Text starts %.120q, want the header and the best file first", got.Text)
		}
		stored := e.scanStrings(`SELECT map_text FROM repo_map_cache WHERE repo_id = ?`, e.repo)
		if diff := cmp.Diff(stored, []string{got.Text}); diff != "" {
			t.Errorf("RepoMap().Text differs from the cached row (-stored +got):\n%s", diff)
		}
		other, err := e.reader.RepoMap(ctx, e.repo, 100)
		if err != nil || !other.Degraded || other.Reason != domain.ReasonNoData {
			t.Errorf("RepoMap(budget 100) = %+v, %v, want degraded no_data: only the 1500 map is cached", other, err)
		}
	})
}
