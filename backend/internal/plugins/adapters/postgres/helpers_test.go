package postgres_test

import (
	"encoding/json"
	"os"
	"testing"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/plugins/adapters/postgres"
	"dev-digest-go/backend/internal/plugins/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func ptr[T any](v T) *T { return &v }

// env is a migrated database with one workspace (ws) and a second one (other).
type env struct {
	db         *gorm.DB
	store      *pgstore.Store
	ws, other  string
	nextRepoNo int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	e := &env{db: d.DB, store: pgstore.NewStore(d.DB)}
	e.ws = e.scalar(t, `INSERT INTO workspaces (name) VALUES ('w') RETURNING id`)
	e.other = e.scalar(t, `INSERT INTO workspaces (name) VALUES ('o') RETURNING id`)
	return e
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if err := e.db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) scalar(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := e.db.Raw(sql, args...).Scan(&out).Error; err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return out
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.Raw(sql, args...).Scan(&n).Error; err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

func (e *env) strings(t *testing.T, sql string, args ...any) []string {
	t.Helper()
	out := []string{}
	if err := e.db.Raw(sql, args...).Scan(&out).Error; err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return out
}

// userAgent inserts an agent that no plugin owns; the creation order follows the call order.
func (e *env) userAgent(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return e.scalar(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled, created_at)
		VALUES (?, ?, 'openai', 'gpt-x', 'prompt of '||?::text, ?, clock_timestamp()) RETURNING id`, ws, name, name, enabled)
}

func (e *env) userSkill(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return e.scalar(t, `INSERT INTO skills (workspace_id, name, description, type, source, body, enabled, created_at)
		VALUES (?, ?, 'desc', 'custom', 'manual', 'body of '||?::text, ?, clock_timestamp()) RETURNING id`, ws, name, name, enabled)
}

func (e *env) link(t *testing.T, agent, skill string, order int) {
	t.Helper()
	e.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, ?)`, agent, skill, order)
}

func (e *env) evalCase(t *testing.T, ws, kind, owner, name string) string {
	t.Helper()
	return e.scalar(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, input_diff, created_at)
		VALUES (?, ?, ?, ?, 'diff', clock_timestamp()) RETURNING id`, ws, kind, owner, name)
}

func (e *env) repo(t *testing.T, ws, fullName string) string {
	t.Helper()
	return e.scalar(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', ?) RETURNING id`, ws, fullName)
}

// learning inserts a memory row of the user (no plugin); repo may be "".
func (e *env) learning(t *testing.T, ws, repo, content string) {
	t.Helper()
	var r any
	if repo != "" {
		r = repo
	}
	e.exec(t, `INSERT INTO memory (workspace_id, repo_id, scope, kind, content, confidence, created_at)
		VALUES (?, ?, 'repo', 'fact', ?, 0.5, clock_timestamp())`, ws, r, content)
}

// plan is an install plan with two agents, three skills, two eval cases and two learnings:
//
//	agent "<p>-reviewer": links skill "<p>-style" then "<p>-sec" (so link order != name order)
//	agent "<p>-idle": no skills, disabled, with an output schema
//	skill "<p>-docs": linked by nobody, disabled, with evidence files
func plan(p string) domain.Install {
	return domain.Install{
		Name: p, Version: "1.2.0", Description: ptr("the " + p + " plugin"), Source: p + ".zip",
		Agents: []domain.InstallAgent{
			{
				Name: p + "-reviewer", Description: "reviews", Provider: "openai", Model: "gpt-x", SystemPrompt: "be nice",
				Enabled: true, Strategy: "map-reduce", CIFailOn: "warning", RepoIntel: true,
				Skills: []string{p + "-style", p + "-sec"},
			},
			{
				Name: p + "-idle", Provider: "anthropic", Model: "claude-x", SystemPrompt: "idle",
				OutputSchema: json.RawMessage(`{"type":"object"}`), Enabled: false, Strategy: "single-pass", CIFailOn: "critical",
			},
		},
		Skills: []domain.InstallSkill{
			{Name: p + "-style", Description: "style rules", Type: "convention", Source: "imported_file", SourceRef: "plugin:" + p,
				Body: "style body", Enabled: true, VersionNote: "Installed from plugin " + p},
			{Name: p + "-sec", Description: "security rules", Type: "security", Source: "imported_file", SourceRef: "plugin:" + p,
				Body: "sec body", Enabled: true, VersionNote: "Installed from plugin " + p},
			{Name: p + "-docs", Description: "docs", Type: "custom", Source: "imported_file", SourceRef: "plugin:" + p,
				Body: "docs body", Enabled: false, EvidenceFiles: []string{"a.md", "b.md"}, VersionNote: "Installed from plugin " + p},
		},
		EvalCases: []domain.InstallCase{
			{Name: "agent case", OwnerKind: "agent", OwnerRef: p + "-reviewer", InputDiff: ptr("diff --git"),
				InputFiles: json.RawMessage(`["a.go"]`), InputMeta: json.RawMessage(`{"pr_title":"t"}`),
				ExpectedOutput: json.RawMessage(`{"kind":"must_find"}`), Notes: ptr("n")},
			{Name: "skill case", OwnerKind: "skill", OwnerRef: p + "-sec"},
		},
		Learnings: []domain.InstallLearning{
			{Scope: "global", Kind: "fact", Content: "first learning", Confidence: ptr(0.75)},
			{Scope: "repo", Kind: "decision", Content: "second learning"},
		},
	}
}
