package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
)

// Spec C FR46: groups are keyed by host + name for GitHub and host + project id for GitLab, and
// ordered by repo then host.
func TestGroupByRepo_GitLabIdentity(t *testing.T) {
	t.Parallel()
	gl := func(id, repo, host string, project int64) domain.RepoInstallation {
		return domain.RepoInstallation{ID: id, AgentName: "a", Repo: repo, TargetType: domain.TargetGitLabCI, Host: host, ProjectID: project}
	}
	gh := func(id, repo string) domain.RepoInstallation {
		return domain.RepoInstallation{ID: id, AgentName: "a", Repo: repo, TargetType: domain.TargetGHA}
	}
	got := domain.GroupByRepo([]domain.RepoInstallation{
		gl("5", "g/p", "gitlab.b.test", 7), // same path, other host
		gl("4", "g/p", "gitlab.a.test", 7),
		gl("3", "g/renamed", "gitlab.a.test", 7), // same project, path changed: one group
		gl("2", "g/p", "gitlab.a.test", 8),       // other project, same path: its own group
		gh("1", "g/p"),
	})
	type summary struct {
		Repo, Host, TargetType string
		ProjectID              int64
		IDs                    []string
	}
	var have []summary
	for _, g := range got {
		s := summary{Repo: g.Repo, Host: g.Host, TargetType: g.TargetType, ProjectID: g.ProjectID}
		for _, in := range g.Installations {
			s.IDs = append(s.IDs, in.ID)
		}
		have = append(have, s)
	}
	// Installations 3 and 4 share host and project id, so they form one group; it takes the path
	// of its first installation (ordered by agent name, installed_at, id).
	want := []summary{
		{"g/p", "github.com", domain.TargetGHA, 0, []string{"1"}},
		{"g/p", "gitlab.a.test", domain.TargetGitLabCI, 8, []string{"2"}},
		{"g/p", "gitlab.b.test", domain.TargetGitLabCI, 7, []string{"5"}},
		{"g/renamed", "gitlab.a.test", domain.TargetGitLabCI, 7, []string{"3", "4"}},
	}
	if diff := cmp.Diff(want, have); diff != "" {
		t.Errorf("GroupByRepo() mismatch (-want +got):\n%s", diff)
	}
}
