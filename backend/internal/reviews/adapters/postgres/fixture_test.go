package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/adapters/postgres"
	"dev-digest-go/backend/internal/reviews/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// fixture is a migrated database with two workspaces, a repo and a pull request (#482) of the
// first one.
type fixture struct {
	db    *gorm.DB
	store *postgres.Store
	tx    *pg.Tx
	ws    string
	other string
	repo  string
	pr    string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB), tx: pg.NewTx(d.DB)}
	f.ws = f.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = f.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.repo = f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', 'payments-api', 'acme/payments-api') RETURNING id`, f.ws)
	f.pr = f.pull(t, f.ws, f.repo, 482)
	return f
}

// insert runs a statement that ends in RETURNING id and returns the id.
func (f fixture) insert(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := f.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (f fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := f.db.Exec(query, args...).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return n
}

// str reads one nullable text value; "<nil>" stands for NULL.
func (f fixture) str(t *testing.T, query string, args ...any) string {
	t.Helper()
	var v *string
	if err := f.db.Raw(query, args...).Scan(&v).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	if v == nil {
		return "<nil>"
	}
	return *v
}

func (f fixture) pull(t *testing.T, ws, repo string, number int) string {
	t.Helper()
	return f.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, ?, 'Add rate limiting', 'dev', 'feat', 'main', 'a1b2c3d4e5f6') RETURNING id`, ws, repo, number)
}

func (f fixture) agent(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return f.insert(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled)
		VALUES (?, ?, 'openai', 'gpt', 'be careful', ?) RETURNING id`, ws, name, enabled)
}

func (f fixture) skill(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return f.insert(t, `INSERT INTO skills (workspace_id, name, description, type, source, body, enabled, version)
		VALUES (?, ?, 'd', 'custom', 'manual', 'body', ?, 3) RETURNING id`, ws, name, enabled)
}

// run creates a run through the store.
func (f fixture) run(t *testing.T, agent string, multi *string) string {
	t.Helper()
	id, err := f.store.CreateRun(t.Context(), domain.NewRun{
		WorkspaceID: f.ws, AgentID: agent, PRID: f.pr, Provider: "openai", Model: "gpt", MultiAgentRunID: multi,
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return id
}

func intp(v int) *int           { return &v }
func floatp(v float64) *float64 { return &v }
func strp(v string) *string     { return &v }
func completion() domain.RunCompletion {
	return domain.RunCompletion{
		DurationMS: 1500, TokensIn: 100, TokensOut: 20, CostUSD: floatp(0.002135), FindingsCount: 2,
		Grounding: "2/2 passed", Score: intp(61), Blockers: intp(1),
	}
}

func finding(file string, start, end int, title string) reviewer.Finding {
	return reviewer.Finding{
		ID: "model-id", Severity: reviewer.SeverityWarning, Category: reviewer.CategoryBug, Title: title,
		File: file, StartLine: start, EndLine: end, Rationale: "because " + title, Confidence: 0.9,
	}
}

// doneReview writes a finished run with its review and findings in one transaction, as the
// executor persists it, and returns the run and review ids.
func (f fixture) doneReview(t *testing.T, agent string, multi *string, findings ...reviewer.Finding) (runID, reviewID string) {
	t.Helper()
	runID = f.run(t, agent, multi)
	err := f.tx.InTx(t.Context(), func(ctx context.Context) error {
		id, err := f.store.InsertReview(ctx, domain.NewReview{
			WorkspaceID: f.ws, PRID: f.pr, AgentID: agent, RunID: runID, Verdict: reviewer.VerdictComment,
			Summary: "summary", Score: 70, Model: "gpt",
		})
		if err != nil {
			return err
		}
		reviewID = id
		if err := f.store.InsertFindings(ctx, id, findings, nil); err != nil {
			return err
		}
		ok, err := f.store.CompleteRunIfRunning(ctx, runID, completion())
		if err != nil || !ok {
			t.Fatalf("CompleteRunIfRunning() = %v, %v", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	return runID, reviewID
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// sleepBriefly paces a poll of pg_locks; the polled condition is what ends the loop.
func sleepBriefly() { time.Sleep(5 * time.Millisecond) }
