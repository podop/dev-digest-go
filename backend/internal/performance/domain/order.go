package domain

import (
	"sort"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// compareNames orders two strings by UTF-16 code units, as the JavaScript `<` Node used does (a
// database collation would ignore dashes). reviewer/domain is the Phase 5 shared kernel of the
// JavaScript text rules.
func compareNames(a, b string) int { return reviewer.CompareUnits(a, b) }

// compareKeys orders optional keys ascending in code-unit order with nil last.
func compareKeys(a, b *string) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return compareNames(*a, *b)
}

// SortAgents orders agents by name in code-unit order, then by id so that agents with the same
// name come out in a fixed order (Node left it to the database).
func SortAgents(agents []AgentRef) []AgentRef {
	out := append([]AgentRef(nil), agents...)
	sort.SliceStable(out, func(i, j int) bool {
		if c := compareNames(out[i].Name, out[j].Name); c != 0 {
			return c < 0
		}
		return out[i].ID < out[j].ID
	})
	return out
}
