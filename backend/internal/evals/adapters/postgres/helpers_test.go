package postgres_test

import (
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	pgstore "dev-digest-go/backend/internal/evals/adapters/postgres"
	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// env is a migrated database with one workspace.
type env struct {
	db    *gorm.DB
	store *pgstore.Store
	ws    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	e := &env{db: d.DB, store: pgstore.NewStore(d.DB)}
	e.ws = e.workspace(t)
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

func (e *env) workspace(t *testing.T) string {
	t.Helper()
	return e.scalar(t, `INSERT INTO workspaces (name) VALUES ('w') RETURNING id`)
}

func (e *env) agent(t *testing.T, ws, name string) string {
	t.Helper()
	return e.scalar(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, strategy, version)
		VALUES (?, ?, 'openai', 'gpt-x', 'prompt of '||?::text, 'auto', 3) RETURNING id`, ws, name, name)
}

func (e *env) skill(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return e.scalar(t, `INSERT INTO skills (workspace_id, name, description, type, source, body, enabled, version)
		VALUES (?, ?, 'desc '||?::text, 'custom', 'manual', 'body of '||?::text, ?, 2) RETURNING id`, ws, name, name, name, enabled)
}

func (e *env) link(t *testing.T, agent, skill string, order int) {
	t.Helper()
	e.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, ?)`, agent, skill, order)
}

// finding inserts repo, PR, review and one finding; patch "" leaves the file without a pr_files row.
func (e *env) finding(t *testing.T, ws, agent string, file, patch string) (findingID, reviewID, prID string) {
	t.Helper()
	repo := e.scalar(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', 'o/r'||substr(gen_random_uuid()::text,1,6)) RETURNING id`, ws)
	prID = e.scalar(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, body)
		VALUES (?, ?, 7, 'PR title', 'me', 'b', 'main', 'abc123', 'PR body') RETURNING id`, ws, repo)
	if patch != "" {
		e.exec(t, `INSERT INTO pr_files (pr_id, path, patch) VALUES (?, ?, ?)`, prID, file, patch)
	}
	var ag any
	if agent != "" {
		ag = agent
	}
	reviewID = e.scalar(t, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind) VALUES (?, ?, ?, 'review') RETURNING id`, ws, prID, ag)
	findingID = e.scalar(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
		VALUES (?, ?, 3, 5, 'CRITICAL', 'security', 'T', 'R', 0.9) RETURNING id`, reviewID, file)
	return findingID, reviewID, prID
}

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }
func fp(f float64) *float64 {
	return &f
}

// manualCase is a valid manual case of the owner.
func manualCase(ws string, kind domain.OwnerKind, owner, name string) domain.NewCase {
	return domain.NewCase{
		WorkspaceID: ws, OwnerKind: kind, OwnerID: owner, Name: name,
		Expectation: domain.Expectation{
			Kind: domain.MustFind, File: "a.go", StartLine: 1, EndLine: 2,
			Severity: sp("CRITICAL"), Category: sp("bug"), Title: sp("T"),
		},
		InputDiff: "diff --git a/a.go b/a.go\n+x\n",
		InputMeta: domain.InputMeta{PRTitle: "pr", PRDescription: sp("d")},
	}
}

func (e *env) newCase(t *testing.T, ws string, kind domain.OwnerKind, owner, name string) domain.Case {
	t.Helper()
	c, err := e.store.CreateCase(t.Context(), manualCase(ws, kind, owner, name))
	if err != nil {
		t.Fatalf("CreateCase(%q): %v", name, err)
	}
	return c
}

// newRun creates a running agent run over the cases.
func (e *env) newRun(t *testing.T, ws, agent string, cases ...string) domain.Run {
	t.Helper()
	r, err := e.store.CreateRun(t.Context(), domain.NewRun{
		WorkspaceID: ws, OwnerKind: domain.OwnerAgent, AgentID: agent, AgentVersion: 3, AgentName: "frozen",
		Config:  domain.Config{Provider: "openai", Model: "m", SystemPrompt: "p", Strategy: "auto"},
		CaseIDs: cases,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return r
}

func (e *env) finish(t *testing.T, r domain.Run, status domain.RunStatus) {
	t.Helper()
	ok, err := e.store.FinishRun(t.Context(), r.WorkspaceID, r.ID, domain.RunFinish{
		Status: status, FinishedAt: time.Now(), DurationMS: 10,
	})
	if err != nil || !ok {
		t.Fatalf("FinishRun = %v, %v; want true, nil", ok, err)
	}
}
