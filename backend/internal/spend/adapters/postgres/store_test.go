package postgres_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/spend/adapters/postgres"
	"dev-digest-go/backend/internal/spend/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

var (
	from = time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	to   = time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	mid  = time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
)

// ledger is a migrated database with the store under test and seeding helpers.
type ledger struct {
	t     *testing.T
	db    *gorm.DB
	store *pgstore.Store
}

func newLedger(t *testing.T) *ledger {
	t.Helper()
	d := pgtest.Migrated(t)
	return &ledger{t: t, db: d.DB, store: pgstore.NewStore(d.DB)}
}

// scalar runs an INSERT ... RETURNING id (or any single-value statement).
func (l *ledger) scalar(query string, args ...any) string {
	l.t.Helper()
	var out string
	if err := l.db.Raw(query, args...).Scan(&out).Error; err != nil {
		l.t.Fatalf("%s: %v", strings.Fields(query)[2], err)
	}
	return out
}

func (l *ledger) exec(query string, args ...any) {
	l.t.Helper()
	if err := l.db.Exec(query, args...).Error; err != nil {
		l.t.Fatalf("exec %.40s: %v", query, err)
	}
}

// tenant is one workspace with a repo, a pull request and an eval case: the anchors the
// cost records hang on.
type tenant struct {
	l                      *ledger
	ws, repo, pr, evalCase string
	n                      int
}

func (l *ledger) newTenant(name string) *tenant {
	l.t.Helper()
	tn := &tenant{l: l}
	tn.ws = l.scalar(`INSERT INTO workspaces (name) VALUES (?) RETURNING id`, name)
	tn.repo = l.scalar(`INSERT INTO repos (workspace_id, owner, name, full_name)
		VALUES (?, 'o', 'r', 'o/r') RETURNING id`, tn.ws)
	tn.pr = tn.newPR()
	tn.evalCase = l.scalar(`INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name)
		VALUES (?, 'agent', gen_random_uuid(), 'c') RETURNING id`, tn.ws)
	return tn
}

// newPR inserts another pull request (pr_intent and pr_brief are one row per PR).
func (tn *tenant) newPR() string {
	tn.n++
	return tn.l.scalar(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, ?, 't', 'a', 'b', 'main', 'sha') RETURNING id`, tn.ws, tn.repo, tn.n)
}

func (tn *tenant) agentRun(cost any, at time.Time) {
	tn.l.exec(`INSERT INTO agent_runs (workspace_id, ran_at, cost_usd) VALUES (?, ?, ?)`, tn.ws, at, cost)
}

func (tn *tenant) evalRun(cost any, at time.Time) {
	tn.l.exec(`INSERT INTO eval_runs (case_id, ran_at, cost_usd) VALUES (?, ?, ?)`, tn.evalCase, at, cost)
}

func (tn *tenant) conventionScan(cost any, at time.Time) {
	tn.l.exec(`INSERT INTO convention_scans (workspace_id, repo_id, status, started_at, cost_usd) VALUES (?, ?, 'done', ?, ?)`,
		tn.ws, tn.repo, at, cost)
}

func (tn *tenant) intent(cost any, at time.Time) {
	tn.l.exec(`INSERT INTO pr_intent (pr_id, intent, cost_usd, derived_at) VALUES (?, 'i', ?, ?)`, tn.newPR(), cost, at)
}

// brief stores a pr_brief document with the given raw jsonb text.
func (tn *tenant) brief(doc string) {
	tn.l.exec(`INSERT INTO pr_brief (pr_id, json) VALUES (?, ?::jsonb)`, tn.newPR(), doc)
}

func (tn *tenant) briefAt(cost float64, at time.Time) {
	tn.brief(fmt.Sprintf(`{"generated_at": %q, "cost_usd": %v}`, at.Format("2006-01-02T15:04:05.000Z07:00"), cost))
}

// onboarding stores a tour of a fresh repo of the tenant; doc is the raw jsonb text.
func (tn *tenant) onboarding(doc string, at time.Time) {
	tn.n++
	repo := tn.l.scalar(`INSERT INTO repos (workspace_id, owner, name, full_name)
		VALUES (?, 'o', ?, ?) RETURNING id`, tn.ws, fmt.Sprint("r", tn.n), fmt.Sprint("o/r", tn.n))
	tn.l.exec(`INSERT INTO onboarding (repo_id, json, generated_at) VALUES (?, ?::jsonb, ?)`, repo, doc, at)
}

func (tn *tenant) onboardingAt(cost float64, at time.Time) {
	tn.onboarding(fmt.Sprintf(`{"cost_usd": %v}`, cost), at)
}

// mirrors adds records that mirror cost already counted elsewhere; they must never count.
func (tn *tenant) mirrors(at time.Time) {
	agent := tn.l.scalar(`INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
		VALUES (?, 'a', 'openai', 'm', 'p') RETURNING id`, tn.ws)
	suite := tn.l.scalar(`INSERT INTO eval_suite_runs (workspace_id, agent_id, agent_version, config, case_ids, status, started_at, cost_usd)
		VALUES (?, ?, 1, '{}', '[]', 'completed', ?, 1000) RETURNING id`, tn.ws, agent, at)
	tn.l.exec(`INSERT INTO eval_runs (case_id, ran_at, cost_usd, run_id) VALUES (?, ?, 0, ?)`, tn.evalCase, at, suite)
	tn.l.exec(`INSERT INTO ci_runs (ran_at, cost_usd, source) VALUES (?, 2000, 'ci')`, at)
}

func (l *ledger) spend(ws string, from, to time.Time) domain.Micros {
	l.t.Helper()
	got, err := l.store.MonthSpend(context.Background(), ws, from, to)
	if err != nil {
		l.t.Fatalf("MonthSpend(%s, %v, %v) error: %v", ws, from, to, err)
	}
	return got
}

// seedAll adds one record of each of the six sources at time at; the costs are distinct powers
// of two (in cents) so a wrong subset shows in the total.
func (tn *tenant) seedAll(at time.Time) {
	tn.agentRun(0.01, at)
	tn.evalRun(0.02, at)
	tn.conventionScan(0.04, at)
	tn.intent(0.08, at)
	tn.briefAt(0.16, at)
	tn.onboardingAt(0.32, at)
}

const allSixMicros = domain.Micros(630_000)

func TestMonthSpend_countsAllSixSources(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	tn.seedAll(mid)

	if got := l.spend(tn.ws, from, to); got != allSixMicros {
		t.Errorf("MonthSpend = %d, want %d (sum of the six sources)", got, allSixMicros)
	}
}

func TestMonthSpend_eachSourceAlone(t *testing.T) {
	tests := []struct {
		name string
		seed func(tn *tenant)
		want domain.Micros
	}{
		{"agent_runs", func(tn *tenant) { tn.agentRun(0.01, mid) }, 10_000},
		{"eval_runs", func(tn *tenant) { tn.evalRun(0.02, mid) }, 20_000},
		{"convention_scans", func(tn *tenant) { tn.conventionScan(0.04, mid) }, 40_000},
		{"pr_intent", func(tn *tenant) { tn.intent(0.08, mid) }, 80_000},
		{"pr_brief", func(tn *tenant) { tn.briefAt(0.16, mid) }, 160_000},
		{"onboarding", func(tn *tenant) { tn.onboardingAt(0.32, mid) }, 320_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newLedger(t)
			tn := l.newTenant("a")
			tc.seed(tn)
			if got := l.spend(tn.ws, from, to); got != tc.want {
				t.Errorf("MonthSpend = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMonthSpend_mirrorsExcluded(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	tn.mirrors(mid)
	if got := l.spend(tn.ws, from, to); got != 0 {
		t.Fatalf("MonthSpend with only mirror records = %d, want 0", got)
	}

	tn.seedAll(mid)
	if got := l.spend(tn.ws, from, to); got != allSixMicros {
		t.Errorf("MonthSpend with mirrors next to the six sources = %d, want %d", got, allSixMicros)
	}
}

func TestMonthSpend_workspacesIsolated(t *testing.T) {
	l := newLedger(t)
	a, b := l.newTenant("a"), l.newTenant("b")
	a.seedAll(mid)
	b.agentRun(5, mid)
	b.briefAt(7, mid)
	b.onboardingAt(11, mid)

	if got := l.spend(a.ws, from, to); got != allSixMicros {
		t.Errorf("MonthSpend(a) = %d, want %d", got, allSixMicros)
	}
	if got, want := l.spend(b.ws, from, to), domain.Micros(23_000_000); got != want {
		t.Errorf("MonthSpend(b) = %d, want %d", got, want)
	}
	if got := l.spend("00000000-0000-0000-0000-000000000000", from, to); got != 0 {
		t.Errorf("MonthSpend(unknown workspace) = %d, want 0", got)
	}
}

func TestMonthSpend_monthBoundaries(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	us := time.Microsecond

	tn.seedAll(from.Add(-us)) // just before the month: out
	tn.seedAll(to)            // first instant of the next month: out
	if got := l.spend(tn.ws, from, to); got != 0 {
		t.Fatalf("MonthSpend with records outside [from, to) = %d, want 0", got)
	}

	tn.seedAll(from)        // first instant: in
	tn.seedAll(to.Add(-us)) // last microsecond: in
	if got, want := l.spend(tn.ws, from, to), 2*allSixMicros; got != want {
		t.Errorf("MonthSpend with records on the boundaries = %d, want %d (from inclusive, to exclusive)", got, want)
	}
}

func TestMonthSpend_nextMonthIsDisjoint(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	tn.seedAll(to.Add(-time.Microsecond))
	tn.seedAll(to)

	if got := l.spend(tn.ws, from, to); got != allSixMicros {
		t.Errorf("MonthSpend(October) = %d, want %d", got, allSixMicros)
	}
	if got := l.spend(tn.ws, to, to.AddDate(0, 1, 0)); got != allSixMicros {
		t.Errorf("MonthSpend(November) = %d, want %d", got, allSixMicros)
	}
}

func TestMonthSpend_nullCostCountsAsZero(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	tn.agentRun(nil, mid)
	tn.evalRun(nil, mid)
	tn.conventionScan(nil, mid)
	tn.intent(nil, mid)
	tn.brief(fmt.Sprintf(`{"generated_at": %q, "cost_usd": null}`, mid.Format(time.RFC3339)))
	tn.onboarding(`{"cost_usd": null}`, mid)
	if got := l.spend(tn.ws, from, to); got != 0 {
		t.Errorf("MonthSpend with NULL costs = %d, want 0", got)
	}

	tn.agentRun(0.5, mid)
	if got, want := l.spend(tn.ws, from, to), domain.Micros(500_000); got != want {
		t.Errorf("MonthSpend with a NULL and a real cost = %d, want %d", got, want)
	}
}

func TestMonthSpend_malformedJSONDocsSkipped(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	okTime := mid.Format(time.RFC3339)

	tn.briefAt(0.16, mid) // the only good brief
	tn.brief(`{"generated_at": "not a date", "cost_usd": 100}`)
	tn.brief(`{"generated_at": "2026-02-31T00:00:00Z", "cost_usd": 100}`)
	tn.brief(`{"generated_at": 20261015, "cost_usd": 100}`)
	tn.brief(`{"cost_usd": 100}`)
	tn.brief(fmt.Sprintf(`{"generated_at": %q, "cost_usd": "abc"}`, okTime))
	tn.brief(fmt.Sprintf(`{"generated_at": %q, "cost_usd": {"a": 1}}`, okTime))
	tn.brief(fmt.Sprintf(`{"generated_at": %q}`, okTime))
	tn.brief(`[]`)
	tn.brief(`"text"`)
	tn.brief(`null`)

	tn.onboardingAt(0.32, mid) // the only good tour
	tn.onboarding(`{"cost_usd": "abc"}`, mid)
	tn.onboarding(`{"cost_usd": [1]}`, mid)
	tn.onboarding(`{}`, mid)
	tn.onboarding(`[]`, mid)
	tn.onboarding(`"text"`, mid)

	if got, want := l.spend(tn.ws, from, to), domain.Micros(480_000); got != want {
		t.Errorf("MonthSpend with malformed documents = %d, want %d (they are skipped, not an error)", got, want)
	}
}

func TestMonthSpend_roundsToMicros(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	// convention_scans.cost_usd is double precision: 0.1 + 0.2 must still total 0.300000.
	tn.conventionScan(0.1, mid)
	tn.conventionScan(0.2, mid)
	tn.agentRun(0.000001, mid)
	if got, want := l.spend(tn.ws, from, to), domain.Micros(300_001); got != want {
		t.Errorf("MonthSpend = %d, want %d", got, want)
	}
}

// TestMonthSpend_explain records the plan of the query for the NFR4 latency check: it must run
// under EXPLAIN ANALYZE and touch each of the six sources.
func TestMonthSpend_explain(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	tn.seedAll(mid)

	var plan []string
	err := l.db.Raw("EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) "+pgstore.MonthSpendSQL,
		map[string]any{"ws": tn.ws, "from": from, "to": to}).Scan(&plan).Error
	if err != nil {
		t.Fatalf("EXPLAIN ANALYZE: %v", err)
	}
	text := strings.Join(plan, "\n")
	for _, table := range []string{"agent_runs", "eval_runs", "convention_scans", "pr_intent", "pr_brief", "onboarding"} {
		if !strings.Contains(text, table) {
			t.Errorf("plan does not mention %s:\n%s", table, text)
		}
	}
	t.Logf("plan:\n%s", text)
}
