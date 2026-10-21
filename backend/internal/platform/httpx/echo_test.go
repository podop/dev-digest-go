package httpx_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
)

const webOrigin = "http://localhost:3100"

func newTestEcho(t *testing.T) *echo.Echo {
	t.Helper()
	e := httpx.New([]string{webOrigin}, slog.New(slog.DiscardHandler))
	e.GET("/ok", func(c *echo.Context) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	e.GET("/boom", func(c *echo.Context) error { return errors.New("db password leaked in message") })
	e.GET("/panic", func(c *echo.Context) error { panic("bug") })
	e.GET("/teapot", func(c *echo.Context) error { return echo.NewHTTPError(http.StatusTeapot, "short and stout") })
	return e
}

func TestErrorEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		want       httpx.ErrorDetail
	}{
		{
			name: "unknown route", method: http.MethodGet, path: "/nope?x=1",
			wantStatus: http.StatusNotFound,
			want:       httpx.ErrorDetail{Code: "not_found", Message: "Route GET /nope not found"},
		},
		{
			name: "unknown method on known path", method: http.MethodDelete, path: "/ok",
			wantStatus: http.StatusNotFound,
			want:       httpx.ErrorDetail{Code: "not_found", Message: "Route DELETE /ok not found"},
		},
		{
			name: "plain error hides message", method: http.MethodGet, path: "/boom",
			wantStatus: http.StatusInternalServerError,
			want:       httpx.ErrorDetail{Code: "internal_error", Message: "Internal error"},
		},
		{
			name: "panic is recovered", method: http.MethodGet, path: "/panic",
			wantStatus: http.StatusInternalServerError,
			want:       httpx.ErrorDetail{Code: "internal_error", Message: "Internal error"},
		},
		{
			name: "framework 4xx keeps status", method: http.MethodGet, path: "/teapot",
			wantStatus: http.StatusTeapot,
			want:       httpx.ErrorDetail{Code: "internal_error", Message: "short and stout"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			newTestEcho(t).ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, rec.Code, tc.wantStatus)
			}
			var got httpx.ErrorBody
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("%s %s: decode body: %v", tc.method, tc.path, err)
			}
			if diff := cmp.Diff(tc.want, got.Error); diff != "" {
				t.Errorf("%s %s error mismatch (-want +got):\n%s", tc.method, tc.path, diff)
			}
		})
	}
}

func TestCORS(t *testing.T) {
	tests := []struct {
		name      string
		origin    string
		wantAllow string
	}{
		{name: "web app origin", origin: webOrigin, wantAllow: webOrigin},
		{name: "other origin", origin: "http://evil.test", wantAllow: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodOptions, "/ok", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodPut)
			rec := httptest.NewRecorder()
			newTestEcho(t).ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllow {
				t.Errorf("preflight from %q Allow-Origin = %q, want %q", tc.origin, got, tc.wantAllow)
			}
			if tc.wantAllow != "" && rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Errorf("preflight from %q Allow-Credentials = %q, want true", tc.origin, rec.Header().Get("Access-Control-Allow-Credentials"))
			}
		})
	}
}
