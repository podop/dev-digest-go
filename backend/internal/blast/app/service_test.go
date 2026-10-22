package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/blast/app"
	"dev-digest-go/backend/internal/blast/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

const (
	ws = "ws-1"
	pr = "pr-1"
)

// fakeStore holds one pull request; a nil pull means the store does not know it.
type fakeStore struct {
	pull     *domain.Pull
	paths    []string
	pullErr  error
	pathsErr error
}

var _ app.Store = (*fakeStore)(nil)

func (f *fakeStore) FindPull(_ context.Context, workspaceID, prID string) (domain.Pull, error) {
	if f.pullErr != nil {
		return domain.Pull{}, f.pullErr
	}
	if f.pull == nil || workspaceID != ws || prID != pr {
		return domain.Pull{}, domain.ErrNotFound
	}
	return *f.pull, nil
}

func (f *fakeStore) ChangedPaths(context.Context, string) ([]string, error) {
	return f.paths, f.pathsErr
}

type fakeIntel struct {
	res     domain.PortResult
	err     error
	repos   []string
	changed [][]string
}

var _ app.RepoIntel = (*fakeIntel)(nil)

func (f *fakeIntel) BlastRadius(_ context.Context, repoID string, changed []string) (domain.PortResult, error) {
	f.repos = append(f.repos, repoID)
	f.changed = append(f.changed, changed)
	return f.res, f.err
}

type ghCall struct {
	owner, name string
	limit       int
}

type fakeGitHub struct {
	merged []domain.MergedPR
	err    error
	calls  []ghCall
}

var _ app.GitHub = (*fakeGitHub)(nil)

func (f *fakeGitHub) ListMergedPulls(_ context.Context, owner, name string, limit int) ([]domain.MergedPR, error) {
	f.calls = append(f.calls, ghCall{owner, name, limit})
	return f.merged, f.err
}

type rig struct {
	svc   *app.Service
	store *fakeStore
	intel *fakeIntel
	gh    *fakeGitHub
	logs  *bytes.Buffer
}

func newRig(paths ...string) *rig {
	r := &rig{
		store: &fakeStore{
			pull:  &domain.Pull{ID: pr, RepoID: "repo-1", Number: 5, Owner: "acme", Name: "api"},
			paths: paths,
		},
		intel: &fakeIntel{},
		gh:    &fakeGitHub{},
		logs:  &bytes.Buffer{},
	}
	log := slog.New(slog.NewJSONHandler(r.logs, nil))
	r.svc = app.NewService(r.store, r.intel, r.gh, app.Options{Log: log, Redact: func(s string) string {
		return strings.ReplaceAll(s, "ghp_secret", "[redacted]")
	}})
	return r
}

// lines decodes the JSON log lines written so far.
func (r *rig) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(r.logs.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		delete(m, "time")
		out = append(out, m)
	}
	return out
}

func wantNotFound(t *testing.T, what string, err error) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != apperr.KindNotFound || ae.Message != "Pull request not found" {
		t.Errorf("%s error = %v, want 404 \"Pull request not found\"", what, err)
	}
}

// AC5007 (service): one port read over the stored paths; the info line carries the fields of
// spec 7.15 and no reason when the answer is not degraded.
func TestRadius_ReadsOnceAndLogs(t *testing.T) {
	r := newRig("src/a.ts", "src/b.ts")
	r.intel.res = domain.PortResult{
		ChangedSymbols: []domain.ChangedSymbol{{Name: "foo", File: "src/a.ts", Kind: "function"}},
		Callers: []domain.Caller{
			{File: "src/x.ts", Symbol: "h", ViaSymbol: "foo", Line: 4, Rank: 0.5},
			{File: "src/y.ts", Symbol: "g", ViaSymbol: "foo", Line: 9, Rank: 0.4},
		},
		FactsByFile: map[string]domain.Facts{"src/x.ts": {Endpoints: []string{"GET /x"}}},
	}

	got, err := r.svc.Radius(t.Context(), ws, pr)
	if err != nil {
		t.Fatalf("Radius() error = %v", err)
	}
	want := domain.BuildRadius(r.intel.res)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Radius() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([][]string{{"src/a.ts", "src/b.ts"}}, r.intel.changed); diff != "" {
		t.Errorf("port reads mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"repo-1"}, r.intel.repos); diff != "" {
		t.Errorf("port repos mismatch (-want +got):\n%s", diff)
	}
	wantLogs := []map[string]any{{
		"level": "INFO", "msg": "blast radius read from index",
		"repoId": "repo-1", "changedFiles": 2.0, "degraded": false, "callers": 2.0,
	}}
	if diff := cmp.Diff(wantLogs, r.lines(t)); diff != "" {
		t.Errorf("log lines mismatch (-want +got):\n%s", diff)
	}
}

func TestRadius_DegradedLogsTheReason(t *testing.T) {
	r := newRig("src/a.ts")
	r.intel.res = domain.PortResult{Degraded: true, Reason: domain.ReasonIndexPartial}

	got, err := r.svc.Radius(t.Context(), ws, pr)
	if err != nil {
		t.Fatalf("Radius() error = %v", err)
	}
	if !got.Degraded || got.Reason != domain.ReasonIndexPartial {
		t.Errorf("Radius() degraded/reason = %v/%q, want true/index_partial", got.Degraded, got.Reason)
	}
	wantLogs := []map[string]any{{
		"level": "INFO", "msg": "blast radius read from index",
		"repoId": "repo-1", "changedFiles": 1.0, "degraded": true, "reason": "index_partial", "callers": 0.0,
	}}
	if diff := cmp.Diff(wantLogs, r.lines(t)); diff != "" {
		t.Errorf("log lines mismatch (-want +got):\n%s", diff)
	}
}

// A pull request without files still goes through the port, which answers it degraded.
func TestRadius_NoFilesStillReadsThePort(t *testing.T) {
	r := newRig()
	r.intel.res = domain.PortResult{Degraded: true, Reason: domain.ReasonNoData}

	got, err := r.svc.Radius(t.Context(), ws, pr)
	if err != nil {
		t.Fatalf("Radius() error = %v", err)
	}
	if !got.Degraded || got.Reason != domain.ReasonNoData || len(r.intel.changed) != 1 || len(r.intel.changed[0]) != 0 {
		t.Errorf("Radius() = %+v after reads %v, want degraded no_data after one empty read", got, r.intel.changed)
	}
}

func TestRadius_Errors(t *testing.T) {
	boom := errors.New("connection reset")
	tests := []struct {
		name  string
		setup func(*rig)
	}{
		{"pull lookup", func(r *rig) { r.store.pullErr = boom }},
		{"paths", func(r *rig) { r.store.pathsErr = boom }},
		{"port", func(r *rig) { r.intel.err = boom }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig("a.ts")
			tc.setup(r)
			_, err := r.svc.Radius(t.Context(), ws, pr)
			if !errors.Is(err, boom) {
				t.Errorf("Radius() error = %v, want it to wrap %v", err, boom)
			}
			if _, ok := apperr.As(err); ok {
				t.Errorf("Radius() error = %v, want a plain error (500 internal_error)", err)
			}
			if got := r.lines(t); len(got) != 0 {
				t.Errorf("log lines = %v, want none", got)
			}
		})
	}
}

func TestRadiusAndHistory_UnknownPullRequestIsNotFound(t *testing.T) {
	for name, tc := range map[string]struct{ ws, pr string }{
		"other workspace": {"ws-2", pr},
		"unknown pull":    {ws, "pr-2"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig("a.ts")
			_, err := r.svc.Radius(t.Context(), tc.ws, tc.pr)
			wantNotFound(t, "Radius()", err)
			_, err = r.svc.History(t.Context(), tc.ws, tc.pr)
			wantNotFound(t, "History()", err)
			if len(r.intel.repos) != 0 || len(r.gh.calls) != 0 {
				t.Errorf("reads = %v / %v, want none", r.intel.repos, r.gh.calls)
			}
		})
	}
}

func TestRadiusFor_ReadsWithoutPullLookupAndLogs(t *testing.T) {
	r := newRig()
	r.store.pull = nil
	r.intel.res = domain.PortResult{Callers: []domain.Caller{{File: "x.ts", Symbol: "h", ViaSymbol: "foo", Line: 1}}}

	if _, err := r.svc.RadiusFor(t.Context(), "repo-9", []string{"a.ts"}); err != nil {
		t.Fatalf("RadiusFor() error = %v", err)
	}
	wantLogs := []map[string]any{{
		"level": "INFO", "msg": "blast radius read from index",
		"repoId": "repo-9", "changedFiles": 1.0, "degraded": false, "callers": 1.0,
	}}
	if diff := cmp.Diff(wantLogs, r.lines(t)); diff != "" {
		t.Errorf("log lines mismatch (-want +got):\n%s", diff)
	}
}

// AC5008 (service): one list read of 30, the selection of the domain.
func TestHistory_SelectsPriorPulls(t *testing.T) {
	r := newRig("src/a.ts", "src/b.ts")
	r.gh.merged = []domain.MergedPR{
		{Number: 5, Title: "Self", Author: "me", MergedAt: "2026-01-02T00:00:00Z", Files: []string{"src/a.ts"}},
		{Number: 4, Title: "Touches a", Author: "ann", MergedAt: "2026-01-01T00:00:00Z", Files: []string{"src/a.ts", "zzz.ts"}},
		{Number: 3, Title: "Unrelated", Author: "bob", MergedAt: "2026-01-01T00:00:00Z", Files: []string{"other.ts"}},
	}

	got, err := r.svc.History(t.Context(), ws, pr)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	want := []domain.PriorPR{{
		PRNumber: 4, Title: "Touches a", MergedAt: "2026-01-01T00:00:00Z", Author: "ann", FilesOverlap: []string{"src/a.ts"},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("History() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]ghCall{{"acme", "api", domain.HistoryListLimit}}, r.gh.calls, cmp.AllowUnexported(ghCall{})); diff != "" {
		t.Errorf("GitHub calls mismatch (-want +got):\n%s", diff)
	}
	if got := r.lines(t); len(got) != 0 {
		t.Errorf("log lines = %v, want none", got)
	}
}

func TestHistory_NoFilesMakesNoGitHubCall(t *testing.T) {
	r := newRig()
	got, err := r.svc.History(t.Context(), ws, pr)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("History(no files) = %#v, %v, want a non-nil empty list", got, err)
	}
	if len(r.gh.calls) != 0 {
		t.Errorf("GitHub calls = %v, want none", r.gh.calls)
	}
}

func TestHistory_GitHubFailureIsAnEmptyListAndOneWarning(t *testing.T) {
	for _, tc := range []struct{ name, msg, wantErr string }{
		{"server error", "GitHub answered 500", "GitHub answered 500"},
		{"no token", "GITHUB_TOKEN is not configured", "GITHUB_TOKEN is not configured"},
		{"secret in text", "bad credentials ghp_secret", "bad credentials [redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig("src/a.ts")
			r.gh.err = errors.New(tc.msg)
			got, err := r.svc.History(t.Context(), ws, pr)
			if err != nil || got == nil || len(got) != 0 {
				t.Errorf("History() = %#v, %v, want a non-nil empty list and no error", got, err)
			}
			wantLogs := []map[string]any{{
				"level": "WARN", "msg": "prior PR history unavailable; returning an empty list",
				"err": tc.wantErr, "repoId": "repo-1",
			}}
			if diff := cmp.Diff(wantLogs, r.lines(t)); diff != "" {
				t.Errorf("log lines mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHistory_StoreErrorsAreInternal(t *testing.T) {
	boom := errors.New("connection reset")
	r := newRig("a.ts")
	r.store.pathsErr = boom
	if _, err := r.svc.History(t.Context(), ws, pr); !errors.Is(err, boom) {
		t.Errorf("History() error = %v, want it to wrap %v", err, boom)
	}
}

func TestNewService_NilDependencyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewService(nil store) did not panic")
		}
	}()
	app.NewService(nil, &fakeIntel{}, &fakeGitHub{}, app.Options{})
}
