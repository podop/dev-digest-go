package app_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
)

// This file holds the hand-written fakes of the ports of ci/app and the log capture the tests
// share. The fakes record their calls in the shape of the Node goldens (testdata/goldens/ci.json),
// so a scenario compares the recorded calls with the golden's.

// calls records fake port calls as generic JSON objects ({"fn": …, other fields}).
type calls struct {
	mu   sync.Mutex
	list []map[string]any
}

func (c *calls) add(fn string, fields map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[string]any{"fn": fn}
	for k, v := range fields {
		m[k] = v
	}
	c.list = append(c.list, m)
}

// snapshot returns the recorded calls normalised to generic JSON values.
func (c *calls) snapshot(t testing.TB) []any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	return normalize(t, append([]map[string]any{}, c.list...)).([]any)
}

// normalize round-trips v through JSON so that Go values compare with decoded golden values.
func normalize(t testing.TB, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return out
}

// logLine is a captured log line in the golden's shape.
type logLine struct {
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Obj   map[string]any `json:"obj"`
}

// logCapture is a slog.Handler that records every line.
type logCapture struct {
	mu    *sync.Mutex
	lines *[]logLine
}

func newLogCapture() *logCapture { return &logCapture{mu: &sync.Mutex{}, lines: &[]logLine{}} }

func (h *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCapture) Handle(_ context.Context, r slog.Record) error {
	obj := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		obj[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.lines = append(*h.lines, logLine{Level: strings.ToLower(r.Level.String()), Msg: r.Message, Obj: obj})
	return nil
}

func (h *logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logCapture) WithGroup(string) slog.Handler      { return h }

func (h *logCapture) logger() *slog.Logger { return slog.New(h) }

func (h *logCapture) snapshot(t testing.TB) []any {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	return normalize(t, *h.lines).([]any)
}

// fakeAgents is an in-memory AgentDirectory.
type fakeAgents struct {
	agents map[string]domain.ExportAgent
}

func (f fakeAgents) Get(_ context.Context, _, id string) (domain.ExportAgent, bool, error) {
	a, ok := f.agents[id]
	return a, ok, nil
}

func (f fakeAgents) Exists(_ context.Context, _, id string) (bool, error) {
	_, ok := f.agents[id]
	return ok, nil
}

// fakeRepos is an in-memory RepoDirectory.
type fakeRepos struct {
	repos map[string]domain.ImportedRepo
}

func (f fakeRepos) FindByFullName(_ context.Context, _, fullName string) (domain.ImportedRepo, bool, error) {
	r, ok := f.repos[fullName]
	return r, ok, nil
}

// fakeRunner is a RunnerBundle.
type fakeRunner struct {
	artifact app.RunnerArtifact
	ok       bool
}

func (f fakeRunner) Runner() (app.RunnerArtifact, bool) { return f.artifact, f.ok }

var testRunner = app.RunnerArtifact{
	Encoded: "H4sIAAAAAAAA\n",
	SHA256:  strings.Repeat("ab", 32),
	BuildID: "0123456789ab",
}

// fakeStore is an in-memory Store. Exported fields configure it; calls records the calls the
// goldens know (slugOwner, upsertInstallation, listInstallations, storedRunKeys, recordRun).
type fakeStore struct {
	calls *calls

	// export
	existing    *domain.Installation // the installation FindInstallation finds before an upsert
	slugOwner   string               // "" = nobody
	upsertErr   error
	vanish      bool // FindInstallation finds nothing after the upsert
	saved       *domain.Installation
	listResults []domain.Installation

	// ingest
	installs    []domain.Installation
	stored      map[domain.RunKey]bool
	recordFalse map[domain.RunKey]bool
	keysErr     error
	recordErr   error
	recorded    []domain.NewRun
	runs        []domain.Run
}

var installedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (f *fakeStore) ListInstallations(_ context.Context, _ string, flt app.InstallationFilter) ([]domain.Installation, error) {
	filter := map[string]any{}
	if flt.AgentID != "" {
		filter["agentId"] = flt.AgentID
	}
	f.calls.add("listInstallations", map[string]any{"filter": filter})
	src := f.installs
	if f.listResults != nil {
		src = f.listResults
	}
	var out []domain.Installation
	for _, i := range src {
		if flt.AgentID == "" || i.AgentID == flt.AgentID {
			out = append(out, i)
		}
	}
	return out, nil
}

func (f *fakeStore) FindInstallation(_ context.Context, _, agentID, repo string) (domain.Installation, bool, error) {
	if f.saved != nil {
		return *f.saved, true, nil
	}
	if f.existing != nil && f.existing.AgentID == agentID && f.existing.Repo == repo {
		return *f.existing, true, nil
	}
	return domain.Installation{}, false, nil
}

func (f *fakeStore) SlugOwner(_ context.Context, repo, slug string) (string, bool, error) {
	f.calls.add("slugOwner", map[string]any{"repo": repo, "slug": slug})
	return f.slugOwner, f.slugOwner != "", nil
}

func (f *fakeStore) UpsertInstallation(_ context.Context, in domain.NewInstallation) error {
	f.calls.add("upsertInstallation", map[string]any{"values": map[string]any{
		"agentId": in.AgentID, "repo": in.Repo, "slug": in.Slug, "manifestVersion": in.ManifestVersion,
	}})
	if f.upsertErr != nil {
		return f.upsertErr
	}
	if !f.vanish {
		slug, version := in.Slug, in.ManifestVersion
		f.saved = &domain.Installation{
			ID: "inst-new", AgentID: in.AgentID, Repo: in.Repo, TargetType: domain.TargetGHA,
			InstalledAt: installedAt, Slug: &slug, ManifestVersion: &version,
		}
	}
	return nil
}

func (f *fakeStore) StoredRunKeys(_ context.Context, keys []domain.RunKey) (map[domain.RunKey]bool, error) {
	names := make([]string, len(keys))
	out := map[domain.RunKey]bool{}
	for i, k := range keys {
		names[i] = keyName(k)
		if f.stored[k] {
			out[k] = true
		}
	}
	f.calls.add("storedRunKeys", map[string]any{"keys": names})
	if f.keysErr != nil {
		return nil, f.keysErr
	}
	return out, nil
}

func (f *fakeStore) RecordRun(_ context.Context, run domain.NewRun) (app.Recorded, error) {
	key := domain.RunKey{GithubRunID: run.GithubRunID, Attempt: run.RunAttempt}
	f.calls.add("recordRun", map[string]any{"key": keyName(key)})
	if f.recordErr != nil {
		return app.Recorded{}, f.recordErr
	}
	if f.recordFalse[key] || f.stored[key] {
		return app.Recorded{}, nil
	}
	if f.stored == nil {
		f.stored = map[domain.RunKey]bool{}
	}
	f.stored[key] = true
	f.recorded = append(f.recorded, run)
	return app.Recorded{AgentRunID: "agent-run-" + keyName(key), Stored: true}, nil
}

func (f *fakeStore) ListRuns(_ context.Context, _ string, flt app.RunFilter) ([]domain.Run, error) {
	return f.runs[:min(len(f.runs), flt.Limit)], nil
}

func keyName(k domain.RunKey) string {
	return strings.Join([]string{itoa(k.GithubRunID), itoa(int64(k.Attempt))}, ":")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// fakeGitHub is a GitHub whose behaviour is given as functions; a nil function is a test bug.
type fakeGitHub struct {
	calls      *calls
	configured bool

	commitFiles  func(app.RepoRef, app.CommitFilesInput) error
	findOpenPR   func(app.RepoRef, string) (string, bool, error)
	openPR       func(app.RepoRef, app.OpenPullRequestInput) (string, error)
	listRuns     func(app.RepoRef, string, int) ([]domain.WorkflowRun, error)
	listArtifact func(app.RepoRef, int64) ([]domain.RunArtifact, error)
	download     func(app.RepoRef, int64, int64) ([]byte, error)
	// onCall runs at the start of every ingest call (the ingest tests advance their clock there).
	onCall func()
	// hook, when set, runs at the start of every ingest call with the call's context; a non-nil
	// result is the call's error (a call that blocks until its context ends).
	hook func(context.Context) error
}

func (f *fakeGitHub) Configured() bool { return f.configured }

func refShape(r app.RepoRef) map[string]any { return map[string]any{"owner": r.Owner, "name": r.Name} }

func (f *fakeGitHub) CommitFiles(_ context.Context, repo app.RepoRef, in app.CommitFilesInput) error {
	paths := make([]string, len(in.Files))
	for i, file := range in.Files {
		paths[i] = file.Path
	}
	f.calls.add("commitFiles", map[string]any{
		"ref": refShape(repo), "branch": in.Branch, "base": in.Base, "message": in.Message, "paths": paths,
	})
	return f.commitFiles(repo, in)
}

func (f *fakeGitHub) FindOpenPR(_ context.Context, repo app.RepoRef, branch string) (string, bool, error) {
	f.calls.add("findOpenPr", map[string]any{"ref": refShape(repo), "branch": branch})
	return f.findOpenPR(repo, branch)
}

func (f *fakeGitHub) OpenPullRequest(_ context.Context, repo app.RepoRef, in app.OpenPullRequestInput) (string, error) {
	f.calls.add("openPullRequest", map[string]any{
		"ref": refShape(repo), "title": in.Title, "head": in.Head, "base": in.Base, "body": in.Body,
	})
	return f.openPR(repo, in)
}

func (f *fakeGitHub) ListWorkflowRuns(ctx context.Context, repo app.RepoRef, file string, perPage int) ([]domain.WorkflowRun, error) {
	f.calls.add("listWorkflowRuns", map[string]any{
		"repo": repo.Owner + "/" + repo.Name, "workflowFile": file, "opts": map[string]any{"perPage": perPage},
	})
	if f.onCall != nil {
		f.onCall()
	}
	if f.hook != nil {
		if err := f.hook(ctx); err != nil {
			return nil, err
		}
	}
	return f.listRuns(repo, file, perPage)
}

func (f *fakeGitHub) ListRunArtifacts(ctx context.Context, repo app.RepoRef, runID int64) ([]domain.RunArtifact, error) {
	f.calls.add("listRunArtifacts", map[string]any{"repo": repo.Owner + "/" + repo.Name, "runId": runID})
	if f.onCall != nil {
		f.onCall()
	}
	if f.hook != nil {
		if err := f.hook(ctx); err != nil {
			return nil, err
		}
	}
	return f.listArtifact(repo, runID)
}

func (f *fakeGitHub) DownloadArtifact(ctx context.Context, repo app.RepoRef, id, maxBytes int64) ([]byte, error) {
	f.calls.add("downloadArtifact", map[string]any{
		"repo": repo.Owner + "/" + repo.Name, "artifactId": id, "opts": map[string]any{"maxBytes": maxBytes},
	})
	if f.onCall != nil {
		f.onCall()
	}
	if f.hook != nil {
		if err := f.hook(ctx); err != nil {
			return nil, err
		}
	}
	return f.download(repo, id, maxBytes)
}

var _ app.GitHub = (*fakeGitHub)(nil)

// fakeLedger records the CI runs it is asked to append; err makes every append fail.
type fakeLedger struct {
	runs []app.CIRun
	err  error
}

func (f *fakeLedger) RecordCIRun(_ context.Context, run app.CIRun) error {
	if f.err != nil {
		return f.err
	}
	f.runs = append(f.runs, run)
	return nil
}

// fakeTx runs fn and, like a database, undoes what the store and the ledger wrote inside it when
// fn fails. It counts the transactions it ran.
type fakeTx struct {
	store  *fakeStore
	ledger *fakeLedger
	ran    int
}

func (f *fakeTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	f.ran++
	stored := maps.Clone(f.store.stored)
	recorded := slices.Clone(f.store.recorded)
	ledgerRuns := slices.Clone(f.ledger.runs)
	err := fn(ctx)
	if err != nil {
		f.store.stored, f.store.recorded, f.ledger.runs = stored, recorded, ledgerRuns
	}
	return err
}
