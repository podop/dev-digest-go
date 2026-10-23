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
func baselineFS(t *testing.T) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	for _, name := range []string{"0001_baseline.up.sql", "0001_baseline.down.sql"} {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = &fstest.MapFile{Data: body}
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

	m := newMigrator(t, db, migrations.FS)
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
