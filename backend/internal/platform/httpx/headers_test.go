package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/httpx"
)

// wantSecurityHeaders are the values of spec section 7.1 "Security headers".
var wantSecurityHeaders = map[string]string{
	"Content-Security-Policy":           "default-src 'self';base-uri 'self';font-src 'self' https: data:;form-action 'self';frame-ancestors 'self';img-src 'self' data:;object-src 'none';script-src 'self';script-src-attr 'none';style-src 'self' https: 'unsafe-inline';upgrade-insecure-requests",
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

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	preflight := func() *http.Request {
		r := httptest.NewRequest(http.MethodOptions, "/anything", nil)
		r.Header.Set("Origin", webOrigin)
		r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		return r
	}
	tests := []struct {
		name string
		req  func() *http.Request
		want int
	}{
		{"success", func() *http.Request { return httptest.NewRequest(http.MethodGet, "/settings", nil) }, http.StatusOK},
		{"not found", func() *http.Request { return httptest.NewRequest(http.MethodGet, "/nope", nil) }, http.StatusNotFound},
		{"validation error", func() *http.Request { return httptest.NewRequest(http.MethodGet, "/pulls/abc", nil) }, http.StatusUnprocessableEntity},
		{"internal error", func() *http.Request { return httptest.NewRequest(http.MethodGet, "/boom", nil) }, http.StatusInternalServerError},
		{"panic", func() *http.Request { return httptest.NewRequest(http.MethodGet, "/panic", nil) }, http.StatusInternalServerError},
		{"preflight", preflight, http.StatusNoContent},
		{"head", func() *http.Request { return httptest.NewRequest(http.MethodHead, "/ok", nil) }, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			newTestEcho(t).ServeHTTP(rec, tc.req())

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			got := map[string]string{}
			for k := range wantSecurityHeaders {
				got[k] = rec.Header().Get(k)
			}
			if diff := cmp.Diff(wantSecurityHeaders, got); diff != "" {
				t.Errorf("security headers mismatch (-want +got):\n%s", diff)
			}
			if v := rec.Header().Get("X-Powered-By"); v != "" {
				t.Errorf("X-Powered-By = %q, want it absent", v)
			}
		})
	}
}

func TestSecurityHeadersOnRateLimited(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	e := newEcho(t, httpx.Options{
		RateLimit: httpx.RateLimit{Enabled: true, Limit: 1},
		Now:       func() time.Time { return now },
	})
	var rec *httptest.ResponseRecorder
	for range 2 {
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rec.Code)
	}
	if got, want := rec.Header().Get("Strict-Transport-Security"), wantSecurityHeaders["Strict-Transport-Security"]; got != want {
		t.Errorf("429 Strict-Transport-Security = %q, want %q", got, want)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("429 response lacks Content-Security-Policy")
	}
}
