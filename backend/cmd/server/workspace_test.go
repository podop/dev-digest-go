package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	settingsapp "dev-digest-go/backend/internal/settings/app"
	workspacedomain "dev-digest-go/backend/internal/workspace/domain"
)

// Wiring of the workspace context (plan S15): GET and PUT /workspace come from it (data dir and
// display name), and the trace pruner runs from the start until the stop signal.

func TestWorkspace_RoutesAreWired(t *testing.T) {
	s := startStack(t, spec{})

	got := s.get("/workspace")
	if got.Status != http.StatusOK {
		t.Fatalf("GET /workspace = %d %s, want 200", got.Status, got.Body)
	}
	body := got.Obj(t)
	if body["name"] != "default" || body["dataDir"] != s.cfg.DataDir {
		t.Errorf("GET /workspace name/dataDir = %v/%v, want default/%s", body["name"], body["dataDir"], s.cfg.DataDir)
	}

	put := s.do(http.MethodPut, "/workspace", map[string]any{"name": "  Acme  "})
	if put.Status != http.StatusOK || put.Obj(t)["name"] != "Acme" {
		t.Fatalf("PUT /workspace = %d %s, want 200 with the trimmed name Acme", put.Status, put.Body)
	}
	if name := s.get("/workspace").Obj(t)["name"]; name != "Acme" {
		t.Errorf("GET /workspace name after the rename = %v, want Acme", name)
	}
}

func TestWorkspace_PrunerRunsUntilStop(t *testing.T) {
	s := startStack(t, spec{pruneInterval: 20 * time.Millisecond})

	eventually(t, 30*time.Second, "two trace prune passes", func() bool {
		return len(logLines(t, s.logs, "trace prune done")) >= 2
	})
	if err := s.stop(); err != nil {
		t.Fatalf("serve after the stop signal = %v, want nil", err)
	}
	// serve has returned, so the pruner goroutine was waited for: no pass may start any more.
	n := len(logLines(t, s.logs, "trace prune done"))
	time.Sleep(100 * time.Millisecond)
	if after := len(logLines(t, s.logs, "trace prune done")); after != n {
		t.Errorf("trace prune passes after serve returned = %d, want %d (the pruner outlived serve)", after, n)
	}
	if got := logLines(t, s.logs, "trace prune failed"); len(got) != 0 {
		t.Errorf("trace prune failed lines = %v, want none (a shutdown is not a failure)", got)
	}
}

// fakeSettings is an in-memory settingsRW.
type fakeSettings struct {
	bag settingsapp.Bag
	err error
}

func (f *fakeSettings) Get(context.Context, string) (settingsapp.Bag, error) { return f.bag, f.err }

func (f *fakeSettings) Update(_ context.Context, _, _ string, e settingsapp.Bag) (settingsapp.Bag, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.bag == nil {
		f.bag = settingsapp.Bag{}
	}
	for k, v := range e {
		f.bag[k] = v
	}
	return f.bag, nil
}

func TestWorkspaceNames(t *testing.T) {
	tests := []struct {
		name      string
		bag       settingsapp.Bag
		wantName  string
		wantFound bool
	}{
		{"absent", settingsapp.Bag{}, "", false},
		{"string", settingsapp.Bag{"workspace_name": json.RawMessage(`"Acme"`)}, "Acme", true},
		{"null is found but empty", settingsapp.Bag{"workspace_name": json.RawMessage(`null`)}, "", true},
		{"number is found but empty", settingsapp.Bag{"workspace_name": json.RawMessage(`7`)}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := workspaceNames{settings: &fakeSettings{bag: tc.bag}}.WorkspaceName(t.Context(), "w")
			if err != nil || got != tc.wantName || found != tc.wantFound {
				t.Errorf("WorkspaceName() = %q, %v, %v; want %q, %v, nil", got, found, err, tc.wantName, tc.wantFound)
			}
		})
	}

	t.Run("set stores a JSON string", func(t *testing.T) {
		fs := &fakeSettings{}
		if err := (workspaceNames{settings: fs}).SetWorkspaceName(t.Context(), "w", "u", `A "b"`); err != nil {
			t.Fatalf("SetWorkspaceName() = %v, want nil", err)
		}
		if got := string(fs.bag["workspace_name"]); got != `"A \"b\""` {
			t.Errorf("stored workspace_name = %s, want \"A \\\"b\\\"\"", got)
		}
	})
	t.Run("read error is returned", func(t *testing.T) {
		boom := errors.New("boom")
		if _, _, err := (workspaceNames{settings: &fakeSettings{err: boom}}).WorkspaceName(t.Context(), "w"); !errors.Is(err, boom) {
			t.Errorf("WorkspaceName() error = %v, want it to wrap boom", err)
		}
	})
}

func TestWorkspaceSettings_Retention(t *testing.T) {
	tests := []struct {
		name    string
		bag     settingsapp.Bag
		want    workspacedomain.Retention
		wantErr error
	}{
		{"absent is the default", settingsapp.Bag{}, workspacedomain.DefaultRetention(), nil},
		{"null keeps forever", settingsapp.Bag{"retention_days": json.RawMessage(`null`)}, workspacedomain.KeepForever(), nil},
		{"a choice", settingsapp.Bag{"retention_days": json.RawMessage(`30`)}, workspacedomain.Retention{Days: 30}, nil},
		{"not a choice", settingsapp.Bag{"retention_days": json.RawMessage(`45`)}, workspacedomain.Retention{}, workspacedomain.ErrInvalidRetention},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspaceSettings{settings: &fakeSettings{bag: tc.bag}}.Retention(t.Context(), "w")
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Errorf("Retention() = %+v, %v; want %+v, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
