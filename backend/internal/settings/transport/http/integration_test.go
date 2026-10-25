package settingshttp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/platform/secrets"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
	settingspg "dev-digest-go/backend/internal/settings/adapters/postgres"
	"dev-digest-go/backend/internal/settings/adapters/providers"
	"dev-digest-go/backend/internal/settings/app"
	settingshttp "dev-digest-go/backend/internal/settings/transport/http"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// upstream is a stub for GitHub and the LLM providers; it records the credentials it saw.
type upstream struct {
	srv *httptest.Server

	mu       sync.Mutex
	githubOK bool
	auths    []string
	paths    []string

	ghScopes   *string       // X-OAuth-Scopes of GET /gh/user; nil sends no header
	orIDs      []string      // model ids of GET /or/models
	orStatus   int           // non-zero answers GET /or/models with this status
	orRedirect string        // non-empty answers GET /or/models with a 302 to this URL
	orDelay    time.Duration // GET /or/models waits this long before answering
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{githubOK: true, orIDs: []string{"a", "b", "c"}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.auths = append(u.auths, r.Header.Get("Authorization"))
		u.paths = append(u.paths, r.URL.Path)
		ok, scopes, ids, status, redirect, delay := u.githubOK, u.ghScopes, u.orIDs, u.orStatus, u.orRedirect, u.orDelay
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/gh/user":
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
				return
			}
			if scopes != nil {
				w.Header().Set("X-OAuth-Scopes", *scopes)
			}
			_, _ = io.WriteString(w, `{"login":"octo"}`)
		case "/or/models":
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
			if redirect != "" {
				http.Redirect(w, r, redirect, http.StatusFound)
				return
			}
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"boom"}`)
				return
			}
			data := make([]map[string]string, len(ids))
			for i, id := range ids {
				data[i] = map[string]string{"id": id}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) lastAuth() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.auths) == 0 {
		return ""
	}
	return u.auths[len(u.auths)-1]
}

type stack struct {
	e          *echo.Echo
	up         *upstream
	secretPath string
	clock      *fakeClock
	res        seed.Result
	db         *gorm.DB
	logs       *bytes.Buffer // the settings service's log output
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

type stackOpts struct {
	seed      bool
	env       map[string]string
	rateLimit bool
	secretsIn string // initial secrets file content, "" for no file

	openRouterTimeout time.Duration // 0 keeps the provider default
}

func newStack(t *testing.T, o stackOpts) *stack {
	t.Helper()
	d := pgtest.Migrated(t)
	s := &stack{db: d.DB, up: newUpstream(t), clock: &fakeClock{t: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}}
	if o.seed {
		res, err := seed.New(seedpg.NewStore(d.DB), slog.New(slog.DiscardHandler)).Run(t.Context())
		if err != nil {
			t.Fatalf("seed error = %v", err)
		}
		s.res = res
	}

	s.secretPath = filepath.Join(t.TempDir(), "data", "secrets.json")
	if o.secretsIn != "" {
		if err := os.MkdirAll(filepath.Dir(s.secretPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.secretPath, []byte(o.secretsIn), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sec := secrets.New(s.secretPath, func(k string) string { return o.env[k] })
	gh := github.New(github.Options{
		Token:   func() string { return sec.Get(secrets.GitHubToken) },
		BaseURL: s.up.srv.URL + "/gh",
		Retry:   &retry.Options{},
	})
	s.logs = &bytes.Buffer{}
	svc := app.NewService(app.Deps{
		Store:   settingspg.NewStore(d.DB),
		Secrets: sec,
		GitHub:  gh,
		Providers: providers.New(providers.Options{
			OpenRouterBaseURL: s.up.srv.URL + "/or", OpenRouterTimeout: o.openRouterTimeout, Retry: &retry.Options{},
		}),
		Logger: slog.New(slog.NewTextHandler(s.logs, nil)),
	})
	ids := tenancy.NewResolver(tenancypg.NewStore(d.DB))

	s.e = httpx.New(httpx.Options{
		CORSOrigins: []string{uiOrigin},
		RateLimit:   httpx.RateLimit{Enabled: o.rateLimit, Buckets: settingshttp.RateLimitBuckets()},
		Now:         s.clock.Now,
	})
	settingshttp.AddRoutes(s.e, settingshttp.NewHandler(svc, ids))
	return s
}

func (s *stack) call(method, path, body string) *httptest.ResponseRecorder {
	ct := ""
	if body != "" {
		ct = "application/json"
	}
	return do(s.e, method, path, ct, body)
}

func jsonMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", rec.Body.String(), err)
	}
	return m
}

// defaultAutomaticReviews is what GET /settings reports while no value is stored (FR1).
var defaultAutomaticReviews = map[string]any{"enabled": false, "agent_ids": []any{}, "on_new_pr": true, "on_new_commits": true, "enabled_at": nil}

var seededSettings = map[string]any{"polling_interval_min": 5.0, "theme": "dark", "density": "regular", "sync_to_folder": true, "automatic_reviews": defaultAutomaticReviews}

func TestGetSettings_seeded(t *testing.T) { // AC37
	s := newStack(t, stackOpts{seed: true})
	rec := s.call(http.MethodGet, "/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings status = %d, want 200", rec.Code)
	}
	if diff := cmp.Diff(seededSettings, jsonMap(t, rec)); diff != "" {
		t.Errorf("GET /settings mismatch (-want +got):\n%s", diff)
	}
}

func TestGetSettings_emptyStoreHoldsOnlyTheAutomaticReviewsDefault(t *testing.T) {
	s := newStack(t, stackOpts{seed: true})
	if err := s.db.Exec(`DELETE FROM settings`).Error; err != nil {
		t.Fatal(err)
	}
	rec := s.call(http.MethodGet, "/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings on an empty store status = %d, want 200", rec.Code)
	}
	want := map[string]any{"automatic_reviews": defaultAutomaticReviews}
	if diff := cmp.Diff(want, jsonMap(t, rec)); diff != "" {
		t.Errorf("GET /settings on an empty store mismatch (-want +got):\n%s", diff)
	}
}

func TestPutSettings_returnsFullBag(t *testing.T) { // AC38
	s := newStack(t, stackOpts{seed: true})
	rec := s.call(http.MethodPut, "/settings", `{"feature_models":{"risk_brief":{"provider":"openai","model":"gpt-4.1"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /settings status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	want := map[string]any{
		"polling_interval_min": 5.0, "theme": "dark", "density": "regular", "sync_to_folder": true,
		"feature_models":    map[string]any{"risk_brief": map[string]any{"provider": "openai", "model": "gpt-4.1"}},
		"automatic_reviews": defaultAutomaticReviews,
	}
	if diff := cmp.Diff(want, jsonMap(t, rec)); diff != "" {
		t.Errorf("PUT /settings mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(want, jsonMap(t, s.call(http.MethodGet, "/settings", ""))); diff != "" {
		t.Errorf("GET /settings after PUT mismatch (-want +got):\n%s", diff)
	}
}

func TestPutSettings_automaticReviews(t *testing.T) { // AC2, AC3, FR2
	s := newStack(t, stackOpts{seed: true})
	const a, b = "6f1c0d2e-0000-4000-8000-00000000000a", "6f1c0d2e-0000-4000-8000-00000000000b"
	stored := func() map[string]any {
		t.Helper()
		return jsonMap(t, s.call(http.MethodGet, "/settings", ""))["automatic_reviews"].(map[string]any)
	}
	put := func(body string) map[string]any {
		t.Helper()
		rec := s.call(http.MethodPut, "/settings", `{"automatic_reviews":`+body+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT automatic_reviews=%s status = %d, want 200 (%s)", body, rec.Code, rec.Body)
		}
		got := jsonMap(t, rec)["automatic_reviews"].(map[string]any)
		if diff := cmp.Diff(got, stored()); diff != "" {
			t.Errorf("PUT automatic_reviews=%s answered something other than GET stores (-put +get):\n%s", body, diff)
		}
		return got
	}

	got := put(`{"enabled":true,"enabled_at":"x"}`)
	if got["enabled"] != true || got["enabled_at"] != nil {
		t.Errorf("enable without agents = %v, want enabled true and enabled_at null (not armed)", got)
	}
	got = put(`{"agent_ids":["` + a + `","` + a + `"]}`)
	at, _ := got["enabled_at"].(string)
	if diff := cmp.Diff([]any{a}, got["agent_ids"]); diff != "" {
		t.Errorf("duplicate ids mismatch (-want +got):\n%s", diff)
	}
	if !strings.HasSuffix(at, "Z") || len(at) != len("2006-01-02T15:04:05.000Z") {
		t.Errorf("enabled_at after arming = %q, want ISO-8601 UTC with milliseconds", at)
	}
	if got = put(`{"agent_ids":["` + b + `"],"on_new_pr":false}`); got["enabled_at"] != at || got["on_new_pr"] != false {
		t.Errorf("edit while armed = %v, want enabled_at %s kept and on_new_pr false", got, at)
	}
	if got = put(`false`); got["enabled"] != false || got["enabled_at"] != nil || got["on_new_pr"] != false {
		t.Errorf("pause = %v, want enabled false, enabled_at null, other fields kept", got)
	}
}

func TestPutSettings_automaticReviewsInvalidChangesNothing(t *testing.T) { // AC3
	s := newStack(t, stackOpts{seed: true})
	rec := s.call(http.MethodPut, "/settings", `{"automatic_reviews":{"agent_ids":["x"]}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("PUT status = %d, want 422 (%s)", rec.Code, rec.Body)
	}
	for _, want := range []string{`"validation_error"`, `"invalid_string"`, `"Invalid uuid"`, `"automatic_reviews"`, `"agent_ids"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("422 body %s lacks %s", rec.Body, want)
		}
	}
	if diff := cmp.Diff(seededSettings, jsonMap(t, s.call(http.MethodGet, "/settings", ""))); diff != "" {
		t.Errorf("GET /settings after an invalid PUT mismatch (-want +got):\n%s", diff)
	}
}

func TestPutSettings_invalidBodiesChangeNothing(t *testing.T) { // AC39
	s := newStack(t, stackOpts{seed: true})
	for _, body := range []string{`{"polling_interval_min":0}`, `{"theme":null}`, `{"feature_models":{"nope":{"provider":"openai","model":"m"}}}`, `[]`, `{"theme":"light","density":"bad"}`} {
		if rec := s.call(http.MethodPut, "/settings", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("PUT /settings %s status = %d, want 422", body, rec.Code)
		}
	}
	if diff := cmp.Diff(seededSettings, jsonMap(t, s.call(http.MethodGet, "/settings", ""))); diff != "" {
		t.Errorf("GET /settings after invalid PUTs mismatch (-want +got):\n%s", diff)
	}
}

func TestPutSettings_unknownKeyWithNull(t *testing.T) { // AC40
	s := newStack(t, stackOpts{seed: true})
	if rec := s.call(http.MethodPut, "/settings", `{"x_custom":null}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	got := jsonMap(t, s.call(http.MethodGet, "/settings", ""))
	if v, ok := got["x_custom"]; !ok || v != nil {
		t.Errorf("GET /settings x_custom = %v (present %v), want present null", v, ok)
	}
}

func TestPutSettings_bodyRules(t *testing.T) { // AC6, AC7, AC8, AC10, AC11
	s := newStack(t, stackOpts{seed: true})
	padded := func(n int) string {
		const prefix, suffix = `{"x":"`, `"}`
		return prefix + strings.Repeat("a", n-len(prefix)-len(suffix)) + suffix
	}
	tests := []struct {
		name, contentType, body string
		wantStatus              int
		wantMessage             string
	}{
		{"exactly 1 MiB", "application/json", padded(1 << 20), 200, ""},
		{"1 MiB + 1", "application/json", padded(1<<20 + 1), 413, "Request body is too large"},
		{"invalid JSON", "application/json", `{bad`, 400, "Body is not valid JSON but content-type is set to 'application/json'"},
		{"form content type", "application/x-www-form-urlencoded", `a=b`, 415, "Unsupported Media Type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(s.e, http.MethodPut, "/settings", tc.contentType, tc.body)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantMessage != "" && !strings.Contains(rec.Body.String(), tc.wantMessage) {
				t.Errorf("body %s lacks %q", rec.Body, tc.wantMessage)
			}
		})
	}

	t.Run("empty JSON body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/settings", nil)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Body cannot be empty when content-type is set to 'application/json'") {
			t.Errorf("status/body = %d %s, want 400 with the empty-body message", rec.Code, rec.Body)
		}
	})
}

func TestSecretsStatus_envFallback(t *testing.T) { // AC41
	s := newStack(t, stackOpts{seed: true, env: map[string]string{"OPENAI_API_KEY": "sk-a", "GITHUB_PAT": "ghp-b", "ANTHROPIC_API_KEY": ""}})
	rec := s.call(http.MethodGet, "/settings/secrets-status", "")
	want := `{"openai":true,"anthropic":false,"openrouter":false,"github":true}`
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != want {
		t.Errorf("GET secrets-status = %d %s, want 200 %s", rec.Code, got, want)
	}
}

func TestTestConnection_githubSavesKeyWith0600(t *testing.T) { // AC42, AC46 (token used next call)
	s := newStack(t, stackOpts{seed: true})
	rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"github","key":"ghp_test"}`)
	want := map[string]any{"provider": "github", "ok": true, "message": "Connected as @octo", "login": "octo", "scopes": nil}
	if got := withoutLatency(t, rec); rec.Code != http.StatusOK || !cmp.Equal(want, got) {
		t.Errorf("POST test-connection = %d %v, want 200 %v", rec.Code, got, want)
	}
	if got := s.up.lastAuth(); got != "Bearer ghp_test" {
		t.Errorf("GitHub stub saw Authorization %q, want Bearer ghp_test", got)
	}
	raw, err := os.ReadFile(s.secretPath)
	if err != nil {
		t.Fatalf("read secrets file: %v", err)
	}
	var file map[string]string
	if err := json.Unmarshal(raw, &file); err != nil || file["GITHUB_TOKEN"] != "ghp_test" {
		t.Errorf("secrets file = %s (err %v), want GITHUB_TOKEN ghp_test", raw, err)
	}
	if fi, err := os.Stat(s.secretPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("secrets file mode = %v (err %v), want 0600", fi.Mode().Perm(), err)
	}
}

func TestTestConnection_wrongKeyStaysSaved(t *testing.T) { // AC43
	s := newStack(t, stackOpts{seed: true})
	s.up.mu.Lock()
	s.up.githubOK = false
	s.up.mu.Unlock()

	rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"github","key":"ghp_bad"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := jsonMap(t, rec)
	if got["ok"] != false || strings.Contains(rec.Body.String(), "ghp_bad") {
		t.Errorf("response = %s, want ok:false without the key", rec.Body)
	}
	if _, has := got["detail"]; has {
		t.Errorf("response has detail: %s", rec.Body)
	}
	status := s.call(http.MethodGet, "/settings/secrets-status", "")
	if want := `{"openai":false,"anthropic":false,"openrouter":false,"github":true}`; strings.TrimSpace(status.Body.String()) != want {
		t.Errorf("secrets-status = %s, want %s", status.Body, want)
	}
}

func TestTestConnection_openRouter(t *testing.T) {
	s := newStack(t, stackOpts{seed: true})
	// AC44: no key anywhere.
	rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"openrouter"}`)
	want := `{"provider":"openrouter","ok":false,"message":"OPENROUTER_API_KEY is not configured","latency_ms":null,"model":null,"model_reachable":null}`
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != want {
		t.Errorf("no key = %d %s, want 200 %s", rec.Code, got, want)
	}
	// AC45: three models.
	rec = s.call(http.MethodPost, "/settings/test-connection", `{"provider":"openrouter","key":"or-key"}`)
	wantOK := map[string]any{"provider": "openrouter", "ok": true, "message": "OK — 3 models available",
		"model": "deepseek/deepseek-v4-flash", "model_reachable": false}
	if got := withoutLatency(t, rec); rec.Code != http.StatusOK || !cmp.Equal(wantOK, got) {
		t.Errorf("with key = %d %v, want 200 %v", rec.Code, got, wantOK)
	}
	if got := s.up.lastAuth(); got != "Bearer or-key" {
		t.Errorf("stub saw Authorization %q, want Bearer or-key", got)
	}
}

// withoutLatency decodes a test-connection answer, checks that latency_ms is a non-negative
// integer or null and removes it, as its value changes from run to run.
func withoutLatency(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	m := jsonMap(t, rec)
	if l, ok := m["latency_ms"]; ok && l != nil {
		if f, isNum := l.(float64); !isNum || f < 0 || f != float64(int(f)) {
			t.Errorf("latency_ms = %v, want a non-negative integer", l)
		}
	} else if ok {
		return m
	}
	delete(m, "latency_ms")
	return m
}

func TestSecrets_malformedFileRecovers(t *testing.T) { // AC47
	s := newStack(t, stackOpts{seed: true, secretsIn: "{not json"})
	status := s.call(http.MethodGet, "/settings/secrets-status", "")
	if want := `{"openai":false,"anthropic":false,"openrouter":false,"github":false}`; strings.TrimSpace(status.Body.String()) != want {
		t.Errorf("secrets-status on a malformed file = %s, want %s", status.Body, want)
	}
	s.call(http.MethodPost, "/settings/test-connection", `{"provider":"openrouter","key":"or-key"}`)
	raw, err := os.ReadFile(s.secretPath)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]string
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("secrets file %q is not valid JSON after a save: %v", raw, err)
	}
	if diff := cmp.Diff(map[string]string{"OPENROUTER_API_KEY": "or-key"}, file); diff != "" {
		t.Errorf("secrets file mismatch (-want +got):\n%s", diff)
	}
}

func TestSecrets_fileWinsOverEnv(t *testing.T) { // AC48 (through the GitHub stub)
	s := newStack(t, stackOpts{seed: true, env: map[string]string{"GITHUB_TOKEN": "env-tok"}, secretsIn: `{"GITHUB_TOKEN":"file-tok"}`})
	s.call(http.MethodPost, "/settings/test-connection", `{"provider":"github"}`)
	if got := s.up.lastAuth(); got != "Bearer file-tok" {
		t.Errorf("GitHub stub saw Authorization %q, want Bearer file-tok", got)
	}
}

func TestUnprovisionedDatabase(t *testing.T) { // AC25
	s := newStack(t, stackOpts{seed: false})
	rec := s.call(http.MethodGet, "/settings", "")
	want := `{"error":{"code":"internal_error","message":"Internal error"}}`
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusInternalServerError || got != want {
		t.Errorf("GET /settings = %d %s, want 500 %s", rec.Code, got, want)
	}
	rec = s.call(http.MethodPost, "/settings/test-connection", `{"provider":"github"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("POST test-connection status = %d, want 200", rec.Code)
	}
}

func TestTestConnection_rateLimit(t *testing.T) { // AC12, AC13
	s := newStack(t, stackOpts{seed: true, rateLimit: true})
	for i := 1; i <= 20; i++ {
		if rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"openrouter"}`); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d status = 429, want not limited", i)
		}
	}
	rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"openrouter"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request 21 status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal_error"`) || !strings.Contains(rec.Body.String(), "Rate limit exceeded, retry in 1 minute") {
		t.Errorf("429 body = %s, want the rate-limit envelope", rec.Body)
	}
	if rec := s.call(http.MethodGet, "/settings", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /settings after the bucket is spent status = %d, want 200", rec.Code)
	}
}
