package peers_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	agentsdomain "dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/ci/adapters/peers"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
	reposapp "dev-digest-go/backend/internal/repos/app"
	reposdomain "dev-digest-go/backend/internal/repos/domain"
	skillsdomain "dev-digest-go/backend/internal/skills/domain"
)

type fakeAgentService struct {
	agents map[string]agentsdomain.Agent
	links  map[string][]agentsdomain.SkillLink
	err    error
}

func (f fakeAgentService) Get(_ context.Context, _, id string) (agentsdomain.Agent, error) {
	if f.err != nil {
		return agentsdomain.Agent{}, f.err
	}
	a, ok := f.agents[id]
	if !ok {
		return agentsdomain.Agent{}, apperr.NotFound("Agent not found")
	}
	return a, nil
}

func (f fakeAgentService) SkillLinks(_ context.Context, _, id string) ([]agentsdomain.SkillLink, error) {
	return f.links[id], nil
}

type fakeSkillService struct {
	skills map[string]skillsdomain.Skill
	err    error
}

func (f fakeSkillService) Get(_ context.Context, _, id string) (skillsdomain.Skill, error) {
	if f.err != nil {
		return skillsdomain.Skill{}, f.err
	}
	s, ok := f.skills[id]
	if !ok {
		return skillsdomain.Skill{}, apperr.NotFound("Skill not found")
	}
	return s, nil
}

func TestAgents_Get(t *testing.T) {
	agents := fakeAgentService{
		agents: map[string]agentsdomain.Agent{"a1": {
			ID: "a1", Name: "Sec", Provider: "openrouter", Model: "m", SystemPrompt: "sp", Strategy: "single-pass",
			CIFailOn: "critical", Version: 3, Description: "ignored",
		}},
		links: map[string][]agentsdomain.SkillLink{"a1": {
			{AgentID: "a1", SkillID: "s2", Order: 0},
			{AgentID: "a1", SkillID: "s-off", Order: 1},
			{AgentID: "a1", SkillID: "s-gone", Order: 2},
			{AgentID: "a1", SkillID: "s1", Order: 3},
		}},
	}
	skills := fakeSkillService{skills: map[string]skillsdomain.Skill{
		"s1":    {ID: "s1", Name: "One", Body: "body one", Enabled: true},
		"s2":    {ID: "s2", Name: "Two", Body: "body two", Enabled: true},
		"s-off": {ID: "s-off", Name: "Off", Body: "x", Enabled: false},
	}}
	d := peers.NewAgents(agents, skills)

	got, ok, err := d.Get(t.Context(), "w", "a1")
	if err != nil || !ok {
		t.Fatalf("Get(a1) = ok %v, err %v, want ok, nil", ok, err)
	}
	want := domain.ExportAgent{
		ID: "a1", Name: "Sec", Provider: "openrouter", Model: "m", SystemPrompt: "sp", Strategy: "single-pass",
		CIFailOn: "critical", Version: 3,
		Skills: []domain.ExportSkill{{Name: "Two", Body: "body two"}, {Name: "One", Body: "body one"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Get(a1) mismatch (-want +got):\n%s", diff)
	}

	if _, ok, err := d.Get(t.Context(), "w", "nope"); ok || err != nil {
		t.Errorf("Get(nope) = ok %v, err %v, want false, nil", ok, err)
	}
	if ok, err := d.Exists(t.Context(), "w", "a1"); !ok || err != nil {
		t.Errorf("Exists(a1) = %v, %v, want true, nil", ok, err)
	}
	if ok, err := d.Exists(t.Context(), "w", "nope"); ok || err != nil {
		t.Errorf("Exists(nope) = %v, %v, want false, nil", ok, err)
	}
}

func TestAgents_Errors(t *testing.T) {
	boom := errors.New("db down")
	agents := fakeAgentService{
		agents: map[string]agentsdomain.Agent{"a1": {ID: "a1"}},
		links:  map[string][]agentsdomain.SkillLink{"a1": {{SkillID: "s1"}}},
	}
	tests := []struct {
		name string
		d    *peers.Agents
	}{
		{name: "agent lookup", d: peers.NewAgents(fakeAgentService{err: boom}, fakeSkillService{})},
		{name: "skill lookup", d: peers.NewAgents(agents, fakeSkillService{err: boom})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := tc.d.Get(t.Context(), "w", "a1"); !errors.Is(err, boom) {
				t.Errorf("Get(a1) error = %v, want %v", err, boom)
			}
		})
	}
}

type fakeRepoService struct {
	repos []reposdomain.Repo
	err   error
}

func (f fakeRepoService) List(context.Context, string) ([]reposdomain.Repo, error) {
	return f.repos, f.err
}

func TestAgents_Get_PluginGating(t *testing.T) {
	off := &skillsdomain.PluginRef{ID: "p1", Name: "Pack", Enabled: false}
	on := &skillsdomain.PluginRef{ID: "p2", Name: "Live", Enabled: true}
	agents := fakeAgentService{
		agents: map[string]agentsdomain.Agent{
			"owned": {ID: "owned", Name: "Owned", Plugin: &agentsdomain.PluginRef{ID: "p1", Name: "Pack", Enabled: false}},
			"plain": {ID: "plain", Name: "Plain"},
		},
		links: map[string][]agentsdomain.SkillLink{
			"plain": {{SkillID: "s-off", Order: 0}, {SkillID: "s-on", Order: 1}, {SkillID: "s-own", Order: 2}},
		},
	}
	skills := fakeSkillService{skills: map[string]skillsdomain.Skill{
		"s-off": {ID: "s-off", Name: "Of disabled plugin", Enabled: true, Plugin: off},
		"s-on":  {ID: "s-on", Name: "Of enabled plugin", Enabled: true, Plugin: on},
		"s-own": {ID: "s-own", Name: "Own", Enabled: true},
	}}
	d := peers.NewAgents(agents, skills)

	owned, ok, err := d.Get(t.Context(), "w", "owned")
	if err != nil || !ok {
		t.Fatalf("Get(owned) = ok %v, err %v", ok, err)
	}
	if diff := cmp.Diff(&domain.PluginRef{Name: "Pack", Enabled: false}, owned.Plugin); diff != "" {
		t.Errorf("Get(owned).Plugin mismatch (-want +got):\n%s", diff)
	}

	plain, _, err := d.Get(t.Context(), "w", "plain")
	if err != nil {
		t.Fatalf("Get(plain) error = %v", err)
	}
	if plain.Plugin != nil {
		t.Errorf("Get(plain).Plugin = %+v, want nil", plain.Plugin)
	}
	want := []domain.ExportSkill{{Name: "Of enabled plugin"}, {Name: "Own"}}
	if diff := cmp.Diff(want, plain.Skills); diff != "" {
		t.Errorf("Get(plain).Skills mismatch (-want +got):\n%s", diff)
	}
}

func strp(s string) *string { return &s }

var (
	ghRepo = reposdomain.Repo{ID: "r-gh", Owner: "acme", Name: "web", FullName: "acme/web", DefaultBranch: "main",
		Provider: "github", Host: "github.com", ClonePath: strp("/clones/acme/web")}
	legacyRepo = reposdomain.Repo{ID: "r-old", Owner: "acme", Name: "old", FullName: "acme/old", DefaultBranch: "main"}
	glRepo     = reposdomain.Repo{ID: "r-gl", Owner: "acme", Name: "web", FullName: "acme/web", DefaultBranch: "trunk",
		Provider: "gitlab", Host: "gitlab.com", ProjectID: 42, WebURL: "https://gitlab.com/acme/web"}
	glOther = reposdomain.Repo{ID: "r-gl2", Owner: "acme", Name: "web", FullName: "acme/web", DefaultBranch: "dev",
		Provider: "gitlab", Host: "git.corp.example:8443", ProjectID: 7, WebURL: "https://git.corp.example:8443/acme/web",
		ClonePath: strp("/clones/@gitlab/git.corp.example_8443/7")}
)

func TestRepos_Find(t *testing.T) {
	tests := []struct {
		name    string
		repos   []reposdomain.Repo
		q       app.RepoQuery
		want    app.ExportRepo
		found   bool
		wantIDs []string
	}{
		{name: "by id, github", repos: []reposdomain.Repo{glRepo, ghRepo}, q: app.RepoQuery{ID: "r-gh"}, found: true,
			want: app.ExportRepo{ID: "r-gh", Provider: app.ProviderGitHub, Host: "github.com", Owner: "acme", Name: "web",
				FullName: "acme/web", DefaultBranch: "main", ClonePath: "/clones/acme/web"}},
		{name: "by id, gitlab", repos: []reposdomain.Repo{ghRepo, glOther}, q: app.RepoQuery{ID: "r-gl2"}, found: true,
			want: app.ExportRepo{ID: "r-gl2", Provider: app.ProviderGitLab, Host: "git.corp.example:8443", Owner: "acme",
				Name: "web", FullName: "acme/web", DefaultBranch: "dev", ProjectID: 7,
				WebURL: "https://git.corp.example:8443/acme/web", ClonePath: "/clones/@gitlab/git.corp.example_8443/7"}},
		{name: "id wins over full name", repos: []reposdomain.Repo{ghRepo, glRepo}, q: app.RepoQuery{ID: "r-gl", FullName: "acme/web"},
			found: true, want: app.ExportRepo{ID: "r-gl", Provider: app.ProviderGitLab, Host: "gitlab.com", Owner: "acme",
				Name: "web", FullName: "acme/web", DefaultBranch: "trunk", ProjectID: 42, WebURL: "https://gitlab.com/acme/web"}},
		{name: "unknown id is not another repo's name", repos: []reposdomain.Repo{ghRepo}, q: app.RepoQuery{ID: "nope", FullName: "acme/web"}},
		{name: "legacy empty provider is github", repos: []reposdomain.Repo{legacyRepo}, q: app.RepoQuery{FullName: "acme/old"},
			found: true, want: app.ExportRepo{ID: "r-old", Provider: app.ProviderGitHub, Host: "github.com", Owner: "acme",
				Name: "old", FullName: "acme/old", DefaultBranch: "main"}},
		{name: "github wins over gitlab, either order", repos: []reposdomain.Repo{glRepo, glOther, ghRepo}, q: app.RepoQuery{FullName: "acme/web"},
			found: true, want: app.ExportRepo{ID: "r-gh", Provider: app.ProviderGitHub, Host: "github.com", Owner: "acme",
				Name: "web", FullName: "acme/web", DefaultBranch: "main", ClonePath: "/clones/acme/web"}},
		{name: "github beats a gitlab one", repos: []reposdomain.Repo{ghRepo, glRepo}, q: app.RepoQuery{FullName: "acme/web"}, found: true,
			want: app.ExportRepo{ID: "r-gh", Provider: app.ProviderGitHub, Host: "github.com", Owner: "acme", Name: "web",
				FullName: "acme/web", DefaultBranch: "main", ClonePath: "/clones/acme/web"}},
		{name: "only gitlab", repos: []reposdomain.Repo{glRepo}, q: app.RepoQuery{FullName: "acme/web"}, found: true,
			want: app.ExportRepo{ID: "r-gl", Provider: app.ProviderGitLab, Host: "gitlab.com", Owner: "acme", Name: "web",
				FullName: "acme/web", DefaultBranch: "trunk", ProjectID: 42, WebURL: "https://gitlab.com/acme/web"}},
		{name: "gitlab on two hosts is ambiguous", repos: []reposdomain.Repo{glRepo, glOther}, q: app.RepoQuery{FullName: "acme/web"},
			wantIDs: []string{"r-gl", "r-gl2"}},
		{name: "exact match only", repos: []reposdomain.Repo{ghRepo}, q: app.RepoQuery{FullName: "Acme/Web"}},
		{name: "other name", repos: []reposdomain.Repo{glRepo}, q: app.RepoQuery{FullName: "acme/other"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := peers.NewRepos(fakeRepoService{repos: tc.repos}).Find(t.Context(), "w", tc.q)
			if tc.wantIDs != nil {
				var amb *app.AmbiguousRepoError
				if !errors.As(err, &amb) {
					t.Fatalf("Find(%+v) error = %v, want *AmbiguousRepoError", tc.q, err)
				}
				if diff := cmp.Diff(tc.wantIDs, amb.IDs); diff != "" {
					t.Errorf("Find(%+v) ids mismatch (-want +got):\n%s", tc.q, diff)
				}
				return
			}
			if err != nil {
				t.Fatalf("Find(%+v) error = %v", tc.q, err)
			}
			if ok != tc.found {
				t.Errorf("Find(%+v) found = %v, want %v", tc.q, ok, tc.found)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Find(%+v) mismatch (-want +got):\n%s", tc.q, diff)
			}
		})
	}
}

func TestRepos_Find_ListError(t *testing.T) {
	boom := errors.New("db down")
	if _, _, err := peers.NewRepos(fakeRepoService{err: boom}).Find(t.Context(), "w", app.RepoQuery{FullName: "a/b"}); !errors.Is(err, boom) {
		t.Errorf("Find error = %v, want %v", err, boom)
	}
}

type pathCall struct {
	Workspace, ID string
	P             reposapp.ProjectPath
}

type fakePathStore struct {
	calls      []pathCall
	skippedFor string
	err        error
}

func (f *fakePathStore) UpdatePath(_ context.Context, ws, id string, p reposapp.ProjectPath) (string, error) {
	f.calls = append(f.calls, pathCall{ws, id, p})
	return f.skippedFor, f.err
}

func resolveTo(base string) glclient.ResolveFunc {
	return func(_ context.Context, host string) (glclient.Binding, bool, error) {
		if host == "" {
			return glclient.Binding{}, false, nil
		}
		return glclient.Binding{Host: host, BaseURL: base, Token: "glpat-secret-token-value"}, true, nil
	}
}

func exportGL() app.ExportRepo {
	return app.ExportRepo{ID: "r-gl", Provider: app.ProviderGitLab, Host: "gitlab.com", Owner: "acme", Name: "web",
		FullName: "acme/web", ProjectID: 42, WebURL: "https://gitlab.com/acme/web"}
}

func TestRepos_FollowGitLabPath(t *testing.T) {
	t.Run("renamed", func(t *testing.T) {
		store := &fakePathStore{}
		d := peers.NewRepos(fakeRepoService{}).WithGitLab(store, resolveTo("https://gitlab.com/"), nil)
		got, err := d.FollowGitLabPath(t.Context(), "w", exportGL(), "acme/team/web2")
		if err != nil {
			t.Fatalf("FollowGitLabPath() error = %v", err)
		}
		want := exportGL()
		want.FullName, want.Owner, want.Name, want.WebURL = "acme/team/web2", "acme/team", "web2", "https://gitlab.com/acme/team/web2"
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("FollowGitLabPath() mismatch (-want +got):\n%s", diff)
		}
		wantCalls := []pathCall{{"w", "r-gl", reposapp.ProjectPath{FullName: "acme/team/web2", Owner: "acme/team", Name: "web2",
			WebURL: "https://gitlab.com/acme/team/web2"}}}
		if diff := cmp.Diff(wantCalls, store.calls); diff != "" {
			t.Errorf("UpdatePath calls mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("web address is built from the connection's prefix", func(t *testing.T) {
		store := &fakePathStore{}
		d := peers.NewRepos(fakeRepoService{}).WithGitLab(store, resolveTo("https://git.corp.example/gitlab"), nil)
		repo := exportGL()
		repo.Host, repo.WebURL = "git.corp.example", ""
		got, err := d.FollowGitLabPath(t.Context(), "w", repo, "acme/web")
		if err != nil || got.WebURL != "https://git.corp.example/gitlab/acme/web" {
			t.Errorf("FollowGitLabPath() = %+v, %v, want web_url from the connection (a missing address is filled)", got, err)
		}
	})
	t.Run("unchanged path is not stored", func(t *testing.T) {
		store := &fakePathStore{}
		d := peers.NewRepos(fakeRepoService{}).WithGitLab(store, resolveTo("https://gitlab.com"), nil)
		got, err := d.FollowGitLabPath(t.Context(), "w", exportGL(), "acme/web")
		if err != nil || len(store.calls) != 0 {
			t.Errorf("FollowGitLabPath(same) = %+v, %v, %d store calls, want none", got, err, len(store.calls))
		}
	})
	t.Run("path held by another repo", func(t *testing.T) {
		var buf strings.Builder
		store := &fakePathStore{skippedFor: "r-other"}
		d := peers.NewRepos(fakeRepoService{}).WithGitLab(store, resolveTo("https://gitlab.com"),
			slog.New(slog.NewTextHandler(&buf, nil)))
		got, err := d.FollowGitLabPath(t.Context(), "w", exportGL(), "acme/taken")
		if err != nil {
			t.Fatalf("FollowGitLabPath() error = %v", err)
		}
		if diff := cmp.Diff(exportGL(), got); diff != "" {
			t.Errorf("FollowGitLabPath(taken) mismatch (-want +got):\n%s", diff)
		}
		if !strings.Contains(buf.String(), "r-other") || strings.Contains(buf.String(), "glpat") {
			t.Errorf("log = %q, want one warning naming the other repo and no token", buf.String())
		}
	})
}

func TestRepos_FollowGitLabPath_Refusals(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name    string
		d       *peers.Repos
		repo    app.ExportRepo
		path    string
		wantErr error
	}{
		{name: "not a plain path", d: peers.NewRepos(nil).WithGitLab(&fakePathStore{}, resolveTo("https://gitlab.com"), nil),
			repo: exportGL(), path: "acme/../etc"},
		{name: "one segment", d: peers.NewRepos(nil).WithGitLab(&fakePathStore{}, resolveTo("https://gitlab.com"), nil),
			repo: exportGL(), path: "web"},
		{name: "percent sign", d: peers.NewRepos(nil).WithGitLab(&fakePathStore{}, resolveTo("https://gitlab.com"), nil),
			repo: exportGL(), path: "acme/we%62"},
		{name: "github repository", d: peers.NewRepos(nil).WithGitLab(&fakePathStore{}, resolveTo("https://gitlab.com"), nil),
			repo: app.ExportRepo{ID: "r-gh", Provider: app.ProviderGitHub, Host: "github.com", FullName: "acme/web"}, path: "acme/web2"},
		{name: "not configured", d: peers.NewRepos(nil), repo: exportGL(), path: "acme/web2"},
		{name: "connection gone", d: peers.NewRepos(nil).WithGitLab(&fakePathStore{}, resolveTo("https://gitlab.com"), nil),
			repo: app.ExportRepo{ID: "r", Provider: app.ProviderGitLab, FullName: "acme/web"}, path: "acme/web2"},
		{name: "store fails", d: peers.NewRepos(nil).WithGitLab(&fakePathStore{err: boom}, resolveTo("https://gitlab.com"), nil),
			repo: exportGL(), path: "acme/web2", wantErr: boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.d.FollowGitLabPath(t.Context(), "w", tc.repo, tc.path)
			if err == nil {
				t.Fatalf("FollowGitLabPath(%q) = %+v, nil, want an error", tc.path, got)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("FollowGitLabPath(%q) error = %v, want %v", tc.path, err, tc.wantErr)
			}
		})
	}
}
