package rank

import (
	"testing"

	"dev-digest-go/backend/internal/repointel/domain"
)

// Variables: constant arithmetic would give exactly 0.3 instead of 0.30000000000000004.
var tenth, fifth = 0.1, 0.2

func TestAssignPercentiles(t *testing.T) {
	tests := []struct {
		name  string
		ranks []float64
		want  []int
	}{
		{name: "halves round up", ranks: []float64{1, 2, 3, 4, 5, 6, 7, 8}, want: []int{13, 25, 38, 50, 63, 75, 88, 100}},
		{name: "unsorted input keeps row order", ranks: []float64{3, 1, 2}, want: []int{100, 33, 67}},
		{name: "tie group takes its last position", ranks: []float64{1, 1, 2, 3}, want: []int{50, 50, 75, 100}},
		{name: "all equal", ranks: []float64{0.5, 0.5, 0.5}, want: []int{100, 100, 100}},
		{name: "single", ranks: []float64{0.7}, want: []int{100}},
		{name: "near equal values are separate groups", ranks: []float64{tenth + fifth, 0.3}, want: []int{100, 50}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := make([]domain.FileRankRow, len(tc.ranks))
			for i, r := range tc.ranks {
				rows[i] = domain.FileRankRow{Rank: r}
			}
			assignPercentiles(rows)
			for i, row := range rows {
				if row.Percentile != tc.want[i] {
					t.Errorf("assignPercentiles(%v)[%d] = %d, want %d", tc.ranks, i, row.Percentile, tc.want[i])
				}
			}
		})
	}
}

func TestRoundHalfUp(t *testing.T) {
	tests := []struct {
		in   float64
		want int
	}{{0, 0}, {0.49999999999999994, 0}, {0.5, 1}, {12.5, 13}, {99.5, 100}, {100, 100}, {33.333333333333336, 33}, {66.66666666666667, 67}}
	for _, tc := range tests {
		if got := roundHalfUp(tc.in); got != tc.want {
			t.Errorf("roundHalfUp(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
