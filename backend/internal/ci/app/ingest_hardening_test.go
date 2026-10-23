package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
)

// The artifact of a workflow run is written by whoever can run the workflow, so a verified
// artifact is still hostile data. These tests cover what the golden cannot: values the database
// cannot hold, the call timeout of a refresh and a few caps.

const instSlug = "sec"

func baseResult() obj {
	return obj{
		"findings_count": 2, "critical": 1, "warning": 1, "suggestion": 0, "cost_usd": 0.0123, "duration_ms": 4500,
		"agent": "Security Reviewer", "version": "2", "pr_number": 42, "commit_sha": "abc123", "repository": "acme/widgets",
		"repository_id": 99, "run_id": 1001, "run_attempt": 1, "model": "deepseek/deepseek-v4-flash",
		"manifest_version": 3, "skills": []string{"a"}, "blockers": 1,
	}
}

// artifactRig has one installation (slug "sec", acme/widgets) whose runs are 1001, 1002, ...; the
// artifact of run i is zips[i].
func artifactRig(t testing.TB, zips ...[]byte) *ingestRig {
	t.Helper()
	rig := newIngestRig(t)
	slug, version := instSlug, 3
	rig.store.installs = []domain.Installation{{
		ID: "inst-1", AgentID: "agent-1", Repo: "acme/widgets", TargetType: "gha",
		InstalledAt: installedAt, Slug: &slug, ManifestVersion: &version,
	}}
	rig.gh.listRuns = func(app.RepoRef, string, int) ([]domain.WorkflowRun, error) {
		conclusion := "success"
		var runs []domain.WorkflowRun
		for i := range zips {
			runs = append(runs, domain.WorkflowRun{
				ID: int64(1001 + i), RunAttempt: 1, HeadSHA: "abc123", Path: domain.WorkflowPath(instSlug),
				Event: "pull_request", Conclusion: &conclusion, HTMLURL: fmt.Sprintf("https://github.com/acme/widgets/actions/runs/%d", 1001+i),
				RepositoryID: 99, PRNumbers: []int{42}, CreatedAt: "2026-03-01T10:00:00Z",
			})
		}
		return runs, nil
	}
	rig.gh.listArtifact = func(_ app.RepoRef, runID int64) ([]domain.RunArtifact, error) {
		return []domain.RunArtifact{{
			ID: runID + 500, Name: domain.ArtifactName(instSlug), SizeInBytes: 500, CreatedAt: "2026-03-01T10:05:00Z",
		}}, nil
	}
	rig.gh.download = func(_ app.RepoRef, id, _ int64) ([]byte, error) { return zips[id-500-1001], nil }
	return rig
}

func resultZip(t testing.TB, raw []byte) []byte {
	t.Helper()
	return zipOf(t, domain.ResultFileName, raw)
}

func zipOf(t testing.TB, name string, content []byte) []byte {
	t.Helper()
	return buildZip(t, obj{"entries": []any{obj{"name": name, "hex": fmt.Sprintf("%x", content)}}})
}

func marshal(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func with(base obj, over obj) obj { return mergeObj(base, over) }

func refresh(t testing.TB, rig *ingestRig) domain.RefreshResult {
	t.Helper()
	got, err := rig.svc.Refresh(t.Context(), "ws-1", "")
	if err != nil {
		t.Fatalf("Refresh() error = %v, want a result", err)
	}
	return got
}

// A verified artifact whose values exceed the target columns, or whose text holds a NUL, is
// skipped as artifact_invalid before any insert: a database error here would fail every refresh
// for as long as GitHub lists the run.
func TestRefreshHostileArtifactIsSkippedBeforeInsert(t *testing.T) {
	tests := []struct {
		name     string
		override obj
	}{
		{"findings_count past int32", obj{"findings_count": 1 << 40}},
		{"findings_count at 2^53-1", obj{"findings_count": 9007199254740991}},
		{"findings_count negative", obj{"findings_count": -1}},
		{"blockers past int32", obj{"blockers": 3_000_000_000}},
		{"blockers negative", obj{"blockers": -5}},
		{"duration_ms past int32", obj{"duration_ms": 2_147_483_648}},
		{"duration_ms negative", obj{"duration_ms": -1}},
		{"pr_number past int32", obj{"pr_number": 4_294_967_296}},
		{"pr_number negative", obj{"pr_number": -7}},
		{"manifest_version past int32", obj{"manifest_version": 2_147_483_648}},
		{"cost past numeric(12,6)", obj{"cost_usd": 1e12}},
		{"cost at 1e6", obj{"cost_usd": 1e6}},
		{"cost rounding up to 1e6", obj{"cost_usd": 999_999.9999996}},
		{"cost negative", obj{"cost_usd": -0.5}},
		{"model with NUL", obj{"model": "deepseek\x00/flash"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := resultZip(t, marshal(t, with(baseResult(), tc.override)))
			good := resultZip(t, marshal(t, with(baseResult(), obj{"run_id": 1002})))
			rig := artifactRig(t, bad, good)

			got := refresh(t, rig)

			want := []domain.SkippedRun{{GithubRunID: 1001, Reason: domain.SkipInvalid}}
			if diff := cmp.Diff(want, got.Installations[0].Skipped); diff != "" {
				t.Errorf("Skipped mismatch (-want +got):\n%s", diff)
			}
			if got.Installations[0].State != domain.StateOK || got.Degraded {
				t.Errorf("state = %s degraded = %v, want ok and not degraded", got.Installations[0].State, got.Degraded)
			}
			// The next run is still processed: one hostile artifact does not stop the refresh.
			if len(rig.store.recorded) != 1 || rig.store.recorded[0].GithubRunID != 1002 || got.Ingested != 1 {
				t.Errorf("recorded = %+v ingested = %d, want only run 1002", rig.store.recorded, got.Ingested)
			}
			for _, c := range rig.store.calls.snapshot(t) {
				if asObj(c)["fn"] == "recordRun" && asObj(c)["key"] == "1001:1" {
					t.Errorf("RecordRun called for the hostile run")
				}
			}
		})
	}
}

func TestRefreshInvalidUTF8ArtifactIsSkipped(t *testing.T) {
	raw := bytes.Replace(marshal(t, with(baseResult(), obj{"model": "MODEL"})), []byte("MODEL"), []byte{'a', 0xff, 'b'}, 1)
	rig := artifactRig(t, resultZip(t, raw))
	got := refresh(t, rig)
	want := []domain.SkippedRun{{GithubRunID: 1001, Reason: domain.SkipInvalid}}
	if diff := cmp.Diff(want, got.Installations[0].Skipped); diff != "" {
		t.Errorf("Skipped mismatch (-want +got):\n%s", diff)
	}
	if len(rig.store.recorded) != 0 {
		t.Errorf("recorded = %+v, want nothing", rig.store.recorded)
	}
}

// The largest values a column holds are stored, not skipped.
func TestRefreshStoresValuesAtTheColumnLimits(t *testing.T) {
	limits := obj{
		"findings_count": 2_147_483_647, "blockers": 2_147_483_647, "duration_ms": 2_147_483_647,
		"pr_number": 2_147_483_647, "manifest_version": 2_147_483_647, "cost_usd": 999_999.999999,
	}
	rig := artifactRig(t, resultZip(t, marshal(t, with(baseResult(), limits))))
	got := refresh(t, rig)
	if got.Ingested != 1 || len(got.Installations[0].Skipped) != 0 {
		t.Fatalf("ingested = %d skipped = %+v, want 1 and none", got.Ingested, got.Installations[0].Skipped)
	}
	r := rig.store.recorded[0]
	if r.FindingsCount != 2_147_483_647 || r.Blockers != 2_147_483_647 || *r.DurationMS != 2_147_483_647 || *r.CostUSD != 999_999.999999 {
		t.Errorf("recorded run = %+v, want the limits kept", r)
	}
}

// The store may refuse a value the use case let through (ErrNotStorable); that is a skip as well.
func TestRefreshStoreRefusalIsASkip(t *testing.T) {
	rig := artifactRig(t, resultZip(t, marshal(t, baseResult())))
	rig.store.recordErr = fmt.Errorf("record run: %w", app.ErrNotStorable)
	got := refresh(t, rig)
	want := []domain.SkippedRun{{GithubRunID: 1001, Reason: domain.SkipInvalid}}
	if diff := cmp.Diff(want, got.Installations[0].Skipped); diff != "" {
		t.Errorf("Skipped mismatch (-want +got):\n%s", diff)
	}
	if got.Ingested != 0 || got.Degraded {
		t.Errorf("ingested = %d degraded = %v, want 0 and not degraded", got.Ingested, got.Degraded)
	}
}

// A ZIP bomb: a 1 KiB-ish download whose entry inflates far past the JSON cap.
func TestRefreshZipBombIsTooLarge(t *testing.T) {
	bomb := zipOf(t, domain.ResultFileName, bytes.Repeat([]byte{' '}, 8<<20))
	if len(bomb) >= domain.MaxArtifactZipBytes {
		t.Fatalf("test zip is %d bytes, want it small", len(bomb))
	}
	rig := artifactRig(t, bomb)
	got := refresh(t, rig)
	want := []domain.SkippedRun{{GithubRunID: 1001, Reason: domain.SkipTooLarge}}
	if diff := cmp.Diff(want, got.Installations[0].Skipped); diff != "" {
		t.Errorf("Skipped mismatch (-want +got):\n%s", diff)
	}
}

func TestRefreshTwoEntriesOfTheResultNameAreInvalid(t *testing.T) {
	var buf = buildZip(t, obj{"entries": []any{
		obj{"name": domain.ResultFileName, "text": "{}"}, obj{"name": domain.ResultFileName, "text": "{}"},
	}})
	rig := artifactRig(t, buf)
	got := refresh(t, rig)
	want := []domain.SkippedRun{{GithubRunID: 1001, Reason: domain.SkipInvalid}}
	if diff := cmp.Diff(want, got.Installations[0].Skipped); diff != "" {
		t.Errorf("Skipped mismatch (-want +got):\n%s", diff)
	}
}

// An adapter that hands over more than the cap is not trusted.
func TestRefreshOversizedDownloadIsTooLarge(t *testing.T) {
	rig := artifactRig(t, []byte{})
	rig.gh.download = func(app.RepoRef, int64, int64) ([]byte, error) {
		return make([]byte, domain.MaxArtifactZipBytes+1), nil
	}
	got := refresh(t, rig)
	want := []domain.SkippedRun{{GithubRunID: 1001, Reason: domain.SkipTooLarge}}
	if diff := cmp.Diff(want, got.Installations[0].Skipped); diff != "" {
		t.Errorf("Skipped mismatch (-want +got):\n%s", diff)
	}
}

func TestRefreshWrappedTooLargeSentinelIsASkip(t *testing.T) {
	rig := artifactRig(t, []byte{})
	rig.gh.download = func(app.RepoRef, int64, int64) ([]byte, error) {
		return nil, fmt.Errorf("download: %w", app.ErrArtifactTooLarge)
	}
	got := refresh(t, rig)
	if got.Installations[0].State != domain.StateOK || len(got.Installations[0].Skipped) != 1 ||
		got.Installations[0].Skipped[0].Reason != domain.SkipTooLarge {
		t.Errorf("installation = %+v, want ok with one artifact_too_large skip", got.Installations[0])
	}
}

// Each GitHub call has its own 30 s timeout (P11), and a call starts only before the 45 s deadline.
func TestRefreshCallTimeoutAndBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newIngestRig(t)
		rig.svc = app.NewIngestService(app.IngestDeps{
			Agents: fakeAgents{}, Store: rig.store, GitHub: rig.gh, Zip: nopReader{}, Log: rig.logs.logger(),
		})
		for _, id := range []string{"1", "2", "3"} {
			slug, version := instSlug, 3
			rig.store.installs = append(rig.store.installs, domain.Installation{
				ID: "inst-" + id, AgentID: "agent-1", Repo: "acme/r" + id, TargetType: "gha",
				InstalledAt: installedAt, Slug: &slug, ManifestVersion: &version,
			})
		}
		start := time.Now()
		var started []time.Duration
		rig.gh.hook = func(ctx context.Context) error {
			started = append(started, time.Since(start))
			dl, ok := ctx.Deadline()
			if !ok || dl.Sub(time.Now()) > domain.GitHubCallTimeout {
				t.Errorf("call context deadline = %v (set %v), want at most %v from now", dl, ok, domain.GitHubCallTimeout)
			}
			<-ctx.Done() // a GitHub call that never answers
			return &app.GitHubError{Transport: true, Cause: ctx.Err()}
		}

		got := refresh(t, rig)

		if diff := cmp.Diff([]time.Duration{0, 30 * time.Second}, started); diff != "" {
			t.Errorf("call start times mismatch (-want +got):\n%s", diff)
		}
		var states []domain.RefreshState
		for _, i := range got.Installations {
			states = append(states, i.State)
		}
		// The third call would start at 60 s, after the 45 s deadline: not_checked.
		want := []domain.RefreshState{domain.StateError, domain.StateError, domain.StateNotChecked}
		if diff := cmp.Diff(want, states); diff != "" {
			t.Errorf("states mismatch (-want +got):\n%s", diff)
		}
		if !got.Degraded {
			t.Error("Degraded = false, want true")
		}
		if elapsed := time.Since(start); elapsed != 60*time.Second {
			t.Errorf("refresh took %v, want 60s (two calls of 30s)", elapsed)
		}
	})
}

// A cancelled request stops the refresh instead of marking every installation as failed.
func TestRefreshStopsWhenTheCallerIsGone(t *testing.T) {
	rig := artifactRig(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	rig.gh.hook = func(ctx context.Context) error {
		cancel()
		return &app.GitHubError{Transport: true, Cause: ctx.Err()}
	}
	_, err := rig.svc.Refresh(ctx, "ws-1", "")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Refresh() error = %v, want context.Canceled", err)
	}
	for _, l := range rig.logs.snapshot(t) {
		t.Errorf("log line %v after the caller left", l)
	}
}

// Logs carry ids, codes and counts, never the cause of a GitHub error or artifact bytes.
func TestRefreshLogsNeverCarryTheCause(t *testing.T) {
	rig := artifactRig(t, nil)
	rig.gh.listRuns = func(app.RepoRef, string, int) ([]domain.WorkflowRun, error) {
		return nil, &app.GitHubError{Status: 500, Cause: errors.New("Bearer ghp_topsecret")}
	}
	refresh(t, rig)
	lines := rig.logs.snapshot(t)
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1: %v", len(lines), lines)
	}
	if text := fmt.Sprint(lines[0]); strings.Contains(text, "topsecret") || strings.Contains(text, "Bearer") {
		t.Errorf("log line %s carries the cause", text)
	}
}

func TestRefreshListInstallationsFailureIsInternal(t *testing.T) {
	rig := newIngestRig(t)
	boom := errors.New("connection refused")
	svc := app.NewIngestService(app.IngestDeps{
		Agents: fakeAgents{}, Store: failingStore{fakeStore: rig.store, err: boom}, GitHub: rig.gh, Zip: nopReader{},
	})
	_, err := svc.Refresh(t.Context(), "ws-1", "")
	if !errors.Is(err, boom) {
		t.Errorf("Refresh() error = %v, want it to wrap %v", err, boom)
	}
}

func TestListRuns(t *testing.T) {
	rig := newIngestRig(t)
	id := "run-1"
	rig.store.runs = []domain.Run{{ID: id}, {ID: "run-2"}}
	got, err := rig.svc.ListRuns(t.Context(), "ws-1", app.RunFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != id {
		t.Errorf("ListRuns() = %+v, want only run-1", got)
	}
}

type nopReader struct{}

func (nopReader) Entry([]byte, string, int64) ([]byte, error) { return nil, app.ErrEntryInvalid }

type failingStore struct {
	*fakeStore
	err error
}

func (f failingStore) ListInstallations(context.Context, string, app.InstallationFilter) ([]domain.Installation, error) {
	return nil, f.err
}
