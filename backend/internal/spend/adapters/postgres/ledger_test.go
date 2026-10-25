package postgres_test

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"dev-digest-go/backend/internal/platform/postgres/migrate"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/spend/adapters/postgres"
	"dev-digest-go/backend/internal/spend/domain"
	"dev-digest-go/backend/migrations"
)

// preLedgerVersion is the last migration before the cost ledger (0005_cost_ledger).
const preLedgerVersion = 4

// newLegacyLedger returns a database migrated up to the version before the cost ledger, so that the
// cost records of the six sources can be seeded the way they exist before the upgrade (AC13).
func newLegacyLedger(t *testing.T) *ledger {
	t.Helper()
	d := pgtest.Empty(t)
	l := &ledger{t: t, db: d.DB, store: pgstore.NewStore(d.DB)}
	n, err := l.migrator(upToVersion(t, preLedgerVersion)).Up(t.Context())
	if err != nil || n != preLedgerVersion {
		t.Fatalf("Up(versions 1-%d) = %d, %v, want %d, nil", preLedgerVersion, n, err, preLedgerVersion)
	}
	return l
}

// upToVersion returns the embedded migrations with a version <= max.
func upToVersion(t *testing.T, max int) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		v, err := strconv.Atoi(name[:strings.IndexByte(name, '_')])
		if err != nil {
			t.Fatalf("migration %s: no numeric version prefix", name)
		}
		if v > max {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &fstest.MapFile{Data: body}
	}
	return out
}

func (l *ledger) migrator(fsys fs.FS) *migrate.Migrator {
	l.t.Helper()
	sqlDB, err := l.db.DB()
	if err != nil {
		l.t.Fatalf("DB.DB(): %v", err)
	}
	m, err := migrate.New(sqlDB, fsys, slog.New(slog.DiscardHandler))
	if err != nil {
		l.t.Fatalf("migrate.New(): %v", err)
	}
	return m
}

// upgrade applies the cost ledger migration, which backfills the ledger and the totals.
func (l *ledger) upgrade() {
	l.t.Helper()
	if n, err := l.migrator(migrations.FS).Up(context.Background()); err != nil || n != 1 {
		l.t.Fatalf("Up() = %d, %v, want 1, nil (the cost ledger migration only)", n, err)
	}
}

type monthKey struct {
	ws    string
	month time.Time
}

// referenceTotals derives the pre-upgrade month spend of every workspace for the months from
// first to last (inclusive, each the first instant of a month) with the old query.
func (l *ledger) referenceTotals(workspaces []string, first, last time.Time) map[monthKey]domain.Micros {
	l.t.Helper()
	out := map[monthKey]domain.Micros{}
	for _, ws := range workspaces {
		for m := first; !m.After(last); m = domain.NextMonthStart(m) {
			out[monthKey{ws, m}] = l.referenceSpend(ws, m, domain.NextMonthStart(m))
		}
	}
	return out
}

// assertParity checks AC13: after the upgrade, the stored total of every (workspace, month)
// equals the reference total and no entry is unpriced.
func (l *ledger) assertParity(want map[monthKey]domain.Micros) {
	l.t.Helper()
	for k, ref := range want {
		got, err := l.store.MonthTotal(context.Background(), k.ws, k.month)
		if err != nil {
			l.t.Fatalf("MonthTotal(%s, %v) error: %v", k.ws, k.month, err)
		}
		if got.Spent != ref {
			l.t.Errorf("MonthTotal(%s, %s).Spent = %d, want %d (the pre-upgrade derived total)",
				k.ws, k.month.Format("2006-01"), got.Spent, ref)
		}
		if got.Unpriced != 0 {
			l.t.Errorf("MonthTotal(%s, %s).Unpriced = %d, want 0", k.ws, k.month.Format("2006-01"), got.Unpriced)
		}
		l.assertTotalsMatchEntries(k.ws)
	}
}

// TestBackfill_parityWithOldQuery is the Settings A AC9 fixture at the version before the
// ledger (AC13): all six sources, the mirrors, NULL costs, malformed brief and tour documents,
// month boundaries, three months and two workspaces. After the upgrade each month's total
// equals what the old query derived. Every cost has at most 6 decimals (D15).
func TestBackfill_parityWithOldQuery(t *testing.T) {
	l := newLegacyLedger(t)
	a, b := l.newTenant("a"), l.newTenant("b")
	us := time.Microsecond
	okTime := mid.Format(time.RFC3339)

	// October of a: the six sources at the middle and on both boundaries.
	a.seedAll(mid)
	a.seedAll(from)
	a.seedAll(to.Add(-us))
	// Next to them: the mirrors, NULL costs and costs that need exact rounding.
	a.mirrors(mid)
	a.agentRun(nil, mid)
	a.evalRun(nil, mid)
	a.conventionScan(nil, mid)
	a.intent(nil, mid)
	a.brief(fmt.Sprintf(`{"generated_at": %q, "cost_usd": null}`, okTime))
	a.onboarding(`{"cost_usd": null}`, mid)
	a.conventionScan(0.1, mid) // double precision: 0.1 + 0.2 must still total 300000
	a.conventionScan(0.2, mid)
	a.agentRun(0.000001, mid)
	// Malformed documents are skipped, not an error; the two good ones count.
	a.briefAt(0.16, mid)
	a.brief(`{"generated_at": "not a date", "cost_usd": 100}`)
	a.brief(`{"generated_at": "2026-02-31T00:00:00Z", "cost_usd": 100}`)
	a.brief(`{"generated_at": 20261015, "cost_usd": 100}`)
	a.brief(`{"cost_usd": 100}`)
	a.brief(fmt.Sprintf(`{"generated_at": %q, "cost_usd": "abc"}`, okTime))
	a.brief(fmt.Sprintf(`{"generated_at": %q, "cost_usd": {"a": 1}}`, okTime))
	a.brief(fmt.Sprintf(`{"generated_at": %q}`, okTime))
	a.brief(`[]`)
	a.brief(`"text"`)
	a.brief(`null`)
	a.onboardingAt(0.32, mid)
	a.onboarding(`{"cost_usd": "abc"}`, mid)
	a.onboarding(`{"cost_usd": [1]}`, mid)
	a.onboarding(`{}`, mid)
	a.onboarding(`[]`, mid)
	a.onboarding(`"text"`, mid)
	// September and November of a: just before and exactly at the October bounds.
	a.seedAll(from.Add(-us))
	a.seedAll(to)
	// Workspace b: other costs, and nothing of a's.
	b.agentRun(5, mid)
	b.briefAt(7, mid)
	b.onboardingAt(11, mid)
	b.mirrors(to)
	b.agentRun(2.5, to.AddDate(0, 1, 0))

	workspaces := []string{a.ws, b.ws}
	sep := from.AddDate(0, -1, 0)
	want := l.referenceTotals(workspaces, sep.AddDate(0, -1, 0), to.AddDate(0, 2, 0))

	// The fixture itself must be what it is meant to be, or parity proves nothing.
	for k, w := range map[monthKey]domain.Micros{
		{a.ws, sep}:                   allSixMicros,
		{a.ws, from}:                  3*allSixMicros + 300_001 + 480_000,
		{a.ws, to}:                    allSixMicros,
		{b.ws, from}:                  23_000_000,
		{b.ws, to}:                    0,
		{b.ws, to.AddDate(0, 1, 0)}:   2_500_000,
		{b.ws, sep.AddDate(0, -1, 0)}: 0,
	} {
		if want[k] != w {
			t.Fatalf("reference total (%s, %s) = %d, want %d: the fixture is not what the test assumes",
				k.ws, k.month.Format("2006-01"), want[k], w)
		}
	}

	l.upgrade()
	l.assertParity(want)
	if n := l.count(`SELECT count(*) FROM cost_ledger WHERE origin <> 'backfill'`); n != 0 {
		t.Errorf("entries not of origin backfill = %d, want 0", n)
	}
}

// TestBackfill_parityEachSourceAlone: every source counts by itself, and the mirrors by
// themselves count nothing.
func TestBackfill_parityEachSourceAlone(t *testing.T) {
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
		{"mirrors only", func(tn *tenant) { tn.mirrors(mid) }, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newLegacyLedger(t)
			tn := l.newTenant("a")
			tc.seed(tn)
			want := l.referenceTotals([]string{tn.ws}, from, from)
			if got := want[monthKey{tn.ws, from}]; got != tc.want {
				t.Fatalf("reference total = %d, want %d", got, tc.want)
			}
			l.upgrade()
			l.assertParity(want)
		})
	}
}

// TestBackfill_ciRunDedupe: an agent run of source 'ci' with a ci_runs row is backfilled with the
// dedupe key of that CI run, so a later re-ingest appends nothing and the total stays (FR10, A11);
// the ci_runs mirror itself is not counted.
func TestBackfill_ciRunDedupe(t *testing.T) {
	l := newLegacyLedger(t)
	tn := l.newTenant("a")
	ciAgentRun := l.scalar(`INSERT INTO agent_runs (workspace_id, ran_at, cost_usd, source)
		VALUES (?, ?, 0.5, 'ci') RETURNING id`, tn.ws, mid)
	l.exec(`INSERT INTO ci_runs (ran_at, cost_usd, source, agent_run_id, github_run_id, run_attempt)
		VALUES (?, 0.5, 'ci', ?, 777, 2)`, mid, ciAgentRun)
	// A CI agent run without a ci_runs row keeps a source-derived key.
	orphan := l.scalar(`INSERT INTO agent_runs (workspace_id, ran_at, cost_usd, source)
		VALUES (?, ?, 0.25, 'ci') RETURNING id`, tn.ws, mid)
	tn.agentRun(0.125, mid) // a local run

	want := l.referenceTotals([]string{tn.ws}, from, from)
	if got := want[monthKey{tn.ws, from}]; got != 875_000 {
		t.Fatalf("reference total = %d, want 875000", got)
	}
	l.upgrade()
	l.assertParity(want)

	keys := map[string]string{}
	rows, err := l.db.Raw(`SELECT dedupe_key, kind FROM cost_ledger WHERE workspace_id = ?`, tn.ws).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, kind string
		if err := rows.Scan(&k, &kind); err != nil {
			t.Fatal(err)
		}
		keys[k] = kind
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for k, kind := range map[string]string{
		domain.CIDedupeKey(777, 2):     "ci_run",
		"backfill:agent_run:" + orphan: "ci_run",
	} {
		if keys[k] != kind {
			t.Errorf("entry %q kind = %q, want %q (entries: %v)", k, keys[k], kind, keys)
		}
	}

	// Re-ingest of the same run attempt: nothing is added; another attempt is new.
	reingest := domain.Entry{
		WorkspaceID: tn.ws, OccurredAt: mid, Kind: domain.KindCIRun, Origin: domain.OriginCIIngest,
		Cost: micros(500_000), RefID: ciAgentRun, DedupeKey: domain.CIDedupeKey(777, 2),
	}
	if l.append(reingest) {
		t.Error("Append(re-ingest of the backfilled CI run) stored = true, want false")
	}
	if got, want := l.total(tn.ws, from), (domain.MonthTotal{Spent: 875_000}); got != want {
		t.Errorf("MonthTotal after the re-ingest = %+v, want %+v", got, want)
	}
	reingest.DedupeKey = domain.CIDedupeKey(777, 3)
	if !l.append(reingest) {
		t.Error("Append(another attempt of the run) stored = false, want true")
	}
	if got, want := l.total(tn.ws, from), (domain.MonthTotal{Spent: 1_375_000}); got != want {
		t.Errorf("MonthTotal after another attempt = %+v, want %+v", got, want)
	}
	l.assertTotalsMatchEntries(tn.ws)
}

// TestBackfill_freshDatabaseIsEmpty: a database without cost records backfills nothing (D18), and
// the totals read as zero.
func TestBackfill_freshDatabaseIsEmpty(t *testing.T) {
	l := newLegacyLedger(t)
	tn := l.newTenant("a")
	l.upgrade()
	if n := l.count(`SELECT count(*) FROM cost_ledger`) + l.count(`SELECT count(*) FROM spend_month_totals`); n != 0 {
		t.Errorf("rows after upgrading a database without cost records = %d, want 0", n)
	}
	if got := l.total(tn.ws, from); got != (domain.MonthTotal{}) {
		t.Errorf("MonthTotal = %+v, want zero", got)
	}
}
