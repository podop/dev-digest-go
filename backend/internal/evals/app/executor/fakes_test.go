package executor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/evals/app/executor"
	"dev-digest-go/backend/internal/evals/domain"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ---- store ---------------------------------------------------------------------------------

// fakeStore is an in-memory executor.Store that records what the executor does to it.
type fakeStore struct {
	mu sync.Mutex
	// cases are the cases that exist; a missing id is a deleted case.
	cases map[string]domain.Case
	// loadFail, recordFail make a call fail with the error text; recordGone answers the
	// foreign-key violation; finishFail fails the final write.
	loadFail, recordFail map[string]string
	recordGone           map[string]bool
	finishFail           string

	loads, attempts []string
	records         []domain.NewResult
	dropped         int
	finals          []domain.RunFinish
}

var _ executor.Store = (*fakeStore)(nil)

func newFakeStore(cases ...domain.Case) *fakeStore {
	s := &fakeStore{cases: map[string]domain.Case{}}
	for _, c := range cases {
		s.cases[c.ID] = c
	}
	return s
}

func (s *fakeStore) GetCase(_ context.Context, _, caseID string) (domain.Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads = append(s.loads, caseID)
	if msg := s.loadFail[caseID]; msg != "" {
		return domain.Case{}, errors.New(msg)
	}
	c, ok := s.cases[caseID]
	if !ok {
		return domain.Case{}, domain.ErrNotFound
	}
	return c, nil
}

func (s *fakeStore) RecordResult(_ context.Context, _ string, r domain.NewResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, r.CaseID)
	if msg := s.recordFail[r.CaseID]; msg != "" {
		return errors.New(msg)
	}
	if s.recordGone[r.CaseID] {
		return domain.ErrRowGone
	}
	s.records = append(s.records, r)
	return nil
}

func (s *fakeStore) DropRunCase(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropped++
	return nil
}

func (s *fakeStore) FinishRun(_ context.Context, _, _ string, f domain.RunFinish) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finals = append(s.finals, f)
	if s.finishFail != "" {
		return false, errors.New(s.finishFail)
	}
	return true, nil
}

// snapshot returns the recorded state under the lock.
func (s *fakeStore) snapshot() (records []domain.NewResult, finals []domain.RunFinish) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.NewResult(nil), s.records...), append([]domain.RunFinish(nil), s.finals...)
}

// ---- providers and LLM ---------------------------------------------------------------------

// llmFunc is one model call of a case; it reports usage through the binding like the real LLM
// adapter does.
type llmFunc func(ctx context.Context, b executor.LLMBinding, c reviewerapp.Call) (reviewerapp.CallResult, error)

// fakeProviders resolves an LLM that runs llm; it redacts the marker secret.
type fakeProviders struct {
	mu         sync.Mutex
	llm        llmFunc
	resolveErr error
	resolved   []string
}

var _ executor.Providers = (*fakeProviders)(nil)

const secretMarker = "sk-or-SECRET-MARKER"

func (p *fakeProviders) Resolve(_ context.Context, b executor.LLMBinding) (reviewerapp.LLM, error) {
	p.mu.Lock()
	p.resolved = append(p.resolved, b.Provider)
	err := p.resolveErr
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return boundLLM{binding: b, fn: p.llm}, nil
}

func (p *fakeProviders) Redact(s string) string {
	return strings.ReplaceAll(s, secretMarker, "[REDACTED]")
}

type boundLLM struct {
	binding executor.LLMBinding
	fn      llmFunc
}

func (l boundLLM) Review(ctx context.Context, c reviewerapp.Call) (reviewerapp.CallResult, error) {
	return l.fn(ctx, l.binding, c)
}

// emptyReview is a model answer without findings.
func emptyReview() reviewerapp.CallResult {
	return reviewerapp.CallResult{Review: reviewer.Review{Verdict: reviewer.VerdictApprove, Summary: "ok", Score: 100}}
}

var caseIDInPrompt = regexp.MustCompile(`Review pull request(?: #\d+)? "Case (c\d+)"`)

// caseOf is the case id a call reviews, read from the task line of the user message.
func caseOf(c reviewerapp.Call) string {
	for _, m := range c.Messages {
		if m.Role == reviewer.RoleUser {
			if g := caseIDInPrompt.FindStringSubmatch(m.Content); g != nil {
				return g[1]
			}
		}
	}
	return "?"
}

// ---- cases and runs ------------------------------------------------------------------------

const singleDiff = "diff --git a/src/a.ts b/src/a.ts\n--- a/src/a.ts\n+++ b/src/a.ts\n@@ -8,3 +8,5 @@\n const a = 1;\n+const b = 2;\n+const c = 3;\n const d = 4;\n const e = 5;"

func mustFind() domain.Expectation {
	sev, cat, title := "WARNING", "bug", "T"
	return domain.Expectation{Kind: domain.MustFind, File: "src/a.ts", StartLine: 9, EndLine: 10, Severity: &sev, Category: &cat, Title: &title}
}

// evalCase is a case with the standard expectation and diff, titled "Case <id>".
func evalCase(id string) domain.Case {
	return domain.Case{
		ID: id, WorkspaceID: "ws-1", OwnerKind: domain.OwnerAgent, OwnerID: "agent-1", Name: id,
		Expectation: mustFind(), InputDiff: singleDiff, InputMeta: domain.InputMeta{PRTitle: "Case " + id},
	}
}

// evalRun is a running run of the cases.
func evalRun(caseIDs ...string) domain.Run {
	return domain.Run{
		ID: "run-1", WorkspaceID: "ws-1", OwnerKind: domain.OwnerAgent, AgentID: "agent-1", AgentVersion: 3,
		AgentName: "Security Reviewer", Status: domain.RunRunning, CasesTotal: len(caseIDs), CaseIDs: caseIDs,
		Config: domain.Config{Provider: "openrouter", Model: "m", SystemPrompt: "You are a security reviewer.", Strategy: "single-pass"},
	}
}

// ---- logs ----------------------------------------------------------------------------------

// logBuf collects JSON log lines.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuf) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (b *logBuf) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logLine is one decoded log line: level, message and the remaining fields.
type logLine struct {
	Level string
	Msg   string
	Obj   map[string]any
}

func (b *logBuf) lines(t testing.TB) []logLine {
	t.Helper()
	var out []logLine
	for _, raw := range strings.Split(strings.TrimSpace(b.text()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", raw, err)
		}
		l := logLine{Level: strings.ToLower(m["level"].(string)), Msg: m["msg"].(string), Obj: m}
		delete(m, "level")
		delete(m, "msg")
		delete(m, "time")
		out = append(out, l)
	}
	return out
}

// ---- harness -------------------------------------------------------------------------------

// harness is an executor over fakes with the real review engine.
type harness struct {
	t     *testing.T
	store *fakeStore
	prov  *fakeProviders
	logs  *logBuf
	exec  *executor.Executor
}

func newHarness(t *testing.T, store *fakeStore, llm llmFunc, opts executor.Options, clock func() time.Time) *harness {
	t.Helper()
	h := &harness{t: t, store: store, prov: &fakeProviders{llm: llm}, logs: &logBuf{}}
	h.exec = executor.New(executor.Deps{
		Store: store, Providers: h.prov, Engine: reviewerapp.Run, Clock: clock, Log: h.logs.logger(),
	}, opts)
	return h
}
