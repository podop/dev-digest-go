package settingshttp_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
)

// The attack bodies a cross-site page can send without a CORS preflight: a "simple" request
// whose Content-Type is text/plain (fetch with mode no-cors, or <form enctype="text/plain">).
const (
	attackPut  = `{"github_token":"x","polling_interval_min":5}`
	attackConn = `{"provider":"github","key":"ATTACKER"}`
)

func TestCSRF_PlainTextFromForeignOriginNeverReachesTheService(t *testing.T) {
	tests := []struct {
		method, path, body string
	}{
		{http.MethodPut, "/settings", attackPut},
		{http.MethodPost, "/settings/test-connection", attackConn},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			svc := &fakeService{}
			rec := do(newRouter(svc, fakeIdentities{}), tc.method, tc.path, "text/plain;charset=UTF-8", tc.body, "Origin", foreignOrigin)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 (%s)", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
			}
			if svc.updates != 0 || svc.gotKey != "" || svc.gotProv != "" {
				t.Errorf("service saw updates=%d provider=%q key=%q, want no call", svc.updates, svc.gotProv, svc.gotKey)
			}
		})
	}
}

func TestCSRF_PlainTextIsAStringNotAnObject(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		headers                  []string
	}{
		{"PUT without origin", http.MethodPut, "/settings", attackPut, nil},
		{"PUT own origin", http.MethodPut, "/settings", attackPut, []string{"Origin", uiOrigin}},
		{"test-connection without origin", http.MethodPost, "/settings/test-connection", attackConn, nil},
		{"test-connection own origin", http.MethodPost, "/settings/test-connection", attackConn, []string{"Origin", uiOrigin}},
		{"empty text/plain body", http.MethodPut, "/settings", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := do(newRouter(svc, fakeIdentities{}), tc.method, tc.path, "text/plain", tc.body, tc.headers...)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body)
			}
			want := []issueView{{Keyword: "invalid_type", InstancePath: "/", Message: "Expected object, received string"}}
			if diff := cmp.Diff(want, issues(t, rec)); diff != "" {
				t.Errorf("issues mismatch (-want +got):\n%s", diff)
			}
			if svc.updates != 0 || svc.gotKey != "" || svc.gotProv != "" {
				t.Errorf("service saw updates=%d provider=%q key=%q, want no call", svc.updates, svc.gotProv, svc.gotKey)
			}
		})
	}
}

func TestCSRF_JSONStillWorks(t *testing.T) {
	for _, origin := range [][]string{nil, {"Origin", uiOrigin}} {
		svc := &fakeService{}
		h := newRouter(svc, fakeIdentities{})
		if rec := do(h, http.MethodPut, "/settings", "application/json; charset=utf-8", `{"polling_interval_min":5}`, origin...); rec.Code != http.StatusOK || svc.updates != 1 {
			t.Errorf("PUT /settings (headers %v) = %d with %d updates, want 200 with 1 (%s)", origin, rec.Code, svc.updates, rec.Body)
		}
		if rec := do(h, http.MethodPost, "/settings/test-connection", "Application/JSON", attackConn, origin...); rec.Code != http.StatusOK || svc.gotKey != "ATTACKER" {
			t.Errorf("POST test-connection (headers %v) = %d key %q, want 200 and the key passed on (%s)", origin, rec.Code, svc.gotKey, rec.Body)
		}
	}
}

// The reported exploit: a text/plain test-connection from another site overwrote the stored
// GitHub token. The stored secret must stay as it was.
func TestCSRF_StoredTokenSurvivesForeignPlainTextPost(t *testing.T) {
	s := newStack(t, stackOpts{seed: true, secretsIn: `{"GITHUB_TOKEN":"ghp_mine"}`})
	rec := do(s.e, http.MethodPost, "/settings/test-connection", "text/plain;charset=UTF-8", attackConn, "Origin", foreignOrigin)
	if rec.Code != http.StatusForbidden {
		t.Errorf("foreign text/plain test-connection = %d, want 403", rec.Code)
	}
	rec = do(s.e, http.MethodPost, "/settings/test-connection", "text/plain;charset=UTF-8", attackConn)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("text/plain test-connection without Origin = %d, want 422", rec.Code)
	}
	raw, err := os.ReadFile(s.secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"GITHUB_TOKEN":"ghp_mine"}`; got != want {
		t.Errorf("secrets file = %s, want it untouched: %s", got, want)
	}
	if got := s.up.lastAuth(); got != "" {
		t.Errorf("upstream saw Authorization %q, want no call", got)
	}
}
