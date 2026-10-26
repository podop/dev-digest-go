package peers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	agentsdomain "dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/ci/adapters/peers"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
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

func TestRepos_FindByFullName(t *testing.T) {
	d := peers.NewRepos(fakeRepoService{repos: []reposdomain.Repo{
		{Owner: "acme", Name: "other", FullName: "acme/other", DefaultBranch: "dev"},
		{Owner: "acme", Name: "widgets", FullName: "acme/widgets", DefaultBranch: "main"},
	}})
	got, ok, err := d.FindByFullName(t.Context(), "w", "acme/widgets")
	if err != nil || !ok {
		t.Fatalf("FindByFullName(acme/widgets) = ok %v, err %v", ok, err)
	}
	want := domain.ImportedRepo{Owner: "acme", Name: "widgets", FullName: "acme/widgets", DefaultBranch: "main"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindByFullName(acme/widgets) mismatch (-want +got):\n%s", diff)
	}
	if _, ok, err := d.FindByFullName(t.Context(), "w", "Acme/Widgets"); ok || err != nil {
		t.Errorf("FindByFullName(Acme/Widgets) = ok %v, err %v, want false, nil (exact match)", ok, err)
	}
	boom := errors.New("db down")
	if _, _, err := peers.NewRepos(fakeRepoService{err: boom}).FindByFullName(t.Context(), "w", "a/b"); !errors.Is(err, boom) {
		t.Errorf("FindByFullName error = %v, want %v", err, boom)
	}
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

func TestRepos_FindByFullName_HostIsolation(t *testing.T) {
	github := reposdomain.Repo{Owner: "acme", Name: "web", FullName: "acme/web", DefaultBranch: "main", Provider: "github", Host: "github.com"}
	legacy := reposdomain.Repo{Owner: "acme", Name: "old", FullName: "acme/old", DefaultBranch: "main"}
	gitlab := reposdomain.Repo{Owner: "acme", Name: "web", FullName: "acme/web", DefaultBranch: "trunk", Provider: "gitlab", Host: "gitlab.com"}
	tests := []struct {
		name    string
		repos   []reposdomain.Repo
		find    string
		want    domain.ImportedRepo
		found   bool
		wantErr error
	}{
		{name: "only gitlab", repos: []reposdomain.Repo{gitlab}, find: "acme/web", wantErr: app.ErrUnsupportedHost},
		{name: "both, gitlab first", repos: []reposdomain.Repo{gitlab, github}, find: "acme/web",
			want: domain.ImportedRepo{Owner: "acme", Name: "web", FullName: "acme/web", DefaultBranch: "main"}, found: true},
		{name: "both, github first", repos: []reposdomain.Repo{github, gitlab}, find: "acme/web",
			want: domain.ImportedRepo{Owner: "acme", Name: "web", FullName: "acme/web", DefaultBranch: "main"}, found: true},
		{name: "empty provider is github", repos: []reposdomain.Repo{legacy}, find: "acme/old",
			want: domain.ImportedRepo{Owner: "acme", Name: "old", FullName: "acme/old", DefaultBranch: "main"}, found: true},
		{name: "gitlab elsewhere does not match another name", repos: []reposdomain.Repo{gitlab}, find: "acme/other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := peers.NewRepos(fakeRepoService{repos: tc.repos}).FindByFullName(t.Context(), "w", tc.find)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("FindByFullName(%s) error = %v, want %v", tc.find, err, tc.wantErr)
			}
			if ok != tc.found {
				t.Errorf("FindByFullName(%s) found = %v, want %v", tc.find, ok, tc.found)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FindByFullName(%s) mismatch (-want +got):\n%s", tc.find, diff)
			}
		})
	}
}
