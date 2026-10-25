package migrate_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/postgres/migrate"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/migrations"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestLoad_EmbeddedMigrations(t *testing.T) {
	got, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatalf("Load(migrations.FS) error = %v, want nil", err)
	}
	if len(got) == 0 || got[0].Version != 1 || got[0].Name != "baseline" {
		t.Fatalf("Load(migrations.FS)[0] = %+v, want version 1 baseline", got)
	}
	for _, m := range got {
		if strings.TrimSpace(m.Up) == "" || strings.TrimSpace(m.Down) == "" {
			t.Errorf("migration %d has an empty up or down", m.Version)
		}
	}
	if !strings.Contains(got[0].Up, "CREATE EXTENSION IF NOT EXISTS vector") {
		t.Errorf("baseline up does not create the vector extension")
	}
	if !strings.Contains(got[0].Down, "DROP EXTENSION vector") {
		t.Errorf("baseline down does not drop the vector extension")
	}
}

func TestLoad(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	tests := []struct {
		name    string
		fsys    fs.FS
		want    []int64
		wantErr string
	}{
		{
			name: "sorted by version",
			fsys: fstest.MapFS{
				"0002_b.up.sql": file("b"), "0002_b.down.sql": file("-b"),
				"0001_a.up.sql": file("a"), "0001_a.down.sql": file("-a"),
				"README.md": file("ignored"),
			},
			want: []int64{1, 2},
		},
		{
			name:    "missing down",
			fsys:    fstest.MapFS{"0001_a.up.sql": file("a")},
			wantErr: "version 1 (a) has no down file",
		},
		{
			name:    "missing up",
			fsys:    fstest.MapFS{"0001_a.down.sql": file("a")},
			wantErr: "version 1 (a) has no up file",
		},
		{
			name: "two names for one version",
			fsys: fstest.MapFS{
				"0001_a.up.sql": file("a"), "0001_a.down.sql": file("a"),
				"0001_b.up.sql": file("b"), "0001_b.down.sql": file("b"),
			},
			wantErr: "version 1 has two names",
		},
		{
			name: "same version spelled twice",
			fsys: fstest.MapFS{
				"0001_a.up.sql": file("a"), "1_a.up.sql": file("a"), "0001_a.down.sql": file("a"),
			},
			wantErr: "version 1 has two up files",
		},
		{
			name:    "malformed sql file name",
			fsys:    fstest.MapFS{"init.sql": file("x")},
			wantErr: `migration file "init.sql"`,
		},
		{
			name:    "version zero",
			fsys:    fstest.MapFS{"0000_a.up.sql": file("a"), "0000_a.down.sql": file("a")},
			wantErr: "invalid version",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := migrate.Load(tc.fsys)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			var versions []int64
			for _, m := range got {
				versions = append(versions, m.Version)
			}
			if diff := cmp.Diff(tc.want, versions); diff != "" {
				t.Errorf("Load() versions mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// baselineFS holds only migration 0001. The Node-parity tests are pinned to it: later
// versions intentionally add to the schema the Node migrations 0000-0020 produce.
func baselineFS(t *testing.T) fs.FS { return embeddedFS(t, "0001_baseline") }

// embeddedFS copies the named embedded migrations (without .up.sql / .down.sql) into a MapFS, so
// a test can stop at a version whatever is added after it.
func embeddedFS(t *testing.T, names ...string) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	for _, base := range names {
		for _, name := range []string{base + ".up.sql", base + ".down.sql"} {
			body, err := fs.ReadFile(migrations.FS, name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			out[name] = &fstest.MapFile{Data: body}
		}
	}
	return out
}

func newMigrator(t *testing.T, db *sql.DB, fsys fs.FS) *migrate.Migrator {
	t.Helper()
	m, err := migrate.New(db, fsys, discard())
	if err != nil {
		t.Fatalf("migrate.New() error = %v", err)
	}
	return m
}

func sqlDBOf(t *testing.T, d *pgtest.Database) *sql.DB {
	t.Helper()
	sqlDB, err := d.DB.DB()
	if err != nil {
		t.Fatalf("DB.DB() error = %v", err)
	}
	return sqlDB
}

func TestUp_MatchesNodeSchema(t *testing.T) {
	want := referenceSnapshot(t)
	d := pgtest.Empty(t)
	m := newMigrator(t, sqlDBOf(t, d), baselineFS(t))

	n, err := m.Up(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(want, catalog(t, sqlDBOf(t, d))); diff != "" {
		t.Errorf("schema after Up differs from Node migrations 0000-0020 (-node +go):\n%s", diff)
	}
	var tables int
	if err := sqlDBOf(t, d).QueryRow(`SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 46 {
		t.Errorf("application tables = %d, want 46", tables)
	}
}

func TestUp_Idempotent(t *testing.T) {
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	m := newMigrator(t, db, migrations.FS)
	if _, err := m.Up(t.Context()); err != nil {
		t.Fatalf("first Up() error = %v", err)
	}
	before := catalog(t, db)
	statusBefore, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	n, err := m.Up(t.Context())
	if err != nil || n != 0 {
		t.Fatalf("second Up() = %d, %v, want 0, nil", n, err)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema changed by second Up (-before +after):\n%s", diff)
	}
	statusAfter, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(statusBefore, statusAfter); diff != "" {
		t.Errorf("status changed by second Up (-before +after):\n%s", diff)
	}
}

func TestUp_ConcurrentMigratorsApplyOnce(t *testing.T) {
	all, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatalf("Load(migrations.FS) error = %v", err)
	}
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for range 3 {
		wg.Go(func() {
			m, err := migrate.New(db, migrations.FS, discard())
			if err != nil {
				t.Errorf("migrate.New() error = %v", err)
				return
			}
			n, err := m.Up(context.Background())
			if err != nil {
				t.Errorf("Up() error = %v", err)
				return
			}
			mu.Lock()
			total += n
			mu.Unlock()
		})
	}
	wg.Wait()
	if total != len(all) {
		t.Errorf("versions applied across 3 concurrent Up calls = %d, want %d", total, len(all))
	}
}

func TestDown_ThenUpRestoresSchema(t *testing.T) {
	want := referenceSnapshot(t)
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	m := newMigrator(t, db, baselineFS(t))
	if _, err := m.Up(t.Context()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 1 {
		t.Fatalf("Down() = %d, %v, %v, want 1, true, nil", v, ok, err)
	}
	var tables []string
	rows, err := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tables) != 0 {
		t.Errorf("tables left after Down = %v, want none", tables)
	}
	var ext int
	if err := db.QueryRow(`SELECT count(*) FROM pg_extension WHERE extname = 'vector'`).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if ext != 0 {
		t.Errorf("vector extension still installed after Down")
	}
	status, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 1 || status[0].Applied {
		t.Errorf("Status() after Down = %+v, want version 1 not applied", status)
	}
	if _, ok, err := m.Down(t.Context()); err != nil || ok {
		t.Errorf("second Down() = ok %v, err %v, want false, nil", ok, err)
	}

	if _, err := m.Up(t.Context()); err != nil {
		t.Fatalf("Up() after Down error = %v", err)
	}
	if diff := cmp.Diff(want, catalog(t, db)); diff != "" {
		t.Errorf("schema after down->up differs from Node (-node +go):\n%s", diff)
	}
}

// TestMigration0002_PRMergeState: Up adds merged_at, closed_at and draft to pull_requests,
// one Down removes exactly those three columns (the rest equals the baseline), Up restores them.
func TestMigration0002_PRMergeState(t *testing.T) {
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)

	base := newMigrator(t, db, baselineFS(t))
	if _, err := base.Up(t.Context()); err != nil {
		t.Fatalf("Up() baseline error = %v", err)
	}
	baseline := catalog(t, db)

	m := newMigrator(t, db, embeddedFS(t, "0001_baseline", "0002_pr_merge_state"))
	n, err := m.Up(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 2 only)", n, err)
	}
	afterUp := catalog(t, db)
	added := onlyIn(afterUp, baseline)
	want := []string{
		"column pull_requests.closed_at #19 timestamp with time zone default=<none> generated= identity=",
		"column pull_requests.draft #20 boolean NOT NULL default=false generated= identity=",
		"column pull_requests.merged_at #18 timestamp with time zone default=<none> generated= identity=",
	}
	if diff := cmp.Diff(want, added); diff != "" {
		t.Errorf("objects added by 0002 mismatch (-want +got):\n%s", diff)
	}
	if removed := onlyIn(baseline, afterUp); len(removed) != 0 {
		t.Errorf("objects removed by 0002 = %v, want none", removed)
	}

	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 2 {
		t.Fatalf("Down() = %d, %v, %v, want 2, true, nil", v, ok, err)
	}
	if diff := cmp.Diff(baseline, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0002 differs from baseline (-baseline +got):\n%s", diff)
	}

	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
}

// TestMigration0003_SpendQueryIndexes: Up adds exactly the two covering indexes of the month-spend
// query, Down drops exactly those two (the rest equals version 2), Up restores them.
func TestMigration0003_SpendQueryIndexes(t *testing.T) {
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)

	upTo2 := newMigrator(t, db, embeddedFS(t, "0001_baseline", "0002_pr_merge_state"))
	if _, err := upTo2.Up(t.Context()); err != nil {
		t.Fatalf("Up() versions 1-2 error = %v", err)
	}
	before := catalog(t, db)

	upTo3 := embeddedFS(t, "0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes")
	m := newMigrator(t, db, upTo3)
	all, err := migrate.Load(upTo3)
	if err != nil {
		t.Fatalf("Load(versions 1-3) error = %v", err)
	}
	if len(all) != 3 || all[2].Version != 3 || all[2].Name != "spend_query_indexes" {
		t.Fatalf("Load(versions 1-3) = %d migrations, last %+v, want 3 ending in version 3 spend_query_indexes", len(all), all[len(all)-1])
	}
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 3 only)", n, err)
	}
	afterUp := catalog(t, db)
	want := []string{
		"index agent_runs agent_runs_ws_ran_idx CREATE INDEX agent_runs_ws_ran_idx ON public.agent_runs USING btree (workspace_id, ran_at) INCLUDE (cost_usd)",
		"index eval_runs eval_runs_ran_idx CREATE INDEX eval_runs_ran_idx ON public.eval_runs USING btree (ran_at) INCLUDE (case_id, cost_usd)",
	}
	if diff := cmp.Diff(want, onlyIn(afterUp, before)); diff != "" {
		t.Errorf("objects added by 0003 mismatch (-want +got):\n%s", diff)
	}
	if removed := onlyIn(before, afterUp); len(removed) != 0 {
		t.Errorf("objects removed by 0003 = %v, want none", removed)
	}

	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 3 {
		t.Fatalf("Down() = %d, %v, %v, want 3, true, nil", v, ok, err)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0003 differs from version 2 (-v2 +got):\n%s", diff)
	}

	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
}

// TestMigration0004_ResetHistoryIndexes: Up adds exactly the index on ci_runs.agent_run_id (the
// foreign key a history reset scans once per deleted agent run, Settings B D27), Down drops exactly
// that index (the rest equals version 3), Up restores it.
func TestMigration0004_ResetHistoryIndexes(t *testing.T) {
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)

	upTo3 := newMigrator(t, db, embeddedFS(t, "0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes"))
	if _, err := upTo3.Up(t.Context()); err != nil {
		t.Fatalf("Up() versions 1-3 error = %v", err)
	}
	before := catalog(t, db)

	upTo4 := embeddedFS(t, "0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes", "0004_reset_history_indexes")
	m := newMigrator(t, db, upTo4)
	all, err := migrate.Load(upTo4)
	if err != nil {
		t.Fatalf("Load(versions 1-4) error = %v", err)
	}
	if len(all) != 4 || all[3].Version != 4 || all[3].Name != "reset_history_indexes" {
		t.Fatalf("Load(versions 1-4) = %d migrations, last %+v, want 4 ending in version 4 reset_history_indexes", len(all), all[len(all)-1])
	}
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 4 only)", n, err)
	}
	afterUp := catalog(t, db)
	want := []string{
		"index ci_runs ci_runs_agent_run_idx CREATE INDEX ci_runs_agent_run_idx ON public.ci_runs USING btree (agent_run_id)",
	}
	if diff := cmp.Diff(want, onlyIn(afterUp, before)); diff != "" {
		t.Errorf("objects added by 0004 mismatch (-want +got):\n%s", diff)
	}
	if removed := onlyIn(before, afterUp); len(removed) != 0 {
		t.Errorf("objects removed by 0004 = %v, want none", removed)
	}

	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 4 {
		t.Fatalf("Down() = %d, %v, %v, want 4, true, nil", v, ok, err)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0004 differs from version 3 (-v3 +got):\n%s", diff)
	}

	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
}

// ledgerFixture is the pre-ledger data of TestMigration0005_CostLedger (database at version 4):
// every cost source, the mirror, NULL and malformed costs, boundary times, two months and two
// workspaces. Ids are fixed so that expectations can name them.
const ledgerFixture = `
INSERT INTO workspaces (id, name) VALUES
  ('00000000-0000-0000-0000-0000000000a1', 'w1'), ('00000000-0000-0000-0000-0000000000a2', 'w2');
INSERT INTO repos (id, workspace_id, owner, name, full_name) VALUES
  ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-0000000000a1', 'o', 'r1', 'o/r1'),
  ('00000000-0000-0000-0000-0000000000b2', '00000000-0000-0000-0000-0000000000a1', 'o', 'r2', 'o/r2');
INSERT INTO pull_requests (id, workspace_id, repo_id, number, title, author, branch, base, head_sha) VALUES
  ('00000000-0000-0000-0000-0000000000c1', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 1, 't', 'a', 'b', 'main', 's'),
  ('00000000-0000-0000-0000-0000000000c2', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 2, 't', 'a', 'b', 'main', 's'),
  ('00000000-0000-0000-0000-0000000000c3', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 3, 't', 'a', 'b', 'main', 's'),
  ('00000000-0000-0000-0000-0000000000c4', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 4, 't', 'a', 'b', 'main', 's'),
  ('00000000-0000-0000-0000-0000000000c5', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 5, 't', 'a', 'b', 'main', 's'),
  ('00000000-0000-0000-0000-0000000000c6', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 6, 't', 'a', 'b', 'main', 's');
-- agent_runs: d1 local (2026-09-30 23:30 UTC, written with an offset), d2 NULL cost, d3 negative,
-- d4 CI with a ci_runs row, d5 CI without one, d6 other workspace.
INSERT INTO agent_runs (id, workspace_id, ran_at, provider, model, tokens_in, tokens_out, source, cost_usd) VALUES
  ('00000000-0000-0000-0000-0000000000d1', '00000000-0000-0000-0000-0000000000a1', '2026-10-01 01:30:00+02', 'openai', 'gpt', 100, 50, 'local', 0.123456),
  ('00000000-0000-0000-0000-0000000000d2', '00000000-0000-0000-0000-0000000000a1', '2026-10-02 10:00:00+00', 'openai', 'gpt', 1, 1, 'local', NULL),
  ('00000000-0000-0000-0000-0000000000d3', '00000000-0000-0000-0000-0000000000a1', '2026-10-02 10:00:00+00', 'openai', 'gpt', 1, 1, 'local', -1),
  ('00000000-0000-0000-0000-0000000000d4', '00000000-0000-0000-0000-0000000000a1', '2026-10-05 08:00:00+00', 'openrouter', NULL, NULL, NULL, 'ci', 0.5),
  ('00000000-0000-0000-0000-0000000000d5', '00000000-0000-0000-0000-0000000000a1', '2026-10-06 08:00:00+00', 'openrouter', NULL, NULL, NULL, 'ci', 0.25),
  ('00000000-0000-0000-0000-0000000000d6', '00000000-0000-0000-0000-0000000000a2', '2026-10-01 00:00:00+00', 'openai', 'gpt', 3, 4, 'local', 1);
-- The mirror of d4 (not counted) and a second ci_runs row without a link.
INSERT INTO ci_runs (id, agent_run_id, github_run_id, run_attempt, ran_at, cost_usd, source) VALUES
  ('00000000-0000-0000-0000-0000000000e1', '00000000-0000-0000-0000-0000000000d4', 111, 2, '2026-10-05 08:00:00+00', 0.5, 'ci'),
  ('00000000-0000-0000-0000-0000000000e2', NULL, 222, 1, '2026-10-07 08:00:00+00', 9, 'ci');
INSERT INTO eval_cases (id, workspace_id, owner_kind, owner_id, name) VALUES
  ('00000000-0000-0000-0000-0000000000f1', '00000000-0000-0000-0000-0000000000a1', 'agent', '00000000-0000-0000-0000-0000000000f9', 'c');
INSERT INTO eval_runs (id, case_id, ran_at, cost_usd) VALUES
  ('00000000-0000-0000-0000-000000000101', '00000000-0000-0000-0000-0000000000f1', '2026-10-08 00:00:00+00', 0.000001),
  ('00000000-0000-0000-0000-000000000102', '00000000-0000-0000-0000-0000000000f1', '2026-10-08 00:00:00+00', NULL);
-- convention_scans (double precision): 0.0000005 rounds half away from zero to 1 micro-USD;
-- NaN, Infinity and 1e12 are malformed.
INSERT INTO convention_scans (id, workspace_id, repo_id, status, model, tokens_in, tokens_out, started_at, cost_usd) VALUES
  ('00000000-0000-0000-0000-000000000111', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 'done', 'm', 7, 8, '2026-10-09 00:00:00+00', 0.0000005),
  ('00000000-0000-0000-0000-000000000112', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 'done', 'm', 1, 1, '2026-10-09 00:00:00+00', 'NaN'),
  ('00000000-0000-0000-0000-000000000113', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 'done', 'm', 1, 1, '2026-10-09 00:00:00+00', 'Infinity'),
  ('00000000-0000-0000-0000-000000000114', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 'done', 'm', 1, 1, '2026-10-09 00:00:00+00', 1e12);
-- pr_intent: c1 counted, c2 without a time (malformed).
INSERT INTO pr_intent (pr_id, intent, provider, model, tokens_in, tokens_out, cost_usd, derived_at) VALUES
  ('00000000-0000-0000-0000-0000000000c1', 'i', 'openai', 'gpt', 5, 6, 2.5, '2026-10-10 00:00:00+00'),
  ('00000000-0000-0000-0000-0000000000c2', 'i', 'openai', 'gpt', 5, 6, 3, NULL);
-- pr_brief: c3 counted, c4 cost is a string, c5 time is garbage, c6 an array (no cost).
INSERT INTO pr_brief (pr_id, json) VALUES
  ('00000000-0000-0000-0000-0000000000c3', '{"cost_usd": 0.75, "generated_at": "2026-10-11T12:00:00Z"}'),
  ('00000000-0000-0000-0000-0000000000c4', '{"cost_usd": "x", "generated_at": "2026-10-11T12:00:00Z"}'),
  ('00000000-0000-0000-0000-0000000000c5', '{"cost_usd": 0.1, "generated_at": "garbage"}'),
  ('00000000-0000-0000-0000-0000000000c6', '[1, 2]');
-- onboarding: b1 counted, b2 cost is a string.
INSERT INTO onboarding (repo_id, json, generated_at) VALUES
  ('00000000-0000-0000-0000-0000000000b1', '{"cost_usd": 0.3}', '2026-10-12 00:00:00+00'),
  ('00000000-0000-0000-0000-0000000000b2', '{"cost_usd": "bad"}', '2026-10-12 00:00:00+00');
`

// ledgerRows returns the entries of the ledger without their random ids, sorted by dedupe key.
func ledgerRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryStrings(t, db, `SELECT format('%s kind=%s origin=%s ws=%s at=%s prov=%s model=%s in=%s out=%s cost=%s ref=%s',
	    dedupe_key, kind, origin, w.name, to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US'),
	    provider, model, coalesce(tokens_in::text, '-'), coalesce(tokens_out::text, '-'),
	    coalesce(cost_micros::text, '-'), coalesce(ref_id::text, '-'))
	  FROM cost_ledger l JOIN workspaces w ON w.id = l.workspace_id ORDER BY dedupe_key`)
}

func totalsRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryStrings(t, db, `SELECT format('%s %s slot=%s spent=%s unpriced=%s entries=%s',
	    w.name, to_char(month, 'YYYY-MM-DD'), slot, spent_micros, unpriced_count, entry_count)
	  FROM spend_month_totals t JOIN workspaces w ON w.id = t.workspace_id ORDER BY 1`)
}

func queryStrings(t *testing.T, db *sql.DB, q string) []string {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("query failed: %v\n%s", err, q)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMigration0005_CostLedger: Up creates exactly the three ledger tables and backfills them
// from the cost records of the other contexts (FR10), Down drops exactly those tables and leaves
// the cost records alone, and Up again recreates the same entries without duplicates (EC18).
func TestMigration0005_CostLedger(t *testing.T) {
	upTo4Names := []string{"0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes", "0004_reset_history_indexes"}
	upTo5Names := append(append([]string{}, upTo4Names...), "0005_cost_ledger")

	t.Run("backfill, down, up", func(t *testing.T) {
		d := pgtest.Empty(t)
		db := sqlDBOf(t, d)

		if _, err := newMigrator(t, db, embeddedFS(t, upTo4Names...)).Up(t.Context()); err != nil {
			t.Fatalf("Up() versions 1-4 error = %v", err)
		}
		before := catalog(t, db)
		if _, err := db.ExecContext(t.Context(), ledgerFixture); err != nil {
			t.Fatalf("load fixture: %v", err)
		}
		var costRecords int
		if err := db.QueryRow(`SELECT (SELECT count(*) FROM agent_runs) + (SELECT count(*) FROM ci_runs) + (SELECT count(*) FROM pr_brief)`).Scan(&costRecords); err != nil {
			t.Fatal(err)
		}

		upTo5 := embeddedFS(t, upTo5Names...)
		all, err := migrate.Load(upTo5)
		if err != nil {
			t.Fatalf("Load(versions 1-5) error = %v", err)
		}
		if len(all) != 5 || all[4].Version != 5 || all[4].Name != "cost_ledger" {
			t.Fatalf("Load(versions 1-5) = %d migrations, last %+v, want 5 ending in version 5 cost_ledger", len(all), all[len(all)-1])
		}
		m := newMigrator(t, db, upTo5)
		if n, err := m.Up(t.Context()); err != nil || n != 1 {
			t.Fatalf("Up() = %d, %v, want 1, nil (version 5 only)", n, err)
		}
		afterUp := catalog(t, db)
		if removed := onlyIn(before, afterUp); len(removed) != 0 {
			t.Errorf("objects removed by 0005 = %v, want none", removed)
		}
		var tables, indexes []string
		for _, line := range onlyIn(afterUp, before) {
			switch {
			case strings.HasPrefix(line, "table "):
				tables = append(tables, line)
			case strings.HasPrefix(line, "index "):
				indexes = append(indexes, line)
			}
			if !strings.Contains(line, " cost_ledger") && !strings.Contains(line, " spend_month_totals") &&
				!strings.Contains(line, "column cost_ledger") && !strings.Contains(line, "column spend_month_totals") {
				t.Errorf("0005 added %q, which names none of its tables", line)
			}
		}
		if diff := cmp.Diff([]string{"table cost_ledger", "table cost_ledger_backfill", "table spend_month_totals"}, tables); diff != "" {
			t.Errorf("tables added by 0005 mismatch (-want +got):\n%s", diff)
		}
		wantIndexes := []string{
			"index cost_ledger cost_ledger_dedupe_key_key CREATE UNIQUE INDEX cost_ledger_dedupe_key_key ON public.cost_ledger USING btree (dedupe_key)",
			"index cost_ledger cost_ledger_pkey CREATE UNIQUE INDEX cost_ledger_pkey ON public.cost_ledger USING btree (id)",
			"index cost_ledger cost_ledger_ws_occurred_idx CREATE INDEX cost_ledger_ws_occurred_idx ON public.cost_ledger USING btree (workspace_id, occurred_at)",
			"index cost_ledger_backfill cost_ledger_backfill_pkey CREATE UNIQUE INDEX cost_ledger_backfill_pkey ON public.cost_ledger_backfill USING btree (id)",
			"index spend_month_totals spend_month_totals_pkey CREATE UNIQUE INDEX spend_month_totals_pkey ON public.spend_month_totals USING btree (workspace_id, month, slot)",
		}
		if diff := cmp.Diff(wantIndexes, indexes); diff != "" {
			t.Errorf("indexes added by 0005 mismatch (-want +got):\n%s", diff)
		}

		// The backfill: one entry per counted record with a usable cost. d2/eval 102 have no cost
		// (EC17), d3, the NaN/Infinity/1e12 scans, c2 (no time), c4, c5 and b2 are malformed, the
		// mirror ci_runs rows and the array brief c6 carry nothing.
		wantEntries := []string{
			"backfill:convention_scan:00000000-0000-0000-0000-000000000111 kind=conventions_scan origin=backfill ws=w1 at=2026-10-09T00:00:00.000000 prov= model=m in=7 out=8 cost=1 ref=00000000-0000-0000-0000-000000000111",
			"backfill:eval_run:00000000-0000-0000-0000-000000000101 kind=eval_case origin=backfill ws=w1 at=2026-10-08T00:00:00.000000 prov= model= in=- out=- cost=1 ref=00000000-0000-0000-0000-0000000000f1",
			"backfill:agent_run:00000000-0000-0000-0000-0000000000d1 kind=review_run origin=backfill ws=w1 at=2026-09-30T23:30:00.000000 prov=openai model=gpt in=100 out=50 cost=123456 ref=00000000-0000-0000-0000-0000000000d1",
			"backfill:agent_run:00000000-0000-0000-0000-0000000000d5 kind=ci_run origin=backfill ws=w1 at=2026-10-06T08:00:00.000000 prov=openrouter model= in=- out=- cost=250000 ref=00000000-0000-0000-0000-0000000000d5",
			"backfill:agent_run:00000000-0000-0000-0000-0000000000d6 kind=review_run origin=backfill ws=w2 at=2026-10-01T00:00:00.000000 prov=openai model=gpt in=3 out=4 cost=1000000 ref=00000000-0000-0000-0000-0000000000d6",
			"backfill:onboarding:00000000-0000-0000-0000-0000000000b1 kind=onboarding origin=backfill ws=w1 at=2026-10-12T00:00:00.000000 prov= model= in=- out=- cost=300000 ref=00000000-0000-0000-0000-0000000000b1",
			"backfill:pr_brief:00000000-0000-0000-0000-0000000000c3 kind=brief origin=backfill ws=w1 at=2026-10-11T12:00:00.000000 prov= model= in=- out=- cost=750000 ref=00000000-0000-0000-0000-0000000000c3",
			"backfill:pr_intent:00000000-0000-0000-0000-0000000000c1 kind=review_intent origin=backfill ws=w1 at=2026-10-10T00:00:00.000000 prov=openai model=gpt in=5 out=6 cost=2500000 ref=00000000-0000-0000-0000-0000000000c1",
			"ci:111:2 kind=ci_run origin=backfill ws=w1 at=2026-10-05T08:00:00.000000 prov=openrouter model= in=- out=- cost=500000 ref=00000000-0000-0000-0000-0000000000d4",
		}
		sort.Strings(wantEntries)
		entries := ledgerRows(t, db)
		if diff := cmp.Diff(wantEntries, entries); diff != "" {
			t.Errorf("backfilled entries mismatch (-want +got):\n%s", diff)
		}
		wantTotals := []string{
			"w1 2026-09-01 slot=0 spent=123456 unpriced=0 entries=1",
			"w1 2026-10-01 slot=0 spent=4300002 unpriced=0 entries=7",
			"w2 2026-10-01 slot=0 spent=1000000 unpriced=0 entries=1",
		}
		totals := totalsRows(t, db)
		if diff := cmp.Diff(wantTotals, totals); diff != "" {
			t.Errorf("stored totals mismatch (-want +got):\n%s", diff)
		}
		wantSummary := `{"brief": 1, "ci_run": 2, "eval_case": 1, "onboarding": 1, "review_run": 2, "review_intent": 1, "intent_refresh": 0, "conventions_scan": 1} skipped=8 logged=false`
		summary := queryStrings(t, db, `SELECT created::text || ' skipped=' || skipped || ' logged=' || (logged_at IS NOT NULL) FROM cost_ledger_backfill`)
		if len(summary) != 1 {
			t.Fatalf("cost_ledger_backfill has %d rows, want 1", len(summary))
		}
		var got, want map[string]any
		gotJSON, _, _ := strings.Cut(summary[0], " skipped=")
		wantJSON, _, _ := strings.Cut(wantSummary, " skipped=")
		if err := json.Unmarshal([]byte(gotJSON), &got); err != nil {
			t.Fatalf("summary created %q: %v", gotJSON, err)
		}
		if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("backfill summary created mismatch (-want +got):\n%s", diff)
		}
		if _, tail, _ := strings.Cut(summary[0], " skipped="); tail != "8 logged=false" {
			t.Errorf("backfill summary tail = %q, want %q", tail, "8 logged=false")
		}

		// The table accepts what the app writes and refuses what it must not.
		const ws = "00000000-0000-0000-0000-0000000000a1"
		ok := `INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, dedupe_key) VALUES ($1, now(), $2, 'live', $3)`
		if _, err := db.Exec(ok, ws, "ci_run", "live:1"); err != nil {
			t.Errorf("insert with empty provider/model, no tokens, cost or ref error = %v, want nil", err)
		}
		for name, q := range map[string]string{
			"duplicate dedupe key": `INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, dedupe_key) VALUES ('` + ws + `', now(), 'brief', 'live', 'ci:111:2')`,
			"unknown kind":         `INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, dedupe_key) VALUES ('` + ws + `', now(), 'x', 'live', 'live:2')`,
			"unknown origin":       `INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, dedupe_key) VALUES ('` + ws + `', now(), 'brief', 'x', 'live:3')`,
			"negative cost":        `INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, dedupe_key, cost_micros) VALUES ('` + ws + `', now(), 'brief', 'live', 'live:4', -1)`,
			"negative tokens":      `INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, dedupe_key, tokens_in) VALUES ('` + ws + `', now(), 'brief', 'live', 'live:5', -1)`,
			"unknown workspace":    `INSERT INTO cost_ledger (workspace_id, occurred_at, kind, origin, dedupe_key) VALUES ('00000000-0000-0000-0000-0000000000ff', now(), 'brief', 'live', 'live:6')`,
			"slot out of range":    `INSERT INTO spend_month_totals (workspace_id, month, slot) VALUES ('` + ws + `', '2026-10-01', 8)`,
			"month not the first":  `INSERT INTO spend_month_totals (workspace_id, month, slot) VALUES ('` + ws + `', '2026-10-02', 0)`,
			"second summary row":   `INSERT INTO cost_ledger_backfill (id, created, skipped, duration_ms) VALUES (2, '{}', 0, 0)`,
		} {
			if _, err := db.Exec(q); err == nil {
				t.Errorf("%s: insert error = nil, want a constraint violation", name)
			}
		}
		if _, err := db.Exec(`DELETE FROM cost_ledger WHERE dedupe_key = 'live:1'`); err != nil {
			t.Fatal(err)
		}

		v, okDown, err := m.Down(t.Context())
		if err != nil || !okDown || v != 5 {
			t.Fatalf("Down() = %d, %v, %v, want 5, true, nil", v, okDown, err)
		}
		if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
			t.Errorf("schema after Down of 0005 differs from version 4 (-v4 +got):\n%s", diff)
		}
		var after int
		if err := db.QueryRow(`SELECT (SELECT count(*) FROM agent_runs) + (SELECT count(*) FROM ci_runs) + (SELECT count(*) FROM pr_brief)`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != costRecords {
			t.Errorf("cost records after Down = %d, want %d (unchanged)", after, costRecords)
		}

		if n, err := m.Up(t.Context()); err != nil || n != 1 {
			t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
		}
		if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
			t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
		}
		if diff := cmp.Diff(entries, ledgerRows(t, db)); diff != "" {
			t.Errorf("entries after Down->Up differ from the first Up (-first +got):\n%s", diff)
		}
		if diff := cmp.Diff(totals, totalsRows(t, db)); diff != "" {
			t.Errorf("totals after Down->Up differ from the first Up (-first +got):\n%s", diff)
		}
		var distinct, rows int
		if err := db.QueryRow(`SELECT count(DISTINCT dedupe_key), count(*) FROM cost_ledger`).Scan(&distinct, &rows); err != nil {
			t.Fatal(err)
		}
		if distinct != rows || rows != len(wantEntries) {
			t.Errorf("entries after Down->Up = %d rows, %d distinct keys, want %d, %d", rows, distinct, len(wantEntries), len(wantEntries))
		}
	})

	// The seed runs after the migrations (D18): on a new database the backfill finds nothing.
	t.Run("fresh database backfills nothing", func(t *testing.T) {
		d := pgtest.Empty(t)
		db := sqlDBOf(t, d)
		if _, err := newMigrator(t, db, embeddedFS(t, upTo5Names...)).Up(t.Context()); err != nil {
			t.Fatalf("Up() versions 1-5 error = %v", err)
		}
		if got := ledgerRows(t, db); len(got) != 0 {
			t.Errorf("entries on a fresh database = %v, want none", got)
		}
		if got := totalsRows(t, db); len(got) != 0 {
			t.Errorf("totals on a fresh database = %v, want none", got)
		}
		got := queryStrings(t, db, `SELECT jsonb_object_agg(k, v)::text || ' skipped=' || min(skipped) FROM cost_ledger_backfill, jsonb_each_text(created) AS e(k, v)`)
		want := []string{`{"brief": "0", "ci_run": "0", "eval_case": "0", "onboarding": "0", "review_run": "0", "review_intent": "0", "intent_refresh": "0", "conventions_scan": "0"} skipped=0`}
		if len(got) != 1 {
			t.Fatalf("summary = %v, want one row", got)
		}
		var g, w map[string]any
		gj, gt, _ := strings.Cut(got[0], " skipped=")
		wj, wt, _ := strings.Cut(want[0], " skipped=")
		if err := json.Unmarshal([]byte(gj), &g); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(wj), &w); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(w, g); diff != "" || gt != wt {
			t.Errorf("summary on a fresh database mismatch (-want +got):\n%s skipped %s, want %s", diff, gt, wt)
		}
	})
}

// onlyIn returns the entries of a that are not in b (both sorted).
func onlyIn(a, b []string) []string {
	have := make(map[string]bool, len(b))
	for _, s := range b {
		have[s] = true
	}
	var out []string
	for _, s := range a {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

func TestStatus(t *testing.T) {
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	m := newMigrator(t, db, baselineFS(t))

	got, err := m.Status(t.Context())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(got) != 1 || got[0].Version != 1 || got[0].Name != "baseline" || got[0].Applied {
		t.Errorf("Status() on empty database = %+v, want version 1 baseline not applied", got)
	}
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Errorf("Status() created schema_migrations")
	}

	if _, err := m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err = m.Status(t.Context())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(got) != 1 || !got[0].Applied || got[0].AppliedAt.IsZero() {
		t.Errorf("Status() after Up = %+v, want version 1 applied with a time", got)
	}
}

func TestUp_PreexistingReposTableLeavesDatabaseUnchanged(t *testing.T) {
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	if _, err := db.Exec(`CREATE TABLE repos (legacy int)`); err != nil {
		t.Fatal(err)
	}
	before := objects(t, db)
	m := newMigrator(t, db, migrations.FS)

	n, err := m.Up(t.Context())
	if err == nil {
		t.Fatalf("Up() = %d, nil, want an error", n)
	}
	if !strings.Contains(err.Error(), "version 1 (baseline)") {
		t.Errorf("Up() error = %q, want it to name version 1 (baseline)", err)
	}
	if diff := cmp.Diff(before, objects(t, db)); diff != "" {
		t.Errorf("database changed by failed Up (-before +after):\n%s", diff)
	}
}

func TestUp_FailingVersionRollsBackOnlyItself(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	fsys := fstest.MapFS{
		"0001_one.up.sql":   file(`CREATE TABLE one (id int)`),
		"0001_one.down.sql": file(`DROP TABLE one`),
		"0002_two.up.sql":   file(`CREATE TABLE two (id int); THIS IS NOT SQL;`),
		"0002_two.down.sql": file(`DROP TABLE two`),
	}
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	m := newMigrator(t, db, fsys)

	n, err := m.Up(t.Context())
	if err == nil || !strings.Contains(err.Error(), "version 2 (two)") {
		t.Fatalf("Up() = %d, %v, want an error naming version 2 (two)", n, err)
	}
	if n != 1 {
		t.Errorf("Up() applied = %d, want 1", n)
	}
	want := []string{"one", "schema_migrations"}
	var got []string
	rows, err := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("tables after failed version 2 mismatch (-want +got):\n%s", diff)
	}
}

func TestUp_LogsEachAppliedVersion(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	d := pgtest.Empty(t)
	m, err := migrate.New(sqlDBOf(t, d), baselineFS(t), log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log output %q is not one JSON line: %v", buf.String(), err)
	}
	if line["msg"] != "migration applied" || line["version"] != float64(1) || line["name"] != "baseline" {
		t.Errorf("log line = %v, want msg, version 1 and name baseline", line)
	}
	if _, ok := line["duration_ms"]; !ok {
		t.Errorf("log line = %v, want a duration_ms field", line)
	}
}

// referenceSnapshot migrates a scratch database with the Node Drizzle files (testdata/drizzle)
// and returns its catalog snapshot. The result is computed once per test binary.
var (
	refOnce sync.Once
	refSnap []string
	refErr  error
)

func referenceSnapshot(t *testing.T) []string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: needs Docker (skipped with -short)")
	}
	refOnce.Do(func() {
		refSnap, refErr = buildReference(t)
	})
	if refErr != nil {
		t.Fatalf("build Node reference schema: %v", refErr)
	}
	return refSnap
}

func buildReference(t *testing.T) ([]string, error) {
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join("testdata", "drizzle", "*.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) != 21 {
		t.Fatalf("testdata/drizzle has %d files, want 21 (0000-0020)", len(files))
	}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, stmt := range strings.Split(string(body), "--> statement-breakpoint") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("apply %s: %v\n%s", f, err, stmt)
			}
		}
	}
	return catalog(t, db), nil
}

// catalogQueries each return one text column. Bookkeeping (schema_migrations) is excluded.
// Column order is compared by rank within the table, because dropped Node columns leave
// gaps in attnum.
var catalogQueries = []string{
	`SELECT 'extension ' || extname FROM pg_extension`,
	`SELECT 'table ' || tablename FROM pg_tables
	   WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`,
	`SELECT 'column ' || c.relname || '.' || a.attname
	       || ' #' || row_number() OVER (PARTITION BY c.relname ORDER BY a.attnum)
	       || ' ' || format_type(a.atttypid, a.atttypmod)
	       || CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END
	       || ' default=' || coalesce(pg_get_expr(d.adbin, d.adrelid), '<none>')
	       || ' generated=' || a.attgenerated::text || ' identity=' || a.attidentity::text
	   FROM pg_attribute a
	   JOIN pg_class c ON c.oid = a.attrelid
	   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
	   LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	   WHERE c.relkind = 'r' AND c.relname <> 'schema_migrations'
	     AND a.attnum > 0 AND NOT a.attisdropped`,
	`SELECT 'constraint ' || c.relname || ' ' || con.conname || ' ' || con.contype::text
	       || ' ' || pg_get_constraintdef(con.oid)
	   FROM pg_constraint con
	   JOIN pg_class c ON c.oid = con.conrelid
	   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
	   WHERE c.relname <> 'schema_migrations'`,
	`SELECT 'index ' || tablename || ' ' || indexname || ' ' || indexdef FROM pg_indexes
	   WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`,
	`SELECT 'trigger ' || c.relname || ' ' || pg_get_triggerdef(t.oid)
	   FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
	   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
	   WHERE NOT t.tgisinternal AND c.relname <> 'schema_migrations'`,
	`SELECT 'sequence ' || relname FROM pg_class c
	   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
	   WHERE c.relkind = 'S'`,
	`SELECT 'type ' || t.typname || ' ' || t.typtype::text FROM pg_type t
	   JOIN pg_namespace n ON n.oid = t.typnamespace AND n.nspname = 'public'
	   WHERE t.typtype IN ('e', 'd')`,
	`SELECT 'function ' || p.proname FROM pg_proc p
	   JOIN pg_namespace n ON n.oid = p.pronamespace AND n.nspname = 'public'
	   WHERE NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')`,
}

// catalog returns the sorted schema description of the public schema.
func catalog(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, q := range catalogQueries {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("catalog query failed: %v\n%s", err, q)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(out)
	return out
}

// objects lists every relation, extension and type name, to prove a failed run changed nothing.
func objects(t *testing.T, db *sql.DB) []string {
	t.Helper()
	out := catalog(t, db)
	rows, err := db.Query(`SELECT 'relation ' || relname FROM pg_class c
	   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}
