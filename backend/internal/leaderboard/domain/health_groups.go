package domain

import (
	"cmp"
	"math"
	"slices"
)

// HealthGroup is the health-by-author or health-by-repository entry of one group of rows. Key is
// the author login or the repository id, Label the author login or the repository name. AvgHealth
// is nil when no row of the group has a health; AvgFindings (one decimal) is never nil, a group
// has at least one row.
type HealthGroup struct {
	Key         string
	Label       string
	PRs         int
	AvgHealth   *int
	AvgFindings *float64
}

// HealthByAuthor groups the rows by author (the rows' health-by-size averages per author).
// Order: see sortHealthGroups.
func HealthByAuthor(rows []Row) []HealthGroup {
	return healthGroups(rows, func(r Row) (string, string) { return r.Author, r.Author })
}

// HealthByRepo groups the rows by repository id, labelled with the repository name. Order: see
// sortHealthGroups.
func HealthByRepo(rows []Row) []HealthGroup {
	return healthGroups(rows, func(r Row) (string, string) { return r.RepoID, r.Repo })
}

// healthGroups averages health and findings per group like HealthBySize (every row counts, size
// unknown or not) and orders the groups worst health first. The result is never nil.
func healthGroups(rows []Row, group func(Row) (key, label string)) []HealthGroup {
	type acc struct {
		label                             string
		prs, healthN, healthSum, findings int
	}
	accs := map[string]*acc{}
	for _, r := range rows {
		key, label := group(r)
		a, ok := accs[key]
		if !ok {
			a = &acc{label: label}
			accs[key] = a
		}
		a.prs++
		a.findings += r.Findings.Critical + r.Findings.Warning + r.Findings.Suggestion
		if r.Health != nil {
			a.healthN++
			a.healthSum += *r.Health
		}
	}
	out := make([]HealthGroup, 0, len(accs))
	for key, a := range accs {
		g := HealthGroup{Key: key, Label: a.label, PRs: a.prs}
		if a.healthN > 0 {
			h := int(math.Round(float64(a.healthSum) / float64(a.healthN)))
			g.AvgHealth = &h
		}
		f := aggRound(float64(a.findings)/float64(a.prs), 1)
		g.AvgFindings = &f
		out = append(out, g)
	}
	sortHealthGroups(out)
	return out
}

// sortHealthGroups orders worst health first: average health ascending, groups without a health
// last; then more PRs first, then label and key ascending.
func sortHealthGroups(gs []HealthGroup) {
	slices.SortFunc(gs, func(a, b HealthGroup) int {
		return cmp.Or(
			compareHealth(a.AvgHealth, b.AvgHealth),
			cmp.Compare(b.PRs, a.PRs),
			cmp.Compare(a.Label, b.Label),
			cmp.Compare(a.Key, b.Key),
		)
	})
}

// compareHealth orders lower health first and nil after every value.
func compareHealth(a, b *int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return cmp.Compare(*a, *b)
}
