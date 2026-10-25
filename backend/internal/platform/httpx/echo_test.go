package httpx_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
)

const webOrigin = "http://localhost:3100"

func newTestEcho(t *testing.T) *echo.Echo {
	t.Helper()
	return newTestEchoLog(t, slog.New(slog.DiscardHandler))
}

func newTestEchoLog(t *testing.T, log *slog.Logger) *echo.Echo {
	t.Helper()
	return newEcho(t, httpx.Options{Log: log, CORSOrigins: []string{webOrigin}})
}

// newEcho builds the API instance with the routes the tests in this package exercise.
func newEcho(t *testing.T, opts httpx.Options) *echo.Echo {
	t.Helper()
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	e := httpx.New(opts)
	okJSON := func(c *echo.Context) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) }
	e.GET("/ok", okJSON)
	e.GET("/settings", okJSON)
	e.GET("/repos", okJSON)
	e.GET("/health", okJSON)
	e.GET("/health/ready", okJSON)
	e.PUT("/settings", echoBody)
	e.POST("/settings/test-connection", okJSON)
	e.POST("/echo", echoBody)
	e.DELETE("/echo", echoBody)
	e.GET("/boom", func(c *echo.Context) error { return errors.New("db password leaked in message") })
	e.GET("/panic", func(c *echo.Context) error { panic("bug") })
	e.GET("/teapot", func(c *echo.Context) error { return echo.NewHTTPError(http.StatusTeapot, "short and stout") })
	e.GET("/apperr/:kind", func(c *echo.Context) error { return appErrorFor(c.Param("kind")) })
	e.GET("/apperr-wrapped", func(c *echo.Context) error {
		return fmt.Errorf("load repo: %w", apperr.Conflict("already added").WithDetails(map[string]string{"repo": "a/b"}))
	})
	e.GET("/pulls/:id", func(c *echo.Context) error {
		id, err := httpx.PathUUID(c, "id")
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"id": id})
	})
	return e
}

// echoBody answers 200 with the body length and the body itself, as the handler sees it.
func echoBody(c *echo.Context) error {
	b, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"len": len(b), "body": string(b)})
}

func appErrorFor(kind string) error {
	switch kind {
	case "invalid_input":
		return apperr.InvalidInput("bad value")
	case "not_found":
		return apperr.NotFound("no such thing")
	case "conflict":
		return apperr.Conflict("already there")
	case "payload_too_large":
		return apperr.PayloadTooLarge("too big")
	case "validation":
		return apperr.Validation("invalid")
	case "internal":
		return apperr.Internal("broken")
	case "config":
		return apperr.Config("missing setting")
	case "external_service":
		return apperr.ExternalService("upstream down").WithCause(errors.New("dial tcp 10.0.0.1: refused"))
	case "payment_required":
		return apperr.PaymentRequired("cap reached").WithCode("spend_cap_reached").
			WithDetails(map[string]any{"cap_usd": 50, "spent_usd": 50.12, "month_start": "2026-10-01T00:00:00.000Z"})
	case "custom_code":
		return apperr.ExternalService("GitHub unavailable").WithCode("github_unavailable")
	default:
		return apperr.New(apperr.Kind(0), "unclassified")
	}
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
