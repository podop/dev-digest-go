// Package integration_test runs the whole repo-intel pipeline against a real Postgres: the real
// indexer, jobs, service and read port on the real stores, the real clone walk and the real
// tree-sitter and tokenizer adapters, over a temporary clone directory. Git is a fake that edits
// that directory (no network, no clone, no process). Faults come only from the tests: SQL
// triggers that raise or wait on an advisory lock the test holds, and thin wrappers around the
// clone reader and the analyzer. Everything that needs the database skips under -short.
package integration_test

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"dev-digest-go/backend/internal/jobs"
	jobspg "dev-digest-go/backend/internal/jobs/adapters/postgres"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/repointel/adapters/clonetree"
	pgstore "dev-digest-go/backend/internal/repointel/adapters/postgres"
	"dev-digest-go/backend/internal/repointel/adapters/tokenizer"
	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// tokens is shared by every test: the cl100k_base vocabulary is loaded once per counter.
var tokens = sync.OnceValue(func() *tokenizer.Counter { return tokenizer.New(nil) })

// --- statement log -----------------------------------------------------------------------

// statementLog is a GORM logger that records every statement and its bind parameter count.
type statementLog struct {
	logger.Interface
	mu    sync.Mutex
	stmts []statement
}

type statement struct {
	sql    string
	params int
}

func (l *statementLog) LogMode(logger.LogLevel) logger.Interface { return l }

// Trace evaluates the statement, which makes GORM call ParamsFilter.
func (l *statementLog) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	fc()
}

// ParamsFilter sees the statement and its bind values before they are explained.
func (l *statementLog) ParamsFilter(_ context.Context, sql string, params ...any) (string, []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stmts = append(l.stmts, statement{sql: sql, params: len(params)})
	return sql, params
}

func (l *statementLog) all() []statement {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.stmts)
}

// --- fake git ----------------------------------------------------------------------------

// change is one commit of the fake git: files written and paths deleted.
type change struct {
	write map[string]string
	del   []string
}

// gitSim is a fake git over the clone directory. A commit edits the directory at once; a staged
// commit is the remote's, applied by Sync. It records which paths each commit touched so
// DiffNames answers like `git diff --name-only`, and it fails like git for an unknown base.
type gitSim struct {
	mu      sync.Mutex
	dir     string
	n       int
	head    string
	shas    []string
	changes map[string][]string
	staged  []change

	headErr, diffErr, syncErr, branchErr error
}

func newGitSim(dir string) *gitSim {
	return &gitSim{dir: dir, changes: map[string][]string{}}
}

// apply writes the change to the directory and records it as the new head.
func (g *gitSim) apply(c change) string {
	var paths []string
	for p, content := range c.write {
		full := filepath.Join(g.dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			panic(err)
		}
		paths = append(paths, p)
	}
	for _, p := range c.del {
		if err := os.Remove(filepath.Join(g.dir, filepath.FromSlash(p))); err != nil {
			panic(err)
		}
		paths = append(paths, p)
	}
	g.n++
	sum := sha1.Sum([]byte(fmt.Sprintf("commit-%d", g.n)))
	sha := fmt.Sprintf("%x", sum)
	g.shas = append(g.shas, sha)
	g.changes[sha] = paths
	g.head = sha
	return sha
}

// Commit applies a commit to the clone and returns its sha.
func (g *gitSim) Commit(write map[string]string, del ...string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.apply(change{write: write, del: del})
}

// Stage queues a remote commit that Sync will apply.
func (g *gitSim) Stage(write map[string]string, del ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.staged = append(g.staged, change{write: write, del: del})
}

func (g *gitSim) setHeadErr(err error) { g.mu.Lock(); g.headErr = err; g.mu.Unlock() }
func (g *gitSim) setDiffErr(err error) { g.mu.Lock(); g.diffErr = err; g.mu.Unlock() }
func (g *gitSim) setSyncErr(err error) { g.mu.Lock(); g.syncErr = err; g.mu.Unlock() }

// Head implements app.Git.
func (g *gitSim) Head(context.Context, string, string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.headErr != nil {
		return "", g.headErr
	}
	return g.head, nil
}

// DiffNames implements app.Git: the union of the paths of the commits after base up to head.
func (g *gitSim) DiffNames(_ context.Context, _, _, base, head string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.diffErr != nil {
		return nil, g.diffErr
	}
	i, j := slices.Index(g.shas, base), slices.Index(g.shas, head)
	if i < 0 || j < i {
		return nil, fmt.Errorf("git diff: bad object %s", base)
	}
	set := map[string]struct{}{}
	for _, sha := range g.shas[i+1 : j+1] {
		for _, p := range g.changes[sha] {
			set[p] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(set)), nil
}

// DefaultBranch implements app.Git.
func (g *gitSim) DefaultBranch(context.Context, string, string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.branchErr != nil {
		return "", g.branchErr
	}
	return "main", nil
}

// Sync implements app.Git: apply the staged remote commits and return the new head.
func (g *gitSim) Sync(context.Context, string, string, string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.syncErr != nil {
		return "", g.syncErr
	}
	for _, c := range g.staged {
		g.apply(c)
	}
	g.staged = nil
	return g.head, nil
}

func (g *gitSim) currentHead() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.head
}

var _ app.Git = (*gitSim)(nil)

// --- fault wrappers ----------------------------------------------------------------------

// readLog wraps the real SourceFS: it records every ReadFile and fails the paths in bad.
type readLog struct {
	domain.SourceFS
	mu    sync.Mutex
	reads []readAttempt
	bad   map[string]error
}

type readAttempt struct {
	rel string
	err error
}

func newReadLog(inner domain.SourceFS) *readLog {
	return &readLog{SourceFS: inner, bad: map[string]error{}}
}

func (r *readLog) ReadFile(root, rel string) ([]byte, error) {
	r.mu.Lock()
	bad := r.bad[rel]
	r.mu.Unlock()
	if bad != nil {
		return nil, bad
	}
	data, err := r.SourceFS.ReadFile(root, rel)
	r.mu.Lock()
	r.reads = append(r.reads, readAttempt{rel: rel, err: err})
	r.mu.Unlock()
	return data, err
}

func (r *readLog) failRead(rel string) {
	r.mu.Lock()
	r.bad[rel] = fs.ErrPermission
	r.mu.Unlock()
}

func (r *readLog) attempts() []readAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.reads)
}

// pauseAnalyzer wraps the real analyzer: the n-th Extract call signals paused and waits for
// release before it parses.
type pauseAnalyzer struct {
	app.Analyzer
	n       int64
	calls   atomic.Int64
	paused  chan struct{}
	release chan struct{}
}

func newPauseAnalyzer(inner app.Analyzer, n int) *pauseAnalyzer {
	return &pauseAnalyzer{Analyzer: inner, n: int64(n), paused: make(chan struct{}), release: make(chan struct{})}
}

func (p *pauseAnalyzer) Extract(ctx context.Context, path, text string) (domain.Extraction, error) {
	if p.calls.Add(1) == p.n {
		close(p.paused)
		select {
		case <-p.release:
		case <-ctx.Done():
			return domain.Extraction{}, ctx.Err()
		}
	}
	return p.Analyzer.Extract(ctx, path, text)
}

// stepClock is a clock that moves forward by step on every reading.
func stepClock(step time.Duration) func() time.Time {
	var n atomic.Int64
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return base.Add(time.Duration(n.Add(1)) * step) }
}

// discardQueue is the Enqueuer of a Jobs whose handlers the test calls directly.
type discardQueue struct{}

func (discardQueue) Enqueue(context.Context, string, string, any) (string, error) {
	return "", errors.New("no queue in this test")
}

// --- fixture -----------------------------------------------------------------------------

// baseFiles is a small TypeScript project: core is imported by a, b, c and the test; c imports
// a, server imports b. Junk paths (a test, a declaration, a migration) and two files the walk
// does not take (.md, .py) are part of it.
func baseFiles() map[string]string {
	return map[string]string{
		"src/core.ts":         "export function core(): number {\n  return 1;\n}\n\nexport class Store {}\n",
		"src/a.ts":            "import { core } from './core';\n\nexport function runA(): number {\n  return core();\n}\n",
		"src/b.ts":            "import { core } from './core';\n\nexport function runB(): number {\n  return core() + 1;\n}\n",
		"src/c.ts":            "import { core } from './core';\nimport { runA } from './a';\n\nexport function runC(): number {\n  return runA() + core();\n}\n",
		"src/server.ts":       "import { runB } from './b';\n\nconst app = makeApp();\napp.get('/x', () => runB());\n",
		"src/core.test.ts":    "import { core } from './core';\n\ntest('core', () => {\n  expect(core()).toBe(1);\n});\n",
		"src/types.d.ts":      "export interface Shape {\n  id: string;\n}\n",
		"db/migrations/01.ts": "export function up(): void {}\n",
		"README.md":           "# fixture\n",
		"tools/build.py":      "print('hi')\n",
	}
}

// --- environment -------------------------------------------------------------------------

type options struct {
	files    map[string]string
	fsWrap   func(domain.SourceFS) domain.SourceFS
	anWrap   func(app.Analyzer) app.Analyzer
	workers  int
	now      func() time.Time
	noClone  bool
	disabled bool
}

type option func(*options)

func withFiles(f map[string]string) option  { return func(o *options) { o.files = f } }
func withWorkers(n int) option              { return func(o *options) { o.workers = n } }
func withClock(now func() time.Time) option { return func(o *options) { o.now = now } }
func withoutClone() option                  { return func(o *options) { o.noClone = true } }
func withFlagOff() option                   { return func(o *options) { o.disabled = true } }
func withFS(w func(domain.SourceFS) domain.SourceFS) option {
	return func(o *options) { o.fsWrap = w }
}
func withAnalyzer(w func(app.Analyzer) app.Analyzer) option {
	return func(o *options) { o.anWrap = w }
}

// env is one repo with its clone, the stores and the use cases wired on a fresh database.
type env struct {
	t       *testing.T
	raw     *gorm.DB // plain handle: assertions and fault setup
	db      *gorm.DB // the handle the stack uses; it records every statement
	stmts   *statementLog
	store   *pgstore.Store
	tx      *platformpg.Tx
	ws      string
	repo    string
	dir     string
	git     *gitSim
	indexer *app.Indexer
	jobs    *app.Jobs
	service *app.Service
	reader  *app.Reader
	faults  int
}

func newEnv(t *testing.T, opts ...option) *env {
	t.Helper()
	o := options{files: baseFiles()}
	for _, opt := range opts {
		opt(&o)
	}
	d := pgtest.Migrated(t)
	stmts := &statementLog{Interface: logger.Discard}
	db := d.DB.Session(&gorm.Session{Logger: stmts})
	e := &env{t: t, raw: d.DB, db: db, stmts: stmts, store: pgstore.NewStore(db), tx: platformpg.NewTx(db)}
	// Two levels below the temp dir, so a "../../etc/x.json" reference points at a real sibling.
	e.dir = filepath.Join(t.TempDir(), "x", "y")
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.git = newGitSim(e.dir)
	e.git.Commit(o.files)

	if err := d.DB.Raw(`INSERT INTO workspaces (name) VALUES ('default') RETURNING id`).Scan(&e.ws).Error; err != nil {
		t.Fatal(err)
	}
	var clone *string
	if !o.noClone {
		clone = &e.dir
	}
	err := d.DB.Raw(`INSERT INTO repos (workspace_id, owner, name, full_name, default_branch, clone_path)
		VALUES (?, 'o', 'r', 'o/r', 'main', ?) RETURNING id`, e.ws, clone).Scan(&e.repo).Error
	if err != nil {
		t.Fatal(err)
	}

	var files domain.SourceFS = clonetree.New()
	if o.fsWrap != nil {
		files = o.fsWrap(files)
	}
	var an app.Analyzer = treesitter.New()
	if o.anWrap != nil {
		an = o.anWrap(an)
	}
	log := slog.New(slog.DiscardHandler)
	e.indexer = app.NewIndexer(app.IndexerDeps{
		Reader: e.store, Writer: e.store, Tx: e.tx, Git: e.git, Files: files, Analyzer: an,
		Graph: app.NewImportGraph(files, an, log), Tokens: tokens(),
	}, app.IndexerOptions{Now: o.now, Workers: o.workers, Log: log})
	e.jobs = app.NewJobs(app.JobsDeps{Indexer: e.indexer, Repos: e.store, Git: e.git, Queue: discardQueue{}},
		app.JobsOptions{Now: o.now, Log: log})
	e.service = app.NewService(e.store, discardQueue{}, log)
	e.reader = app.NewReader(e.store, !o.disabled)
	return e
}

// mustFull runs a full index and fails the test on an error.
func (e *env) mustFull() app.Result {
	e.t.Helper()
	res, err := e.indexer.Full(e.t.Context(), e.repo)
	if err != nil {
		e.t.Fatalf("Full() error = %v", err)
	}
	return res
}

// mustIncremental runs an incremental index and fails the test on an error.
func (e *env) mustIncremental() app.Result {
	e.t.Helper()
	res, err := e.indexer.Incremental(e.t.Context(), e.repo)
	if err != nil {
		e.t.Fatalf("Incremental() error = %v", err)
	}
	return res
}

// state returns the repo's state row.
func (e *env) state() (domain.IndexState, bool) {
	e.t.Helper()
	st, ok, err := e.store.GetState(e.t.Context(), e.repo)
	if err != nil {
		e.t.Fatalf("GetState() error = %v", err)
	}
	return st, ok
}

func (e *env) mustState() domain.IndexState {
	e.t.Helper()
	st, ok := e.state()
	if !ok {
		e.t.Fatal("no repo_index_state row, want one")
	}
	return st
}

// count returns the repo's row count in table.
func (e *env) count(table string) int {
	e.t.Helper()
	var n int
	if err := e.raw.Raw(fmt.Sprintf(`SELECT count(*) FROM %s WHERE repo_id = ?`, table), e.repo).Scan(&n).Error; err != nil {
		e.t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// exec runs a statement on the plain handle.
func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if err := e.raw.Exec(sql, args...).Error; err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

// scanStrings returns the first column of a query as strings.
func (e *env) scanStrings(sql string, args ...any) []string {
	e.t.Helper()
	var out []string
	if err := e.raw.Raw(sql, args...).Scan(&out).Error; err != nil {
		e.t.Fatalf("query %q: %v", sql, err)
	}
	return out
}

// indexTables are the seven tables of the index.
var indexTables = []string{"symbols", `"references"`, "file_edges", "file_rank", "file_facts", "repo_map_cache", "repo_index_state"}

// dump returns the repo's rows of table as sorted JSON documents; strip names the keys left out.
func (e *env) dump(table string, strip ...string) []string {
	e.t.Helper()
	expr := "to_jsonb(x)"
	for _, k := range strip {
		expr += " - '" + k + "'"
	}
	if table == "repo_index_state" {
		expr = "(" + expr + ") #- '{stats,durationMs}'"
	}
	rows := e.scanStrings(fmt.Sprintf(`SELECT (%s)::text FROM %s x WHERE repo_id = ?`, expr, table), e.repo)
	slices.Sort(rows)
	return rows
}

// snapshot dumps the seven tables exactly as stored (ids and timestamps included).
func (e *env) snapshot() map[string][]string {
	e.t.Helper()
	out := map[string][]string{}
	for _, tbl := range indexTables {
		out[tbl] = e.dump(tbl)
	}
	return out
}

// content dumps the seven tables without ids, repo id and timestamps, and without the run's
// duration: what two indexes of the same commit must agree on.
func (e *env) content() map[string][]string {
	e.t.Helper()
	out := map[string][]string{}
	for _, tbl := range indexTables {
		strip := []string{"repo_id"}
		switch tbl {
		case "symbols", `"references"`:
			strip = append(strip, "id")
		case "repo_map_cache":
			strip = append(strip, "created_at")
		case "repo_index_state":
			strip = append(strip, "updated_at")
		}
		out[tbl] = e.dump(tbl, strip...)
	}
	return out
}

// paths returns the sorted values of a path column of the repo's rows.
func (e *env) paths(table, column string) []string {
	e.t.Helper()
	rows := e.scanStrings(fmt.Sprintf(`SELECT %s FROM %s WHERE repo_id = ?`, column, table), e.repo)
	slices.Sort(rows)
	return rows
}

// symbolNames returns the sorted "path:name" of the repo's symbols.
func (e *env) symbolNames() []string {
	e.t.Helper()
	rows := e.scanStrings(`SELECT path || ':' || name FROM symbols WHERE repo_id = ?`, e.repo)
	slices.Sort(rows)
	return rows
}

// statInt reads an integer stat; the stats document decodes numbers as float64.
func statInt(t *testing.T, st domain.IndexState, key string) int {
	t.Helper()
	v, ok := st.Stats[key].(float64)
	if !ok {
		t.Fatalf("stats[%q] = %v (%T), want a number", key, st.Stats[key], st.Stats[key])
	}
	return int(v)
}

// --- fault injection ---------------------------------------------------------------------

// failInserts makes every INSERT statement into table raise msg (P8).
func (e *env) failInserts(table, msg string) {
	e.t.Helper()
	e.faults++
	fn := fmt.Sprintf("test_fail_%d", e.faults)
	e.exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION '%%', '%s'; END $$`, fn, msg))
	e.exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON %s FOR EACH STATEMENT EXECUTE FUNCTION %s()`, fn, table, fn))
}

// pauseInserts makes every INSERT statement into table wait, after the insert, for the advisory
// lock named key; the test holds that lock with holdLock (P8).
func (e *env) pauseInserts(table, key string) {
	e.t.Helper()
	e.faults++
	fn := fmt.Sprintf("test_pause_%d", e.faults)
	e.exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		PERFORM pg_advisory_xact_lock(hashtextextended('%s', 0)); RETURN NULL; END $$`, fn, key))
	e.exec(fmt.Sprintf(`CREATE TRIGGER %s AFTER INSERT ON %s FOR EACH STATEMENT EXECUTE FUNCTION %s()`, fn, table, fn))
}

// holdLock takes the session-level advisory lock named key on a connection of its own and
// returns the function that releases it (also run at cleanup).
func (e *env) holdLock(key string) (release func()) {
	e.t.Helper()
	sqlDB, err := e.raw.DB()
	if err != nil {
		e.t.Fatal(err)
	}
	conn, err := sqlDB.Conn(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := conn.ExecContext(e.t.Context(), `SELECT pg_advisory_lock(hashtextextended($1, 0))`, key); err != nil {
		e.t.Fatalf("hold lock %q: %v", key, err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			ctx := context.WithoutCancel(e.t.Context())
			if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key); err != nil {
				e.t.Errorf("release lock %q: %v", key, err)
			}
			if err := conn.Close(); err != nil {
				e.t.Errorf("close lock connection: %v", err)
			}
		})
	}
	e.t.Cleanup(release)
	return release
}

// repoLock is the name of the repo's own advisory lock (FR3028).
func (e *env) repoLock() string { return "repo-intel:" + e.repo }

// waitWaiters blocks until n sessions wait for an advisory lock of this database. Postgres
// offers no event for it, so it polls.
func (e *env) waitWaiters(n int) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var got int
		err := e.raw.Raw(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&got).Error
		if err != nil {
			e.t.Fatalf("count lock waiters: %v", err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%d sessions wait for an advisory lock, want %d", got, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- jobs --------------------------------------------------------------------------------

// runner builds the real job runner on the jobs table with the three repo-intel kinds registered
// under attemptLimit, as the composition root does with domain.JobTimeout.
func (e *env) runner(attemptLimit time.Duration) *jobs.Runner {
	e.t.Helper()
	r := jobs.New(jobspg.NewStore(e.raw), jobs.Options{Concurrency: 3, Retries: -1})
	j := app.NewJobs(app.JobsDeps{Indexer: e.indexer, Repos: e.store, Git: e.git, Queue: r}, app.JobsOptions{})
	r.Register(domain.KindIndex, j.Index, jobs.WithTimeout(attemptLimit))
	r.Register(domain.KindRefresh, j.Refresh, jobs.WithTimeout(attemptLimit))
	r.Register(domain.KindResync, j.Resync, jobs.WithTimeout(attemptLimit))
	e.t.Cleanup(func() {
		if err := r.Shutdown(jobs.DefaultShutdownTimeout); err != nil {
			e.t.Errorf("runner Shutdown() = %v", err)
		}
	})
	return r
}

type jobRecord struct {
	Status   string
	Attempts int
	Error    string
}

// job reads a job row.
func (e *env) job(id string) jobRecord {
	e.t.Helper()
	var rec jobRecord
	err := e.raw.Raw(`SELECT status, attempts, coalesce(error, '') AS error FROM jobs WHERE id = ?`, id).Scan(&rec).Error
	if err != nil {
		e.t.Fatalf("read job %s: %v", id, err)
	}
	return rec
}

func (e *env) payload() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"repoId":%q}`, e.repo))
}

// manyFiles returns n tiny modules src/f0000.ts ... and the sorted list of their paths.
func manyFiles(n int) (files map[string]string, paths []string) {
	files = make(map[string]string, n)
	for i := range n {
		p := fmt.Sprintf("src/f%04d.ts", i)
		files[p] = fmt.Sprintf("export function f%04d(): number {\n  return %d;\n}\n", i, i)
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return files, paths
}
