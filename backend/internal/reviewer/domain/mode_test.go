package domain_test

import (
	"testing"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func diffOf(lines ...int) domain.Diff {
	var d domain.Diff
	for _, n := range lines {
		d.Files = append(d.Files, domain.DiffFile{Additions: n})
	}
	return d
}

// AC4042
func TestSelectMode(t *testing.T) {
	tests := []struct {
		name     string
		strategy domain.Strategy
		diff     domain.Diff
		want     domain.Mode
	}{
		{"auto, 1 file x 500 lines", domain.StrategyAuto, diffOf(500), domain.ModeSinglePass},
		{"auto, 2 files x 150 lines", domain.StrategyAuto, diffOf(150, 150), domain.ModeSinglePass},
		{"auto, 2 files x 250 lines", domain.StrategyAuto, diffOf(250, 250), domain.ModeMapReduce},
		{"auto, exactly the threshold", domain.StrategyAuto, diffOf(200, 200), domain.ModeSinglePass},
		{"auto, one line above the threshold", domain.StrategyAuto, diffOf(200, 201), domain.ModeMapReduce},
		{"auto counts deletions", domain.StrategyAuto, domain.Diff{Files: []domain.DiffFile{{Deletions: 300}, {Additions: 101}}}, domain.ModeMapReduce},
		{"map-reduce with 1 file", domain.StrategyMapReduce, diffOf(1000), domain.ModeSinglePass},
		{"map-reduce with 2 small files", domain.StrategyMapReduce, diffOf(1, 1), domain.ModeMapReduce},
		{"map-reduce with no file", domain.StrategyMapReduce, domain.Diff{}, domain.ModeSinglePass},
		{"single-pass is always single-pass", domain.StrategySinglePass, diffOf(900, 900, 900), domain.ModeSinglePass},
		{"an unknown strategy behaves like auto", domain.Strategy("bogus"), diffOf(250, 250), domain.ModeMapReduce},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.SelectMode(tc.strategy, tc.diff, domain.MapThresholdLines); got != tc.want {
				t.Errorf("SelectMode(%q, diff, %d) = %q, want %q", tc.strategy, domain.MapThresholdLines, got, tc.want)
			}
		})
	}
}

func TestEffectiveMode(t *testing.T) {
	tests := []struct {
		selected domain.Mode
		chunks   int
		want     domain.Mode
	}{
		{domain.ModeSinglePass, 1, domain.ModeSinglePass},
		{domain.ModeSinglePass, 3, domain.ModeMapReduce},
		{domain.ModeMapReduce, 1, domain.ModeMapReduce},
		{domain.ModeMapReduce, 5, domain.ModeMapReduce},
	}
	for _, tc := range tests {
		if got := domain.EffectiveMode(tc.selected, tc.chunks); got != tc.want {
			t.Errorf("EffectiveMode(%q, %d) = %q, want %q", tc.selected, tc.chunks, got, tc.want)
		}
	}
}

func TestChunksFor(t *testing.T) {
	d := domain.ParseUnifiedDiff("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n+x\ndiff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@\n+y")
	single := domain.ChunksFor(domain.ModeSinglePass, d)
	if len(single) != 1 || single[0].Label != "all files" || single[0].DiffText != d.Raw {
		t.Errorf("ChunksFor(single-pass) = %+v, want one chunk %q with the whole diff", single, "all files")
	}
	mapped := domain.ChunksFor(domain.ModeMapReduce, d)
	if len(mapped) != 2 || mapped[0].Label != "a" || mapped[1].Label != "b" {
		t.Fatalf("ChunksFor(map-reduce) = %+v, want chunks a and b", mapped)
	}
	if want := "diff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@\n+y"; mapped[1].DiffText != want {
		t.Errorf("ChunksFor(map-reduce)[1].DiffText = %q, want %q", mapped[1].DiffText, want)
	}
}
