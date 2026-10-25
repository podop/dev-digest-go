package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	conventionsapp "dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/llm"
	spendapp "dev-digest-go/backend/internal/spend/app"
)

// Component tests of the spend context through the real server (plan S28): the month total, which
// since the cost ledger is the sum of the ledger entries only (AC9, ledger FR8/FR11), the cap check on
// all eight entry routes (AC10, AC11, NFR6) and the workspace default model (AC8). The wiring smoke
// tests are in spend_wiring_test.go.

// refusedMsg is the INFO line one refusal writes (spend/app.Service.Admit).
const refusedMsg = "spend cap reached: request refused"

// setCap stores the monthly cap through PUT /settings (nil clears it).
func setCap(t testing.TB, s *stack, capUSD any) {
	t.Helper()
	if put := s.do(http.MethodPut, "/settings", map[string]any{"monthly_spend_cap_usd": capUSD}); put.Status != http.StatusOK {
		t.Fatalf("PUT /settings monthly_spend_cap_usd=%v = %d %s, want 200", capUSD, put.Status, put.Body)
	}
}

// workspaceID is the id of the seeded workspace.
func workspaceID(t testing.TB, s *stack) string {
	t.Helper()
	return scalar[string](t, s.db.DB, `SELECT id::text FROM workspaces ORDER BY created_at LIMIT 1`)
}

// capNow is the fake clock of these tests: a month far from the seeded demo data (which is dated
// near the real now), so the month's spend of a fresh server is exactly 0 and a test adds
// hand-computed amounts to it.
var capNow = time.Date(2031, 1, 15, 12, 0, 0, 0, time.UTC)

func capClock() time.Time { return capNow }

// requireNoSpend fails unless the fake month has no spend yet.
func requireNoSpend(t testing.TB, s *stack) {
	t.Helper()
	if got := s.get("/settings/spend").Obj(t)["spent_usd"]; got != float64(0) {
		t.Fatalf("GET /settings/spend on a fresh server: spent_usd = %v, want 0", got)
	}
}

// seedLedgerSpend records a priced ledger entry of usd for workspace ws at the instant at, the way
// the spend store appends one: a cost_ledger row plus the stored month total (slot 0) it belongs to
// (the UTC month of at). Plain SQL on the cost ledger tables (migration 0005), so these tests do
// not depend on the store's code.
func seedLedgerSpend(t testing.TB, s *stack, ws string, at time.Time, usd float64) {
	t.Helper()
	insertLedgerEntry(t, s, ws, at, int64(math.Round(usd*1e6)))
}

// seedLedgerUnpriced records a ledger entry without a known cost: it adds to the month's unpriced
// count and to nothing else.
func seedLedgerUnpriced(t testing.TB, s *stack, ws string, at time.Time) {
	t.Helper()
	insertLedgerEntry(t, s, ws, at, nil)
}

// insertLedgerEntry writes one review_run entry; micros is an int64 amount in micro-USD or nil.
func insertLedgerEntry(t testing.TB, s *stack, ws string, at time.Time, micros any) {
	t.Helper()
	execSQL(t, s, `WITH e AS (
			INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, cost_micros, dedupe_key)
			VALUES (?, ?::timestamptz, 'review_run', 'live', ?::bigint, 'seed:' || gen_random_uuid()::text)
			RETURNING workspace_id, occurred_at, cost_micros)
		INSERT INTO spend_month_totals (workspace_id, month, slot, spent_micros, unpriced_count, entry_count)
		SELECT workspace_id, date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date, 0,
			COALESCE(cost_micros, 0), (cost_micros IS NULL)::int, 1
		FROM e
		ON CONFLICT (workspace_id, month, slot) DO UPDATE SET
			spent_micros = spend_month_totals.spent_micros + EXCLUDED.spent_micros,
			unpriced_count = spend_month_totals.unpriced_count + EXCLUDED.unpriced_count,
			entry_count = spend_month_totals.entry_count + EXCLUDED.entry_count`, ws, at, micros)
}

// ---- AC9: the month total ----

func TestSpend_MonthTotalIsTheLedgerAndIgnoresFeatureRows(t *testing.T) { // AC9 (ledger FR8, FR11; Settings A FR12, EC4, EC5, EC7)
	s := startStack(t, spec{now: capClock})
	ws := workspaceID(t, s)
	acme := repoByName(t, s, "acme/payments-api")
	requireNoSpend(t, s)

	var n int
	pr := func(workspace, repo string) string {
		n++
		return scalar[string](t, s.db.DB, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
			VALUES (?, ?, ?, 't', 'a', 'b', 'main', 'sha') RETURNING id::text`, workspace, repo, 9000+n)
	}
	evalCase := func(workspace string) string {
		return scalar[string](t, s.db.DB, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name)
			VALUES (?, 'agent', gen_random_uuid(), 'c') RETURNING id::text`, workspace)
	}
	brief := func(workspace, repo string, cost float64, at string) {
		execSQL(t, s, `INSERT INTO pr_brief (pr_id, json) VALUES (?, ?::jsonb)`, pr(workspace, repo),
			fmt.Sprintf(`{"generated_at": %q, "cost_usd": %v}`, at, cost))
	}
	tour := func(workspace string, cost float64, at time.Time) {
		n++
		repo := scalar[string](t, s.db.DB, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', ?, ?) RETURNING id::text`,
			workspace, fmt.Sprint("r", n), fmt.Sprint("o/r", n))
		execSQL(t, s, `INSERT INTO onboarding (repo_id, json, generated_at) VALUES (?, ?::jsonb, ?)`, repo, fmt.Sprintf(`{"cost_usd": %v}`, cost), at)
	}
	mid := capNow
	start := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2031, 2, 1, 0, 0, 0, 0, time.UTC)

	// Feature cost rows as the old month query summed them, written after the upgrade: one of each of
	// the six sources inside the month, and the mirrors. None of them is a ledger entry, so none moves
	// the month spend (FR11).
	execSQL(t, s, `INSERT INTO agent_runs (workspace_id, ran_at, cost_usd) VALUES (?, ?, 0.01)`, ws, mid)
	execSQL(t, s, `INSERT INTO eval_runs (case_id, ran_at, cost_usd) VALUES (?, ?, 0.02)`, evalCase(ws), mid)
	execSQL(t, s, `INSERT INTO convention_scans (workspace_id, repo_id, status, started_at, cost_usd) VALUES (?, ?, 'done', ?, 0.04)`, ws, acme, mid)
	execSQL(t, s, `INSERT INTO pr_intent (pr_id, intent, cost_usd, derived_at) VALUES (?, 'i', 0.08, ?)`, pr(ws, acme), mid)
	brief(ws, acme, 0.16, mid.Format(time.RFC3339))
	tour(ws, 0.32, mid)
	execSQL(t, s, `INSERT INTO agent_runs (workspace_id, ran_at, cost_usd) VALUES (?, ?, 0.64)`, ws, start)
	agent := scalar[string](t, s.db.DB, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
		VALUES (?, 'mirror', 'openai', 'm', 'p') RETURNING id::text`, ws)
	suite := scalar[string](t, s.db.DB, `INSERT INTO eval_suite_runs (workspace_id, agent_id, agent_version, config, case_ids, status, started_at, cost_usd)
		VALUES (?, ?, 1, '{}', '[]', 'completed', ?, 1000) RETURNING id::text`, ws, agent, mid)
	execSQL(t, s, `INSERT INTO eval_runs (case_id, ran_at, cost_usd, run_id) VALUES (?, ?, 0, ?)`, evalCase(ws), mid, suite)
	execSQL(t, s, `INSERT INTO ci_runs (ran_at, cost_usd, source) VALUES (?, 2000, 'ci')`, mid)

	requireNoSpend(t, s)
	setCap(t, s, 0.5)
	wantFree := map[string]any{"month_start": "2031-01-01T00:00:00.000Z", "spent_usd": float64(0), "cap_usd": 0.5, "remaining_usd": 0.5, "cap_reached": false, "unpriced_count": float64(0)}
	if diff := cmp.Diff(wantFree, s.get("/settings/spend").Obj(t)); diff != "" {
		t.Errorf("GET /settings/spend with feature cost rows only mismatch (-want +got):\n%s", diff)
	}
	setCap(t, s, nil)

	// Ledger entries in the month: 0.01 + 0.02 + 0.04 + 0.08 count; one exactly at its first instant,
	// one in its last millisecond, and one whose local date is already in February but whose UTC
	// instant is not (the month is the UTC month).
	seedLedgerSpend(t, s, ws, mid, 0.01)
	seedLedgerSpend(t, s, ws, start, 0.02)
	seedLedgerSpend(t, s, ws, end.Add(-time.Millisecond), 0.04)
	seedLedgerSpend(t, s, ws, time.Date(2031, 2, 1, 3, 0, 0, 0, time.FixedZone("UTC+5", 5*3600)), 0.08)
	// In the month but without a known cost: it adds nothing (EC7).
	seedLedgerUnpriced(t, s, ws, mid)
	// Outside the month (the upper bound is exclusive; a UTC-5 local time of January 31 is February 1
	// in UTC) or of another workspace: none counts.
	seedLedgerSpend(t, s, ws, end, 2.56)
	seedLedgerSpend(t, s, ws, start.Add(-time.Millisecond), 5.12)
	seedLedgerSpend(t, s, ws, time.Date(2031, 1, 31, 23, 30, 0, 0, time.FixedZone("UTC-5", -5*3600)), 0.5)
	other := scalar[string](t, s.db.DB, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id::text`)
	seedLedgerSpend(t, s, other, mid, 10.24)

	// 0.01 + 0.02 + 0.04 + 0.08
	const total = 0.15
	want := map[string]any{"month_start": "2031-01-01T00:00:00.000Z", "spent_usd": total, "cap_usd": nil, "remaining_usd": nil, "cap_reached": false, "unpriced_count": float64(1)}
	if diff := cmp.Diff(want, s.get("/settings/spend").Obj(t)); diff != "" {
		t.Errorf("GET /settings/spend without a cap mismatch (-want +got):\n%s", diff)
	}
	setCap(t, s, total)
	want["cap_usd"], want["remaining_usd"], want["cap_reached"] = total, float64(0), true
	if diff := cmp.Diff(want, s.get("/settings/spend").Obj(t)); diff != "" {
		t.Errorf("GET /settings/spend with the cap equal to the spend mismatch (-want +got):\n%s", diff)
	}
	setCap(t, s, 0.16)
	want["cap_usd"], want["remaining_usd"], want["cap_reached"] = 0.16, 0.01, false
	if diff := cmp.Diff(want, s.get("/settings/spend").Obj(t)); diff != "" {
		t.Errorf("GET /settings/spend with the cap one cent above the spend mismatch (-want +got):\n%s", diff)
	}

	// A feature row written now still changes nothing.
	execSQL(t, s, `INSERT INTO agent_runs (workspace_id, ran_at, cost_usd) VALUES (?, ?, 3)`, ws, mid)
	if diff := cmp.Diff(want, s.get("/settings/spend").Obj(t)); diff != "" {
		t.Errorf("GET /settings/spend after one more feature cost row mismatch (-want +got):\n%s", diff)
	}
}

// ---- the eight entry routes ----

// capFixture is a server whose repository o/r is cloned and indexed, with an eval agent and an eval
// skill that have cases, so that every entry route can get past its own 404 and 422 checks and reach
// the cap check.
type capFixture struct {
	t     *testing.T
	s     *stack
	sc    *scripted
	repo  string // o/r, cloned and indexed
	cold  string // acme/payments-api, never cloned, no index
	pr    string // seeded pull request #482
	agent string // eval agent with 2 cases
	skill string // eval skill with 2 cases
	ws    string
}

// newCapFixture starts the stack. extra scripts the model's answers per schema; the tour is always
// answered with tourAnswer and every other schema like the mock.
func newCapFixture(t *testing.T, extra map[string]callFn) *capFixture {
	t.Helper()
	handlers := map[string]callFn{"OnboardingTour": func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return answer(tourAnswer, 900, 300, 0.002), nil
	}}
	for schema, fn := range extra {
		handlers[schema] = fn
	}
	sc := routed(handlers)
	s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll(), now: capClock})
	f := &capFixture{t: t, s: s, sc: sc, repo: repo, cold: repoByName(t, s, "acme/payments-api"), pr: pr482(t, s), ws: workspaceID(t, s)}
	requireNoSpend(t, s)
	f.agent = evAgent(t, s, "Cap Agent")
	evSuiteCases(t, s, "agent", f.agent, 2)
	f.skill = newSkill(t, s, "cap-skill", "BODY")
	evSuiteCases(t, s, "skill", f.skill, 2)
	return f
}

// seedSpend records a ledger entry of usd in the fake clock's month, so that a cap below it is reached.
func (f *capFixture) seedSpend(usd float64) {
	f.t.Helper()
	seedLedgerSpend(f.t, f.s, f.ws, capNow, usd)
}

// work counts everything an entry route could create, plus the model calls made so far.
func (f *capFixture) work() map[string]int {
	f.t.Helper()
	out := map[string]int{"model calls": f.sc.calls()}
	for _, table := range []string{"agent_runs", "multi_agent_runs", "eval_suite_runs", "eval_runs", "convention_scans", "pr_intent", "pr_brief", "onboarding", "cost_ledger"} {
		out[table] = count(f.t, f.s.db.DB, "SELECT count(*) FROM "+table)
	}
	out["conventions jobs"] = count(f.t, f.s.db.DB, `SELECT count(*) FROM jobs WHERE kind = ?`, conventionsapp.KindExtract)
	return out
}

// entryRoute is one of the eight LLM entry routes.
type entryRoute struct {
	name     string
	op       string
	admitted int // the status when the cap is not reached
	call     func(f *capFixture) response
	// settle waits until the work an admitted call started has finished.
	settle func(f *capFixture, r response)
}

func (f *capFixture) routes() []entryRoute {
	t, s := f.t, f.s
	awaitEval := func(_ *capFixture, r response) { evAwait(t, s, r.Obj(t)["id"].(string)) }
	return []entryRoute{
		{
			name: "review_start", op: spendapp.OpReviewStart, admitted: http.StatusOK,
			call: func(f *capFixture) response { return startReview(t, s, f.pr, map[string]any{"all": true}) },
			settle: func(f *capFixture, r response) {
				for _, run := range r.Obj(t)["runs"].([]any) {
					waitStatus(t, s, run.(map[string]any)["run_id"].(string), "done")
				}
			},
		},
		{
			name: "eval_agent_start", op: spendapp.OpEvalAgentStart, admitted: http.StatusAccepted,
			call:   func(f *capFixture) response { return s.post("/agents/"+f.agent+"/eval-runs", nil) },
			settle: awaitEval,
		},
		{
			name: "eval_skill_start", op: spendapp.OpEvalSkillStart, admitted: http.StatusAccepted,
			call: func(f *capFixture) response {
				return s.post("/skills/"+f.skill+"/eval-runs", map[string]any{"agent_id": f.agent})
			},
			settle: awaitEval,
		},
		{
			name: "eval_run_all", op: spendapp.OpEvalRunAll, admitted: http.StatusAccepted,
			call: func(f *capFixture) response { return s.post("/eval-runs/run-all", nil) },
			settle: func(f *capFixture, r response) {
				started, _ := r.Obj(t)["started"].([]any)
				if len(started) == 0 {
					t.Errorf("run-all below the cap started no run: %s", r.Body)
				}
				for _, run := range started {
					evAwait(t, s, run.(map[string]any)["id"].(string))
				}
			},
		},
		{
			name: "conventions_extract", op: spendapp.OpConventionsExtract, admitted: http.StatusAccepted,
			call:   func(f *capFixture) response { return s.post("/repos/"+f.repo+"/conventions/extract", nil) },
			settle: func(f *capFixture, _ response) { waitScan(t, s, f.repo) },
		},
		{
			name: "intent_refresh", op: spendapp.OpIntentRefresh, admitted: http.StatusOK,
			call: func(f *capFixture) response { return s.post("/pulls/"+f.pr+"/intent/refresh", nil) },
		},
		{
			name: "brief_generate", op: spendapp.OpBriefGenerate, admitted: http.StatusOK,
			call: func(f *capFixture) response { return s.post("/pulls/"+f.pr+"/brief", nil) },
		},
		{
			name: "onboarding_generate", op: spendapp.OpOnboardingGenerate, admitted: http.StatusOK,
			call: func(f *capFixture) response { return s.post("/repos/"+f.repo+"/onboarding", nil) },
		},
	}
}

// refusals lists the log entries of the refusals so far.
func (f *capFixture) refusals() []map[string]any { return logLines(f.t, f.s.logs, refusedMsg) }

// wantRefused checks the 402 answer of a route and its single INFO line (NFR6).
func (f *capFixture) wantRefused(r response, op string, before int) {
	f.t.Helper()
	t := f.t
	if r.Status != http.StatusPaymentRequired || r.errorCode(t) != "spend_cap_reached" {
		t.Fatalf("%s with the cap reached = %d %s, want 402 spend_cap_reached", op, r.Status, r.Body)
	}
	if got, want := message(t, r), "Monthly spend cap of $1.00 reached: $2.50 used since 2031-01-01 (UTC)."; got != want {
		t.Errorf("%s 402 message = %q, want %q", op, got, want)
	}
	wantDetails := map[string]any{"cap_usd": 1.0, "spent_usd": 2.5, "month_start": "2031-01-01T00:00:00.000Z"}
	if diff := cmp.Diff(wantDetails, obj(t, r.JSON(t), "error", "details")); diff != "" {
		t.Errorf("%s 402 details mismatch (-want +got):\n%s", op, diff)
	}
	lines := f.refusals()
	if len(lines) != before+1 {
		t.Fatalf("%s: %d refusal log line(s) in total, want %d (exactly one more)", op, len(lines), before+1)
	}
	line := lines[len(lines)-1]
	want := map[string]any{"level": "INFO", "op": op, "workspace_id": f.ws, "cap_usd": 1.0, "spent_usd": 2.5, "month_start": "2031-01-01T00:00:00.000Z"}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s refusal log field %s = %v, want %v (line %v)", op, k, line[k], v, line)
		}
	}
}

// AC10, NFR6: with the cap reached every entry route answers 402 and creates nothing. The fixture
// lets each route pass its own 404 and 422 checks, so this proves the cap check is wired in each.
func TestCap_RefusesEveryEntryRoute(t *testing.T) {
	f := newCapFixture(t, nil)
	f.seedSpend(2.5)
	setCap(t, f.s, 1.0)
	if got := f.s.get("/settings/spend").Obj(t); got["cap_reached"] != true || got["spent_usd"] != 2.5 {
		t.Fatalf("GET /settings/spend = %v, want cap_reached with spent 2.5", got)
	}
	for _, route := range f.routes() {
		t.Run(route.name, func(t *testing.T) {
			before, lines := f.work(), len(f.refusals())
			f.wantRefused(route.call(f), route.op, lines)
			if diff := cmp.Diff(before, f.work()); diff != "" {
				t.Errorf("%s created work although it was refused (-before +after):\n%s", route.name, diff)
			}
		})
	}
	var ops []string
	for _, line := range f.refusals() {
		ops = append(ops, line["op"].(string))
	}
	var wantOps []string
	for _, route := range f.routes() {
		wantOps = append(wantOps, route.op)
	}
	if diff := cmp.Diff(wantOps, ops); diff != "" {
		t.Errorf("ops of the refusal log lines mismatch (-want +got):\n%s", diff)
	}
}

// AC10 (below the cap the same calls behave as today), NFR6 (no refusal line when admitted).
func TestCap_BelowCapAdmitsEveryEntryRoute(t *testing.T) {
	f := newCapFixture(t, nil)
	f.seedSpend(0.5)
	setCap(t, f.s, 1000)
	for _, route := range f.routes() {
		t.Run(route.name, func(t *testing.T) {
			r := route.call(f)
			if r.Status != route.admitted {
				t.Fatalf("%s below the cap = %d %s, want %d", route.name, r.Status, r.Body, route.admitted)
			}
			if route.settle != nil {
				route.settle(f, r)
			}
		})
	}
	if lines := f.refusals(); len(lines) != 0 {
		t.Errorf("refusal log lines below the cap = %v, want none", lines)
	}
	// The same calls with no cap at all are admitted too.
	setCap(t, f.s, nil)
	for _, route := range f.routes() {
		t.Run(route.name+" without a cap", func(t *testing.T) {
			r := route.call(f)
			if r.Status != route.admitted {
				t.Fatalf("%s without a cap = %d %s, want %d", route.name, r.Status, r.Body, route.admitted)
			}
			if route.settle != nil {
				route.settle(f, r)
			}
		})
	}
}

// AC11, D16: when the spend cannot be read the route answers 500 and starts nothing, whatever the
// cause: a stored cap that is not a valid amount, or a ledger query that fails.
func TestCap_SpendReadErrorFailsClosed(t *testing.T) {
	f := newCapFixture(t, nil)
	causes := []struct {
		name           string
		breakIt, fixIt func()
	}{
		{
			name: "unreadable cap",
			breakIt: func() {
				execSQL(t, f.s, `DELETE FROM settings WHERE key = 'monthly_spend_cap_usd'`)
				execSQL(t, f.s, `INSERT INTO settings (workspace_id, key, value) VALUES (?, 'monthly_spend_cap_usd', '"abc"'::jsonb)`, f.ws)
			},
			fixIt: func() { execSQL(t, f.s, `DELETE FROM settings WHERE key = 'monthly_spend_cap_usd'`) },
		},
		{
			// The month spend is read from spend_month_totals alone (ledger FR8). A cap is set because
			// without one the check does not read the spend (D24).
			name: "failing ledger",
			breakIt: func() {
				setCap(t, f.s, 1000)
				execSQL(t, f.s, `ALTER TABLE spend_month_totals RENAME TO spend_month_totals_gone`)
			},
			fixIt: func() {
				execSQL(t, f.s, `ALTER TABLE spend_month_totals_gone RENAME TO spend_month_totals`)
				setCap(t, f.s, nil)
			},
		},
	}
	for _, cause := range causes {
		for _, route := range f.routes() {
			t.Run(cause.name+"/"+route.name, func(t *testing.T) {
				before := f.work()
				cause.breakIt()
				r := route.call(f)
				cause.fixIt()
				if r.Status != http.StatusInternalServerError || r.errorCode(t) != "internal_error" {
					t.Fatalf("%s with a %s = %d %s, want 500 internal_error", route.name, cause.name, r.Status, r.Body)
				}
				if diff := cmp.Diff(before, f.work()); diff != "" {
					t.Errorf("%s did work although the spend could not be read (-before +after):\n%s", route.name, diff)
				}
			})
		}
	}
	if lines := f.refusals(); len(lines) != 0 {
		t.Errorf("refusal log lines for read errors = %v, want none (a read error is not a refusal)", lines)
	}
}

// AC11, EC8, EC9 and the user decisions of 2026-10-24: the 404 and 422 checks of a route come before
// the cap; the cap comes before the 409 and before the checks inside the generation.
func TestCap_RefusalOrdering(t *testing.T) {
	f := newCapFixture(t, nil)
	s := f.s
	f.seedSpend(2.5)
	setCap(t, f.s, 1.0)

	t.Run("404 and 422 come first", func(t *testing.T) {
		bare := evAgent(t, s, "Bare Agent")
		bareSkill := newSkill(t, s, "bare-skill", "BODY")
		tests := []struct {
			name   string
			resp   func() response
			status int
			code   string
		}{
			{"review of an unknown pull request", func() response { return startReview(t, s, unknownID, map[string]any{"all": true}) }, 404, ""},
			{"review with an invalid body", func() response { return startReview(t, s, f.pr, map[string]any{"all": "yes"}) }, 422, "validation_error"},
			{"eval agent start of an unknown agent", func() response { return s.post("/agents/"+unknownID+"/eval-runs", nil) }, 404, "not_found"},
			{"eval agent start without cases", func() response { return s.post("/agents/"+bare+"/eval-runs", nil) }, 422, "no_eval_cases"},
			{"eval skill start of an unknown skill", func() response { return s.post("/skills/"+unknownID+"/eval-runs", map[string]any{"agent_id": f.agent}) }, 404, "not_found"},
			{"eval skill start without agent_id", func() response { return s.post("/skills/"+f.skill+"/eval-runs", map[string]any{}) }, 422, "validation_error"},
			{"eval skill start of an unknown host agent", func() response { return s.post("/skills/"+f.skill+"/eval-runs", map[string]any{"agent_id": unknownID}) }, 422, "host_agent_not_found"},
			{"eval skill start without cases", func() response { return s.post("/skills/"+bareSkill+"/eval-runs", map[string]any{"agent_id": f.agent}) }, 422, "no_eval_cases"},
			{"conventions of an unknown repo", func() response { return s.post("/repos/"+unknownID+"/conventions/extract", nil) }, 404, ""},
			{"conventions of a repo that is not cloned", func() response { return s.post("/repos/"+f.cold+"/conventions/extract", nil) }, 422, "not_cloned"},
			{"intent refresh of an unknown pull request", func() response { return s.post("/pulls/"+unknownID+"/intent/refresh", nil) }, 404, ""},
			{"intent refresh with an id that is no uuid", func() response { return s.post("/pulls/not-a-uuid/intent/refresh", nil) }, 422, "validation_error"},
			{"brief of an unknown pull request", func() response { return s.post("/pulls/"+unknownID+"/brief", nil) }, 404, ""},
			{"onboarding of an unknown repo", func() response { return s.post("/repos/"+unknownID+"/onboarding", nil) }, 404, ""},
		}
		before, lines := f.work(), len(f.refusals())
		for _, tc := range tests {
			r := tc.resp()
			if r.Status != tc.status || (tc.code != "" && r.errorCode(t) != tc.code) {
				t.Errorf("%s with the cap reached = %d %s, want %d %s", tc.name, r.Status, r.Body, tc.status, tc.code)
			}
		}
		if got := len(f.refusals()); got != lines {
			t.Errorf("refusal log lines after the 404/422 calls = %d, want %d: the cap check must not run", got, lines)
		}
		if diff := cmp.Diff(before, f.work()); diff != "" {
			t.Errorf("a 404/422 call created work (-before +after):\n%s", diff)
		}
	})

	t.Run("402 beats the 409 and the checks inside the generation", func(t *testing.T) {
		e := newEvFixtures(t, s)
		busyAgent := evAgent(t, s, "Busy Agent")
		busyCases := evSuiteCases(t, s, "agent", busyAgent, 2)
		e.run(evRun{agent: busyAgent, status: "running", cases: busyCases})
		busySkill := newSkill(t, s, "busy-skill", "BODY")
		skillCases := evSuiteCases(t, s, "skill", busySkill, 2)
		e.run(evRun{agent: f.agent, skill: busySkill, status: "running", cases: skillCases})
		execSQL(t, s, `INSERT INTO convention_scans (workspace_id, repo_id, status, started_at) VALUES (?, ?, 'running', now())`, f.ws, f.repo)
		noFiles := newFx(t, s).pr(f.cold, 9100, map[string]string{})

		tests := []struct {
			name string
			op   string
			call func() response
		}{
			{"eval agent with a run in progress (409)", spendapp.OpEvalAgentStart, func() response { return s.post("/agents/"+busyAgent+"/eval-runs", nil) }},
			{"eval skill with a run in progress (409)", spendapp.OpEvalSkillStart, func() response {
				return s.post("/skills/"+busySkill+"/eval-runs", map[string]any{"agent_id": f.agent})
			}},
			{"conventions with a scan running (409)", spendapp.OpConventionsExtract, func() response { return s.post("/repos/"+f.repo+"/conventions/extract", nil) }},
			{"onboarding without an index (422 index_not_ready)", spendapp.OpOnboardingGenerate, func() response { return s.post("/repos/"+f.cold+"/onboarding", nil) }},
			{"brief of a pull request without files (422 no_changed_files)", spendapp.OpBriefGenerate, func() response { return s.post("/pulls/"+noFiles+"/brief", nil) }},
		}
		for _, tc := range tests {
			before, lines := f.work(), len(f.refusals())
			f.wantRefused(tc.call(), tc.op, lines)
			if diff := cmp.Diff(before, f.work()); diff != "" {
				t.Errorf("%s created work (-before +after):\n%s", tc.name, diff)
			}
		}
	})
}

// AC11 (EC9): a brief that is generating does not turn the next request into a 409 once the cap is
// reached; the running generation still completes.
func TestCap_RefusedBriefWhileOneIsGenerating(t *testing.T) {
	g := newIntentGate()
	t.Cleanup(g.Open)
	f := newCapFixture(t, map[string]callFn{"PrBriefLlm": g.call})
	s := f.s
	first := make(chan response, 1)
	go func() {
		first <- evWait(t, evDoAsync(s, http.MethodPost, "/pulls/"+f.pr+"/brief", nil), "the running brief")
	}()
	eventually(t, 30*time.Second, "the first brief to be inside its model call", func() bool { return countOf(f.sc, "PrBriefLlm") == 1 })

	if r := s.post("/pulls/"+f.pr+"/brief", nil); r.Status != http.StatusConflict || r.errorCode(t) != "generation_in_progress" {
		t.Fatalf("second brief below the cap = %d %s, want 409 generation_in_progress", r.Status, r.Body)
	}
	f.seedSpend(2.5)
	setCap(t, s, 1.0)
	f.wantRefused(s.post("/pulls/"+f.pr+"/brief", nil), spendapp.OpBriefGenerate, 0)

	g.Open()
	if r := <-first; r.Status != http.StatusOK {
		t.Errorf("the running brief = %d %s, want 200: crossing the cap must not abort it", r.Status, r.Body)
	}
}

// AC11 (EC6, EC8): the cap is checked once, when a review starts. A review that is running when the
// cap is crossed finishes with status done, and two starts that arrive together one cent below the
// cap are both admitted.
func TestCap_IsCheckedAtStartOnly(t *testing.T) {
	t.Run("a running review completes when the cap is crossed", func(t *testing.T) {
		g := newGate()
		t.Cleanup(g.Open)
		s := startStack(t, spec{llmAdapters: g.forAll(), now: capClock})
		pr := pr482(t, s)
		setCap(t, s, 1000)
		runs, _ := startOK(t, s, pr, map[string]any{"all": true})
		eventually(t, 30*time.Second, "a review to be inside its model call", func() bool { return g.calls() > 0 })

		setCap(t, s, 0)
		if r := startReview(t, s, pr, map[string]any{"all": true}); r.Status != http.StatusPaymentRequired {
			t.Fatalf("review start with the cap reached = %d %s, want 402", r.Status, r.Body)
		}
		g.Open()
		for _, run := range runs {
			if got := waitStatus(t, s, run, "done", "failed", "cancelled"); got != "done" {
				t.Errorf("run %s ended %q after the cap was crossed, want done", run, got)
			}
		}
	})

	t.Run("two concurrent starts one cent below the cap are both admitted", func(t *testing.T) {
		s := startStack(t, spec{llmAdapters: mockScript().forAll(), now: capClock})
		requireNoSpend(t, s)
		ws := workspaceID(t, s)
		seedLedgerSpend(t, s, ws, capNow, 0.99)
		setCap(t, s, 1.0)
		if got := s.get("/settings/spend").Obj(t); got["cap_reached"] != false || got["remaining_usd"] != 0.01 {
			t.Fatalf("GET /settings/spend = %v, want 0.01 remaining", got)
		}
		prs := []string{pr482(t, s), scalar[string](t, s.db.DB, `SELECT id::text FROM pull_requests WHERE number = 483`)}
		a := evDoAsync(s, http.MethodPost, "/pulls/"+prs[0]+"/review", map[string]any{"all": true})
		b := evDoAsync(s, http.MethodPost, "/pulls/"+prs[1]+"/review", map[string]any{"all": true})
		ra, rb := evWait(t, a, "first start"), evWait(t, b, "second start")
		if ra.Status != http.StatusOK || rb.Status != http.StatusOK {
			t.Errorf("concurrent review starts = %d %s and %d %s, want both 200", ra.Status, ra.Body, rb.Status, rb.Body)
		}
	})
}

// AC10, AC11 (user decision 2026-10-24: one rule, the cap before any work): an empty agent list is
// refused with 402 too, while below the cap the same calls are answered as before.
func TestCap_EmptyAgentListsAnswer402(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	pr := pr482(t, s)
	execSQL(t, s, `UPDATE agents SET enabled = false`)

	if r := startReview(t, s, pr, map[string]any{"all": true}); r.Status != http.StatusOK || len(r.Obj(t)["runs"].([]any)) != 0 {
		t.Fatalf("review of all enabled agents with none enabled = %d %s, want 200 with no runs", r.Status, r.Body)
	}
	for _, a := range list(t, s.get("/agents")) {
		if r := s.do(http.MethodDelete, "/agents/"+a["id"].(string), nil); r.Status >= 300 {
			t.Fatalf("DELETE /agents/%v = %d %s", a["id"], r.Status, r.Body)
		}
	}
	if r := s.post("/eval-runs/run-all", nil); r.Status != http.StatusAccepted {
		t.Fatalf("run-all without agents = %d %s, want 202", r.Status, r.Body)
	}

	setCap(t, s, 0)
	if r := startReview(t, s, pr, map[string]any{"all": true}); r.Status != http.StatusPaymentRequired || r.errorCode(t) != "spend_cap_reached" {
		t.Errorf("review of all enabled agents with none enabled and the cap reached = %d %s, want 402", r.Status, r.Body)
	}
	if r := s.post("/eval-runs/run-all", nil); r.Status != http.StatusPaymentRequired || r.errorCode(t) != "spend_cap_reached" {
		t.Errorf("run-all without agents and the cap reached = %d %s, want 402", r.Status, r.Body)
	}
}

// ---- AC8: the default model ----

// modelSpy is the three providers' adapters, each recording what it was asked.
type modelSpy struct {
	byProvider map[llm.Provider]*scripted
}

func newModelSpy() *modelSpy {
	answerTour := func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return answer(tourAnswer, 900, 300, 0.002), nil
	}
	one := func() *scripted { return routed(map[string]callFn{"OnboardingTour": answerTour}) }
	return &modelSpy{byProvider: map[llm.Provider]*scripted{llm.OpenRouter: one(), llm.OpenAI: one(), llm.Anthropic: one()}}
}

func (m *modelSpy) adapters() map[llm.Provider]llm.Adapter {
	out := map[llm.Provider]llm.Adapter{}
	for p, sc := range m.byProvider {
		out[p] = sc
	}
	return out
}

// seen is how many attempts each provider has received.
func (m *modelSpy) seen() map[llm.Provider]int {
	out := map[llm.Provider]int{}
	for p, sc := range m.byProvider {
		out[p] = sc.calls()
	}
	return out
}

// used lists the "provider/model" pairs of the attempts of one schema made since mark.
func (m *modelSpy) used(schema string, mark map[llm.Provider]int) []string {
	var out []string
	for p, sc := range m.byProvider {
		for i := mark[p]; i < sc.calls(); i++ {
			at := sc.attempt(i)
			if pair := string(p) + "/" + at.Model; at.SchemaName == schema && !slices.Contains(out, pair) {
				out = append(out, pair)
			}
		}
	}
	sort.Strings(out)
	return out
}

// AC8 (FR9, EC10): default_model decides the provider and model of every feature without a choice of
// its own; a feature with its own choice keeps it; with no default each feature uses its registry
// default. The call of each feature is observed at the model adapter, over the real routes.
func TestDefaultModel_DrivesFeaturesWithoutTheirOwnChoice(t *testing.T) {
	spy := newModelSpy()
	s, _, repo := tourStack(t, spec{llmAdapters: spy.adapters()})
	pr := pr482(t, s)

	features := []struct {
		name, schema string
		run          func()
	}{
		{"conventions", "ConventionExtraction", func() { runScan(t, s, repo) }},
		{"intent", "IntentClassification", func() {
			if r := s.post("/pulls/"+pr+"/intent/refresh", nil); r.Status != http.StatusOK {
				t.Fatalf("intent refresh = %d %s, want 200", r.Status, r.Body)
			}
		}},
		{"brief", "PrBriefLlm", func() {
			if r := s.post("/pulls/"+pr+"/brief", nil); r.Status != http.StatusOK {
				t.Fatalf("brief = %d %s, want 200", r.Status, r.Body)
			}
		}},
		{"onboarding", "OnboardingTour", func() {
			if r := s.post("/repos/"+repo+"/onboarding", nil); r.Status != http.StatusOK {
				t.Fatalf("onboarding = %d %s, want 200", r.Status, r.Body)
			}
		}},
	}
	// observe runs every feature and returns the provider/model each one called.
	observe := func() map[string][]string {
		t.Helper()
		out := map[string][]string{}
		for _, f := range features {
			mark := spy.seen()
			f.run()
			out[f.name] = spy.used(f.schema, mark)
		}
		return out
	}
	want := func(conventions, intent, brief, onboarding string) map[string][]string {
		return map[string][]string{"conventions": {conventions}, "intent": {intent}, "brief": {brief}, "onboarding": {onboarding}}
	}

	// default_model unset: the registry default of each feature.
	if diff := cmp.Diff(want("openai/gpt-5.4", "openrouter/deepseek/deepseek-v4-flash", "openai/gpt-4.1", "openrouter/deepseek/deepseek-v4-flash"), observe()); diff != "" {
		t.Errorf("models used without a default_model mismatch (-want +got):\n%s", diff)
	}

	// default_model set: every feature without a choice of its own uses it.
	if put := s.do(http.MethodPut, "/settings", map[string]any{"default_model": map[string]any{"provider": "openrouter", "model": "a/x"}}); put.Status != http.StatusOK {
		t.Fatalf("PUT /settings default_model = %d %s, want 200", put.Status, put.Body)
	}
	if diff := cmp.Diff(want("openrouter/a/x", "openrouter/a/x", "openrouter/a/x", "openrouter/a/x"), observe()); diff != "" {
		t.Errorf("models used with default_model openrouter a/x mismatch (-want +got):\n%s", diff)
	}

	// A feature with its own choice keeps it.
	setFeatureModel(t, s, "conventions", "anthropic", "claude-haiku-4-5")
	if diff := cmp.Diff(want("anthropic/claude-haiku-4-5", "openrouter/a/x", "openrouter/a/x", "openrouter/a/x"), observe()); diff != "" {
		t.Errorf("models used with an own choice for conventions mismatch (-want +got):\n%s", diff)
	}
}
