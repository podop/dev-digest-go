package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/spend/adapters/postgres"
	"dev-digest-go/backend/internal/spend/app"
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

var (
	_ app.Totals  = (*pgstore.Store)(nil)
	_ app.Entries = (*pgstore.Store)(nil)
)

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

// ---- the ledger store (current schema) ----

func micros(n int64) *domain.Micros { m := domain.Micros(n); return &m }

func int64p(n int64) *int64 { return &n }

// entryAt is a live review-run entry of ws with a unique key unless the test reuses one.
func entryAt(ws, key string, cost *domain.Micros, at time.Time) domain.Entry {
	return domain.Entry{
		WorkspaceID: ws, OccurredAt: at, Kind: domain.KindReviewRun, Origin: domain.OriginLive,
		Provider: "openai", Model: "gpt-4o", Cost: cost, DedupeKey: key,
	}
}

func (l *ledger) append(e domain.Entry) bool {
	l.t.Helper()
	stored, err := l.store.Append(context.Background(), e)
	if err != nil {
		l.t.Fatalf("Append(%s) error: %v", e.DedupeKey, err)
	}
	return stored
}

func (l *ledger) total(ws string, month time.Time) domain.MonthTotal {
	l.t.Helper()
	got, err := l.store.MonthTotal(context.Background(), ws, month)
	if err != nil {
		l.t.Fatalf("MonthTotal(%s, %v) error: %v", ws, month, err)
	}
	return got
}

func (l *ledger) count(query string, args ...any) int {
	l.t.Helper()
	var n int
	if err := l.db.Raw(query, args...).Scan(&n).Error; err != nil {
		l.t.Fatalf("count %.50s: %v", query, err)
	}
	return n
}

// assertTotalsMatchEntries checks the FR9 invariant for the workspace: for every month, the
// stored total (spent, unpriced, entry count) equals the aggregate over the entries.
func (l *ledger) assertTotalsMatchEntries(ws string) {
	l.t.Helper()
	type agg struct {
		Month                    string
		Spent, Unpriced, Entries int64
	}
	var want, got []agg
	err := l.db.Raw(`SELECT to_char(date_trunc('month', occurred_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS month,
			COALESCE(SUM(cost_micros), 0)::bigint AS spent,
			count(*) FILTER (WHERE cost_micros IS NULL) AS unpriced, count(*) AS entries
		FROM cost_ledger WHERE workspace_id = ? GROUP BY 1 ORDER BY 1`, ws).Scan(&want).Error
	if err != nil {
		l.t.Fatalf("aggregate entries: %v", err)
	}
	err = l.db.Raw(`SELECT to_char(month, 'YYYY-MM-DD') AS month, SUM(spent_micros)::bigint AS spent,
			SUM(unpriced_count)::bigint AS unpriced, SUM(entry_count)::bigint AS entries
		FROM spend_month_totals WHERE workspace_id = ? GROUP BY 1 ORDER BY 1`, ws).Scan(&got).Error
	if err != nil {
		l.t.Fatalf("aggregate totals: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		l.t.Errorf("stored totals of %s differ from the entries (-entries +totals):\n%s", ws, diff)
	}
}

func TestAppend_storesEntryAndTotal(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	ref := tn.pr

	full := domain.Entry{
		WorkspaceID: tn.ws, OccurredAt: mid, Kind: domain.KindBrief, Origin: domain.OriginLive,
		Provider: "anthropic", Model: "claude", TokensIn: int64p(1200), TokensOut: int64p(34),
		Cost: micros(123_456), RefID: ref, DedupeKey: "live:full",
	}
	if !l.append(full) {
		t.Fatal("Append(full) stored = false, want true")
	}
	// Provider and model '' and an empty ref stay as they are: '' and NULL.
	bare := domain.Entry{
		WorkspaceID: tn.ws, OccurredAt: mid, Kind: domain.KindCIRun, Origin: domain.OriginCIIngest,
		Cost: micros(1), DedupeKey: domain.CIDedupeKey(99, 1),
	}
	if !l.append(bare) {
		t.Fatal("Append(bare) stored = false, want true")
	}

	type row struct {
		Kind, Origin, Provider, Model string
		TokensIn, TokensOut, Cost     *int64
		Ref                           *string
	}
	read := func(key string) row {
		var r row
		err := l.db.Raw(`SELECT kind, origin, provider, model, tokens_in, tokens_out, cost_micros AS cost, ref_id::text AS ref
			FROM cost_ledger WHERE dedupe_key = ?`, key).Scan(&r).Error
		if err != nil {
			t.Fatalf("read entry %s: %v", key, err)
		}
		return r
	}
	wantFull := row{Kind: "brief", Origin: "live", Provider: "anthropic", Model: "claude",
		TokensIn: int64p(1200), TokensOut: int64p(34), Cost: int64p(123_456), Ref: &ref}
	if diff := cmp.Diff(wantFull, read("live:full")); diff != "" {
		t.Errorf("stored full entry mismatch (-want +got):\n%s", diff)
	}
	wantBare := row{Kind: "ci_run", Origin: "ci_ingest", Provider: "", Model: "", Cost: int64p(1)}
	if diff := cmp.Diff(wantBare, read(domain.CIDedupeKey(99, 1))); diff != "" {
		t.Errorf("stored bare entry mismatch (-want +got):\n%s", diff)
	}

	if got, want := l.total(tn.ws, from), (domain.MonthTotal{Spent: 123_457}); got != want {
		t.Errorf("MonthTotal = %+v, want %+v", got, want)
	}
	l.assertTotalsMatchEntries(tn.ws)
}

func TestAppend_nilCostIsUnpriced(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	l.append(entryAt(tn.ws, "k1", nil, mid))
	l.append(entryAt(tn.ws, "k2", nil, mid))
	l.append(entryAt(tn.ws, "k3", micros(0), mid)) // a zero cost is known, not unpriced
	l.append(entryAt(tn.ws, "k4", micros(250_000), mid))

	if got, want := l.total(tn.ws, from), (domain.MonthTotal{Spent: 250_000, Unpriced: 2}); got != want {
		t.Errorf("MonthTotal = %+v, want %+v", got, want)
	}
	if n := l.count(`SELECT sum(entry_count) FROM spend_month_totals WHERE workspace_id = ?`, tn.ws); n != 4 {
		t.Errorf("entry_count = %d, want 4", n)
	}
	l.assertTotalsMatchEntries(tn.ws)
}

func TestAppend_duplicateKeyIsNoop(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	if !l.append(entryAt(tn.ws, "dup", micros(500), mid)) {
		t.Fatal("first Append stored = false, want true")
	}
	// Same key again, even with another cost, month or workspace: nothing changes, no error.
	other := l.newTenant("b")
	for _, e := range []domain.Entry{
		entryAt(tn.ws, "dup", micros(500), mid),
		entryAt(tn.ws, "dup", micros(9_999), to),
		entryAt(tn.ws, "dup", nil, mid),
		entryAt(other.ws, "dup", micros(7), mid),
	} {
		if l.append(e) {
			t.Errorf("Append(duplicate %+v) stored = true, want false", e)
		}
	}
	if got, want := l.total(tn.ws, from), (domain.MonthTotal{Spent: 500}); got != want {
		t.Errorf("MonthTotal after duplicates = %+v, want %+v", got, want)
	}
	if got := l.total(tn.ws, to); got != (domain.MonthTotal{}) {
		t.Errorf("MonthTotal(next month) after duplicates = %+v, want zero", got)
	}
	if got := l.total(other.ws, from); got != (domain.MonthTotal{}) {
		t.Errorf("MonthTotal(other workspace) after duplicates = %+v, want zero", got)
	}
	if n := l.count(`SELECT count(*) FROM cost_ledger`); n != 1 {
		t.Errorf("entries = %d, want 1", n)
	}
}

// TestAppend_failedTotalRollsBackEntry: when the total cannot be updated, the entry is not
// stored either (FR9: one all-or-nothing step).
func TestAppend_failedTotalRollsBackEntry(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	l.exec(`ALTER TABLE spend_month_totals ADD CONSTRAINT test_no_totals CHECK (false)`)

	stored, err := l.store.Append(context.Background(), entryAt(tn.ws, "k", micros(5), mid))
	if err == nil || stored {
		t.Fatalf("Append with an unwritable total = %v, %v, want false and an error", stored, err)
	}
	if n := l.count(`SELECT count(*) FROM cost_ledger`); n != 0 {
		t.Errorf("entries after a failed append = %d, want 0 (rolled back with the total)", n)
	}

	l.exec(`ALTER TABLE spend_month_totals DROP CONSTRAINT test_no_totals`)
	if !l.append(entryAt(tn.ws, "k", micros(5), mid)) {
		t.Error("Append after the total is writable again stored = false, want true (the key was not used up)")
	}
}

func TestAppend_invalidEntryIsRejected(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	bad := map[string]domain.Entry{
		"kind":      {WorkspaceID: tn.ws, OccurredAt: mid, Kind: "nope", Origin: domain.OriginLive, DedupeKey: "a"},
		"origin":    {WorkspaceID: tn.ws, OccurredAt: mid, Kind: domain.KindBrief, Origin: "nope", DedupeKey: "b"},
		"cost":      {WorkspaceID: tn.ws, OccurredAt: mid, Kind: domain.KindBrief, Origin: domain.OriginLive, Cost: micros(-1), DedupeKey: "c"},
		"workspace": {WorkspaceID: "00000000-0000-0000-0000-000000000000", OccurredAt: mid, Kind: domain.KindBrief, Origin: domain.OriginLive, DedupeKey: "d"},
	}
	for name, e := range bad {
		t.Run(name, func(t *testing.T) {
			if stored, err := l.store.Append(context.Background(), e); err == nil || stored {
				t.Errorf("Append(%s) = %v, %v, want false and an error", name, stored, err)
			}
		})
	}
	if n := l.count(`SELECT count(*) FROM cost_ledger`) + l.count(`SELECT count(*) FROM spend_month_totals`); n != 0 {
		t.Errorf("rows after rejected appends = %d, want 0", n)
	}
}

// TestAppend_monthSplitsAtUTCBoundary (EC11): the month of an entry is its UTC month.
func TestAppend_monthSplitsAtUTCBoundary(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	plus2 := time.FixedZone("UTC+2", 2*3600)
	for i, tc := range []struct {
		name string
		at   time.Time
		cost int64
	}{
		{"last millisecond of October", to.Add(-time.Millisecond), 1},
		{"first instant of November", to, 10},
		{"first instant of October", from, 100},
		{"November 1st 01:00 at UTC+2 is still October", time.Date(2026, time.November, 1, 1, 0, 0, 0, plus2), 1000},
		{"October 31st 23:30 at UTC-2 is already November", time.Date(2026, time.October, 31, 23, 30, 0, 0, time.FixedZone("UTC-2", -2*3600)), 10_000},
	} {
		l.append(entryAt(tn.ws, fmt.Sprint("k", i), micros(tc.cost), tc.at))
	}
	if got, want := l.total(tn.ws, from), (domain.MonthTotal{Spent: 1 + 100 + 1000}); got != want {
		t.Errorf("MonthTotal(October) = %+v, want %+v", got, want)
	}
	if got, want := l.total(tn.ws, to), (domain.MonthTotal{Spent: 10 + 10_000}); got != want {
		t.Errorf("MonthTotal(November) = %+v, want %+v", got, want)
	}
	l.assertTotalsMatchEntries(tn.ws)
}

func TestAppend_joinsCallerTransaction(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	tx := platformpg.NewTx(l.db)
	errRollback := errors.New("roll back")

	err := tx.InTx(context.Background(), func(ctx context.Context) error {
		if stored, err := l.store.Append(ctx, entryAt(tn.ws, "in-tx", micros(40), mid)); err != nil || !stored {
			t.Errorf("Append in tx = %v, %v, want true, nil", stored, err)
		}
		// The caller sees its own uncommitted entry through the same handle.
		if got, err := l.store.MonthTotal(ctx, tn.ws, mid); err != nil || got.Spent != 40 {
			t.Errorf("MonthTotal in tx = %+v, %v, want 40 spent", got, err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("InTx error = %v, want the rollback error", err)
	}
	if n := l.count(`SELECT count(*) FROM cost_ledger`) + l.count(`SELECT count(*) FROM spend_month_totals`); n != 0 {
		t.Errorf("rows after the caller rolled back = %d, want 0 (the append joined the transaction)", n)
	}

	err = tx.InTx(context.Background(), func(ctx context.Context) error {
		_, err := l.store.Append(ctx, entryAt(tn.ws, "in-tx", micros(40), mid))
		return err
	})
	if err != nil {
		t.Fatalf("InTx(commit) error: %v", err)
	}
	if got, want := l.total(tn.ws, mid), (domain.MonthTotal{Spent: 40}); got != want {
		t.Errorf("MonthTotal after commit = %+v, want %+v", got, want)
	}
}

// TestAppend_concurrent (AC10, EC12, NFR5): 50 appends to workspace 1 and 10 to workspace 2 with
// known and unknown costs, plus 10 duplicates of workspace 1's keys, all at once.
func TestAppend_concurrent(t *testing.T) {
	l := newLedger(t)
	a, b := l.newTenant("a"), l.newTenant("b")

	type job struct {
		e    domain.Entry
		dupe bool
	}
	var jobs []job
	var wantA, wantB domain.MonthTotal
	for i := range 50 {
		var cost *domain.Micros
		if i%5 != 0 {
			cost = micros(int64(1_000_003 + i*37))
			wantA.Spent += *cost
		} else {
			wantA.Unpriced++
		}
		e := entryAt(a.ws, fmt.Sprint("a", i), cost, mid)
		jobs = append(jobs, job{e: e})
		if i < 10 {
			jobs = append(jobs, job{e: e, dupe: true})
		}
	}
	for i := range 10 {
		var cost *domain.Micros
		if i%3 != 0 {
			cost = micros(int64(7_000_001 + i))
			wantB.Spent += *cost
		} else {
			wantB.Unpriced++
		}
		jobs = append(jobs, job{e: entryAt(b.ws, fmt.Sprint("b", i), cost, mid)})
	}

	var wg sync.WaitGroup
	var stored atomic.Int64
	start := make(chan struct{})
	for _, j := range jobs {
		wg.Go(func() {
			<-start
			ok, err := l.store.Append(context.Background(), j.e)
			if err != nil {
				t.Errorf("Append(%s) error: %v", j.e.DedupeKey, err)
				return
			}
			if ok {
				stored.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()

	if got, want := stored.Load(), int64(60); got != want {
		t.Errorf("appends stored = %d, want %d (each key once, duplicates are no-ops)", got, want)
	}
	if got := l.total(a.ws, from); got != wantA {
		t.Errorf("MonthTotal(workspace 1) = %+v, want %+v", got, wantA)
	}
	if got := l.total(b.ws, from); got != wantB {
		t.Errorf("MonthTotal(workspace 2) = %+v, want %+v", got, wantB)
	}
	l.assertTotalsMatchEntries(a.ws)
	l.assertTotalsMatchEntries(b.ws)
	if n := l.count(`SELECT count(*) FROM cost_ledger WHERE workspace_id = ?`, a.ws); n != 50 {
		t.Errorf("entries of workspace 1 = %d, want 50", n)
	}
	// The appends spread over the slot rows (A8), within the 8 the schema allows.
	slotRows := l.count(`SELECT count(*) FROM spend_month_totals WHERE workspace_id = ?`, a.ws)
	if slotRows < 2 || slotRows > pgstore.Slots {
		t.Errorf("slot rows of workspace 1 = %d, want 2..%d", slotRows, pgstore.Slots)
	}
}

func TestMonthTotal_emptyAndIsolated(t *testing.T) {
	l := newLedger(t)
	a, b := l.newTenant("a"), l.newTenant("b")
	if got := l.total(a.ws, from); got != (domain.MonthTotal{}) {
		t.Errorf("MonthTotal(no entries) = %+v, want zero (EC19)", got)
	}
	l.append(entryAt(a.ws, "x", micros(33), mid))
	if got := l.total(b.ws, from); got != (domain.MonthTotal{}) {
		t.Errorf("MonthTotal(other workspace) = %+v, want zero", got)
	}
	if got := l.total("00000000-0000-0000-0000-000000000000", from); got != (domain.MonthTotal{}) {
		t.Errorf("MonthTotal(unknown workspace) = %+v, want zero", got)
	}
	// Any instant of the month names the month.
	for _, at := range []time.Time{from, mid, to.Add(-time.Nanosecond)} {
		if got := l.total(a.ws, at); got.Spent != 33 {
			t.Errorf("MonthTotal(at %v) = %+v, want 33 spent", at, got)
		}
	}
	if got := l.total(a.ws, to); got.Spent != 0 {
		t.Errorf("MonthTotal(November) = %+v, want zero", got)
	}
}

// TestMonthTotal_sumsAllSlots: the total is the sum of the slot rows of the month, whatever
// slots hold it.
func TestMonthTotal_sumsAllSlots(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	var want domain.MonthTotal
	for slot := range pgstore.Slots {
		l.exec(`INSERT INTO spend_month_totals (workspace_id, month, slot, spent_micros, unpriced_count, entry_count)
			VALUES (?, '2026-10-01', ?, ?, ?, 1)`, tn.ws, slot, 1000*(slot+1), slot)
		want.Spent += domain.Micros(1000 * (slot + 1))
		want.Unpriced += slot
	}
	l.exec(`INSERT INTO spend_month_totals (workspace_id, month, slot, spent_micros) VALUES (?, '2026-11-01', 0, 5)`, tn.ws)
	if got := l.total(tn.ws, mid); got != want {
		t.Errorf("MonthTotal = %+v, want %+v", got, want)
	}
}

// TestAppend_saturatesTheMonthTotal: a slot near the bigint maximum does not fail the append with
// an overflow; it saturates, and so does the sum of several saturated slots (D19, security L2).
func TestAppend_saturatesTheMonthTotal(t *testing.T) {
	const maxBigint = int64(1<<63 - 1)
	l := newLedger(t)
	tn := l.newTenant("a")
	// Every slot holds the maximum minus 5, so any entry of 10 or more pushes its slot past it.
	for slot := range pgstore.Slots {
		l.exec(`INSERT INTO spend_month_totals (workspace_id, month, slot, spent_micros, unpriced_count, entry_count)
			VALUES (?, '2026-10-01', ?, ?, 0, 1)`, tn.ws, slot, maxBigint-5)
	}

	for i, cost := range []int64{10, 1_000_000_000_000, maxBigint} {
		key := fmt.Sprintf("sat-%d", i)
		if !l.append(entryAt(tn.ws, key, micros(cost), mid)) {
			t.Fatalf("Append(%s, %d micros) stored = false, want true", key, cost)
		}
	}
	if !l.append(entryAt(tn.ws, "sat-unpriced", nil, mid)) {
		t.Fatalf("Append(unpriced) on a saturated month stored = false, want true")
	}

	if got, want := l.count(`SELECT count(*) FROM spend_month_totals WHERE workspace_id = ? AND spent_micros > ?`, tn.ws, maxBigint), 0; got != want {
		t.Errorf("slots above the bigint maximum = %d, want %d", got, want)
	}
	// Eight saturated slots sum past the bigint range; the read clamps instead of failing.
	got := l.total(tn.ws, mid)
	if want := (domain.MonthTotal{Spent: domain.Micros(maxBigint), Unpriced: 1}); got != want {
		t.Errorf("MonthTotal(saturated month) = %+v, want %+v", got, want)
	}
	if n := l.count(`SELECT count(*) FROM cost_ledger WHERE workspace_id = ?`, tn.ws); n != 4 {
		t.Errorf("cost_ledger rows = %d, want 4 (the entries are kept whole)", n)
	}
}

func TestClaimBackfillSummary_ClaimsTheRowOnce(t *testing.T) { // FR15
	l := newLedger(t)
	err := l.db.Exec(`UPDATE cost_ledger_backfill
		SET created = CAST(? AS jsonb), skipped = 4, duration_ms = 1500, logged_at = NULL WHERE id = 1`,
		`{"review_run": 3, "ci_run": 2, "future_kind": 9}`).Error
	if err != nil {
		t.Fatalf("set backfill row: %v", err)
	}

	got, claimed, err := l.store.ClaimBackfillSummary(t.Context())
	if err != nil || !claimed {
		t.Fatalf("ClaimBackfillSummary() first call = _, %v, %v, want _, true, nil", claimed, err)
	}
	want := app.BackfillSummary{
		Created:  map[domain.Kind]int{domain.KindReviewRun: 3, domain.KindCIRun: 2},
		Skipped:  4,
		Duration: 1500 * time.Millisecond,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ClaimBackfillSummary() first call mismatch (-want +got):\n%s", diff)
	}
	var stamped bool
	if err := l.db.Raw(`SELECT logged_at IS NOT NULL FROM cost_ledger_backfill WHERE id = 1`).Scan(&stamped).Error; err != nil || !stamped {
		t.Errorf("logged_at stamped after the claim = %v, %v, want true, nil", stamped, err)
	}

	if _, claimed, err := l.store.ClaimBackfillSummary(t.Context()); err != nil || claimed {
		t.Errorf("ClaimBackfillSummary() second call = _, %v, %v, want _, false, nil", claimed, err)
	}
}
