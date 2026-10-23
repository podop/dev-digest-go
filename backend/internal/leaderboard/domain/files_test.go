package domain_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

func aggItems(file string, n int, severity string) []domain.FindingItem {
	var out []domain.FindingItem
	for range n {
		out = append(out, domain.FindingItem{File: file, Severity: severity})
	}
	return out
}

func aggFileRow(repo string, number int, items ...[]domain.FindingItem) domain.Row {
	r := domain.Row{PRID: fmt.Sprintf("%s-%d", repo, number), RepoID: "id-" + repo, Repo: repo, Number: number}
	var all []domain.FindingItem
	for _, it := range items {
		all = append(all, it...)
	}
	r.Reviews = []domain.RowReview{{ReviewID: "rv", Items: all}}
	return r
}

func TestHotFiles_Empty(t *testing.T) {
	got := domain.HotFiles(nil)
	if got == nil || len(got) != 0 {
		t.Errorf("HotFiles(nil) = %#v, want an empty non-nil slice", got)
	}
}

func TestHotFilesGroupsByRepoAndPath(t *testing.T) {
	rows := []domain.Row{
		aggFileRow("api", 12, aggItems("a.go", 2, domain.SeverityCritical), aggItems("a.go", 1, domain.SeverityWarning)),
		aggFileRow("api", 3, aggItems("a.go", 1, domain.SeveritySuggestion)),
		aggFileRow("api", 12, aggItems("a.go", 1, domain.SeverityWarning)), // same PR again: number listed once
		aggFileRow("web", 5, aggItems("a.go", 1, domain.SeverityCritical)), // same path, other repo
	}
	got := domain.HotFiles(rows)
	want := []domain.HotFile{
		{Path: "a.go", RepoID: "id-api", Repo: "api", Findings: 5, Critical: 2, PRs: []int{3, 12}},
		{Path: "a.go", RepoID: "id-web", Repo: "web", Findings: 1, Critical: 1, PRs: []int{5}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HotFiles(rows) mismatch (-want +got):\n%s", diff)
	}
}

func TestHotFilesExcludeDismissedKeepAccepted(t *testing.T) {
	when := aggAt(1)
	items := []domain.FindingItem{
		{File: "a.go", Severity: domain.SeverityCritical},
		{File: "a.go", Severity: domain.SeverityCritical, AcceptedAt: &when},
		{File: "a.go", Severity: domain.SeverityCritical, DismissedAt: &when},
		{File: "a.go", Severity: domain.SeverityCritical, AcceptedAt: &when, DismissedAt: &when}, // dismissed wins (EC7)
		{File: "only-dismissed.go", Severity: domain.SeverityWarning, DismissedAt: &when},
		{File: "", Severity: domain.SeverityWarning}, // no path
	}
	got := domain.HotFiles([]domain.Row{aggFileRow("api", 1, items)})
	want := []domain.HotFile{{Path: "a.go", RepoID: "id-api", Repo: "api", Findings: 2, Critical: 2, PRs: []int{1}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HotFiles(rows) mismatch (-want +got):\n%s", diff)
	}
}

func TestHotFilesTopSixOrderedByFindingsCriticalPath(t *testing.T) {
	row := aggFileRow("api", 1,
		aggItems("f1.go", 9, domain.SeverityWarning),
		aggItems("f2.go", 5, domain.SeverityWarning),
		aggItems("f3.go", 3, domain.SeverityWarning), aggItems("f3.go", 2, domain.SeverityCritical), // 5, 2 critical: ahead of f2
		aggItems("b.go", 4, domain.SeverityWarning), // 4, 0
		aggItems("a.go", 4, domain.SeverityWarning), // 4, 0: ties with b.go, path ascending
		aggItems("c.go", 3, domain.SeverityWarning),
		aggItems("d.go", 2, domain.SeverityWarning),
		aggItems("e.go", 1, domain.SeverityWarning),
	)
	got := domain.HotFiles([]domain.Row{row})
	var paths []string
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	want := []string{"f1.go", "f3.go", "f2.go", "a.go", "b.go", "c.go"}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("HotFiles(9 files) paths mismatch (-want +got):\n%s", diff)
	}
	if len(got) != domain.HotFilesLimit {
		t.Errorf("HotFiles(9 files) returned %d, want %d", len(got), domain.HotFilesLimit)
	}
}

func TestHotFilesCountAllSnapshotReviews(t *testing.T) {
	r := aggFileRow("api", 1)
	r.Reviews = []domain.RowReview{
		{ReviewID: "r1", Items: aggItems("a.go", 1, domain.SeverityCritical)},
		{ReviewID: "r2", Items: aggItems("a.go", 2, domain.SeverityWarning)},
	}
	got := domain.HotFiles([]domain.Row{r})
	want := []domain.HotFile{{Path: "a.go", RepoID: "id-api", Repo: "api", Findings: 3, Critical: 1, PRs: []int{1}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HotFiles(two reviews) mismatch (-want +got):\n%s", diff)
	}
}

func aggSizeRow(size domain.Size, health *int, findings domain.FindingCounts) domain.Row {
	return domain.Row{Size: size, Health: health, Findings: findings}
}

func TestHealthBySize_Empty(t *testing.T) {
	got := domain.HealthBySize(nil)
	var want []domain.SizeBucket
	for _, s := range domain.Sizes {
		want = append(want, domain.SizeBucket{Size: s})
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HealthBySize(nil) mismatch (-want +got):\n%s", diff)
	}
}

func TestHealthBySizeFiveBucketsWithNulls(t *testing.T) {
	rows := []domain.Row{
		aggSizeRow(domain.SizeS, aggPtr(90), domain.FindingCounts{Critical: 1}),
		aggSizeRow(domain.SizeS, aggPtr(81), domain.FindingCounts{Warning: 1, Suggestion: 1}),
		aggSizeRow(domain.SizeS, aggPtr(70), domain.FindingCounts{Warning: 4}),    // mean 80.33 -> 80, findings 7/3 -> 2.3
		aggSizeRow(domain.SizeL, nil, domain.FindingCounts{Warning: 2}),           // rows but no health at all
		aggSizeRow(domain.SizeNone, aggPtr(5), domain.FindingCounts{Critical: 9}), // unknown size is left out (EC9)
		aggSizeRow(domain.SizeXL, aggPtr(40), domain.FindingCounts{}),
		aggSizeRow(domain.SizeXL, nil, domain.FindingCounts{Suggestion: 1}), // nil health skipped in the mean
		aggSizeRow(domain.SizeXS, aggPtr(100), domain.FindingCounts{}),
	}
	got := domain.HealthBySize(rows)
	want := []domain.SizeBucket{
		{Size: domain.SizeXS, PRs: 1, AvgHealth: aggPtr(100), AvgFindings: aggPtr(0.0)},
		{Size: domain.SizeS, PRs: 3, AvgHealth: aggPtr(80), AvgFindings: aggPtr(2.3)},
		{Size: domain.SizeM},
		{Size: domain.SizeL, PRs: 1, AvgFindings: aggPtr(2.0)},
		{Size: domain.SizeXL, PRs: 2, AvgHealth: aggPtr(40), AvgFindings: aggPtr(0.5)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HealthBySize(rows) mismatch (-want +got):\n%s", diff)
	}
}

func TestHealthBySizeRoundsHealthHalfUp(t *testing.T) {
	rows := []domain.Row{
		aggSizeRow(domain.SizeM, aggPtr(70), domain.FindingCounts{}),
		aggSizeRow(domain.SizeM, aggPtr(71), domain.FindingCounts{}),
	}
	got := domain.HealthBySize(rows)[2]
	if got.AvgHealth == nil || *got.AvgHealth != 71 {
		t.Errorf("HealthBySize(70, 71)[M].AvgHealth = %v, want 71", got.AvgHealth)
	}
}
