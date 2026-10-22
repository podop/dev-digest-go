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

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// jobsEnv is an env plus the Jobs handlers writing their log lines to a buffer.
type jobsEnv struct {
	*env
	jobs  *app.Jobs
	queue *fakeQueue
	logs  *bytes.Buffer
}

func newJobsEnv(t *testing.T) *jobsEnv {
	t.Helper()
	e := newEnv(t)
	logs := &bytes.Buffer{}
	queue := &fakeQueue{}
	j := app.NewJobs(app.JobsDeps{Indexer: e.ix, Repos: e.store, Git: e.git, Queue: queue, Redact: maskKey{}},
		app.JobsOptions{Now: e.clock.Now, Log: slog.New(slog.NewJSONHandler(logs, nil))})
	return &jobsEnv{env: e, jobs: j, queue: queue, logs: logs}
}

func payloadOf(id string) json.RawMessage {
	return json.RawMessage(`{"repoId":"` + id + `","owner":"ignored","name":"ignored"}`)
}

// lines returns the decoded repo_intel_index log lines.
func (j *jobsEnv) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(j.logs.Bytes()), []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", l, err)
		}
		if m["msg"] == "repo_intel_index" {
			out = append(out, m)
		}
	}
	return out
}

func (j *jobsEnv) oneLine(t *testing.T) map[string]any {
	t.Helper()
	lines := j.lines(t)
	if len(lines) != 1 {
		t.Fatalf("repo_intel_index lines = %d, want exactly 1:\n%s", len(lines), j.logs)
	}
	return lines[0]
}

func TestJobs_EachAttemptWritesOneLogLine(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(j *jobsEnv)
		wantErr    bool
		wantStatus string
		wantReason string
		wantError  bool
	}{
		{name: "full", setup: func(j *jobsEnv) {}, wantStatus: "full"},
		{name: "degraded", setup: func(j *jobsEnv) {
			r := j.store.repos[repoID]
			r.ClonePath = ""
			j.store.repos[repoID] = r
		}, wantStatus: "degraded", wantReason: "no_clone"},
		{name: "failed", setup: func(j *jobsEnv) {
			j.store.hooks["InsertSymbols"] = func(context.Context) error { return errors.New("db down sk-secret") }
		}, wantErr: true, wantStatus: "failed", wantReason: "index_failed", wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := newJobsEnv(t)
			j.addFile("src/a.ts", "const topSecretSource = 1", domain.Symbol{Name: "a", Kind: "function", Line: 1, EndLine: 1, Signature: "function topSecretSignature()"})
			tc.setup(j)

			err := j.jobs.Index(t.Context(), payloadOf(repoID))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Index() error = %v, want error: %v", err, tc.wantErr)
			}
			line := j.oneLine(t)
			if line["repo_id"] != repoID || line["kind"] != "repo-intel-index" || line["status"] != tc.wantStatus {
				t.Errorf("log line = %v, want repo_id %s, kind repo-intel-index, status %s", line, repoID, tc.wantStatus)
			}
			if got, _ := line["reason"].(string); got != tc.wantReason {
				t.Errorf("log reason = %q, want %q", got, tc.wantReason)
			}
			for _, k := range []string{"files_indexed", "files_skipped", "duration_ms"} {
				if _, ok := line[k]; !ok {
					t.Errorf("log line has no %s: %v", k, line)
				}
			}
			errText, hasErr := line["error"].(string)
			if hasErr != tc.wantError || strings.Contains(errText, "sk-secret") {
				t.Errorf("log error = %q (present %v), want present %v and redacted", errText, hasErr, tc.wantError)
			}
			if out := j.logs.String(); strings.Contains(out, "topSecret") {
				t.Errorf("log carries source text or a signature:\n%s", out)
			}
		})
	}
}

func TestJobs_IndexReportsCountsInTheLine(t *testing.T) {
	j := newJobsEnv(t)
	j.addFile("src/a.ts", "x", fn("a", 1))
	j.addFile("src/b.ts", "x", fn("b", 1))
	j.clone.unreadable["src/b.ts"] = true
	if err := j.jobs.Index(t.Context(), payloadOf(repoID)); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	line := j.oneLine(t)
	if line["files_indexed"] != float64(1) || line["files_skipped"] != float64(1) || line["status"] != "partial" {
		t.Errorf("log line = %v, want partial, 1 indexed, 1 skipped", line)
	}
}

func TestJobs_RefreshRunsTheIncrementalIndex(t *testing.T) {
	j := newJobsEnv(t)
	j.addFile("src/a.ts", "x", fn("a", 1))
	j.seedIndex(domain.StatusFull)
	j.git.head = "old-sha"
	if err := j.jobs.Refresh(t.Context(), payloadOf(repoID)); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	line := j.oneLine(t)
	if line["kind"] != "repo-intel-refresh" || line["reason"] != "sha_unchanged" || line["status"] != "full" {
		t.Errorf("log line = %v, want repo-intel-refresh / sha_unchanged / full", line)
	}
}

func TestJobs_IgnoreTheFlagAndOtherPayloadFields(t *testing.T) {
	j := newJobsEnv(t)
	j.addFile("src/a.ts", "x", fn("a", 1))
	// The payload names another owner and name; the repo row decides.
	if err := j.jobs.Index(t.Context(), payloadOf(repoID)); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if st := j.state(t); st.Status != domain.StatusFull {
		t.Errorf("state = %+v, want full", st)
	}
}

func TestJobs_BadPayloadIsAnError(t *testing.T) {
	j := newJobsEnv(t)
	for name, raw := range map[string]string{"not json": `{`, "empty repo id": `{"repoId":""}`, "missing repo id": `{}`} {
		t.Run(name, func(t *testing.T) {
			for kind, h := range map[string]func(context.Context, json.RawMessage) error{
				"index": j.jobs.Index, "refresh": j.jobs.Refresh, "resync": j.jobs.Resync,
			} {
				if err := h(t.Context(), json.RawMessage(raw)); err == nil {
					t.Errorf("%s handler(%s) error = nil, want a decode error", kind, raw)
				}
			}
		})
	}
	j.noState(t)
}

func TestJobs_ResyncEndsWithoutWriting(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(j *jobsEnv)
		wantReason string
	}{
		{name: "repo does not exist", setup: func(j *jobsEnv) { delete(j.store.repos, repoID) }, wantReason: "no_clone"},
		{name: "repo not cloned", setup: func(j *jobsEnv) {
			r := j.store.repos[repoID]
			r.ClonePath = ""
			j.store.repos[repoID] = r
		}, wantReason: "no_clone"},
		{name: "sync fails", setup: func(j *jobsEnv) { j.git.syncErr = errors.New("auth failed sk-secret") }, wantReason: "sync_failed:auth failed [redacted]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := newJobsEnv(t)
			j.seedIndex(domain.StatusFull)
			tc.setup(j)
			before := j.store.states[repoID]
			if err := j.jobs.Resync(t.Context(), payloadOf(repoID)); err != nil {
				t.Fatalf("Resync() error = %v, want the job to end done", err)
			}
			if diff := cmp.Diff(before, j.store.states[repoID]); diff != "" {
				t.Errorf("state changed (-before +after):\n%s", diff)
			}
			if line := j.oneLine(t); line["reason"] != tc.wantReason || line["status"] != "degraded" {
				t.Errorf("log line = %v, want degraded / %q", line, tc.wantReason)
			}
		})
	}
}

func TestJobs_ResyncSyncsThenIndexesIncrementally(t *testing.T) {
	j := newJobsEnv(t)
	j.addFile("src/a.ts", "x", fn("a", 1))
	j.seedIndex(domain.StatusFull)
	j.git.branch = "trunk"
	j.git.diff = []string{"README.md"}
	if err := j.jobs.Resync(t.Context(), payloadOf(repoID)); err != nil {
		t.Fatalf("Resync() error = %v", err)
	}
	if diff := cmp.Diff([]string{"trunk"}, j.git.synced); diff != "" {
		t.Errorf("synced branches mismatch (-want +got):\n%s", diff)
	}
	if st := j.state(t); st.LastIndexedSHA != "head-sha" {
		t.Errorf("state.LastIndexedSHA = %q, want head-sha after the incremental index", st.LastIndexedSHA)
	}
	if line := j.oneLine(t); line["kind"] != "repo-intel-resync" || line["reason"] != "no_supported_changes" {
		t.Errorf("log line = %v, want repo-intel-resync / no_supported_changes", line)
	}
}

func TestJobs_ResyncFallsBackToTheStoredBranch(t *testing.T) {
	j := newJobsEnv(t)
	j.addFile("src/a.ts", "x", fn("a", 1))
	j.seedIndex(domain.StatusFull)
	j.git.branchErr = errors.New("remote unreachable")
	if err := j.jobs.Resync(t.Context(), payloadOf(repoID)); err != nil {
		t.Fatalf("Resync() error = %v", err)
	}
	if diff := cmp.Diff([]string{"main"}, j.git.synced); diff != "" {
		t.Errorf("synced branches mismatch (-want +got):\n%s", diff)
	}
}

func TestJobs_ResyncCancelledSyncIsAnError(t *testing.T) {
	j := newJobsEnv(t)
	j.seedIndex(domain.StatusFull)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	j.git.onSync = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	before := j.store.states[repoID]
	if err := j.jobs.Resync(ctx, payloadOf(repoID)); !errors.Is(err, context.Canceled) {
		t.Errorf("Resync() error = %v, want context.Canceled", err)
	}
	if diff := cmp.Diff(before, j.store.states[repoID]); diff != "" {
		t.Errorf("state changed (-before +after):\n%s", diff)
	}
}

func TestJobs_Triggers(t *testing.T) {
	j := newJobsEnv(t)
	if err := j.jobs.TriggerIndex(t.Context(), "ws-1", "repo-9", "acme", "widgets"); err != nil {
		t.Fatalf("TriggerIndex() error = %v", err)
	}
	if err := j.jobs.TriggerRefresh(t.Context(), "ws-1", "repo-9"); err != nil {
		t.Fatalf("TriggerRefresh() error = %v", err)
	}
	want := []queuedJob{
		{"ws-1", "repo-intel-index", `{"repoId":"repo-9","owner":"acme","name":"widgets"}`},
		{"ws-1", "repo-intel-refresh", `{"repoId":"repo-9"}`},
	}
	if diff := cmp.Diff(want, j.queue.jobs); diff != "" {
		t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
	}

	j.queue.err = errors.New("no handler")
	if err := j.jobs.TriggerIndex(t.Context(), "ws-1", "repo-9", "acme", "widgets"); err == nil {
		t.Error("TriggerIndex() error = nil, want the enqueue failure")
	}
	if err := j.jobs.TriggerRefresh(t.Context(), "ws-1", "repo-9"); !errors.Is(err, j.queue.err) {
		t.Errorf("TriggerRefresh() error = %v, want it to wrap the enqueue failure", err)
	}
}
