package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/jobs"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/settings/adapters/providers"
	"dev-digest-go/backend/internal/skills/adapters/urlfetch"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// syncBuffer is a log sink that the server and the test can use concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// entries parses the JSON log lines.
func (b *syncBuffer) entries(t testing.TB) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// spec describes one test server. The zero value is a seeded, migrated database with rate
// limiting off, no GitHub token and every outbound call pointed at stubs.
type spec struct {
	db         *pgtest.Database // nil: a migrated database
	rateLimit  bool
	noMigrate  bool
	noSeed     bool
	env        map[string]string // process environment as the secrets store sees it
	jobs       jobs.Options
	gitBinary  string
	providers  providers.Options
	remoteDir  string // directory holding <owner>/<name>.git; "" = an empty one
	secretFile string // content of secrets.json, "" = none

	allowedHosts []string // Host allowlist; nil = no Host check

	// Phase 2 seams. llmURLs defaults to a closed local port so no test reaches a real provider.
	llmURLs        llmURLs
	llm            llm.Options          // retry backoff, budget, price book; Keys/Adapters/Logger are wire's
	urlfetch       urlfetch.Options     // skill URL import: resolver, dialer, TLS roots
	extractTimeout time.Duration        // conventions.extract per-attempt limit; 0 = 300 s
	noIntelJobs    bool                 // leave the repo-intel job kinds unregistered (degraded resync)
	config         func(*config.Config) // adjusts the config, e.g. LLMProviderOverride, ProjectContextGlobs

	// Phase 4 seam: scripted provider adapters replace the real ones (options.llmAdapters).
	llmAdapters map[llm.Provider]llm.Adapter
}

// stack is a running server with its stubs.
type stack struct {
	t       testing.TB
	db      *pgtest.Database
	cfg     config.Config
	logs    *syncBuffer
	gh      *githubStub
	base    string
	remote  string
	client  *http.Client
	cancel  context.CancelFunc
	done    chan error
	stopped bool
}

// newConfig is the configuration the tests start from.
func newConfig(t testing.TB, db *pgtest.Database, sp spec) config.Config {
	t.Helper()
	dataDir := t.TempDir()
	return config.Config{
		Addr:             "127.0.0.1:0",
		DatabaseURL:      db.DSN,
		CORSOrigins:      []string{"http://localhost:3100"},
		AllowedHosts:     sp.allowedHosts,
		DataDir:          dataDir,
		CloneDir:         filepath.Join(dataDir, "workspace"),
		RateLimitEnabled: sp.rateLimit,
		AutoMigrate:      !sp.noMigrate,
		AutoSeed:         !sp.noSeed,

		ProjectContextGlobs: []string{config.DefaultProjectContextGlobs},
	}
}

// startStack boots the server through serve and waits until it listens.
func startStack(t testing.TB, sp spec) *stack {
	t.Helper()
	if sp.db == nil {
		sp.db = pgtest.Migrated(t)
	}
	s := &stack{t: t, db: sp.db, logs: &syncBuffer{}, gh: newGitHubStub(t), client: &http.Client{Timeout: 30 * time.Second}}
	s.cfg = newConfig(t, sp.db, sp)
	if sp.config != nil {
		sp.config(&s.cfg)
	}
	if sp.secretFile != "" {
		if err := os.MkdirAll(s.cfg.DataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.cfg.DataDir, "secrets.json"), []byte(sp.secretFile), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s.remote = sp.remoteDir
	if s.remote == "" {
		s.remote = t.TempDir()
	}
	env := sp.env
	opts := []option{
		withGetenv(func(k string) string { return env[k] }),
		withLogOutput(s.logs),
		func(o *options) {
			o.githubBaseURL = s.gh.URL()
			o.providers = sp.providers
			o.remoteBase = "file://" + s.remote
			o.gitBinary = sp.gitBinary
			o.jobs = sp.jobs
			o.llmURLs = sp.llmURLs
			if o.llmURLs == (llmURLs{}) {
				closed := "http://127.0.0.1:1"
				o.llmURLs = llmURLs{openAI: closed, anthropic: closed, openRouter: closed}
			}
			o.llm = sp.llm
			o.llmAdapters = sp.llmAdapters
			o.urlfetch = sp.urlfetch
			o.extractTimeout = sp.extractTimeout
			o.noRepoIntelJobs = sp.noIntelJobs
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan error, 1)
	ready := make(chan net.Addr, 1)
	opts = append(opts, func(o *options) { o.onListen = func(a net.Addr) { ready <- a } })
	go func() { s.done <- serve(ctx, s.cfg, opts...) }()

	select {
	case a := <-ready:
		s.base = "http://" + a.String()
	case err := <-s.done:
		cancel()
		t.Fatalf("serve returned before listening: %v\nlogs:\n%s", err, s.logs.String())
	case <-time.After(60 * time.Second):
		cancel()
		t.Fatalf("serve did not start listening\nlogs:\n%s", s.logs.String())
	}
	t.Cleanup(func() { _ = s.stop() })
	return s
}

// stop cancels the server context (what SIGTERM does) and returns serve's result.
func (s *stack) stop() error {
	s.t.Helper()
	if s.stopped {
		return nil
	}
	s.stopped = true
	s.cancel()
	s.client.CloseIdleConnections()
	select {
	case err := <-s.done:
		return err
	case <-time.After(30 * time.Second):
		s.t.Errorf("serve did not return within 30s of the stop signal")
		return nil
	}
}

// response is a finished HTTP exchange.
type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body into a generic value.
func (r response) JSON(t testing.TB) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("body is not JSON (status %d): %q: %v", r.Status, r.Body, err)
	}
	return v
}

// Obj decodes the body into an object.
func (r response) Obj(t testing.TB) map[string]any {
	t.Helper()
	m, ok := r.JSON(t).(map[string]any)
	if !ok {
		t.Fatalf("body is not a JSON object: %q", r.Body)
	}
	return m
}

// errorCode returns error.code of an envelope.
func (r response) errorCode(t testing.TB) string {
	t.Helper()
	e, _ := r.Obj(t)["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// do sends a request; body may be nil, a string/[]byte (sent as is) or any JSON value.
func (s *stack) do(method, path string, body any, headers ...string) response {
	s.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			s.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.base+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" { // net/http sends req.Host, not the header map entry
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return response{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

func (s *stack) get(path string) response { s.t.Helper(); return s.do(http.MethodGet, path, nil) }
func (s *stack) post(path string, body any) response {
	s.t.Helper()
	return s.do(http.MethodPost, path, body)
}

// eventually polls cond until it holds or d passes.
func eventually(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		<-tick.C
	}
}

// jobRow is one row of the jobs table.
type jobRow struct {
	ID         string
	Kind       string
	Status     string
	Attempts   int
	Error      *string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

func jobRows(t testing.TB, db *gorm.DB, kind string) []jobRow {
	t.Helper()
	var rows []jobRow
	q := db.Table("jobs").Order("scheduled_at, id")
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if err := q.Find(&rows).Error; err != nil {
		t.Fatalf("read jobs: %v", err)
	}
	return rows
}

// waitJobs waits until n jobs of kind exist and all of them are finished.
func (s *stack) waitJobs(kind string, n int) []jobRow {
	s.t.Helper()
	var rows []jobRow
	eventually(s.t, 60*time.Second, fmt.Sprintf("%d finished %q job(s)", n, kind), func() bool {
		rows = jobRows(s.t, s.db.DB, kind)
		if len(rows) < n {
			return false
		}
		for _, r := range rows {
			if r.Status != "done" && r.Status != "failed" {
				return false
			}
		}
		return true
	})
	return rows
}

// scalar runs a single-value query.
func scalar[T any](t testing.TB, db *gorm.DB, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := db.Raw(sql, args...).Scan(&v).Error; err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

// ---- GitHub stub ----

type ghRequest struct {
	Method, Path, Query, Auth string
	Body                      []byte
}

// githubStub plays api.github.com for repository o/r (and any owner but acme, which answers
// 404 so the stored data is served).
type githubStub struct {
	srv *httptest.Server
	mu  sync.Mutex
	req []ghRequest
	// override, when set, may answer a request itself and report that it did.
	override func(w http.ResponseWriter, r *http.Request) bool
}

func newGitHubStub(t testing.TB) *githubStub {
	g := &githubStub{}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serveHTTP))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *githubStub) URL() string { return g.srv.URL }

func (g *githubStub) setOverride(f func(w http.ResponseWriter, r *http.Request) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.override = f
}

func (g *githubStub) requests() []ghRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ghRequest(nil), g.req...)
}

func (g *githubStub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.req = append(g.req, ghRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Body: body})
	override := g.override
	g.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if override != nil && override(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/repos/acme/") {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	const pr = `{"number":7,"title":"Add feature","user":{"login":"octo"},"head":{"ref":"feat","sha":"abc123"},` +
		`"base":{"ref":"main"},"state":"open","merged_at":null,"created_at":"2026-10-19T10:00:00Z","updated_at":"2026-10-20T10:00:00Z"`
	switch {
	case r.URL.Path == "/user":
		_, _ = io.WriteString(w, `{"login":"octo"}`)
	case strings.HasSuffix(r.URL.Path, "/pulls") && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, "["+pr+"}]")
	case strings.HasSuffix(r.URL.Path, "/pulls/7"):
		_, _ = io.WriteString(w, pr+`,"body":"Fixes #12","additions":10,"deletions":2,"changed_files":2}`)
	case strings.HasSuffix(r.URL.Path, "/pulls/7/files"):
		_, _ = io.WriteString(w, `[{"filename":"a.ts","additions":7,"deletions":1,"patch":"@@ -1 +1 @@\n-a\n+b"},{"filename":"b.ts","additions":3,"deletions":1}]`)
	case strings.HasSuffix(r.URL.Path, "/pulls/7/commits"):
		_, _ = io.WriteString(w, `[{"sha":"c0ffee","commit":{"message":"feat: x","author":{"name":"Octo","date":"2026-10-19T10:00:00Z"}},"author":{"login":"octo"}}]`)
	case strings.HasSuffix(r.URL.Path, "/issues/12"):
		_, _ = io.WriteString(w, `{"number":12,"title":"Bug","body":"Details","state":"open"}`)
	case strings.HasSuffix(r.URL.Path, "/pulls/7/comments") && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `[{"id":1,"path":"a.ts","line":3,"original_line":3,"side":"RIGHT","body":"nit","user":{"login":"octo"},`+
			`"created_at":"2026-10-20T10:00:00Z","html_url":"https://github.com/o/r/pull/7#r1","in_reply_to_id":null},`+
			`{"id":2,"path":"a.ts","line":null,"original_line":3,"side":null,"body":"reply","user":{"login":"octo"},`+
			`"created_at":"2026-10-20T11:00:00Z","html_url":"https://github.com/o/r/pull/7#r2","in_reply_to_id":1}]`)
	case strings.HasSuffix(r.URL.Path, "/pulls/7/comments") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":99,"path":"a.ts","line":3,"original_line":3,"side":"RIGHT","body":"x","user":{"login":"octo"},`+
			`"created_at":"2026-10-20T12:00:00Z","html_url":"https://github.com/o/r/pull/7#r99","in_reply_to_id":null}`)
	default:
		http.NotFound(w, r)
	}
}

// ---- git fixtures ----

func gitRun(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// makeRemote creates <remoteDir>/<owner>/<name>.git, a bare repository with three commits on
// main, and returns the working repository it was cloned from (to add more commits).
func makeRemote(t testing.TB, remoteDir, owner, name string) string {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	for i := 1; i <= 3; i++ {
		file := filepath.Join(work, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(file, []byte(fmt.Sprintf("content %d\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, work, "add", ".")
		gitRun(t, work, "commit", "-q", "-m", fmt.Sprintf("commit %d", i))
	}
	ownerDir := filepath.Join(remoteDir, owner)
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ownerDir, "clone", "-q", "--bare", work, name+".git")
	return work
}

// gitWrapper writes an executable that records every invocation (arguments and GIT_CONFIG_*
// environment) to the returned log file and then runs the real git. With slowClone, a clone
// instead sleeps until killed.
func gitWrapper(t testing.TB, slowClone bool) (binary, logFile string) {
	t.Helper()
	dir := t.TempDir()
	binary, logFile = filepath.Join(dir, "git-wrapper"), filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"{ echo '--- call'; for a in \"$@\"; do printf '%s\\n' \"$a\"; done; env | grep '^GIT_CONFIG_'; } >> '" + logFile + "'\n"
	if slowClone {
		script += "for a in \"$@\"; do [ \"$a\" = clone ] && exec sleep 120; done\n"
	}
	script += "exec git \"$@\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, logFile
}

// ---- startup and operator commands ----

// tableNames lists the tables of schema public.
func tableNames(t testing.TB, db *gorm.DB) []string {
	t.Helper()
	var names []string
	if err := db.Raw(`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY 1`).Scan(&names).Error; err != nil {
		t.Fatal(err)
	}
	return names
}

// logLines returns the log entries whose msg is msg.
func logLines(t testing.TB, b *syncBuffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range b.entries(t) {
		if e["msg"] == msg {
			out = append(out, e)
		}
	}
	return out
}

// serveOnce runs serve to completion (it must fail before listening) and returns its error.
func serveOnce(t testing.TB, db *pgtest.Database, sp spec) (*syncBuffer, error) {
	t.Helper()
	logs := &syncBuffer{}
	listened := false
	err := serve(t.Context(), newConfig(t, db, sp),
		withLogOutput(logs),
		func(o *options) { o.onListen = func(net.Addr) { listened = true } })
	if listened {
		t.Error("the server listened although startup should have failed")
	}
	return logs, err
}

func TestStartup_EmptyDatabaseMigratesAndSeedsBeforeListening(t *testing.T) { // AC32, AC101
	db := pgtest.Empty(t)
	s := startStack(t, spec{db: db})

	resp := s.get("/repos")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /repos status = %d, want 200; body %s", resp.Status, resp.Body)
	}
	var names []string
	for _, r := range resp.JSON(t).([]any) {
		names = append(names, r.(map[string]any)["full_name"].(string))
	}
	if len(names) != 1 || names[0] != "acme/payments-api" {
		t.Errorf("GET /repos full_names = %v, want [acme/payments-api]", names)
	}

	applied := logLines(t, s.logs, "migration applied")
	if len(applied) != 1 || applied[0]["version"] != float64(1) {
		t.Errorf("migration log lines = %v, want one line for version 1", applied)
	} else if _, ok := applied[0]["duration_ms"]; !ok {
		t.Errorf("migration log line %v has no duration_ms", applied[0])
	}
	seeded := logLines(t, s.logs, "seed complete")
	if len(seeded) != 1 || seeded[0]["repos"] != float64(1) || seeded[0]["pull_requests"] != float64(1) {
		t.Errorf("seed log lines = %v, want one line with repos=1 pull_requests=1", seeded)
	}
}

func TestStartup_ExistingReposTableFailsWithoutListening(t *testing.T) { // AC31
	db := pgtest.Empty(t)
	if err := db.DB.Exec(`CREATE TABLE repos (id int)`).Error; err != nil {
		t.Fatal(err)
	}
	logs, err := serveOnce(t, db, spec{})
	if err == nil {
		t.Fatal("serve() = nil on a database that already has a repos table, want an error")
	}
	if !strings.Contains(err.Error(), "version 1") {
		t.Errorf("serve() error = %q, want it to name version 1", err)
	}
	failed := logLines(t, logs, "startup failed")
	if len(failed) != 1 || failed[0]["step"] != "migrate" || !strings.Contains(fmt.Sprint(failed[0]["error"]), "version 1") {
		t.Errorf("startup failure log lines = %v, want one migrate line naming version 1", failed)
	}
	if got, want := tableNames(t, db.DB), []string{"repos"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tables after the failed baseline = %v, want %v", got, want)
	}
}

func TestStartup_AutoMigrateOff(t *testing.T) { // AC33
	t.Run("seed enabled fails and creates nothing", func(t *testing.T) {
		db := pgtest.Empty(t)
		logs, err := serveOnce(t, db, spec{noMigrate: true})
		if err == nil {
			t.Fatal("serve() = nil with DB_AUTO_MIGRATE=false on an empty database, want the seed to fail")
		}
		if failed := logLines(t, logs, "startup failed"); len(failed) != 1 || failed[0]["step"] != "seed" {
			t.Errorf("startup failure log lines = %v, want one seed line", failed)
		}
		if got := tableNames(t, db.DB); len(got) != 0 {
			t.Errorf("tables = %v, want none", got)
		}
	})
	t.Run("seed disabled serves health and creates nothing", func(t *testing.T) {
		db := pgtest.Empty(t)
		s := startStack(t, spec{db: db, noMigrate: true, noSeed: true})
		if resp := s.get("/health"); resp.Status != http.StatusOK {
			t.Errorf("GET /health status = %d, want 200", resp.Status)
		}
		if got := tableNames(t, db.DB); len(got) != 0 {
			t.Errorf("tables = %v, want none", got)
		}
		// AC3017: the repo-intel backfill cannot list repos on an empty database; it is logged
		// once and does not stop the startup.
		if failed := logLines(t, s.logs, "repo-intel backfill failed"); len(failed) != 1 {
			t.Errorf("repo-intel backfill failure log lines = %v, want exactly one", failed)
		}
	})
}

func TestStartup_SeedOffLeavesNoContext(t *testing.T) { // AC25
	s := startStack(t, spec{noSeed: true})
	resp := s.get("/settings")
	if resp.Status != http.StatusInternalServerError || resp.errorCode(t) != "internal_error" {
		t.Errorf("GET /settings = %d %s, want 500 internal_error", resp.Status, resp.Body)
	}
	resp = s.post("/settings/test-connection", map[string]string{"provider": "github"})
	if resp.Status != http.StatusOK {
		t.Errorf("POST /settings/test-connection = %d %s, want 200", resp.Status, resp.Body)
	}
}

func TestRun_OperatorCommands(t *testing.T) { // AC27-AC30 wiring, FR17
	db := pgtest.Empty(t)
	env := map[string]string{"DATABASE_URL": db.DSN, "DEVDIGEST_DATA_DIR": t.TempDir()}
	getenv := func(k string) string { return env[k] }
	runCmd := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(t.Context(), append([]string{"server"}, args...), getenv, &out)
		return out.String(), err
	}

	if out, err := runCmd("migrate", "status"); err != nil || !strings.Contains(out, "pending") {
		t.Errorf("migrate status on an empty database = %q, %v; want a pending version", out, err)
	}
	if out, err := runCmd("migrate", "up"); err != nil || !strings.Contains(out, "1 version(s) applied") {
		t.Errorf("migrate up = %q, %v; want 1 version applied", out, err)
	}
	if out, err := runCmd("migrate", "up"); err != nil || !strings.Contains(out, "0 version(s) applied") {
		t.Errorf("second migrate up = %q, %v; want 0 versions applied", out, err)
	}
	if out, err := runCmd("migrate", "status"); err != nil || !strings.Contains(out, "0001") || !strings.Contains(out, "applied 20") {
		t.Errorf("migrate status after up = %q, %v; want version 1 applied", out, err)
	}
	if out, err := runCmd("seed"); err != nil || !strings.Contains(out, "row(s) inserted") {
		t.Errorf("seed = %q, %v; want a row count", out, err)
	}
	if out, err := runCmd("migrate", "down"); err != nil || !strings.Contains(out, "reverted version 1") {
		t.Errorf("migrate down = %q, %v; want version 1 reverted", out, err)
	}
	if got := tableNames(t, db.DB); len(got) != 1 || got[0] != "schema_migrations" {
		t.Errorf("tables after migrate down = %v, want only schema_migrations", got)
	}
	if _, err := runCmd("bogus"); err == nil {
		t.Error("run(bogus) = nil, want an error")
	}
	if _, err := runCmd("migrate"); err == nil {
		t.Error("run(migrate) without a subcommand = nil, want an error")
	}
}
