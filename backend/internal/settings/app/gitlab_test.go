package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/settings/app"
)

type fakeGitLab struct {
	result app.ConnTestResult
	err    error

	calls  int
	gotID  string
	gotKey string
}

func (f *fakeGitLab) TestConnection(_ context.Context, id, key string) (app.ConnTestResult, error) {
	f.calls++
	f.gotID, f.gotKey = id, key
	return f.result, f.err
}

func newGitLabRig(g *fakeGitLab) (*app.Service, *fakeSecrets, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	secrets := &fakeSecrets{}
	clock := &stepClock{now: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), step: time.Millisecond}
	deps := app.Deps{
		Store: &fakeStore{}, Secrets: secrets, GitHub: fakeGitHub{}, Providers: &fakeProviders{},
		Clock: clock.Now, Logger: slog.New(slog.NewTextHandler(logs, nil)),
	}
	if g != nil {
		deps.GitLab = g
	}
	return app.NewService(deps), secrets, logs
}

func TestTestGitLabConnection_delegatesSetsProviderAndLogsWithoutTheKey(t *testing.T) { // FR9, FR10
	login, access := "ann", "full"
	g := &fakeGitLab{result: app.ConnTestResult{OK: true, Message: "Connected as @ann", Login: &login, TokenAccess: &access,
		Warnings: []app.TestWarning{{Code: "token_expiring", Message: "Token expires on 2026-11-01 (in 6 days)"}}}}
	svc, secrets, logs := newGitLabRig(g)

	got, err := svc.TestGitLabConnection(t.Context(), "c-1", "glpat-secret-token-value")
	if err != nil {
		t.Fatalf("TestGitLabConnection() error = %v", err)
	}
	want := app.ConnTestResult{Provider: app.GitLab, OK: true, Message: "Connected as @ann", Login: &login, TokenAccess: &access,
		Warnings: []app.TestWarning{{Code: "token_expiring", Message: "Token expires on 2026-11-01 (in 6 days)"}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("TestGitLabConnection() mismatch (-want +got):\n%s", diff)
	}
	if g.gotID != "c-1" || g.gotKey != "glpat-secret-token-value" {
		t.Errorf("port got (%q, %q), want (c-1, glpat-secret-token-value)", g.gotID, g.gotKey)
	}
	if len(secrets.values) != 0 {
		t.Errorf("settings secrets written %v, want none: the GitLab token belongs to the connection", secrets.values)
	}
	line := logs.String()
	if !strings.Contains(line, "provider=gitlab") || !strings.Contains(line, "ok=true") || strings.Contains(line, "glpat-secret") || strings.Contains(line, "c-1") {
		t.Errorf("log = %q, want provider and ok, no key and no connection id", line)
	}
}

func TestTestGitLabConnection_scrubsTheKeyFromMessageAndWarnings(t *testing.T) {
	g := &fakeGitLab{result: app.ConnTestResult{Message: "401 for glpat-secret-token-value",
		Warnings: []app.TestWarning{{Code: "scopes_unknown", Message: "echo glpat-secret-token-value"}}}}
	svc, _, _ := newGitLabRig(g)
	got, err := svc.TestGitLabConnection(t.Context(), "c-1", "glpat-secret-token-value")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Message, "glpat-secret") || strings.Contains(got.Warnings[0].Message, "glpat-secret") {
		t.Errorf("TestGitLabConnection() = %q / %q, want the key scrubbed", got.Message, got.Warnings[0].Message)
	}
	if got.Warnings[0].Code != "scopes_unknown" {
		t.Errorf("warning code = %q, want scopes_unknown", got.Warnings[0].Code)
	}
}

func TestTestGitLabConnection_errors(t *testing.T) {
	notFound := apperr.NotFound("GitLab connection not found").WithCode("gitlab_connection_not_found")
	boom := errors.New("store down")
	tests := []struct {
		name     string
		g        *fakeGitLab // nil: no port
		id       string
		wantCode string // apperr code, "" for a plain error
		wantErr  error
		wantCall bool
	}{
		{name: "no port wired", id: "c-1", wantCode: "gitlab_connection_not_found"},
		{name: "empty id", g: &fakeGitLab{}, id: "", wantCode: "gitlab_connection_not_found"},
		{name: "unknown connection", g: &fakeGitLab{err: notFound}, id: "ghost", wantErr: notFound, wantCall: true},
		{name: "other failure passes through", g: &fakeGitLab{err: boom}, id: "c-1", wantErr: boom, wantCall: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := newGitLabRig(tc.g)
			got, err := svc.TestGitLabConnection(t.Context(), tc.id, "k")
			if err == nil {
				t.Fatalf("TestGitLabConnection(%q) error = nil, want one", tc.id)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("TestGitLabConnection(%q) error = %v, want %v", tc.id, err, tc.wantErr)
			}
			if tc.wantCode != "" {
				if ae, ok := apperr.As(err); !ok || ae.Kind != apperr.KindNotFound || ae.Code != tc.wantCode {
					t.Errorf("TestGitLabConnection(%q) error = %v, want a NotFound with code %s", tc.id, err, tc.wantCode)
				}
			}
			if diff := cmp.Diff(app.ConnTestResult{}, got); diff != "" {
				t.Errorf("TestGitLabConnection(%q) result on error (-want +got):\n%s", tc.id, diff)
			}
			if tc.g != nil && (tc.g.calls == 1) != tc.wantCall {
				t.Errorf("port called %d times, want called = %v", tc.g.calls, tc.wantCall)
			}
		})
	}
}

func TestConnTestProviderNames(t *testing.T) {
	want := []string{"openai", "anthropic", "openrouter", "github", "gitlab"}
	if diff := cmp.Diff(want, app.ConnTestProviderNames()); diff != "" {
		t.Errorf("ConnTestProviderNames() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(want[:4], app.ProviderNames()); diff != "" {
		t.Errorf("ProviderNames() must stay the four stored-key providers (-want +got):\n%s", diff)
	}
	if got := app.GitLab.SecretName(); got != "" {
		t.Errorf("GitLab.SecretName() = %q, want none: tokens belong to connections", got)
	}
}
