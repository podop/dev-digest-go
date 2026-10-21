package httpx_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
