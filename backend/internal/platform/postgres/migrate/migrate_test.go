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

// autoReviewFixture is the data of TestMigration0006_AutoReviewRecords at version 5: one workspace,
// two repositories with one PR each, and a local and a CI agent run.
const autoReviewFixture = `
INSERT INTO workspaces (id, name) VALUES ('00000000-0000-0000-0000-0000000000a1', 'w1');
INSERT INTO repos (id, workspace_id, owner, name, full_name) VALUES
  ('00000000-0000-0000-0000-0000000000b1', '00000000-0000-0000-0000-0000000000a1', 'o', 'r1', 'o/r1'),
  ('00000000-0000-0000-0000-0000000000b2', '00000000-0000-0000-0000-0000000000a1', 'o', 'r2', 'o/r2');
INSERT INTO pull_requests (id, workspace_id, repo_id, number, title, author, branch, base, head_sha) VALUES
  ('00000000-0000-0000-0000-0000000000c1', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b1', 1, 't', 'a', 'b', 'main', 'sha1'),
  ('00000000-0000-0000-0000-0000000000c2', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000b2', 1, 't', 'a', 'b', 'main', 'sha1');
INSERT INTO agent_runs (id, workspace_id, pr_id, source, status) VALUES
  ('00000000-0000-0000-0000-0000000000d1', '00000000-0000-0000-0000-0000000000a1', '00000000-0000-0000-0000-0000000000c1', 'local', 'done'),
  ('00000000-0000-0000-0000-0000000000d2', '00000000-0000-0000-0000-0000000000a1', NULL, 'ci', 'done');
`

// TestMigration0006_AutoReviewRecords: Up creates exactly the record table and widens the run
// source rule to 'auto' (FR10); the table keeps one record per (PR, head SHA), goes with its PR and
// repository, and does not reference agent runs; Down drops the table, turns 'auto' runs into
// 'local' (lossy) and restores the old rule; Up again restores version 6 (AC4).
func TestMigration0006_AutoReviewRecords(t *testing.T) {
	upTo5Names := []string{"0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes", "0004_reset_history_indexes", "0005_cost_ledger"}
	upTo6Names := append(append([]string{}, upTo5Names...), "0006_auto_review_records")

	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	if _, err := newMigrator(t, db, embeddedFS(t, upTo5Names...)).Up(t.Context()); err != nil {
		t.Fatalf("Up() versions 1-5 error = %v", err)
	}
	if _, err := db.ExecContext(t.Context(), autoReviewFixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	before := catalog(t, db)
	runsBefore := queryStrings(t, db, `SELECT id || ' ' || source FROM agent_runs ORDER BY id`)

	upTo6 := embeddedFS(t, upTo6Names...)
	all, err := migrate.Load(upTo6)
	if err != nil {
		t.Fatalf("Load(versions 1-6) error = %v", err)
	}
	if len(all) != 6 || all[5].Version != 6 || all[5].Name != "auto_review_records" {
		t.Fatalf("Load(versions 1-6) = %d migrations, last %+v, want 6 ending in version 6 auto_review_records", len(all), all[len(all)-1])
	}
	m := newMigrator(t, db, upTo6)
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 6 only)", n, err)
	}
	afterUp := catalog(t, db)

	const oldRule = "constraint agent_runs agent_runs_source_chk c CHECK ((source = ANY (ARRAY['local'::text, 'ci'::text])))"
	if diff := cmp.Diff([]string{oldRule}, onlyIn(before, afterUp)); diff != "" {
		t.Errorf("objects removed by 0006 mismatch (-want +got):\n%s", diff)
	}
	var added []string
	for _, line := range onlyIn(afterUp, before) {
		if !strings.Contains(line, "auto_review_records") && !strings.Contains(line, "agent_runs_source_chk") {
			t.Errorf("0006 added %q, which names neither its table nor the source rule", line)
		}
		if strings.HasPrefix(line, "table ") || strings.HasPrefix(line, "index ") || strings.Contains(line, "agent_runs_source_chk") {
			added = append(added, line)
		}
	}
	wantAdded := []string{
		"constraint agent_runs agent_runs_source_chk c CHECK ((source = ANY (ARRAY['local'::text, 'ci'::text, 'auto'::text])))",
		"index auto_review_records auto_review_records_pkey CREATE UNIQUE INDEX auto_review_records_pkey ON public.auto_review_records USING btree (id)",
		"index auto_review_records auto_review_records_pr_head_key CREATE UNIQUE INDEX auto_review_records_pr_head_key ON public.auto_review_records USING btree (pr_id, head_sha)",
		"index auto_review_records auto_review_records_ws_decided_idx CREATE INDEX auto_review_records_ws_decided_idx ON public.auto_review_records USING btree (workspace_id, decided_at)",
		"table auto_review_records",
	}
	if diff := cmp.Diff(wantAdded, added); diff != "" {
		t.Errorf("tables, indexes and source rule added by 0006 mismatch (-want +got):\n%s", diff)
	}
	// The record table does not reference agent_runs: a history reset deletes the runs of every
	// source and must neither be blocked by the records nor delete them.
	wantFKs := []string{"auto_review_records_pr_id_pull_requests_id_fk", "auto_review_records_workspace_id_workspaces_id_fk"}
	gotFKs := queryStrings(t, db, `SELECT conname FROM pg_constraint WHERE conrelid = 'auto_review_records'::regclass AND contype = 'f' ORDER BY 1`)
	if diff := cmp.Diff(wantFKs, gotFKs); diff != "" {
		t.Errorf("foreign keys of auto_review_records mismatch (-want +got):\n%s", diff)
	}

	exec := func(q string) error {
		_, err := db.ExecContext(t.Context(), q)
		return err
	}
	const ws = "00000000-0000-0000-0000-0000000000a1"
	const pr1, pr2 = "00000000-0000-0000-0000-0000000000c1", "00000000-0000-0000-0000-0000000000c2"
	record := func(pr, sha, event, outcome, runs string) string {
		return `INSERT INTO auto_review_records (workspace_id, pr_id, head_sha, event, outcome, runs_started, decided_at)
		  VALUES ('` + ws + `', '` + pr + `', '` + sha + `', '` + event + `', '` + outcome + `', ` + runs + `, '2026-10-25 12:00:00+00')`
	}

	if err := exec(`INSERT INTO agent_runs (id, workspace_id, pr_id, source, status) VALUES
	  ('00000000-0000-0000-0000-0000000000d3', '` + ws + `', '` + pr1 + `', 'auto', 'running')`); err != nil {
		t.Fatalf("insert an auto run: %v", err)
	}
	if err := exec(`INSERT INTO agent_runs (workspace_id, source, status) VALUES ('` + ws + `', 'bogus', 'done')`); err == nil {
		t.Errorf("inserting source 'bogus' succeeded, want the source rule to reject it")
	}
	if err := exec(record(pr1, "sha1", "new_pr", "started", "2")); err != nil {
		t.Fatalf("insert a record: %v", err)
	}
	if err := exec(record(pr1, "sha1", "new_commits", "failed", "0")); err == nil || !strings.Contains(err.Error(), "auto_review_records_pr_head_key") {
		t.Errorf("second record for (PR, head SHA) error = %v, want a violation of auto_review_records_pr_head_key", err)
	}
	if err := exec(record(pr1, "sha2", "new_commits", "skipped_spend_cap", "0")); err != nil {
		t.Errorf("record for another head of the same PR: %v", err)
	}
	if err := exec(record(pr2, "sha1", "new_pr", "skipped_trigger_off", "0")); err != nil {
		t.Errorf("record for the same head of another PR: %v", err)
	}
	rejected := map[string]string{
		"unknown event":    record(pr2, "x1", "other", "started", "0"),
		"unknown outcome":  record(pr2, "x2", "new_pr", "done", "0"),
		"negative runs":    record(pr2, "x3", "new_pr", "started", "-1"),
		"unknown PR":       record("00000000-0000-0000-0000-0000000000ff", "x4", "new_pr", "started", "0"),
		"missing decision": `INSERT INTO auto_review_records (workspace_id, pr_id, head_sha, event, outcome) VALUES ('` + ws + `', '` + pr2 + `', 'x5', 'new_pr', 'started')`,
	}
	for name, q := range rejected {
		if err := exec(q); err == nil {
			t.Errorf("insert with %s succeeded, want a constraint to reject it", name)
		}
	}
	for _, outcome := range []string{"started", "skipped_trigger_off", "skipped_already_reviewed", "skipped_no_agents", "skipped_spend_cap", "failed"} {
		if err := exec(record(pr1, "o-"+outcome, "new_commits", outcome, "0")); err != nil {
			t.Errorf("record with outcome %q: %v", outcome, err)
		}
	}
	if err := exec(`DELETE FROM auto_review_records WHERE head_sha LIKE 'o-%'`); err != nil {
		t.Fatal(err)
	}

	// A history reset deletes the runs of every source; the records stay.
	if err := exec(`DELETE FROM agent_runs`); err != nil {
		t.Fatalf("delete all agent runs: %v", err)
	}
	if got := queryStrings(t, db, `SELECT count(*)::text FROM auto_review_records`); !cmp.Equal(got, []string{"3"}) {
		t.Errorf("records after deleting every agent run = %v, want 3", got)
	}
	// Deleting a repository removes its PRs' records.
	if err := exec(`DELETE FROM repos WHERE id = '00000000-0000-0000-0000-0000000000b2'`); err != nil {
		t.Fatalf("delete repo: %v", err)
	}
	wantRecords := []string{pr1 + " sha1", pr1 + " sha2"}
	if diff := cmp.Diff(wantRecords, queryStrings(t, db, `SELECT pr_id || ' ' || head_sha FROM auto_review_records ORDER BY head_sha`)); diff != "" {
		t.Errorf("records after deleting the repository of PR 2 mismatch (-want +got):\n%s", diff)
	}

	// Down: the auto run becomes local, nothing else about the runs changes.
	if err := exec(`INSERT INTO agent_runs (id, workspace_id, pr_id, source, status) VALUES
	  ('00000000-0000-0000-0000-0000000000d1', '` + ws + `', '` + pr1 + `', 'local', 'done'),
	  ('00000000-0000-0000-0000-0000000000d2', '` + ws + `', NULL, 'ci', 'done'),
	  ('00000000-0000-0000-0000-0000000000d3', '` + ws + `', '` + pr1 + `', 'auto', 'done')`); err != nil {
		t.Fatalf("re-insert runs: %v", err)
	}
	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 6 {
		t.Fatalf("Down() = %d, %v, %v, want 6, true, nil", v, ok, err)
	}
	wantRuns := append(append([]string{}, runsBefore...), "00000000-0000-0000-0000-0000000000d3 local")
	if diff := cmp.Diff(wantRuns, queryStrings(t, db, `SELECT id || ' ' || source FROM agent_runs ORDER BY id`)); diff != "" {
		t.Errorf("agent runs after Down mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0006 differs from version 5 (-v5 +got):\n%s", diff)
	}
	if err := exec(`UPDATE agent_runs SET source = 'auto'`); err == nil {
		t.Errorf("source 'auto' accepted after Down, want the old rule")
	}

	// Up again: the same schema, an empty record table, 'auto' accepted.
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
	if got := queryStrings(t, db, `SELECT count(*)::text FROM auto_review_records`); !cmp.Equal(got, []string{"0"}) {
		t.Errorf("records after Down->Up = %v, want none", got)
	}
	if err := exec(`UPDATE agent_runs SET source = 'auto' WHERE id = '00000000-0000-0000-0000-0000000000d3'`); err != nil {
		t.Errorf("source 'auto' after Down->Up: %v", err)
	}
}

// TestMigration0007_PRHeadRepo: Up adds the nullable head_is_fork column (old rows stay NULL) and
// widens the outcome rule to 'skipped_fork' (D13); Down drops the column, turns stored
// 'skipped_fork' records into 'skipped_trigger_off' (lossy, documented) and restores the old rule;
// Up again restores version 7.
func TestMigration0007_PRHeadRepo(t *testing.T) {
	upTo6Names := []string{"0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes", "0004_reset_history_indexes", "0005_cost_ledger", "0006_auto_review_records"}
	upTo7Names := append(append([]string{}, upTo6Names...), "0007_pr_head_repo")

	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	if _, err := newMigrator(t, db, embeddedFS(t, upTo6Names...)).Up(t.Context()); err != nil {
		t.Fatalf("Up() versions 1-6 error = %v", err)
	}
	if _, err := db.ExecContext(t.Context(), autoReviewFixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	exec := func(q string) error {
		_, err := db.ExecContext(t.Context(), q)
		return err
	}
	const ws = "00000000-0000-0000-0000-0000000000a1"
	const pr1, pr2 = "00000000-0000-0000-0000-0000000000c1", "00000000-0000-0000-0000-0000000000c2"
	record := func(pr, sha, outcome string) string {
		return `INSERT INTO auto_review_records (workspace_id, pr_id, head_sha, event, outcome, runs_started, decided_at)
		  VALUES ('` + ws + `', '` + pr + `', '` + sha + `', 'new_pr', '` + outcome + `', 0, '2026-10-25 12:00:00+00')`
	}
	if err := exec(record(pr1, "x0", "skipped_fork")); err == nil {
		t.Errorf("version 6 accepts outcome 'skipped_fork', want its rule to reject it")
	}
	before := catalog(t, db)

	upTo7 := embeddedFS(t, upTo7Names...)
	all, err := migrate.Load(upTo7)
	if err != nil {
		t.Fatalf("Load(versions 1-7) error = %v", err)
	}
	if len(all) != 7 || all[6].Version != 7 || all[6].Name != "pr_head_repo" {
		t.Fatalf("Load(versions 1-7) = %d migrations, last %+v, want 7 ending in version 7 pr_head_repo", len(all), all[len(all)-1])
	}
	m := newMigrator(t, db, upTo7)
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 7 only)", n, err)
	}
	afterUp := catalog(t, db)

	const oldRule = "constraint auto_review_records auto_review_records_outcome_chk c CHECK ((outcome = ANY (ARRAY['started'::text, 'skipped_trigger_off'::text, 'skipped_already_reviewed'::text, 'skipped_no_agents'::text, 'skipped_spend_cap'::text, 'failed'::text])))"
	const newRule = "constraint auto_review_records auto_review_records_outcome_chk c CHECK ((outcome = ANY (ARRAY['started'::text, 'skipped_trigger_off'::text, 'skipped_already_reviewed'::text, 'skipped_no_agents'::text, 'skipped_spend_cap'::text, 'skipped_fork'::text, 'failed'::text])))"
	if diff := cmp.Diff([]string{oldRule}, onlyIn(before, afterUp)); diff != "" {
		t.Errorf("objects removed by 0007 mismatch (-want +got):\n%s", diff)
	}
	added := onlyIn(afterUp, before)
	if len(added) != 2 || added[1] != newRule ||
		!strings.HasPrefix(added[0], "column pull_requests.head_is_fork #") ||
		!strings.HasSuffix(added[0], " boolean default=<none> generated= identity=") {
		t.Errorf("objects added by 0007 = %q, want the nullable boolean column head_is_fork without default, and the widened outcome rule", added)
	}
	if got := queryStrings(t, db, `SELECT count(*)::text FROM pull_requests WHERE head_is_fork IS NULL`); !cmp.Equal(got, []string{"2"}) {
		t.Errorf("pull requests with an unknown head repository after Up = %v, want both fixture rows", got)
	}
	if err := exec(`UPDATE pull_requests SET head_is_fork = true WHERE id = '` + pr1 + `'`); err != nil {
		t.Fatalf("set head_is_fork: %v", err)
	}
	if err := exec(record(pr1, "sha1", "skipped_fork")); err != nil {
		t.Fatalf("record with outcome 'skipped_fork': %v", err)
	}
	if err := exec(record(pr2, "sha1", "skipped_spend_cap")); err != nil {
		t.Fatalf("record with outcome 'skipped_spend_cap': %v", err)
	}
	if err := exec(record(pr2, "sha2", "bogus")); err == nil {
		t.Errorf("unknown outcome accepted after Up, want the outcome rule to reject it")
	}

	// Down: the fork record becomes skipped_trigger_off, other records are untouched, the column goes.
	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 7 {
		t.Fatalf("Down() = %d, %v, %v, want 7, true, nil", v, ok, err)
	}
	wantRecords := []string{pr1 + " sha1 skipped_trigger_off", pr2 + " sha1 skipped_spend_cap"}
	if diff := cmp.Diff(wantRecords, queryStrings(t, db, `SELECT pr_id || ' ' || head_sha || ' ' || outcome FROM auto_review_records ORDER BY pr_id`)); diff != "" {
		t.Errorf("records after Down mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0007 differs from version 6 (-v6 +got):\n%s", diff)
	}
	if err := exec(record(pr1, "x1", "skipped_fork")); err == nil {
		t.Errorf("outcome 'skipped_fork' accepted after Down, want the old rule")
	}

	// Up again: the same schema, the converted record stays converted, every PR is unknown again.
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
	if got := queryStrings(t, db, `SELECT count(*)::text FROM pull_requests WHERE head_is_fork IS NULL`); !cmp.Equal(got, []string{"2"}) {
		t.Errorf("pull requests with an unknown head repository after Down->Up = %v, want 2", got)
	}
	if diff := cmp.Diff(wantRecords, queryStrings(t, db, `SELECT pr_id || ' ' || head_sha || ' ' || outcome FROM auto_review_records ORDER BY pr_id`)); diff != "" {
		t.Errorf("records after Down->Up mismatch (-want +got):\n%s", diff)
	}
}

// TestMigration0008_PluginOwnership: Up adds the plugin description, flips the default of enabled to
// false (existing rows keep their value), adds UNIQUE (workspace_id, name) and the nullable plugin_id
// owner (FK without cascade, partial index) to agents, skills and memory (C2); Down drops all of it,
// restores the default and is lossy for ownership and descriptions (D15/CR1); Up again restores
// version 8 (AC24, NFR6).
func TestMigration0008_PluginOwnership(t *testing.T) {
	upTo7Names := []string{"0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes", "0004_reset_history_indexes", "0005_cost_ledger", "0006_auto_review_records", "0007_pr_head_repo"}
	upTo8Names := append(append([]string{}, upTo7Names...), "0008_plugin_ownership")

	const ws1, ws2 = "00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"
	const plugin1, plugin2 = "00000000-0000-0000-0000-0000000000e1", "00000000-0000-0000-0000-0000000000e2"

	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	if _, err := newMigrator(t, db, embeddedFS(t, upTo7Names...)).Up(t.Context()); err != nil {
		t.Fatalf("Up() versions 1-7 error = %v", err)
	}
	exec := func(q string) error {
		_, err := db.ExecContext(t.Context(), q)
		return err
	}
	// Version 7 data: two workspaces, a plugin row written with the old default (enabled), a user
	// agent, skill and learning.
	if err := exec(`
INSERT INTO workspaces (id, name) VALUES ('` + ws1 + `', 'w1'), ('` + ws2 + `', 'w2');
INSERT INTO installed_plugins (id, workspace_id, name) VALUES ('` + plugin1 + `', '` + ws1 + `', 'p1');
INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES ('` + ws1 + `', 'a1', 'openai', 'm', 's');
INSERT INTO skills (workspace_id, name, description, type, source, body) VALUES ('` + ws1 + `', 's1', 'd', 'custom', 'manual', 'b');
INSERT INTO memory (workspace_id, scope, kind, content) VALUES ('` + ws1 + `', 'global', 'learning', 'c');`); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	before := catalog(t, db)

	upTo8 := embeddedFS(t, upTo8Names...)
	all, err := migrate.Load(upTo8)
	if err != nil {
		t.Fatalf("Load(versions 1-8) error = %v", err)
	}
	if len(all) != 8 || all[7].Version != 8 || all[7].Name != "plugin_ownership" {
		t.Fatalf("Load(versions 1-8) = %d migrations, last %+v, want 8 ending in version 8 plugin_ownership", len(all), all[len(all)-1])
	}
	m := newMigrator(t, db, upTo8)
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 8 only)", n, err)
	}
	afterUp := catalog(t, db)

	wantRemoved := []string{"column installed_plugins.enabled #7 boolean NOT NULL default=true generated= identity="}
	if diff := cmp.Diff(wantRemoved, onlyIn(before, afterUp)); diff != "" {
		t.Errorf("objects removed by 0008 mismatch (-want +got):\n%s", diff)
	}
	wantAdded := []string{
		"column agents.plugin_id #@AGENTS@ uuid default=<none> generated= identity=",
		"column installed_plugins.description #8 text default=<none> generated= identity=",
		"column installed_plugins.enabled #7 boolean NOT NULL default=false generated= identity=",
		"column memory.plugin_id #@MEMORY@ uuid default=<none> generated= identity=",
		"column skills.plugin_id #@SKILLS@ uuid default=<none> generated= identity=",
		"constraint agents agents_plugin_id_installed_plugins_id_fk f FOREIGN KEY (plugin_id) REFERENCES installed_plugins(id)",
		"constraint installed_plugins installed_plugins_workspace_id_name_key u UNIQUE (workspace_id, name)",
		"constraint memory memory_plugin_id_installed_plugins_id_fk f FOREIGN KEY (plugin_id) REFERENCES installed_plugins(id)",
		"constraint skills skills_plugin_id_installed_plugins_id_fk f FOREIGN KEY (plugin_id) REFERENCES installed_plugins(id)",
		"index agents agents_plugin_idx CREATE INDEX agents_plugin_idx ON public.agents USING btree (plugin_id) WHERE (plugin_id IS NOT NULL)",
		"index installed_plugins installed_plugins_workspace_id_name_key CREATE UNIQUE INDEX installed_plugins_workspace_id_name_key ON public.installed_plugins USING btree (workspace_id, name)",
		"index memory memory_plugin_idx CREATE INDEX memory_plugin_idx ON public.memory USING btree (plugin_id) WHERE (plugin_id IS NOT NULL)",
		"index skills skills_plugin_idx CREATE INDEX skills_plugin_idx ON public.skills USING btree (plugin_id) WHERE (plugin_id IS NOT NULL)",
	}
	// The owner column is the last column of its table, so its position is the table's column count.
	for _, table := range []string{"AGENTS", "MEMORY", "SKILLS"} {
		n := queryStrings(t, db, `SELECT count(*)::text FROM information_schema.columns WHERE table_schema = 'public' AND table_name = '`+strings.ToLower(table)+`'`)
		for j, line := range wantAdded {
			wantAdded[j] = strings.ReplaceAll(line, "#@"+table+"@", "#"+n[0])
		}
	}
	if diff := cmp.Diff(wantAdded, onlyIn(afterUp, before)); diff != "" {
		t.Errorf("objects added by 0008 mismatch (-want +got):\n%s", diff)
	}

	// Existing rows: the plugin keeps enabled = true, has no description; the user's rows have no owner.
	if got := queryStrings(t, db, `SELECT enabled::text || ' ' || coalesce(description, '<null>') FROM installed_plugins`); !cmp.Equal(got, []string{"true <null>"}) {
		t.Errorf("existing plugin after Up = %v, want enabled and without description", got)
	}
	if got := queryStrings(t, db, `SELECT (SELECT count(*) FROM agents WHERE plugin_id IS NULL)::text || ' ' || (SELECT count(*) FROM skills WHERE plugin_id IS NULL)::text || ' ' || (SELECT count(*) FROM memory WHERE plugin_id IS NULL)::text`); !cmp.Equal(got, []string{"1 1 1"}) {
		t.Errorf("user-owned rows after Up = %v, want one agent, skill and learning without owner", got)
	}
	// A new plugin starts disabled.
	if err := exec(`INSERT INTO installed_plugins (id, workspace_id, name) VALUES ('` + plugin2 + `', '` + ws1 + `', 'p2')`); err != nil {
		t.Fatalf("insert a plugin: %v", err)
	}
	if got := queryStrings(t, db, `SELECT enabled::text FROM installed_plugins WHERE id = '`+plugin2+`'`); !cmp.Equal(got, []string{"false"}) {
		t.Errorf("enabled of a new plugin = %v, want false", got)
	}
	// One plugin per name in a workspace; the same name in another workspace is fine.
	if err := exec(`INSERT INTO installed_plugins (workspace_id, name) VALUES ('` + ws1 + `', 'p2')`); err == nil || !strings.Contains(err.Error(), "installed_plugins_workspace_id_name_key") {
		t.Errorf("second plugin named p2 in a workspace error = %v, want a violation of installed_plugins_workspace_id_name_key", err)
	}
	if err := exec(`INSERT INTO installed_plugins (workspace_id, name) VALUES ('` + ws2 + `', 'p2')`); err != nil {
		t.Errorf("plugin of the same name in another workspace: %v", err)
	}
	// Owned rows: an unknown plugin is refused; a plugin that still owns rows cannot be deleted
	// (no cascade); after its children are gone it can.
	for table, q := range map[string]string{
		"agents": `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, plugin_id) VALUES ('` + ws1 + `', 'a2', 'openai', 'm', 's', '%s')`,
		"skills": `INSERT INTO skills (workspace_id, name, description, type, source, body, plugin_id) VALUES ('` + ws1 + `', 's2', 'd', 'custom', 'manual', 'b', '%s')`,
		"memory": `INSERT INTO memory (workspace_id, scope, kind, content, plugin_id) VALUES ('` + ws1 + `', 'global', 'learning', 'c2', '%s')`,
	} {
		if err := exec(strings.Replace(q, "%s", "00000000-0000-0000-0000-0000000000ff", 1)); err == nil {
			t.Errorf("%s row owned by an unknown plugin accepted, want the foreign key to reject it", table)
		}
		if err := exec(strings.Replace(q, "%s", plugin2, 1)); err != nil {
			t.Errorf("%s row owned by a plugin: %v", table, err)
		}
	}
	if err := exec(`DELETE FROM installed_plugins WHERE id = '` + plugin2 + `'`); err == nil {
		t.Errorf("deleting a plugin that owns an agent, a skill and a learning succeeded, want the foreign key to refuse it")
	}
	if err := exec(`DELETE FROM agents WHERE plugin_id = '` + plugin2 + `'`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`DELETE FROM skills WHERE plugin_id = '` + plugin2 + `'`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`DELETE FROM memory WHERE plugin_id = '` + plugin2 + `'`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`DELETE FROM installed_plugins WHERE id = '` + plugin2 + `'`); err != nil {
		t.Errorf("deleting a plugin whose children are gone: %v", err)
	}
	// Deleting a workspace still removes its plugins (the 0001 cascade) once the owned rows go with it.
	if err := exec(`DELETE FROM workspaces WHERE id = '` + ws2 + `'`); err != nil {
		t.Errorf("deleting a workspace with a plugin: %v", err)
	}

	// Down is lossy: owned rows stay as ordinary rows, descriptions go, the plugin keeps its state.
	if err := exec(`UPDATE installed_plugins SET description = 'x', enabled = false WHERE id = '` + plugin1 + `';
	  INSERT INTO agents (workspace_id, name, provider, model, system_prompt, plugin_id) VALUES ('` + ws1 + `', 'owned', 'openai', 'm', 's', '` + plugin1 + `')`); err != nil {
		t.Fatalf("prepare Down: %v", err)
	}
	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 8 {
		t.Fatalf("Down() = %d, %v, %v, want 8, true, nil", v, ok, err)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0008 differs from version 7 (-v7 +got):\n%s", diff)
	}
	if got := queryStrings(t, db, `SELECT name || ' ' || enabled::text FROM installed_plugins`); !cmp.Equal(got, []string{"p1 false"}) {
		t.Errorf("plugins after Down = %v, want p1 kept with its enabled value", got)
	}
	if got := queryStrings(t, db, `SELECT name FROM agents ORDER BY name`); !cmp.Equal(got, []string{"a1", "owned"}) {
		t.Errorf("agents after Down = %v, want both kept", got)
	}
	if err := exec(`INSERT INTO installed_plugins (workspace_id, name) VALUES ('` + ws1 + `', 'p3')`); err != nil {
		t.Fatal(err)
	}
	if got := queryStrings(t, db, `SELECT enabled::text FROM installed_plugins WHERE name = 'p3'`); !cmp.Equal(got, []string{"true"}) {
		t.Errorf("enabled of a plugin inserted after Down = %v, want the old default true", got)
	}
	if err := exec(`DELETE FROM installed_plugins WHERE name = 'p3'`); err != nil {
		t.Fatal(err)
	}

	// Up again: the same schema; ownership is gone, so every row is user-owned.
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
	if got := queryStrings(t, db, `SELECT count(*)::text FROM agents WHERE plugin_id IS NOT NULL`); !cmp.Equal(got, []string{"0"}) {
		t.Errorf("owned agents after Down->Up = %v, want none", got)
	}
}

// TestMigration0009_CustomProviders: Up adds custom_providers and custom_provider_models with CHECKs
// that mirror FR1 (slug, name, protocol, base URL shape, auth header, extra-header names, structured
// output, manual model ids, labels and prices), widens agents_provider_chk to `custom:<slug>`, and adds
// ci_installations.provider (existing rows get openrouter, then the default is dropped, FR37, EC35);
// Down REFUSES while an agent uses a custom provider (A11), otherwise reverses everything and leaves
// every installation, CI run and agent otherwise unchanged; Up again restores version 9 (AC48).
func TestMigration0009_CustomProviders(t *testing.T) {
	upTo8Names := []string{"0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes", "0004_reset_history_indexes", "0005_cost_ledger", "0006_auto_review_records", "0007_pr_head_repo", "0008_plugin_ownership"}
	upTo9Names := append(append([]string{}, upTo8Names...), "0009_custom_providers")

	const ws1, ws2 = "00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"
	const agent1, inst1, inst2, run1 = "00000000-0000-0000-0000-0000000000b1", "00000000-0000-0000-0000-0000000000c1", "00000000-0000-0000-0000-0000000000c2", "00000000-0000-0000-0000-0000000000d1"

	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	if _, err := newMigrator(t, db, embeddedFS(t, upTo8Names...)).Up(t.Context()); err != nil {
		t.Fatalf("Up() versions 1-8 error = %v", err)
	}
	exec := func(q string) error {
		_, err := db.ExecContext(t.Context(), q)
		return err
	}
	// Version 8 data (AC48): two Phase 6 installations of an openai agent, one ingested CI run.
	if err := exec(`
INSERT INTO workspaces (id, name) VALUES ('` + ws1 + `', 'w1'), ('` + ws2 + `', 'w2');
INSERT INTO agents (id, workspace_id, name, provider, model, system_prompt) VALUES ('` + agent1 + `', '` + ws1 + `', 'a1', 'openai', 'm', 's');
INSERT INTO ci_installations (id, agent_id, repo, target_type, slug, manifest_version) VALUES
  ('` + inst1 + `', '` + agent1 + `', 'acme/api', 'gha', 'a1', 1),
  ('` + inst2 + `', '` + agent1 + `', 'acme/web', 'cli', NULL, NULL);
INSERT INTO ci_runs (id, ci_installation_id, pr_number, status) VALUES ('` + run1 + `', '` + inst1 + `', 7, 'success');`); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	before := catalog(t, db)
	const rowsQ = `SELECT (SELECT string_agg(i.id::text || ' ' || i.agent_id::text || ' ' || i.repo || ' ' || i.target_type || ' ' || coalesce(i.slug, '-'), ',' ORDER BY i.id) FROM ci_installations i)
	  || ' | ' || (SELECT string_agg(r.id::text || ' ' || r.ci_installation_id::text || ' ' || r.pr_number::text || ' ' || r.status, ',' ORDER BY r.id) FROM ci_runs r)
	  || ' | ' || (SELECT string_agg(a.id::text || ' ' || a.provider, ',' ORDER BY a.id) FROM agents a)`
	rowsBefore := queryStrings(t, db, rowsQ)

	upTo9 := embeddedFS(t, upTo9Names...)
	all, err := migrate.Load(upTo9)
	if err != nil {
		t.Fatalf("Load(versions 1-9) error = %v", err)
	}
	if len(all) != 9 || all[8].Version != 9 || all[8].Name != "custom_providers" {
		t.Fatalf("Load(versions 1-9) = %d migrations, last %+v, want 9 ending in version 9 custom_providers", len(all), all[len(all)-1])
	}
	m := newMigrator(t, db, upTo9)
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 9 only)", n, err)
	}
	afterUp := catalog(t, db)

	wantRemoved := []string{
		"constraint agents agents_provider_chk c CHECK ((provider = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openrouter'::text])))",
	}
	if diff := cmp.Diff(wantRemoved, onlyIn(before, afterUp)); diff != "" {
		t.Errorf("objects removed by 0009 mismatch (-want +got):\n%s", diff)
	}
	wantAdded := []string{
		"column ci_installations.provider #8 text NOT NULL default=<none> generated= identity=",
		"column custom_provider_models.completion_per_m #6 numeric default=<none> generated= identity=",
		"column custom_provider_models.label #4 text default=<none> generated= identity=",
		"column custom_provider_models.model_id #3 text NOT NULL default=<none> generated= identity=",
		"column custom_provider_models.position #7 integer NOT NULL default=<none> generated= identity=",
		"column custom_provider_models.prompt_per_m #5 numeric default=<none> generated= identity=",
		"column custom_provider_models.provider_id #2 text NOT NULL default=<none> generated= identity=",
		"column custom_provider_models.workspace_id #1 uuid NOT NULL default=<none> generated= identity=",
		"column custom_providers.auth_header #7 text default=<none> generated= identity=",
		"column custom_providers.auth_mode #6 text NOT NULL default=<none> generated= identity=",
		"column custom_providers.base_url #5 text NOT NULL default=<none> generated= identity=",
		"column custom_providers.created_at #10 timestamp with time zone NOT NULL default=now() generated= identity=",
		"column custom_providers.extra_header_names #8 text[] NOT NULL default='{}'::text[] generated= identity=",
		"column custom_providers.id #2 text NOT NULL default=<none> generated= identity=",
		"column custom_providers.name #3 text NOT NULL default=<none> generated= identity=",
		"column custom_providers.protocol #4 text NOT NULL default=<none> generated= identity=",
		"column custom_providers.structured_output #9 text NOT NULL default='strict'::text generated= identity=",
		"column custom_providers.updated_at #11 timestamp with time zone NOT NULL default=now() generated= identity=",
		"column custom_providers.workspace_id #1 uuid NOT NULL default=<none> generated= identity=",
		"constraint agents agents_provider_chk c CHECK (((provider = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openrouter'::text])) OR (provider ~ '^custom:[a-z0-9-]{1,32}$'::text)))",
		"constraint ci_installations ci_installations_provider_chk c CHECK (((provider = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openrouter'::text])) OR (provider ~ '^custom:[a-z0-9-]{1,32}$'::text)))",
		"constraint custom_provider_models custom_provider_models_label_chk c CHECK (((label IS NULL) OR (char_length(label) <= 200)))",
		"constraint custom_provider_models custom_provider_models_model_id_chk c CHECK (((char_length(model_id) >= 1) AND (char_length(model_id) <= 200)))",
		"constraint custom_provider_models custom_provider_models_pkey p PRIMARY KEY (workspace_id, provider_id, model_id)",
		"constraint custom_provider_models custom_provider_models_position_chk c CHECK (((\"position\" >= 0) AND (\"position\" < 200)))",
		"constraint custom_provider_models custom_provider_models_pricing_chk c CHECK ((((prompt_per_m IS NULL) AND (completion_per_m IS NULL)) OR ((prompt_per_m IS NOT NULL) AND (completion_per_m IS NOT NULL) AND ((prompt_per_m >= (0)::numeric) AND (prompt_per_m <= (10000)::numeric)) AND ((completion_per_m >= (0)::numeric) AND (completion_per_m <= (10000)::numeric)) AND (prompt_per_m = round(prompt_per_m, 6)) AND (completion_per_m = round(completion_per_m, 6)))))",
		"constraint custom_provider_models custom_provider_models_provider_fk f FOREIGN KEY (workspace_id, provider_id) REFERENCES custom_providers(workspace_id, id) ON DELETE CASCADE",
		"constraint custom_providers custom_providers_auth_header_chk c CHECK ((((auth_mode = 'bearer'::text) AND (auth_header IS NULL)) OR ((auth_mode = 'header'::text) AND (auth_header IS NOT NULL) AND ((char_length(auth_header) >= 1) AND (char_length(auth_header) <= 64)) AND (auth_header ~ '^[!#$%&''*+.^_`|~0-9A-Za-z-]+$'::text))))",
		"constraint custom_providers custom_providers_auth_mode_chk c CHECK ((auth_mode = ANY (ARRAY['bearer'::text, 'header'::text])))",
		"constraint custom_providers custom_providers_base_url_chk c CHECK (((char_length(base_url) <= 2048) AND (base_url ~* '^https?://[^/?#@[:space:]]+(/[^?#[:space:]]*)?$'::text) AND (base_url !~ '/$'::text)))",
		"constraint custom_providers custom_providers_extra_header_names_chk c CHECK (((cardinality(extra_header_names) <= 5) AND (array_position(extra_header_names, NULL::text) IS NULL)))",
		"constraint custom_providers custom_providers_id_chk c CHECK ((id ~ '^[a-z0-9-]{1,32}$'::text))",
		"constraint custom_providers custom_providers_json_object_protocol_chk c CHECK (((structured_output <> 'json_object'::text) OR (protocol = 'openai'::text)))",
		"constraint custom_providers custom_providers_name_chk c CHECK ((((char_length(btrim(name)) >= 1) AND (char_length(btrim(name)) <= 80)) AND (name !~ '[[:cntrl:]]'::text)))",
		"constraint custom_providers custom_providers_pkey p PRIMARY KEY (workspace_id, id)",
		"constraint custom_providers custom_providers_protocol_chk c CHECK ((protocol = ANY (ARRAY['openai'::text, 'anthropic'::text])))",
		"constraint custom_providers custom_providers_structured_output_chk c CHECK ((structured_output = ANY (ARRAY['strict'::text, 'json_object'::text, 'prompt_only'::text])))",
		"constraint custom_providers custom_providers_workspace_id_workspaces_id_fk f FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE",
		"index custom_provider_models custom_provider_models_pkey CREATE UNIQUE INDEX custom_provider_models_pkey ON public.custom_provider_models USING btree (workspace_id, provider_id, model_id)",
		"index custom_providers custom_providers_pkey CREATE UNIQUE INDEX custom_providers_pkey ON public.custom_providers USING btree (workspace_id, id)",
		"table custom_provider_models",
		"table custom_providers",
	}
	if diff := cmp.Diff(wantAdded, onlyIn(afterUp, before)); diff != "" {
		t.Errorf("objects added by 0009 mismatch (-want +got):\n%s", diff)
	}

	// Existing installations get openrouter (EC35); nothing else about them, their runs or the agent changed.
	if got := queryStrings(t, db, `SELECT string_agg(provider, ',' ORDER BY id) FROM ci_installations`); !cmp.Equal(got, []string{"openrouter,openrouter"}) {
		t.Errorf("provider of the existing installations after Up = %v, want openrouter for both", got)
	}
	if diff := cmp.Diff(rowsBefore, queryStrings(t, db, rowsQ)); diff != "" {
		t.Errorf("installations, runs and agents after Up mismatch (-before +got):\n%s", diff)
	}

	// The default is dropped: an insert must name the provider (FR37); it is a built-in id or a slug.
	inst := func(provider string) string {
		return `INSERT INTO ci_installations (agent_id, repo, target_type` + provider
	}
	if err := exec(inst(`) VALUES ('` + agent1 + `', 'acme/none', 'gha')`)); err == nil {
		t.Errorf("installation without a provider accepted, want NOT NULL without default")
	}
	for i, p := range []string{"openrouter", "openai", "anthropic", "custom:synapse", "custom:a-b-1", "custom:" + strings.Repeat("a", 32)} {
		if err := exec(inst(`, provider) VALUES ('` + agent1 + `', 'acme/ok` + string(rune('a'+i)) + `', 'gha', '` + p + `')`)); err != nil {
			t.Errorf("installation with provider %q: %v", p, err)
		}
	}
	for _, p := range []string{"", "custom:", "custom:Syn", "custom:a_b", "custom:" + strings.Repeat("a", 33), "gemini", "OpenAI", "custom:ab\n"} {
		if err := exec(inst(`, provider) VALUES ('` + agent1 + `', 'acme/bad', 'gha', '` + p + `')`)); err == nil || !strings.Contains(err.Error(), "ci_installations_provider_chk") {
			t.Errorf("installation with provider %q error = %v, want a violation of ci_installations_provider_chk", p, err)
		}
	}
	if err := exec(`DELETE FROM ci_installations WHERE repo LIKE 'acme/ok%'`); err != nil {
		t.Fatal(err)
	}

	// Agents: built-ins and a well-formed custom id are accepted, everything else is refused (FR7).
	agent := func(provider string) string {
		return `INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES ('` + ws1 + `', 'x', '` + provider + `', 'm', 's')`
	}
	for _, p := range []string{"openrouter", "openai", "anthropic", "custom:synapse", "custom:0", "custom:a-b-1", "custom:" + strings.Repeat("z", 32)} {
		if err := exec(agent(p)); err != nil {
			t.Errorf("agent with provider %q: %v", p, err)
		}
	}
	for _, p := range []string{"", "custom:", "custom:Syn", "custom:a_b", "custom:" + strings.Repeat("a", 33), "synapse", "custom", "gemini", "xcustom:a", "custom:a:b", "custom:ab\n"} {
		if err := exec(agent(p)); err == nil || !strings.Contains(err.Error(), "agents_provider_chk") {
			t.Errorf("agent with provider %q error = %v, want a violation of agents_provider_chk", p, err)
		}
	}
	if err := exec(`DELETE FROM agents WHERE name = 'x'`); err != nil {
		t.Fatal(err)
	}

	// custom_providers: a valid row, then one broken rule at a time (FR1, FR3, FR4).
	valid := map[string]string{
		"workspace_id": "'" + ws1 + "'", "id": "'synapse'", "name": "'Synapse'", "protocol": "'openai'",
		"base_url": "'https://gw.example.com/v1'", "auth_mode": "'bearer'", "auth_header": "NULL",
	}
	provider := func(over map[string]string) string {
		cols := map[string]string{}
		for k, v := range valid {
			cols[k] = v
		}
		for k, v := range over {
			cols[k] = v
		}
		var names, vals []string
		for k := range cols {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			vals = append(vals, cols[k])
		}
		return `INSERT INTO custom_providers (` + strings.Join(names, ", ") + `) VALUES (` + strings.Join(vals, ", ") + `)`
	}
	if err := exec(provider(nil)); err != nil {
		t.Fatalf("insert a custom provider: %v", err)
	}
	if got := queryStrings(t, db, `SELECT structured_output || ' ' || extra_header_names::text || ' ' || (created_at IS NOT NULL)::text || ' ' || (updated_at IS NOT NULL)::text FROM custom_providers`); !cmp.Equal(got, []string{"strict {} true true"}) {
		t.Errorf("defaults of a custom provider = %v, want strict, no extra headers, both timestamps set", got)
	}
	for _, tc := range []struct {
		name string
		over map[string]string
		want string // violated constraint
	}{
		{"same id twice in a workspace", nil, "custom_providers_pkey"},
		{"unknown workspace", map[string]string{"workspace_id": "'" + "00000000-0000-0000-0000-0000000000ff" + "'", "id": "'other'"}, "custom_providers_workspace_id_workspaces_id_fk"},
		{"upper-case id", map[string]string{"id": "'Syn'"}, "custom_providers_id_chk"},
		{"empty id", map[string]string{"id": "''"}, "custom_providers_id_chk"},
		{"id with underscore", map[string]string{"id": "'a_b'"}, "custom_providers_id_chk"},
		{"id with the custom: prefix", map[string]string{"id": "'custom:a'"}, "custom_providers_id_chk"},
		{"33-character id", map[string]string{"id": "'" + strings.Repeat("a", 33) + "'"}, "custom_providers_id_chk"},
		{"empty name", map[string]string{"id": "'n1'", "name": "''"}, "custom_providers_name_chk"},
		{"blank name", map[string]string{"id": "'n2'", "name": "'   '"}, "custom_providers_name_chk"},
		{"81-character name", map[string]string{"id": "'n3'", "name": "'" + strings.Repeat("n", 81) + "'"}, "custom_providers_name_chk"},
		{"control character in the name", map[string]string{"id": "'n4'", "name": "E'a\\tb'"}, "custom_providers_name_chk"},
		{"unknown protocol", map[string]string{"id": "'p1'", "protocol": "'gemini'"}, "custom_providers_protocol_chk"},
		{"ftp base url", map[string]string{"id": "'u1'", "base_url": "'ftp://x'"}, "custom_providers_base_url_chk"},
		{"base url without host", map[string]string{"id": "'u2'", "base_url": "'https:///v1'"}, "custom_providers_base_url_chk"},
		{"base url with userinfo", map[string]string{"id": "'u3'", "base_url": "'https://u:p@x/v1'"}, "custom_providers_base_url_chk"},
		{"base url with query", map[string]string{"id": "'u4'", "base_url": "'https://x/v1?k=1'"}, "custom_providers_base_url_chk"},
		{"base url with fragment", map[string]string{"id": "'u5'", "base_url": "'https://x/v1#f'"}, "custom_providers_base_url_chk"},
		{"base url with a trailing slash", map[string]string{"id": "'u6'", "base_url": "'https://x/v1/'"}, "custom_providers_base_url_chk"},
		{"base url with a space", map[string]string{"id": "'u7'", "base_url": "'https://x/v 1'"}, "custom_providers_base_url_chk"},
		{"base url of 2049 characters", map[string]string{"id": "'u8'", "base_url": "'https://x/" + strings.Repeat("a", 2039) + "'"}, "custom_providers_base_url_chk"},
		// Also violates the header-name rule; PostgreSQL reports the first constraint by name.
		{"unknown auth mode", map[string]string{"id": "'m1'", "auth_mode": "'basic'"}, "custom_providers_auth_"},
		{"bearer with a header name", map[string]string{"id": "'m2'", "auth_header": "'x-api-key'"}, "custom_providers_auth_header_chk"},
		{"header mode without a header name", map[string]string{"id": "'m3'", "auth_mode": "'header'"}, "custom_providers_auth_header_chk"},
		{"header name that is not a token", map[string]string{"id": "'m4'", "auth_mode": "'header'", "auth_header": "'x api key'"}, "custom_providers_auth_header_chk"},
		{"65-character header name", map[string]string{"id": "'m5'", "auth_mode": "'header'", "auth_header": "'" + strings.Repeat("h", 65) + "'"}, "custom_providers_auth_header_chk"},
		{"six extra header names", map[string]string{"id": "'h1'", "extra_header_names": "ARRAY['a','b','c','d','e','f']"}, "custom_providers_extra_header_names_chk"},
		{"null extra header name", map[string]string{"id": "'h2'", "extra_header_names": "ARRAY['a',NULL]"}, "custom_providers_extra_header_names_chk"},
		{"unknown structured output", map[string]string{"id": "'s1'", "structured_output": "'loose'"}, "custom_providers_structured_output_chk"},
		{"json_object with the anthropic protocol", map[string]string{"id": "'s2'", "protocol": "'anthropic'", "structured_output": "'json_object'"}, "custom_providers_json_object_protocol_chk"},
	} {
		if err := exec(provider(tc.over)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want a violation of %s", tc.name, err, tc.want)
		}
	}
	for name, over := range map[string]map[string]string{
		"32-character id":              {"id": "'" + strings.Repeat("a", 32) + "'"},
		"id of digits and dashes":      {"id": "'0-9'"},
		"same id in another workspace": {"workspace_id": "'" + ws2 + "'"},
		"80-character name, padded":    {"id": "'n1'", "name": "' " + strings.Repeat("n", 80) + " '"},
		"http loopback with a port":    {"id": "'l1'", "base_url": "'http://localhost:11434/v1'"},
		"ipv6 literal":                 {"id": "'l2'", "base_url": "'http://[::1]:8080'"},
		"base url with @ in the path":  {"id": "'l3'", "base_url": "'https://x/a@b'"},
		"base url of 2048 characters":  {"id": "'l4'", "base_url": "'https://x/" + strings.Repeat("a", 2038) + "'"},
		"header mode":                  {"id": "'a1'", "auth_mode": "'header'", "auth_header": "'X-Api_Key.1'"},
		"anthropic with prompt_only":   {"id": "'a2'", "protocol": "'anthropic'", "structured_output": "'prompt_only'"},
		"openai with json_object":      {"id": "'a3'", "structured_output": "'json_object'"},
		"five extra header names":      {"id": "'a4'", "extra_header_names": "ARRAY['a','b','c','d','e']"},
	} {
		if err := exec(provider(over)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := exec(`DELETE FROM custom_providers WHERE id <> 'synapse' OR workspace_id <> '` + ws1 + `'`); err != nil {
		t.Fatal(err)
	}

	// custom_provider_models: unique id per provider, optional label, prices both or neither (FR1).
	model := func(provider, id, label, prompt, completion, position string) string {
		return `INSERT INTO custom_provider_models (workspace_id, provider_id, model_id, label, prompt_per_m, completion_per_m, "position")
		  VALUES ('` + ws1 + `', '` + provider + `', '` + id + `', ` + label + `, ` + prompt + `, ` + completion + `, ` + position + `)`
	}
	for _, q := range []string{
		model("synapse", "llama-3", "NULL", "NULL", "NULL", "0"),
		model("synapse", "llama-3-priced", "'Llama 3'", "0", "10000", "1"),
		model("synapse", "llama-3-cents", "NULL", "0.000001", "9999.999999", "199"),
		model("synapse", strings.Repeat("m", 200), "'"+strings.Repeat("l", 200)+"'", "NULL", "NULL", "2"),
	} {
		if err := exec(q); err != nil {
			t.Errorf("valid manual model: %v\n%s", err, q)
		}
	}
	for _, tc := range []struct {
		name, q, want string
	}{
		{"model id twice", model("synapse", "llama-3", "NULL", "NULL", "NULL", "5"), "custom_provider_models_pkey"},
		{"unknown provider", model("ghost", "m", "NULL", "NULL", "NULL", "0"), "custom_provider_models_provider_fk"},
		{"empty model id", model("synapse", "", "NULL", "NULL", "NULL", "0"), "custom_provider_models_model_id_chk"},
		{"201-character model id", model("synapse", strings.Repeat("m", 201), "NULL", "NULL", "NULL", "0"), "custom_provider_models_model_id_chk"},
		{"201-character label", model("synapse", "m1", "'"+strings.Repeat("l", 201)+"'", "NULL", "NULL", "0"), "custom_provider_models_label_chk"},
		{"position 200", model("synapse", "m2", "NULL", "NULL", "NULL", "200"), "custom_provider_models_position_chk"},
		{"negative position", model("synapse", "m3", "NULL", "NULL", "NULL", "-1"), "custom_provider_models_position_chk"},
		{"only the prompt price", model("synapse", "m4", "NULL", "1", "NULL", "0"), "custom_provider_models_pricing_chk"},
		{"only the completion price", model("synapse", "m5", "NULL", "NULL", "1", "0"), "custom_provider_models_pricing_chk"},
		{"negative price", model("synapse", "m6", "NULL", "-0.000001", "1", "0"), "custom_provider_models_pricing_chk"},
		{"price above 10000", model("synapse", "m7", "NULL", "1", "10000.000001", "0"), "custom_provider_models_pricing_chk"},
		{"seven decimals", model("synapse", "m8", "NULL", "0.0000001", "1", "0"), "custom_provider_models_pricing_chk"},
	} {
		if err := exec(tc.q); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want a violation of %s", tc.name, err, tc.want)
		}
	}
	// The same model id under another provider is fine; deleting a provider deletes its models;
	// deleting a workspace deletes its providers (and so their models).
	if err := exec(provider(map[string]string{"id": "'second'"}) + `; ` + model("second", "llama-3", "NULL", "NULL", "NULL", "0")); err != nil {
		t.Errorf("same model id under another provider: %v", err)
	}
	if err := exec(`DELETE FROM custom_providers WHERE id = 'second'`); err != nil {
		t.Fatal(err)
	}
	if got := queryStrings(t, db, `SELECT count(*)::text FROM custom_provider_models WHERE provider_id = 'second'`); !cmp.Equal(got, []string{"0"}) {
		t.Errorf("models of a deleted provider = %v, want none", got)
	}
	if err := exec(`INSERT INTO custom_providers (workspace_id, id, name, protocol, base_url, auth_mode) VALUES ('` + ws2 + `', 'gone', 'G', 'openai', 'http://x', 'bearer');
	  INSERT INTO custom_provider_models (workspace_id, provider_id, model_id, "position") VALUES ('` + ws2 + `', 'gone', 'm', 0);
	  DELETE FROM workspaces WHERE id = '` + ws2 + `'`); err != nil {
		t.Fatalf("delete a workspace with a provider: %v", err)
	}
	if got := queryStrings(t, db, `SELECT (SELECT count(*) FROM custom_providers WHERE workspace_id = '`+ws2+`')::text || ' ' || (SELECT count(*) FROM custom_provider_models WHERE workspace_id = '`+ws2+`')::text`); !cmp.Equal(got, []string{"0 0"}) {
		t.Errorf("providers and models of a deleted workspace = %v, want none", got)
	}
	if err := exec(`INSERT INTO workspaces (id, name) VALUES ('` + ws2 + `', 'w2')`); err != nil {
		t.Fatal(err)
	}

	// Down refuses while an agent uses a custom provider (A11): the error names the agent, the
	// transaction rolled back, the schema and the version are unchanged.
	if err := exec(`INSERT INTO agents (id, workspace_id, name, provider, model, system_prompt) VALUES ('00000000-0000-0000-0000-0000000000b2', '` + ws1 + `', 'custom-agent', 'custom:synapse', 'm', 's')`); err != nil {
		t.Fatalf("insert an agent on a custom provider: %v", err)
	}
	if _, _, err := m.Down(t.Context()); err == nil || !strings.Contains(err.Error(), "cannot revert 0009_custom_providers: 1 agent(s) use a custom provider") || !strings.Contains(err.Error(), "00000000-0000-0000-0000-0000000000b2 (custom:synapse)") {
		t.Fatalf("Down() with a custom agent error = %v, want the refusal naming the agent", err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after a refused Down differs from version 9 (-v9 +got):\n%s", diff)
	}
	if got := queryStrings(t, db, `SELECT count(*)::text FROM custom_providers`); !cmp.Equal(got, []string{"1"}) {
		t.Errorf("custom providers after a refused Down = %v, want the provider kept", got)
	}
	if st, err := m.Status(t.Context()); err != nil || len(st) != 9 || !st[8].Applied {
		t.Errorf("Status() after a refused Down = %+v, %v, want version 9 still applied", st, err)
	}

	// A built-in agent does not block Down. With the custom agent gone, Down reverses everything.
	if err := exec(`DELETE FROM agents WHERE name = 'custom-agent'; UPDATE ci_installations SET provider = 'custom:synapse' WHERE id = '` + inst2 + `'`); err != nil {
		t.Fatal(err)
	}
	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 9 {
		t.Fatalf("Down() = %d, %v, %v, want 9, true, nil", v, ok, err)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0009 differs from version 8 (-v8 +got):\n%s", diff)
	}
	if diff := cmp.Diff(rowsBefore, queryStrings(t, db, rowsQ)); diff != "" {
		t.Errorf("installations, runs and agents after Down mismatch (-before +got):\n%s", diff)
	}
	if err := exec(agent("custom:synapse")); err == nil || !strings.Contains(err.Error(), "agents_provider_chk") {
		t.Errorf("agent on a custom provider after Down error = %v, want the old rule", err)
	}

	// Up again: the same schema; the installations are given openrouter again.
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
	if got := queryStrings(t, db, `SELECT string_agg(provider, ',' ORDER BY id) FROM ci_installations`); !cmp.Equal(got, []string{"openrouter,openrouter"}) {
		t.Errorf("provider of the installations after Down->Up = %v, want openrouter for both", got)
	}
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
