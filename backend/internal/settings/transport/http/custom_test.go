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

func TestPutSettings_customProviderIDs(t *testing.T) { // FR7, AC8
	defined := map[string]bool{"custom:synapse": true}
	tests := []struct {
		name string
		body string
		want []issueView // nil: accepted
	}{
		{name: "default_model with a defined custom provider", body: `{"default_model":{"provider":"custom:synapse","model":"m"}}`},
		{name: "feature_models with a defined custom provider", body: `{"feature_models":{"conventions":{"provider":"custom:synapse","model":"m"}}}`},
		{name: "default_model undefined", body: `{"default_model":{"provider":"custom:ghost","model":"m"}}`, want: []issueView{
			{Keyword: "custom", InstancePath: "/default_model/provider", Message: "Custom provider custom:ghost is not configured"}}},
		{name: "feature_models undefined", body: `{"feature_models":{"conventions":{"provider":"custom:ghost","model":"m"}}}`, want: []issueView{
			{Keyword: "custom", InstancePath: "/feature_models/conventions/provider", Message: "Custom provider custom:ghost is not configured"}}},
		{name: "both undefined are both reported", body: `{"default_model":{"provider":"custom:ghost","model":"m"},"feature_models":{"conventions":{"provider":"custom:ghost","model":"m"}}}`, want: []issueView{
			{Keyword: "custom", InstancePath: "/feature_models/conventions/provider", Message: "Custom provider custom:ghost is not configured"},
			{Keyword: "custom", InstancePath: "/default_model/provider", Message: "Custom provider custom:ghost is not configured"}}},
		{name: "malformed custom id is an invalid value as today", body: `{"default_model":{"provider":"custom:Bad","model":"m"}}`, want: []issueView{
			{Keyword: "invalid_enum_value", InstancePath: "/default_model/provider"}}},
		{name: "empty slug is an invalid value", body: `{"default_model":{"provider":"custom:","model":"m"}}`, want: []issueView{
			{Keyword: "invalid_enum_value", InstancePath: "/default_model/provider"}}},
		{name: "bare slug is an invalid value", body: `{"default_model":{"provider":"synapse","model":"m"}}`, want: []issueView{
			{Keyword: "invalid_enum_value", InstancePath: "/default_model/provider"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{defined: defined}
			rec := do(newRouter(svc, fakeIdentities{}), http.MethodPut, "/settings", "application/json", tc.body)
			if tc.want == nil {
				if rec.Code != http.StatusOK {
					t.Fatalf("PUT /settings %s status = %d, want 200 (%s)", tc.body, rec.Code, rec.Body)
				}
				if svc.updates != 1 {
					t.Errorf("Update called %d times, want 1", svc.updates)
				}
				return
			}
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("PUT /settings %s status = %d, want 422 (%s)", tc.body, rec.Code, rec.Body)
			}
			got := issues(t, rec)
			for i := range got {
				if got[i].Keyword == "invalid_enum_value" {
					got[i].Message = "" // the enum text is the existing one
				}
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PUT /settings %s issues mismatch (-want +got):\n%s", tc.body, diff)
			}
			if svc.updates != 0 {
				t.Errorf("Update called %d times on an invalid body, want 0", svc.updates)
			}
		})
	}
}

func TestPutSettings_customProviderIsLookedUpInTheWorkspace(t *testing.T) { // FR7
	svc := &fakeService{defined: map[string]bool{"custom:synapse": true}}
	rec := do(newRouter(svc, fakeIdentities{}), http.MethodPut, "/settings", "application/json",
		`{"default_model":{"provider":"custom:synapse","model":"m"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if diff := cmp.Diff([]string{"ws/custom:synapse"}, svc.definedAsked); diff != "" {
		t.Errorf("CustomDefined calls mismatch (-want +got):\n%s", diff)
	}
}

func TestPutSettings_builtInProvidersNeverAskTheCustomPort(t *testing.T) { // NFR11
	svc := &fakeService{}
	h := newRouter(svc, fakeIdentities{err: errors.New("not provisioned")})
	rec := do(h, http.MethodPut, "/settings", "application/json", `{"default_model":{"provider":"openai","model":"m"}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 from the missing workspace as before", rec.Code)
	}
	if len(svc.definedAsked) != 0 {
		t.Errorf("CustomDefined asked %v for a built-in provider, want no call", svc.definedAsked)
	}
	rec = do(h, http.MethodPut, "/settings", "application/json", `{"default_model":{"provider":"nope","model":"m"}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid built-in id status = %d, want 422 before the workspace is needed", rec.Code)
	}
}

func TestPutSettings_customProviderWithoutWorkspaceIs500NotValidation(t *testing.T) { // fail closed, no misleading 422
	svc := &fakeService{defined: map[string]bool{"custom:synapse": true}}
	rec := do(newRouter(svc, fakeIdentities{err: errors.New("not provisioned")}), http.MethodPut, "/settings", "application/json",
		`{"default_model":{"provider":"custom:synapse","model":"m"}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (%s)", rec.Code, rec.Body)
	}
	if svc.updates != 0 {
		t.Errorf("Update called %d times, want 0", svc.updates)
	}
}

func TestTestConnection_customProvider(t *testing.T) { // FR11, AC11
	t.Run("accepted id goes to the custom use case with the workspace and key", func(t *testing.T) {
		svc := &fakeService{customResult: app.ConnTestResult{Provider: "custom:synapse", OK: true, Message: "OK — 2 models available",
			LatencyMS: ptr(5), Model: ptr("a"), ModelReachable: ptr(true)}}
		rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json",
			`{"provider":"custom:synapse","key":"k-1"}`)
		want := `{"provider":"custom:synapse","ok":true,"message":"OK — 2 models available","latency_ms":5,"model":"a","model_reachable":true}`
		if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != want {
			t.Errorf("POST test-connection = %d %s, want 200 %s", rec.Code, got, want)
		}
		if svc.gotCustomWS != "ws" || svc.gotCustomID != "custom:synapse" || svc.gotCustomKey != "k-1" {
			t.Errorf("TestCustomConnection got (%q, %q, %q), want (ws, custom:synapse, k-1)", svc.gotCustomWS, svc.gotCustomID, svc.gotCustomKey)
		}
		if svc.gotProv != "" {
			t.Errorf("the built-in TestConnection was called with %q", svc.gotProv)
		}
	})
	t.Run("a gateway without a model list passes with a warning and null reachability", func(t *testing.T) {
		warn := "Model list not available; key not verified"
		svc := &fakeService{customResult: app.ConnTestResult{Provider: "custom:synapse", OK: true, Message: "OK — 0 models available",
			LatencyMS: ptr(5), Warning: &warn}}
		rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json",
			`{"provider":"custom:synapse"}`)
		want := `{"provider":"custom:synapse","ok":true,"message":"OK — 0 models available","latency_ms":5,"model":null,"model_reachable":null,"warning":"Model list not available; key not verified"}`
		if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != want {
			t.Errorf("POST test-connection = %d %s, want 200 %s", rec.Code, got, want)
		}
	})
	t.Run("a failed test is data with null extras and no warning key", func(t *testing.T) {
		svc := &fakeService{customResult: app.ConnTestResult{Provider: "custom:synapse", Message: "Invalid API key"}}
		rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json",
			`{"provider":"custom:synapse"}`)
		want := `{"provider":"custom:synapse","ok":false,"message":"Invalid API key","latency_ms":null,"model":null,"model_reachable":null}`
		if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != want {
			t.Errorf("POST test-connection = %d %s, want 200 %s", rec.Code, got, want)
		}
	})
	t.Run("an undefined provider is 404 custom_provider_not_found", func(t *testing.T) {
		svc := &fakeService{customErr: apperr.NotFound("Custom provider custom:ghost is not configured").WithCode("custom_provider_not_found")}
		rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json",
			`{"provider":"custom:ghost","key":"k"}`)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"custom_provider_not_found"`) {
			t.Errorf("POST test-connection = %d %s, want 404 custom_provider_not_found", rec.Code, rec.Body)
		}
	})
	t.Run("no workspace is an error, not a test", func(t *testing.T) {
		svc := &fakeService{}
		rec := do(newRouter(svc, fakeIdentities{err: errors.New("not provisioned")}), http.MethodPost, "/settings/test-connection", "application/json",
			`{"provider":"custom:synapse"}`)
		if rec.Code != http.StatusInternalServerError || svc.customCalls != 0 {
			t.Errorf("status = %d, custom calls = %d, want 500 and 0", rec.Code, svc.customCalls)
		}
	})
	t.Run("a malformed custom id is a validation error", func(t *testing.T) {
		svc := &fakeService{}
		rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json",
			`{"provider":"custom:Bad"}`)
		if rec.Code != http.StatusUnprocessableEntity || svc.customCalls != 0 {
			t.Errorf("status = %d, custom calls = %d, want 422 and 0", rec.Code, svc.customCalls)
		}
	})
}

func TestCustomProviders_withoutAPortEveryCustomIDIsUndefined(t *testing.T) { // FR7, FR11, NFR11: real stack, no custom port wired
	s := newStack(t, stackOpts{seed: true})
	rec := s.call(http.MethodPut, "/settings", `{"default_model":{"provider":"custom:ghost","model":"m"}}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Custom provider custom:ghost is not configured") {
		t.Errorf("PUT default_model custom:ghost = %d %s, want 422 not configured", rec.Code, rec.Body)
	}
	rec = s.call(http.MethodPost, "/settings/test-connection", `{"provider":"custom:ghost","key":"k"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"custom_provider_not_found"`) {
		t.Errorf("POST test-connection custom:ghost = %d %s, want 404 custom_provider_not_found", rec.Code, rec.Body)
	}
	rec = s.call(http.MethodGet, "/settings/secrets-status", "")
	if want := map[string]any{"openai": false, "anthropic": false, "openrouter": false, "github": false}; rec.Code != http.StatusOK {
		t.Errorf("GET secrets-status = %d", rec.Code)
	} else if diff := cmp.Diff(want, jsonMap(t, rec)); diff != "" {
		t.Errorf("secrets-status keeps its four keys (-want +got):\n%s", diff)
	}
}
