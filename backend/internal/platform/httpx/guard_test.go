package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
)

func TestHostCheck(t *testing.T) {
	allowed := []string{"localhost", "127.0.0.1", "[::1]", "Backend"}
	tests := []struct {
		name    string
		allowed []string
		host    string
		want    int
	}{
		{"localhost with port", allowed, "localhost:3001", http.StatusOK},
		{"loopback ip", allowed, "127.0.0.1:3101", http.StatusOK},
		{"ipv6 loopback", allowed, "[::1]:3001", http.StatusOK},
		{"service name any case", allowed, "BACKEND:3001", http.StatusOK},
		{"foreign host", allowed, "evil.example", http.StatusForbidden},
		{"foreign host with allowed port", allowed, "evil.example:3001", http.StatusForbidden},
		{"allowed name as suffix", allowed, "localhost.evil.example", http.StatusForbidden},
		{"allowed name as prefix", allowed, "evil-localhost", http.StatusForbidden},
		{"unbracketed ipv6", allowed, "::1", http.StatusForbidden},
		{"allowed name with non-numeric port", allowed, "localhost:3100.evil.example", http.StatusForbidden},
		{"no well-formed entry fails closed", []string{"::1", "localhost:x"}, "localhost:3001", http.StatusForbidden},
		{"empty host list disables the check", nil, "evil.example", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEcho(t, httpx.Options{CORSOrigins: []string{webOrigin}, AllowedHosts: tc.allowed})
			for _, path := range []string{"/ok", "/health", "/nope"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Host = tc.host
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				want := tc.want
				if want == http.StatusOK && path == "/nope" {
					want = http.StatusNotFound
				}
				if rec.Code != want {
					t.Errorf("GET %s with Host %q = %d, want %d", path, tc.host, rec.Code, want)
				}
				if tc.want == http.StatusForbidden {
					wantBody := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Host not allowed"}}
					if diff := cmp.Diff(wantBody, decodeJSON(t, rec)); diff != "" {
						t.Errorf("GET %s body mismatch (-want +got):\n%s", path, diff)
					}
				}
			}
		})
	}
}

func TestOriginCheck(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		origin *string // nil: no Origin header
		want   int
	}{
		{"PUT foreign origin", http.MethodPut, "/settings", ptr("http://evil.example"), http.StatusForbidden},
		{"POST foreign origin", http.MethodPost, "/echo", ptr("http://evil.example"), http.StatusForbidden},
		{"DELETE foreign origin", http.MethodDelete, "/echo", ptr("http://evil.example"), http.StatusForbidden},
		{"POST null origin", http.MethodPost, "/echo", ptr("null"), http.StatusForbidden},
		{"POST prefix of allowed origin", http.MethodPost, "/echo", ptr(webOrigin + ".evil.example"), http.StatusForbidden},
		{"POST empty origin", http.MethodPost, "/echo", ptr(""), http.StatusForbidden},
		{"foreign origin on unknown route", http.MethodPost, "/nope", ptr("http://evil.example"), http.StatusForbidden},
		{"POST own origin", http.MethodPost, "/echo", ptr(webOrigin), http.StatusOK},
		{"PUT own origin", http.MethodPut, "/settings", ptr(webOrigin), http.StatusOK},
		{"POST without origin", http.MethodPost, "/echo", nil, http.StatusOK},
		{"GET foreign origin", http.MethodGet, "/ok", ptr("http://evil.example"), http.StatusOK},
		{"HEAD foreign origin", http.MethodHead, "/ok", ptr("http://evil.example"), http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEcho(t)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"a":1}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.origin != nil {
				req.Header.Set("Origin", *tc.origin)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			want := tc.want
			if want == http.StatusOK && tc.path == "/nope" {
				want = http.StatusNotFound
			}
			if rec.Code != want {
				t.Fatalf("%s %s Origin=%v = %d, want %d (%s)", tc.method, tc.path, tc.origin, rec.Code, want, rec.Body)
			}
			if tc.want == http.StatusForbidden {
				wantBody := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Origin not allowed"}}
				if diff := cmp.Diff(wantBody, decodeJSON(t, rec)); diff != "" {
					t.Errorf("body mismatch (-want +got):\n%s", diff)
				}
				if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
					t.Errorf("Access-Control-Allow-Origin = %q on a refused request, want none", got)
				}
			}
		})
	}
}

func TestOriginCheck_PreflightUnchanged(t *testing.T) {
	e := newTestEcho(t)
	req := httptest.NewRequest(http.MethodOptions, "/settings", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight from a foreign origin = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
	}
}

func TestReadJSONBody(t *testing.T) {
	tests := []struct {
		name        string
		contentType string // empty: header not sent
		body        string
		noBody      bool
		want        string
		wantStatus  int
	}{
		{name: "json", contentType: "application/json", body: `{"a":1}`, want: `{"a":1}`},
		{name: "json with charset", contentType: "application/json; charset=utf-8", body: `{"a":1}`, want: `{"a":1}`},
		{name: "json upper case", contentType: "Application/JSON", body: `{"a":1}`, want: `{"a":1}`},
		{name: "text plain object", contentType: "text/plain;charset=UTF-8", body: `{"key":"ATTACKER"}`, want: `"{\"key\":\"ATTACKER\"}"`},
		{name: "text plain empty", contentType: "text/plain", body: "", want: `""`},
		{name: "text plain not json", contentType: "text/plain", body: "hello", want: `"hello"`},
		{name: "no body, no content type", noBody: true, want: ""},
		{name: "form content type is refused first", contentType: "application/x-www-form-urlencoded", body: "a=1", wantStatus: http.StatusUnsupportedMediaType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := httpx.New(httpx.Options{})
			e.POST("/doc", func(c *echo.Context) error {
				raw, err := httpx.ReadJSONBody(c)
				if err != nil {
					return err
				}
				return c.String(http.StatusOK, string(raw))
			})
			var req *http.Request
			if tc.noBody {
				req = httptest.NewRequest(http.MethodPost, "/doc", nil)
			} else {
				req = httptest.NewRequest(http.MethodPost, "/doc", strings.NewReader(tc.body))
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			wantStatus := tc.wantStatus
			if wantStatus == 0 {
				wantStatus = http.StatusOK
			}
			if rec.Code != wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, wantStatus, rec.Body)
			}
			if wantStatus == http.StatusOK && rec.Body.String() != tc.want {
				t.Errorf("ReadJSONBody = %q, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a JSON object: %q: %v", rec.Body, err)
	}
	return got
}
