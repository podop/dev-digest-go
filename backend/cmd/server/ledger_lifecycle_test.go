package main

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// Component tests of the cost ledger's lifecycle through the real server (plan S21): the ledger
// outlives the things it describes (deletion of a run, eval case, agent, PR, repository; spec AC8,
// FR4, EC13), Settings B's reset and the retention prune leave it alone (AC9, FR12, EC14, EC15),
// and CI ingest records a run once, also across a reset (AC6, FR6, EC8-EC10, D16).

// llEntry is one cost_ledger row as the tests compare it: everything but the insertion order.
type llEntry struct {
	ID         string
	Kind       string
	Origin     string
	RefID      *string
	CostMicros *int64
	OccurredAt time.Time
	DedupeKey  string
}

// llTotal is one stored month total slot.
type llTotal struct {
	Month    string
	Slot     int
	Spent    int64
	Unpriced int
	Entries  int
}

// llState is the whole observable ledger: its entries, its stored totals and what the spend route
// reports for the server's current month.
type llState struct {
	Entries  []llEntry
	Totals   []llTotal
	SpentUSD any
	Unpriced any
}

func llRead(t testing.TB, s *stack) llState {
	t.Helper()
	var st llState
	err := s.db.DB.Raw(`SELECT id::text AS id, kind, origin, ref_id::text AS ref_id, cost_micros, occurred_at, dedupe_key
		FROM cost_ledger ORDER BY dedupe_key, id`).Scan(&st.Entries).Error
	if err != nil {
		t.Fatalf("read cost_ledger: %v", err)
	}
	err = s.db.DB.Raw(`SELECT month::text AS month, slot, spent_micros AS spent, unpriced_count AS unpriced, entry_count AS entries
		FROM spend_month_totals ORDER BY month, slot`).Scan(&st.Totals).Error
	if err != nil {
		t.Fatalf("read spend_month_totals: %v", err)
	}
	resp := s.get("/settings/spend")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /settings/spend = %d %s, want 200", resp.Status, resp.Body)
	}
	body := resp.Obj(t)
	st.SpentUSD, st.Unpriced = body["spent_usd"], body["unpriced_count"]
	return st
}

// llWant fails unless the spend route reports the given month spend and unknown-cost count.
func llWant(t testing.TB, st llState, usd float64, unpriced int) {
	t.Helper()
	got, ok := st.SpentUSD.(float64)
	if !ok || math.Abs(got-usd) > 1e-9 {
		t.Errorf("GET /settings/spend spent_usd = %v, want %v", st.SpentUSD, usd)
	}
	if st.Unpriced != float64(unpriced) {
		t.Errorf("GET /settings/spend unpriced_count = %v, want %d", st.Unpriced, unpriced)
	}
}

// llSame fails when the ledger differs from before: entries (ids included), totals and the route.
func llSame(t testing.TB, what string, before, after llState) {
	t.Helper()
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("the ledger changed %s (-before +after):\n%s", what, diff)
	}
}

// llSeed appends one entry the way the spend store does (the entry plus the month total it
// belongs to), with a chosen kind and reference. micros is an int64 amount or nil (unknown cost);
// ref is a uuid string or nil.
func llSeed(t testing.TB, s *stack, ws string, at time.Time, kind string, ref, micros any) {
	t.Helper()
	execSQL(t, s, `WITH e AS (
			INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, cost_micros, ref_id, dedupe_key)
			VALUES (?, ?::timestamptz, ?, 'live', ?::bigint, ?::uuid, 'seed:' || gen_random_uuid()::text)
			RETURNING workspace_id, occurred_at, cost_micros)
		INSERT INTO spend_month_totals (workspace_id, month, slot, spent_micros, unpriced_count, entry_count)
		SELECT workspace_id, date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date, 0,
			COALESCE(cost_micros, 0), (cost_micros IS NULL)::int, 1
		FROM e
		ON CONFLICT (workspace_id, month, slot) DO UPDATE SET
			spent_micros = spend_month_totals.spent_micros + EXCLUDED.spent_micros,
			unpriced_count = spend_month_totals.unpriced_count + EXCLUDED.unpriced_count,
			entry_count = spend_month_totals.entry_count + EXCLUDED.entry_count`, ws, at, kind, micros, ref)
}

// llFixture adds, on top of seedRunHistory, an agent run of the history agent and entries of every
// kind that points at something deletable. The month (capNow) holds 0.81 USD and two unknown-cost
// entries; the month before holds one more.
type llFixture struct {
	historyIDs
	extraRun string
}

const (
	llMonthUSD      = 0.81
	llMonthUnpriced = 2
)

func llSeedLedger(t testing.TB, s *stack) llFixture {
	t.Helper()
	id := seedRunHistory(t, s)
	extra := scalar[string](t, s.db.DB, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, status, source)
		VALUES (?, ?, ?, 'done', 'local') RETURNING id::text`, id.ws, id.agent, id.pr)
	at := capNow.Add(-time.Hour)
	llSeed(t, s, id.ws, at, "review_run", id.run, int64(300000))
	llSeed(t, s, id.ws, at, "review_run", extra, int64(150000))
	llSeed(t, s, id.ws, at, "review_run", extra, nil)
	llSeed(t, s, id.ws, at, "eval_case", id.findingCase, int64(200000))
	llSeed(t, s, id.ws, at, "review_intent", id.pr, int64(50000))
	llSeed(t, s, id.ws, at, "brief", id.pr, nil)
	llSeed(t, s, id.ws, at, "intent_refresh", id.pr, int64(40000))
	llSeed(t, s, id.ws, at, "onboarding", id.repo, int64(70000))
	llSeed(t, s, id.ws, capNow.AddDate(0, -1, 0), "review_run", id.run, int64(90000))
	return llFixture{historyIDs: id, extraRun: extra}
}

// ---- AC8: deletions ----

func TestLL_DeletionsKeepEveryEntryAndTheMonthSpend(t *testing.T) { // AC8, FR4, EC13
	s := startStack(t, spec{now: capClock})
	f := llSeedLedger(t, s)
	before := llRead(t, s)
	llWant(t, before, llMonthUSD, llMonthUnpriced)
	if len(before.Entries) != 9 {
		t.Fatalf("seeded cost_ledger entries = %d, want 9", len(before.Entries))
	}

	steps := []struct {
		name   string
		path   string
		status int
		gone   string // SQL counting what the delete must remove: want 0
		args   []any
	}{
		{"run", "/runs/" + f.run, http.StatusOK, `SELECT count(*) FROM agent_runs WHERE id = ?`, []any{f.run}},
		{"eval case", "/eval-cases/" + f.findingCase, http.StatusNoContent, `SELECT count(*) FROM eval_cases WHERE id = ?`, []any{f.findingCase}},
		{"agent", "/agents/" + f.agent, http.StatusOK, `SELECT count(*) FROM agents WHERE id = ?`, []any{f.agent}},
		{"repository", "/repos/" + f.repo, http.StatusOK, `SELECT count(*) FROM repos WHERE id = ?`, []any{f.repo}},
	}
	for _, step := range steps {
		if got := s.do(http.MethodDelete, step.path, nil); got.Status != step.status {
			t.Fatalf("DELETE %s = %d %s, want %d", step.path, got.Status, got.Body, step.status)
		}
		if n := count(t, s.db.DB, step.gone, step.args...); n != 0 {
			t.Errorf("after DELETE %s the %s still exists (%d rows), so the test proves nothing", step.path, step.name, n)
		}
		llSame(t, "after deleting the "+step.name, before, llRead(t, s))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE id = ?`, f.pr); n != 0 {
		t.Errorf("the PR %s survived the deletion of its repository, want it gone", f.pr)
	}
}

// ---- AC9: reset and prune ----

func TestLL_ResetHistoryKeepsTheLedger(t *testing.T) { // AC9, FR12, EC14, D6
	s := startStack(t, spec{now: historyClock, secretFile: secretsJSON()})
	f := llSeedLedger(t, s)
	// The history clock is another month than capNow; the fixture's month is read at capNow, so
	// the route would report 0. The stored totals and entries are what the reset must keep.
	before := llRead(t, s)
	if len(before.Entries) != 9 || len(before.Totals) != 2 {
		t.Fatalf("seeded ledger = %d entries, %d totals, want 9 and 2", len(before.Entries), len(before.Totals))
	}
	wantRuns := count(t, s.db.DB, `SELECT count(*) FROM agent_runs`)
	if wantRuns == 0 {
		t.Fatal("the fixture has no agent run, so the reset would prove nothing")
	}

	for _, confirm := range []string{"Default", "default"} { // a refused reset, then the real one
		got := s.post("/workspace/reset-history", map[string]any{"confirm": confirm})
		wantStatus := http.StatusUnprocessableEntity
		if confirm == "default" {
			wantStatus = http.StatusOK
		}
		if got.Status != wantStatus {
			t.Fatalf("reset with %q = %d %s, want %d", confirm, got.Status, got.Body, wantStatus)
		}
		llSame(t, "by the reset with "+confirm, before, llRead(t, s))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM agent_runs`); n != 0 {
		t.Errorf("agent_runs after the reset = %d, want 0 (it was %d): the reset did not run", n, wantRuns)
	}
	// The entries still point at the deleted runs: the reference is informational.
	if n := count(t, s.db.DB, `SELECT count(*) FROM cost_ledger WHERE ref_id = ?`, f.run); n != 2 {
		t.Errorf("entries that reference the deleted run %s = %d, want 2", f.run, n)
	}
}

func TestLL_ResetHistoryKeepsTheMonthSpendOnTheRoute(t *testing.T) { // AC9, FR12, EC14, D6/Q1
	s := startStack(t, spec{now: capClock})
	llSeedLedger(t, s)
	before := llRead(t, s)
	llWant(t, before, llMonthUSD, llMonthUnpriced)

	if got := s.post("/workspace/reset-history", map[string]any{"confirm": "default"}); got.Status != http.StatusOK {
		t.Fatalf("POST /workspace/reset-history = %d %s, want 200", got.Status, got.Body)
	}
	after := llRead(t, s)
	llWant(t, after, llMonthUSD, llMonthUnpriced)
	llSame(t, "by the reset", before, after)
}

func TestLL_RetentionPruneKeepsTheLedger(t *testing.T) { // AC9, FR12, EC15, D10
	db := pgtest.Migrated(t)
	first := startStack(t, spec{db: db, now: capClock})
	eventually(t, 30*time.Second, "the first boot's prune", func() bool {
		return len(logLines(t, first.logs, "trace prune done")) == 1
	})
	runs := seedPruneRuns(t, first)
	ws := workspaceID(t, first)
	for i, r := range runs { // an entry for every run, the pruned ones included
		llSeed(t, first, ws, capNow.Add(-time.Hour), "review_run", r.id, int64(10000*(i+1)))
	}
	llSeed(t, first, ws, capNow.Add(-time.Hour), "review_run", nil, nil)
	putRetention(t, first, 30)
	before := llRead(t, first)
	llWant(t, before, 0.15, 1)
	if err := first.stop(); err != nil {
		t.Fatalf("stop the first boot: %v", err)
	}

	second := startStack(t, spec{db: db, now: capClock})
	eventually(t, 30*time.Second, "the startup prune line", func() bool {
		return len(logLines(t, second.logs, "trace prune done")) == 1
	})
	line := logLines(t, second.logs, "trace prune done")[0]
	if line["deleted"] != float64(4) {
		t.Fatalf("the startup prune deleted %v traces, want 4 (aged 31, 89, 91 and 400 days), so the test proves nothing", line["deleted"])
	}
	llSame(t, "by the retention prune", before, llRead(t, second))
}

func TestLL_ExportCarriesNoLedger(t *testing.T) { // AC9, FR12, B FR14
	s := startStack(t, spec{now: historyClock, secretFile: secretsJSON()})
	id := seedRunHistory(t, s)
	without := s.get("/workspace/export")
	if without.Status != http.StatusOK {
		t.Fatalf("GET /workspace/export = %d %s, want 200", without.Status, without.Body)
	}

	llSeed(t, s, id.ws, historyNow.Add(-time.Hour), "review_run", id.run, int64(1234567))
	llSeed(t, s, id.ws, historyNow.Add(-time.Hour), "brief", id.pr, nil)
	with := s.get("/workspace/export")
	if with.Status != http.StatusOK {
		t.Fatalf("GET /workspace/export with entries = %d %s, want 200", with.Status, with.Body)
	}

	if string(with.Body) != string(without.Body) {
		t.Errorf("the export changed when entries were added (the clock is fixed), want the same bytes:\nwithout %.300s\nwith    %.300s", without.Body, with.Body)
	}
	st := llRead(t, s)
	for _, e := range st.Entries {
		for what, needle := range map[string]string{"entry id": e.ID, "dedupe key": e.DedupeKey} {
			if strings.Contains(string(with.Body), needle) {
				t.Errorf("GET /workspace/export contains the %s %q, want it absent", what, needle)
			}
		}
	}
	for _, needle := range []string{"cost_ledger", "spend_month_totals", "1234567", "1.234567", "unpriced_count", "dedupe"} {
		if strings.Contains(string(with.Body), needle) {
			t.Errorf("GET /workspace/export contains %q, want no ledger data", needle)
		}
	}
}

// ---- AC6: CI ingest ----

// llCI is a CI stack: a server on the clock now, an agent, and an installation of acme/ci-one.
type llCI struct {
	s     *stack
	gh    *ciGH
	agent string
	slug  string
	now   time.Time
}

func llCIStack(t *testing.T, now time.Time) llCI {
	t.Helper()
	sp := ciSpec()
	sp.now = func() time.Time { return now }
	s, gh := ciStack(t, sp)
	agent := ciCreateAgent(t, s, "Ledger Reviewer", "openrouter")
	const slug = "ledger-reviewer"
	ciInstall(t, s, agent, "acme/ci-one", slug, 1, now.Add(-240*time.Hour))
	return llCI{s: s, gh: gh, agent: agent, slug: slug, now: now}
}

// good registers a successful run with a verified result artifact; over replaces result keys.
func (c llCI) good(t *testing.T, id int64, created time.Time, over map[string]any) {
	t.Helper()
	c.gh.addGoodRun(t, "acme/ci-one", c.slug, ciRun(c.slug, "acme/ci-one", id, created), id+9000, over)
}

// bare registers a completed run with no result artifact (the runner died): a failed run.
func (c llCI) bare(id int64, created time.Time, conclusion string) {
	run := ciRun(c.slug, "acme/ci-one", id, created)
	run.Conclusion = conclusion
	key := "acme/ci-one/devdigest-review-" + c.slug + ".yml"
	c.gh.mu.Lock()
	c.gh.workflows[key] = append(c.gh.workflows[key], run)
	c.gh.mu.Unlock()
}

func (c llCI) refresh(t *testing.T) int {
	t.Helper()
	resp := c.s.post("/ci/refresh", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	ingested, _ := resp.Obj(t)["ingested"].(float64)
	return int(ingested)
}

// llCIEntries lists the ledger entries of kind ci_run.
func llCIEntries(t testing.TB, s *stack) []llEntry {
	t.Helper()
	var out []llEntry
	for _, e := range llRead(t, s).Entries {
		if e.Kind == "ci_run" {
			out = append(out, e)
		}
	}
	return out
}

// llCIFixture registers four runs on the clock capNow: one priced, one with a null cost, one
// failed (a "failure" conclusion) and one successful run whose artifact is missing.
func llCIFixture(t *testing.T) llCI {
	c := llCIStack(t, capNow)
	c.good(t, 601, capNow.Add(-2*time.Hour), map[string]any{"cost_usd": 0.12})
	c.good(t, 602, capNow.Add(-3*time.Hour), map[string]any{"cost_usd": nil})
	c.bare(603, capNow.Add(-4*time.Hour), "failure")
	c.bare(604, capNow.Add(-5*time.Hour), "success")
	return c
}

func TestLL_CIIngestRecordsOnceAndOnlyForArtifacts(t *testing.T) { // AC6, FR6, EC8, EC9
	c := llCIFixture(t)
	if got := c.refresh(t); got != 4 {
		t.Fatalf("first refresh ingested %d runs, want 4 (two with an artifact, two failed)", got)
	}

	entries := llCIEntries(t, c.s)
	byKey := map[string]llEntry{}
	for _, e := range entries {
		byKey[e.DedupeKey] = e
	}
	if len(entries) != 2 || len(byKey) != 2 {
		t.Fatalf("ci_run entries = %+v, want exactly the runs 601 and 602", entries)
	}
	priced, unpriced := byKey["ci:601:1"], byKey["ci:602:1"]
	if priced.CostMicros == nil || *priced.CostMicros != 120000 || priced.Origin != "ci_ingest" || !priced.OccurredAt.Equal(capNow.Add(-2*time.Hour)) {
		t.Errorf("entry of run 601 = %+v, want 120000 micros, origin ci_ingest, dated at the run's creation time %s", priced, capNow.Add(-2*time.Hour))
	}
	if unpriced.CostMicros != nil || unpriced.Origin != "ci_ingest" || !unpriced.OccurredAt.Equal(capNow.Add(-3*time.Hour)) {
		t.Errorf("entry of run 602 = %+v, want an unknown cost, origin ci_ingest, dated at the run's creation time %s", unpriced, capNow.Add(-3*time.Hour))
	}
	for _, key := range []string{"ci:603:1", "ci:604:1"} {
		if _, ok := byKey[key]; ok {
			t.Errorf("ledger holds the entry %s of a run without an artifact, want none", key)
		}
	}
	if n := count(t, c.s.db.DB, `SELECT count(*) FROM ci_runs`); n != 4 {
		t.Errorf("ci_runs = %d, want 4 (the failed runs are stored too)", n)
	}
	before := llRead(t, c.s)
	llWant(t, before, 0.12, 1)

	// EC8: the same runs on a second refresh. They are stored already, so nothing is ingested.
	if got := c.refresh(t); got != 0 {
		t.Errorf("second refresh ingested %d runs, want 0", got)
	}
	llSame(t, "by a second refresh", before, llRead(t, c.s))
}

func TestLL_CIReingestAfterResetDoesNotDoubleCount(t *testing.T) { // AC6, FR6, FR12, EC8, D16
	c := llCIFixture(t)
	c.refresh(t)
	before := llRead(t, c.s)
	llWant(t, before, 0.12, 1)
	if len(llCIEntries(t, c.s)) != 2 {
		t.Fatalf("ci_run entries after the first refresh = %d, want 2", len(llCIEntries(t, c.s)))
	}

	if got := c.s.post("/workspace/reset-history", map[string]any{"confirm": "default"}); got.Status != http.StatusOK {
		t.Fatalf("POST /workspace/reset-history = %d %s, want 200", got.Status, got.Body)
	}
	if n := count(t, c.s.db.DB, `SELECT count(*) FROM ci_runs`); n != 0 {
		t.Fatalf("ci_runs after the reset = %d, want 0", n)
	}
	llSame(t, "by the reset", before, llRead(t, c.s))

	// The runs are new to CI again, so they are stored and ingested a second time; the dedupe key
	// ci:<run>:<attempt> is what keeps the ledger at one entry each.
	if got := c.refresh(t); got != 4 {
		t.Fatalf("refresh after the reset ingested %d runs, want 4", got)
	}
	if n := count(t, c.s.db.DB, `SELECT count(*) FROM ci_runs`); n != 4 {
		t.Errorf("ci_runs after the re-ingest = %d, want 4", n)
	}
	llSame(t, "by the re-ingest after the reset", before, llRead(t, c.s))
}

func TestLL_CIRunOfLastMonthCountsThere(t *testing.T) { // AC6, FR6, FR7, EC10
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	created := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	c := llCIStack(t, now)
	c.good(t, 701, created, map[string]any{"cost_usd": 0.12})
	if got := c.refresh(t); got != 1 {
		t.Fatalf("refresh ingested %d runs, want 1", got)
	}

	entries := llCIEntries(t, c.s)
	if len(entries) != 1 || !entries[0].OccurredAt.Equal(created) {
		t.Fatalf("ci_run entries = %+v, want one dated %s (the run's creation, not the ingest time %s)", entries, created, now)
	}
	st := llRead(t, c.s)
	if len(st.Totals) != 1 || st.Totals[0].Month != "2026-09-01" || st.Totals[0].Spent != 120000 || st.Totals[0].Entries != 1 {
		t.Errorf("stored month totals = %+v, want one slot of 2026-09-01 holding 120000 micros and 1 entry", st.Totals)
	}
	llWant(t, st, 0, 0) // October, the current month, holds nothing
}

// D19, security L1: a forged artifact cannot pin the month at its cap. A run reporting more than
// $10,000 is stored (the run record is untouched) with an unknown cost; $10,000 exactly is kept.
// CI ingest already refuses an artifact whose cost its numeric(12,6) column cannot hold
// (artifact_invalid, so 9.9e11 never reaches the ledger); the ceiling covers the band below that.
func TestLL_CIImplausibleArtifactCostIsUnknown(t *testing.T) { // D19, FR3, FR6
	c := llCIStack(t, capNow)
	c.good(t, 801, capNow.Add(-2*time.Hour), map[string]any{"cost_usd": 9.9e11})
	c.good(t, 802, capNow.Add(-3*time.Hour), map[string]any{"cost_usd": 10000})
	c.good(t, 803, capNow.Add(-4*time.Hour), map[string]any{"cost_usd": 10000.01})
	c.good(t, 804, capNow.Add(-5*time.Hour), map[string]any{"cost_usd": 999999.99})
	if got := c.refresh(t); got != 3 {
		t.Fatalf("refresh ingested %d runs, want 3 (run 801 is refused as invalid before the ledger)", got)
	}

	byKey := map[string]llEntry{}
	for _, e := range llCIEntries(t, c.s) {
		byKey[e.DedupeKey] = e
	}
	if _, ok := byKey["ci:801:1"]; ok || len(byKey) != 3 {
		t.Fatalf("ci_run entries = %+v, want exactly the runs 802, 803 and 804", byKey)
	}
	for _, key := range []string{"ci:803:1", "ci:804:1"} {
		if e := byKey[key]; e.CostMicros != nil {
			t.Errorf("entry %s has cost %d micros, want an unknown cost (reported cost above $10,000)", key, *e.CostMicros)
		}
	}
	if e := byKey["ci:802:1"]; e.CostMicros == nil || *e.CostMicros != 10_000_000_000 {
		t.Errorf("entry ci:802:1 = %+v, want 10000000000 micros ($10,000 exactly is kept)", e)
	}
	llWant(t, llRead(t, c.s), 10000, 2)
}

// D19, security L2: a month total at the bigint maximum is served and enforced like any other: the
// spend route answers 200 and a capped entry point answers 402, not 500.
func TestLL_SaturatedMonthTotalDoesNotBreakTheSpendRouteOrTheGate(t *testing.T) { // D19
	s := startStack(t, spec{now: capClock})
	ws := workspaceID(t, s)
	requireNoSpend(t, s)
	for slot := range 8 {
		execSQL(t, s, `INSERT INTO spend_month_totals (workspace_id, month, slot, spent_micros, unpriced_count, entry_count)
			VALUES (?, '2031-01-01', ?, 9223372036854775807, 0, 1)`, ws, slot)
	}
	setCap(t, s, 50)

	resp := s.get("/settings/spend")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /settings/spend on a saturated month = %d %s, want 200", resp.Status, resp.Body)
	}
	if got := resp.Obj(t); got["cap_reached"] != true || got["remaining_usd"] != float64(0) || got["spent_usd"] != 9223372036854.775 {
		t.Errorf("GET /settings/spend on a saturated month = %v, want cap_reached true, remaining_usd 0 and spent_usd 9223372036854.775", got)
	}
	pr := s.get("/repos/" + repoByName(t, s, "acme/payments-api") + "/pulls").JSON(t).([]any)[0].(map[string]any)["id"].(string)
	if r := startReview(t, s, pr, map[string]any{"all": true}); r.Status != http.StatusPaymentRequired || r.errorCode(t) != "spend_cap_reached" {
		t.Errorf("POST review on a saturated month = %d %s, want 402 spend_cap_reached", r.Status, r.Body)
	}
}
