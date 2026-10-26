package app_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

// setRepoHost turns the env's repo into a GitLab project (or back to GitHub).
func (e *env) setRepoHost(provider, host string, projectID int64) {
	r := e.store.repos[repoID]
	r.Provider, r.Host, r.ProjectID = provider, host, projectID
	e.store.repos[repoID] = r
}

func TestRepoCloneKey(t *testing.T) {
	tests := []struct {
		name      string
		repo      domain.Repo
		wantOwner string
		wantName  string
	}{
		{"github", domain.Repo{Provider: "github", Host: "github.com", Owner: "acme", Name: "widgets"}, "acme", "widgets"},
		{"legacy empty provider", domain.Repo{Owner: "acme", Name: "widgets"}, "acme", "widgets"},
		{"gitlab", domain.Repo{Provider: "gitlab", Host: "gitlab.example.com:8443", ProjectID: 42, Owner: "grp/sub", Name: "web"}, "@gitlab/gitlab.example.com_8443", "42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner, name := tc.repo.CloneKey()
			if owner != tc.wantOwner || name != tc.wantName {
				t.Errorf("CloneKey() = (%q, %q), want (%q, %q)", owner, name, tc.wantOwner, tc.wantName)
			}
		})
	}
}

func TestFull_GitLabRepoReadsTheGitLabCloneKey(t *testing.T) {
	e := newEnv(t)
	e.addFile("src/a.ts", "export function a() {}", fn("a", 1))
	e.setRepoHost("gitlab", "gitlab.example.com", 42)
	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if diff := cmp.Diff([]string{"@gitlab/gitlab.example.com/42"}, e.git.keys); diff != "" {
		t.Errorf("git keys mismatch (-want +got):\n%s", diff)
	}
}

func TestFull_GitHubRepoReadsOwnerAndName(t *testing.T) {
	e := newEnv(t)
	e.addFile("src/a.ts", "export function a() {}", fn("a", 1))
	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if diff := cmp.Diff([]string{"acme/widgets"}, e.git.keys); diff != "" {
		t.Errorf("git keys mismatch (-want +got):\n%s", diff)
	}
}

func TestIncremental_GitLabRepoReadsTheGitLabCloneKey(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	e.setRepoHost("gitlab", "gitlab.example.com", 42)
	e.git.head = "new-sha"
	e.git.diff = []string{"src/a.ts"}
	if _, err := e.ix.Incremental(t.Context(), repoID); err != nil {
		t.Fatalf("Incremental() error = %v", err)
	}
	want := []string{"@gitlab/gitlab.example.com/42", "@gitlab/gitlab.example.com/42"}
	if diff := cmp.Diff(want, e.git.keys); diff != "" {
		t.Errorf("git keys (Head, DiffNames) mismatch (-want +got):\n%s", diff)
	}
}

func TestJobs_ResyncGitLabRepoUsesTheGitLabCloneKey(t *testing.T) {
	j := newJobsEnv(t)
	j.addFile("src/a.ts", "x", fn("a", 1))
	j.seedIndex(domain.StatusFull)
	j.setRepoHost("gitlab", "gitlab.example.com", 42)
	j.git.branch = "trunk"
	j.git.diff = []string{"README.md"}
	if err := j.jobs.Resync(t.Context(), payloadOf(repoID)); err != nil {
		t.Fatalf("Resync() error = %v", err)
	}
	for i, k := range j.git.keys {
		if k != "@gitlab/gitlab.example.com/42" {
			t.Errorf("git call %d located the clone under %q, want @gitlab/gitlab.example.com/42", i, k)
		}
	}
	if len(j.git.keys) < 3 {
		t.Errorf("git calls = %v, want DefaultBranch, Sync and the incremental Head", j.git.keys)
	}
}
