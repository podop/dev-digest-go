package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/settings/adapters/providers"
)

// Component tests of Settings slice A through the real server (plan S27): test-connection with the
// model check, GitHub scopes, the new Settings keys, GET /about and GET /settings/spend, the guards
// of the new routes, and the contract goldens the UI contract test reads (NFR9). The spend wiring
// itself (cap read from the bag, 402 on an entry point) is covered by spend_wiring_test.go; the cap
// semantics by spend_cap_test.go.

// secretKey is a recognisable provider key: neither it nor a recognisable part of it may appear in
// a response or a log line (NFR1).
const secretKey = "sk-or-v1-LEAKCANARY0xQ7Zw9pLm3vT"

// keyFragments are the parts of secretKey that are searched for besides the whole key.
var keyFragments = []string{secretKey, "LEAKCANARY0xQ7", "Q7Zw9pLm3vT"}

// modelStub plays a provider's model-list endpoint and records every request it sees.
type modelStub struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []string // "METHOD path" of each request
}

func newModelStub(t testing.TB, handler http.HandlerFunc) *modelStub {
	t.Helper()
	m := &modelStub{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.got = append(m.got, r.Method+" "+r.URL.Path)
		m.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// listing answers a model list with the given ids.
func listing(ids ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		data := make([]string, len(ids))
		for i, id := range ids {
			data[i] = fmt.Sprintf(`{"id":%q}`, id)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(data, ","))
	}
}

func (m *modelStub) requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.got)
}

// assertNoKey fails when text holds the whole key or a recognisable part of it.
func assertNoKey(t testing.TB, where, text string) {
	t.Helper()
	assertNoSecret(t, where, text, keyFragments...)
}

// issuePaths returns the path of every issue of a 422 envelope.
func issuePaths(t testing.TB, resp response) []string {
	t.Helper()
	e, _ := resp.Obj(t)["error"].(map[string]any)
	details, _ := e["details"].([]any)
	var out []string
	for _, d := range details {
		params, _ := d.(map[string]any)["params"].(map[string]any)
		issue, _ := params["issue"].(map[string]any)
		path, _ := issue["path"].([]any)
		out = append(out, fmt.Sprint(path))
	}
	return out
}

// putSettings sends PUT /settings with a single key.
func putSettings(s *stack, key string, value any) response {
	s.t.Helper()
	return s.do(http.MethodPut, "/settings", map[string]any{key: value})
}

// connTest posts a test-connection request and requires a 200.
func connTest(t testing.TB, s *stack, provider, key string) map[string]any {
	t.Helper()
	body := map[string]string{"provider": provider}
	if key != "" {
		body["key"] = key
	}
	resp := s.post("/settings/test-connection", body)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /settings/test-connection %s = %d %s, want 200", provider, resp.Status, resp.Body)
	}
	return resp.Obj(t)
}

// assertFailedTest checks an ok:false answer: 200 with null extras and no key in it.
func assertFailedTest(t testing.TB, got map[string]any, raw string) {
	t.Helper()
	if got["ok"] != false {
		t.Errorf("test-connection ok = %v, want false; body %s", got["ok"], raw)
	}
	for _, k := range []string{"latency_ms", "model", "model_reachable", "login", "scopes"} {
		if v, present := got[k]; present && v != nil {
			t.Errorf("test-connection %s = %v, want null on a failed test", k, v)
		}
	}
	assertNoKey(t, "test-connection response", raw)
}

func TestSettings_TestConnectionChecksTheModel(t *testing.T) { // AC4, NFR2
	stub := newModelStub(t, listing("a/x", "deepseek/deepseek-v4-flash"))
	s := startStack(t, spec{providers: providers.Options{OpenRouterBaseURL: stub.srv.URL}})

	got := connTest(t, s, "openrouter", secretKey)
	if got["ok"] != true || got["model"] != "deepseek/deepseek-v4-flash" || got["model_reachable"] != true {
		t.Errorf("test-connection without default_model = %v, want ok, deepseek/deepseek-v4-flash, reachable", got)
	}
	if ms, ok := got["latency_ms"].(float64); !ok || ms < 0 {
		t.Errorf("test-connection latency_ms = %v, want a number >= 0", got["latency_ms"])
	}
	if want := []string{"GET /models"}; !slices.Equal(stub.requests(), want) {
		t.Errorf("provider requests after one test = %v, want %v", stub.requests(), want)
	}

	if put := putSettings(s, "default_model", map[string]string{"provider": "openrouter", "model": "a/y"}); put.Status != http.StatusOK {
		t.Fatalf("PUT /settings default_model = %d %s, want 200", put.Status, put.Body)
	}
	got = connTest(t, s, "openrouter", "")
	if got["ok"] != true || got["model"] != "a/y" || got["model_reachable"] != false {
		t.Errorf("test-connection with default_model a/y = %v, want ok, a/y, not reachable", got)
	}
	if want := []string{"GET /models", "GET /models"}; !slices.Equal(stub.requests(), want) {
		t.Errorf("provider requests after two tests = %v, want exactly one GET /models per attempt", stub.requests())
	}

	// A default_model of another provider does not change what this provider checks.
	putSettings(s, "default_model", map[string]string{"provider": "openai", "model": "gpt-4.1"})
	if got := connTest(t, s, "openrouter", ""); got["model"] != "deepseek/deepseek-v4-flash" || got["model_reachable"] != true {
		t.Errorf("test-connection with an OpenAI default_model = %v, want the OpenRouter reference model", got)
	}
}

func TestSettings_TestConnectionFailuresAreOkFalse(t *testing.T) { // AC5, EC1, NFR1, NFR2, NFR6
	other := newModelStub(t, listing("a/x"))
	redirect := newModelStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+"/models", http.StatusFound)
	})
	broken := newModelStub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"boom for `+secretKey+`"}}`, http.StatusInternalServerError)
	})
	slow := newModelStub(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})

	cases := []struct {
		name string
		opts providers.Options
	}{
		{"302 to another host", providers.Options{OpenRouterBaseURL: redirect.srv.URL}},
		{"500", providers.Options{OpenRouterBaseURL: broken.srv.URL}},
		{"delay past the timeout", providers.Options{OpenRouterBaseURL: slow.srv.URL, OpenRouterTimeout: 150 * time.Millisecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := startStack(t, spec{providers: tc.opts})
			resp := s.post("/settings/test-connection", map[string]string{"provider": "openrouter", "key": secretKey})
			if resp.Status != http.StatusOK {
				t.Fatalf("POST /settings/test-connection = %d %s, want 200", resp.Status, resp.Body)
			}
			assertFailedTest(t, resp.Obj(t), string(resp.Body))

			assertNoKey(t, "server log", s.logs.String())
			lines := logLines(t, s.logs, "settings test connection")
			if len(lines) != 1 || lines[0]["provider"] != "openrouter" || lines[0]["ok"] != false || lines[0]["latency_ms"] == nil {
				t.Errorf("test log lines = %v, want one with provider openrouter, ok false and latency_ms", lines)
			}
		})
	}
	if got := other.requests(); len(got) != 0 {
		t.Errorf("the redirect target received %v, want no request (a 302 is not followed)", got)
	}
}

func TestSettings_TestConnectionSuccessKeepsTheKeyOutOfResponseAndLogs(t *testing.T) { // NFR1, NFR6
	stub := newModelStub(t, listing("a/x", "deepseek/deepseek-v4-flash"))
	s := startStack(t, spec{providers: providers.Options{OpenRouterBaseURL: stub.srv.URL}})

	resp := s.post("/settings/test-connection", map[string]string{"provider": "openrouter", "key": secretKey})
	if resp.Status != http.StatusOK || resp.Obj(t)["ok"] != true {
		t.Fatalf("POST /settings/test-connection = %d %s, want an ok test", resp.Status, resp.Body)
	}
	assertNoKey(t, "test-connection response", string(resp.Body))

	// Every route that reports on settings, spend or the build answers without the key.
	if put := putSettings(s, "monthly_spend_cap_usd", 0); put.Status != http.StatusOK {
		t.Fatalf("PUT /settings cap = %d %s, want 200", put.Status, put.Body)
	}
	assertNoKey(t, "PUT /settings response", string(putSettings(s, "default_model", map[string]string{"provider": "openrouter", "model": "a/x"}).Body))
	for _, path := range []string{"/settings", "/settings/secrets-status", "/settings/spend", "/about"} {
		assertNoKey(t, "GET "+path, string(s.get(path).Body))
	}
	acme := repoByName(t, s, "acme/payments-api")
	pr := s.get("/repos/" + acme + "/pulls").JSON(t).([]any)[0].(map[string]any)["id"].(string)
	refused := startReview(t, s, pr, map[string]any{"all": true})
	if refused.Status != http.StatusPaymentRequired {
		t.Fatalf("POST /pulls/%s/review with the cap reached = %d %s, want 402", pr, refused.Status, refused.Body)
	}
	assertNoKey(t, "402 response", string(refused.Body))

	if got := logLines(t, s.logs, "settings test connection"); len(got) != 1 {
		t.Errorf("test log lines = %d, want 1", len(got))
	}
	assertNoKey(t, "server log", s.logs.String())
}

func TestSettings_GitHubScopes(t *testing.T) { // AC6, FR16, EC3
	s := startStack(t, spec{})
	user := func(scopes *string) {
		s.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/user" {
				return false
			}
			if scopes != nil {
				w.Header().Set("X-OAuth-Scopes", *scopes)
			}
			_, _ = fmt.Fprint(w, `{"login":"ann"}`)
			return true
		})
	}
	str := func(v string) *string { return &v }

	cases := []struct {
		name       string
		header     *string
		wantScopes any
	}{
		{"scopes listed", str("repo, read:org"), []any{"repo", "read:org"}},
		{"header empty", str(""), []any{}},
		{"header absent", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user(tc.header)
			got := connTest(t, s, "github", "ghp_scopes_token")
			if got["ok"] != true || got["login"] != "ann" || got["message"] != "Connected as @ann" {
				t.Errorf("test-connection github = %v, want ok, login ann, \"Connected as @ann\"", got)
			}
			if diff := cmp.Diff(tc.wantScopes, got["scopes"]); diff != "" {
				t.Errorf("test-connection github scopes mismatch (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("302 is not followed", func(t *testing.T) {
		other := newModelStub(t, listing())
		s.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/user" {
				return false
			}
			http.Redirect(w, r, other.srv.URL+"/user", http.StatusFound)
			return true
		})
		resp := s.post("/settings/test-connection", map[string]string{"provider": "github", "key": "ghp_scopes_token"})
		assertFailedTest(t, resp.Obj(t), string(resp.Body))
		if got := other.requests(); len(got) != 0 {
			t.Errorf("the redirect target received %v, want no request", got)
		}
	})
}

func TestSettings_DefaultModelAndSpendCapRoundTrip(t *testing.T) { // AC7, FR7, FR11, EC4
	s := startStack(t, spec{})
	get := func(key string) any { return s.get("/settings").Obj(t)[key] }

	t.Run("default_model", func(t *testing.T) {
		want := map[string]any{"provider": "openrouter", "model": "a/x"}
		if put := putSettings(s, "default_model", want); put.Status != http.StatusOK {
			t.Fatalf("PUT default_model = %d %s, want 200", put.Status, put.Body)
		}
		if diff := cmp.Diff(want, get("default_model")); diff != "" {
			t.Errorf("GET /settings default_model mismatch (-want +got):\n%s", diff)
		}
		if put := putSettings(s, "default_model", nil); put.Status != http.StatusOK {
			t.Fatalf("PUT default_model null = %d %s, want 200", put.Status, put.Body)
		}
		if got := get("default_model"); got != nil {
			t.Errorf("GET /settings default_model after null = %v, want null", got)
		}

		putSettings(s, "default_model", want)
		for name, bad := range map[string]any{
			"unknown provider":   map[string]any{"provider": "foo", "model": "a/x"},
			"missing model":      map[string]any{"provider": "openrouter"},
			"empty model":        map[string]any{"provider": "openrouter", "model": ""},
			"missing provider":   map[string]any{"model": "a/x"},
			"not an object":      "openrouter/a/x",
			"unknown provider 2": map[string]any{"provider": "github", "model": "a/x"},
		} {
			resp := putSettings(s, "default_model", bad)
			if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
				t.Errorf("PUT default_model (%s) = %d %s, want 422 validation_error", name, resp.Status, resp.Body)
				continue
			}
			if paths := issuePaths(t, resp); len(paths) == 0 || !strings.HasPrefix(paths[0], "[default_model") {
				t.Errorf("PUT default_model (%s) issue paths = %v, want a path under default_model", name, paths)
			}
		}
		if diff := cmp.Diff(want, get("default_model")); diff != "" {
			t.Errorf("default_model after rejected updates mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("monthly_spend_cap_usd", func(t *testing.T) {
		for _, ok := range []any{50, 0, 12.34, 1000000, nil} {
			if put := putSettings(s, "monthly_spend_cap_usd", ok); put.Status != http.StatusOK {
				t.Errorf("PUT monthly_spend_cap_usd %v = %d %s, want 200", ok, put.Status, put.Body)
				continue
			}
			var want any // JSON numbers decode as float64
			if n, isNum := ok.(int); isNum {
				want = float64(n)
			} else if f, isFloat := ok.(float64); isFloat {
				want = f
			}
			if diff := cmp.Diff(want, get("monthly_spend_cap_usd")); diff != "" {
				t.Errorf("GET /settings monthly_spend_cap_usd after %v mismatch (-want +got):\n%s", ok, diff)
			}
		}

		putSettings(s, "monthly_spend_cap_usd", 50)
		for _, bad := range []any{-1, 1000000.01, 1.234, "50"} {
			resp := putSettings(s, "monthly_spend_cap_usd", bad)
			if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
				t.Errorf("PUT monthly_spend_cap_usd %v = %d %s, want 422 validation_error", bad, resp.Status, resp.Body)
				continue
			}
			if diff := cmp.Diff([]string{"[monthly_spend_cap_usd]"}, issuePaths(t, resp)); diff != "" {
				t.Errorf("PUT monthly_spend_cap_usd %v issue paths mismatch (-want +got):\n%s", bad, diff)
			}
		}
		if got := get("monthly_spend_cap_usd"); got != float64(50) {
			t.Errorf("monthly_spend_cap_usd after rejected updates = %v, want the stored 50", got)
		}
	})
}

func TestAbout_ReportsBuildAndPathsAndRefusesForeignHosts(t *testing.T) { // AC14, FR20, NFR5
	// The test binary is not stamped (-X main.version / main.buildID), so both read "dev"; the stamp
	// itself is a link-time value and is checked by the image build (S26).
	s := startStack(t, spec{allowedHosts: []string{"localhost", "127.0.0.1"}})
	got := s.get("/about").Obj(t)
	want := map[string]any{
		"version":       "dev",
		"build_id":      "dev",
		"runner":        map[string]any{"kind": "go", "version": "2"},
		"config_format": "devdigest-plugin/v1",
		"data_dir":      s.cfg.DataDir,
		"secrets_file":  filepath.Join(s.cfg.DataDir, "secrets.json"),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GET /about mismatch (-want +got):\n%s", diff)
	}

	wantBody := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Host not allowed"}}
	for _, path := range []string{"/about", "/settings/spend"} {
		resp := s.do(http.MethodGet, path, nil, "Host", "evil.example")
		if resp.Status != http.StatusForbidden {
			t.Errorf("GET %s with a foreign Host = %d %s, want 403", path, resp.Status, resp.Body)
			continue
		}
		if diff := cmp.Diff(wantBody, resp.JSON(t)); diff != "" {
			t.Errorf("GET %s foreign Host body mismatch (-want +got):\n%s", path, diff)
		}
		if resp := s.do(http.MethodGet, path, nil, "Host", "localhost"); resp.Status != http.StatusOK {
			t.Errorf("GET %s with Host localhost = %d %s, want 200", path, resp.Status, resp.Body)
		}
	}
}

func TestSettings_SpendStatusShape(t *testing.T) { // AC14 (FR12), FR13
	s := startStack(t, spec{now: func() time.Time { return time.Date(2031, 3, 9, 8, 0, 0, 0, time.UTC) }})
	if put := putSettings(s, "monthly_spend_cap_usd", 12.5); put.Status != http.StatusOK {
		t.Fatalf("PUT cap = %d %s, want 200", put.Status, put.Body)
	}
	want := map[string]any{
		"month_start":    "2031-03-01T00:00:00.000Z",
		"spent_usd":      float64(0),
		"cap_usd":        12.5,
		"remaining_usd":  12.5,
		"cap_reached":    false,
		"unpriced_count": float64(0), // always sent, 0 included (ledger FR13)
	}
	if diff := cmp.Diff(want, s.get("/settings/spend").Obj(t)); diff != "" {
		t.Errorf("GET /settings/spend mismatch (-want +got):\n%s", diff)
	}
}

func TestSettings_GuardsOnTheNewKeys(t *testing.T) { // NFR5
	s := startStack(t, spec{})
	const evil = "http://evil.example"
	body := `{"monthly_spend_cap_usd":1,"default_model":{"provider":"openrouter","model":"a/x"}}`

	resp := s.do(http.MethodPut, "/settings", body, "Content-Type", "text/plain;charset=UTF-8", "Origin", evil)
	wantForbidden := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Origin not allowed"}}
	if resp.Status != http.StatusForbidden {
		t.Fatalf("foreign-origin PUT /settings = %d %s, want 403", resp.Status, resp.Body)
	}
	if diff := cmp.Diff(wantForbidden, resp.JSON(t)); diff != "" {
		t.Errorf("foreign-origin PUT body mismatch (-want +got):\n%s", diff)
	}
	if resp := s.do(http.MethodPut, "/settings", body, "Origin", evil); resp.Status != http.StatusForbidden {
		t.Errorf("foreign-origin JSON PUT /settings = %d %s, want 403", resp.Status, resp.Body)
	}
	if resp := s.do(http.MethodPut, "/settings", body, "Content-Type", "text/plain"); resp.Status != http.StatusUnprocessableEntity {
		t.Errorf("text/plain PUT /settings = %d %s, want 422", resp.Status, resp.Body)
	}
	if got := s.get("/settings").Obj(t); got["monthly_spend_cap_usd"] != nil || got["default_model"] != nil {
		t.Errorf("GET /settings after refused writes = %v, want neither new key stored", got)
	}
	if resp := s.do(http.MethodPut, "/settings", body, "Origin", "http://localhost:3100"); resp.Status != http.StatusOK {
		t.Errorf("PUT /settings from the UI origin = %d %s, want 200", resp.Status, resp.Body)
	}

	// The new routes are read-only: other methods get the router's envelope, not a handler.
	for _, path := range []string{"/about", "/settings/spend"} {
		resp := s.do(http.MethodPost, path, map[string]any{})
		if resp.Status != http.StatusNotFound || resp.errorCode(t) != "not_found" {
			t.Errorf("POST %s = %d %s, want 404 not_found", path, resp.Status, resp.Body)
		}
	}
}

func TestRateLimit_TestConnectionBucketDoesNotCoverTheNewRoutes(t *testing.T) { // AC24, NFR3
	s := startStack(t, spec{rateLimit: true})
	for i := 1; i <= 20; i++ {
		if resp := s.post("/settings/test-connection", map[string]string{"provider": "github"}); resp.Status != http.StatusOK {
			t.Fatalf("test-connection %d = %d %s, want 200", i, resp.Status, resp.Body)
		}
	}
	if resp := s.post("/settings/test-connection", map[string]string{"provider": "github"}); resp.Status != http.StatusTooManyRequests {
		t.Fatalf("test-connection 21 = %d %s, want 429", resp.Status, resp.Body)
	}
	for _, path := range []string{"/about", "/settings/spend"} {
		for i := 1; i <= 20; i++ {
			if resp := s.get(path); resp.Status != http.StatusOK {
				t.Fatalf("GET %s request %d with the test-connection bucket exhausted = %d %s, want 200", path, i, resp.Status, resp.Body)
			}
		}
	}
}

// TestContracts_SettingsA records the answers the UI contract test validates (NFR9). It runs on its
// own server with a fixed clock so the month and the spend do not depend on the day of the run.
func TestContracts_SettingsA(t *testing.T) {
	stub := newModelStub(t, listing("a/x", "deepseek/deepseek-v4-flash"))
	s := startStack(t, spec{
		now:       func() time.Time { return time.Date(2031, 1, 15, 12, 0, 0, 0, time.UTC) },
		providers: providers.Options{OpenRouterBaseURL: stub.srv.URL},
	})
	g := &goldens{t: t, cloneDir: s.cfg.CloneDir, dataDir: s.cfg.DataDir, ids: map[string]string{}}

	g.check("about", s.get("/about"), http.StatusOK)
	if put := putSettings(s, "monthly_spend_cap_usd", 12.5); put.Status != http.StatusOK {
		t.Fatalf("PUT cap = %d %s, want 200", put.Status, put.Body)
	}
	g.check("settings_spend", s.get("/settings/spend"), http.StatusOK)
	g.check("test_connection_llm", s.post("/settings/test-connection", map[string]string{"provider": "openrouter", "key": secretKey}), http.StatusOK)

	if put := putSettings(s, "monthly_spend_cap_usd", 0); put.Status != http.StatusOK {
		t.Fatalf("PUT cap 0 = %d %s, want 200", put.Status, put.Body)
	}
	acme := repoByName(t, s, "acme/payments-api")
	pr := s.get("/repos/" + acme + "/pulls").JSON(t).([]any)[0].(map[string]any)["id"].(string)
	refused := startReview(t, s, pr, map[string]any{"all": true})
	g.check("spend_cap_reached", refused, http.StatusPaymentRequired)

	// The golden normalises every timestamp, so the month of the 402 is asserted here.
	details, _ := refused.Obj(t)["error"].(map[string]any)["details"].(map[string]any)
	if details["month_start"] != "2031-01-01T00:00:00.000Z" || details["cap_usd"] != float64(0) || details["spent_usd"] != float64(0) {
		t.Errorf("402 details = %v, want month_start 2031-01-01T00:00:00.000Z, cap_usd 0, spent_usd 0", details)
	}
	assertNoKey(t, "server log", s.logs.String())
}
