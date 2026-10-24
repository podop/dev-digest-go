package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

func aggHot(path, repo string, findings, critical int, prs ...int) domain.HotFile {
	return domain.HotFile{Path: path, RepoID: "id-" + repo, Repo: repo, Findings: findings, Critical: critical, PRs: prs}
}

func TestHotFiles_Empty(t *testing.T) {
	got := domain.HotFiles(nil)
	if got == nil || len(got) != 0 {
		t.Errorf("HotFiles(nil) = %#v, want an empty non-nil slice", got)
	}
}

// The store counts the findings per (repository, path) in SQL (dismissed excluded, accepted
// kept, files without a path ignored, PR numbers distinct: pinned by the store tests); the
// domain keeps the order and the cap.
func TestHotFilesOrdersByFindingsCriticalPathRepo(t *testing.T) {
	in := []domain.HotFile{
		aggHot("b.go", "web", 3, 1, 5),
		aggHot("a.go", "web", 5, 1, 5),
		aggHot("a.go", "api", 5, 2, 3, 12),
		aggHot("c.go", "api", 5, 2, 1),
		aggHot("a.go", "api", 3, 0, 9),
	}
	got := domain.HotFiles(in)
	want := []domain.HotFile{
		aggHot("a.go", "api", 5, 2, 3, 12), // findings 5, critical 2; path a.go before c.go
		aggHot("c.go", "api", 5, 2, 1),
		aggHot("a.go", "web", 5, 1, 5), // fewer criticals
		aggHot("b.go", "web", 3, 1, 5), // 3 findings: critical 1 before 0
		aggHot("a.go", "api", 3, 0, 9),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("HotFiles(files) mismatch (-want +got):\n%s", diff)
	}
}

func TestHotFilesTieBreaksRepoPRsRepoID(t *testing.T) {
	in := []domain.HotFile{
		{Path: "a.go", RepoID: "id-2", Repo: "web", Findings: 1, PRs: []int{1}},
		{Path: "a.go", RepoID: "id-9", Repo: "api", Findings: 1, PRs: []int{2}},
		{Path: "a.go", RepoID: "id-5", Repo: "api", Findings: 1, PRs: []int{1, 3}},
		{Path: "a.go", RepoID: "id-4", Repo: "api", Findings: 1, PRs: []int{1}},
		{Path: "a.go", RepoID: "id-3", Repo: "api", Findings: 1, PRs: []int{1}},
	}
	var ids []string
	for _, f := range domain.HotFiles(in) {
		ids = append(ids, f.RepoID)
	}
	// repo api before web; among api the PR lists [1] [1] [1 3] [2] ascending (a prefix is
	// smaller); equal lists by repository id
	want := []string{"id-3", "id-4", "id-5", "id-9", "id-2"}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("HotFiles() repository order mismatch (-want +got):\n%s", diff)
	}
}

func TestHotFilesTopSixOrderedByFindingsCriticalPath(t *testing.T) {
	in := []domain.HotFile{
		aggHot("e.go", "api", 1, 0, 1),
		aggHot("d.go", "api", 2, 0, 1),
		aggHot("c.go", "api", 3, 0, 1),
		aggHot("a.go", "api", 4, 0, 1), // 4, 0: ties with b.go, path ascending
		aggHot("b.go", "api", 4, 0, 1),
		aggHot("f3.go", "api", 5, 2, 1), // 5, 2 critical: ahead of f2
		aggHot("f2.go", "api", 5, 0, 1),
		aggHot("f1.go", "api", 9, 0, 1),
	}
	got := domain.HotFiles(in)
	var paths []string
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	want := []string{"f1.go", "f3.go", "f2.go", "a.go", "b.go", "c.go"}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("HotFiles(8 files) paths mismatch (-want +got):\n%s", diff)
	}
	if len(got) != domain.HotFilesLimit {
		t.Errorf("HotFiles(8 files) returned %d, want %d", len(got), domain.HotFilesLimit)
	}
}

func TestHotFilesSortsPRsAndLeavesInputAlone(t *testing.T) {
	in := []domain.HotFile{aggHot("a.go", "api", 1, 0, 12, 3)}
	got := domain.HotFiles(in)
	if diff := cmp.Diff([]int{3, 12}, got[0].PRs); diff != "" {
		t.Errorf("HotFiles() PRs mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]int{12, 3}, in[0].PRs); diff != "" {
		t.Errorf("HotFiles() changed its input (-want +got):\n%s", diff)
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
