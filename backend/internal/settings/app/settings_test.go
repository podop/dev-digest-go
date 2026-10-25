package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/settings/app"
)

type fakeStore struct {
	bag       app.Bag
	listErr   error
	upsertErr error
	upserts   int
}

func (f *fakeStore) List(context.Context, string) (app.Bag, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := app.Bag{}
	for k, v := range f.bag {
		out[k] = v
	}
	return out, nil
}

func (f *fakeStore) Upsert(_ context.Context, _, _ string, entries app.Bag) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserts++
	if f.bag == nil {
		f.bag = app.Bag{}
	}
	for k, v := range entries {
		f.bag[k] = v
	}
	return nil
}

type fakeSecrets struct {
	values map[string]string
	setErr error
}

func (f *fakeSecrets) Get(name string) string { return f.values[name] }

func (f *fakeSecrets) Set(name, value string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[name] = value
	return nil
}

type fakeGitHub struct {
	user github.User
	err  error
}

func (f fakeGitHub) CurrentUser(context.Context) (github.User, error) { return f.user, f.err }

type fakeProviders struct {
	list    app.ModelList
	err     error
	gotKey  string
	gotProv app.Provider
	calls   int
}

func (f *fakeProviders) ListModels(_ context.Context, p app.Provider, key string) (app.ModelList, error) {
	f.calls++
	f.gotProv, f.gotKey = p, key
	return f.list, f.err
}

// stepClock advances by step on every reading, so a latency is a known number of steps.
type stepClock struct {
	now  time.Time
	step time.Duration
}

func (c *stepClock) Now() time.Time {
	t := c.now
	c.now = c.now.Add(c.step)
	return t
}

type rig struct {
	svc       *app.Service
	store     *fakeStore
	secrets   *fakeSecrets
	providers *fakeProviders
	logs      *bytes.Buffer
}

func newRig(gh fakeGitHub) *rig {
	r := &rig{store: &fakeStore{}, secrets: &fakeSecrets{}, providers: &fakeProviders{}, logs: &bytes.Buffer{}}
	clock := &stepClock{now: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), step: 7 * time.Millisecond}
	r.svc = app.NewService(app.Deps{
		Store: r.store, Secrets: r.secrets, GitHub: gh, Providers: r.providers,
		Clock: clock.Now, Logger: slog.New(slog.NewTextHandler(r.logs, nil)),
	})
	return r
}

func ids(ids ...string) app.ModelList { return app.ModelList{IDs: ids, Count: len(ids)} }

func TestGet_emptyStoreHoldsOnlyTheAutomaticReviewsDefault(t *testing.T) { // AC1
	r := newRig(fakeGitHub{})
	got, err := r.svc.Get(t.Context(), "ws")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	want := app.Bag{app.AutomaticReviewsKey: json.RawMessage(`{"enabled":false,"agent_ids":[],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Get() mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdate_returnsFullBagAfterWrite(t *testing.T) {
	r := newRig(fakeGitHub{})
	r.store.bag = app.Bag{"theme": json.RawMessage(`"dark"`), "density": json.RawMessage(`"regular"`)}

	got, err := r.svc.Update(t.Context(), "ws", "user", app.Bag{"theme": json.RawMessage(`"light"`)})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	want := app.Bag{"theme": json.RawMessage(`"light"`), "density": json.RawMessage(`"regular"`), app.AutomaticReviewsKey: defaultAutoJSON}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Update() mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdate_emptyPatchWritesNothing(t *testing.T) {
	r := newRig(fakeGitHub{})
	if _, err := r.svc.Update(t.Context(), "ws", "user", app.Bag{}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if r.store.upserts != 0 {
		t.Errorf("upserts = %d, want 0", r.store.upserts)
	}
}

func TestUpdate_storeErrorsAreWrapped(t *testing.T) {
	boom := errors.New("boom")
	r := newRig(fakeGitHub{})
	r.store.upsertErr = boom
	if _, err := r.svc.Update(t.Context(), "ws", "user", app.Bag{"a": json.RawMessage(`1`)}); !errors.Is(err, boom) {
		t.Errorf("Update() error = %v, want it to wrap %v", err, boom)
	}
	r = newRig(fakeGitHub{})
	r.store.listErr = boom
	if _, err := r.svc.Get(t.Context(), "ws"); !errors.Is(err, boom) {
		t.Errorf("Get() error = %v, want it to wrap %v", err, boom)
	}
}

func TestSecretsStatus(t *testing.T) {
	r := newRig(fakeGitHub{})
	r.secrets.values = map[string]string{"OPENAI_API_KEY": "sk-a", "GITHUB_TOKEN": "ghp", "ANTHROPIC_API_KEY": ""}
	want := app.SecretsStatus{OpenAI: true, GitHub: true}
	if got := r.svc.SecretsStatus(); got != want {
		t.Errorf("SecretsStatus() = %+v, want %+v", got, want)
	}
}

func ptr[T any](v T) *T { return &v }

func TestTestConnection(t *testing.T) {
	const key = "sk-secret-123"
	tests := []struct {
		name      string
		provider  app.Provider
		key       string
		preset    map[string]string
		gh        fakeGitHub
		list      app.ModelList
		provErr   error
		setErr    error
		want      app.ConnTestResult
		wantSaved map[string]string
	}{
		{
			name: "github ok saves the key", provider: app.GitHub, key: "ghp_test",
			gh: fakeGitHub{user: github.User{Login: "octo", Scopes: []string{"repo", "read:org"}, ScopesReported: true}},
			want: app.ConnTestResult{Provider: app.GitHub, OK: true, Message: "Connected as @octo",
				LatencyMS: ptr(7), Login: ptr("octo"), Scopes: []string{"repo", "read:org"}},
			wantSaved: map[string]string{"GITHUB_TOKEN": "ghp_test"},
		},
		{
			name: "github classic token without scopes reports an empty list", provider: app.GitHub, key: "ghp_test",
			gh: fakeGitHub{user: github.User{Login: "octo", Scopes: []string{}, ScopesReported: true}},
			want: app.ConnTestResult{Provider: app.GitHub, OK: true, Message: "Connected as @octo",
				LatencyMS: ptr(7), Login: ptr("octo"), Scopes: []string{}},
			wantSaved: map[string]string{"GITHUB_TOKEN": "ghp_test"},
		},
		{
			name: "github fine-grained token reports no scopes", provider: app.GitHub, key: "ghp_test",
			gh: fakeGitHub{user: github.User{Login: "octo", Scopes: []string{}}},
			want: app.ConnTestResult{Provider: app.GitHub, OK: true, Message: "Connected as @octo",
				LatencyMS: ptr(7), Login: ptr("octo")},
			wantSaved: map[string]string{"GITHUB_TOKEN": "ghp_test"},
		},
		{
			name: "github failure keeps the key and scrubs it", provider: app.GitHub, key: key,
			gh:        fakeGitHub{err: fmt.Errorf("github: HTTP 401: bad token %s", key)},
			want:      app.ConnTestResult{Provider: app.GitHub, Message: "github: HTTP 401: bad token [REDACTED]"},
			wantSaved: map[string]string{"GITHUB_TOKEN": key},
		},
		{
			name: "no key anywhere", provider: app.OpenRouter,
			want: app.ConnTestResult{Provider: app.OpenRouter, Message: "OPENROUTER_API_KEY is not configured"},
		},
		{
			name: "github not configured", provider: app.GitHub,
			want: app.ConnTestResult{Provider: app.GitHub, Message: "GITHUB_TOKEN is not configured"},
		},
		{
			name: "openrouter checks the reference model", provider: app.OpenRouter, key: key,
			list: ids("a/x", "deepseek/deepseek-v4-flash", "c"),
			want: app.ConnTestResult{Provider: app.OpenRouter, OK: true, Message: "OK — 3 models available",
				LatencyMS: ptr(7), Model: ptr("deepseek/deepseek-v4-flash"), ModelReachable: ptr(true)},
			wantSaved: map[string]string{"OPENROUTER_API_KEY": key},
		},
		{
			name: "openrouter reference model not listed", provider: app.OpenRouter, key: key,
			list: ids("a/x"),
			want: app.ConnTestResult{Provider: app.OpenRouter, OK: true, Message: "OK — 1 models available",
				LatencyMS: ptr(7), Model: ptr("deepseek/deepseek-v4-flash"), ModelReachable: ptr(false)},
			wantSaved: map[string]string{"OPENROUTER_API_KEY": key},
		},
		{
			name: "openai checks gpt-4.1 in the unfiltered list", provider: app.OpenAI, key: key,
			list: app.ModelList{IDs: []string{"text-embedding-3", "gpt-4.1"}, Count: 1},
			want: app.ConnTestResult{Provider: app.OpenAI, OK: true, Message: "OK — 1 models available",
				LatencyMS: ptr(7), Model: ptr("gpt-4.1"), ModelReachable: ptr(true)},
			wantSaved: map[string]string{"OPENAI_API_KEY": key},
		},
		{
			name: "anthropic checks the first listed model", provider: app.Anthropic,
			preset: map[string]string{"ANTHROPIC_API_KEY": "stored"}, list: ids("claude-x", "claude-y"),
			want: app.ConnTestResult{Provider: app.Anthropic, OK: true, Message: "OK — 2 models available",
				LatencyMS: ptr(7), Model: ptr("claude-x"), ModelReachable: ptr(true)},
			wantSaved: map[string]string{"ANTHROPIC_API_KEY": "stored"},
		},
		{
			name: "anthropic listing nothing checks no model", provider: app.Anthropic,
			preset: map[string]string{"ANTHROPIC_API_KEY": "stored"},
			want: app.ConnTestResult{Provider: app.Anthropic, OK: true, Message: "OK — 0 models available",
				LatencyMS: ptr(7)},
		},
		{
			name: "provider error is scrubbed everywhere", provider: app.OpenAI, key: key,
			provErr:   fmt.Errorf("OpenAI /models returned 401: Incorrect key %s (%s)", key, key),
			want:      app.ConnTestResult{Provider: app.OpenAI, Message: "OpenAI /models returned 401: Incorrect key [REDACTED] ([REDACTED])"},
			wantSaved: map[string]string{"OPENAI_API_KEY": key},
		},
		{
			name: "save failure is a failed test", provider: app.OpenAI, key: key, setErr: errors.New("create secrets dir: denied"),
			want: app.ConnTestResult{Provider: app.OpenAI, Message: "Could not save the key: create secrets dir: denied"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(tc.gh)
			r.secrets.values = tc.preset
			r.secrets.setErr = tc.setErr
			r.providers.list, r.providers.err = tc.list, tc.provErr

			got := r.svc.TestConnection(t.Context(), "ws", tc.provider, tc.key)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("TestConnection(%s) mismatch (-want +got):\n%s", tc.provider, diff)
			}
			if tc.wantSaved != nil {
				if diff := cmp.Diff(tc.wantSaved, r.secrets.values); diff != "" {
					t.Errorf("saved secrets mismatch (-want +got):\n%s", diff)
				}
			}
			if strings.Contains(got.Message, key) {
				t.Errorf("message %q contains the key", got.Message)
			}
			for _, secret := range []string{key, "ghp_test", "stored"} {
				if strings.Contains(r.logs.String(), secret) {
					t.Errorf("log output %q contains the key %q", r.logs, secret)
				}
			}
		})
	}
}

func TestTestConnection_defaultModel(t *testing.T) {
	list := ids("deepseek/deepseek-v4-flash", "a/x", "gpt-4.1")
	tests := []struct {
		name        string
		provider    app.Provider
		workspaceID string
		bag         app.Bag
		listErr     error
		wantModel   string
		wantReach   bool
		wantWarn    bool
	}{
		{name: "default for the tested provider wins", provider: app.OpenRouter, workspaceID: "ws",
			bag: app.Bag{"default_model": json.RawMessage(`{"provider":"openrouter","model":"a/x"}`)}, wantModel: "a/x", wantReach: true},
		{name: "default not listed is unreachable", provider: app.OpenRouter, workspaceID: "ws",
			bag: app.Bag{"default_model": json.RawMessage(`{"provider":"openrouter","model":"a/y"}`)}, wantModel: "a/y"},
		{name: "default of another provider is ignored", provider: app.OpenAI, workspaceID: "ws",
			bag: app.Bag{"default_model": json.RawMessage(`{"provider":"openrouter","model":"a/x"}`)}, wantModel: "gpt-4.1", wantReach: true},
		{name: "null default uses the reference model", provider: app.OpenRouter, workspaceID: "ws",
			bag: app.Bag{"default_model": json.RawMessage(`null`)}, wantModel: "deepseek/deepseek-v4-flash", wantReach: true},
		{name: "malformed default uses the reference model", provider: app.OpenRouter, workspaceID: "ws",
			bag: app.Bag{"default_model": json.RawMessage(`"x"`)}, wantModel: "deepseek/deepseek-v4-flash", wantReach: true},
		{name: "no workspace warns and uses the reference model", provider: app.OpenRouter,
			wantModel: "deepseek/deepseek-v4-flash", wantReach: true, wantWarn: true},
		{name: "unreadable settings warn and use the reference model", provider: app.OpenRouter, workspaceID: "ws",
			listErr: errors.New("db down"), wantModel: "deepseek/deepseek-v4-flash", wantReach: true, wantWarn: true},
		{name: "default anthropic model replaces the first listed", provider: app.Anthropic, workspaceID: "ws",
			bag: app.Bag{"default_model": json.RawMessage(`{"provider":"anthropic","model":"claude-z"}`)}, wantModel: "claude-z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(fakeGitHub{})
			r.store.bag, r.store.listErr = tc.bag, tc.listErr
			r.providers.list = list
			got := r.svc.TestConnection(t.Context(), tc.workspaceID, tc.provider, "sk-k")
			if !got.OK || got.Model == nil || got.ModelReachable == nil {
				t.Fatalf("TestConnection(%s) = %+v, want ok with a checked model", tc.provider, got)
			}
			if *got.Model != tc.wantModel || *got.ModelReachable != tc.wantReach {
				t.Errorf("TestConnection(%s) model = %q reachable = %v, want %q %v",
					tc.provider, *got.Model, *got.ModelReachable, tc.wantModel, tc.wantReach)
			}
			if warned := strings.Contains(r.logs.String(), "level=WARN"); warned != tc.wantWarn {
				t.Errorf("WARN logged = %v, want %v (logs: %s)", warned, tc.wantWarn, r.logs)
			}
		})
	}
}

func TestTestConnection_doesNotReadSettingsForGitHubOrFailures(t *testing.T) {
	r := newRig(fakeGitHub{user: github.User{Login: "octo"}})
	r.store.listErr = errors.New("must not be read")
	r.svc.TestConnection(t.Context(), "ws", app.GitHub, "ghp")
	r.providers.err = errors.New("boom")
	r.svc.TestConnection(t.Context(), "ws", app.OpenAI, "sk")
	if strings.Contains(r.logs.String(), "level=WARN") {
		t.Errorf("settings were read for a GitHub test or a failed test: %s", r.logs)
	}
}

func TestTestConnection_logsOneInfoLine(t *testing.T) { // NFR6
	r := newRig(fakeGitHub{})
	r.providers.list = ids("deepseek/deepseek-v4-flash")
	r.svc.TestConnection(t.Context(), "ws", app.OpenRouter, "sk-abc")
	lines := strings.Split(strings.TrimSpace(r.logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1: %q", len(lines), lines)
	}
	for _, want := range []string{"level=INFO", "provider=openrouter", "ok=true", "latency_ms="} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("log line %q lacks %q", lines[0], want)
		}
	}
}

func TestTestConnection_providerGetsStoredKey(t *testing.T) {
	r := newRig(fakeGitHub{})
	r.svc.TestConnection(t.Context(), "ws", app.OpenAI, "sk-new")
	if r.providers.gotProv != app.OpenAI || r.providers.gotKey != "sk-new" {
		t.Errorf("ListModels called with (%s, %q), want (openai, sk-new)", r.providers.gotProv, r.providers.gotKey)
	}
}
