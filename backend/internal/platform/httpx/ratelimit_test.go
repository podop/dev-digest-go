package httpx_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)} }

var testConnectionBucket = httpx.Bucket{Method: http.MethodPost, Path: "/settings/test-connection", Limit: 20}

func limitedEcho(t *testing.T, clk *fakeClock, mutate func(*httpx.RateLimit)) http.Handler {
	t.Helper()
	rl := httpx.RateLimit{Enabled: true, Buckets: []httpx.Bucket{testConnectionBucket}}
	if mutate != nil {
		mutate(&rl)
	}
	return newEcho(t, httpx.Options{RateLimit: rl, Now: clk.Now})
}

// call sends one request from ip and returns the recorded response.
func call(h http.Handler, method, path, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = ip + ":4321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRateLimitTestConnectionBucket(t *testing.T) {
	clk := newClock()
	h := limitedEcho(t, clk, nil)
	const ip = "192.0.2.10"

	for i := 1; i <= 20; i++ {
		if rec := call(h, http.MethodPost, "/settings/test-connection", ip); rec.Code != http.StatusOK {
			t.Fatalf("test-connection request %d status = %d, want 200", i, rec.Code)
		}
	}
	rec := call(h, http.MethodPost, "/settings/test-connection", ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("test-connection request 21 status = %d, want 429", rec.Code)
	}
	var got httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if want := (httpx.ErrorDetail{Code: "internal_error", Message: "Rate limit exceeded, retry in 1 minute"}); got.Error != want {
		t.Errorf("429 error = %+v, want %+v", got.Error, want)
	}
	if got, want := rec.Header().Get("Retry-After"), "60"; got != want {
		t.Errorf("Retry-After = %q, want %q", got, want)
	}

	// AC13: a limited bucket does not affect other routes.
	if rec := call(h, http.MethodGet, "/settings", ip); rec.Code != http.StatusOK {
		t.Errorf("GET /settings after the bucket is spent status = %d, want 200", rec.Code)
	}
}

func TestRateLimitGlobalBudget(t *testing.T) {
	clk := newClock()
	h := limitedEcho(t, clk, nil)
	const ip = "192.0.2.11"

	for i := 1; i <= 120; i++ {
		if rec := call(h, http.MethodGet, "/settings", ip); rec.Code != http.StatusOK {
			t.Fatalf("GET /settings request %d status = %d, want 200", i, rec.Code)
		}
	}
	if rec := call(h, http.MethodGet, "/settings", ip); rec.Code != http.StatusTooManyRequests {
		t.Errorf("GET /settings request 121 status = %d, want 429", rec.Code)
	}
	// The global budget is shared by all routes without their own bucket.
	if rec := call(h, http.MethodGet, "/repos", ip); rec.Code != http.StatusTooManyRequests {
		t.Errorf("GET /repos after the global budget is spent status = %d, want 429", rec.Code)
	}
	// AC14: health is never counted.
	for _, path := range []string{"/health", "/health/ready"} {
		if rec := call(h, http.MethodGet, path, ip); rec.Code != http.StatusOK {
			t.Errorf("GET %s from a limited IP status = %d, want 200", path, rec.Code)
		}
	}
	// Another client has its own budget.
	if rec := call(h, http.MethodGet, "/settings", "192.0.2.12"); rec.Code != http.StatusOK {
		t.Errorf("GET /settings from another IP status = %d, want 200", rec.Code)
	}
}

func TestRateLimitNotCounted(t *testing.T) {
	clk := newClock()
	h := limitedEcho(t, clk, func(rl *httpx.RateLimit) { rl.Limit = 2 })
	const ip = "192.0.2.13"

	for range 50 {
		call(h, http.MethodGet, "/nope", ip) // unmatched routes
	}
	for range 50 {
		req := httptest.NewRequest(http.MethodOptions, "/settings", nil) // preflights
		req.RemoteAddr = ip + ":1"
		req.Header.Set("Origin", webOrigin)
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("preflight status = %d, want 204", rec.Code)
		}
	}
	for i := 1; i <= 2; i++ {
		if rec := call(h, http.MethodGet, "/settings", ip); rec.Code != http.StatusOK {
			t.Errorf("GET /settings request %d after uncounted traffic status = %d, want 200", i, rec.Code)
		}
	}
	if rec := call(h, http.MethodGet, "/settings", ip); rec.Code != http.StatusTooManyRequests {
		t.Errorf("GET /settings request 3 status = %d, want 429", rec.Code)
	}
}

func TestRateLimitDisabled(t *testing.T) {
	// AC15: the zero value (RATE_LIMIT_ENABLED=false) leaves every route unlimited.
	e := newEcho(t, httpx.Options{RateLimit: httpx.RateLimit{Limit: 1}})
	for i := 1; i <= 200; i++ {
		if rec := call(e, http.MethodGet, "/settings", "192.0.2.14"); rec.Code != http.StatusOK {
			t.Fatalf("GET /settings request %d status = %d, want 200", i, rec.Code)
		}
	}
}

func TestRateLimitWindow(t *testing.T) {
	clk := newClock()
	h := limitedEcho(t, clk, func(rl *httpx.RateLimit) { rl.Limit = 1 })
	const ip = "192.0.2.15"

	call(h, http.MethodGet, "/settings", ip) // window starts at the first request
	tests := []struct {
		name       string
		advance    time.Duration
		wantStatus int
		wantRetry  string
		wantMsg    string
	}{
		{"just after start", 0, http.StatusTooManyRequests, "60", "Rate limit exceeded, retry in 1 minute"},
		{"remaining 59.5 s rounds up", 500 * time.Millisecond, http.StatusTooManyRequests, "60", "Rate limit exceeded, retry in 1 minute"},
		{"remaining 1.5 s rounds up", 58 * time.Second, http.StatusTooManyRequests, "2", "Rate limit exceeded, retry in 2 seconds"},
		{"remaining 1 s", 500 * time.Millisecond, http.StatusTooManyRequests, "1", "Rate limit exceeded, retry in 1 second"},
		{"remaining 0.5 s rounds up to 1 s", 500 * time.Millisecond, http.StatusTooManyRequests, "1", "Rate limit exceeded, retry in 1 second"},
		{"window expired", 500 * time.Millisecond, http.StatusOK, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clk.Advance(tc.advance)
			rec := call(h, http.MethodGet, "/settings", ip)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.wantRetry {
				t.Errorf("Retry-After = %q, want %q", got, tc.wantRetry)
			}
			if tc.wantMsg != "" {
				var body httpx.ErrorBody
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode body %q: %v", rec.Body.String(), err)
				}
				if body.Error.Message != tc.wantMsg {
					t.Errorf("message = %q, want %q", body.Error.Message, tc.wantMsg)
				}
			}
		})
	}
	// A fresh window counts again from the first request after the reset.
	if rec := call(h, http.MethodGet, "/settings", ip); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second request in the new window status = %d, want 429", rec.Code)
	}
}

func TestRateLimitIgnoresForwardedHeaders(t *testing.T) {
	clk := newClock()
	h := limitedEcho(t, clk, func(rl *httpx.RateLimit) { rl.Limit = 1 })
	for i := range 2 {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req.RemoteAddr = "192.0.2.16:9"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i))
		req.Header.Set("X-Real-Ip", fmt.Sprintf("203.0.113.%d", i))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		wantStatus := http.StatusOK
		if i == 1 {
			wantStatus = http.StatusTooManyRequests
		}
		if rec.Code != wantStatus {
			t.Errorf("request %d with spoofed forwarding headers status = %d, want %d", i, rec.Code, wantStatus)
		}
	}
}

const (
	reviewPath = "/pulls/:id/review"
	eventsPath = "/runs/:id/events"
	pullID     = "0b7a4a52-3c2d-4a3e-9a51-6f1d2f6a7b10"
)

// phase4Echo registers the review start route and the SSE route the Phase 4 limits apply to.
func phase4Echo(t *testing.T, clk *fakeClock, mutate func(*httpx.RateLimit)) http.Handler {
	t.Helper()
	rl := httpx.RateLimit{
		Enabled: true,
		Buckets: []httpx.Bucket{{Method: http.MethodPost, Path: reviewPath, Limit: 10}},
		Exempt:  []httpx.Route{{Method: http.MethodGet, Path: eventsPath}},
	}
	if mutate != nil {
		mutate(&rl)
	}
	e := httpx.New(httpx.Options{RateLimit: rl, Now: clk.Now, Log: slog.New(slog.DiscardHandler)})
	ok := func(c *echo.Context) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) }
	e.POST(reviewPath, ok)
	e.GET(eventsPath, ok)
	e.GET("/settings", ok)
	return e
}

func TestRateLimitReviewBucketAndExemptEvents(t *testing.T) {
	clk := newClock()
	h := phase4Echo(t, clk, nil)
	const ip = "192.0.2.20"
	review := "/pulls/" + pullID + "/review"

	for i := 1; i <= 10; i++ {
		if rec := call(h, http.MethodPost, review, ip); rec.Code != http.StatusOK {
			t.Fatalf("POST review request %d status = %d, want 200", i, rec.Code)
		}
	}
	rec := call(h, http.MethodPost, review, ip)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("POST review request 11 status = %d, want 429", rec.Code)
	}
	var body httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if got, want := body.Error.Code, "internal_error"; got != want {
		t.Errorf("429 error code = %q, want %q", got, want)
	}

	// 130 SSE connects in the same window are never limited, even after the bucket is spent.
	for i := 1; i <= 130; i++ {
		if rec := call(h, http.MethodGet, "/runs/"+pullID+"/events", ip); rec.Code != http.StatusOK {
			t.Fatalf("GET events request %d status = %d, want 200", i, rec.Code)
		}
	}
}

func TestRateLimitExemptRouteDoesNotSpendGlobalBudget(t *testing.T) {
	clk := newClock()
	h := phase4Echo(t, clk, func(rl *httpx.RateLimit) { rl.Limit = 2 })
	const ip = "192.0.2.21"

	for range 50 {
		call(h, http.MethodGet, "/runs/"+pullID+"/events", ip)
	}
	for i := 1; i <= 2; i++ {
		if rec := call(h, http.MethodGet, "/settings", ip); rec.Code != http.StatusOK {
			t.Errorf("GET /settings request %d after exempt traffic status = %d, want 200", i, rec.Code)
		}
	}
	if rec := call(h, http.MethodGet, "/settings", ip); rec.Code != http.StatusTooManyRequests {
		t.Errorf("GET /settings request 3 status = %d, want 429", rec.Code)
	}
}

func TestRateLimitExemptMatchesMethod(t *testing.T) {
	clk := newClock()
	// Exempting GET on the review path must not exempt POST on it: the bucket still applies.
	h := phase4Echo(t, clk, func(rl *httpx.RateLimit) {
		rl.Exempt = []httpx.Route{{Method: http.MethodGet, Path: reviewPath}}
	})
	const ip = "192.0.2.22"
	review := "/pulls/" + pullID + "/review"
	for range 10 {
		call(h, http.MethodPost, review, ip)
	}
	if rec := call(h, http.MethodPost, review, ip); rec.Code != http.StatusTooManyRequests {
		t.Errorf("POST review request 11 with only GET exempted status = %d, want 429", rec.Code)
	}
}

const (
	agentEvalPath = "/agents/:id/eval-runs"
	skillEvalPath = "/skills/:id/eval-runs"
)

// sharedBucketEcho registers two run-start routes drawing on one named 10/min bucket keyed by a
// constant (single-tenant workspace key), plus a route with its own per-route bucket.
func sharedBucketEcho(t *testing.T, clk *fakeClock) http.Handler {
	t.Helper()
	workspace := func(*echo.Context) string { return "workspace" }
	rl := httpx.RateLimit{
		Enabled: true,
		Buckets: []httpx.Bucket{
			{Method: http.MethodPost, Path: agentEvalPath, Limit: 10, Name: "eval-run-start", Key: workspace},
			{Method: http.MethodPost, Path: skillEvalPath, Limit: 10, Name: "eval-run-start", Key: workspace},
			{Method: http.MethodPost, Path: reviewPath, Limit: 10},
		},
	}
	e := httpx.New(httpx.Options{RateLimit: rl, Now: clk.Now, Log: slog.New(slog.DiscardHandler)})
	ok := func(c *echo.Context) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) }
	e.POST(agentEvalPath, ok)
	e.POST(skillEvalPath, ok)
	e.POST(reviewPath, ok)
	e.GET("/settings", ok)
	return e
}

func TestRateLimitSharedNamedBucket(t *testing.T) {
	clk := newClock()
	h := sharedBucketEcho(t, clk)
	agent := "/agents/" + pullID + "/eval-runs"
	skill := "/skills/" + pullID + "/eval-runs"

	// 6 agent + 4 skill starts from different IPs spend the one workspace-keyed budget.
	for i := 1; i <= 6; i++ {
		if rec := call(h, http.MethodPost, agent, fmt.Sprintf("192.0.2.%d", 30+i)); rec.Code != http.StatusOK {
			t.Fatalf("agent eval start %d status = %d, want 200", i, rec.Code)
		}
	}
	for i := 7; i <= 10; i++ {
		if rec := call(h, http.MethodPost, skill, fmt.Sprintf("192.0.2.%d", 30+i)); rec.Code != http.StatusOK {
			t.Fatalf("skill eval start %d status = %d, want 200", i, rec.Code)
		}
	}
	for _, path := range []string{agent, skill} {
		rec := call(h, http.MethodPost, path, "192.0.2.99")
		if rec.Code != http.StatusTooManyRequests {
			t.Errorf("POST %s after the shared budget is spent status = %d, want 429", path, rec.Code)
			continue
		}
		var body httpx.ErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body %q: %v", rec.Body.String(), err)
		}
		if got, want := body.Error.Code, "internal_error"; got != want {
			t.Errorf("429 error code = %q, want %q", got, want)
		}
		if got, want := rec.Header().Get("Retry-After"), "60"; got != want {
			t.Errorf("Retry-After = %q, want %q", got, want)
		}
	}

	// Other routes keep their own budgets: the review bucket and the global budget are untouched.
	if rec := call(h, http.MethodPost, "/pulls/"+pullID+"/review", "192.0.2.99"); rec.Code != http.StatusOK {
		t.Errorf("POST review after the shared bucket is spent status = %d, want 200", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/settings", "192.0.2.99"); rec.Code != http.StatusOK {
		t.Errorf("GET /settings after the shared bucket is spent status = %d, want 200", rec.Code)
	}

	// The window reopens the shared budget.
	clk.Advance(61 * time.Second)
	if rec := call(h, http.MethodPost, skill, "192.0.2.99"); rec.Code != http.StatusOK {
		t.Errorf("POST skill eval start after the window status = %d, want 200", rec.Code)
	}
}

func TestRateLimitNamedBucketWithoutKeyIsPerIP(t *testing.T) {
	clk := newClock()
	h := phase4Echo(t, clk, func(rl *httpx.RateLimit) {
		rl.Buckets = []httpx.Bucket{{Method: http.MethodPost, Path: reviewPath, Limit: 2, Name: "shared"}}
	})
	review := "/pulls/" + pullID + "/review"
	for range 2 {
		call(h, http.MethodPost, review, "192.0.2.40")
	}
	if rec := call(h, http.MethodPost, review, "192.0.2.40"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("third POST from the same IP status = %d, want 429", rec.Code)
	}
	if rec := call(h, http.MethodPost, review, "192.0.2.41"); rec.Code != http.StatusOK {
		t.Errorf("first POST from another IP status = %d, want 200", rec.Code)
	}
}
