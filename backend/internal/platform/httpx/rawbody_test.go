package httpx_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
)

const (
	zipLimit  = 5 << 20
	jsonLimit = 20 << 20
	rawRoute  = "/plugins/import"
)

// newRawEcho serves POST /plugins/import as a raw-body route (zip <= 5 MiB, json <= 20 MiB) that
// echoes what ReadRawBody returned, next to ordinary JSON routes.
func newRawEcho(t *testing.T) *echo.Echo {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{"http://localhost:3100"},
		AllowedHosts: []string{"example.com"},
		RawBodies: map[string]map[string]int64{
			"POST " + rawRoute: {"application/zip": zipLimit, "Application/JSON": jsonLimit},
		},
	})
	e.POST(rawRoute, func(c *echo.Context) error {
		media, body, err := httpx.ReadRawBody(c)
		if err != nil {
			return err
		}
		c.Response().Header().Set("X-Media", media)
		return c.String(http.StatusOK, "n="+itoa(len(body)))
	})
	e.POST("/other", func(c *echo.Context) error {
		body, err := httpx.ReadJSONBody(c)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, string(body))
	})
	e.POST("/misconfigured", func(c *echo.Context) error {
		_, _, err := httpx.ReadRawBody(c)
		return err
	})
	return e
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func doRaw(e *echo.Echo, path, contentType string, body io.Reader, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Host = "example.com"
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response body %.200q is not the error envelope: %v", rec.Body.String(), err)
	}
	return env.Error.Code
}

func TestRawBodyUnsupportedMediaType(t *testing.T) {
	t.Parallel()
	e := newRawEcho(t)
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{"text/plain", "text/plain", "PK"},
		{"text/plain with charset", "text/plain; charset=utf-8", "{}"},
		{"multipart/form-data", "multipart/form-data; boundary=x", "--x--"},
		{"application/x-www-form-urlencoded", "application/x-www-form-urlencoded", "a=b"},
		{"no content type with a body", "", "PK"},
		{"no content type and no body", "", ""},
		{"malformed content type", "application/zip;;;=", "PK"},
		{"zip look-alike", "application/zipx", "PK"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := doRaw(e, rawRoute, tc.contentType, strings.NewReader(tc.body))
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("POST %s as %q: status = %d, want 415", rawRoute, tc.contentType, rec.Code)
			}
			if got := errorCode(t, rec); got != httpx.CodeUnsupportedMediaType {
				t.Errorf("POST %s as %q: error code = %q, want %q", rawRoute, tc.contentType, got, httpx.CodeUnsupportedMediaType)
			}
		})
	}
}

func TestRawBodyLimitPerMediaType(t *testing.T) {
	t.Parallel()
	e := newRawEcho(t)
	tests := []struct {
		name        string
		contentType string
		size        int
		chunked     bool
		want        int
	}{
		{"zip at limit", "application/zip", zipLimit, false, http.StatusOK},
		{"zip over limit", "application/zip", zipLimit + 1, false, http.StatusRequestEntityTooLarge},
		{"zip over limit, no declared length", "application/zip", zipLimit + 1, true, http.StatusRequestEntityTooLarge},
		{"zip at limit, no declared length", "application/zip", zipLimit, true, http.StatusOK},
		{"json at limit", "application/json", jsonLimit, false, http.StatusOK},
		{"json over limit", "application/json", jsonLimit + 1, false, http.StatusRequestEntityTooLarge},
		{"json over limit, no declared length", "application/json", jsonLimit + 1, true, http.StatusRequestEntityTooLarge},
		{"json above the zip limit is fine", "application/json", zipLimit + 1, false, http.StatusOK},
		{"json with parameters, mixed case", "Application/JSON; charset=utf-8", jsonLimit, false, http.StatusOK},
		{"empty zip", "application/zip", 0, false, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body io.Reader = strings.NewReader(strings.Repeat("a", tc.size))
			if tc.chunked {
				body = io.NopCloser(body) // hides the length: ContentLength is -1
			}
			rec := doRaw(e, rawRoute, tc.contentType, body)
			if rec.Code != tc.want {
				t.Fatalf("POST %s with %d bytes of %q: status = %d, want %d", rawRoute, tc.size, tc.contentType, rec.Code, tc.want)
			}
			switch tc.want {
			case http.StatusOK:
				if got, want := rec.Body.String(), "n="+itoa(tc.size); got != want {
					t.Errorf("handler saw %q, want %q", got, want)
				}
			case http.StatusRequestEntityTooLarge:
				if got := errorCode(t, rec); got != httpx.CodePayloadTooLarge {
					t.Errorf("error code = %q, want %q", got, httpx.CodePayloadTooLarge)
				}
			}
		})
	}
}

func TestReadRawBodyReturnsMediaAndBytes(t *testing.T) {
	t.Parallel()
	e := httpx.New(httpx.Options{
		Log:       slog.New(slog.DiscardHandler),
		RawBodies: map[string]map[string]int64{"POST /up": {"application/zip": 16, "application/json": 16}},
	})
	type seen struct {
		media string
		body  string
	}
	var got seen
	e.POST("/up", func(c *echo.Context) error {
		media, body, err := httpx.ReadRawBody(c)
		got = seen{media, string(body)}
		return err
	})
	// The bytes are not interpreted: a body that is not valid JSON passes as application/json.
	rec := doRaw(e, "/up", "application/json; charset=utf-8", strings.NewReader("not json \x00"))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("POST /up: status = %d, want 2xx", rec.Code)
	}
	if diff := cmp.Diff(seen{"application/json", "not json \x00"}, got, cmp.AllowUnexported(seen{})); diff != "" {
		t.Errorf("ReadRawBody mismatch (-want +got):\n%s", diff)
	}
}

func TestRawBodyForeignOriginAndHostStayRefused(t *testing.T) {
	t.Parallel()
	e := newRawEcho(t)
	for _, ct := range []string{"application/zip", "application/json"} {
		t.Run("foreign origin "+ct, func(t *testing.T) {
			t.Parallel()
			rec := doRaw(e, rawRoute, ct, strings.NewReader("PK"), "Origin", "https://evil.example")
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST %s as %s from a foreign Origin: status = %d, want 403", rawRoute, ct, rec.Code)
			}
		})
	}
	t.Run("allowed origin", func(t *testing.T) {
		t.Parallel()
		rec := doRaw(e, rawRoute, "application/zip", strings.NewReader("PK"), "Origin", "http://localhost:3100")
		if rec.Code != http.StatusOK {
			t.Errorf("POST %s from the UI origin: status = %d, want 200", rawRoute, rec.Code)
		}
	})
	t.Run("foreign host", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, rawRoute, strings.NewReader("PK"))
		req.Host = "rebind.evil.example"
		req.Header.Set("Content-Type", "application/zip")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s with a foreign Host: status = %d, want 403", rawRoute, rec.Code)
		}
	})
}

func TestRawBodyLeavesOtherRoutesUnchanged(t *testing.T) {
	t.Parallel()
	e := newRawEcho(t)
	tests := []struct {
		name        string
		contentType string
		size        int
		want        int
	}{
		{"text/plain is still read as a JSON string", "text/plain", 2, http.StatusOK},
		{"zip is still 415", "application/zip", 2, http.StatusUnsupportedMediaType},
		{"default limit at 1 MiB", "text/plain", 1 << 20, http.StatusOK},
		{"default limit over 1 MiB", "text/plain", 1<<20 + 1, http.StatusRequestEntityTooLarge},
		{"json over the default limit", "application/json", 1<<20 + 1, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := strings.Repeat("a", tc.size)
			if tc.contentType == "application/json" {
				body = jsonPadded(tc.size)
			}
			rec := doRaw(e, "/other", tc.contentType, strings.NewReader(body))
			if rec.Code != tc.want {
				t.Fatalf("POST /other as %q with %d bytes: status = %d, want %d", tc.contentType, tc.size, rec.Code, tc.want)
			}
			if tc.want == http.StatusUnsupportedMediaType {
				// Unchanged: the framework 415 keeps its internal_error code.
				if got := errorCode(t, rec); got != "internal_error" {
					t.Errorf("error code = %q, want internal_error", got)
				}
			}
		})
	}
	t.Run("text/plain body becomes a JSON string", func(t *testing.T) {
		t.Parallel()
		rec := doRaw(e, "/other", "text/plain", strings.NewReader("hi"))
		if got, want := rec.Body.String(), `"hi"`; got != want {
			t.Errorf("POST /other as text/plain: body = %q, want %q", got, want)
		}
	})
	t.Run("other method on the raw path keeps the JSON rules", func(t *testing.T) {
		t.Parallel()
		e := httpx.New(httpx.Options{
			Log:       slog.New(slog.DiscardHandler),
			RawBodies: map[string]map[string]int64{"POST /up": {"application/zip": 8}},
		})
		e.PUT("/up", func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
		req := httptest.NewRequest(http.MethodPut, "/up", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("PUT /up: status = %d, want 204", rec.Code)
		}
	})
}

func TestReadRawBodyOutsideRawRouteFails(t *testing.T) {
	t.Parallel()
	e := newRawEcho(t)
	rec := doRaw(e, "/misconfigured", "application/json", strings.NewReader("{}"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("ReadRawBody on a route without RawBodies: status = %d, want 500", rec.Code)
	}
}
