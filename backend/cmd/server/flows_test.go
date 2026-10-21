package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/jobs"
	"dev-digest-go/backend/internal/settings/adapters/providers"
)

var update = flag.Bool("update", false, "rewrite the golden contract files in testdata/contracts")

// securityHeaders are the headers of spec section 7.1 on every response.
var securityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self';base-uri 'self';font-src 'self' https: data:;form-action 'self';frame-ancestors 'self';" +
		"img-src 'self' data:;object-src 'none';script-src 'self';script-src-attr 'none';style-src 'self' https: 'unsafe-inline';upgrade-insecure-requests",
	"Cross-Origin-Opener-Policy":        "same-origin",
	"Cross-Origin-Resource-Policy":      "same-origin",
	"Origin-Agent-Cluster":              "?1",
	"Referrer-Policy":                   "no-referrer",
	"Strict-Transport-Security":         "max-age=31536000; includeSubDomains",
	"X-Content-Type-Options":            "nosniff",
	"X-Dns-Prefetch-Control":            "off",
	"X-Download-Options":                "noopen",
	"X-Frame-Options":                   "SAMEORIGIN",
	"X-Permitted-Cross-Domain-Policies": "none",
	"X-Xss-Protection":                  "0",
}

func TestHTTP_UnknownRoutes(t *testing.T) { // AC1, AC2, AC20
	s := startStack(t, spec{})

	resp := s.get("/agents")
	want := map[string]any{"error": map[string]any{"code": "not_found", "message": "Route GET /agents not found"}}
	if resp.Status != http.StatusNotFound {
		t.Errorf("GET /agents status = %d, want 404", resp.Status)
	}
	if diff := cmp.Diff(want, resp.JSON(t)); diff != "" {
		t.Errorf("GET /agents body mismatch (-want +got):\n%s", diff)
	}

	resp = s.post("/health?x=1", nil)
	wantMsg := "Route POST /health not found"
	got, _ := resp.Obj(t)["error"].(map[string]any)
	if resp.Status != http.StatusNotFound || got["message"] != wantMsg {
		t.Errorf("POST /health?x=1 = %d %s, want 404 %q", resp.Status, resp.Body, wantMsg)
	}

	for path, status := range map[string]int{"/repos": http.StatusOK, "/nope": http.StatusNotFound} {
		resp := s.do(http.MethodHead, path, nil)
		if resp.Status != status || len(resp.Body) != 0 {
			t.Errorf("HEAD %s = %d with %d body bytes, want %d with none", path, resp.Status, len(resp.Body), status)
		}
	}
}

func TestHTTP_SecurityHeadersOnEveryResponse(t *testing.T) { // AC16
	s := startStack(t, spec{})
	for _, path := range []string{"/settings", "/agents", "/pulls/abc"} {
		resp := s.get(path)
		for name, want := range securityHeaders {
			if got := resp.Header.Get(name); got != want {
				t.Errorf("GET %s: header %s = %q, want %q", path, name, got, want)
			}
		}
		if got := resp.Header.Get("X-Powered-By"); got != "" {
			t.Errorf("GET %s: X-Powered-By = %q, want none", path, got)
		}
	}
	if resp := s.get("/pulls/abc"); resp.Status != http.StatusUnprocessableEntity {
		t.Errorf("GET /pulls/abc = %d, want 422", resp.Status)
	}
}

func TestRateLimit(t *testing.T) {
	t.Run("test-connection bucket of 20 per window", func(t *testing.T) { // AC12, AC13
		s := startStack(t, spec{rateLimit: true})
		for i := 1; i <= 20; i++ {
			if resp := s.post("/settings/test-connection", map[string]string{"provider": "openrouter"}); resp.Status == http.StatusTooManyRequests {
				t.Fatalf("test-connection request %d was limited, want only request 21", i)
			}
		}
		resp := s.post("/settings/test-connection", map[string]string{"provider": "openrouter"})
		if resp.Status != http.StatusTooManyRequests {
			t.Fatalf("test-connection request 21 = %d, want 429", resp.Status)
		}
		e, _ := resp.Obj(t)["error"].(map[string]any)
		if e["code"] != "internal_error" || !regexp.MustCompile(`^Rate limit exceeded, retry in (\d+ seconds?|1 minute)$`).MatchString(fmt.Sprint(e["message"])) {
			t.Errorf("429 error = %v, want internal_error with a retry message", e)
		}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || secs < 1 || secs > 60 {
			t.Errorf("Retry-After = %q, want an integer in 1..60", resp.Header.Get("Retry-After"))
		}
		if resp := s.get("/settings"); resp.Status != http.StatusOK {
			t.Errorf("GET /settings after the bucket ran out = %d, want 200", resp.Status)
		}
	})

	t.Run("global limit of 120 per window, health exempt", func(t *testing.T) { // AC14
		s := startStack(t, spec{rateLimit: true})
		for i := 1; i <= 120; i++ {
			if resp := s.get("/settings"); resp.Status != http.StatusOK {
				t.Fatalf("GET /settings request %d = %d, want 200", i, resp.Status)
			}
		}
		if resp := s.get("/settings"); resp.Status != http.StatusTooManyRequests {
			t.Errorf("GET /settings request 121 = %d, want 429", resp.Status)
		}
		for _, path := range []string{"/health", "/health/ready"} {
			if resp := s.get(path); resp.Status != http.StatusOK {
				t.Errorf("GET %s while limited = %d, want 200", path, resp.Status)
			}
		}
	})

	t.Run("switched off", func(t *testing.T) { // AC15
		s := startStack(t, spec{rateLimit: false})
		for i := 1; i <= 200; i++ {
			if resp := s.get("/settings"); resp.Status == http.StatusTooManyRequests {
				t.Fatalf("GET /settings request %d was limited with limiting off", i)
			}
		}
	})
}

func TestHealth_DatabaseDown(t *testing.T) { // AC21
	s := startStack(t, spec{})
	s.db.Drop(t)

	health := s.get("/health")
	if health.Status != http.StatusOK {
		t.Errorf("GET /health status = %d, want 200", health.Status)
	}
	if diff := cmp.Diff(map[string]any{"status": "ok"}, health.JSON(t)); diff != "" {
		t.Errorf("GET /health body mismatch (-want +got):\n%s", diff)
	}
	resp := s.get("/health/ready")
	if resp.Status != http.StatusServiceUnavailable {
		t.Errorf("GET /health/ready status = %d, want 503", resp.Status)
	}
	if diff := cmp.Diff(map[string]any{"ready": false}, resp.JSON(t)); diff != "" {
		t.Errorf("GET /health/ready body mismatch (-want +got):\n%s", diff)
	}
	resp = s.get("/repos")
	if resp.Status != http.StatusInternalServerError || resp.errorCode(t) != "internal_error" {
		t.Errorf("GET /repos = %d %s, want 500 internal_error", resp.Status, resp.Body)
	}
}

func TestSecrets_SavedTokenIsUsedWithoutRestart(t *testing.T) { // AC46
	s := startStack(t, spec{})
	resp := s.post("/settings/test-connection", map[string]string{"provider": "github", "key": "ghp_saved"})
	if resp.Status != http.StatusOK || resp.Obj(t)["message"] != "Connected as @octo" {
		t.Fatalf("test-connection = %d %s, want 200 Connected as @octo", resp.Status, resp.Body)
	}

	acme := repoByName(t, s, "acme/payments-api")
	if resp := s.get("/repos/" + acme + "/pulls"); resp.Status != http.StatusOK {
		t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", acme, resp.Status, resp.Body)
	}
	var listCalls int
	for _, r := range s.gh.requests() {
		if strings.HasSuffix(r.Path, "/pulls") {
			listCalls++
			if r.Auth != "Bearer ghp_saved" {
				t.Errorf("PR list request Authorization = %q, want the saved token", r.Auth)
			}
		}
	}
	if listCalls == 0 {
		t.Error("the GitHub stub received no PR list request")
	}
}

// repoByName returns the id of the listed repo with that full name.
func repoByName(t testing.TB, s *stack, fullName string) string {
	t.Helper()
	for _, r := range s.get("/repos").JSON(t).([]any) {
		m := r.(map[string]any)
		if m["full_name"] == fullName {
			return m["id"].(string)
		}
	}
	t.Fatalf("repo %s is not listed", fullName)
	return ""
}

func TestRepos_AddDuplicateAndRefreshJobs(t *testing.T) { // AC49, AC50, AC55
	s := startStack(t, spec{})

	resp := s.post("/repos", map[string]string{"url": " https://github.com/o/r.git "})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	repo := resp.Obj(t)
	for key, want := range map[string]any{"owner": "o", "name": "r", "full_name": "o/r", "default_branch": "main", "clone_path": nil} {
		if repo[key] != want {
			t.Errorf("created repo %s = %v, want %v", key, repo[key], want)
		}
	}
	id := repo["id"].(string)
	s.waitJobs("clone", 1)
	if n := len(s.gh.requests()); n != 0 {
		t.Errorf("GitHub stub received %d request(s) while adding a repo, want none", n)
	}

	resp = s.post("/repos", map[string]string{"url": "git@github.com:o/r"})
	if resp.Status != http.StatusOK || resp.Obj(t)["id"] != id {
		t.Errorf("duplicate POST /repos = %d %s, want 200 with id %s", resp.Status, resp.Body, id)
	}
	if n := len(jobRows(t, s.db.DB, "")); n != 1 {
		t.Errorf("jobs after the duplicate POST = %d, want 1", n)
	}

	// clone_path is null: refresh enqueues a clone.
	resp = s.post("/repos/"+id+"/refresh", nil)
	if resp.Status != http.StatusOK || string(bytes.TrimSpace(resp.Body)) != `{"status":"refreshing"}` {
		t.Errorf("POST /repos/:id/refresh = %d %s, want 200 {\"status\":\"refreshing\"}", resp.Status, resp.Body)
	}
	s.waitJobs("clone", 2)

	// clone_path is set: refresh enqueues a refresh.
	if err := s.db.DB.Exec(`UPDATE repos SET clone_path = ? WHERE id = ?`, filepath.Join(s.cfg.CloneDir, "o", "r"), id).Error; err != nil {
		t.Fatal(err)
	}
	if resp := s.post("/repos/"+id+"/refresh", nil); resp.Status != http.StatusOK {
		t.Fatalf("second refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	s.waitJobs("refresh", 1)
	if n, want := len(jobRows(t, s.db.DB, "clone")), 2; n != want {
		t.Errorf("clone jobs = %d, want %d", n, want)
	}
}

func TestJobs_CloneFromLocalRemote(t *testing.T) { // AC62, AC101
	remote := t.TempDir()
	makeRemote(t, remote, "o", "r")
	s := startStack(t, spec{remoteDir: remote})

	if resp := s.post("/repos", map[string]string{"url": "https://github.com/o/r"}); resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	rows := s.waitJobs("clone", 1)
	row := rows[0]
	if row.Status != "done" || row.Attempts != 1 || row.StartedAt == nil || row.FinishedAt == nil {
		t.Fatalf("clone job = %+v, want done, attempts 1, started and finished set", row)
	}

	clonePath := filepath.Join(s.cfg.CloneDir, "o", "r")
	var repo map[string]any
	for _, r := range s.get("/repos").JSON(t).([]any) {
		if m := r.(map[string]any); m["full_name"] == "o/r" {
			repo = m
		}
	}
	if repo["clone_path"] != clonePath {
		t.Errorf("clone_path = %v, want %s", repo["clone_path"], clonePath)
	}
	if repo["last_polled_at"] == nil {
		t.Error("last_polled_at is null after a clone, want it set")
	}
	if got := gitRun(t, clonePath, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("commits in the clone = %s, want 1 (depth 1)", got)
	}

	lines := logLines(t, s.logs, "job finished")
	if len(lines) != 1 {
		t.Fatalf("job log lines = %v, want exactly one", lines)
	}
	l := lines[0]
	if l["kind"] != "clone" || l["job_id"] != row.ID || l["status"] != "done" || l["attempts"] != float64(1) {
		t.Errorf("job log line = %v, want kind clone, job_id %s, status done, attempts 1", l, row.ID)
	}
	if _, ok := l["duration_ms"]; !ok {
		t.Errorf("job log line %v has no duration_ms", l)
	}
}

func TestJobs_CloneOfMissingSourceFails(t *testing.T) { // AC63
	s := startStack(t, spec{}) // the remote directory is empty
	if resp := s.post("/repos", map[string]string{"url": "https://github.com/o/missing"}); resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	row := s.waitJobs("clone", 1)[0]
	if row.Status != "failed" || row.Error == nil || *row.Error == "" || row.Attempts != 0 {
		t.Fatalf("clone job = %+v, want failed with an error and 0 attempts", row)
	}
	if took := row.FinishedAt.Sub(*row.StartedAt); took >= 120*time.Second {
		t.Errorf("clone job took %s, want under 120s", took)
	}
	if got := scalar[*string](t, s.db.DB, `SELECT clone_path FROM repos WHERE full_name = 'o/missing'`); got != nil {
		t.Errorf("clone_path = %q, want null", *got)
	}
}

func TestJobs_TokenNeverReachesArgvConfigLogsOrErrors(t *testing.T) { // AC70
	const token = "ghp_secret_x"
	remote := t.TempDir()
	makeRemote(t, remote, "o", "r")
	binary, callLog := gitWrapper(t, false)
	s := startStack(t, spec{remoteDir: remote, gitBinary: binary, env: map[string]string{"GITHUB_TOKEN": token}})
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))

	id := s.post("/repos", map[string]string{"url": "https://github.com/o/r"}).Obj(t)["id"].(string)
	s.waitJobs("clone", 1)
	if resp := s.post("/repos/"+id+"/refresh", nil); resp.Status != http.StatusOK {
		t.Fatalf("refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	s.waitJobs("refresh", 1)
	// A clone that fails with the token configured must not echo it either.
	if resp := s.post("/repos", map[string]string{"url": "https://github.com/o/gone"}); resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos o/gone = %d, want 201", resp.Status)
	}
	rows := s.waitJobs("clone", 2)

	for _, r := range rows {
		if r.Status == "failed" && r.Error != nil {
			assertNoSecret(t, "jobs.error", *r.Error, token, basic)
		}
	}
	if got := rows[0].Status + "/" + rows[1].Status; got != "done/failed" && got != "failed/done" {
		t.Errorf("clone job statuses = %s, want one done and one failed", got)
	}
	assertNoSecret(t, "server log", s.logs.String(), token, basic)

	clone := filepath.Join(s.cfg.CloneDir, "o", "r")
	cfgFile, err := os.ReadFile(filepath.Join(clone, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, ".git/config", string(cfgFile), token, basic)
	assertNoSecret(t, "git remote -v", gitRun(t, clone, "remote", "-v"), token, basic)

	raw, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	var sawHeader bool
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case line == "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader":
			sawHeader = true
		case strings.HasPrefix(line, "GIT_CONFIG_VALUE_0="):
			if line != "GIT_CONFIG_VALUE_0=Authorization: Basic "+basic {
				t.Errorf("auth header env = %q, want the basic credential", line)
			}
		default:
			assertNoSecret(t, "git argv", line, token, basic)
		}
	}
	if !sawHeader {
		t.Errorf("git never received the per-command header for https://github.com/; calls:\n%s", raw)
	}
}

func assertNoSecret(t testing.TB, where, text string, secrets ...string) {
	t.Helper()
	for _, sec := range secrets {
		if strings.Contains(text, sec) {
			t.Errorf("%s contains the secret %q", where, sec)
		}
	}
}

func TestLogging_NoSecretsAndOneLinePerRequest(t *testing.T) { // AC98
	const (
		bearer = "abc123xyz"
		apiKey = "sk-zzz"
		token  = "ghp_log_token"
	)
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s"}}`, apiKey)
	}))
	t.Cleanup(openai.Close)
	s := startStack(t, spec{
		env:       map[string]string{"GITHUB_TOKEN": token},
		providers: providers.Options{OpenAIBaseURL: openai.URL},
	})

	if resp := s.do(http.MethodGet, "/repos", nil, "Authorization", "Bearer "+bearer); resp.Status != http.StatusOK {
		t.Fatalf("GET /repos = %d, want 200", resp.Status)
	}
	resp := s.post("/settings/test-connection", map[string]string{"provider": "openai", "key": apiKey})
	msg, _ := resp.Obj(t)["message"].(string)
	if resp.Status != http.StatusOK || resp.Obj(t)["ok"] != false || !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("test-connection = %d %s, want ok:false with the key redacted", resp.Status, resp.Body)
	}
	assertNoSecret(t, "test-connection response", string(resp.Body), apiKey)
	s.post("/repos", map[string]string{"url": "https://github.com/o/gone"})
	s.waitJobs("clone", 1)

	assertNoSecret(t, "server log", s.logs.String(), bearer, apiKey, token)
	var sawGET, sawPOST bool
	for _, e := range logLines(t, s.logs, "request") {
		for _, key := range []string{"method", "path", "status", "duration_ms"} {
			if _, ok := e[key]; !ok {
				t.Errorf("request log line %v lacks %q", e, key)
			}
		}
		sawGET = sawGET || (e["method"] == "GET" && e["path"] == "/repos" && e["status"] == float64(200))
		sawPOST = sawPOST || (e["method"] == "POST" && e["path"] == "/settings/test-connection" && e["status"] == float64(200))
	}
	if !sawGET || !sawPOST {
		t.Errorf("request log lines missing: GET /repos = %v, POST /settings/test-connection = %v", sawGET, sawPOST)
	}
}

func TestShutdown_DrainsRequestsAndStopsJobs(t *testing.T) { // AC94
	binary, _ := gitWrapper(t, true)
	s := startStack(t, spec{
		gitBinary: binary,
		jobs:      jobs.Options{Concurrency: 1},
		env:       map[string]string{"GITHUB_TOKEN": "tok"},
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/pulls") {
			return false
		}
		once.Do(func() { close(entered) })
		<-release
		_, _ = fmt.Fprint(w, "[]")
		return true
	})

	first := s.post("/repos", map[string]string{"url": "https://github.com/o/slow"}).Obj(t)["id"].(string)
	s.post("/repos", map[string]string{"url": "https://github.com/o/queued"})
	eventually(t, 30*time.Second, "the first clone job to run", func() bool {
		rows := jobRows(t, s.db.DB, "clone")
		return len(rows) == 2 && rows[0].Status == "running"
	})

	polled := make(chan response, 1)
	go func() { polled <- s.post("/repos/"+first+"/poll", nil) }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the poll request never reached the GitHub stub")
	}

	start := time.Now()
	s.cancel() // SIGTERM
	eventually(t, 10*time.Second, "the listener to close", func() bool {
		conn, err := (&http.Client{Timeout: time.Second}).Get(s.base + "/health")
		if err == nil {
			_ = conn.Body.Close()
			return false
		}
		return true
	})
	close(release)

	select {
	case resp := <-polled:
		if resp.Status != http.StatusOK {
			t.Errorf("in-flight poll = %d %s, want 200 after the drain", resp.Status, resp.Body)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the in-flight request did not complete")
	}
	if err := s.stop(); err != nil {
		t.Errorf("serve() = %v after the stop signal, want nil", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("shutdown took %s, want within 20s", took)
	}

	rows := jobRows(t, s.db.DB, "clone")
	if len(rows) != 2 || rows[0].Status != "failed" || rows[1].Status != "queued" {
		t.Errorf("job statuses after shutdown = %+v, want first failed and second queued", rows)
	}
}

// ---- contract goldens (AC26) ----

var (
	uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	timePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`)
	wireTime    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
)

// goldens normalizes volatile values (ids, "now" timestamps, the clone directory) and compares
// or rewrites testdata/contracts/<name>.json.
type goldens struct {
	t        *testing.T
	cloneDir string
	ids      map[string]string
}

func (g *goldens) check(name string, resp response, wantStatus int) {
	g.t.Helper()
	if resp.Status != wantStatus {
		g.t.Fatalf("%s: status = %d, want %d; body %s", name, resp.Status, wantStatus, resp.Body)
	}
	text := strings.ReplaceAll(string(resp.Body), g.cloneDir, "/data/workspace")
	text = uuidPattern.ReplaceAllStringFunc(text, func(id string) string {
		id = strings.ToLower(id)
		if _, ok := g.ids[id]; !ok {
			g.ids[id] = fmt.Sprintf("00000000-0000-4000-8000-%012d", len(g.ids)+1)
		}
		return g.ids[id]
	})
	text = timePattern.ReplaceAllString(text, "2026-01-01T00:00:00.000Z")

	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		g.t.Fatalf("%s: body is not JSON: %v", name, err)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		g.t.Fatal(err)
	}
	pretty = append(pretty, '\n')

	path := filepath.Join("testdata", "contracts", name+".json")
	if *update {
		if err := os.WriteFile(path, pretty, 0o600); err != nil {
			g.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		g.t.Fatalf("%s: %v (run go test ./cmd/server -run Contracts -update)", name, err)
	}
	if diff := cmp.Diff(string(want), string(pretty)); diff != "" {
		g.t.Errorf("%s: response differs from %s (-want +got):\n%s", name, path, diff)
	}
}

func TestContracts_GoldenResponses(t *testing.T) { // AC26
	remote := t.TempDir()
	makeRemote(t, remote, "o", "r")
	s := startStack(t, spec{remoteDir: remote})
	g := &goldens{t: t, cloneDir: s.cfg.CloneDir, ids: map[string]string{}}

	g.check("health", s.get("/health"), http.StatusOK)
	g.check("health_ready", s.get("/health/ready"), http.StatusOK)
	g.check("settings_get", s.get("/settings"), http.StatusOK)
	g.check("settings_put", s.do(http.MethodPut, "/settings", map[string]any{
		"theme":             "light",
		"automatic_reviews": true,
		"feature_models":    map[string]any{"risk_brief": map[string]string{"provider": "openai", "model": "gpt-4.1"}},
	}), http.StatusOK)

	// The seeded repo has no GitHub token yet: the stored rows are served.
	acme := repoByName(t, s, "acme/payments-api")
	list := s.get("/repos/" + acme + "/pulls")
	g.check("acme_pulls_list", list, http.StatusOK)
	acmePR := list.JSON(t).([]any)[0].(map[string]any)["id"].(string)
	g.check("acme_pull_detail", s.get("/pulls/"+acmePR), http.StatusOK)

	g.check("test_connection", s.post("/settings/test-connection", map[string]string{"provider": "github", "key": "ghp_contract"}), http.StatusOK)
	g.check("secrets_status", s.get("/settings/secrets-status"), http.StatusOK)

	created := s.post("/repos", map[string]string{"url": "https://github.com/o/r"})
	g.check("repos_create", created, http.StatusCreated)
	repo := created.Obj(t)["id"].(string)
	s.waitJobs("clone", 1)
	g.check("repos_list", s.get("/repos"), http.StatusOK)
	g.check("workspace", s.get("/workspace"), http.StatusOK)

	g.check("repo_poll", s.post("/repos/"+repo+"/poll", nil), http.StatusOK)
	pulls := s.get("/repos/" + repo + "/pulls")
	g.check("pulls_list", pulls, http.StatusOK)
	pr := pulls.JSON(t).([]any)[0].(map[string]any)["id"].(string)
	g.check("pull_detail", s.get("/pulls/"+pr), http.StatusOK)
	g.check("pull_comments", s.get("/pulls/"+pr+"/comments"), http.StatusOK)
	g.check("pull_comment_create", s.post("/pulls/"+pr+"/comments", map[string]any{"path": "a.ts", "line": 3, "body": "x"}), http.StatusOK)

	g.check("repo_refresh", s.post("/repos/"+repo+"/refresh", nil), http.StatusOK)
	s.waitJobs("refresh", 1)
	g.check("repo_delete", s.do(http.MethodDelete, "/repos/"+repo, nil), http.StatusOK)
}

func TestContracts_TimestampsUseWireFormat(t *testing.T) { // AC22
	s := startStack(t, spec{})
	for _, r := range s.get("/repos").JSON(t).([]any) {
		repo := r.(map[string]any)
		for _, key := range []string{"id", "workspace_id", "owner", "name", "full_name", "default_branch", "clone_path", "last_polled_at", "created_by"} {
			if _, ok := repo[key]; !ok {
				t.Errorf("repo %v lacks key %q", repo["full_name"], key)
			}
		}
		if at, ok := repo["last_polled_at"].(string); ok && !wireTime.MatchString(at) {
			t.Errorf("last_polled_at = %q, want ISO-8601 UTC with milliseconds", at)
		}
	}
}
