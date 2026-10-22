package rank_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/rank"
)

func TestIsJunkPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"src/app.test.ts", true},
		{"src/app.spec.tsx", true},
		{"types/global.d.ts", true},
		{"src/__tests__/a.ts", true},
		{"src/__mocks__/a.ts", true},
		{"pkg/test/a.ts", true},
		{"pkg/tests/a.ts", true},
		{"db/migrations/001.ts", true},
		{"src/__fixtures__/a.ts", true},
		{"next.config.js", true},
		{"vitest.setup.ts", true},
		{"src/jest.setup.js", true},
		{".eslintrc.js", true},
		{"prettier.rc.js", true},
		{"SRC/App.TEST.ts", true},
		{"src/app.ts", false},
		{"test/a.ts", false}, // "/test/" needs a parent directory
		{"src/contest.ts", false},
		{"src/latest/a.ts", false},
		{"src/esl\u0130nt.ts", false}, // JavaScript lower-cases U+0130 to "i" + a combining dot
	}
	for _, tc := range tests {
		if got := rank.IsJunkPath(tc.path); got != tc.want {
			t.Errorf("IsJunkPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func ranked(paths ...string) []domain.RankedPath {
	out := make([]domain.RankedPath, len(paths))
	for i, p := range paths {
		out[i] = domain.RankedPath{Path: p, Rank: float64(len(paths) - i)}
	}
	return out
}

func TestTopFiles(t *testing.T) {
	rows := ranked("a.test.ts", "src/b.ts", "src/gen/c.ts", "src/d.ts", "src/e.ts")
	tests := []struct {
		name    string
		n       int
		exclude []string
		want    []string
	}{
		{name: "junk dropped, first n kept", n: 2, want: []string{"src/b.ts", "src/gen/c.ts"}},
		{name: "exclude is a case-sensitive substring", n: 5, exclude: []string{"gen/", "E.TS"}, want: []string{"src/b.ts", "src/d.ts", "src/e.ts"}},
		{name: "fewer than n", n: 10, want: []string{"src/b.ts", "src/gen/c.ts", "src/d.ts", "src/e.ts"}},
		{name: "zero", n: 0, want: []string{}},
		{name: "negative", n: -3, want: []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rank.TopFiles(rows, tc.n, tc.exclude)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("TopFiles(n=%d, exclude=%v) mismatch (-want +got):\n%s", tc.n, tc.exclude, diff)
			}
		})
	}
}

func TestTopFilesScan(t *testing.T) {
	for n, want := range map[int]int{-1: 100, 0: 100, 1: 100, 10: 100, 11: 110, 12: 120} {
		if got := rank.TopFilesScan(n); got != want {
			t.Errorf("TopFilesScan(%d) = %d, want %d", n, got, want)
		}
	}
}

func rp(path string, r float64) domain.RankedPath { return domain.RankedPath{Path: path, Rank: r} }

func TestCriticalPaths(t *testing.T) {
	tests := []struct {
		name   string
		edges  []domain.Edge
		ranked []domain.RankedPath
		want   [][]string
	}{
		{
			name:   "no edges",
			ranked: []domain.RankedPath{rp("a", 3), rp("b", 2)},
			want:   [][]string{},
		},
		{
			name:   "follows the highest ranked target for two hops",
			edges:  []domain.Edge{{From: "a", To: "b"}, {From: "a", To: "c"}, {From: "c", To: "d"}, {From: "c", To: "e"}},
			ranked: []domain.RankedPath{rp("a", 9), rp("c", 8), rp("b", 7), rp("e", 6), rp("d", 5)},
			want:   [][]string{{"a", "c", "e"}, {"c", "e"}},
		},
		{
			name:   "missing rank counts as 0 and ties go to the smaller path",
			edges:  []domain.Edge{{From: "a", To: "z"}, {From: "a", To: "m"}, {From: "a", To: "ghost"}},
			ranked: []domain.RankedPath{rp("a", 5), rp("m", 0), rp("z", 0)},
			want:   [][]string{{"a", "ghost"}},
		},
		{
			name:   "visited files are not revisited",
			edges:  []domain.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
			ranked: []domain.RankedPath{rp("a", 2), rp("b", 1)},
			want:   [][]string{{"a", "b"}, {"b", "a"}},
		},
		{
			name:   "chains shorter than 2 are dropped",
			edges:  []domain.Edge{{From: "x", To: "y"}},
			ranked: []domain.RankedPath{rp("a", 2), rp("b", 1)},
			want:   [][]string{},
		},
		{
			name:   "only the five top ranked files are roots",
			edges:  []domain.Edge{{From: "r6", To: "t"}},
			ranked: []domain.RankedPath{rp("r1", 6), rp("r2", 5), rp("r3", 4), rp("r4", 3), rp("r5", 2), rp("r6", 1), rp("t", 0.5)},
			want:   [][]string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rank.CriticalPaths(tc.edges, tc.ranked)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("CriticalPaths mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCriticalPaths_DropsDuplicateChains(t *testing.T) {
	// Two roots cannot produce the same chain (a chain starts at its root), so a duplicate needs
	// the same root listed twice, as a ranked list with a repeated path would.
	edges := []domain.Edge{{From: "a", To: "b"}}
	got := rank.CriticalPaths(edges, []domain.RankedPath{rp("a", 2), rp("a", 2), rp("b", 1)})
	if diff := cmp.Diff([][]string{{"a", "b"}}, got); diff != "" {
		t.Errorf("CriticalPaths(repeated root) mismatch (-want +got):\n%s", diff)
	}
}
