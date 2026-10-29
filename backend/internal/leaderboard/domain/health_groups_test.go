package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

func aggGroupRow(author, repoID, repo string, health *int, findings domain.FindingCounts) domain.Row {
	return domain.Row{Author: author, RepoID: repoID, Repo: repo, Health: health, Findings: findings}
}

func TestHealthGroups_Empty(t *testing.T) {
	for name, f := range map[string]func([]domain.Row) []domain.HealthGroup{
		"HealthByAuthor": domain.HealthByAuthor,
		"HealthByRepo":   domain.HealthByRepo,
	} {
		if got := f(nil); got == nil || len(got) != 0 {
			t.Errorf("%s(nil) = %#v, want an empty non-nil slice", name, got)
		}
	}
}

func TestHealthByAuthorAveragesAndOrdersWorstFirst(t *testing.T) {
	rows := []domain.Row{
		aggGroupRow("ann", "r1", "api", aggPtr(90), domain.FindingCounts{Critical: 1}),
		aggGroupRow("ann", "r2", "web", aggPtr(81), domain.FindingCounts{Warning: 1, Suggestion: 1}),
		aggGroupRow("ann", "r1", "api", nil, domain.FindingCounts{Suggestion: 1}),
		aggGroupRow("bob", "r1", "api", aggPtr(60), domain.FindingCounts{Critical: 2}),
		aggGroupRow("cyd", "r2", "web", nil, domain.FindingCounts{}),
		aggGroupRow("dee", "r2", "web", aggPtr(86), domain.FindingCounts{}),
		aggGroupRow("dee", "r2", "web", aggPtr(85), domain.FindingCounts{}), // 85.5 rounds half up to 86
		aggGroupRow("eve", "r1", "api", aggPtr(86), domain.FindingCounts{Warning: 1}),
	}
	got := domain.HealthByAuthor(rows)
	want := []domain.HealthGroup{
		{Key: "bob", Label: "bob", PRs: 1, AvgHealth: aggPtr(60), AvgFindings: aggPtr(2.0)},
		{Key: "ann", Label: "ann", PRs: 3, AvgHealth: aggPtr(86), AvgFindings: aggPtr(1.3)},
		// same health as ann: more PRs first, then the name
		{Key: "dee", Label: "dee", PRs: 2, AvgHealth: aggPtr(86), AvgFindings: aggPtr(0.0)},
		{Key: "eve", Label: "eve", PRs: 1, AvgHealth: aggPtr(86), AvgFindings: aggPtr(1.0)},
		// no scored review: last
		{Key: "cyd", Label: "cyd", PRs: 1, AvgFindings: aggPtr(0.0)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HealthByAuthor(rows) mismatch (-want +got):\n%s", diff)
	}
}

func TestHealthByRepoGroupsByIDWithTheNameAsLabel(t *testing.T) {
	rows := []domain.Row{
		aggGroupRow("ann", "r1", "api", aggPtr(90), domain.FindingCounts{Critical: 1}),
		aggGroupRow("bob", "r1", "api", aggPtr(70), domain.FindingCounts{Warning: 3}),
		aggGroupRow("ann", "r2", "web", aggPtr(95), domain.FindingCounts{}),
		// another repository with the same name (other owner or host) stays its own group
		aggGroupRow("cyd", "r3", "api", aggPtr(40), domain.FindingCounts{Critical: 4}),
	}
	got := domain.HealthByRepo(rows)
	want := []domain.HealthGroup{
		{Key: "r3", Label: "api", PRs: 1, AvgHealth: aggPtr(40), AvgFindings: aggPtr(4.0)},
		{Key: "r1", Label: "api", PRs: 2, AvgHealth: aggPtr(80), AvgFindings: aggPtr(2.0)},
		{Key: "r2", Label: "web", PRs: 1, AvgHealth: aggPtr(95), AvgFindings: aggPtr(0.0)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HealthByRepo(rows) mismatch (-want +got):\n%s", diff)
	}
}
