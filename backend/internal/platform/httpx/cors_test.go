package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"dev-digest-go/backend/internal/platform/httpx"
)

func TestCORSSimpleRequests(t *testing.T) {
	tests := []struct {
		name      string
		origins   []string
		origin    string // empty: no Origin header
		wantAllow string
	}{
		{name: "allowed origin", origins: []string{webOrigin}, origin: webOrigin, wantAllow: webOrigin},
		{name: "second allowed origin", origins: []string{"http://a.test", webOrigin}, origin: webOrigin, wantAllow: webOrigin},
		{name: "disallowed origin", origins: []string{webOrigin}, origin: "http://evil.example"},
		{name: "prefix of allowed origin", origins: []string{webOrigin}, origin: webOrigin + ".evil.example"},
		{name: "no origin header", origins: []string{webOrigin}},
		{name: "empty origin list", origin: webOrigin},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/repos", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			newEcho(t, httpx.Options{CORSOrigins: tc.origins}).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("GET /repos from %q status = %d, want 200", tc.origin, rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllow {
				t.Errorf("GET /repos from %q Allow-Origin = %q, want %q", tc.origin, got, tc.wantAllow)
			}
			wantCreds := ""
			if tc.wantAllow != "" {
				wantCreds = "true"
			}
			if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != wantCreds {
				t.Errorf("GET /repos from %q Allow-Credentials = %q, want %q", tc.origin, got, wantCreds)
			}
			if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Origin" {
				t.Errorf("GET /repos from %q Vary = %q, want [Origin]", tc.origin, got)
			}
		})
	}
}

func TestCORSPreflight(t *testing.T) {
	tests := []struct {
		name        string
		origin      string
		path        string
		reqHeaders  string
		wantAllow   string
		wantMethods string
		wantHeaders string
	}{
		{
			name: "allowed origin", origin: webOrigin, path: "/repos/x/refresh", reqHeaders: "content-type",
			wantAllow: webOrigin, wantMethods: "GET,HEAD,PUT,PATCH,POST,DELETE", wantHeaders: "content-type",
		},
		{
			name: "path that matches no route", origin: webOrigin, path: "/no/such/route",
			wantAllow: webOrigin, wantMethods: "GET,HEAD,PUT,PATCH,POST,DELETE",
		},
		{name: "disallowed origin", origin: "http://evil.example", path: "/repos", reqHeaders: "content-type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodOptions, tc.path, nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			if tc.reqHeaders != "" {
				req.Header.Set("Access-Control-Request-Headers", tc.reqHeaders)
			}
			rec := httptest.NewRecorder()
			newTestEcho(t).ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Errorf("OPTIONS %s status = %d, want 204", tc.path, rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("OPTIONS %s body = %q, want empty", tc.path, rec.Body.String())
			}
			checks := []struct{ header, want string }{
				{"Access-Control-Allow-Origin", tc.wantAllow},
				{"Access-Control-Allow-Methods", tc.wantMethods},
				{"Access-Control-Allow-Headers", tc.wantHeaders},
			}
			for _, ch := range checks {
				if got := rec.Header().Get(ch.header); got != ch.want {
					t.Errorf("OPTIONS %s %s = %q, want %q", tc.path, ch.header, got, ch.want)
				}
			}
		})
	}
}

func TestOptionsWithoutPreflightHeadersIsNotAPreflight(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/repos", nil)
	req.Header.Set("Origin", webOrigin) // no Access-Control-Request-Method
	rec := httptest.NewRecorder()
	newTestEcho(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("OPTIONS /repos without Access-Control-Request-Method status = %d, want 404", rec.Code)
	}
}
