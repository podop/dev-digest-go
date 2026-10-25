package postgres_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/workspace/adapters/postgres"
	"dev-digest-go/backend/internal/workspace/domain"
)

// historyEnv is a migrated database and the store under test.
type historyEnv struct {
	t     *testing.T
	db    *gorm.DB
	store *pgstore.Store
}

func newHistoryEnv(t *testing.T) *historyEnv {
	t.Helper()
	d := pgtest.Migrated(t)
	return &historyEnv{t: t, db: d.DB, store: pgstore.NewStore(d.DB)}
}

func (e *historyEnv) exec(query string, args ...any) {
	e.t.Helper()
	if err := e.db.Exec(query, args...).Error; err != nil {
		e.t.Fatalf("exec %.60s: %v", query, err)
	}
}

func (e *historyEnv) scalar(query string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.db.Raw(query, args...).Scan(&out).Error; err != nil {
		e.t.Fatalf("query %.60s: %v", query, err)
	}
	return out
}

func (e *historyEnv) count(query string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.db.Raw(query, args...).Scan(&n).Error; err != nil {
		e.t.Fatalf("count %.60s: %v", query, err)
	}
	return n
}

// historyTenant is one workspace holding every kind of row FR16 deletes and the kept ones around
// them.
type historyTenant struct {
	ws, repo, pr1, pr2, agent, evalCase, findingCase string
	run1, run2, review1, review3, finding1           string
}

// wantDeleted is what resetting a tenant seeded by seedTenant removes.
var wantDeleted = domain.Deleted{
	AgentRuns: 2, RunTraces: 2, Reviews: 3, Findings: 3, MultiAgentRuns: 1, EvalSuiteRuns: 1,
	EvalRuns: 2, CIRuns: 3, ComposedReviews: 1, Learnings: 2, PRsUnmarked: 2,
}

// seedTenant fills a workspace: two agent runs (local and CI) with traces and a run-skill link,
// three reviews (two with a run, one without) with three findings, a multi-agent run, a suite run
// with one eval run plus one suite-less eval run, three CI runs (two by installation, one only by
// its run), a composed review, two learnings, two reviewed PRs, an eval case from a finding and
// the kept configuration (agent, skill, setting, installation, eval case).
func (e *historyEnv) seedTenant(name string) historyTenant {
	e.t.Helper()
	tn := historyTenant{}
	tn.ws = e.scalar(`INSERT INTO workspaces (name) VALUES (?) RETURNING id`, name)
	tn.repo = e.scalar(`INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', 'o/r') RETURNING id`, tn.ws)
	pr := func(n int, sha string) string {
		return e.scalar(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, last_reviewed_sha, status)
			VALUES (?, ?, ?, 't', 'a', 'b', 'main', 'head', ?, 'reviewed') RETURNING id`, tn.ws, tn.repo, n, sha)
	}
	tn.pr1, tn.pr2 = pr(1, "sha1"), pr(2, "sha2")
	tn.agent = e.scalar(`INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, 'a', 'openai', 'm', 'p') RETURNING id`, tn.ws)
	skill := e.scalar(`INSERT INTO skills (workspace_id, name, description, type, source, body) VALUES (?, 's', 'd', 'custom', 'manual', 'b') RETURNING id`, tn.ws)
	e.exec(`INSERT INTO agent_skills (agent_id, skill_id) VALUES (?, ?)`, tn.agent, skill)
	e.exec(`INSERT INTO settings (workspace_id, key, value) VALUES (?, 'workspace_name', '"x"')`, tn.ws)
	inst := e.scalar(`INSERT INTO ci_installations (agent_id, repo, target_type) VALUES (?, 'o/r', 'gha') RETURNING id`, tn.agent)
	tn.evalCase = e.scalar(`INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'agent', ?, 'c') RETURNING id`, tn.ws, tn.agent)

	multi := e.scalar(`INSERT INTO multi_agent_runs (workspace_id, pr_id) VALUES (?, ?) RETURNING id`, tn.ws, tn.pr1)
	tn.run1 = e.scalar(`INSERT INTO agent_runs (workspace_id, agent_id, pr_id, status, source, multi_agent_run_id)
		VALUES (?, ?, ?, 'done', 'local', ?) RETURNING id`, tn.ws, tn.agent, tn.pr1, multi)
	tn.run2 = e.scalar(`INSERT INTO agent_runs (workspace_id, agent_id, pr_id, status, source)
		VALUES (?, ?, ?, 'done', 'ci') RETURNING id`, tn.ws, tn.agent, tn.pr2)
	e.exec(`INSERT INTO run_traces (run_id, trace) VALUES (?, '{}'), (?, '{}')`, tn.run1, tn.run2)
	e.exec(`INSERT INTO agent_run_skills (run_id, skill_id, skill_version, "order") VALUES (?, ?, 1, 0)`, tn.run1, skill)

	review := func(run any) string {
		return e.scalar(`INSERT INTO reviews (workspace_id, pr_id, kind, run_id) VALUES (?, ?, 'review', ?) RETURNING id`, tn.ws, tn.pr1, run)
	}
	tn.review1, _ = review(tn.run1), review(tn.run2)
	tn.review3 = review(nil)
	finding := func(rev string) string {
		return e.scalar(`INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
			VALUES (?, 'f.go', 1, 2, 'WARNING', 'bug', 't', 'r', 0.5) RETURNING id`, rev)
	}
	tn.finding1 = finding(tn.review1)
	finding(tn.review1)
	finding(tn.review3)
	tn.findingCase = e.scalar(`INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, input_diff, input_meta, expected_output,
			source_finding_id, source_review_id, source_pr_id)
		VALUES (?, 'agent', ?, 'from finding', 'diff', '{}', '{"kind":"finding","file":"f.go","start_line":1,"end_line":2}', ?, ?, ?) RETURNING id`, tn.ws, tn.agent, tn.finding1, tn.review1, tn.pr1)

	e.exec(`INSERT INTO ci_runs (ci_installation_id, agent_run_id, status) VALUES (?, ?, 'done'), (?, NULL, 'done')`, inst, tn.run2, inst)
	e.exec(`INSERT INTO ci_runs (ci_installation_id, agent_run_id, status) VALUES (NULL, ?, 'done')`, tn.run1)
	suite := e.scalar(`INSERT INTO eval_suite_runs (workspace_id, agent_id, agent_version, config, case_ids, status)
		VALUES (?, ?, 1, '{}', '[]', 'completed') RETURNING id`, tn.ws, tn.agent)
	e.exec(`INSERT INTO eval_runs (case_id, run_id, status) VALUES (?, ?, 'passed'), (?, NULL, 'failed')`, tn.evalCase, suite, tn.evalCase)
	e.exec(`INSERT INTO composed_reviews (pr_id, body) VALUES (?, 'b')`, tn.pr1)
	e.exec(`INSERT INTO memory (workspace_id, repo_id, scope, kind, content) VALUES (?, ?, 'repo', 'fact', 'c1'), (?, NULL, 'global', 'fact', 'c2')`, tn.ws, tn.repo, tn.ws)
	return tn
}

// tableCounts counts the rows of every table of the public schema.
func (e *historyEnv) tableCounts() map[string]int64 {
	e.t.Helper()
	var names []string
	if err := e.db.Raw(`SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&names).Error; err != nil {
		e.t.Fatalf("list tables: %v", err)
	}
	out := map[string]int64{}
	for _, n := range names {
		out[n] = e.count(`SELECT count(*) FROM "` + n + `"`)
	}
	return out
}

// historyKinds are the tables a reset empties for the workspace (FR16).
var historyKinds = []string{
	"agent_runs", "run_traces", "agent_run_skills", "reviews", "findings", "multi_agent_runs",
	"eval_suite_runs", "eval_runs", "ci_runs", "composed_reviews", "memory",
}

// Reset must delete every FR16 kind of the workspace and nothing else (AC16, FR16, EC7).
func TestReset_DeletesEveryKindAndKeepsTheRest(t *testing.T) {
	e := newHistoryEnv(t)
	a, b := e.seedTenant("a"), e.seedTenant("b")
	before := e.tableCounts()

	got, active, err := e.store.Reset(t.Context(), a.ws)
	if err != nil || active != 0 {
		t.Fatalf("Reset(a) = %+v, active %d, err %v; want no error and no active run", got, active, err)
	}
	if diff := cmp.Diff(wantDeleted, got); diff != "" {
		t.Errorf("Reset(a) deleted mismatch (-want +got):\n%s", diff)
	}

	after := e.tableCounts()
	removed := map[string]int64{ // rows of tenant a, per table
		"agent_runs": 2, "run_traces": 2, "agent_run_skills": 1, "reviews": 3, "findings": 3, "multi_agent_runs": 1,
		"eval_suite_runs": 1, "eval_runs": 2, "ci_runs": 3, "composed_reviews": 1, "memory": 2,
	}
	for table, n := range before {
		if want := n - removed[table]; after[table] != want {
			t.Errorf("table %s has %d rows after Reset(a), want %d (kept tables unchanged)", table, after[table], want)
		}
	}
	for _, table := range historyKinds {
		if n := e.count(`SELECT count(*) FROM `+table) - e.count(historyTenantCount(table), sql.Named("ws", b.ws)); n != 0 {
			t.Errorf("table %s holds %d rows that are not tenant b's after Reset(a), want 0", table, n)
		}
	}
	if n := e.count(`SELECT count(*) FROM pull_requests WHERE workspace_id = ? AND last_reviewed_sha IS NOT NULL`, a.ws); n != 0 {
		t.Errorf("workspace a has %d PRs with last_reviewed_sha, want 0", n)
	}
	if n := e.count(`SELECT count(*) FROM pull_requests WHERE workspace_id = ? AND last_reviewed_sha IS NOT NULL`, b.ws); n != 2 {
		t.Errorf("workspace b has %d PRs with last_reviewed_sha, want 2 (untouched)", n)
	}

	again, active, err := e.store.Reset(t.Context(), a.ws)
	if err != nil || active != 0 {
		t.Fatalf("second Reset(a) = %+v, active %d, err %v", again, active, err)
	}
	if diff := cmp.Diff(domain.Deleted{}, again); diff != "" {
		t.Errorf("second Reset(a) mismatch (-want +got):\n%s", diff)
	}
}

// historyTenantCount counts the rows of a deleted kind that belong to one workspace.
func historyTenantCount(table string) string {
	switch table {
	case "agent_runs", "reviews", "multi_agent_runs", "eval_suite_runs", "memory":
		return `SELECT count(*) FROM ` + table + ` WHERE workspace_id = @ws`
	case "run_traces", "agent_run_skills":
		return `SELECT count(*) FROM ` + table + ` WHERE run_id IN (SELECT id FROM agent_runs WHERE workspace_id = @ws)`
	case "findings":
		return `SELECT count(*) FROM findings WHERE review_id IN (SELECT id FROM reviews WHERE workspace_id = @ws)`
	case "eval_runs":
		return `SELECT count(*) FROM eval_runs WHERE case_id IN (SELECT id FROM eval_cases WHERE workspace_id = @ws)`
	case "ci_runs":
		return `SELECT count(*) FROM ci_runs WHERE ci_installation_id IN
			(SELECT i.id FROM ci_installations i JOIN agents a ON a.id = i.agent_id WHERE a.workspace_id = @ws)
			OR agent_run_id IN (SELECT id FROM agent_runs WHERE workspace_id = @ws)`
	default: // composed_reviews
		return `SELECT count(*) FROM composed_reviews WHERE pr_id IN (SELECT id FROM pull_requests WHERE workspace_id = @ws)`
	}
}

// An eval case made from a finding survives Reset with its source ids and the columns the evals
// store reads (EC6, AC16). Read with plain SQL: adapters do not import each other; the HTTP read
// path is proven in cmd/server (TestWorkspaceHistory_ResetDeletesHistoryAndKeepsTheRest).
func TestReset_CaseBuiltFromFindingStillReads(t *testing.T) {
	e := newHistoryEnv(t)
	a := e.seedTenant("a")

	if _, _, err := e.store.Reset(t.Context(), a.ws); err != nil {
		t.Fatalf("Reset(a): %v", err)
	}
	if n := e.count(`SELECT count(*) FROM findings WHERE id = ?`, a.finding1); n != 0 {
		t.Fatalf("finding %s still exists after Reset", a.finding1)
	}
	type caseRow struct {
		Name            string
		InputDiff       string
		SourceFindingID *string
		SourceReviewID  *string
		SourcePRID      *string
		ExpectedOutput  string
	}
	var got []caseRow
	err := e.db.Raw(`SELECT name, input_diff, source_finding_id, source_review_id, source_pr_id, expected_output::text AS expected_output
		FROM eval_cases WHERE id = ? AND workspace_id = ?`, a.findingCase, a.ws).Scan(&got).Error
	if err != nil {
		t.Fatalf("read case %s after Reset: %v", a.findingCase, err)
	}
	if len(got) != 1 {
		t.Fatalf("eval case %s rows after Reset = %d, want 1", a.findingCase, len(got))
	}
	c := got[0]
	if c.Name != "from finding" || c.InputDiff != "diff" {
		t.Errorf("case %s = name %q, diff %q; want %q and %q", a.findingCase, c.Name, c.InputDiff, "from finding", "diff")
	}
	if c.SourceFindingID == nil || *c.SourceFindingID != a.finding1 {
		t.Errorf("case %s source_finding_id = %v, want %s", a.findingCase, c.SourceFindingID, a.finding1)
	}
	if c.SourceReviewID == nil || c.SourcePRID == nil {
		t.Errorf("case %s source review %v / PR %v, want both kept", a.findingCase, c.SourceReviewID, c.SourcePRID)
	}
	if !strings.Contains(c.ExpectedOutput, `"kind"`) || !strings.Contains(c.ExpectedOutput, "f.go") {
		t.Errorf("case %s expected_output = %s, want the finding expectation intact", a.findingCase, c.ExpectedOutput)
	}
	if n := e.count(`SELECT count(*) FROM eval_cases WHERE workspace_id = ? AND source_finding_id = ?`, a.ws, a.finding1); n != 1 {
		t.Errorf("cases with source_finding_id %s = %d, want 1", a.finding1, n)
	}
}

// A running agent run or eval suite run makes Reset refuse and delete nothing (FR17, AC17).
func TestReset_RunningRunRefuses(t *testing.T) {
	cases := []struct {
		name   string
		insert string
	}{
		{"agent run", `INSERT INTO agent_runs (workspace_id, status) VALUES (?, 'running')`},
		{"eval suite run", `INSERT INTO eval_suite_runs (workspace_id, agent_id, agent_version, config, case_ids, status)
			SELECT workspace_id, id, 1, '{}', '[]', 'running' FROM agents WHERE workspace_id = ?`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHistoryEnv(t)
			a := e.seedTenant("a")
			e.exec(tc.insert, a.ws)
			before := e.tableCounts()

			got, active, err := e.store.Reset(t.Context(), a.ws)
			if err != nil {
				t.Fatalf("Reset(a) with a running %s: %v", tc.name, err)
			}
			if active != 1 {
				t.Errorf("Reset(a) active = %d, want 1", active)
			}
			if diff := cmp.Diff(domain.Deleted{}, got); diff != "" {
				t.Errorf("Reset(a) deleted mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(before, e.tableCounts()); diff != "" {
				t.Errorf("row counts changed by a refused Reset (-before +after):\n%s", diff)
			}
		})
	}
}

// A running run of another workspace does not block this one.
func TestReset_RunningRunOfAnotherWorkspaceIsIgnored(t *testing.T) {
	e := newHistoryEnv(t)
	a, b := e.seedTenant("a"), e.seedTenant("b")
	e.exec(`INSERT INTO agent_runs (workspace_id, status) VALUES (?, 'running')`, b.ws)

	_, active, err := e.store.Reset(t.Context(), a.ws)
	if err != nil || active != 0 {
		t.Fatalf("Reset(a) = active %d, err %v; want it to run", active, err)
	}
}

// freeze installs a statement trigger on memory deletes that waits for an advisory lock; the
// returned release function frees it. While the test holds the lock, a Reset sits inside its
// transaction after the table lock and the run check, which is the barrier of EC5 and EC11.
func (e *historyEnv) freeze() (release func()) {
	e.t.Helper()
	e.exec(`CREATE FUNCTION hist_freeze() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(hashtextextended('hist-freeze', 0)); RETURN NULL; END $$`)
	e.exec(`CREATE TRIGGER hist_freeze AFTER DELETE ON memory FOR EACH STATEMENT EXECUTE FUNCTION hist_freeze()`)
	hold := e.db.Begin()
	if hold.Error != nil {
		e.t.Fatalf("begin hold: %v", hold.Error)
	}
	if err := hold.Exec(`SELECT pg_advisory_xact_lock(hashtextextended('hist-freeze', 0))`).Error; err != nil {
		e.t.Fatalf("hold freeze lock: %v", err)
	}
	var once sync.Once
	release = func() { once.Do(func() { _ = hold.Rollback().Error }) }
	e.t.Cleanup(release)
	return release
}

// waitUntil polls a pg_locks condition until it holds: the barrier replaces sleeps.
func (e *historyEnv) waitUntil(what, query string, want int64) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for e.count(query) < want {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const (
	ungrantedAdvisory = `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`
	ungrantedRunsLock = `SELECT count(*) FROM pg_locks WHERE locktype = 'relation' AND NOT granted
		AND relation = 'agent_runs'::regclass`
)

type resetResult struct {
	deleted domain.Deleted
	active  int
	err     error
}

func (e *historyEnv) resetAsync(ctx context.Context, ws string) <-chan resetResult {
	ch := make(chan resetResult, 1)
	go func() {
		d, active, err := e.store.Reset(ctx, ws)
		ch <- resetResult{d, active, err}
	}()
	return ch
}

// Barrier 1 (EC5, AC18): a review run is inserted but not yet committed when Reset starts. Reset
// waits for it, then sees it running and answers active, deleting nothing.
func TestReset_WaitsForUncommittedRunThenRefuses(t *testing.T) {
	e := newHistoryEnv(t)
	a := e.seedTenant("a")
	before := e.tableCounts()

	writer := e.db.Begin()
	if writer.Error != nil {
		t.Fatalf("begin: %v", writer.Error)
	}
	var runID string
	if err := writer.Raw(`INSERT INTO agent_runs (workspace_id, pr_id, status) VALUES (?, ?, 'running') RETURNING id`, a.ws, a.pr1).Scan(&runID).Error; err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := writer.Exec(`INSERT INTO reviews (workspace_id, pr_id, kind, run_id) VALUES (?, ?, 'review', ?)`, a.ws, a.pr1, runID).Error; err != nil {
		t.Fatalf("insert review: %v", err)
	}

	res := e.resetAsync(t.Context(), a.ws)
	e.waitUntil("Reset to wait for the table lock", ungrantedRunsLock, 1)
	if err := writer.Commit().Error; err != nil {
		t.Fatalf("commit run: %v", err)
	}

	got := <-res
	if got.err != nil || got.active != 1 {
		t.Fatalf("Reset(a) = %+v; want active 1 and no error", got)
	}
	if diff := cmp.Diff(domain.Deleted{}, got.deleted); diff != "" {
		t.Errorf("Reset(a) deleted mismatch (-want +got):\n%s", diff)
	}
	want := before
	want["agent_runs"]++
	want["reviews"]++
	if diff := cmp.Diff(want, e.tableCounts()); diff != "" {
		t.Errorf("row counts after the refused Reset (-want +got):\n%s", diff)
	}
}

// Barrier 2 (EC5, AC18): a run is started while Reset is deleting. The insert waits for the
// table lock; once Reset commits, the run and its review exist whole and were not counted.
func TestReset_RunStartedDuringResetIsKeptWhole(t *testing.T) {
	e := newHistoryEnv(t)
	a := e.seedTenant("a")
	release := e.freeze()

	res := e.resetAsync(t.Context(), a.ws)
	e.waitUntil("Reset to reach the freeze", ungrantedAdvisory, 1)

	type inserted struct {
		runID string
		err   error
	}
	ins := make(chan inserted, 1)
	go func() {
		var id string
		err := e.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Raw(`INSERT INTO agent_runs (workspace_id, pr_id, status) VALUES (?, ?, 'running') RETURNING id`, a.ws, a.pr1).Scan(&id).Error; err != nil {
				return err
			}
			return tx.Exec(`INSERT INTO reviews (workspace_id, pr_id, kind, run_id) VALUES (?, ?, 'review', ?)`, a.ws, a.pr1, id).Error
		})
		ins <- inserted{id, err}
	}()
	e.waitUntil("the run start to wait for Reset", ungrantedRunsLock, 1)
	release()

	got := <-res
	if got.err != nil || got.active != 0 {
		t.Fatalf("Reset(a) = %+v; want it to delete", got)
	}
	if diff := cmp.Diff(wantDeleted, got.deleted); diff != "" {
		t.Errorf("Reset(a) deleted mismatch (-want +got):\n%s", diff)
	}
	in := <-ins
	if in.err != nil {
		t.Fatalf("run start after Reset: %v", in.err)
	}
	if n := e.count(`SELECT count(*) FROM agent_runs WHERE id = ? AND status = 'running'`, in.runID); n != 1 {
		t.Errorf("run %s rows = %d, want 1 (created after the reset)", in.runID, n)
	}
	if n := e.count(`SELECT count(*) FROM reviews WHERE run_id = ?`, in.runID); n != 1 {
		t.Errorf("review rows of run %s = %d, want 1", in.runID, n)
	}
}

// A prune and a reset at the same time both finish without error (EC11, AC18). The prune waits for
// the advisory lock of the frozen reset and runs after it.
func TestPruneTraces_ConcurrentWithResetBothFinish(t *testing.T) {
	e := newHistoryEnv(t)
	a := e.seedTenant("a")
	release := e.freeze()

	res := e.resetAsync(t.Context(), a.ws)
	e.waitUntil("Reset to reach the freeze", ungrantedAdvisory, 1)

	type pruned struct {
		n   int64
		err error
	}
	pr := make(chan pruned, 1)
	go func() {
		n, err := e.store.PruneTraces(t.Context(), a.ws, time.Now().Add(time.Hour))
		pr <- pruned{n, err}
	}()
	e.waitUntil("the prune to wait for the history lock", ungrantedAdvisory, 2)
	release()

	if got := <-res; got.err != nil || got.active != 0 || got.deleted.RunTraces != 2 {
		t.Errorf("Reset(a) = %+v; want 2 traces deleted and no error", got)
	}
	if got := <-pr; got.err != nil || got.n != 0 {
		t.Errorf("PruneTraces after the reset = %d, %v; want 0 and no error (the reset took the traces)", got.n, got.err)
	}
}

// A prune batch waits for the history lock longer than a reset can hold it (EC11, NFR3): the lock
// is held for 12 s, past the 10 s a reset waits, and the prune still finishes without error once
// it is released.
func TestPruneTraces_WaitsOutALockHeldPastTheResetTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("holds the history lock for 12 s")
	}
	e := newHistoryEnv(t)
	a := e.seedTenant("a")

	hold := e.db.Begin()
	if hold.Error != nil {
		t.Fatalf("begin hold: %v", hold.Error)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = hold.Rollback().Error }) }
	t.Cleanup(release)
	if err := hold.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, "workspace-history:"+a.ws).Error; err != nil {
		t.Fatalf("hold history lock: %v", err)
	}

	type pruned struct {
		n   int64
		err error
	}
	pr := make(chan pruned, 1)
	go func() {
		n, err := e.store.PruneTraces(t.Context(), a.ws, time.Now().Add(time.Hour))
		pr <- pruned{n, err}
	}()
	e.waitUntil("the prune to wait for the history lock", ungrantedAdvisory, 1)

	select {
	case got := <-pr:
		t.Fatalf("PruneTraces returned %d, %v while the lock was held; want it to wait", got.n, got.err)
	case <-time.After(12 * time.Second):
	}
	release()

	got := <-pr
	if got.err != nil || got.n != 2 {
		t.Errorf("PruneTraces after the lock was released = %d, %v; want 2 traces and no error", got.n, got.err)
	}
}

// A lock the reset cannot get within 10 s is an error and deletes nothing (A4).
func TestReset_LockTimeoutDeletesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 10 s lock timeout")
	}
	e := newHistoryEnv(t)
	a := e.seedTenant("a")
	before := e.tableCounts()

	hold := e.db.Begin()
	if err := hold.Exec(`LOCK TABLE agent_runs IN ROW EXCLUSIVE MODE`).Error; err != nil {
		t.Fatalf("hold table lock: %v", err)
	}
	defer func() { _ = hold.Rollback().Error }()

	start := time.Now()
	_, _, err := e.store.Reset(t.Context(), a.ws)
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("Reset(a) with the table held = %v; want a lock timeout error", err)
	}
	if took := time.Since(start); took < 9*time.Second || took > 20*time.Second {
		t.Errorf("Reset(a) gave up after %v, want about 10s", took)
	}
	if diff := cmp.Diff(before, e.tableCounts()); diff != "" {
		t.Errorf("row counts changed by a timed-out Reset (-before +after):\n%s", diff)
	}
}
