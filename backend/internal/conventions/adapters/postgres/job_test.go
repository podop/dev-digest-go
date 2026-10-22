package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/conventions/adapters/clonefiles"
	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/conventionstest"
	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/jobs"
	jobspg "dev-digest-go/backend/internal/jobs/adapters/postgres"
	"dev-digest-go/backend/internal/llm"
)

const secretKey = "sk-secret-xyz"

// lockedBuf is a log sink safe for concurrent writers.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type noSettings struct{}

func (noSettings) Get(context.Context, string) (map[string]json.RawMessage, error) { return nil, nil }

// stack is the whole scan pipeline over a real database, a real runner, a real clone directory
// and a scripted LLM provider.
type stack struct {
	*env
	svc      *app.Service
	runner   *jobs.Runner
	provider *conventionstest.Provider
	logs     *lockedBuf
	clone    string
}

type stackOpts struct {
	reply   func(ctx context.Context, n int, a llm.Attempt) (llm.AttemptResult, error)
	keys    conventionstest.Keys
	timeout time.Duration // job limit; 0 = app.JobTimeout
}

func newStack(t *testing.T, o stackOpts) *stack {
	t.Helper()
	e := newEnv(t)
	s := &stack{env: e, logs: &lockedBuf{}, provider: &conventionstest.Provider{Reply: o.reply}}
	log := slog.New(slog.NewTextHandler(s.logs, nil))
	if o.keys == nil {
		o.keys = conventionstest.Keys{"OPENAI_API_KEY": secretKey}
	}
	if o.timeout == 0 {
		o.timeout = app.JobTimeout
	}

	s.clone = t.TempDir()
	for rel, content := range map[string]string{
		"package.json": `{"name":"x"}`,
		"src/a.ts":     "import x from 'y';\n  export function getUser(id: string) {\n    return id;\n  }\n",
		"src/b.ts":     "export const b = 1;\n",
	} {
		p := filepath.Join(s.clone, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.repo = e.addRepo(t, "o/cloned", s.clone)

	client := conventionstest.NewClient(t, s.provider, o.keys, log)
	s.runner = jobs.New(jobspg.NewStore(e.db), jobs.Options{Logger: log, RetryBaseDelay: time.Millisecond})
	t.Cleanup(func() {
		if err := s.runner.Shutdown(10 * time.Second); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	extract := app.NewJobs(app.JobsDeps{
		Store: e.store, Repos: e.store, Files: clonefiles.New(), LLM: client, Features: noSettings{}, Redact: client,
	}, app.JobsOptions{Timeout: o.timeout, Log: log})
	s.runner.Register(app.KindExtract, extract.Extract, jobs.WithTimeout(o.timeout))
	s.svc = app.NewService(e.store, e.store, s.runner, app.ServiceOptions{Redact: client})
	return s
}

// scanAndWait starts a scan and waits for the runner to finish it.
func (s *stack) scanAndWait(t *testing.T) domain.Scan {
	t.Helper()
	started, err := s.svc.Extract(t.Context(), s.ws, s.repo)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := s.runner.Wait(ctx); err != nil {
		t.Fatalf("job did not finish: %v", err)
	}
	return s.scan(t, started.ID)
}

type jobRec struct {
	Status   string
	Attempts int
	Error    *string
	Payload  string
}

func (s *stack) jobRow(t *testing.T) jobRec {
	t.Helper()
	var r jobRec
	if err := s.db.Raw(`SELECT status, attempts, error, payload::text AS payload FROM jobs WHERE kind = ?`, app.KindExtract).Scan(&r).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func candidate(rule, path string, start int, snippet string) json.RawMessage {
	b, err := json.Marshal(map[string]any{
		"category": "style", "rule": rule, "confidence": 0.8,
		"evidence": []map[string]any{{"path": path, "start_line": start, "end_line": start, "snippet": snippet}},
	})
	if err != nil {
		panic(err)
	}
	return b
}

func answerWith(raw string) func(context.Context, int, llm.Attempt) (llm.AttemptResult, error) {
	return func(context.Context, int, llm.Attempt) (llm.AttemptResult, error) {
		return conventionstest.Answer(raw), nil
	}
}

func TestScan_EndToEnd(t *testing.T) {
	s := newStack(t, stackOpts{reply: answerWith(conventionstest.Candidates(
		candidate("Export functions by name", "src/a.ts", 2, "export function getUser(id: string) {"),
		candidate("Constants are exported", "src/b.ts", 1, "export const b = 1;"),
		candidate("Ghost rule", "src/ghost.ts", 1, "x"),
	))})
	// AC271: decided and edited rules stay, the unedited pending one is replaced.
	s.addConvention(t, s.repo, convSpec{rule: "Accepted stays", status: "accepted"})
	s.addConvention(t, s.repo, convSpec{rule: "Rejected stays", status: "rejected"})
	s.addConvention(t, s.repo, convSpec{rule: "Edited stays", status: "pending", edited: true})
	s.addConvention(t, s.repo, convSpec{rule: "Old pending goes", status: "pending"})

	// AC261: the route's answer is the running scan, and the job row carries the payload.
	started, err := s.svc.Extract(t.Context(), s.ws, s.repo)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if started.Status != domain.ScanRunning || len(started.SampledFiles) != 0 || started.Proposed != 0 || started.FinishedAt != nil {
		t.Errorf("Extract() = %+v, want a running scan with nothing in it", started)
	}
	if err := s.runner.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	scan := s.scan(t, started.ID)
	if scan.Status != domain.ScanDone || scan.Kept != 2 || scan.Proposed != 3 || scan.Model == nil || *scan.Model != "gpt-5.4" ||
		scan.CostUSD == nil || scan.FinishedAt == nil || scan.Error != nil {
		t.Errorf("scan = %+v, want done, kept 2 of 3, model gpt-5.4, a cost and finished_at", scan)
	}
	if diff := cmp.Diff([]string{"package.json", "src/a.ts", "src/b.ts"}, scan.SampledFiles); diff != "" {
		t.Errorf("sampled files mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]domain.Dropped{{Rule: "Ghost rule", Path: "src/ghost.ts", Reason: domain.ReasonFileNotFound}}, scan.Dropped); diff != "" {
		t.Errorf("dropped mismatch (-want +got):\n%s", diff)
	}
	list, err := s.store.List(t.Context(), s.repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Accepted stays", "Export functions by name", "Constants are exported", "Edited stays", "Rejected stays"}
	if diff := cmp.Diff(want, rules(list)); diff != "" {
		t.Errorf("conventions mismatch (-want +got):\n%s", diff)
	}
	for _, c := range list {
		if c.Rule == "Export functions by name" {
			wantEv := []domain.Evidence{{Path: "src/a.ts", StartLine: 2, EndLine: 2, Snippet: "export function getUser(id: string) {"}}
			if diff := cmp.Diff(wantEv, c.Evidence); diff != "" {
				t.Errorf("stored evidence is the real, dedented file text (-want +got):\n%s", diff)
			}
		}
	}

	job := s.jobRow(t)
	if job.Status != "done" || job.Attempts != 1 || job.Error != nil {
		t.Errorf("job = %+v, want done, attempts 1, no error", job)
	}
	var p app.ExtractPayload
	if err := json.Unmarshal([]byte(job.Payload), &p); err != nil || p != (app.ExtractPayload{ScanID: started.ID, WorkspaceID: s.ws, RepoID: s.repo}) {
		t.Errorf("job payload = %s (%v), want {scanId, workspaceId, repoId}", job.Payload, err)
	}
	if n := len(s.provider.Calls()); n != 1 {
		t.Errorf("provider requests = %d, want 1", n)
	}
}

// AC302: every candidate cites a missing file.
func TestScan_AllEvidenceMissingKeepsDecidedRules(t *testing.T) {
	s := newStack(t, stackOpts{reply: answerWith(conventionstest.Candidates(
		candidate("One", "nope/1.ts", 1, "x"), candidate("Two", "nope/2.ts", 1, "x")))})
	s.addConvention(t, s.repo, convSpec{rule: "Accepted stays", status: "accepted"})
	scan := s.scanAndWait(t)
	if scan.Status != domain.ScanDone || scan.Kept != 0 || scan.Proposed != 2 || len(scan.Dropped) != 2 {
		t.Fatalf("scan = %+v, want done, kept 0 of 2, 2 dropped", scan)
	}
	for _, d := range scan.Dropped {
		if d.Reason != domain.ReasonFileNotFound {
			t.Errorf("dropped %+v, want reason file_not_found", d)
		}
	}
	if list, _ := s.store.List(t.Context(), s.repo); len(list) != 1 || list[0].Rule != "Accepted stays" {
		t.Errorf("conventions = %v, want only the accepted rule", rules(list))
	}
}

// AC272: no key. The route has answered 202 already; the scan fails, the job is done, once.
func TestScan_MissingKey(t *testing.T) {
	s := newStack(t, stackOpts{keys: conventionstest.Keys{"X": "y"}, reply: answerWith(`{"candidates":[]}`)})
	s.addConvention(t, s.repo, convSpec{rule: "Pending stays when the scan fails", status: "pending"})
	scan := s.scanAndWait(t)
	if scan.Status != domain.ScanFailed || scan.Error == nil || *scan.Error != "OPENAI_API_KEY is not configured" {
		t.Errorf("scan = %s %v, want failed with OPENAI_API_KEY is not configured", scan.Status, scan.Error)
	}
	if job := s.jobRow(t); job.Status != "done" || job.Attempts != 1 {
		t.Errorf("job = %+v, want done with attempts 1", job)
	}
	if list, _ := s.store.List(t.Context(), s.repo); len(list) != 1 {
		t.Errorf("conventions = %v, want no change", rules(list))
	}
}

// AC273: invalid output twice.
func TestScan_InvalidOutputTwice(t *testing.T) {
	s := newStack(t, stackOpts{reply: answerWith(`{"candidates":"x"}`)})
	scan := s.scanAndWait(t)
	const prefix = "OpenAI structured output failed schema validation for ConventionExtraction after 2 attempt(s)"
	if scan.Status != domain.ScanFailed || scan.Error == nil || !strings.HasPrefix(*scan.Error, prefix) || len([]rune(*scan.Error)) > 500 {
		t.Errorf("scan = %s %v, want failed with %q... of at most 500 characters", scan.Status, scan.Error, prefix)
	}
	if n := len(s.provider.Calls()); n != 2 {
		t.Errorf("provider requests = %d, want exactly 2", n)
	}
}

// AC274: a provider that never answers and a job limit of 2 s.
func TestScan_JobTimeout(t *testing.T) {
	s := newStack(t, stackOpts{timeout: 2 * time.Second, reply: func(ctx context.Context, _ int, _ llm.Attempt) (llm.AttemptResult, error) {
		<-ctx.Done()
		return llm.AttemptResult{}, ctx.Err()
	}})
	start := time.Now()
	scan := s.scanAndWait(t)
	if scan.Status != domain.ScanFailed || scan.Error == nil || *scan.Error != "The scan timed out after 2 s" {
		t.Errorf("scan = %s %v, want failed with the 2 s timeout text", scan.Status, scan.Error)
	}
	if took := time.Since(start); took > 12*time.Second {
		t.Errorf("scan ended after %v, want within 12 s", took)
	}
	if n := len(s.provider.Calls()); n > 2 {
		t.Errorf("provider requests = %d, want at most 2", n)
	}
	if job := s.jobRow(t); job.Status != "done" || job.Attempts != 1 {
		t.Errorf("job = %+v, want done, not retried", job)
	}
}

// EC205: the process shuts down while the model call is in flight.
func TestScan_Shutdown(t *testing.T) {
	inFlight := make(chan struct{})
	var once sync.Once
	s := newStack(t, stackOpts{reply: func(ctx context.Context, _ int, _ llm.Attempt) (llm.AttemptResult, error) {
		once.Do(func() { close(inFlight) })
		<-ctx.Done()
		return llm.AttemptResult{}, ctx.Err()
	}})
	started, err := s.svc.Extract(t.Context(), s.ws, s.repo)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-inFlight:
	case <-time.After(30 * time.Second):
		t.Fatal("the model call never started")
	}
	if err := s.runner.Shutdown(10 * time.Second); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	scan := s.scan(t, started.ID)
	if scan.Status != domain.ScanFailed || scan.Error == nil || *scan.Error != "The scan was interrupted" || scan.FinishedAt == nil {
		t.Errorf("scan = %+v, want failed with %q", scan, "The scan was interrupted")
	}
	if job := s.jobRow(t); job.Status != "done" || job.Attempts != 1 {
		t.Errorf("job = %+v, want done, not retried", job)
	}
}

// AC275: the key never reaches the scan, the job row or a log line.
func TestScan_ProviderErrorNeverLeaksTheKey(t *testing.T) {
	s := newStack(t, stackOpts{reply: func(context.Context, int, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{}, fmt.Errorf("401 Incorrect API key provided: %s", secretKey)
	}})
	scan := s.scanAndWait(t)
	if scan.Status != domain.ScanFailed || scan.Error == nil || strings.Contains(*scan.Error, secretKey) || !strings.Contains(*scan.Error, "[REDACTED]") {
		t.Errorf("scan error = %v, want the key replaced by [REDACTED]", scan.Error)
	}
	job := s.jobRow(t)
	if job.Error != nil && strings.Contains(*job.Error, secretKey) {
		t.Errorf("job error contains the key: %s", *job.Error)
	}
	if logs := s.logs.String(); strings.Contains(logs, secretKey) || !strings.Contains(logs, "conventions scan failed") {
		t.Errorf("logs = %s, want the failure logged without the key", logs)
	}
}

// AC263: a running scan started a minute ago blocks a new one; one started 11 minutes ago is
// failed and replaced.
func TestExtract_StaleScanHandling(t *testing.T) {
	s := newStack(t, stackOpts{reply: answerWith(`{"candidates":[]}`)})
	young := s.addScan(t, s.repo, "running", time.Now().Add(-time.Minute))
	if _, err := s.svc.Extract(t.Context(), s.ws, s.repo); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("Extract() with a 1-minute-old running scan: error = %v, want scan_running", err)
	}
	if got := s.scan(t, young); got.Status != domain.ScanRunning {
		t.Errorf("young scan status = %s, want running", got.Status)
	}
	if err := s.db.Exec(`UPDATE convention_scans SET started_at = ? WHERE id = ?`, time.Now().Add(-11*time.Minute), young).Error; err != nil {
		t.Fatal(err)
	}
	fresh, err := s.svc.Extract(t.Context(), s.ws, s.repo)
	if err != nil {
		t.Fatalf("Extract() with an 11-minute-old running scan: error = %v", err)
	}
	if got := s.scan(t, young); got.Status != domain.ScanFailed || got.Error == nil || *got.Error != "The scan was interrupted" {
		t.Errorf("old scan = %+v, want failed with The scan was interrupted", got)
	}
	if fresh.ID == young {
		t.Error("Extract() reused the old scan")
	}
	if err := s.runner.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// AC264: scans left running are failed at startup, before the server listens.
func TestFailInterrupted_AtStartup(t *testing.T) {
	s := newStack(t, stackOpts{})
	id := s.addScan(t, s.repo, "running", time.Now())
	n, err := s.svc.FailInterrupted(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("FailInterrupted() = %d, %v, want 1, nil", n, err)
	}
	if got := s.scan(t, id); got.Status != domain.ScanFailed || got.Error == nil || *got.Error != "The scan was interrupted" || got.FinishedAt == nil {
		t.Errorf("scan = %+v, want failed, interrupted, finished", got)
	}
}
