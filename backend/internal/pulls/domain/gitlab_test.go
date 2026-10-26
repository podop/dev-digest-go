package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/pulls/domain"
)

func TestMRHeadIsFork(t *testing.T) {
	tests := []struct {
		name           string
		source, target int64
		want           bool
	}{
		{"same project", 7, 7, false},
		{"other project", 8, 7, true},
		{"source missing", 0, 7, true},
		{"target missing", 7, 0, true},
		{"both missing", 0, 0, true},
		{"negative source", -1, 7, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.MRHeadIsFork(tc.source, tc.target); got != tc.want {
				t.Errorf("MRHeadIsFork(%d, %d) = %v, want %v", tc.source, tc.target, got, tc.want)
			}
		})
	}
}

func TestMRStatus(t *testing.T) {
	tests := []struct{ state, want string }{
		{"opened", domain.StatusOpen},
		{"merged", domain.StatusMerged},
		{"closed", domain.StatusClosed},
		{"locked", domain.StatusClosed},
		{"", domain.StatusClosed},
		{"draft", domain.StatusClosed},
	}
	for _, tc := range tests {
		t.Run(tc.state, func(t *testing.T) {
			if got := domain.MRStatus(tc.state); got != tc.want {
				t.Errorf("MRStatus(%q) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

func TestMapMergeRequest(t *testing.T) {
	at := func(h int) *time.Time {
		v := time.Date(2026, 10, 1, h, 0, 0, 0, time.UTC)
		return &v
	}
	base := domain.MergeRequest{
		IID: 42, Title: "Add x", Author: "ann", SourceBranch: "feat", TargetBranch: "main", SHA: "abc",
		State: "opened", CreatedAt: at(1), UpdatedAt: at(2), SourceProjectID: 7, TargetProjectID: 7,
	}
	with := func(f func(*domain.MergeRequest)) domain.MergeRequest { mr := base; f(&mr); return mr }

	tests := []struct {
		name   string
		in     domain.MergeRequest
		want   domain.Listed
		wantOK bool
	}{
		{"open", base, domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "open", OpenedAt: at(1), UpdatedAt: at(2),
		}, true},
		{"draft", with(func(m *domain.MergeRequest) { m.Draft = true }), domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "open", OpenedAt: at(1), UpdatedAt: at(2), Draft: true,
		}, true},
		{"merged", with(func(m *domain.MergeRequest) { m.State = "merged"; m.MergedAt = at(3); m.ClosedAt = at(3) }), domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "merged", OpenedAt: at(1), UpdatedAt: at(2), MergedAt: at(3), ClosedAt: at(3),
		}, true},
		{"merged without closed_at keeps it unset", with(func(m *domain.MergeRequest) { m.State = "merged"; m.MergedAt = at(3) }), domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "merged", OpenedAt: at(1), UpdatedAt: at(2), MergedAt: at(3),
		}, true},
		{"closed", with(func(m *domain.MergeRequest) { m.State = "closed"; m.ClosedAt = at(3) }), domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "closed", OpenedAt: at(1), UpdatedAt: at(2), ClosedAt: at(3),
		}, true},
		{"locked reads closed", with(func(m *domain.MergeRequest) { m.State = "locked" }), domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "closed", OpenedAt: at(1), UpdatedAt: at(2),
		}, true},
		{"stale closed_at of a reopened request is dropped", with(func(m *domain.MergeRequest) { m.ClosedAt = at(3); m.MergedAt = at(3) }), domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "open", OpenedAt: at(1), UpdatedAt: at(2),
		}, true},
		{"no author", with(func(m *domain.MergeRequest) { m.Author = "" }), domain.Listed{
			Number: 42, Title: "Add x", Author: "unknown", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "open", OpenedAt: at(1), UpdatedAt: at(2),
		}, true},
		{"fork", with(func(m *domain.MergeRequest) { m.SourceProjectID = 9 }), domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "open", OpenedAt: at(1), UpdatedAt: at(2), HeadIsFork: true,
		}, true},
		{"missing source project counts as a fork", with(func(m *domain.MergeRequest) { m.SourceProjectID = 0 }), domain.Listed{
			Number: 42, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "abc",
			Status: "open", OpenedAt: at(1), UpdatedAt: at(2), HeadIsFork: true,
		}, true},
		{"no sha is skipped", with(func(m *domain.MergeRequest) { m.SHA = "" }), domain.Listed{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.MapMergeRequest(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("MapMergeRequest(%s) ok = %v, want %v", tc.name, ok, tc.wantOK)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("MapMergeRequest(%s) mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestMapMergeRequests_dropsSkippedKeepsOrder(t *testing.T) {
	got := domain.MapMergeRequests([]domain.MergeRequest{
		{IID: 3, SHA: "a", State: "opened"},
		{IID: 2, SHA: "", State: "opened"},
		{IID: 1, SHA: "c", State: "merged"},
	})
	var numbers []int
	for _, l := range got {
		numbers = append(numbers, l.Number)
	}
	if diff := cmp.Diff([]int{3, 1}, numbers); diff != "" {
		t.Errorf("MapMergeRequests numbers mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLabIssueState(t *testing.T) {
	for in, want := range map[string]string{"opened": "open", "closed": "closed"} {
		if got := domain.GitLabIssueState(in); got != want {
			t.Errorf("GitLabIssueState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileFromDiff(t *testing.T) {
	const diff = "@@ -1,3 +1,3 @@\n keep\n-old\n+new\n+more\n\\ No newline at end of file\n"
	patch := diff
	tests := []struct {
		name string
		in   domain.GitLabDiff
		want domain.File
	}{
		{"modified", domain.GitLabDiff{OldPath: "a", NewPath: "b", Diff: diff},
			domain.File{Path: "b", Additions: 2, Deletions: 1, Patch: &patch}},
		{"deleted uses the old path", domain.GitLabDiff{OldPath: "a", NewPath: "a", DeletedFile: true, Diff: diff},
			domain.File{Path: "a", Additions: 2, Deletions: 1, Patch: &patch}},
		{"collapsed", domain.GitLabDiff{NewPath: "b", Diff: diff, Collapsed: true}, domain.File{Path: "b"}},
		{"too large", domain.GitLabDiff{NewPath: "b", Diff: diff, TooLarge: true}, domain.File{Path: "b"}},
		{"empty", domain.GitLabDiff{NewPath: "b"}, domain.File{Path: "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if d := cmp.Diff(tc.want, domain.FileFromDiff(tc.in)); d != "" {
				t.Errorf("FileFromDiff(%s) mismatch (-want +got):\n%s", tc.name, d)
			}
		})
	}
}

func TestCountDiffLines_removedLineStartingWithDashes(t *testing.T) {
	// A removed line "-- x" and an added line "++ y" are content, not file headers.
	add, del := domain.CountDiffLines("@@ -1 +1 @@\n--- x\n+++ y\n")
	if add != 1 || del != 1 {
		t.Errorf("CountDiffLines = (+%d, -%d), want (+1, -1)", add, del)
	}
}

func TestFileTotals(t *testing.T) {
	got := domain.FileTotals([]domain.File{{Path: "a", Additions: 2, Deletions: 1}, {Path: "b", Additions: 3}})
	if want := (domain.DiffStats{Additions: 5, Deletions: 1, FilesCount: 2}); got != want {
		t.Errorf("FileTotals = %+v, want %+v", got, want)
	}
}

func TestPullRef(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{{"gitlab", "!7"}, {"github", "#7"}, {"", "#7"}}
	for _, tc := range tests {
		if got := domain.PullRef(tc.provider, 7); got != tc.want {
			t.Errorf("PullRef(%q, 7) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

func TestRepo_PauseKey(t *testing.T) {
	tests := []struct {
		name string
		repo domain.Repo
		want string
	}{
		{"github default", domain.Repo{Owner: "o", Name: "r"}, "github/github.com"},
		{"github explicit", domain.Repo{Provider: "github", Host: "github.com"}, "github/github.com"},
		{"gitlab", domain.Repo{Provider: "gitlab", Host: "gitlab.com"}, "gitlab/gitlab.com"},
		{"gitlab with port", domain.Repo{Provider: "gitlab", Host: "git.corp:8443"}, "gitlab/git.corp:8443"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.repo.PauseKey(); got != tc.want {
				t.Errorf("PauseKey(%+v) = %q, want %q", tc.repo, got, tc.want)
			}
		})
	}
}
