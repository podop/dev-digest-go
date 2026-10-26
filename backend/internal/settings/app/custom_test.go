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

type fakeCustom struct {
	defined map[string]bool
	result  app.ConnTestResult
	err     error

	definedAsked []string
	gotWS        string
	gotID        string
	gotKey       string
	calls        int
}

func (f *fakeCustom) Defined(_ context.Context, ws, id string) bool {
	f.definedAsked = append(f.definedAsked, ws+"/"+id)
	return f.defined[id]
}

func (f *fakeCustom) TestConnection(_ context.Context, ws, id, key string) (app.ConnTestResult, error) {
	f.calls++
	f.gotWS, f.gotID, f.gotKey = ws, id, key
	return f.result, f.err
}

func newCustomRig(c *fakeCustom) (*app.Service, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	clock := &stepClock{now: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), step: time.Millisecond}
	deps := app.Deps{
		Store: &fakeStore{}, Secrets: &fakeSecrets{}, GitHub: fakeGitHub{}, Providers: &fakeProviders{},
		Clock: clock.Now, Logger: slog.New(slog.NewTextHandler(logs, nil)),
	}
	if c != nil {
		deps.Custom = c
	}
	return app.NewService(deps), logs
}

func TestCustomDefined(t *testing.T) { // FR7
	c := &fakeCustom{defined: map[string]bool{"custom:synapse": true}}
	svc, _ := newCustomRig(c)
	tests := []struct {
		name, ws, id string
		want         bool
		asked        []string
	}{
		{name: "defined", ws: "ws", id: "custom:synapse", want: true, asked: []string{"ws/custom:synapse"}},
		{name: "undefined", ws: "ws", id: "custom:ghost", want: false, asked: []string{"ws/custom:ghost"}},
		{name: "malformed id is not asked", ws: "ws", id: "custom:Bad"},
		{name: "built-in id is not asked", ws: "ws", id: "openai"},
		{name: "no workspace is not asked", ws: "", id: "custom:synapse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c.definedAsked = nil
			if got := svc.CustomDefined(t.Context(), tc.ws, tc.id); got != tc.want {
				t.Errorf("CustomDefined(%q, %q) = %v, want %v", tc.ws, tc.id, got, tc.want)
			}
			if diff := cmp.Diff(tc.asked, c.definedAsked); diff != "" {
				t.Errorf("CustomDefined(%q, %q) port calls mismatch (-want +got):\n%s", tc.ws, tc.id, diff)
			}
		})
	}
}

func TestCustomDefined_withoutPortNothingIsDefined(t *testing.T) { // NFR11
	svc, _ := newCustomRig(nil)
	if svc.CustomDefined(t.Context(), "ws", "custom:synapse") {
		t.Error("CustomDefined() = true without a custom providers port, want false")
	}
}

func TestProviderIsCustom(t *testing.T) {
	tests := map[app.Provider]bool{
		"custom:synapse": true, "custom:a-1": true,
		"custom:Bad": false, "custom:": false, "synapse": false, app.OpenAI: false, app.GitHub: false,
	}
	for p, want := range tests {
		if got := p.IsCustom(); got != want {
			t.Errorf("Provider(%q).IsCustom() = %v, want %v", p, got, want)
		}
	}
}

func TestTestCustomConnection_delegatesAndLogsWithoutTheKey(t *testing.T) { // FR11, AC11
	warn := "Model list not available; key not verified"
	c := &fakeCustom{result: app.ConnTestResult{OK: true, Message: "OK — 0 models available", Warning: &warn}}
	svc, logs := newCustomRig(c)

	got, err := svc.TestCustomConnection(t.Context(), "ws", "custom:synapse", "sk-secret")
	if err != nil {
		t.Fatalf("TestCustomConnection() error = %v", err)
	}
	want := app.ConnTestResult{Provider: "custom:synapse", OK: true, Message: "OK — 0 models available", Warning: &warn}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("TestCustomConnection() mismatch (-want +got):\n%s", diff)
	}
	if c.gotWS != "ws" || c.gotID != "custom:synapse" || c.gotKey != "sk-secret" {
		t.Errorf("port got (%q, %q, %q), want (ws, custom:synapse, sk-secret)", c.gotWS, c.gotID, c.gotKey)
	}
	line := logs.String()
	if !strings.Contains(line, "provider=custom:synapse") || !strings.Contains(line, "ok=true") || strings.Contains(line, "sk-secret") {
		t.Errorf("log = %q, want provider and ok and no key", line)
	}
}

func TestTestCustomConnection_scrubsTheKeyFromTheResult(t *testing.T) {
	warn := "gateway said sk-secret"
	c := &fakeCustom{result: app.ConnTestResult{Message: "401 for sk-secret", Warning: &warn}}
	svc, _ := newCustomRig(c)
	got, err := svc.TestCustomConnection(t.Context(), "ws", "custom:synapse", "sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Message, "sk-secret") || strings.Contains(*got.Warning, "sk-secret") {
		t.Errorf("TestCustomConnection() = %q / %q, want the key scrubbed", got.Message, *got.Warning)
	}
}

func TestTestCustomConnection_errors(t *testing.T) {
	notFound := apperr.NotFound("Custom provider custom:ghost is not configured").WithCode("custom_provider_not_found")
	boom := errors.New("db down")
	tests := []struct {
		name string
		c    *fakeCustom // nil: no port
		ws   string
		id   app.Provider
		want error
	}{
		{name: "the port's not found passes through", c: &fakeCustom{err: notFound}, ws: "ws", id: "custom:ghost", want: notFound},
		{name: "the port's other error passes through", c: &fakeCustom{err: boom}, ws: "ws", id: "custom:synapse", want: boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newCustomRig(tc.c)
			if _, err := svc.TestCustomConnection(t.Context(), tc.ws, tc.id, "k"); !errors.Is(err, tc.want) {
				t.Errorf("TestCustomConnection(%q) error = %v, want %v", tc.id, err, tc.want)
			}
		})
	}

	notCalled := []struct {
		name string
		c    *fakeCustom
		ws   string
		id   app.Provider
	}{
		{name: "no port", ws: "ws", id: "custom:synapse"},
		{name: "no workspace", c: &fakeCustom{}, ws: "", id: "custom:synapse"},
		{name: "malformed id", c: &fakeCustom{}, ws: "ws", id: "custom:Bad"},
		{name: "built-in id", c: &fakeCustom{}, ws: "ws", id: app.OpenAI},
	}
	for _, tc := range notCalled {
		t.Run(tc.name+" is a 404 without a call", func(t *testing.T) {
			svc, _ := newCustomRig(tc.c)
			_, err := svc.TestCustomConnection(t.Context(), tc.ws, tc.id, "k")
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != apperr.KindNotFound || ae.Code != "custom_provider_not_found" {
				t.Fatalf("TestCustomConnection(%q) error = %v, want 404 custom_provider_not_found", tc.id, err)
			}
			if tc.c != nil && tc.c.calls != 0 {
				t.Errorf("port called %d times, want 0", tc.c.calls)
			}
		})
	}
}

func TestTestConnection_customIdIsNotABuiltInProvider(t *testing.T) { // the built-in path stays closed
	c := &fakeCustom{}
	svc, _ := newCustomRig(c)
	got := svc.TestConnection(t.Context(), "ws", "custom:synapse", "k")
	if got.OK || got.Message != "Unknown provider" || c.calls != 0 {
		t.Errorf("TestConnection(custom:synapse) = %+v (port calls %d), want Unknown provider and no delegation", got, c.calls)
	}
}
