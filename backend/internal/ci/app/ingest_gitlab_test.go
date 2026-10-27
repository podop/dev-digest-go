package app_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/adapters/zipentry"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// This file tests the GitLab part of the refresh (spec C FR31-FR36, AC30-AC34 at use-case level)
// over hand-written fakes of the GitLab ports. Names start with gl/gi to stay clear of the other
// test files of the package.

const (
	giHost    = "gitlab.com"
	giBase    = "https://gitlab.com"
	giProject = int64(42)
	giSlug    = "reviewer"
)

var giNow = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)

// giConns is a GitLabIngestConns over a fixed answer per host.
type giConns struct {
	byHost map[string]error // host -> error; a host not listed resolves
	fail   error            // every Resolve fails with it
	asked  []string
}

func (f *giConns) Resolve(_ context.Context, host string) (app.GitLabIngestConn, error) {
	f.asked = append(f.asked, host)
	if f.fail != nil {
		return app.GitLabIngestConn{}, f.fail
	}
	if err, ok := f.byHost[host]; ok {
		return app.GitLabIngestConn{}, err
	}
	return app.GitLabIngestConn{BaseURL: giBase}, nil
}

// giFile is the scripted answer of JobArtifactFile.
type giFile struct {
	body []byte
	err  error
}

// giAPI is a GitLabCI that answers from tables and records every call by name.
type giAPI struct {
	mu        sync.Mutex
	calls     []string
	pipelines []domain.GitLabPipeline
	pipeErr   error
	jobs      map[int64][]domain.GitLabJob // by pipeline id
	jobsErr   error
	mrSource  map[int]int64
	mrErr     map[int]error
	files     map[int64]giFile // by job id
	// latency advances the clock at every call.
	onCall func()
}

func (f *giAPI) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
	if f.onCall != nil {
		f.onCall()
	}
}

func (f *giAPI) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *giAPI) Pipelines(_ context.Context, ref app.GitLabRef, source string, page, perPage int) ([]domain.GitLabPipeline, error) {
	f.record(fmt.Sprintf("pipelines %s %d %s %d %d", ref.Host, ref.ProjectID, source, page, perPage))
	return f.pipelines, f.pipeErr
}

func (f *giAPI) PipelineJobs(_ context.Context, ref app.GitLabRef, pipelineID int64, scopes ...string) ([]domain.GitLabJob, error) {
	f.record(fmt.Sprintf("jobs %s %d %d %s", ref.Host, ref.ProjectID, pipelineID, strings.Join(scopes, ",")))
	return f.jobs[pipelineID], f.jobsErr
}

func (f *giAPI) MergeRequestSourceProject(_ context.Context, ref app.GitLabRef, iid int) (int64, error) {
	f.record(fmt.Sprintf("mr %s %d %d", ref.Host, ref.ProjectID, iid))
	if err := f.mrErr[iid]; err != nil {
		return 0, err
	}
	return f.mrSource[iid], nil
}

func (f *giAPI) JobArtifactFile(_ context.Context, ref app.GitLabRef, jobID int64, file string, limit int) ([]byte, error) {
	f.record(fmt.Sprintf("artifact %s %d %d %s %d", ref.Host, ref.ProjectID, jobID, file, limit))
	a, ok := f.files[jobID]
	if !ok {
		return nil, &app.GitLabError{Status: 404}
	}
	return a.body, a.err
}

// giStore is a GitLabIngestStore keyed by (host, job id, attempt).
type giStore struct {
	installs  []domain.Installation
	listErr   error
	keysErr   error
	recordErr error
	// stored is every stored run in order; first is a run another refresh stores first.
	stored    map[string]domain.RunKey
	runs      []domain.GitLabNewRun
	raced     map[int64]bool
	keysCalls []string
	listed    []app.InstallationFilter
	provider  string
}

func giKey(host string, k domain.RunKey) string {
	return fmt.Sprintf("%s|%d|%d", host, k.GithubRunID, k.Attempt)
}

func (f *giStore) ListGitLabInstallations(_ context.Context, _ string, flt app.InstallationFilter) ([]domain.Installation, error) {
	f.listed = append(f.listed, flt)
	return f.installs, f.listErr
}

func (f *giStore) StoredGitLabRunKeys(_ context.Context, host string, keys []domain.RunKey) (map[domain.RunKey]bool, error) {
	if f.keysErr != nil {
		return nil, f.keysErr
	}
	out := map[domain.RunKey]bool{}
	for _, k := range keys {
		f.keysCalls = append(f.keysCalls, giKey(host, k))
		if _, ok := f.stored[giKey(host, k)]; ok {
			out[k] = true
		}
	}
	return out, nil
}

func (f *giStore) RecordGitLabRun(_ context.Context, run domain.GitLabNewRun) (app.Recorded, error) {
	if f.recordErr != nil {
		return app.Recorded{}, f.recordErr
	}
	k := domain.RunKey{GithubRunID: run.GithubRunID, Attempt: run.RunAttempt}
	if f.raced[run.GithubRunID] || f.stored[giKey(run.Host, k)] == k {
		return app.Recorded{}, nil
	}
	if f.stored == nil {
		f.stored = map[string]domain.RunKey{}
	}
	f.stored[giKey(run.Host, k)] = k
	f.runs = append(f.runs, run)
	return app.Recorded{AgentRunID: fmt.Sprintf("agent-run-%d", run.GithubRunID), Stored: true, Provider: f.provider}, nil
}

// giLedger records the GitLab ledger entries; err fails every append.
type giLedger struct {
	runs []app.GitLabCIRun
	err  error
}

func (f *giLedger) RecordGitLabCIRun(_ context.Context, run app.GitLabCIRun) error {
	if f.err != nil {
		return f.err
	}
	f.runs = append(f.runs, run)
	return nil
}

// giTx undoes the store and ledger writes of a failed fn, like a database transaction.
type giTx struct {
	store  *giStore
	ledger *giLedger
	ran    int
}

func (f *giTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	f.ran++
	stored := make(map[string]domain.RunKey, len(f.store.stored))
	for k, v := range f.store.stored {
		stored[k] = v
	}
	runs, entries := slices.Clone(f.store.runs), slices.Clone(f.ledger.runs)
	err := fn(ctx)
	if err != nil {
		f.store.stored, f.store.runs, f.ledger.runs = stored, runs, entries
	}
	return err
}

// giRig is one Ingest service with the GitLab ports over fakes.
type giRig struct {
	svc    *app.IngestService
	conns  *giConns
	api    *giAPI
	store  *giStore
	ledger *giLedger
	tx     *giTx
	gh     *fakeGitHub
	ghSt   *fakeStore
	logs   *logCapture
	clock  *testClock
}

func giInstall(id string, at time.Time) domain.Installation {
	slug := giSlug
	return domain.Installation{
		ID: id, AgentID: "agent-1", Repo: "acme/web", TargetType: domain.TargetGitLabCI,
		InstalledAt: at, Slug: &slug, Provider: "anthropic", Host: giHost, ProjectID: giProject,
	}
}

// newGIRig builds a rig with one gitlab_ci installation, no GitHub token and the ledger on.
func newGIRig(t testing.TB) *giRig {
	t.Helper()
	clock := &testClock{now: giNow}
	conns := &giConns{}
	api := &giAPI{jobs: map[int64][]domain.GitLabJob{}, mrSource: map[int]int64{}, mrErr: map[int]error{}, files: map[int64]giFile{}}
	store := &giStore{installs: []domain.Installation{giInstall("inst-gl", giNow.Add(-time.Hour))}}
	ledger := &giLedger{}
	tx := &giTx{store: store, ledger: ledger}
	ghSt := &fakeStore{calls: &calls{}}
	gh := &fakeGitHub{calls: &calls{}, configured: false}
	logs := newLogCapture()
	svc := app.NewIngestService(app.IngestDeps{
		Agents: fakeAgents{agents: map[string]domain.ExportAgent{"agent-1": {ID: "agent-1"}}},
		Store:  ghSt, GitHub: gh, Zip: zipentry.New(), Tx: tx, Clock: clock.Now, Log: logs.logger(),
		GitLab: app.GitLabIngestDeps{Conns: conns, CI: api, Store: store, Ledger: ledger},
	})
	return &giRig{svc: svc, conns: conns, api: api, store: store, ledger: ledger, tx: tx, gh: gh, ghSt: ghSt, logs: logs, clock: clock}
}

func (r *giRig) refresh(t testing.TB) domain.RefreshResult {
	t.Helper()
	out, err := r.svc.Refresh(t.Context(), "ws-1", "")
	if err != nil {
		t.Fatalf("Refresh() error = %v, want nil", err)
	}
	return out
}

func (r *giRig) lines() []logLine {
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	return slices.Clone(*r.logs.lines)
}

// giMRPipeline is a merge request pipeline of the installation's project; its job list is set up
// by the caller.
func giMRPipeline(id int64, iid int) domain.GitLabPipeline {
	return domain.GitLabPipeline{
		ID: id, ProjectID: giProject, SHA: fmt.Sprintf("%040d", id), Ref: fmt.Sprintf("refs/merge-requests/%d/head", iid),
		Source: domain.GitLabMergeRequestSource,
	}
}

func giJob(id int64, status string) domain.GitLabJob {
	return domain.GitLabJob{
		ID: id, Name: domain.GitLabJobName(giSlug), Status: status, CreatedAt: "2026-03-02T10:00:00Z",
		ArtifactsFile: &domain.GitLabArtifactsFile{Size: 500},
	}
}

// giResult is the result file the runner writes for a job of a pipeline.
func giResult(p domain.GitLabPipeline, jobID int64, iid int) string {
	return fmt.Sprintf(`{"findings_count":3,"agent":"Reviewer","repository":"acme/web","repository_id":%d,"run_id":%d,`+
		`"run_attempt":1,"commit_sha":%q,"pr_number":%d,"model":"claude-x","cost_usd":0.25,"blockers":1,"duration_ms":900}`,
		giProject, jobID, p.SHA, iid)
}

// giOne sets up one MR pipeline (id 100, iid 7) with one job (id 5001) whose MR comes from the project.
func giOne(r *giRig, mutate func(*domain.GitLabJob)) (domain.GitLabPipeline, domain.GitLabJob) {
	p := giMRPipeline(100, 7)
	j := giJob(5001, "success")
	if mutate != nil {
		mutate(&j)
	}
	r.api.pipelines = []domain.GitLabPipeline{p}
	r.api.jobs[p.ID] = []domain.GitLabJob{j}
	r.api.mrSource[7] = giProject
	return p, j
}

func wantSkips(t *testing.T, res domain.InstallationResult, want ...domain.SkippedRun) {
	t.Helper()
	if want == nil {
		want = []domain.SkippedRun{}
	}
	if diff := cmp.Diff(want, res.Skipped); diff != "" {
		t.Errorf("Skipped mismatch (-want +got):\n%s", diff)
	}
}

// TestGitLabRefresh_StoresMergeRequestJobs covers AC31 (FR32, FR36, EC27) at use-case level: of 3
// MR pipelines (job success, job failed with artifact, job canceled), a branch pipeline and a job of
// another name, two runs are stored with the GitLab values; a second refresh stores nothing.
func TestGitLabRefresh_StoresMergeRequestJobs(t *testing.T) {
	r := newGIRig(t)
	p1, p2, p3 := giMRPipeline(100, 7), giMRPipeline(101, 8), giMRPipeline(102, 9)
	branch := domain.GitLabPipeline{ID: 103, ProjectID: giProject, SHA: strings.Repeat("c", 40), Ref: "refs/heads/main", Source: "push"}
	otherProject := giMRPipeline(104, 11)
	otherProject.ProjectID = 7
	// the success job has no artifact (FR34: a failed run); the failed job has a verified one
	noArtifact := giJob(5001, "success")
	noArtifact.ArtifactsFile = nil
	other := giJob(5099, "success")
	other.Name = "build"
	r.api.pipelines = []domain.GitLabPipeline{p1, p2, p3, branch, otherProject}
	r.api.jobs[p1.ID] = []domain.GitLabJob{other, noArtifact}
	r.api.jobs[p2.ID] = []domain.GitLabJob{giJob(5002, "failed")}
	r.api.jobs[p3.ID] = []domain.GitLabJob{giJob(5003, "canceled")} // never listed by the scope, but a fake may
	r.api.files[5002] = giFile{body: []byte(giResult(p2, 5002, 8))}
	r.api.mrSource[7], r.api.mrSource[8], r.api.mrSource[9] = giProject, giProject, giProject

	out := r.refresh(t)

	if out.Ingested != 2 || out.Degraded {
		t.Fatalf("Refresh() = ingested %d degraded %v, want 2 false", out.Ingested, out.Degraded)
	}
	if len(out.Installations) != 1 || out.Installations[0].State != domain.StateOK {
		t.Fatalf("Refresh().Installations = %+v, want one ok", out.Installations)
	}
	wantSkips(t, out.Installations[0])
	if len(r.store.runs) != 2 {
		t.Fatalf("stored %d runs, want 2", len(r.store.runs))
	}
	failed, ok := r.store.runs[0], r.store.runs[1]
	for _, run := range r.store.runs {
		if run.Host != giHost || run.Source != domain.RunSourceGitLab || run.RunAttempt != 1 || run.InstallationID != "inst-gl" {
			t.Errorf("run %d = host %q source %q attempt %d installation %q, want %s gitlab_ci 1 inst-gl",
				run.GithubRunID, run.Host, run.Source, run.RunAttempt, run.InstallationID, giHost)
		}
	}
	if failed.GithubRunID != 5001 || failed.Status != domain.RunFailed || failed.HeadSHA != p1.SHA || failed.FromArtifact {
		t.Errorf("run without artifact = %+v, want failed job 5001 at the pipeline sha", failed.NewRun)
	}
	if want := giBase + "/acme/web/-/jobs/5001"; failed.GithubURL != want {
		t.Errorf("GithubURL = %q, want %q", failed.GithubURL, want)
	}
	if failed.PRNumber == nil || *failed.PRNumber != 7 {
		t.Errorf("failed run PRNumber = %v, want the iid 7", failed.PRNumber)
	}
	if ok.GithubRunID != 5002 || ok.Status != domain.RunSucceeded || !ok.FromArtifact || ok.FindingsCount != 3 || ok.Blockers != 1 {
		t.Errorf("run with artifact = %+v, want succeeded job 5002 with 3 findings 1 blocker", ok.NewRun)
	}

	// only the run of an artifact reaches the ledger, with the host key (SCR1) and the provider
	// of the installation
	if len(r.ledger.runs) != 1 {
		t.Fatalf("ledger entries = %+v, want 1", r.ledger.runs)
	}
	e := r.ledger.runs[0]
	if e.Host != giHost || e.GithubRunID != 5002 || e.Attempt != 1 || e.Provider != "anthropic" || e.AgentRunID != "agent-run-5002" {
		t.Errorf("ledger entry = %+v, want host %s job 5002 attempt 1 provider anthropic", e, giHost)
	}
	if e.CostUSD == nil || *e.CostUSD != 0.25 || e.Model == nil || *e.Model != "claude-x" {
		t.Errorf("ledger entry cost/model = %v/%v, want 0.25/claude-x", e.CostUSD, e.Model)
	}

	// requests: the MR pipelines list once, jobs for the 3 MR pipelines of the project only
	if got := r.api.count("pipelines " + giHost + " 42 merge_request_event 1 30"); got != 1 {
		t.Errorf("pipeline list calls = %d, want 1 (calls %v)", got, r.api.calls)
	}
	if got := r.api.count("jobs "); got != 3 {
		t.Errorf("job list calls = %d, want 3 (calls %v)", got, r.api.calls)
	}
	if !slices.Contains(r.api.calls, "jobs gitlab.com 42 100 success,failed") {
		t.Errorf("job list is not asked with the scopes success,failed: %v", r.api.calls)
	}

	// a second refresh stores nothing new and reads no merge request or artifact
	before := len(r.api.calls)
	again := r.refresh(t)
	if again.Ingested != 0 || len(r.store.runs) != 2 || len(r.ledger.runs) != 1 {
		t.Errorf("second Refresh() = ingested %d, runs %d, ledger %d, want 0 2 1", again.Ingested, len(r.store.runs), len(r.ledger.runs))
	}
	for _, c := range r.api.calls[before:] {
		if strings.HasPrefix(c, "mr ") || strings.HasPrefix(c, "artifact ") {
			t.Errorf("second refresh made call %q for a stored job", c)
		}
	}
}

// TestGitLabRefresh_NoGitHubToken covers AC30 / EC32 (FR31): GitLab installations alone need no
// GitHub token, a GitHub installation in scope does; nothing at all keeps the old answer.
func TestGitLabRefresh_NoGitHubToken(t *testing.T) {
	ghSlug := "reviewer"
	ghInstall := domain.Installation{ID: "inst-gh", AgentID: "agent-1", Repo: "acme/api", TargetType: domain.TargetGHA, InstalledAt: giNow, Slug: &ghSlug}
	tests := []struct {
		name       string
		gitlab     bool
		github     []domain.Installation
		wantConfig bool
	}{
		{name: "only gitlab", gitlab: true},
		{name: "gitlab and github", gitlab: true, github: []domain.Installation{ghInstall}, wantConfig: true},
		{name: "github without slug does not count", gitlab: true, github: []domain.Installation{{ID: "old", Repo: "acme/old", TargetType: domain.TargetGHA}}},
		{name: "nothing to walk", wantConfig: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newGIRig(t)
			if !tc.gitlab {
				r.store.installs = nil
			}
			r.ghSt.installs = tc.github
			out, err := r.svc.Refresh(t.Context(), "ws-1", "")
			if tc.wantConfig {
				var ae *apperr.Error
				if !errors.As(err, &ae) || ae.Kind != apperr.KindConfig {
					t.Fatalf("Refresh() error = %v, want a config error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Refresh() error = %v, want nil", err)
			}
			if len(out.Installations) != 1 || out.Installations[0].State != domain.StateOK {
				t.Errorf("Refresh().Installations = %+v, want the gitlab one ok", out.Installations)
			}
		})
	}
}

// TestGitLabRefresh_WithoutGitLabDeps: the zero GitLabIngestDeps keeps the GitHub refresh as it was,
// including the config error before any store call.
func TestGitLabRefresh_WithoutGitLabDeps(t *testing.T) {
	rig := newIngestRig(t)
	rig.gh.configured = false
	_, err := rig.svc.Refresh(t.Context(), "ws-1", "")
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindConfig {
		t.Fatalf("Refresh() error = %v, want a config error", err)
	}
	if got := rig.store.calls.snapshot(t); len(got) != 0 {
		t.Errorf("store calls = %v, want none before the token check", got)
	}
}

// TestGitLabRefresh_GitLabRowInGitHubListIsNotWalkedAsGitHub: a GitLab row a store lists among the
// GitHub ones is walked once, by the GitLab path.
func TestGitLabRefresh_GitLabRowInGitHubListIsNotWalkedAsGitHub(t *testing.T) {
	r := newGIRig(t)
	r.gh.configured = true
	r.ghSt.installs = []domain.Installation{r.store.installs[0]}
	out := r.refresh(t)
	if len(out.Installations) != 1 {
		t.Fatalf("Refresh().Installations = %+v, want one", out.Installations)
	}
}

// TestGitLabRefresh_ConnectionProblems covers FR31: a host without connection or token is an error
// of that installation with no GitLab request; the other installations are still ingested.
func TestGitLabRefresh_ConnectionProblems(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want domain.ErrorCode
	}{
		{name: "not connected", err: app.ErrGitLabHostNotConnected, want: domain.CodeRepoHostNotConnected},
		{name: "no token", err: fmt.Errorf("wrapped: %w", app.ErrGitLabTokenMissing), want: domain.CodeGitLabTokenMissing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newGIRig(t)
			other := giInstall("inst-ok", giNow.Add(-2*time.Hour))
			other.Host = "git.corp.example"
			r.store.installs = []domain.Installation{giInstall("inst-bad", giNow.Add(-time.Hour)), other}
			r.conns.byHost = map[string]error{giHost: tc.err}
			giOne(r, nil)

			out := r.refresh(t)

			bad, ok := out.Installations[0], out.Installations[1]
			if bad.State != domain.StateError || bad.ErrorCode != tc.want {
				t.Errorf("first installation = %s %s, want error %s", bad.State, bad.ErrorCode, tc.want)
			}
			if ok.State != domain.StateOK || !out.Degraded {
				t.Errorf("second installation = %s degraded %v, want ok and degraded", ok.State, out.Degraded)
			}
			for _, c := range r.api.calls {
				if strings.Contains(c, giHost) {
					t.Errorf("request for the unusable host: %q", c)
				}
			}
			if !slices.Contains(r.api.calls, "pipelines git.corp.example 42 merge_request_event 1 30") {
				t.Errorf("the other host was not walked: %v", r.api.calls)
			}
		})
	}
}

// TestGitLabRefresh_ConnectionReadFailureAbortsRefresh: a failure to read the connections is not an
// error code of an installation.
func TestGitLabRefresh_ConnectionReadFailureAbortsRefresh(t *testing.T) {
	r := newGIRig(t)
	r.conns.fail = errors.New("db down")
	if _, err := r.svc.Refresh(t.Context(), "ws-1", ""); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("Refresh() error = %v, want the connection read failure", err)
	}
}

// TestGitLabRefresh_ErrorCodes covers FR32 / AC31: the status of a failed call is the code of the
// installation; 404 of the pipeline list is not_installed; one error log line with code and status.
func TestGitLabRefresh_ErrorCodes(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantState domain.RefreshState
		wantCode  domain.ErrorCode
		wantLog   bool
	}{
		{name: "429", err: &app.GitLabError{Status: 429}, wantState: domain.StateError, wantCode: domain.CodeGitLabRateLimited, wantLog: true},
		{name: "403", err: &app.GitLabError{Status: 403}, wantState: domain.StateError, wantCode: domain.CodeGitLabPermissionDenied, wantLog: true},
		{name: "401", err: &app.GitLabError{Status: 401}, wantState: domain.StateError, wantCode: domain.CodeGitLabPermissionDenied, wantLog: true},
		{name: "500", err: &app.GitLabError{Status: 500}, wantState: domain.StateError, wantCode: domain.CodeGitLabUnavailable, wantLog: true},
		{name: "transport", err: &app.GitLabError{Transport: true}, wantState: domain.StateError, wantCode: domain.CodeGitLabUnavailable, wantLog: true},
		{name: "plain error", err: errors.New("boom"), wantState: domain.StateError, wantCode: domain.CodeGitLabUnavailable, wantLog: true},
		{name: "404", err: &app.GitLabError{Status: 404}, wantState: domain.StateNotInstalled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newGIRig(t)
			r.api.pipeErr = tc.err
			out := r.refresh(t)
			res := out.Installations[0]
			if res.State != tc.wantState || res.ErrorCode != tc.wantCode {
				t.Errorf("Refresh() installation = %s %q, want %s %q", res.State, res.ErrorCode, tc.wantState, tc.wantCode)
			}
			if out.Degraded != (tc.wantState == domain.StateError) {
				t.Errorf("Degraded = %v for state %s", out.Degraded, tc.wantState)
			}
			var errs []logLine
			for _, l := range r.lines() {
				if l.Level == "error" {
					errs = append(errs, l)
				}
			}
			if !tc.wantLog {
				if len(errs) != 0 {
					t.Errorf("error log lines = %v, want none", errs)
				}
				return
			}
			if len(errs) != 1 || errs[0].Msg != "ci refresh: installation failed" ||
				errs[0].Obj["code"] != string(tc.wantCode) || errs[0].Obj["installation_id"] != "inst-gl" || errs[0].Obj["repo"] != "acme/web" {
				t.Fatalf("error log = %+v, want one installation failed line with code %s", errs, tc.wantCode)
			}
			if ce, ok := tc.err.(*app.GitLabError); ok && ce.Status != 0 && errs[0].Obj["status"] != int64(ce.Status) {
				t.Errorf("log status = %v, want %d", errs[0].Obj["status"], ce.Status)
			}
		})
	}
}

// TestGitLabRefresh_ErrorOfLaterCall: a failing job list, merge request read or artifact fetch is
// an error of the installation with the code of its status.
func TestGitLabRefresh_ErrorOfLaterCall(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*giRig)
		want  domain.ErrorCode
	}{
		{name: "jobs 403", setup: func(r *giRig) { r.api.jobsErr = &app.GitLabError{Status: 403} }, want: domain.CodeGitLabPermissionDenied},
		{name: "merge request 429", setup: func(r *giRig) { r.api.mrErr[7] = &app.GitLabError{Status: 429} }, want: domain.CodeGitLabRateLimited},
		{name: "artifact 500", setup: func(r *giRig) { r.api.files[5001] = giFile{err: &app.GitLabError{Status: 500}} }, want: domain.CodeGitLabUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newGIRig(t)
			giOne(r, nil)
			tc.setup(r)
			res := r.refresh(t).Installations[0]
			if res.State != domain.StateError || res.ErrorCode != tc.want {
				t.Errorf("installation = %s %q, want error %q", res.State, res.ErrorCode, tc.want)
			}
			if len(r.store.runs) != 0 {
				t.Errorf("stored %d runs, want none", len(r.store.runs))
			}
		})
	}
}

// TestGitLabRefresh_ForkSkip covers AC32 / EC31 (FR33): a merge request from another project (or
// with none known) is never inspected: no artifact request, nothing stored, not in skipped, one
// WARN; the merge request is read once per refresh.
func TestGitLabRefresh_ForkSkip(t *testing.T) {
	tests := []struct {
		name   string
		source int64
		err    error
	}{
		{name: "fork", source: 999},
		{name: "no source project", source: 0},
		{name: "merge request gone", err: &app.GitLabError{Status: 404}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newGIRig(t)
			// two pipelines of the same merge request: its source is read once (A7)
			p1, p2 := giMRPipeline(100, 7), giMRPipeline(101, 7)
			r.api.pipelines = []domain.GitLabPipeline{p2, p1}
			r.api.jobs[p1.ID] = []domain.GitLabJob{giJob(5001, "success")}
			r.api.jobs[p2.ID] = []domain.GitLabJob{giJob(5002, "success")}
			r.api.mrSource[7] = tc.source
			if tc.err != nil {
				r.api.mrErr[7] = tc.err
			}

			out := r.refresh(t)

			res := out.Installations[0]
			if res.State != domain.StateOK || res.Ingested != 0 || out.Degraded {
				t.Errorf("installation = %s ingested %d degraded %v, want ok 0 false", res.State, res.Ingested, out.Degraded)
			}
			wantSkips(t, res)
			if len(r.store.runs) != 0 || r.api.count("artifact ") != 0 {
				t.Errorf("stored %d runs, %d artifact requests, want none", len(r.store.runs), r.api.count("artifact "))
			}
			if got := r.api.count("mr "); got != 1 {
				t.Errorf("merge request reads = %d, want 1 per refresh (calls %v)", got, r.api.calls)
			}
			var warns []logLine
			for _, l := range r.lines() {
				if l.Level == "warn" {
					warns = append(warns, l)
				}
			}
			if len(warns) != 2 {
				t.Fatalf("warn lines = %+v, want 2 (one per job)", warns)
			}
			w := warns[0]
			if w.Msg != "ci refresh: run not inspected" || w.Obj["reason"] != "source_project_differs" ||
				w.Obj["installation_id"] != "inst-gl" || w.Obj["repo"] != "acme/web" || w.Obj["github_run_id"] != int64(5002) {
				t.Errorf("warn line = %+v, want run not inspected for job 5002 with source_project_differs", w)
			}
		})
	}
}

// TestGitLabRefresh_MergeRequestCacheIsPerRefresh: the next refresh reads the merge request again.
func TestGitLabRefresh_MergeRequestCacheIsPerRefresh(t *testing.T) {
	r := newGIRig(t)
	giOne(r, nil)
	r.api.mrSource[7] = 999
	r.refresh(t)
	r.refresh(t)
	if got := r.api.count("mr "); got != 2 {
		t.Errorf("merge request reads over two refreshes = %d, want 2", got)
	}
}

// TestGitLabRefresh_ArtifactOutcomes covers AC33 / EC29 (FR34).
func TestGitLabRefresh_ArtifactOutcomes(t *testing.T) {
	past, future := "2026-03-01T00:00:00Z", "2026-04-01T00:00:00Z"
	tests := []struct {
		name         string
		job          func(*domain.GitLabJob)
		file         *giFile
		wantStored   bool
		wantStatus   string
		wantSkip     domain.SkipReason
		wantDownload bool
		wantState    domain.RefreshState
		wantCode     domain.ErrorCode
	}{
		{name: "no artifact", job: func(j *domain.GitLabJob) { j.ArtifactsFile = nil }, wantStored: true, wantStatus: domain.RunFailed},
		{name: "expired", job: func(j *domain.GitLabJob) { j.ArtifactsExpireAt = past }, wantSkip: domain.SkipExpired},
		{name: "unparsable expiry", job: func(j *domain.GitLabJob) { j.ArtifactsExpireAt = "soon" }, wantSkip: domain.SkipExpired},
		{name: "archive over 1 MiB", job: func(j *domain.GitLabJob) { j.ArtifactsFile.Size = 2 << 20 }, wantSkip: domain.SkipTooLarge},
		{name: "result over 64 KiB", job: func(j *domain.GitLabJob) { j.ArtifactsExpireAt = future },
			file: &giFile{body: []byte(strings.Repeat("x", domain.MaxArtifactJSONBytes+1))}, wantSkip: domain.SkipTooLarge, wantDownload: true},
		{name: "result missing", file: &giFile{err: &app.GitLabError{Status: 404}}, wantSkip: domain.SkipInvalid, wantDownload: true},
		{name: "refused redirect", file: &giFile{err: &app.GitLabError{}}, wantState: domain.StateError, wantCode: domain.CodeGitLabUnavailable, wantDownload: true},
		{name: "artifact forbidden", file: &giFile{err: &app.GitLabError{Status: 403}}, wantState: domain.StateError, wantCode: domain.CodeGitLabPermissionDenied, wantDownload: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newGIRig(t)
			p, _ := giOne(r, tc.job)
			if tc.file != nil {
				r.api.files[5001] = *tc.file
			} else {
				r.api.files[5001] = giFile{body: []byte(giResult(p, 5001, 7))}
			}
			out := r.refresh(t)
			res := out.Installations[0]
			wantState := cmpState(tc.wantState)
			if res.State != wantState || res.ErrorCode != tc.wantCode {
				t.Fatalf("installation = %s %q, want %s %q", res.State, res.ErrorCode, wantState, tc.wantCode)
			}
			if got := r.api.count("artifact ") == 1; got != tc.wantDownload {
				t.Errorf("artifact downloaded = %v, want %v (calls %v)", got, tc.wantDownload, r.api.calls)
			}
			if tc.wantDownload {
				want := fmt.Sprintf("artifact gitlab.com 42 5001 devdigest-result.json %d", domain.GitLabResultReadCap)
				if !slices.Contains(r.api.calls, want) {
					t.Errorf("artifact request = %v, want %q", r.api.calls, want)
				}
			}
			if tc.wantStored {
				if len(r.store.runs) != 1 || res.Ingested != 1 || r.store.runs[0].Status != tc.wantStatus {
					t.Errorf("stored = %+v ingested %d, want one %s run", r.store.runs, res.Ingested, tc.wantStatus)
				}
				if len(r.ledger.runs) != 0 {
					t.Errorf("ledger entries = %v, want none for a run without artifact", r.ledger.runs)
				}
				return
			}
			if len(r.store.runs) != 0 {
				t.Errorf("stored %d runs, want none", len(r.store.runs))
			}
			if tc.wantSkip != "" {
				wantSkips(t, res, domain.SkippedRun{GithubRunID: 5001, Reason: tc.wantSkip})
				for _, l := range r.lines() {
					if l.Level == "warn" && l.Msg == "ci refresh: run skipped" && l.Obj["reason"] != string(tc.wantSkip) {
						t.Errorf("skip line reason = %v, want %s", l.Obj["reason"], tc.wantSkip)
					}
				}
			}
		})
	}
}

func cmpState(s domain.RefreshState) domain.RefreshState {
	if s == "" {
		return domain.StateOK
	}
	return s
}

// TestGitLabRefresh_VerifyArtifact covers AC34 / EC28 (FR35).
func TestGitLabRefresh_VerifyArtifact(t *testing.T) {
	tests := []struct {
		name       string
		body       func(p domain.GitLabPipeline) string
		wantSkip   domain.SkipReason
		wantStored bool
	}{
		{name: "other repository id", body: func(p domain.GitLabPipeline) string {
			return strings.Replace(giResult(p, 5001, 7), `"repository_id":42`, `"repository_id":43`, 1)
		}, wantSkip: domain.SkipMismatch},
		{name: "other run id", body: func(p domain.GitLabPipeline) string { return giResult(p, 5002, 7) }, wantSkip: domain.SkipMismatch},
		{name: "other commit", body: func(p domain.GitLabPipeline) string {
			return strings.Replace(giResult(p, 5001, 7), p.SHA, strings.Repeat("e", 40), 1)
		}, wantSkip: domain.SkipMismatch},
		{name: "missing run id", body: func(p domain.GitLabPipeline) string {
			return strings.Replace(giResult(p, 5001, 7), `"run_id":5001,`, "", 1)
		}, wantSkip: domain.SkipMismatch},
		{name: "negative count", body: func(p domain.GitLabPipeline) string {
			return strings.Replace(giResult(p, 5001, 7), `"findings_count":3`, `"findings_count":-1`, 1)
		}, wantSkip: domain.SkipInvalid},
		{name: "count past int32", body: func(p domain.GitLabPipeline) string {
			return strings.Replace(giResult(p, 5001, 7), `"findings_count":3`, `"findings_count":4294967296`, 1)
		}, wantSkip: domain.SkipInvalid},
		{name: "NUL in the model", body: func(p domain.GitLabPipeline) string {
			return strings.Replace(giResult(p, 5001, 7), `claude-x`, `claude\u0000x`, 1)
		}, wantSkip: domain.SkipInvalid},
		{name: "invalid json", body: func(domain.GitLabPipeline) string { return `{"findings_count":` }, wantSkip: domain.SkipInvalid},
		{name: "repository path differs", body: func(p domain.GitLabPipeline) string {
			return strings.Replace(giResult(p, 5001, 7), `"acme/web"`, `"renamed/elsewhere"`, 1)
		}, wantStored: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newGIRig(t)
			p, _ := giOne(r, nil)
			r.api.files[5001] = giFile{body: []byte(tc.body(p))}
			res := r.refresh(t).Installations[0]
			if tc.wantStored {
				if res.Ingested != 1 || len(r.store.runs) != 1 {
					t.Errorf("ingested %d, stored %d, want 1 1", res.Ingested, len(r.store.runs))
				}
				return
			}
			wantSkips(t, res, domain.SkippedRun{GithubRunID: 5001, Reason: tc.wantSkip})
			if len(r.store.runs) != 0 || len(r.ledger.runs) != 0 {
				t.Errorf("stored %d runs, %d ledger entries, want none", len(r.store.runs), len(r.ledger.runs))
			}
		})
	}
}

// TestGitLabRefresh_StoreOutcomes covers what the store and the ledger can answer (FR36, FR39).
func TestGitLabRefresh_StoreOutcomes(t *testing.T) {
	t.Run("another refresh stored it first", func(t *testing.T) {
		r := newGIRig(t)
		p, _ := giOne(r, nil)
		r.api.files[5001] = giFile{body: []byte(giResult(p, 5001, 7))}
		r.store.raced = map[int64]bool{5001: true}
		out := r.refresh(t)
		if out.Ingested != 0 || len(r.ledger.runs) != 0 {
			t.Errorf("ingested %d, ledger %d, want 0 0", out.Ingested, len(r.ledger.runs))
		}
	})
	t.Run("ledger failure rolls the run back", func(t *testing.T) {
		r := newGIRig(t)
		p, _ := giOne(r, nil)
		r.api.files[5001] = giFile{body: []byte(giResult(p, 5001, 7))}
		r.ledger.err = errors.New("ledger down")
		if _, err := r.svc.Refresh(t.Context(), "ws-1", ""); err == nil || !strings.Contains(err.Error(), "ledger down") {
			t.Fatalf("Refresh() error = %v, want the ledger failure", err)
		}
		if len(r.store.runs) != 0 || len(r.store.stored) != 0 {
			t.Errorf("stored %d runs after the ledger failed, want none", len(r.store.runs))
		}
		r.ledger.err = nil
		if out := r.refresh(t); out.Ingested != 1 {
			t.Errorf("next Refresh() ingested %d, want the retry to store it", out.Ingested)
		}
	})
	t.Run("not storable", func(t *testing.T) {
		r := newGIRig(t)
		p, _ := giOne(r, nil)
		r.api.files[5001] = giFile{body: []byte(giResult(p, 5001, 7))}
		r.store.recordErr = app.ErrNotStorable
		res := r.refresh(t).Installations[0]
		wantSkips(t, res, domain.SkippedRun{GithubRunID: 5001, Reason: domain.SkipInvalid})
	})
	t.Run("store failure aborts", func(t *testing.T) {
		for _, name := range []string{"keys", "record", "list"} {
			r := newGIRig(t)
			p, _ := giOne(r, nil)
			r.api.files[5001] = giFile{body: []byte(giResult(p, 5001, 7))}
			boom := errors.New("db boom")
			switch name {
			case "keys":
				r.store.keysErr = boom
			case "record":
				r.store.recordErr = boom
			case "list":
				r.store.listErr = boom
			}
			if _, err := r.svc.Refresh(t.Context(), "ws-1", ""); !errors.Is(err, boom) {
				t.Errorf("%s: Refresh() error = %v, want the store failure", name, err)
			}
		}
	})
	t.Run("same job id on another host", func(t *testing.T) {
		r := newGIRig(t)
		p, _ := giOne(r, nil)
		r.api.files[5001] = giFile{body: []byte(giResult(p, 5001, 7))}
		r.store.stored = map[string]domain.RunKey{giKey("git.corp.example", domain.RunKey{GithubRunID: 5001, Attempt: 1}): {GithubRunID: 5001, Attempt: 1}}
		if out := r.refresh(t); out.Ingested != 1 {
			t.Errorf("Refresh() ingested %d, want 1: a stored run of another host does not count (EC30)", out.Ingested)
		}
	})
}

// TestGitLabRefresh_UsesAgentFilter: the agent filter reaches the GitLab store.
func TestGitLabRefresh_UsesAgentFilter(t *testing.T) {
	r := newGIRig(t)
	if _, err := r.svc.Refresh(t.Context(), "ws-1", "agent-1"); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if diff := cmp.Diff([]app.InstallationFilter{{AgentID: "agent-1"}}, r.store.listed); diff != "" {
		t.Errorf("ListGitLabInstallations filters mismatch (-want +got):\n%s", diff)
	}
}

// TestGitLabRefresh_OrderAndBudget covers FR31: both targets are walked newest installed_at first
// under one budget; installations the budget did not reach are not_checked.
func TestGitLabRefresh_OrderAndBudget(t *testing.T) {
	ghSlug := "reviewer"
	newGH := func(id string, at time.Time) domain.Installation {
		return domain.Installation{ID: id, AgentID: "agent-1", Repo: "acme/api", TargetType: domain.TargetGHA, InstalledAt: at, Slug: &ghSlug}
	}
	t.Run("order", func(t *testing.T) {
		r := newGIRig(t)
		r.gh.configured = true
		r.gh.listRuns = func(app.RepoRef, string, int) ([]domain.WorkflowRun, error) { return nil, nil }
		r.ghSt.installs = []domain.Installation{newGH("gh-new", giNow), newGH("gh-old", giNow.Add(-3*time.Hour))}
		r.store.installs = []domain.Installation{giInstall("gl-mid", giNow.Add(-time.Hour)), giInstall("gl-older", giNow.Add(-2*time.Hour))}
		out := r.refresh(t)
		var ids []string
		for _, i := range out.Installations {
			ids = append(ids, i.InstallationID)
		}
		if diff := cmp.Diff([]string{"gh-new", "gl-mid", "gl-older", "gh-old"}, ids); diff != "" {
			t.Errorf("installation order mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("budget", func(t *testing.T) {
		r := newGIRig(t)
		r.api.onCall = func() { r.clock.now = r.clock.now.Add(20 * time.Second) }
		var ps []domain.GitLabPipeline
		for i := range 5 {
			p := giMRPipeline(int64(100+i), 7+i)
			ps = append(ps, p)
			r.api.jobs[p.ID] = nil
		}
		r.api.pipelines = ps
		second := giInstall("inst-2", giNow.Add(-2*time.Hour))
		r.store.installs = append(r.store.installs, second)
		out := r.refresh(t)
		if out.Installations[0].State != domain.StateNotChecked || out.Installations[1].State != domain.StateNotChecked || !out.Degraded {
			t.Errorf("states = %s %s degraded %v, want not_checked twice and degraded",
				out.Installations[0].State, out.Installations[1].State, out.Degraded)
		}
		// the pipeline list and two job lists start before the 45 s budget ends, nothing after
		if got := len(r.api.calls); got != 3 {
			t.Errorf("GitLab calls = %d (%v), want 3", got, r.api.calls)
		}
	})
}

// TestGitLabRefresh_NoSensitiveTextInLogs: the signed storage URL of a failed artifact fetch and
// the connection never reach a log line; only ids, counts and enum labels do (NFR1).
func TestGitLabRefresh_NoSensitiveTextInLogs(t *testing.T) {
	r := newGIRig(t)
	giOne(r, nil)
	secret := "https://storage.example/obj?X-Amz-Signature=deadbeef"
	r.api.files[5001] = giFile{err: &app.GitLabError{Cause: errors.New(secret)}}
	out := r.refresh(t)
	if out.Installations[0].ErrorCode != domain.CodeGitLabUnavailable {
		t.Fatalf("installation = %+v, want gitlab_unavailable", out.Installations[0])
	}
	for _, l := range r.lines() {
		if s := fmt.Sprint(l); strings.Contains(s, "deadbeef") || strings.Contains(s, "storage.example") {
			t.Errorf("log line leaks the signed URL: %s", s)
		}
	}
}

// TestNewIngestService_GitLabDeps: a half-wired GitLab ingest, or a GitLab ledger without Tx, is a
// startup bug.
func TestNewIngestService_GitLabDeps(t *testing.T) {
	base := func() app.IngestDeps {
		return app.IngestDeps{
			Agents: fakeAgents{}, Store: &fakeStore{calls: &calls{}}, GitHub: &fakeGitHub{calls: &calls{}}, Zip: zipentry.New(),
		}
	}
	tests := []struct {
		name string
		deps func() app.IngestDeps
	}{
		{name: "missing store", deps: func() app.IngestDeps {
			d := base()
			d.GitLab = app.GitLabIngestDeps{Conns: &giConns{}, CI: &giAPI{}}
			return d
		}},
		{name: "missing connections", deps: func() app.IngestDeps {
			d := base()
			d.GitLab = app.GitLabIngestDeps{CI: &giAPI{}, Store: &giStore{}}
			return d
		}},
		{name: "ledger without tx", deps: func() app.IngestDeps {
			d := base()
			d.GitLab = app.GitLabIngestDeps{Conns: &giConns{}, CI: &giAPI{}, Store: &giStore{}, Ledger: &giLedger{}}
			return d
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewIngestService() did not panic")
				}
			}()
			app.NewIngestService(tc.deps())
		})
	}
}
