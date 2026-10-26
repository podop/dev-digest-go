package settingshttp_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/settings/app"
)

func TestTestConnection_gitlabValidation(t *testing.T) { // FR9
	tests := []struct {
		name string
		body string
		want []issueView
	}{
		{name: "connection_id missing", body: `{"provider":"gitlab"}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/connection_id", Message: "Required"}}},
		{name: "connection_id null", body: `{"provider":"gitlab","connection_id":null}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/connection_id", Message: "Expected string, received null"}}},
		{name: "connection_id not a string", body: `{"provider":"gitlab","connection_id":7}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/connection_id", Message: "Expected string, received number"}}},
		{name: "connection_id empty", body: `{"provider":"gitlab","connection_id":""}`, want: []issueView{{Keyword: "too_small", InstancePath: "/connection_id", Message: "String must contain at least 1 character(s)"}}},
		{name: "key and connection_id are both checked", body: `{"provider":"gitlab","key":""}`, want: []issueView{
			{Keyword: "too_small", InstancePath: "/key", Message: "String must contain at least 1 character(s)"},
			{Keyword: "invalid_type", InstancePath: "/connection_id", Message: "Required"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("POST test-connection %s status = %d, want 422 (%s)", tc.body, rec.Code, rec.Body)
			}
			if diff := cmp.Diff(tc.want, issues(t, rec)); diff != "" {
				t.Errorf("POST test-connection %s issues mismatch (-want +got):\n%s", tc.body, diff)
			}
			if svc.gitlabCalls != 0 {
				t.Errorf("TestGitLabConnection called %d times on an invalid body, want 0", svc.gitlabCalls)
			}
		})
	}
}

func TestTestConnection_connectionIDIsIgnoredForOtherProviders(t *testing.T) { // FR9
	for _, body := range []string{
		`{"provider":"openai","connection_id":7}`,
		`{"provider":"github","connection_id":""}`,
		`{"provider":"custom:synapse","connection_id":null}`,
	} {
		svc := &fakeService{}
		rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json", body)
		if rec.Code != http.StatusOK || svc.gitlabCalls != 0 {
			t.Errorf("POST test-connection %s = %d, gitlab calls = %d, want 200 and 0", body, rec.Code, svc.gitlabCalls)
		}
	}
}

func TestTestConnection_gitlab(t *testing.T) { // FR9, FR10, FR11
	post := func(svc *fakeService, body string) (int, string) {
		rec := do(newRouter(svc, fakeIdentities{err: errors.New("a GitLab test needs no workspace")}), http.MethodPost, "/settings/test-connection", "application/json", body)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	t.Run("a passed test carries every key", func(t *testing.T) {
		svc := &fakeService{gitlabResult: app.ConnTestResult{
			Provider: app.GitLab, OK: true, Message: "Connected as @ann", LatencyMS: ptr(12), Login: strp("ann"),
			Scopes: []string{"api"}, ExpiresAt: strp("2026-11-01"), InstanceVersion: strp("17.5.1-ee"), TokenAccess: strp("full"),
			Warnings: []app.TestWarning{{Code: "token_expiring", Message: "Token expires on 2026-11-01 (in 6 days)"}},
		}}
		code, got := post(svc, `{"provider":"gitlab","connection_id":"c-1","key":"glpat-k"}`)
		want := `{"provider":"gitlab","ok":true,"message":"Connected as @ann","latency_ms":12,"login":"ann","scopes":["api"],` +
			`"expires_at":"2026-11-01","instance_version":"17.5.1-ee","token_access":"full",` +
			`"warnings":[{"code":"token_expiring","message":"Token expires on 2026-11-01 (in 6 days)"}]}`
		if code != http.StatusOK || got != want {
			t.Errorf("POST test-connection = %d %s, want 200 %s", code, got, want)
		}
		if svc.gotGitLabID != "c-1" || svc.gotGitLabKey != "glpat-k" || svc.gotProv != "" || svc.customCalls != 0 {
			t.Errorf("port got (%q, %q), built-in provider %q, custom calls %d; want (c-1, glpat-k), none, 0",
				svc.gotGitLabID, svc.gotGitLabKey, svc.gotProv, svc.customCalls)
		}
	})
	t.Run("a failed test is data: null extras and an empty warnings list", func(t *testing.T) {
		svc := &fakeService{gitlabResult: app.ConnTestResult{Provider: app.GitLab, Message: "GitLab connection corp has no token"}}
		code, got := post(svc, `{"provider":"gitlab","connection_id":"c-1"}`)
		want := `{"provider":"gitlab","ok":false,"message":"GitLab connection corp has no token","latency_ms":null,"login":null,"scopes":null,` +
			`"expires_at":null,"instance_version":null,"token_access":null,"warnings":[]}`
		if code != http.StatusOK || got != want {
			t.Errorf("POST test-connection = %d %s, want 200 %s", code, got, want)
		}
		if svc.gotGitLabKey != "" {
			t.Errorf("port key = %q, want none", svc.gotGitLabKey)
		}
	})
	t.Run("unknown scopes stay null while the test passes", func(t *testing.T) {
		svc := &fakeService{gitlabResult: app.ConnTestResult{Provider: app.GitLab, OK: true, Message: "Connected as @ann", LatencyMS: ptr(3), Login: strp("ann"),
			Warnings: []app.TestWarning{{Code: "scopes_unknown", Message: "m"}, {Code: "version_unknown", Message: "v"}}}}
		code, got := post(svc, `{"provider":"gitlab","connection_id":"c-1"}`)
		want := `{"provider":"gitlab","ok":true,"message":"Connected as @ann","latency_ms":3,"login":"ann","scopes":null,` +
			`"expires_at":null,"instance_version":null,"token_access":null,` +
			`"warnings":[{"code":"scopes_unknown","message":"m"},{"code":"version_unknown","message":"v"}]}`
		if code != http.StatusOK || got != want {
			t.Errorf("POST test-connection = %d %s, want 200 %s", code, got, want)
		}
	})
	t.Run("an unknown connection is 404 gitlab_connection_not_found", func(t *testing.T) {
		svc := &fakeService{gitlabErr: apperr.NotFound("GitLab connection not found").WithCode("gitlab_connection_not_found")}
		code, got := post(svc, `{"provider":"gitlab","connection_id":"ghost","key":"k"}`)
		if code != http.StatusNotFound || !strings.Contains(got, `"code":"gitlab_connection_not_found"`) {
			t.Errorf("POST test-connection = %d %s, want 404 gitlab_connection_not_found", code, got)
		}
	})
}

func TestTestConnection_gitlabWithoutAPortIsUnknown(t *testing.T) { // FR9: real stack, no GitLab port wired
	s := newStack(t, stackOpts{seed: true})
	rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"gitlab","connection_id":"c-1","key":"k"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"gitlab_connection_not_found"`) {
		t.Errorf("POST test-connection gitlab = %d %s, want 404 gitlab_connection_not_found", rec.Code, rec.Body)
	}
}
