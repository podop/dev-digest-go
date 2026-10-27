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
	// TargetType is "gha" or "gitlab_ci" ("" = gha); Host is the host key of the repository
	// ("" = github.com); ProjectID is the GitLab project id, 0 for GitHub (spec C FR46).
	TargetType string
	Host       string
	ProjectID  int64
}

// RepoGroup is one repository with its installations and the newest run of any of them.
type RepoGroup struct {
	Repo string
	// TargetType, Host and ProjectID are those of the installations of the group (they share the
	// repository identity); see RepoInstallation.
	TargetType string
	Host       string
	ProjectID  int64
	// Installations are ordered by agent name (byte order), then installed_at, then id.
	Installations []RepoInstallation
	// LastRun is the run with the newest RanAt among the installations' newest runs (a run without
	// RanAt only counts when no run has one); nil when no installation has a run (spec EC12).
	LastRun *Run
}

// GroupByRepo groups installations by repository identity: host + name for GitHub, host + project
// id for GitLab (spec C FR46; a GitHub installation without a host is on github.com). Groups come
// ordered by name in byte order (the order of PostgreSQL's COLLATE "C"), then by host; the order of
// the input does not matter. A nil or empty input gives a nil result.
func GroupByRepo(list []RepoInstallation) []RepoGroup {
	type identity struct {
		host      string
		repo      string
		projectID int64
	}
	byIdentity := make(map[identity][]RepoInstallation)
	for _, in := range list {
		id := identity{host: repoHost(in.Host)}
		if in.TargetType == TargetGitLabCI {
			id.projectID = in.ProjectID
		} else {
			id.repo = in.Repo
		}
		byIdentity[id] = append(byIdentity[id], in)
	}
	var out []RepoGroup
	for _, items := range byIdentity {
		slices.SortFunc(items, compareInstallations)
		first := items[0]
		out = append(out, RepoGroup{
			Repo: first.Repo, TargetType: first.TargetType, Host: repoHost(first.Host), ProjectID: first.ProjectID,
			Installations: items, LastRun: newestRun(items),
		})
	}
	slices.SortFunc(out, func(a, b RepoGroup) int {
		return cmp.Or(strings.Compare(a.Repo, b.Repo), strings.Compare(a.Host, b.Host), cmp.Compare(a.ProjectID, b.ProjectID))
	})
	return out
}

// repoHost is the host key of a repository: "" is github.com.
func repoHost(host string) string {
	if host == "" {
		return "github.com"
	}
	return host
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
