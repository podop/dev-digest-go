package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/egress"
)

// Component tests of the GitLab connections (GitLab A plan S33: AC1-AC8, AC26) through the real
// server: create, validate, update, delete and test a connection, over a TLS stub GitLab on
// loopback (allowed) whose certificate is the extra CA of the server. The goldens the UI contract
// test validates are written by TestContracts_GitLabConnections. Helpers are prefixed gc (the
// other GitLab component tests share the package).

const (
	gcPath   = "/settings/gitlab-connections"
	gcToken  = "glpat-AAAAAAAAAAAAAAAAAAAA"
	gcToken2 = "glpat-BBBBBBBBBBBBBBBBBBBB"
	gcMsg401 = "GitLab rejected the token (401): it is wrong, expired or revoked"
)

// ---- the stub GitLab ----

// gcReq is one request the stub saw.
type gcReq struct {
	Method, Path, Query string
	Header              http.Header
}

// gcStub plays a GitLab instance over TLS on 127.0.0.1 and records every request. The handlers
// are keyed by the path below /api/v4 ("/user"); any other path answers 404.
type gcStub struct {
	srv    *httptest.Server
	caFile string

	mu       sync.Mutex
	reqs     []gcReq
	handlers map[string]http.HandlerFunc
	// release unblocks handlers that wait for the client to give up.
	release chan struct{}
}

func gcNewStub(t *testing.T) *gcStub {
	t.Helper()
	g := &gcStub{handlers: map[string]http.HandlerFunc{}, release: make(chan struct{})}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	t.Cleanup(func() { close(g.release) }) // runs before Close: a blocked handler would stall it
	g.caFile = filepath.Join(t.TempDir(), "gitlab-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.srv.Certificate().Raw})
	if err := os.WriteFile(g.caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	g.identity(gcIdent{scopes: []string{"api", "read_repository"}, expires: gcDay(30), version: "17.5.1-ee"})
	return g
}

func (g *gcStub) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	p := r.URL.EscapedPath()
	g.mu.Lock()
	g.reqs = append(g.reqs, gcReq{Method: r.Method, Path: p, Query: r.URL.RawQuery, Header: r.Header.Clone()})
	h := g.handlers[strings.TrimPrefix(p, "/api/v4")]
	g.mu.Unlock()
	if h == nil {
		gcJSON(http.StatusNotFound, `{"message":"404 Not Found"}`)(w, r)
		return
	}
	h(w, r)
}

// handle sets the answer of a path below /api/v4.
func (g *gcStub) handle(p string, h http.HandlerFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.handlers[p] = h
}

// requests returns what the stub saw below /api (git traffic and others are left out).
func (g *gcStub) requests() []gcReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []gcReq
	for _, r := range g.reqs {
		if strings.HasPrefix(r.Path, "/api/") {
			out = append(out, r)
		}
	}
	return out
}

func (g *gcStub) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = nil
}

// base is the instance URL a connection gets; hostKey is its host key.
func (g *gcStub) base() string    { return g.srv.URL }
func (g *gcStub) hostKey() string { return strings.TrimPrefix(g.srv.URL, "https://") }

// gcIdent is what the three calls of a connection test answer.
type gcIdent struct {
	scopes     []string
	selfStatus int    // 0 = 200 with the scopes and the expiry
	expires    string // "" = null
	version    string
	metaStatus int // 0 = 200 with the version
}

// identity programs /user (ann), /personal_access_tokens/self and /metadata.
func (g *gcStub) identity(id gcIdent) {
	g.handle("/user", gcJSON(http.StatusOK, `{"id":7,"username":"ann","name":"Ann"}`))
	if id.selfStatus != 0 {
		g.handle("/personal_access_tokens/self", gcJSON(id.selfStatus, `{"message":"400 Bad request"}`))
	} else {
		var expires any
		if id.expires != "" {
			expires = id.expires
		}
		body, _ := json.Marshal(map[string]any{"id": 1, "scopes": id.scopes, "expires_at": expires})
		g.handle("/personal_access_tokens/self", gcJSON(http.StatusOK, string(body)))
	}
	if id.metaStatus != 0 {
		g.handle("/metadata", gcJSON(id.metaStatus, `{"message":"403 Forbidden"}`))
	} else {
		g.handle("/metadata", gcJSON(http.StatusOK, fmt.Sprintf(`{"version":%q,"revision":"abc"}`, id.version)))
	}
}

// gcJSON answers every request with a status and a JSON body.
func gcJSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// gcDay is the UTC date n days after the fixed clock of the tests.
func gcDay(n int) string { return sbNow.AddDate(0, 0, n).Format(time.DateOnly) }

// ---- server and calls ----

// gcBoot starts the server over a fresh database; with a stub, its certificate is the extra CA
// (and the system bundle git would extend, so the test does not depend on the host's).
func gcBoot(t *testing.T, stub *gcStub, mutate ...func(*spec)) *cpStack {
	t.Helper()
	return gcBootOn(t, newSBEnv(t), stub, mutate...)
}

func gcBootOn(t *testing.T, e *sbEnv, stub *gcStub, mutate ...func(*spec)) *cpStack {
	t.Helper()
	if stub != nil {
		t.Setenv("SSL_CERT_FILE", stub.caFile)
		mutate = append([]func(*spec){func(sp *spec) { sp.config = func(c *config.Config) { c.ExtraCAFile = stub.caFile } }}, mutate...)
	}
	return cpBootOn(t, e, mutate...)
}

// gcCreate creates a connection and fails the test unless the answer is 201.
func gcCreate(t testing.TB, s *cpStack, base, token string) map[string]any {
	t.Helper()
	body := map[string]any{"base_url": base}
	if token != "" {
		body["token"] = token
	}
	resp := s.post(gcPath, body)
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST %s %s = %d %s, want 201", gcPath, base, resp.Status, resp.Body)
	}
	return resp.Obj(t)
}

func gcID(c map[string]any) string { id, _ := c["id"].(string); return id }

// gcConnTest runs the connection test of a connection.
func gcConnTest(s *cpStack, id, key string) response {
	s.t.Helper()
	body := map[string]any{"provider": "gitlab", "connection_id": id}
	if key != "" {
		body["key"] = key
	}
	return s.post("/settings/test-connection", body)
}

// gcList returns the connections as generic objects.
func gcList(t testing.TB, s *cpStack) []map[string]any {
	t.Helper()
	resp := s.get(gcPath)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET %s = %d %s, want 200", gcPath, resp.Status, resp.Body)
	}
	list, ok := resp.JSON(t).([]any)
	if !ok {
		t.Fatalf("GET %s body is not an array: %s", gcPath, resp.Body)
	}
	out := make([]map[string]any, len(list))
	for i, item := range list {
		out[i], _ = item.(map[string]any)
	}
	return out
}

// gcTokenOnDisk is the token secrets.json holds for a connection ("" = none).
func gcTokenOnDisk(t testing.TB, s *cpStack, id string) string {
	t.Helper()
	raw := s.secretsFile(t)
	if raw == "" {
		return ""
	}
	var data map[string]map[string]string
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("secrets.json is not a map of objects: %v", err)
	}
	return data["gitlab_connections."+id]["token"]
}

// gcLockSecrets makes the data directory read-only so that the next write of secrets.json fails,
// and returns the function that undoes it (also run when the test ends).
func gcLockSecrets(t *testing.T, s *cpStack) (unlock func()) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	if err := os.Chmod(s.cfg.DataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unlock = func() {
		once.Do(func() {
			if err := os.Chmod(s.cfg.DataDir, 0o700); err != nil {
				t.Errorf("restore the data directory mode: %v", err)
			}
		})
	}
	t.Cleanup(unlock)
	return unlock
}

// gcRepo inserts a GitLab repository of a workspace bound to a host key and returns its id.
func gcRepo(t testing.TB, e *sbEnv, ws, host string, project int, full, webURL string) string {
	t.Helper()
	return e.id(t, `INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url)
		VALUES (?, ?, ?, ?, 'gitlab', ?, ?, ?) RETURNING id::text`,
		ws, path.Dir(full), path.Base(full), full, host, project, webURL)
}

// gcRepoView returns the repository with the id from GET /repos.
func gcRepoView(t testing.TB, s *cpStack, id string) map[string]any {
	t.Helper()
	resp := s.get("/repos")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /repos = %d %s, want 200", resp.Status, resp.Body)
	}
	list, _ := resp.JSON(t).([]any)
	for _, item := range list {
		if m, _ := item.(map[string]any); m["id"] == id {
			return m
		}
	}
	t.Fatalf("GET /repos lists no repository %s: %s", id, resp.Body)
	return nil
}

// gcLogLines returns the log entries with a message.
func gcLogLines(t testing.TB, s *cpStack, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range s.logs.entries(t) {
		if e["msg"] == msg {
			out = append(out, e)
		}
	}
	return out
}

// gcPostRaw posts a create body from a goroutine: it returns the status instead of failing.
func gcPostRaw(s *cpStack, body any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.base+gcPath, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// ---- AC1: create ----

func TestGitLabConn_AC1_CreateNormalizesAndStoresTheToken(t *testing.T) { // AC1, FR1-FR4, EC2
	stub := gcNewStub(t)
	s := gcBoot(t, stub)
	s.leakCheck(gcToken, gcToken2)

	created := gcCreate(t, s, "https://GitLab.Corp.Example:443/gitlab/", gcToken)
	id := gcID(created)
	for k, want := range map[string]any{
		"name": "gitlab.corp.example", "host": "gitlab.corp.example", "base_url": "https://gitlab.corp.example/gitlab",
		"token_configured": true, "repo_count": float64(0),
	} {
		if created[k] != want {
			t.Errorf("created connection %s = %v, want %v", k, created[k], want)
		}
	}
	wantKeys := []string{"base_url", "created_at", "host", "id", "name", "repo_count", "token_configured", "updated_at"}
	var keys []string
	for k := range created {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if diff := cmp.Diff(wantKeys, keys); diff != "" {
		t.Errorf("created connection keys mismatch (-want +got):\n%s", diff)
	}

	list := gcList(t, s)
	if len(list) != 1 || list[0]["id"] != id || list[0]["token_configured"] != true {
		t.Errorf("GET %s = %v, want the created connection with a token", gcPath, list)
	}
	if got := gcTokenOnDisk(t, s, id); got != gcToken {
		t.Errorf("secrets.json holds token %q for the connection, want the one sent", got)
	}
	info, err := os.Stat(filepath.Join(s.cfg.DataDir, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secrets.json mode = %o, want 600", perm)
	}

	// Another port is another host key; a connection to the stub makes no request at all.
	second := gcCreate(t, s, "https://gitlab.corp.example:8443", "")
	if second["host"] != "gitlab.corp.example:8443" || second["name"] != "gitlab.corp.example:8443" || second["token_configured"] != false {
		t.Errorf("connection on port 8443 = %v, want host and name gitlab.corp.example:8443 and no token", second)
	}
	named := s.post(gcPath, map[string]any{"name": "  Corp stub  ", "base_url": stub.base(), "token": gcToken2})
	if named.Status != http.StatusCreated || named.Obj(t)["name"] != "Corp stub" || named.Obj(t)["host"] != stub.hostKey() {
		t.Errorf("POST with a name and the stub URL = %d %s, want 201 with the trimmed name", named.Status, named.Body)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Errorf("the stub GitLab saw %d request(s) while connections were created, want none", len(reqs))
	}
	if n := s.rows(t, "gitlab_connections"); n != 3 {
		t.Errorf("gitlab_connections rows = %d, want 3", n)
	}
}

// ---- AC2: refused base URLs ----

func TestGitLabConn_AC2_RefusesBadBaseURLs(t *testing.T) { // AC2, FR2, EC1
	s := gcBoot(t, nil)
	s.leakCheck(gcToken)
	tooLong := "https://gitlab.example/" + strings.Repeat("a", 2049-len("https://gitlab.example/"))
	if len(tooLong) != 2049 {
		t.Fatalf("test URL has %d characters, want 2049", len(tooLong))
	}
	for _, base := range []string{
		"http://gitlab.example", "https://u:p@gitlab.example", "https://gitlab.example/?a=1", "https://gitlab.example/#x",
		"https://gitlab.com/api/v4", "https://github.com", "https://169.254.169.254", "https://[fe80::1]",
		"https://0.0.0.0", tooLong,
	} {
		t.Run(fmt.Sprintf("%.40s", base), func(t *testing.T) {
			resp := s.post(gcPath, map[string]any{"base_url": base, "token": gcToken})
			if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
				t.Fatalf("POST %s = %d %s, want 422 validation_error", base, resp.Status, resp.Body)
			}
			if diff := cmp.Diff([]string{"/base_url custom"}, cpIssues(t, resp)); diff != "" {
				t.Errorf("POST %s issues mismatch (-want +got):\n%s", base, diff)
			}
		})
	}
	if n := s.rows(t, "gitlab_connections"); n != 0 {
		t.Errorf("gitlab_connections rows after the refused creates = %d, want 0", n)
	}
	if got := s.secretsFile(t); got != "" {
		t.Errorf("secrets.json after the refused creates = %q, want no file", got)
	}
}

func TestGitLabConn_AC2_AHostNameResolvingToLinkLocalIsNotCalled(t *testing.T) { // AC2, NFR2
	stub := gcNewStub(t)
	ccFakeDNS(t, map[string]string{"metadata.gitlab.test": "169.254.169.254"})
	s := gcBoot(t, stub)
	s.leakCheck(gcToken)

	// The name passes the create; the address it resolves to at call time does not.
	id := gcID(gcCreate(t, s, "https://metadata.gitlab.test", gcToken))
	resp := gcConnTest(s, id, "")
	got := resp.Obj(t)
	if resp.Status != http.StatusOK || got["ok"] != false || got["message"] != egress.ForbiddenMessage {
		t.Errorf("test of a host resolving to 169.254.169.254 = %d %s, want ok:false %q", resp.Status, resp.Body, egress.ForbiddenMessage)
	}
	if reqs := stub.requests(); len(reqs) != 0 {
		t.Errorf("the stub saw %d request(s), want none", len(reqs))
	}
}

// ---- AC3: duplicates, races, the limit, unknown ids ----

func TestGitLabConn_AC3_OnePerHostAndAtMostTwenty(t *testing.T) { // AC3, FR1, FR3, EC3
	s := gcBoot(t, nil)
	first := gcCreate(t, s, "https://gitlab.corp.example", "")
	for _, base := range []string{"https://gitlab.corp.example/other", "https://GITLAB.corp.example:443"} {
		resp := s.post(gcPath, map[string]any{"base_url": base})
		if resp.Status != http.StatusConflict || resp.errorCode(t) != "gitlab_connection_exists" {
			t.Errorf("POST %s with the host taken = %d %s, want 409 gitlab_connection_exists", base, resp.Status, resp.Body)
		}
	}

	t.Run("concurrent creates for one host give one 201", func(t *testing.T) {
		const n = 8
		statuses := make([]int, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				statuses[i], errs[i] = gcPostRaw(s, map[string]any{"base_url": "https://race.example", "token": gcToken})
			})
		}
		wg.Wait()
		created, conflicts := 0, 0
		for i := range n {
			switch {
			case errs[i] != nil:
				t.Errorf("concurrent POST %d: %v", i, errs[i])
			case statuses[i] == http.StatusCreated:
				created++
			case statuses[i] == http.StatusConflict:
				conflicts++
			}
		}
		if created != 1 || conflicts != n-1 {
			t.Errorf("%d concurrent POSTs for one host gave %d x 201 and %d x 409 (%v), want 1 and %d", n, created, conflicts, statuses, n-1)
		}
		if got := scalar[int](t, s.db.DB, `SELECT count(*) FROM gitlab_connections WHERE host = 'race.example'`); got != 1 {
			t.Errorf("rows for race.example = %d, want 1", got)
		}
		s.leakCheck(gcToken)
	})

	t.Run("the 21st connection is refused", func(t *testing.T) {
		have := s.rows(t, "gitlab_connections")
		for i := have; i < 20; i++ {
			gcCreate(t, s, fmt.Sprintf("https://h%02d.example", i), "")
		}
		resp := s.post(gcPath, map[string]any{"base_url": "https://one-too-many.example"})
		if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "gitlab_connection_limit" {
			t.Errorf("the 21st POST = %d %s, want 422 gitlab_connection_limit", resp.Status, resp.Body)
		}
		if n := s.rows(t, "gitlab_connections"); n != 20 {
			t.Errorf("gitlab_connections rows = %d, want 20", n)
		}
	})

	t.Run("an unknown id is a 404", func(t *testing.T) {
		unknown := "00000000-0000-4000-8000-00000000dead"
		for _, id := range []string{unknown, "not-a-uuid"} {
			for _, method := range []string{http.MethodPut, http.MethodDelete} {
				var body any
				if method == http.MethodPut {
					body = map[string]any{"name": "x"}
				}
				resp := s.do(method, gcPath+"/"+id, body)
				if resp.Status != http.StatusNotFound || resp.errorCode(t) != "gitlab_connection_not_found" {
					t.Errorf("%s %s/%s = %d %s, want 404 gitlab_connection_not_found", method, gcPath, id, resp.Status, resp.Body)
				}
			}
		}
		if list := gcList(t, s); len(list) != 20 || !slices.ContainsFunc(list, func(c map[string]any) bool { return c["id"] == gcID(first) }) {
			t.Errorf("the connection list changed by the 404s: %d entries", len(list))
		}
	})
}

// ---- AC4: update ----

func TestGitLabConn_AC4_UpdateFollowsThePrefixKeepsOrReplacesTheToken(t *testing.T) { // AC4, FR4-FR6, EC4
	e := newSBEnv(t)
	s := gcBootOn(t, e, nil)
	s.leakCheck(gcToken, gcToken2)
	id := gcID(gcCreate(t, s, "https://gitlab.corp.example/gitlab", gcToken))
	repo := gcRepo(t, e, e.ws, "gitlab.corp.example", 5, "acme/web", "https://gitlab.corp.example/stored/acme/web")
	one := gcPath + "/" + id

	if got := gcRepoView(t, s, repo); got["web_url"] != "https://gitlab.corp.example/gitlab/acme/web" || got["connection_id"] != id {
		t.Errorf("repository before the update: web_url %v, connection_id %v, want the connection's prefix and id", got["web_url"], got["connection_id"])
	}

	t.Run("a new path prefix reaches the bound repositories", func(t *testing.T) {
		resp := s.put(one, map[string]any{"base_url": "https://gitlab.corp.example/git2"})
		if resp.Status != http.StatusOK || resp.Obj(t)["base_url"] != "https://gitlab.corp.example/git2" || resp.Obj(t)["repo_count"] != float64(1) {
			t.Fatalf("PUT a new prefix = %d %s, want 200 with the new base_url and repo_count 1", resp.Status, resp.Body)
		}
		if got := gcRepoView(t, s, repo); got["web_url"] != "https://gitlab.corp.example/git2/acme/web" {
			t.Errorf("repository web_url after the update = %v, want the new prefix", got["web_url"])
		}
	})

	t.Run("another host is refused and nothing changes", func(t *testing.T) {
		before := gcList(t, s)
		for _, base := range []string{"https://other.example/gitlab", "https://gitlab.corp.example:8443/gitlab"} {
			resp := s.put(one, map[string]any{"base_url": base, "name": "Changed"})
			if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
				t.Fatalf("PUT host %s = %d %s, want 422", base, resp.Status, resp.Body)
			}
			if diff := cmp.Diff([]string{"/base_url custom"}, cpIssues(t, resp)); diff != "" {
				t.Errorf("PUT host %s issues mismatch (-want +got):\n%s", base, diff)
			}
		}
		if diff := cmp.Diff(before, gcList(t, s)); diff != "" {
			t.Errorf("the list changed by the refused PUTs (-before +after):\n%s", diff)
		}
	})

	t.Run("the token is kept, removed and replaced", func(t *testing.T) {
		if resp := s.put(one, map[string]any{"name": "Corp"}); resp.Status != http.StatusOK || resp.Obj(t)["token_configured"] != true || resp.Obj(t)["name"] != "Corp" {
			t.Fatalf("PUT without token = %d %s, want 200 with the token kept", resp.Status, resp.Body)
		}
		if got := gcTokenOnDisk(t, s, id); got != gcToken {
			t.Errorf("token on disk after a PUT without token = %q, want the old one", got)
		}
		if resp := s.put(one, map[string]any{"token": nil}); resp.Status != http.StatusOK || resp.Obj(t)["token_configured"] != false {
			t.Fatalf("PUT token:null = %d %s, want 200 token_configured false", resp.Status, resp.Body)
		}
		if got := gcTokenOnDisk(t, s, id); got != "" {
			t.Errorf("token on disk after token:null = %q, want none", got)
		}
		if resp := s.put(one, map[string]any{"token": gcToken2}); resp.Status != http.StatusOK || resp.Obj(t)["token_configured"] != true {
			t.Fatalf("PUT a new token = %d %s, want 200 token_configured true", resp.Status, resp.Body)
		}
		if got := gcTokenOnDisk(t, s, id); got != gcToken2 {
			t.Errorf("token on disk after the replace = %q, want the new one", got)
		}
	})

	t.Run("a secrets write failure on create is a 500 and leaves nothing", func(t *testing.T) { // EC26
		before := gcList(t, s)
		unlock := gcLockSecrets(t, s)
		resp := s.post(gcPath, map[string]any{"base_url": "https://new.example", "token": gcToken})
		unlock()
		if resp.Status != http.StatusInternalServerError {
			t.Errorf("POST with an unwritable secrets file = %d %s, want 500", resp.Status, resp.Body)
		}
		if diff := cmp.Diff(before, gcList(t, s)); diff != "" {
			t.Errorf("the list after the failed create (-before +after):\n%s", diff)
		}
		if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM gitlab_connections WHERE host = 'new.example'`); n != 0 {
			t.Errorf("rows for new.example = %d, want 0", n)
		}
	})
}

func TestGitLabConn_AC4_ANewTokenIsUsedByTheNextRequest(t *testing.T) { // AC4, FR6
	stub := gcNewStub(t)
	s := gcBoot(t, stub)
	s.leakCheck(gcToken, gcToken2)
	id := gcID(gcCreate(t, s, stub.base(), gcToken))

	check := func(token string) {
		t.Helper()
		stub.reset()
		if resp := gcConnTest(s, id, ""); resp.Obj(t)["ok"] != true {
			t.Fatalf("test of the connection = %s, want ok", resp.Body)
		}
		reqs := stub.requests()
		if len(reqs) == 0 {
			t.Fatal("the stub saw no request")
		}
		for _, r := range reqs {
			if got := r.Header.Get("PRIVATE-TOKEN"); got != token {
				t.Errorf("%s %s carried PRIVATE-TOKEN %q, want %q", r.Method, r.Path, got, token)
			}
		}
	}
	check(gcToken)
	if resp := s.put(gcPath+"/"+id, map[string]any{"token": gcToken2}); resp.Status != http.StatusOK {
		t.Fatalf("PUT a new token = %d %s, want 200", resp.Status, resp.Body)
	}
	check(gcToken2)
}

// ---- AC5: delete ----

func TestGitLabConn_AC5_DeleteDisconnectsRepositoriesAndRemovesTheToken(t *testing.T) { // AC5, FR7, FR8, FR16, EC5
	e := newSBEnv(t)
	s := gcBootOn(t, e, nil)
	s.leakCheck(gcToken)
	const host = "gitlab.corp.example"
	id := gcID(gcCreate(t, s, "https://"+host+"/gitlab", gcToken))
	other := e.id(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id::text`)
	r1 := gcRepo(t, e, e.ws, host, 5, "acme/web", "https://"+host+"/stored/acme/web")
	r2 := gcRepo(t, e, other, host, 6, "ops/api", "https://"+host+"/stored/ops/api")
	for ws, repo := range map[string]string{e.ws: r1, other: r2} {
		e.exec(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, status)
			VALUES (?, ?, 1, 'MR', 'ann', 'feature', 'main', 'abc', 'open')`, ws, repo)
	}
	if list := gcList(t, s); len(list) != 1 || list[0]["repo_count"] != float64(2) {
		t.Fatalf("GET %s = %v, want repo_count 2", gcPath, list)
	}
	counts := func() map[string]int64 {
		c := sbTableCounts(t, e.db.DB)
		delete(c, "gitlab_connections")
		return c
	}
	before := counts()

	t.Run("a secrets write failure keeps the connection and the token", func(t *testing.T) {
		unlock := gcLockSecrets(t, s)
		resp := s.del(gcPath + "/" + id)
		unlock()
		if resp.Status != http.StatusInternalServerError {
			t.Errorf("DELETE with an unwritable secrets file = %d %s, want 500", resp.Status, resp.Body)
		}
		if list := gcList(t, s); len(list) != 1 || list[0]["id"] != id || list[0]["token_configured"] != true {
			t.Errorf("GET %s after the failed delete = %v, want the connection with its token", gcPath, list)
		}
		if got := gcTokenOnDisk(t, s, id); got != gcToken {
			t.Errorf("token on disk after the failed delete = %q, want it kept", got)
		}
	})

	resp := s.del(gcPath + "/" + id)
	if resp.Status != http.StatusOK {
		t.Fatalf("DELETE = %d %s, want 200", resp.Status, resp.Body)
	}
	if diff := cmp.Diff(map[string]any{"deleted": id, "disconnected_repos": float64(2)}, resp.Obj(t)); diff != "" {
		t.Errorf("DELETE body mismatch (-want +got):\n%s", diff)
	}
	if got := gcTokenOnDisk(t, s, id); got != "" {
		t.Errorf("token on disk after the delete = %q, want none", got)
	}
	if list := gcList(t, s); len(list) != 0 {
		t.Errorf("GET %s after the delete = %v, want none", gcPath, list)
	}

	// Both workspaces still list their repository, not connected, with the stored address.
	park := func(name string) { e.exec(t, `UPDATE workspaces SET name = ? WHERE id = ?`, name, e.ws) }
	check := func(repo, wantURL string) {
		t.Helper()
		got := gcRepoView(t, s, repo)
		if got["connection_id"] != nil || got["web_url"] != wantURL || got["provider"] != "gitlab" {
			t.Errorf("repository %s after the delete: connection_id %v, web_url %v, provider %v, want null, %s, gitlab",
				repo, got["connection_id"], got["web_url"], got["provider"], wantURL)
		}
	}
	check(r1, "https://"+host+"/stored/acme/web")
	park("parked") // the server serves the workspace named default
	e.exec(t, `UPDATE workspaces SET name = 'default' WHERE id = ?`, other)
	check(r2, "https://"+host+"/stored/ops/api")
	e.exec(t, `UPDATE workspaces SET name = 'other' WHERE id = ?`, other)
	park("default")

	if diff := cmp.Diff(before, counts()); diff != "" {
		t.Errorf("table row counts changed by the delete (-before +after):\n%s", diff)
	}
}

// ---- AC6, AC7: the connection test ----

func TestGitLabConn_AC6_TestMakesThreeCallsAndReportsTheToken(t *testing.T) { // AC6, FR9-FR11
	stub := gcNewStub(t)
	s := gcBoot(t, stub)
	s.leakCheck(gcToken, gcToken2)
	id := gcID(gcCreate(t, s, stub.base(), gcToken))

	resp := gcConnTest(s, id, "")
	if resp.Status != http.StatusOK {
		t.Fatalf("test-connection = %d %s, want 200", resp.Status, resp.Body)
	}
	got := resp.Obj(t)
	latency, isNum := got["latency_ms"].(float64)
	if !isNum || latency < 0 {
		t.Errorf("latency_ms = %v, want a number >= 0", got["latency_ms"])
	}
	delete(got, "latency_ms")
	want := map[string]any{
		"provider": "gitlab", "ok": true, "message": "Connected as @ann", "login": "ann",
		"scopes": []any{"api", "read_repository"}, "expires_at": gcDay(30), "instance_version": "17.5.1-ee",
		"token_access": "full", "warnings": []any{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("test-connection mismatch (-want +got):\n%s", diff)
	}
	var seen []string
	for _, r := range stub.requests() {
		seen = append(seen, r.Method+" "+r.Path)
		if tok := r.Header.Get("PRIVATE-TOKEN"); tok != gcToken {
			t.Errorf("%s %s carried PRIVATE-TOKEN %q, want the connection's token", r.Method, r.Path, tok)
		}
	}
	if diff := cmp.Diff([]string{"GET /api/v4/user", "GET /api/v4/personal_access_tokens/self", "GET /api/v4/metadata"}, seen); diff != "" {
		t.Errorf("requests of the stub mismatch (-want +got):\n%s", diff)
	}

	t.Run("without connection_id", func(t *testing.T) {
		resp := s.post("/settings/test-connection", map[string]any{"provider": "gitlab"})
		if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
			t.Fatalf("test-connection without connection_id = %d %s, want 422", resp.Status, resp.Body)
		}
		if diff := cmp.Diff([]string{"/connection_id invalid_type"}, cpIssues(t, resp)); diff != "" {
			t.Errorf("issues mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("an unknown id saves nothing", func(t *testing.T) {
		stub.reset()
		before := s.secretsFile(t)
		resp := gcConnTest(s, "00000000-0000-4000-8000-00000000dead", gcToken2)
		if resp.Status != http.StatusNotFound || resp.errorCode(t) != "gitlab_connection_not_found" {
			t.Errorf("test-connection of an unknown id = %d %s, want 404 gitlab_connection_not_found", resp.Status, resp.Body)
		}
		if after := s.secretsFile(t); after != before || strings.Contains(after, gcToken2) {
			t.Errorf("secrets.json changed by the test of an unknown id")
		}
		if reqs := stub.requests(); len(reqs) != 0 {
			t.Errorf("the stub saw %d request(s) for an unknown id, want none", len(reqs))
		}
	})
}

func TestGitLabConn_AC6_AKeyIsSavedFirstAndKeptAfterA401(t *testing.T) { // AC6, FR9
	stub := gcNewStub(t)
	s := gcBoot(t, stub)
	s.leakCheck(gcToken)
	id := gcID(gcCreate(t, s, stub.base(), ""))
	stub.handle("/user", gcJSON(http.StatusUnauthorized, `{"message":"401 Unauthorized"}`))

	resp := gcConnTest(s, id, gcToken)
	got := resp.Obj(t)
	if resp.Status != http.StatusOK || got["ok"] != false || got["message"] != gcMsg401 {
		t.Errorf("test with a rejected key = %d %s, want ok:false %q", resp.Status, resp.Body, gcMsg401)
	}
	if tok := gcTokenOnDisk(t, s, id); tok != gcToken {
		t.Errorf("token on disk after the 401 = %q, want the key kept", tok)
	}
	if list := gcList(t, s); len(list) != 1 || list[0]["token_configured"] != true {
		t.Errorf("GET %s after the 401 = %v, want token_configured true", gcPath, list)
	}
	reqs := stub.requests()
	if len(reqs) != 1 || reqs[0].Header.Get("PRIVATE-TOKEN") != gcToken {
		t.Errorf("requests after a 401 = %v, want one /user call with the key", reqs)
	}
}

func TestGitLabConn_AC7_TokenVariants(t *testing.T) { // AC7, FR11, EC7-EC9
	stub := gcNewStub(t)
	s := gcBoot(t, stub)
	s.leakCheck(gcToken)
	id := gcID(gcCreate(t, s, stub.base(), gcToken))
	rw := []string{"read_api", "read_repository"}

	tests := []struct {
		name    string
		ident   gcIdent
		ok      bool
		access  any
		scopes  any
		expires any
		version any
		codes   []string
		message string // "" = "Connected as @ann"
	}{
		{name: "read_api and read_repository", ident: gcIdent{scopes: rw, version: "17.5.1-ee"}, ok: true, access: "read_only",
			scopes: []any{"read_api", "read_repository"}, version: "17.5.1-ee", codes: []string{"read_only_token"}},
		{name: "api", ident: gcIdent{scopes: []string{"api"}, version: "17.5.1-ee"}, ok: true, access: "full",
			scopes: []any{"api"}, version: "17.5.1-ee"},
		{name: "read_api only", ident: gcIdent{scopes: []string{"read_api"}, version: "17.5.1-ee"}, ok: true, access: "read_only",
			scopes: []any{"read_api"}, version: "17.5.1-ee", codes: []string{"read_only_token", "no_repository_scope"}},
		{name: "read_user only", ident: gcIdent{scopes: []string{"read_user"}, version: "17.5.1-ee"}, message: "The token needs the api or read_api scope"},
		{name: "scopes endpoint 400", ident: gcIdent{selfStatus: http.StatusBadRequest, version: "17.5.1-ee"}, ok: true,
			version: "17.5.1-ee", codes: []string{"scopes_unknown"}},
		{name: "no expiry", ident: gcIdent{scopes: []string{"api"}, version: "17.0.0"}, ok: true, access: "full",
			scopes: []any{"api"}, version: "17.0.0"},
		{name: "expires in 14 days", ident: gcIdent{scopes: []string{"api"}, expires: gcDay(14), version: "17.5.1-ee"}, ok: true, access: "full",
			scopes: []any{"api"}, expires: gcDay(14), version: "17.5.1-ee", codes: []string{"token_expiring"}},
		{name: "expires in 15 days", ident: gcIdent{scopes: []string{"api"}, expires: gcDay(15), version: "17.5.1-ee"}, ok: true, access: "full",
			scopes: []any{"api"}, expires: gcDay(15), version: "17.5.1-ee"},
		{name: "version 16.11.3-ee", ident: gcIdent{scopes: []string{"api"}, version: "16.11.3-ee"}, ok: true, access: "full",
			scopes: []any{"api"}, version: "16.11.3-ee", codes: []string{"version_unsupported"}},
		{name: "metadata 403", ident: gcIdent{scopes: []string{"api"}, metaStatus: http.StatusForbidden}, ok: true, access: "full",
			scopes: []any{"api"}, codes: []string{"version_unknown"}},
		{name: "all warnings in order", ident: gcIdent{scopes: []string{"read_api"}, expires: gcDay(14), version: "16.11.3-ee"}, ok: true, access: "read_only",
			scopes: []any{"read_api"}, expires: gcDay(14), version: "16.11.3-ee",
			codes: []string{"read_only_token", "no_repository_scope", "token_expiring", "version_unsupported"}},
		{name: "scopes and version unknown", ident: gcIdent{selfStatus: http.StatusBadRequest, metaStatus: http.StatusForbidden}, ok: true,
			codes: []string{"scopes_unknown", "version_unknown"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub.identity(tc.ident)
			resp := gcConnTest(s, id, "")
			if resp.Status != http.StatusOK {
				t.Fatalf("test-connection = %d %s, want 200", resp.Status, resp.Body)
			}
			got := resp.Obj(t)
			codes := []string{}
			warnings, _ := got["warnings"].([]any)
			for _, w := range warnings {
				m, _ := w.(map[string]any)
				codes = append(codes, fmt.Sprint(m["code"]))
				if msg, _ := m["message"].(string); msg == "" {
					t.Errorf("warning %v has no message", m["code"])
				}
			}
			wantCodes := tc.codes
			if wantCodes == nil {
				wantCodes = []string{}
			}
			if diff := cmp.Diff(wantCodes, codes); diff != "" {
				t.Errorf("warning codes mismatch (-want +got):\n%s", diff)
			}
			wantMsg := tc.message
			if tc.ok {
				wantMsg = "Connected as @ann"
			}
			for k, want := range map[string]any{
				"ok": tc.ok, "message": wantMsg, "token_access": tc.access, "scopes": tc.scopes, "expires_at": tc.expires, "instance_version": tc.version,
			} {
				if !cmp.Equal(want, got[k]) {
					t.Errorf("%s = %v, want %v", k, got[k], want)
				}
			}
		})
	}
}

// ---- AC8: failed tests ----

func TestGitLabConn_AC8_FailedTestsAreDataAndLeakNothing(t *testing.T) { // AC8, FR10, EC6, NFR1, NFR2, NFR9
	stub := gcNewStub(t)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the redirect target received %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(elsewhere.Close)
	s := gcBoot(t, stub)
	s.leakCheck(gcToken)
	id := gcID(gcCreate(t, s, stub.base(), gcToken))

	tests := []struct {
		name    string
		h       http.HandlerFunc
		message string
	}{
		{name: "redirect to another host", message: "GitLab answered with a redirect — check the base URL",
			h: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", elsewhere.URL+"/steal")
				w.WriteHeader(http.StatusFound)
			}},
		{name: "an HTML page", message: "No GitLab API found at " + stub.base(),
			h: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, "<html><body>Sign in</body></html>")
			}},
		{name: "401", message: gcMsg401, h: gcJSON(http.StatusUnauthorized, `{"message":"401 Unauthorized `+gcToken+`"}`)},
		{name: "a delay past the 15 s limit", message: "GitLab is not reachable at " + stub.base(),
			h: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-stub.release:
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub.handle("/user", tc.h)
			stub.reset()
			beforeSettings := len(gcLogLines(t, s, "settings test connection"))
			beforeConn := len(gcLogLines(t, s, "gitlab connection test"))

			resp := gcConnTest(s, id, "")
			got := resp.Obj(t)
			if resp.Status != http.StatusOK || got["ok"] != false || got["message"] != tc.message {
				t.Fatalf("test-connection = %d %s, want 200 ok:false %q", resp.Status, resp.Body, tc.message)
			}
			for _, k := range []string{"latency_ms", "login", "scopes", "expires_at", "instance_version", "token_access"} {
				if v, present := got[k]; !present || v != nil {
					t.Errorf("failed test: %s = %v (present %v), want null", k, v, present)
				}
			}
			if reqs := stub.requests(); len(reqs) != 1 || reqs[0].Path != "/api/v4/user" {
				t.Errorf("stub requests after a failed /user = %v, want only /user", reqs)
			}

			// AC8: exactly one INFO line per test, with provider, host, ok and latency_ms; the
			// settings service adds none of its own for GitLab.
			if lines := gcLogLines(t, s, "settings test connection"); len(lines) != beforeSettings {
				t.Errorf("settings test connection lines = %d, want %d (none for GitLab)", len(lines), beforeSettings)
			}
			conn := gcLogLines(t, s, "gitlab connection test")
			if len(conn) != beforeConn+1 {
				t.Fatalf("gitlab connection test lines = %d, want %d", len(conn), beforeConn+1)
			}
			l := conn[len(conn)-1]
			if l["level"] != "INFO" || l["provider"] != "gitlab" || l["host"] != stub.hostKey() || l["ok"] != false {
				t.Errorf("connection test log line = %v, want INFO provider gitlab, the host key and ok false", l)
			}
			if _, ok := l["latency_ms"].(float64); !ok {
				t.Errorf("connection test log line has no latency_ms: %v", l)
			}
		})
	}
}

// ---- AC26: one log line per GitLab call ----

func TestGitLabConn_AC26_EveryCallLogsOneCleanLine(t *testing.T) { // AC26, NFR9, NFR10
	const (
		title = `<img src=x onerror=alert("TITLE-MARK")>`
		descr = "DESCRIPTION-MARK secret plans"
		diff  = "DIFF-MARK"
	)
	stub := gcNewStub(t)
	stub.handle("/projects/acme%2Fpayments", gcJSON(http.StatusOK, fmt.Sprintf(
		`{"id":42,"path":"payments","path_with_namespace":"acme/payments","default_branch":"main","web_url":%q,"namespace":{"full_path":"acme"}}`,
		stub.base()+"/acme/payments")))
	stub.handle("/projects/42", gcJSON(http.StatusOK, fmt.Sprintf(
		`{"id":42,"path":"payments","path_with_namespace":"acme/payments","default_branch":"main","web_url":%q,"namespace":{"full_path":"acme"}}`,
		stub.base()+"/acme/payments")))
	mr, _ := json.Marshal([]map[string]any{{
		"iid": 3, "title": title, "description": descr, "state": "opened", "draft": false,
		"source_branch": "feature", "target_branch": "main", "sha": "0123456789abcdef0123456789abcdef01234567",
		"source_project_id": 42, "target_project_id": 42, "created_at": "2026-10-20T10:00:00Z", "updated_at": "2026-10-21T10:00:00Z",
		"merged_at": nil, "closed_at": nil, "web_url": stub.base() + "/acme/payments/-/merge_requests/3",
		"author": map[string]any{"username": "ann"}, "changes_marker": diff,
	}})
	stub.handle("/projects/42/merge_requests", gcJSON(http.StatusOK, string(mr)))
	stub.handle("/api/graphql", gcJSON(http.StatusOK, `{"data":{"projects":{"nodes":[{"id":"gid://gitlab/Project/42","mergeRequests":{"nodes":[`+
		`{"iid":"3","diffStatsSummary":{"additions":4,"deletions":1,"fileCount":2}}]}}]}}}`))

	s := gcBoot(t, stub)
	s.leakCheck(gcToken, descr, diff)
	id := gcID(gcCreate(t, s, stub.base(), gcToken))

	if resp := gcConnTest(s, id, ""); resp.Obj(t)["ok"] != true {
		t.Fatalf("test-connection = %s, want ok", resp.Body)
	}
	add := s.post("/repos", map[string]any{"url": stub.base() + "/acme/payments"})
	if add.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", add.Status, add.Body)
	}
	repoID, _ := add.Obj(t)["id"].(string)
	pulls := s.get("/repos/" + repoID + "/pulls")
	if pulls.Status != http.StatusOK {
		t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", repoID, pulls.Status, pulls.Body)
	}
	var list []map[string]any
	if err := json.Unmarshal(pulls.Body, &list); err != nil || len(list) != 1 || list[0]["title"] != title {
		t.Errorf("PR list = %s (%v), want the one MR with its title kept as text", pulls.Body, err)
	}

	var ops []string
	for _, msg := range []string{"gitlab call", "gitlab call failed"} {
		for _, line := range gcLogLines(t, s, msg) {
			op, _ := line["op"].(string)
			ops = append(ops, op)
			if line["host"] != stub.hostKey() {
				t.Errorf("%s line host = %v, want %s", op, line["host"], stub.hostKey())
			}
			if _, ok := line["status"].(float64); !ok {
				t.Errorf("%s line has no status: %v", op, line)
			}
			if a, ok := line["attempt"].(float64); !ok || a != 1 {
				t.Errorf("%s line attempt = %v, want 1", op, line["attempt"])
			}
			if _, ok := line["latency_ms"].(float64); !ok {
				t.Errorf("%s line has no latency_ms: %v", op, line)
			}
		}
	}
	slices.Sort(ops)
	// One line per call: the test makes three, the add one, the list sync a project, a page and the stats.
	want := []string{"graphql_stats", "merge_requests", "metadata", "project", "project", "token_self", "user"}
	if diff := cmp.Diff(want, ops); diff != "" {
		t.Errorf("GitLab call log lines (op) mismatch (-want +got):\n%s", diff)
	}
	if n := len(stub.requests()); n != len(want) {
		t.Errorf("the stub saw %d API request(s), want %d (one per log line)", n, len(want))
	}
	for _, line := range strings.Split(s.logs.String(), "\n") {
		for _, secret := range []string{title, "TITLE-MARK", descr, diff} {
			if strings.Contains(line, secret) {
				t.Errorf("a log line contains %q: %.300s", secret, line)
			}
		}
	}
}

// ---- the contract goldens ----

// TestContracts_GitLabConnections records the answers the UI contract test validates (S37). The
// clock is fixed, so the expiry dates are stable; the stub's port never appears in an answer.
func TestContracts_GitLabConnections(t *testing.T) {
	stub := gcNewStub(t)
	s := gcBoot(t, stub)
	g := sbGoldens(t, s.stack)
	s.leakCheck(gcToken, gcToken2)

	create := s.post(gcPath, map[string]any{"base_url": "https://GitLab.Corp.Example:443/gitlab/", "token": gcToken})
	g.check("gitlab_conn_create", create, http.StatusCreated)
	corp := gcID(create.Obj(t))
	g.check("gitlab_conn_create_no_token", s.post(gcPath, map[string]any{"name": "Cloud", "base_url": "https://gitlab.com"}), http.StatusCreated)
	g.check("gitlab_conn_update", s.put(gcPath+"/"+corp, map[string]any{"name": "Corp", "base_url": "https://gitlab.corp.example/git2", "token": nil}), http.StatusOK)
	g.check("gitlab_conn_list", s.get(gcPath), http.StatusOK)
	g.check("gitlab_conn_exists", s.post(gcPath, map[string]any{"base_url": "https://gitlab.corp.example/other"}), http.StatusConflict)
	g.check("gitlab_conn_invalid", s.post(gcPath, map[string]any{"base_url": "http://gitlab.example/api/v4", "token": "has space"}), http.StatusUnprocessableEntity)
	g.check("gitlab_conn_not_found", s.put(gcPath+"/00000000-0000-4000-8000-00000000dead", map[string]any{"name": "x"}), http.StatusNotFound)

	id := gcID(gcCreate(t, s, stub.base(), gcToken2))
	g.check("gitlab_conn_test_ok", gcConnTest(s, id, ""), http.StatusOK)
	stub.identity(gcIdent{scopes: []string{"read_api"}, expires: gcDay(14), version: "16.11.3-ee"})
	g.check("gitlab_conn_test_warnings", gcConnTest(s, id, ""), http.StatusOK)
	stub.identity(gcIdent{selfStatus: http.StatusBadRequest, metaStatus: http.StatusForbidden})
	g.check("gitlab_conn_test_unknown", gcConnTest(s, id, ""), http.StatusOK)
	stub.identity(gcIdent{scopes: []string{"read_user"}, version: "17.5.1-ee"})
	g.check("gitlab_conn_test_no_scope", gcConnTest(s, id, ""), http.StatusOK)
	stub.handle("/user", gcJSON(http.StatusUnauthorized, `{"message":"401 Unauthorized"}`))
	g.check("gitlab_conn_test_rejected", gcConnTest(s, id, ""), http.StatusOK)

	g.check("gitlab_conn_deleted", s.del(gcPath+"/"+corp), http.StatusOK)
}
