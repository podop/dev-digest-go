package domain

import (
	"slices"
	"time"
)

// Review is the part of a stored review the list rollups need.
type Review struct {
	ID        string
	PRID      string
	AgentID   string // "" when the review has no agent
	Score     *int
	CreatedAt time.Time
}

// FindingsCounts is the number of findings per severity.
type FindingsCounts struct {
	Critical   int
	Warning    int
	Suggestion int
}

// Add returns the sum of c and o.
func (c FindingsCounts) Add(o FindingsCounts) FindingsCounts {
	return FindingsCounts{c.Critical + o.Critical, c.Warning + o.Warning, c.Suggestion + o.Suggestion}
}

// Rollup is the review and cost summary of one PR for the list. The zero value is a PR with no
// review and no known cost.
type Rollup struct {
	Score          *int
	CostUSD        *float64
	LatestReviewID *string
	LastReviewedAt *time.Time
	// LatestReviewIDs holds the newest review of each agent, newest first; empty until reviewed.
	LatestReviewIDs []string
	// FindingsCounts is summed over LatestReviewIDs; nil until reviewed.
	FindingsCounts *FindingsCounts
}

// CurrentReviews keeps the newest review of each agent (reviews without an agent count as one
// agent) and returns them newest first. All reviews must belong to one PR.
func CurrentReviews(reviews []Review) []Review {
	sorted := slices.Clone(reviews)
	slices.SortFunc(sorted, newestFirst)
	seen := make(map[string]bool, len(sorted))
	current := make([]Review, 0, len(sorted))
	for _, r := range sorted {
		if seen[r.AgentID] {
			continue
		}
		seen[r.AgentID] = true
		current = append(current, r)
	}
	return current
}

func newestFirst(a, b Review) int {
	if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
		return c
	}
	switch {
	case a.ID < b.ID:
		return 1
	case a.ID > b.ID:
		return -1
	}
	return 0
}

// NewRollup builds the rollup of one PR from its current reviews (see CurrentReviews), the
// findings counts per review id and the summed known run cost (nil when no run has a cost).
func NewRollup(current []Review, counts map[string]FindingsCounts, cost *float64) Rollup {
	r := Rollup{CostUSD: cost, LatestReviewIDs: []string{}}
	if len(current) == 0 {
		return r
	}
	latest := current[0]
	r.Score = latest.Score
	r.LatestReviewID = &latest.ID
	at := latest.CreatedAt
	r.LastReviewedAt = &at
	var total FindingsCounts
	for _, rv := range current {
		r.LatestReviewIDs = append(r.LatestReviewIDs, rv.ID)
		total = total.Add(counts[rv.ID])
	}
	r.FindingsCounts = &total
	return r
}
