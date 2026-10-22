package seed

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestPR482Patches_MatchNodeGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/pr482_patches.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(want) != 4 {
		t.Fatalf("golden has %d patches, want 4", len(want))
	}
	if diff := cmp.Diff(want, pr482Patches()); diff != "" {
		t.Errorf("pr482Patches() mismatch against Node SEED_PR_482_PATCHES (-want +got):\n%s", diff)
	}
}

// fakeTx records the calls the seed makes and hands out predictable ids.
type fakeTx struct {
	calls   []string
	failOn  string
	patches map[string]string

	workspaceExists bool // EnsureWorkspace reports an existing row
	occupied        bool // HasAgentsOrSkills result
	agents, skills  map[string]string
	links           map[string][]string // agent id -> skill ids
}

func (f *fakeTx) record(name string) error {
	f.calls = append(f.calls, name)
	if name == f.failOn {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeTx) EnsureWorkspace(_ context.Context, name string) (string, bool, error) {
	return "ws", !f.workspaceExists, f.record("workspace:" + name)
}
func (f *fakeTx) EnsureUser(_ context.Context, email, _ string) (string, bool, error) {
	return "user", true, f.record("user:" + email)
}
func (f *fakeTx) EnsureMember(_ context.Context, ws, user, role string) (bool, error) {
	return true, f.record("member:" + ws + "/" + user + "/" + role)
}
func (f *fakeTx) EnsureSetting(_ context.Context, _, _, key string, value json.RawMessage) (bool, error) {
	return true, f.record("setting:" + key + "=" + string(value))
}
func (f *fakeTx) EnsureRepo(_ context.Context, r Repo) (string, bool, error) {
	return "repo", true, f.record("repo:" + r.FullName + "@" + r.WorkspaceID)
}
func (f *fakeTx) EnsurePullRequest(_ context.Context, p PullRequest) (string, bool, error) {
	return "pr", true, f.record("pr:" + p.RepoID)
}
func (f *fakeTx) EnsureFile(_ context.Context, prID string, file File) (bool, bool, error) {
	f.patches[file.Path] = file.Patch
	return true, false, f.record("file:" + prID + "/" + file.Path)
}
func (f *fakeTx) EnsureCommit(_ context.Context, prID string, c Commit) (bool, error) {
	return true, f.record("commit:" + prID + "/" + c.SHA)
}

func (f *fakeTx) HasAgentsOrSkills(_ context.Context, ws string) (bool, error) {
	return f.occupied, f.record("has-knowledge:" + ws)
}
func (f *fakeTx) EnsureAgent(_ context.Context, a Agent) (string, bool, error) {
	if f.agents == nil {
		f.agents = map[string]string{}
	}
	id := "agent:" + a.Name
	f.agents[a.Name] = a.Provider + "/" + a.Model + "/" + a.CreatedBy
	return id, true, f.record("agent:" + a.Name)
}
func (f *fakeTx) EnsureSkill(_ context.Context, sk Skill) (string, bool, error) {
	if f.skills == nil {
		f.skills = map[string]string{}
	}
	f.skills[sk.Name] = sk.Type
	return "skill:" + sk.Name, true, f.record("skill:" + sk.Name)
}
func (f *fakeTx) LinkAgentSkills(_ context.Context, agentID string, skillIDs []string) (int, error) {
	if f.links == nil {
		f.links = map[string][]string{}
	}
	f.links[agentID] = skillIDs
	return len(skillIDs), f.record("link:" + agentID)
}

type fakeStore struct{ tx *fakeTx }

func (s fakeStore) InTx(ctx context.Context, fn func(context.Context, Tx) error) error {
	return fn(ctx, s.tx)
}

func TestSeederRun(t *testing.T) {
	tx := &fakeTx{patches: map[string]string{}}
	res, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantCalls := []string{
		"workspace:default", "user:you@local", "member:ws/user/owner",
		"setting:polling_interval_min=5", `setting:theme="dark"`, `setting:density="regular"`, "setting:sync_to_folder=true",
		"repo:acme/payments-api@ws", "pr:repo",
		"file:pr/src/middleware/ratelimit.ts", "file:pr/src/api/public/webhooks.ts",
		"file:pr/src/config.ts", "file:pr/src/api/users.ts",
		"commit:pr/a1b2c3d4e5f6",
		"agent:General Reviewer", "agent:Security Reviewer", "agent:Performance Reviewer", "agent:Test Quality Reviewer",
		"skill:pr-quality-rubric", "skill:no-then-chains", "skill:secret-leakage-gate", "skill:lethal-trifecta",
		"skill:phantom-api-gate", "skill:n-plus-one-gate", "skill:test-coverage-nudge", "skill:mock-discipline",
		"skill:corner-case-hunter",
		"link:agent:General Reviewer", "link:agent:Security Reviewer", "link:agent:Performance Reviewer",
		"link:agent:Test Quality Reviewer",
	}
	if diff := cmp.Diff(wantCalls, tx.calls); diff != "" {
		t.Errorf("Run() calls mismatch (-want +got):\n%s", diff)
	}
	wantRes := Result{WorkspaceID: "ws", UserID: "user", Workspaces: 1, Users: 1, Members: 1, Settings: 4, Repos: 1, PullRequests: 1, Files: 4, Commits: 1, Agents: 4, Skills: 9, AgentSkills: 9}
	if diff := cmp.Diff(wantRes, res); diff != "" {
		t.Errorf("Run() result mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(pr482Patches(), tx.patches); diff != "" {
		t.Errorf("Run() file patches mismatch (-want +got):\n%s", diff)
	}
}

func TestSeederRun_ErrorNamesTheStep(t *testing.T) {
	tx := &fakeTx{patches: map[string]string{}, failOn: "commit:pr/a1b2c3d4e5f6"}
	_, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context())
	if err == nil || err.Error() != "seed: pr commit: boom" {
		t.Errorf("Run() error = %v, want %q", err, "seed: pr commit: boom")
	}
}

func TestSeederRun_LinksFollowTheNodeOrder(t *testing.T) {
	tx := &fakeTx{patches: map[string]string{}}
	if _, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := map[string][]string{
		"agent:General Reviewer":      {"skill:pr-quality-rubric", "skill:no-then-chains"},
		"agent:Security Reviewer":     {"skill:secret-leakage-gate", "skill:lethal-trifecta", "skill:phantom-api-gate"},
		"agent:Performance Reviewer":  {"skill:n-plus-one-gate"},
		"agent:Test Quality Reviewer": {"skill:test-coverage-nudge", "skill:mock-discipline", "skill:corner-case-hunter"},
	}
	if diff := cmp.Diff(want, tx.links); diff != "" {
		t.Errorf("agent skill links mismatch (-want +got):\n%s", diff)
	}
	for name, got := range tx.agents {
		if got != "openrouter/deepseek/deepseek-v4-flash/user" {
			t.Errorf("agent %s provider/model/creator = %q, want openrouter/deepseek/deepseek-v4-flash/user", name, got)
		}
	}
}

func TestSeederRun_KnowledgeOnlyForNewOrEmptyWorkspace(t *testing.T) {
	tests := []struct {
		name            string
		workspaceExists bool
		occupied        bool
		wantAgents      int
		wantChecked     bool
	}{
		{name: "new workspace", workspaceExists: false, wantAgents: 4},
		{name: "new workspace skips the check even if rows were reported", workspaceExists: false, occupied: true, wantAgents: 4},
		{name: "existing workspace without agents and skills", workspaceExists: true, wantAgents: 4, wantChecked: true},
		{name: "existing workspace with agents or skills", workspaceExists: true, occupied: true, wantAgents: 0, wantChecked: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{patches: map[string]string{}, workspaceExists: tc.workspaceExists, occupied: tc.occupied}
			res, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context())
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if res.Agents != tc.wantAgents {
				t.Errorf("Run() agents = %d, want %d", res.Agents, tc.wantAgents)
			}
			if tc.wantAgents == 0 && (res.Skills != 0 || res.AgentSkills != 0 || len(tx.links) != 0) {
				t.Errorf("Run() = %+v with links %v, want no skills or links", res, tx.links)
			}
			checked := slices.Contains(tx.calls, "has-knowledge:ws")
			if checked != tc.wantChecked {
				t.Errorf("HasAgentsOrSkills called = %v, want %v", checked, tc.wantChecked)
			}
		})
	}
}

func TestSeederRun_KnowledgeErrorNamesTheStep(t *testing.T) {
	tx := &fakeTx{patches: map[string]string{}, failOn: "skill:lethal-trifecta"}
	_, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context())
	if err == nil || err.Error() != "seed: skill lethal-trifecta: boom" {
		t.Errorf("Run() error = %v, want %q", err, "seed: skill lethal-trifecta: boom")
	}
}
