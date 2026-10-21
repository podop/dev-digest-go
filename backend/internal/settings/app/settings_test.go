package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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
	login string
	err   error
}

func (f fakeGitHub) CurrentLogin(context.Context) (string, error) { return f.login, f.err }

type fakeProviders struct {
	n       int
	err     error
	gotKey  string
	gotProv app.Provider
}

func (f *fakeProviders) CountModels(_ context.Context, p app.Provider, key string) (int, error) {
	f.gotProv, f.gotKey = p, key
	return f.n, f.err
}

type rig struct {
	svc       *app.Service
	store     *fakeStore
	secrets   *fakeSecrets
	providers *fakeProviders
}

func newRig(gh fakeGitHub) *rig {
	r := &rig{store: &fakeStore{}, secrets: &fakeSecrets{}, providers: &fakeProviders{}}
	r.svc = app.NewService(app.Deps{Store: r.store, Secrets: r.secrets, GitHub: gh, Providers: r.providers})
	return r
}

func TestGet_emptyStoreIsEmptyBag(t *testing.T) {
	r := newRig(fakeGitHub{})
	got, err := r.svc.Get(t.Context(), "ws")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if diff := cmp.Diff(app.Bag{}, got); diff != "" {
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
	want := app.Bag{"theme": json.RawMessage(`"light"`), "density": json.RawMessage(`"regular"`)}
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

func TestTestConnection(t *testing.T) {
	const key = "sk-secret-123"
	tests := []struct {
		name      string
		provider  app.Provider
		key       string
		preset    map[string]string
		gh        fakeGitHub
		n         int
		provErr   error
		setErr    error
		want      app.ConnTestResult
		wantSaved map[string]string
	}{
		{
			name: "github ok saves the key", provider: app.GitHub, key: "ghp_test", gh: fakeGitHub{login: "octo"},
			want:      app.ConnTestResult{Provider: app.GitHub, OK: true, Message: "Connected as @octo"},
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
			name: "openrouter ok", provider: app.OpenRouter, key: key, n: 3,
			want:      app.ConnTestResult{Provider: app.OpenRouter, OK: true, Message: "OK — 3 models available"},
			wantSaved: map[string]string{"OPENROUTER_API_KEY": key},
		},
		{
			name: "stored key is used when none is posted", provider: app.Anthropic,
			preset: map[string]string{"ANTHROPIC_API_KEY": "stored"}, n: 7,
			want:      app.ConnTestResult{Provider: app.Anthropic, OK: true, Message: "OK — 7 models available"},
			wantSaved: map[string]string{"ANTHROPIC_API_KEY": "stored"},
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
			r.providers.n, r.providers.err = tc.n, tc.provErr

			got := r.svc.TestConnection(t.Context(), tc.provider, tc.key)
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
		})
	}
}

func TestTestConnection_providerGetsStoredKey(t *testing.T) {
	r := newRig(fakeGitHub{})
	r.svc.TestConnection(t.Context(), app.OpenAI, "sk-new")
	if r.providers.gotProv != app.OpenAI || r.providers.gotKey != "sk-new" {
		t.Errorf("CountModels called with (%s, %q), want (openai, sk-new)", r.providers.gotProv, r.providers.gotKey)
	}
}
