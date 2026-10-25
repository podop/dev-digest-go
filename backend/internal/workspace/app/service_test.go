package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/workspace/app"
	"dev-digest-go/backend/internal/workspace/domain"
)

var errBoom = errors.New("boom")

type fakeNames struct {
	name    string
	found   bool
	readErr error
	setErr  error
	sets    []setCall
}

type setCall struct{ WS, User, Name string }

func (f *fakeNames) WorkspaceName(context.Context, string) (string, bool, error) {
	return f.name, f.found, f.readErr
}

func (f *fakeNames) SetWorkspaceName(_ context.Context, ws, user, name string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.sets = append(f.sets, setCall{ws, user, name})
	f.name, f.found = name, true
	return nil
}

type fakeRepos struct {
	snap app.ReposSnapshot
	err  error
}

func (f fakeRepos) Overview(context.Context, string) (app.ReposSnapshot, error) { return f.snap, f.err }

type fakeSettings struct {
	values map[string]json.RawMessage
	err    error
}

func (f fakeSettings) Settings(context.Context, string) (map[string]json.RawMessage, error) {
	return f.values, f.err
}

type fakeReader struct {
	stats    domain.Stats
	data     domain.ExportData
	statsErr error
	dataErr  error
}

func (f fakeReader) Stats(context.Context, string) (domain.Stats, error) { return f.stats, f.statsErr }
func (f fakeReader) ExportData(context.Context, string) (domain.ExportData, error) {
	return f.data, f.dataErr
}

type fakeHistory struct {
	deleted domain.Deleted
	active  int
	err     error
	calls   int
}

func (f *fakeHistory) Reset(context.Context, string) (domain.Deleted, int, error) {
	f.calls++
	return f.deleted, f.active, f.err
}

var now = time.Date(2026, 10, 25, 23, 59, 59, 987_654_321, time.FixedZone("x", -3*3600))

type harness struct {
	svc      *app.Service
	names    *fakeNames
	history  *fakeHistory
	reader   fakeReader
	settings fakeSettings
	repos    fakeRepos
	logs     *bytes.Buffer
}

// newHarness wires a Service over fakes; tweak the fields, then call build.
func newHarness() *harness {
	return &harness{
		names:   &fakeNames{},
		history: &fakeHistory{},
		repos:   fakeRepos{snap: app.ReposSnapshot{CloneDir: "/data/clones"}},
		logs:    &bytes.Buffer{},
	}
}

func (h *harness) build() *app.Service {
	h.svc = app.NewService(app.Config{
		Names: h.names, Repos: h.repos, Settings: h.settings, Reader: h.reader, History: h.history,
		Clock:   func() time.Time { return now },
		Log:     slog.New(slog.NewJSONHandler(h.logs, nil)),
		DataDir: "/data", Version: "1.2.3",
	})
	return h.svc
}

func TestInfo(t *testing.T) {
	cloned := "/data/clones/a"
	snap := app.ReposSnapshot{CloneDir: "/data/clones", Repos: []app.RepoInfo{{ID: "r1", FullName: "acme/a", ClonePath: &cloned}}}
	tests := []struct {
		name     string
		stored   string
		found    bool
		wantName string
	}{
		{"no row falls back to default", "", false, "default"},
		{"stored name", "Acme Payments", true, "Acme Payments"},
		{"stored blank falls back", "   ", true, "default"},
		{"stored control character falls back", "a\x01b", true, "default"},
		{"stored over 80 falls back", strings.Repeat("x", 81), true, "default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			h.names.name, h.names.found = tc.stored, tc.found
			h.repos.snap = snap
			got, err := h.build().Info(t.Context(), "ws1")
			if err != nil {
				t.Fatalf("Info() error = %v", err)
			}
			want := app.Info{WorkspaceID: "ws1", Name: tc.wantName, DataDir: "/data", CloneDir: "/data/clones", Repos: snap.Repos}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Info() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestInfo_Errors(t *testing.T) {
	t.Run("name read", func(t *testing.T) {
		h := newHarness()
		h.names.readErr = errBoom
		if _, err := h.build().Info(t.Context(), "ws1"); !errors.Is(err, errBoom) {
			t.Errorf("Info() error = %v, want wrapping errBoom", err)
		}
	})
	t.Run("repos read", func(t *testing.T) {
		h := newHarness()
		h.repos.err = errBoom
		if _, err := h.build().Info(t.Context(), "ws1"); !errors.Is(err, errBoom) {
			t.Errorf("Info() error = %v, want wrapping errBoom", err)
		}
	})
}

func TestRename(t *testing.T) {
	h := newHarness()
	got, err := h.build().Rename(t.Context(), "ws1", "u1", "  Acme  ")
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	want := []setCall{{WS: "ws1", User: "u1", Name: "Acme"}}
	if diff := cmp.Diff(want, h.names.sets); diff != "" {
		t.Errorf("stored names mismatch (-want +got), want trimmed with the user id:\n%s", diff)
	}
	if got.Name != "Acme" || got.WorkspaceID != "ws1" || got.DataDir != "/data" {
		t.Errorf("Rename() = %+v, want the Info of the renamed workspace", got)
	}
}

func TestRename_Invalid(t *testing.T) {
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"blank", " \t "},
		{"too long", strings.Repeat("é", 81)},
		{"control character", "a\nb"},
		{"invalid utf-8", "a\xffb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			_, err := h.build().Rename(t.Context(), "ws1", "u1", tc.in)
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != apperr.KindValidation || ae.Code != "validation_error" {
				t.Fatalf("Rename(%q) = %v, want a 422 validation_error", tc.in, err)
			}
			if !errors.Is(err, domain.ErrInvalidName) {
				t.Errorf("Rename(%q) error does not wrap ErrInvalidName", tc.in)
			}
			raw, mErr := json.Marshal(ae.Details)
			if mErr != nil || !strings.Contains(string(raw), `"instancePath":"/name"`) {
				t.Errorf("Rename(%q) details = %s, want an issue at /name", tc.in, raw)
			}
			if len(h.names.sets) != 0 {
				t.Errorf("Rename(%q) stored %v, want nothing stored", tc.in, h.names.sets)
			}
		})
	}
}

func TestRename_StoreError(t *testing.T) {
	h := newHarness()
	h.names.setErr = errBoom
	_, err := h.build().Rename(t.Context(), "ws1", "u1", "Acme")
	var ae *apperr.Error
	if !errors.Is(err, errBoom) || errors.As(err, &ae) {
		t.Errorf("Rename() error = %v, want a plain wrapped store error (500)", err)
	}
}

func TestStats(t *testing.T) {
	h := newHarness()
	h.reader.stats = domain.Stats{Agents: 1, Skills: 2, EvalCases: 3, Learnings: 4, Repos: 5, Runs: 6}
	got, err := h.build().Stats(t.Context(), "ws1")
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if diff := cmp.Diff(h.reader.stats, got); diff != "" {
		t.Errorf("Stats() mismatch (-want +got):\n%s", diff)
	}

	h = newHarness()
	h.reader.statsErr = errBoom
	if _, err := h.build().Stats(t.Context(), "ws1"); !errors.Is(err, errBoom) {
		t.Errorf("Stats() error = %v, want wrapping errBoom", err)
	}
}

func TestExport(t *testing.T) {
	h := newHarness()
	h.names.name, h.names.found = "Acme Payments!", true
	h.reader.data = domain.ExportData{
		Agents: []domain.AgentRecord{{ID: "a1", Name: "Reviewer"}},
		EvalCases: []domain.EvalCaseRecord{
			{Name: "c1", OwnerKind: domain.OwnerAgent, OwnerID: "a1"},
			{Name: "orphan", OwnerKind: domain.OwnerAgent, OwnerID: "gone"},
		},
	}
	h.settings.values = map[string]json.RawMessage{
		"theme":                json.RawMessage(`"dark"`),
		"github_token":         json.RawMessage(`"ghp_secret"`),
		"workspace_name":       json.RawMessage(`"Acme Payments!"`),
		"automatic_reviews":    json.RawMessage(`{"enabled":true,"agent_ids":["a1"],"enabled_at":"2026-01-01T00:00:00.000Z"}`),
		"retention_days":       json.RawMessage(`null`),
		"polling_interval_min": json.RawMessage(`5`),
	}
	got, err := h.build().Export(t.Context(), "ws1")
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if want := "devdigest-workspace-acme-payments-2026-10-26.json"; got.FileName != want {
		t.Errorf("Export() file name = %q, want %q (UTC date)", got.FileName, want)
	}
	m := got.Bundle.Manifest
	if m.Name != "Acme Payments!" || m.Version != "1.2.3" || m.Kind != "workspace" || m.ExportedAt != "2026-10-26T02:59:59.987Z" {
		t.Errorf("Export() manifest = %+v, want name, version 1.2.3, kind workspace, UTC ms instant", m)
	}
	if m.Counts.Agents != 1 || m.Counts.EvalCases != 1 {
		t.Errorf("Export() counts = %+v, want 1 agent and 1 eval case (the orphan is skipped)", m.Counts)
	}
	wantSettings := map[string]json.RawMessage{
		"theme":                json.RawMessage(`"dark"`),
		"automatic_reviews":    json.RawMessage(`{"enabled":true}`),
		"retention_days":       json.RawMessage(`null`),
		"polling_interval_min": json.RawMessage(`5`),
	}
	if diff := cmp.Diff(decodeAll(t, wantSettings), decodeAll(t, got.Bundle.Settings)); diff != "" {
		t.Errorf("Export() settings mismatch (-want +got):\n%s", diff)
	}
}

func TestExport_DefaultVersionAndName(t *testing.T) {
	h := newHarness()
	h.svc = app.NewService(app.Config{
		Names: h.names, Repos: h.repos, Settings: h.settings, Reader: h.reader, History: h.history,
		Clock: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler),
	})
	got, err := h.svc.Export(t.Context(), "ws1")
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if got.Bundle.Manifest.Version != "dev" || got.Bundle.Manifest.Name != "default" {
		t.Errorf("Export() manifest = %+v, want version dev and name default", got.Bundle.Manifest)
	}
}

func TestExport_Errors(t *testing.T) {
	tests := []struct {
		name string
		set  func(*harness)
	}{
		{"name read", func(h *harness) { h.names.readErr = errBoom }},
		{"data read", func(h *harness) { h.reader.dataErr = errBoom }},
		{"settings read", func(h *harness) { h.settings.err = errBoom }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			tc.set(h)
			if _, err := h.build().Export(t.Context(), "ws1"); !errors.Is(err, errBoom) {
				t.Errorf("Export() error = %v, want wrapping errBoom", err)
			}
		})
	}
}

func TestReset(t *testing.T) {
	h := newHarness()
	h.names.name, h.names.found = "Acme Payments", true
	h.history.deleted = domain.Deleted{AgentRuns: 3, RunTraces: 4, Reviews: 5, Findings: 6, MultiAgentRuns: 7,
		EvalSuiteRuns: 8, EvalRuns: 9, CIRuns: 10, ComposedReviews: 11, Learnings: 12, PRsUnmarked: 13}
	got, err := h.build().Reset(t.Context(), "ws1", "Acme Payments")
	if err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if diff := cmp.Diff(h.history.deleted, got); diff != "" {
		t.Errorf("Reset() mismatch (-want +got):\n%s", diff)
	}

	lines := strings.Split(strings.TrimSpace(h.logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("Reset() wrote %d log lines, want 1:\n%s", len(lines), h.logs)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if rec["level"] != "INFO" {
		t.Errorf("log level = %v, want INFO", rec["level"])
	}
	counts := map[string]any{"agent_runs": 3., "run_traces": 4., "reviews": 5., "findings": 6., "multi_agent_runs": 7.,
		"eval_suite_runs": 8., "eval_runs": 9., "ci_runs": 10., "composed_reviews": 11., "learnings": 12., "prs_unmarked": 13.}
	for _, std := range []string{"time", "level", "msg"} {
		delete(rec, std)
	}
	if diff := cmp.Diff(counts, rec); diff != "" {
		t.Errorf("log fields mismatch (-want +got), want counts only:\n%s", diff)
	}
	if strings.Contains(h.logs.String(), "Acme") || strings.Contains(h.logs.String(), "ws1") {
		t.Errorf("log line holds the name or workspace id: %s", h.logs)
	}
}

func TestReset_Mismatch(t *testing.T) {
	tests := []struct {
		name, stored, confirm string
	}{
		{"case differs", "Acme", "acme"},
		{"untrimmed", "Acme", "Acme "},
		{"stale name", "Renamed", "Acme"},
		{"empty", "Acme", ""},
		{"default name when none stored", "", "Default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			h.names.name, h.names.found = tc.stored, tc.stored != ""
			_, err := h.build().Reset(t.Context(), "ws1", tc.confirm)
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != apperr.KindValidation || ae.Code != "confirmation_mismatch" ||
				ae.Message != "Type the workspace name exactly to confirm" {
				t.Fatalf("Reset(%q) = %v, want 422 confirmation_mismatch", tc.confirm, err)
			}
			if h.history.calls != 0 {
				t.Errorf("Reset(%q) called the store %d times, want 0", tc.confirm, h.history.calls)
			}
			if h.logs.Len() != 0 {
				t.Errorf("Reset(%q) logged %s, want no log line", tc.confirm, h.logs)
			}
		})
	}
}

func TestReset_DefaultNameConfirms(t *testing.T) {
	h := newHarness()
	if _, err := h.build().Reset(t.Context(), "ws1", "default"); err != nil {
		t.Errorf("Reset(default) error = %v, want nil with no stored name", err)
	}
}

func TestReset_RunActive(t *testing.T) {
	h := newHarness()
	h.history.active = 3
	h.history.deleted = domain.Deleted{AgentRuns: 99}
	got, err := h.build().Reset(t.Context(), "ws1", "default")
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindConflict || ae.Code != "run_active" {
		t.Fatalf("Reset() = %v, want 409 run_active", err)
	}
	if want := "3 runs are in progress — wait for them to finish or cancel them"; ae.Message != want {
		t.Errorf("Reset() message = %q, want %q", ae.Message, want)
	}
	if got != (domain.Deleted{}) {
		t.Errorf("Reset() deleted = %+v, want zero when refused", got)
	}
	if h.logs.Len() != 0 {
		t.Errorf("Reset() logged %s, want no log line when refused", h.logs)
	}
}

func TestReset_Errors(t *testing.T) {
	t.Run("name read", func(t *testing.T) {
		h := newHarness()
		h.names.readErr = errBoom
		if _, err := h.build().Reset(t.Context(), "ws1", "default"); !errors.Is(err, errBoom) {
			t.Errorf("Reset() error = %v, want wrapping errBoom", err)
		}
	})
	t.Run("store", func(t *testing.T) {
		h := newHarness()
		h.history.err = errBoom
		_, err := h.build().Reset(t.Context(), "ws1", "default")
		var ae *apperr.Error
		if !errors.Is(err, errBoom) || errors.As(err, &ae) {
			t.Errorf("Reset() error = %v, want a plain wrapped store error (500)", err)
		}
		if h.logs.Len() != 0 {
			t.Errorf("Reset() logged %s, want no log line on failure", h.logs)
		}
	})
}

// decodeAll decodes raw JSON values so that formatting differences do not matter.
func decodeAll(t *testing.T, in map[string]json.RawMessage) map[string]any {
	t.Helper()
	out := make(map[string]any, len(in))
	for k, v := range in {
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			t.Fatalf("setting %s is not JSON: %v", k, err)
		}
		out[k] = x
	}
	return out
}
