package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf16"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/conventionstest"
	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
)

const (
	scanID = "scan-1"
	secret = "sk-secret-xyz"
)

// goFiles is a small clone: one config file and two sources.
func goFiles() map[string]string {
	return map[string]string{
		"package.json": `{"name":"x"}`,
		"src/a.ts":     "import x from 'y';\n  export function getUser(id: string) {\n    return id;\n  }\n",
		"src/b.ts":     "export const b = 1;\n",
		"README.md":    "# readme\n",
	}
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

type jobEnv struct {
	jobs   *app.Jobs
	store  *memStore
	files  *memFiles
	model  *modelFake
	logs   *bytes.Buffer
	bag    map[string]json.RawMessage
	repos  map[string]app.Repo
	setErr error
}

func newJobs(t *testing.T, files map[string]string, reply func(context.Context, llm.Request) (llm.Result, error)) *jobEnv {
	t.Helper()
	e := &jobEnv{
		store: &memStore{}, files: &memFiles{files: files}, model: &modelFake{reply: reply}, logs: &bytes.Buffer{},
		repos: map[string]app.Repo{repoID: {ID: repoID, FullName: "o/r", ClonePath: "/clones/o/r"}},
	}
	e.store.scans = append(e.store.scans, &domain.Scan{ID: scanID, WorkspaceID: ws, RepoID: repoID, Status: domain.ScanRunning, StartedAt: t0})
	e.build(t, e.model, 0)
	return e
}

func (e *jobEnv) build(t *testing.T, model interface {
	Structured(ctx context.Context, req llm.Request) (llm.Result, error)
}, timeout time.Duration) {
	t.Helper()
	e.jobs = app.NewJobs(app.JobsDeps{
		Store: e.store, Repos: memRepos{e.repos}, Files: e.files, LLM: model,
		Features: settings{bag: e.bag, err: e.setErr}, Redact: redactKey(secret),
	}, app.JobsOptions{Timeout: timeout, Now: func() time.Time { return t0.Add(time.Minute) },
		Log: slog.New(slog.NewTextHandler(e.logs, nil))})
}

func payload() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"scanId":%q,"workspaceId":%q,"repoId":%q}`, scanID, ws, repoID))
}

func (e *jobEnv) scan() domain.Scan { return *e.store.scan(scanID) }

func TestExtract_SuccessStoresResultAndAsksTheModelAsSpecified(t *testing.T) {
	reply := answer(conventionstest.Candidates(
		candidate("Export functions by name", "src/a.ts", 2, "export function getUser(id: string) {"),
		candidate("Use named exports", "src/b.ts", 1, "export const b = 1;"), // equals the accepted rule below
		candidate("Missing file rule", "src/missing.ts", 1, "x"),
	))
	e := newJobs(t, goFiles(), reply)
	e.store.add(domain.Convention{ID: "acc", Status: domain.StatusAccepted, Rule: "Use named exports!"})
	e.store.add(domain.Convention{ID: "pend", Status: domain.StatusPending, Rule: "Pending unedited"})

	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v, want nil", err)
	}
	if len(e.store.completed) != 1 {
		t.Fatalf("CompleteScan calls = %d, scan = %+v, want 1", len(e.store.completed), e.scan())
	}
	got := e.store.completed[0]
	if got.Ref != (app.ScanRef{ID: scanID, WorkspaceID: ws, RepoID: repoID}) {
		t.Errorf("scan ref = %+v", got.Ref)
	}
	cost := 0.0123
	wantSum := app.Summary{
		SampledFiles: []string{"package.json", "src/a.ts", "src/b.ts"}, Proposed: 3, Kept: 1,
		Dropped: []domain.Dropped{
			{Rule: "Use named exports", Path: "src/b.ts", Reason: domain.ReasonDuplicate},
			{Rule: "Missing file rule", Path: "src/missing.ts", Reason: domain.ReasonFileNotFound},
		},
		Model: "gpt-5.4", TokensIn: 1000, TokensOut: 200, CostUSD: &cost,
	}
	if diff := cmp.Diff(wantSum, got.Sum); diff != "" {
		t.Errorf("summary mismatch (-want +got):\n%s", diff)
	}
	wantKept := []domain.KeptConvention{{
		Category: "style", Rule: "Export functions by name", Confidence: 0.8,
		Evidence: []domain.Evidence{{Path: "src/a.ts", StartLine: 2, EndLine: 2, Snippet: "export function getUser(id: string) {"}},
	}}
	if diff := cmp.Diff(wantKept, got.Kept); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}

	reqs := e.model.requests()
	if len(reqs) != 1 {
		t.Fatalf("model requests = %d, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Provider != llm.OpenAI || r.Model != "gpt-5.4" || r.SchemaName != "ConventionExtraction" ||
		r.MaxTokens != 6000 || r.Timeout != 100*time.Second || r.Reprompts != 1 ||
		r.Feature != "conventions" || r.CorrelationID != scanID {
		t.Errorf("request = %+v, want openai gpt-5.4, ConventionExtraction, 6000 tokens, 100 s, 1 reprompt", r)
	}
	if r.Temperature == nil || *r.Temperature != 0.1 {
		t.Errorf("temperature = %v, want 0.1", r.Temperature)
	}
	if len(r.Messages) != 2 || r.Messages[0].Role != llm.System || r.Messages[0].Content != domain.ExtractionSystemPrompt ||
		r.Messages[1].Role != llm.User {
		t.Fatalf("messages = %+v, want a system prompt and a user message", r.Messages)
	}
	user := r.Messages[1].Content
	if !strings.HasPrefix(user, "Repository: o/r\n\nSampled files (config first, then the most central source files):\n\n<untrusted source=\"repository-sample\">\n=== FILE: package.json (config) ===") ||
		!strings.HasSuffix(user, "\n</untrusted>\n\nReturn the house conventions as structured output.") {
		t.Errorf("user message = %q, want the four parts", user)
	}
	if len(r.Sections) != 3 || r.Sections[0].Name != "system" || r.Sections[1].Name != "task" ||
		r.Sections[2].Name != "repository_sample" || r.Sections[2].Trust != llm.Untrusted ||
		r.Sections[2].Items == nil || *r.Sections[2].Items != 3 {
		t.Errorf("sections = %+v, want system, task and an untrusted repository_sample of 3 items", r.Sections)
	}
	if r.Sections[0].Chars != llm.UTF16Len(domain.ExtractionSystemPrompt) {
		t.Errorf("system section chars = %d, want %d", r.Sections[0].Chars, llm.UTF16Len(domain.ExtractionSystemPrompt))
	}
}

func TestExtract_UsesTheConfiguredFeatureModel(t *testing.T) {
	e := newJobs(t, goFiles(), answer(`{"candidates":[]}`))
	e.bag = map[string]json.RawMessage{"feature_models": json.RawMessage(`{"conventions":{"provider":"anthropic","model":"claude-sonnet-5"}}`)}
	e.build(t, e.model, 0)
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	r := e.model.requests()[0]
	if r.Provider != llm.Anthropic || r.Model != "claude-sonnet-5" {
		t.Errorf("request provider/model = %s/%s, want anthropic/claude-sonnet-5", r.Provider, r.Model)
	}
	if s := e.scan(); s.Status != domain.ScanDone {
		t.Errorf("scan status = %s, want done", s.Status)
	}
}

func TestExtract_SampleSkipsBlankAndUnreadableFilesAndDedupesConfigs(t *testing.T) {
	files := map[string]string{
		"package.json": `{"name":"x"}`,
		".eslintrc.js": "module.exports = {};\n", // a config file that is also a source file
		"src/blank.ts": "  \n\t\n",
		"src/a.ts":     "a\n",
	}
	e := newJobs(t, files, answer(`{"candidates":[]}`))
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	want := []string{".eslintrc.js", "package.json", "src/a.ts"}
	if diff := cmp.Diff(want, e.store.completed[0].Sum.SampledFiles); diff != "" {
		t.Errorf("sampled files mismatch (-want +got):\n%s", diff)
	}
}

func TestExtract_FailuresEndTheScanFailedAndTheJobDone(t *testing.T) {
	long := strings.Repeat("é", 700) + secret
	tests := []struct {
		name      string
		files     map[string]string
		mutate    func(*jobEnv)
		reply     func(context.Context, llm.Request) (llm.Result, error)
		want      string
		wantFiles []string
	}{
		{name: "repo without clone path", files: goFiles(), reply: answer(`{}`),
			mutate: func(e *jobEnv) { e.repos[repoID] = app.Repo{ID: repoID, FullName: "o/r"} },
			want:   "The repo is not cloned"},
		{name: "repo deleted", files: goFiles(), reply: answer(`{}`),
			mutate: func(e *jobEnv) { delete(e.repos, repoID) }, want: "The repo is not cloned"},
		{name: "clone directory gone", files: goFiles(), reply: answer(`{}`),
			mutate: func(e *jobEnv) { e.files.missing = true }, want: "The repo is not cloned"},
		{name: "nothing to sample", files: map[string]string{"README.md": "# x\n", "logo.png": "\x89PNG"}, reply: answer(`{}`),
			want: "No source or config files to sample in the clone"},
		{name: "missing key", files: goFiles(), reply: failWith(apperr.Config("OPENAI_API_KEY is not configured")),
			want: "OPENAI_API_KEY is not configured", wantFiles: []string{"package.json", "src/a.ts", "src/b.ts"}},
		{name: "settings unreadable", files: goFiles(), reply: answer(`{}`),
			mutate: func(e *jobEnv) { e.setErr = errors.New("db down") }, want: "load settings: db down",
			wantFiles: []string{"package.json", "src/a.ts", "src/b.ts"}},
		{name: "model answer does not decode into candidates", files: goFiles(), reply: answer(`{"candidates":"x"}`),
			want: "model answer invalid: candidates: Expected array, received string", wantFiles: []string{"package.json", "src/a.ts", "src/b.ts"}},
		{name: "key in the error is redacted and the text cut to 500 characters", files: goFiles(),
			reply: failWith(apperr.ExternalService(long)), wantFiles: []string{"package.json", "src/a.ts", "src/b.ts"},
			want: strings.Repeat("é", 500)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newJobs(t, tc.files, tc.reply)
			if tc.mutate != nil {
				tc.mutate(e)
			}
			e.build(t, e.model, 0)
			if err := e.jobs.Extract(t.Context(), payload()); err != nil {
				t.Fatalf("Extract() error = %v, want nil so the job ends done", err)
			}
			s := e.scan()
			if s.Status != domain.ScanFailed || s.Error == nil || *s.Error != tc.want {
				got := "<nil>"
				if s.Error != nil {
					got = *s.Error
				}
				t.Errorf("scan = %s, error %q, want failed with %q", s.Status, got, tc.want)
			}
			if s.FinishedAt == nil || !s.FinishedAt.Equal(t0.Add(time.Minute)) {
				t.Errorf("finished_at = %v, want the job clock", s.FinishedAt)
			}
			if diff := cmp.Diff(tc.wantFiles, s.SampledFiles, cmpEmpty()); diff != "" {
				t.Errorf("sampled files mismatch (-want +got):\n%s", diff)
			}
			if len(e.store.completed) != 0 {
				t.Errorf("CompleteScan was called for a failing scan")
			}
			if !strings.Contains(e.logs.String(), "conventions scan failed") || !strings.Contains(e.logs.String(), scanID) ||
				!strings.Contains(e.logs.String(), repoID) || strings.Contains(e.logs.String(), secret) {
				t.Errorf("log = %q, want a warning with scan and repo ids and no key", e.logs.String())
			}
		})
	}
}

func TestExtract_StoredErrorIsAtMost500UTF16Units(t *testing.T) {
	e := newJobs(t, goFiles(), failWith(errors.New(strings.Repeat("😀", 400))))
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	got := *e.scan().Error
	if n := len(utf16.Encode([]rune(got))); n != 500 {
		t.Errorf("stored error is %d UTF-16 units, want 500", n)
	}
}

func TestExtract_RedactsKeysBeforeCutting(t *testing.T) {
	// The key straddles the 500 mark; redacting after the cut would leak its first half.
	msg := strings.Repeat("a", 490) + secret
	e := newJobs(t, goFiles(), failWith(errors.New(msg)))
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	got := *e.scan().Error
	if strings.Contains(got, "sk-") || !strings.HasSuffix(got, "[REDACTED]") {
		t.Errorf("stored error = %q, want the key replaced by [REDACTED] before the cut", got[480:])
	}
}

func TestExtract_JobTimeoutAndShutdown(t *testing.T) {
	block := func(ctx context.Context, _ llm.Request) (llm.Result, error) {
		<-ctx.Done()
		return llm.Result{}, fmt.Errorf("OpenAI structured call was cancelled: %w", ctx.Err())
	}
	tests := []struct {
		name    string
		timeout time.Duration
		limit   time.Duration
		cancel  bool
		want    string
	}{
		{"default limit", 0, 300 * time.Second, false, "The scan timed out after 300 s"},
		{"custom limit", 2 * time.Second, 2 * time.Second, false, "The scan timed out after 2 s"},
		{"shutdown", 0, 300 * time.Second, true, "The scan was interrupted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newJobs(t, goFiles(), block)
				e.build(t, e.model, tc.timeout)
				ctx, cancel := context.WithTimeout(context.Background(), tc.limit)
				defer cancel()
				if tc.cancel {
					time.AfterFunc(5*time.Second, cancel)
				}
				start := time.Now()
				if err := e.jobs.Extract(ctx, payload()); err != nil {
					t.Fatalf("Extract() error = %v, want nil", err)
				}
				s := e.scan()
				if s.Status != domain.ScanFailed || s.Error == nil || *s.Error != tc.want {
					t.Errorf("scan = %s %v, want failed with %q", s.Status, s.Error, tc.want)
				}
				if got := e.store.failCtxErr; len(got) != 1 || got[0] != nil {
					t.Errorf("FailScan context errors = %v, want one live context (write survives the job context)", got)
				}
				if tc.cancel && time.Since(start) != 5*time.Second {
					t.Errorf("returned after %v, want 5s", time.Since(start))
				}
			})
		})
	}
}

func TestExtract_ScanNoLongerRunningIsLeftAlone(t *testing.T) {
	e := newJobs(t, goFiles(), answer(`{"candidates":[]}`))
	e.store.scan(scanID).Status = domain.ScanFailed
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v, want nil", err)
	}
	if s := e.scan(); s.Status != domain.ScanFailed || len(e.store.completed) != 0 {
		t.Errorf("scan = %+v, completed = %d, want it untouched", s, len(e.store.completed))
	}
}

func TestExtract_PanicFailsTheScan(t *testing.T) {
	e := newJobs(t, goFiles(), func(context.Context, llm.Request) (llm.Result, error) { panic("kaboom") })
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v, want nil", err)
	}
	if s := e.scan(); s.Status != domain.ScanFailed || s.Error == nil || *s.Error != "scan panicked: kaboom" {
		t.Errorf("scan = %+v, want failed with the panic text", s)
	}
}

func TestExtract_UnreadablePayloadIsTheOnlyError(t *testing.T) {
	e := newJobs(t, goFiles(), answer(`{}`))
	for name, raw := range map[string]string{"not json": `{`, "ids missing": `{"scanId":"s"}`} {
		if err := e.jobs.Extract(t.Context(), json.RawMessage(raw)); err == nil {
			t.Errorf("Extract(%s) error = nil, want a decode error", name)
		}
	}
}

// realClient builds the job over a real llm.Client and a scripted provider.
func realClient(t *testing.T, p *conventionstest.Provider, e *jobEnv, timeout time.Duration) {
	t.Helper()
	client := conventionstest.NewClient(t, p, conventionstest.Keys{"OPENAI_API_KEY": secret}, e.logsLogger())
	e.jobs = app.NewJobs(app.JobsDeps{
		Store: e.store, Repos: memRepos{e.repos}, Files: e.files, LLM: client,
		Features: settings{}, Redact: client,
	}, app.JobsOptions{Timeout: timeout, Now: func() time.Time { return t0 }, Log: e.logsLogger()})
}

func (e *jobEnv) logsLogger() *slog.Logger { return slog.New(slog.NewTextHandler(e.logs, nil)) }

func TestExtract_InvalidOutputTwiceFailsAfterTwoRequests(t *testing.T) {
	p := &conventionstest.Provider{Reply: func(context.Context, int, llm.Attempt) (llm.AttemptResult, error) {
		return conventionstest.Answer(`{"candidates":"x"}`), nil
	}}
	e := newJobs(t, goFiles(), nil)
	realClient(t, p, e, 0)
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	s := e.scan()
	const prefix = "OpenAI structured output failed schema validation for ConventionExtraction after 2 attempt(s)"
	if s.Status != domain.ScanFailed || s.Error == nil || !strings.HasPrefix(*s.Error, prefix) || len(utf16.Encode([]rune(*s.Error))) > 500 {
		t.Errorf("scan = %s %v, want failed with %q... of at most 500 characters", s.Status, s.Error, prefix)
	}
	if n := len(p.Calls()); n != 2 {
		t.Errorf("provider requests = %d, want 2", n)
	}
}

func TestExtract_ProviderErrorNeverLeaksTheKey(t *testing.T) {
	p := &conventionstest.Provider{Reply: func(context.Context, int, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{}, fmt.Errorf("401 Incorrect API key provided: %s", secret)
	}}
	e := newJobs(t, goFiles(), nil)
	realClient(t, p, e, 0)
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	s := e.scan()
	if s.Error == nil || strings.Contains(*s.Error, secret) || !strings.Contains(*s.Error, "[REDACTED]") {
		t.Errorf("scan error = %v, want the key replaced by [REDACTED]", s.Error)
	}
	if strings.Contains(e.logs.String(), secret) {
		t.Errorf("log contains the key: %s", e.logs.String())
	}
}

func TestExtract_MissingKeyFailsTheScan(t *testing.T) {
	p := &conventionstest.Provider{}
	e := newJobs(t, goFiles(), nil)
	client := conventionstest.NewClient(t, p, conventionstest.Keys{}, e.logsLogger())
	e.jobs = app.NewJobs(app.JobsDeps{Store: e.store, Repos: memRepos{e.repos}, Files: e.files, LLM: client,
		Features: settings{}, Redact: client}, app.JobsOptions{})
	if err := e.jobs.Extract(t.Context(), payload()); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if s := e.scan(); s.Status != domain.ScanFailed || s.Error == nil || *s.Error != "OPENAI_API_KEY is not configured" {
		t.Errorf("scan = %s %v, want failed with OPENAI_API_KEY is not configured", s.Status, s.Error)
	}
	if len(p.Calls()) != 0 {
		t.Errorf("provider got %d requests, want none", len(p.Calls()))
	}
}

// AC299: a provider that answers after 150 s must not keep the scan running past the 100 s
// request timeout, with the default limits and at most 2 structured requests. The bubble's fake
// clock makes the 150 s instant.
func TestExtract_SlowProviderEndsAtTheRequestTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &conventionstest.Provider{Reply: func(ctx context.Context, _ int, _ llm.Attempt) (llm.AttemptResult, error) {
			select {
			case <-time.After(150 * time.Second):
				return conventionstest.Answer(`{"candidates":[]}`), nil
			case <-ctx.Done():
				return llm.AttemptResult{}, ctx.Err()
			}
		}}
		e := newJobs(t, goFiles(), nil)
		realClient(t, p, e, 0)
		ctx, cancel := context.WithTimeout(context.Background(), app.JobTimeout)
		defer cancel()

		start := time.Now()
		if err := e.jobs.Extract(ctx, payload()); err != nil {
			t.Fatalf("Extract() error = %v", err)
		}
		elapsed := time.Since(start)
		s := e.scan()
		if s.Status == domain.ScanRunning || elapsed >= 130*time.Second {
			t.Errorf("scan = %s after %v, want it ended within 130 s", s.Status, elapsed)
		}
		if s.Status != domain.ScanFailed || s.Error == nil || !strings.Contains(*s.Error, "timed out after 100000ms") {
			t.Errorf("scan = %s %v, want failed by the 100 s request timeout", s.Status, s.Error)
		}
		if n := len(p.Calls()); n > 2 {
			t.Errorf("provider requests = %d, want at most 2", n)
		}
	})
}

// Concurrent scans of different repos do not share state.
func TestExtract_ConcurrentScans(t *testing.T) {
	e := newJobs(t, goFiles(), answer(`{"candidates":[]}`))
	payloads := map[string]json.RawMessage{}
	for i := range 4 {
		id := fmt.Sprintf("scan-c%d", i)
		repo := fmt.Sprintf("repo-c%d", i)
		e.repos[repo] = app.Repo{ID: repo, FullName: "o/" + repo, ClonePath: "/c"}
		e.store.scans = append(e.store.scans, &domain.Scan{ID: id, WorkspaceID: ws, RepoID: repo, Status: domain.ScanRunning})
		payloads[id] = json.RawMessage(fmt.Sprintf(`{"scanId":%q,"workspaceId":%q,"repoId":%q}`, id, ws, repo))
	}
	var wg sync.WaitGroup
	for id, p := range payloads {
		wg.Go(func() {
			if err := e.jobs.Extract(t.Context(), p); err != nil {
				t.Errorf("Extract(%s) error = %v", id, err)
			}
		})
	}
	wg.Wait()
	for id := range payloads {
		e.store.mu.Lock()
		status := e.store.scan(id).Status
		e.store.mu.Unlock()
		if status != domain.ScanDone {
			t.Errorf("scan %s status = %s, want done", id, status)
		}
	}
}
