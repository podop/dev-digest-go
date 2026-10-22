package httpx_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
)

func TestBodyLimitsPerRoute(t *testing.T) {
	limits := map[string]int64{
		"POST /skills/import/preview":   2_162_688,
		"PUT /repos/:id/context/files":  2_097_152,
		"POST /repos/:id/context/files": 2_097_152,
	}
	e := httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), BodyLimits: limits})
	ok := func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) }
	e.POST("/skills/import/preview", ok)
	e.PUT("/repos/:id/context/files", ok)
	e.POST("/repos/:id/context/files", ok)
	e.PUT("/agents/:id", ok)
	// Same path as a limited route, other method: keeps the default limit.
	e.PUT("/skills/import/preview", ok)

	const id = "3f2b8c1e-7a4d-4e9b-8c1a-2d3e4f5a6b7c"
	tests := []struct {
		name   string
		method string
		path   string
		size   int
		want   int
	}{
		{"preview at limit", http.MethodPost, "/skills/import/preview", 2_162_688, http.StatusNoContent},
		{"preview over limit", http.MethodPost, "/skills/import/preview", 2_162_689, http.StatusRequestEntityTooLarge},
		{"context PUT at limit", http.MethodPut, "/repos/" + id + "/context/files", 2_097_152, http.StatusNoContent},
		{"context PUT over limit", http.MethodPut, "/repos/" + id + "/context/files", 2_097_153, http.StatusRequestEntityTooLarge},
		{"context POST at limit", http.MethodPost, "/repos/" + id + "/context/files", 2_097_152, http.StatusNoContent},
		{"context POST over limit", http.MethodPost, "/repos/" + id + "/context/files", 2_097_153, http.StatusRequestEntityTooLarge},
		{"agents default at limit", http.MethodPut, "/agents/" + id, 1_048_576, http.StatusNoContent},
		{"agents default over limit", http.MethodPut, "/agents/" + id, 1_048_577, http.StatusRequestEntityTooLarge},
		{"other method on a limited path keeps the default", http.MethodPut, "/skills/import/preview", 1_048_577, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, ct := range []string{"application/json", "text/plain"} {
				body := jsonPadded(tc.size)
				if ct == "text/plain" {
					body = strings.Repeat("a", tc.size)
				}
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
				req.Header.Set("Content-Type", ct)
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Errorf("%s %s with %d bytes of %s: status = %d, want %d", tc.method, tc.path, tc.size, ct, rec.Code, tc.want)
				}
				if tc.want == http.StatusRequestEntityTooLarge && !strings.Contains(rec.Body.String(), `"message":"Request body is too large"`) {
					t.Errorf("%s %s body = %.200s, want the 413 envelope", tc.method, tc.path, rec.Body.String())
				}
			}
		})
	}
}

func TestBodyLimitsOverLengthHeaderOnly(t *testing.T) {
	// A declared Content-Length above the route limit is refused without reading the body.
	e := httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), BodyLimits: map[string]int64{"POST /big": 2 << 20}})
	e.POST("/big", func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/big", strings.NewReader(jsonPadded(2<<20+1)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /big over its limit: status = %d, want 413", rec.Code)
	}
}
