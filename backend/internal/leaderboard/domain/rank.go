package domain

import (
	"cmp"
	"slices"
)

// Rank sorts rows in place into table order (spec FR21): health descending with unknown
// health last, then PR number ascending, then repo name. The repository id and PR id only break
// ties between equal repo names and numbers, so the order is deterministic.
func Rank(rows []Row) {
	slices.SortStableFunc(rows, compareRankRows)
}

func compareRankRows(a, b Row) int {
	return cmp.Or(
		compareRankHealth(a.Health, b.Health),
		cmp.Compare(a.Number, b.Number),
		cmp.Compare(a.Repo, b.Repo),
		cmp.Compare(a.RepoID, b.RepoID),
		cmp.Compare(a.PRID, b.PRID),
	)
}

// compareRankHealth orders higher health first and nil after every value.
func compareRankHealth(a, b *int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return cmp.Compare(*b, *a)
}
