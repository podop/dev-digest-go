package rank_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/rank"
)

func names(n int) []string {
	files := make([]string, n)
	for i := range files {
		files[i] = fmt.Sprintf("f%d.ts", i)
	}
	return files
}

func toEdges(files []string, pairs [][2]int) []domain.Edge {
	edges := make([]domain.Edge, len(pairs))
	for i, p := range pairs {
		edges[i] = domain.Edge{From: files[p[0]], To: files[p[1]]}
	}
	return edges
}

// TestFileRank_MatchesGraphology compares against values produced by graphology-metrics 2.4.0
// (the Node implementation at d1c9093) on the same graphs; the golden parity corpus of the
// parity package covers more graphs.
func TestFileRank_MatchesGraphology(t *testing.T) {
	tests := []struct {
		name  string
		files int
		edges [][2]int
		want  []float64
		pct   []int
	}{
		{
			name:  "star",
			files: 5,
			edges: [][2]int{{1, 0}, {2, 0}, {3, 0}, {4, 0}},
			want:  []float64{0.52381048560723087, 0.11904737859819239, 0.11904737859819239, 0.11904737859819239, 0.11904737859819239},
			pct:   []int{100, 80, 80, 80, 80},
		},
		{
			name:  "chain",
			files: 4,
			edges: [][2]int{{0, 1}, {1, 2}, {2, 3}},
			want:  []float64{0.11615558155321362, 0.21488785520742981, 0.29881090166805391, 0.37014566157130258},
			pct:   []int{25, 50, 75, 100},
		},
		{
			name:  "cycle",
			files: 3,
			edges: [][2]int{{0, 1}, {1, 2}, {2, 0}},
			want:  []float64{0.33333333333333331, 0.33333333333333331, 0.33333333333333331},
			pct:   []int{100, 100, 100},
		},
		{
			name:  "dangling and isolated",
			files: 6,
			edges: [][2]int{{0, 1}, {0, 2}, {3, 2}, {4, 5}},
			want:  []float64{0.11695893181501021, 0.16666666666666663, 0.26608213636997946, 0.11695893181501021, 0.11695893181501021, 0.21637440151832305},
			pct:   []int{50, 67, 100, 50, 50, 83},
		},
		{
			name:  "single file",
			files: 1,
			edges: [][2]int{},
			want:  []float64{1.0000000000000000},
			pct:   []int{100},
		},
		{
			name:  "no edges",
			files: 4,
			edges: [][2]int{},
			want:  []float64{0.25000000000000000, 0.25000000000000000, 0.25000000000000000, 0.25000000000000000},
			pct:   []int{100, 100, 100, 100},
		},
		{
			name:  "random with duplicates, self edges and cycles",
			files: 40,
			edges: [][2]int{{30, 8}, {16, 16}, {16, 8}, {16, 24}, {3, 23}, {19, 23}, {3, 15}, {35, 18}, {26, 5}, {8, 0}, {16, 16}, {27, 23}, {3, 31}, {19, 23}, {3, 23}, {11, 15}, {11, 10}, {8, 30}, {20, 4}, {36, 4}, {20, 39}, {19, 31}, {35, 39}, {3, 15}, {3, 2}, {2, 13}, {17, 13}, {1, 5}, {1, 5}, {17, 13}, {33, 37}, {9, 0}, {8, 8}, {8, 16}, {16, 3}, {15, 19}, {7, 19}, {15, 35}, {15, 11}, {39, 3}, {15, 3}, {39, 19}, {34, 2}, {34, 34}, {2, 34}, {26, 18}, {34, 26}, {34, 18}, {10, 34}, {37, 17}, {5, 1}, {13, 25}, {0, 8}, {3, 13}, {0, 32}, {8, 0}, {19, 39}, {35, 15}, {19, 7}, {3, 39}, {28, 12}, {20, 4}, {36, 20}, {12, 36}, {36, 28}, {28, 28}, {20, 36}, {20, 7}, {3, 18}, {26, 18}, {10, 26}, {34, 2}, {26, 34}, {37, 17}, {5, 9}, {37, 17}, {13, 9}, {5, 1}, {8, 8}, {32, 24}, {8, 8}, {19, 13}, {35, 23}, {19, 23}, {19, 7}, {14, 6}, {38, 30}, {9, 5}, {32, 24}, {19, 15}, {36, 36}, {20, 12}, {28, 20}, {4, 12}, {28, 28}, {12, 28}, {28, 23}, {27, 18}, {24, 8}, {8, 24}, {32, 0}, {0, 32}, {8, 24}, {0, 24}, {0, 16}, {24, 32}, {24, 8}, {11, 31}, {19, 32}, {0, 27}, {23, 35}, {10, 26}, {10, 26}, {34, 2}, {34, 18}, {34, 34}, {10, 10}, {10, 10}, {18, 21}, {9, 29}},
			want:  []float64{0.048797707974855165, 0.023686151340424361, 0.015816938179754814, 0.030852277873070117, 0.015513715561587520, 0.041509859221549095, 0.011178841571822952, 0.013469691534178128, 0.067208002640516018, 0.038083879998358744, 0.0093326886624618740, 0.011611977125428594, 0.029528784963530834, 0.033876508032605918, 0.0060426001350863687, 0.026207921162762902, 0.028618437347303394, 0.015544694306436912, 0.035430855814764017, 0.034193988475384032, 0.019262884695652151, 0.036159332714180412, 0.0060426001350863687, 0.036035181105679881, 0.054410617688152266, 0.020440328793020750, 0.016037094534228899, 0.014337943121132433, 0.024789284566146690, 0.016832999746544289, 0.025459335797993917, 0.017231215808550453, 0.041612891713489607, 0.0060426001350863687, 0.021275233095220617, 0.042242889473623327, 0.021867903454570525, 0.011178841571822952, 0.0060426001350863687, 0.026192699792849893},
			pct:   []int{95, 53, 33, 70, 28, 88, 18, 23, 100, 85, 13, 20, 68, 73, 10, 63, 65, 30, 78, 75, 43, 83, 10, 80, 98, 45, 35, 25, 55, 38, 58, 40, 90, 10, 48, 93, 50, 18, 10, 60},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := names(tc.files)
			got := rank.FileRank(files, toEdges(files, tc.edges))
			if len(got) != len(files) {
				t.Fatalf("FileRank(%s) returned %d rows, want %d", tc.name, len(got), len(files))
			}
			for i, row := range got {
				if row.Path != files[i] {
					t.Errorf("row %d path = %q, want %q", i, row.Path, files[i])
				}
				if row.PageRank != tc.want[i] {
					t.Errorf("FileRank(%s)[%d].PageRank = %.17g, want %.17g (diff %g)", tc.name, i, row.PageRank, tc.want[i], math.Abs(row.PageRank-tc.want[i]))
				}
				if row.Rank != row.PageRank || row.Hotness != 0 {
					t.Errorf("FileRank(%s)[%d] rank/hotness = %v/%v, want rank = pagerank and hotness 0", tc.name, i, row.Rank, row.Hotness)
				}
				if row.Percentile != tc.pct[i] {
					t.Errorf("FileRank(%s)[%d].Percentile = %d, want %d", tc.name, i, row.Percentile, tc.pct[i])
				}
			}
		})
	}
}

func TestFileRank_Empty(t *testing.T) {
	got := rank.FileRank(nil, []domain.Edge{{From: "a", To: "b"}})
	if got == nil || len(got) != 0 {
		t.Errorf("FileRank(nil) = %#v, want an empty non-nil slice", got)
	}
}

func TestFileRank_IgnoresUnknownEndpointsSelfAndDuplicateEdges(t *testing.T) {
	files := []string{"a.ts", "b.ts"}
	clean := rank.FileRank(files, []domain.Edge{{From: "a.ts", To: "b.ts"}})
	noisy := rank.FileRank(files, []domain.Edge{
		{From: "a.ts", To: "b.ts"},
		{From: "a.ts", To: "b.ts"},
		{From: "a.ts", To: "a.ts"},
		{From: "a.ts", To: "gone.ts"},
		{From: "gone.ts", To: "b.ts"},
	})
	for i := range clean {
		if clean[i] != noisy[i] {
			t.Errorf("FileRank with noisy edges row %d = %+v, want %+v", i, noisy[i], clean[i])
		}
	}
}

func TestFileRankLimit_NonConvergenceGivesUniformScores(t *testing.T) {
	files := names(5)
	edges := toEdges(files, [][2]int{{1, 0}, {2, 0}, {3, 0}, {4, 0}})
	got := rank.FileRankLimit(files, edges, 1)
	for i, row := range got {
		if row.PageRank != 1.0/5 || row.Rank != 1.0/5 {
			t.Errorf("FileRankLimit(limit 1)[%d] pagerank/rank = %v/%v, want 0.2", i, row.PageRank, row.Rank)
		}
		if row.Percentile != 100 {
			t.Errorf("FileRankLimit(limit 1)[%d].Percentile = %d, want 100", i, row.Percentile)
		}
	}
	if conv := rank.FileRankLimit(files, edges, rank.MaxIterations); conv[0].PageRank == 1.0/5 {
		t.Errorf("FileRank with the default limit did not converge: %v", conv[0].PageRank)
	}
}

func TestFileRank_NoEdgesIsOneTieGroup(t *testing.T) {
	// Ten isolated files tie: one group whose last position is 10, so 100 for all of them.
	for i, row := range rank.FileRank(names(10), nil) {
		if row.Percentile != 100 {
			t.Errorf("FileRank(no edges)[%d].Percentile = %d, want 100", i, row.Percentile)
		}
	}
}

// cancelAfter is a context whose Err is nil for the first n calls and context.Canceled after.
type cancelAfter struct {
	context.Context
	n, calls int
}

func (c *cancelAfter) Err() error {
	if c.calls++; c.calls > c.n {
		return context.Canceled
	}
	return nil
}

// budgetGraph is a graph of 5,000 files and domain.MaxRows edges, the most Build emits.
func budgetGraph() ([]string, []domain.Edge) {
	files := names(5000)
	edges := make([]domain.Edge, 0, domain.MaxRows)
	for i := 0; len(edges) < domain.MaxRows; i++ {
		for d := 1; d <= 100 && len(edges) < domain.MaxRows; d++ {
			edges = append(edges, domain.Edge{From: files[i], To: files[(i+d)%len(files)]})
		}
	}
	return files, edges
}

func TestFileRankContext_MatchesFileRank(t *testing.T) {
	files, edges := budgetGraph()
	start := time.Now()
	got, err := rank.FileRankContext(t.Context(), files, edges)
	t.Logf("FileRankContext(%d files, %d edges) took %v", len(files), len(edges), time.Since(start))
	if err != nil {
		t.Fatalf("FileRankContext() error = %v", err)
	}
	if diff := cmp.Diff(rank.FileRank(files, edges), got); diff != "" {
		t.Errorf("FileRankContext() mismatch with FileRank (-want +got):\n%s", diff)
	}
}

// The rank honours a cancel or JobTimeout while it runs, not only before it starts.
func TestFileRankContext_StopsWhenCtxEnds(t *testing.T) {
	files, edges := budgetGraph()
	for _, n := range []int{0, 1, 50} {
		t.Run(fmt.Sprintf("cancelled after %d checks", n), func(t *testing.T) {
			ctx := &cancelAfter{Context: t.Context(), n: n}
			rows, err := rank.FileRankContext(ctx, files, edges)
			if !errors.Is(err, context.Canceled) || rows != nil {
				t.Fatalf("FileRankContext() = %d rows, %v, want no rows and context.Canceled", len(rows), err)
			}
			if ctx.calls != n+1 {
				t.Errorf("FileRankContext() looked at ctx %d times, want it to stop at the first look after the cancel (%d)", ctx.calls, n+1)
			}
		})
	}
}
