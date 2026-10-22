package executor

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
	"time"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// ---- run bus -------------------------------------------------------------------------------

type busRun struct {
	events    []domain.RunEvent
	completed bool
	cancelled bool
	next      int
	listeners map[int]func()
}

// fakeBus is an in-memory app.Bus: numbered events, sticky cancel flags, listeners. It has no
// subscribers (the executor never subscribes).
type fakeBus struct {
	mu   sync.Mutex
	runs map[string]*busRun
}

var _ app.Bus = (*fakeBus)(nil)

func newFakeBus() *fakeBus { return &fakeBus{runs: map[string]*busRun{}} }

func (b *fakeBus) run(id string) *busRun {
	r := b.runs[id]
	if r == nil {
		r = &busRun{listeners: map[int]func(){}}
		b.runs[id] = r
	}
	return r
}

func (b *fakeBus) Publish(runID string, kind reviewer.EventKind, msg string, data json.RawMessage) domain.RunEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.run(runID)
	e := domain.RunEvent{RunID: runID, Seq: len(r.events) + 1, Kind: kind, Msg: msg, T: "12:00:00", Data: data}
	r.events = append(r.events, e)
	return e
}

func (b *fakeBus) Subscribe(string) app.Subscription { panic("fakeBus: Subscribe is not used") }

func (b *fakeBus) Has(runID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.runs[runID]
	return ok
}

func (b *fakeBus) Buffer(runID string) []domain.RunEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.runs[runID]; r != nil {
		return append([]domain.RunEvent(nil), r.events...)
	}
	return nil
}

func (b *fakeBus) Complete(runID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.run(runID).completed = true
}

func (b *fakeBus) Cancel(runID string) {
	b.mu.Lock()
	r := b.run(runID)
	r.cancelled = true
	var fns []func()
	for _, fn := range r.listeners {
		fns = append(fns, fn)
	}
	b.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (b *fakeBus) IsCancelled(runID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.runs[runID]
	return r != nil && r.cancelled
}

func (b *fakeBus) OnCancel(runID string, fn func()) func() {
	b.mu.Lock()
	r := b.run(runID)
	if r.cancelled {
		b.mu.Unlock()
		fn()
		return func() {}
	}
	id := len(r.listeners) + 1
	for r.listeners[id] != nil {
		id++
	}
	r.listeners[id] = fn
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(r.listeners, id)
		b.mu.Unlock()
	}
}

func (b *fakeBus) CancelAll() []string {
	b.mu.Lock()
	var ids []string
	for id, r := range b.runs {
		if !r.completed {
			ids = append(ids, id)
		}
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.Cancel(id)
	}
	return ids
}

func (b *fakeBus) CompleteAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.runs {
		r.completed = true
	}
}

func (b *fakeBus) isCompleted(runID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.runs[runID]
	return r != nil && r.completed
}

// ---- store and transactions ------------------------------------------------------------------

type runRec struct {
	status     domain.RunStatus
	completion *domain.RunCompletion
	skills     []domain.ReviewSkill
	trace      *domain.Trace
	ended      int
}

type storedFinding struct {
	review   string
	findings []reviewer.Finding
	skillIDs map[string]string
}

// fakeStore is an in-memory app.Store for what the executor uses. Writes made through a ctx
// from fakeTx are buffered and applied only when the transaction commits. The methods the
// executor never calls are promoted from the nil embedded interface and would panic.
type fakeStore struct {
	app.Store

	mu       sync.Mutex
	nextID   int
	runs     map[string]*runRec
	reviews  []domain.NewReview
	findings []storedFinding
	reviewed map[string]string

	files          []reviewer.PatchFile
	filesErr       error
	filesGate      chan struct{}
	filesPanic     bool
	filesCalls     int
	skills         map[string][]domain.ReviewSkill
	skillsErr      error
	insertReviewEr error
	// beforeComplete runs inside CompleteRunIfRunning before the status check.
	beforeComplete func(runID string)
}

func newFakeStore() *fakeStore {
	return &fakeStore{runs: map[string]*runRec{}, reviewed: map[string]string{}, skills: map[string][]domain.ReviewSkill{}}
}

type (
	txKey struct{}
	txBuf struct{ ops []func() }
)

func (s *fakeStore) write(ctx context.Context, f func()) {
	if b, ok := ctx.Value(txKey{}).(*txBuf); ok {
		b.ops = append(b.ops, f)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

func (s *fakeStore) addRun(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[id] = &runRec{status: domain.StatusRunning}
}

func (s *fakeStore) deleteRun(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)
}

func (s *fakeStore) run(id string) runRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.runs[id]; r != nil {
		return *r
	}
	return runRec{}
}

func (s *fakeStore) PullFiles(ctx context.Context, _ string) ([]reviewer.PatchFile, error) {
	s.mu.Lock()
	s.filesCalls++
	gate, panics, err, files := s.filesGate, s.filesPanic, s.filesErr, s.files
	s.mu.Unlock()
	if panics {
		panic("boom in PullFiles")
	}
	if gate != nil {
		<-gate
	}
	return files, err
}

func (s *fakeStore) EnabledSkills(_ context.Context, agentID string) ([]domain.ReviewSkill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skills[agentID], s.skillsErr
}

func (s *fakeStore) RecordRunSkills(_ context.Context, runID string, skills []domain.ReviewSkill) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil {
		return domain.ErrNotFound
	}
	r.skills = skills
	return nil
}

func (s *fakeStore) InsertReview(ctx context.Context, n domain.NewReview) (string, error) {
	s.mu.Lock()
	if s.insertReviewEr != nil {
		defer s.mu.Unlock()
		return "", s.insertReviewEr
	}
	s.nextID++
	id := fmt.Sprintf("rev-%d", s.nextID)
	s.mu.Unlock()
	s.write(ctx, func() { s.reviews = append(s.reviews, n) })
	return id, nil
}

func (s *fakeStore) InsertFindings(ctx context.Context, reviewID string, f []reviewer.Finding, skillIDs map[string]string) error {
	s.write(ctx, func() { s.findings = append(s.findings, storedFinding{reviewID, f, skillIDs}) })
	return nil
}

func (s *fakeStore) MarkReviewed(ctx context.Context, prID, sha string) error {
	s.write(ctx, func() { s.reviewed[prID] = sha })
	return nil
}

func (s *fakeStore) CompleteRunIfRunning(ctx context.Context, runID string, c domain.RunCompletion) (bool, error) {
	if s.beforeComplete != nil {
		s.beforeComplete(runID)
	}
	s.mu.Lock()
	r := s.runs[runID]
	running := r != nil && r.status == domain.StatusRunning
	s.mu.Unlock()
	if r == nil {
		return false, domain.ErrNotFound
	}
	if !running {
		return false, nil
	}
	s.write(ctx, func() {
		r.status = domain.StatusDone
		r.completion = &c
	})
	return true, nil
}

// EndRun follows the port: the record is always written; the status moves from running only.
func (s *fakeStore) EndRun(_ context.Context, runID string, status domain.RunStatus, c domain.RunCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil {
		return domain.ErrNotFound
	}
	r.ended++
	if r.status == domain.StatusDone {
		return nil
	}
	rec := c
	r.completion = &rec
	if r.status == domain.StatusRunning {
		r.status = status
	}
	return nil
}

func (s *fakeStore) SaveTrace(_ context.Context, runID string, t domain.Trace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil {
		return domain.ErrNotFound
	}
	r.trace = &t
	return nil
}

// fakeTx runs fn with a write buffer and applies the buffer to the store only when fn succeeds:
// an error rolls everything back.
type fakeTx struct {
	store   *fakeStore
	mu      sync.Mutex
	commits int
	aborts  int
}

func (t *fakeTx) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	buf := &txBuf{}
	if err := fn(context.WithValue(ctx, txKey{}, buf)); err != nil {
		t.mu.Lock()
		t.aborts++
		t.mu.Unlock()
		return err
	}
	t.store.mu.Lock()
	for _, op := range buf.ops {
		op()
	}
	t.store.mu.Unlock()
	t.mu.Lock()
	t.commits++
	t.mu.Unlock()
	return nil
}

// ---- providers, repo intel, project context, engine -------------------------------------------

type fakeLLM struct{ binding app.LLMBinding }

func (fakeLLM) Review(context.Context, reviewerapp.Call) (reviewerapp.CallResult, error) {
	return reviewerapp.CallResult{}, errors.New("fakeLLM.Review is not used")
}

type fakeProviders struct {
	mu       sync.Mutex
	missing  map[string]string // provider -> Resolve error text
	bindings map[string]app.LLMBinding
}

func newFakeProviders() *fakeProviders {
	return &fakeProviders{missing: map[string]string{}, bindings: map[string]app.LLMBinding{}}
}

func (p *fakeProviders) Resolve(_ context.Context, b app.LLMBinding) (reviewerapp.LLM, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if msg, ok := p.missing[b.Provider]; ok {
		return nil, errors.New(msg)
	}
	p.bindings[b.RunID] = b
	return fakeLLM{binding: b}, nil
}

func (p *fakeProviders) Redact(s string) string {
	return strings.ReplaceAll(s, "sk-secret", "[redacted]")
}

func (p *fakeProviders) binding(runID string) app.LLMBinding {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bindings[runID]
}

type fakeRepoIntel struct {
	mu        sync.Mutex
	callers   []domain.CallerSignature
	callersEr error
	repoMap   domain.RepoMap
	repoMapEr error
	ranks     []domain.PathRank
	ranksEr   error
	calls     int
}

func (r *fakeRepoIntel) CallerSignatures(context.Context, string, []string, int) ([]domain.CallerSignature, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.callers, r.callersEr
}

func (r *fakeRepoIntel) RepoMap(context.Context, string, int) (domain.RepoMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.repoMap, r.repoMapEr
}

func (r *fakeRepoIntel) FileRank(context.Context, string, []string) ([]domain.PathRank, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.ranks, r.ranksEr
}

type fakeProjectContext struct {
	mu     sync.Mutex
	result app.ProjectContextResult
	err    error
	inputs []app.ProjectContextInput
}

func (p *fakeProjectContext) ResolveForRun(_ context.Context, in app.ProjectContextInput) (app.ProjectContextResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs = append(p.inputs, in)
	return p.result, p.err
}

// fakeEngine scripts reviewer/app.Run. The default behaviour is a successful one-pass review; it
// honours Options.Cancelled before its (counted) chunk call, as the real engine does.
type fakeEngine struct {
	mu     sync.Mutex
	script func(ctx context.Context, in reviewerapp.Input, llm reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error)
	inputs []reviewerapp.Input
	opts   []reviewerapp.Options
	// chunkCalls counts the LLM requests the engine would have made.
	chunkCalls int
}

func (f *fakeEngine) run(ctx context.Context, in reviewerapp.Input, llm reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.opts = append(f.opts, opts)
	script := f.script
	f.mu.Unlock()
	if script != nil {
		return script(ctx, in, llm, ev, opts)
	}
	return f.chunk(ev, opts, defaultOutcome())
}

// chunk is the default one-call review.
func (f *fakeEngine) chunk(ev reviewerapp.Events, opts reviewerapp.Options, out reviewer.Outcome) (reviewer.Outcome, error) {
	if opts.Cancelled != nil && opts.Cancelled() {
		return reviewer.Outcome{}, reviewerapp.ErrCancelled
	}
	f.mu.Lock()
	f.chunkCalls++
	f.mu.Unlock()
	ev.Emit(reviewer.EventInfo, "Reviewing 1 changed file(s) in one pass", nil)
	return out, nil
}

func (f *fakeEngine) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chunkCalls
}

func defaultOutcome() reviewer.Outcome {
	cost := 0.00042
	skill := "pr-quality"
	return reviewer.Outcome{
		Review: reviewer.Review{
			Verdict: reviewer.VerdictComment,
			Summary: "ok",
			Score:   85,
			Findings: []reviewer.Finding{
				{ID: "mock-1", Severity: reviewer.SeverityWarning, Category: reviewer.CategorySecurity, Title: "t1", File: "a.go", StartLine: 2, EndLine: 2, Skill: &skill},
				{ID: "mock-2", Severity: reviewer.SeveritySuggestion, Category: reviewer.CategoryPerf, Title: "t2", File: "a.go", StartLine: 1, EndLine: 1},
			},
		},
		Grounding:   "2/2 passed",
		Mode:        reviewer.ModeSinglePass,
		Assembly:    reviewer.Assembly{System: "sys+guard", User: "user"},
		ChunkLabels: []string{"all files"},
		Usage:       reviewer.Usage{TokensIn: 1200, TokensOut: 180, CostUSD: &cost},
		Raw:         "raw output",
	}
}

// ---- log capture -----------------------------------------------------------------------------

type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// records returns the captured log lines as maps (level, msg, attributes).
func (l *logCapture) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(l.text()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func (l *logCapture) atLevel(t *testing.T, level string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range l.records(t) {
		if m["level"] == level {
			out = append(out, m)
		}
	}
	return out
}

// ---- harness ----------------------------------------------------------------------------------

type harness struct {
	store  *fakeStore
	tx     *fakeTx
	bus    *fakeBus
	prov   *fakeProviders
	intel  *fakeRepoIntel
	projc  *fakeProjectContext
	engine *fakeEngine
	logs   *logCapture
	exec   *Executor
}

func fixedClock() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }

func newHarness(t *testing.T, opts Options, clock app.Clock) *harness {
	t.Helper()
	store := newFakeStore()
	h := &harness{
		store: store, tx: &fakeTx{store: store}, bus: newFakeBus(), prov: newFakeProviders(),
		intel: &fakeRepoIntel{}, projc: &fakeProjectContext{}, engine: &fakeEngine{}, logs: &logCapture{},
	}
	h.store.files = []reviewer.PatchFile{{Path: "a.go", Patch: "@@ -1,1 +1,2 @@\n x\n+y"}}
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.exec = New(Deps{
		Store: h.store, Tx: h.tx, Bus: h.bus, Providers: h.prov, RepoIntel: h.intel,
		ProjectContext: h.projc, Engine: h.engine.run, Clock: clock, Log: log,
	}, opts)
	return h
}

func testAgent(n int) domain.ReviewAgent {
	return domain.ReviewAgent{
		ID: fmt.Sprintf("agent-%d", n), Name: fmt.Sprintf("Agent%d", n), Provider: "openai", Model: "gpt-x",
		SystemPrompt: "be careful", Strategy: reviewer.StrategyAuto, CIFailOn: reviewer.FailOnWarning,
		RepoIntel: true, Version: 7,
	}
}

// batch builds a batch of n jobs (run ids run-1..run-n) and registers the rows in the store.
func (h *harness) batch(n int) domain.Batch {
	b := domain.Batch{
		WorkspaceID: "ws-1",
		Pull:        domain.ReviewPull{ID: "pr-1", WorkspaceID: "ws-1", RepoID: "repo-1", Number: 482, Title: "Add limiter", Author: "ann", Base: "main", HeadSHA: "a1b2c3d4e5f6"},
		Repo:        domain.ReviewRepo{ID: "repo-1", Owner: "acme", Name: "api"},
	}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("run-%d", i)
		h.store.addRun(id)
		b.Jobs = append(b.Jobs, domain.Job{Agent: testAgent(i), RunID: id})
	}
	return b
}

// wait blocks until every started batch has finished.
func (h *harness) wait() { h.exec.wg.Wait() }

type ev struct {
	Kind reviewer.EventKind
	Msg  string
}

func (h *harness) events(runID string) []ev {
	var out []ev
	for _, e := range h.bus.Buffer(runID) {
		out = append(out, ev{e.Kind, e.Msg})
	}
	return out
}

func (h *harness) hasEvent(runID string, kind reviewer.EventKind, msg string) bool {
	for _, e := range h.events(runID) {
		if e.Kind == kind && e.Msg == msg {
			return true
		}
	}
	return false
}
