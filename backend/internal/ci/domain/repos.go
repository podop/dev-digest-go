package domain

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// RepoInstallation is a gha installation as the per-repository listing sees it: the agent's name
// beside the installation, and the newest run of that installation (nil = none reported).
type RepoInstallation struct {
	ID, AgentID, AgentName, Repo string
	// Slug is nil for rows that predate the slug.
	Slug        *string
	InstalledAt time.Time
	NewestRun   *Run
}

// RepoGroup is one repository with its installations and the newest run of any of them.
type RepoGroup struct {
	Repo string
	// Installations are ordered by agent name (byte order), then installed_at, then id.
	Installations []RepoInstallation
	// LastRun is the run with the newest RanAt among the installations' newest runs (a run without
	// RanAt only counts when no run has one); nil when no installation has a run (spec EC12).
	LastRun *Run
}

// GroupByRepo groups installations by repository. Repositories come in byte order of the name
// (the order of PostgreSQL's COLLATE "C"); the order of the input does not matter. A nil or empty
// input gives a nil result.
func GroupByRepo(list []RepoInstallation) []RepoGroup {
	byRepo := make(map[string][]RepoInstallation)
	for _, in := range list {
		byRepo[in.Repo] = append(byRepo[in.Repo], in)
	}
	var out []RepoGroup
	for repo, items := range byRepo {
		slices.SortFunc(items, compareInstallations)
		out = append(out, RepoGroup{Repo: repo, Installations: items, LastRun: newestRun(items)})
	}
	slices.SortFunc(out, func(a, b RepoGroup) int { return strings.Compare(a.Repo, b.Repo) })
	return out
}

func compareInstallations(a, b RepoInstallation) int {
	return cmp.Or(
		strings.Compare(a.AgentName, b.AgentName),
		a.InstalledAt.Compare(b.InstalledAt),
		strings.Compare(a.ID, b.ID),
	)
}

// newestRun picks the run with the newest RanAt; ties go to the smaller run id, so the answer does
// not depend on the input order.
func newestRun(items []RepoInstallation) *Run {
	var best *Run
	for _, in := range items {
		if in.NewestRun != nil && (best == nil || runNewer(*in.NewestRun, *best)) {
			best = in.NewestRun
		}
	}
	return best
}

// runNewer reports whether a is a newer run than b: a dated run beats an undated one.
func runNewer(a, b Run) bool {
	switch {
	case a.RanAt != nil && b.RanAt != nil:
		if c := a.RanAt.Compare(*b.RanAt); c != 0 {
			return c > 0
		}
	case a.RanAt != nil:
		return true
	case b.RanAt != nil:
		return false
	}
	return a.ID < b.ID
}
