// Package rank holds the pure ranking rules of repo-intel: PageRank over the import graph with the
// percentile of each file (spec section 7.6), the token-budgeted repo map (section 7.7) and the
// rank-driven read rules of the in-process port (section 7.3). It imports only package domain.
package rank

import (
	"math"
	"sort"

	"dev-digest-go/backend/internal/repointel/domain"
)

// PageRank parameters of graphology-metrics 2.4.0 (centrality/pagerank.js defaults).
const (
	// Alpha is the damping factor.
	Alpha = 0.85
	// MaxIterations is the iteration limit before the ranking gives up.
	MaxIterations = 100
	// Tolerance scales the convergence test: the summed change must drop below N * Tolerance.
	Tolerance = 1e-6
)

// FileRank computes the file_rank rows of files over the import graph edges (FR3024, FR3025):
// one row per entry of files, in that order, with hotness 0 and rank equal to pagerank. Edges
// that are self-edges, have an endpoint outside files, or repeat are ignored. When PageRank does
// not converge in MaxIterations every file gets 1/len(files).
func FileRank(files []string, edges []domain.Edge) []domain.FileRankRow {
	return FileRankLimit(files, edges, MaxIterations)
}

// FileRankLimit is FileRank with an explicit iteration limit; a small limit forces the
// non-convergence fallback (EC3027).
func FileRankLimit(files []string, edges []domain.Edge, maxIterations int) []domain.FileRankRow {
	if len(files) == 0 {
		return []domain.FileRankRow{}
	}
	// Nodes are the distinct files in first-appearance order (graphology mergeNode).
	index := make(map[string]int, len(files))
	for _, f := range files {
		if _, ok := index[f]; !ok {
			index[f] = len(index)
		}
	}
	out := adjacency(index, edges)

	scores, converged := pagerank(len(index), out, maxIterations)
	if !converged {
		uniform := 1 / float64(len(files))
		scores = make([]float64, len(index))
		for i := range scores {
			scores[i] = uniform
		}
	}

	rows := make([]domain.FileRankRow, len(files))
	for i, f := range files {
		s := scores[index[f]]
		rows[i] = domain.FileRankRow{Path: f, PageRank: s, Hotness: 0, Rank: s}
	}
	assignPercentiles(rows)
	return rows
}

// adjacency builds the distinct out-neighbours of every node, dropping self-edges, unknown
// endpoints and duplicates.
func adjacency(index map[string]int, edges []domain.Edge) [][]int {
	out := make([][]int, len(index))
	seen := make(map[[2]int]struct{}, len(edges))
	for _, e := range edges {
		from, ok := index[e.From]
		if !ok {
			continue
		}
		to, ok := index[e.To]
		if !ok || from == to {
			continue
		}
		key := [2]int{from, to}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out[from] = append(out[from], to)
	}
	return out
}

// pagerank mirrors graphology-metrics 2.4.0 operation by operation (see spec section 7.6), so
// the floating-point sums come out bit-identical: the same summation order and the same grouping
// of every expression. Explicit float64 conversions stop the compiler from fusing a multiply and
// an add (FMA) on architectures that would.
func pagerank(n int, out [][]int, maxIterations int) (scores []float64, converged bool) {
	alpha := Alpha // variables, not constants: constant arithmetic would not round to float64
	tolerance := Tolerance
	p := 1 / float64(n)
	teleport := float64((1 - alpha) * p)
	limit := float64(n) * tolerance

	x := make([]float64, n)
	weight := make([]float64, n) // normalised edge weight 1/d(i)
	var dangling []int
	for i := range x {
		x[i] = p
		if d := len(out[i]); d == 0 {
			dangling = append(dangling, i)
		} else {
			weight[i] = 1 / float64(d)
		}
	}

	for iteration := 0; iteration < maxIterations; iteration++ {
		last := x
		x = make([]float64, n)

		dangleSum := 0.0
		for _, i := range dangling {
			dangleSum += last[i]
		}
		dangleSum *= alpha

		share := float64(dangleSum * p)
		for i := 0; i < n; i++ {
			for _, to := range out[i] {
				x[to] += float64(float64(alpha*last[i]) * weight[i])
			}
			x[i] += float64(share + teleport)
		}

		diff := 0.0
		for i := 0; i < n; i++ {
			diff += math.Abs(x[i] - last[i])
		}
		if diff < limit {
			return x, true
		}
	}
	return nil, false
}

// assignPercentiles sets Percentile on every row: ascending by rank, a tie group is a maximal run
// of exactly equal ranks, and every member gets round(100 * lastPosition / N) with halves up.
func assignPercentiles(rows []domain.FileRankRow) {
	n := len(rows)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rows[order[a]].Rank < rows[order[b]].Rank })
	for i := 0; i < n; {
		j := i
		for j+1 < n && rows[order[j+1]].Rank == rows[order[i]].Rank {
			j++
		}
		pct := roundHalfUp(float64(100*(j+1)) / float64(n))
		for k := i; k <= j; k++ {
			rows[order[k]].Percentile = pct
		}
		i = j + 1
	}
}

// roundHalfUp is JavaScript's Math.round for non-negative values.
func roundHalfUp(v float64) int {
	f := math.Floor(v)
	if v-f >= 0.5 {
		f++
	}
	return int(f)
}
