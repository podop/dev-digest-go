package parity_test

// S25 (AC4041, NFR4005): the harness that drives the REAL review run executor (executor.New) with the
// real engine (reviewerapp.Run), the real run bus (runbus behind peers.NewBus), the real LLM path
// (peers.NewProviders -> llmcall -> *llm.Client) and the real service and SSE routes, over in-memory
// fakes of the store, the transaction runner, repo intel and project context. The fakes record
// every port call in the shape of the Node recording (runs.json `calls`), so that the comparison
// is a plain diff. Nothing here asserts anything; runs_test.go does.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/platform/runbus"
	pcdomain "dev-digest-go/backend/internal/projectcontext/domain"
	"dev-digest-go/backend/internal/reviewer/adapters/llmcall"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/adapters/peers"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/app/executor"
	"dev-digest-go/backend/internal/reviews/domain"
	reviewshttp "dev-digest-go/backend/internal/reviews/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

// reviewID is the id of the persisted review in the Node recording; finding ids are not stored.
const reviewID = "11111111-1111-4111-8111-111111111111"

// ---- golden shapes -----------------------------------------------------------------------------

type runsGolden struct {
	Scenarios []runScenario `json:"scenarios"`
}

type runEvent struct {
	Seq  int             `json:"seq"`
	Kind string          `json:"kind"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type runLLMCall struct {
	ChunkIndex  int             `json:"chunkIndex"`
	SchemaName  string          `json:"schemaName"`
	Model       string          `json:"model"`
	Temperature *float64        `json:"temperature"`
	MaxRetries  int             `json:"maxRetries"`
	SessionID   *string         `json:"sessionId"`
	Messages    []goldenMessage `json:"messages"`
}

type scriptUsage struct {
	TokensIn  int      `json:"tokensIn"`
	TokensOut int      `json:"tokensOut"`
	CostUSD   *float64 `json:"costUsd"`
}

type scriptStep struct {
	Raw   string        `json:"raw"`
	Error *string       `json:"error"`
	Usage []scriptUsage `json:"usage"`
}

type runLLM struct {
	Mode         string                `json:"mode"`
	Default      *scriptStep           `json:"default"`
	Chunks       map[string]scriptStep `json:"chunks"`
	ResolveError string                `json:"resolveError"`
	DelayMS      int                   `json:"delayMs"`
	Responses    []struct {
		Content string          `json:"content"`
		Usage   json.RawMessage `json:"usage"`
	} `json:"responses"`
}

type runAgent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	SystemPrompt string `json:"systemPrompt"`
	Strategy     string `json:"strategy"`
	CIFailOn     string `json:"ciFailOn"`
	RepoIntel    bool   `json:"repoIntel"`
	Version      int    `json:"version"`
}

type runSkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Version     int    `json:"version"`
}

type runContextDoc struct {
	Path    string `json:"path"`
	DocType string `json:"doc_type"`
	Origin  struct {
		Kind      string `json:"kind"`
		SkillID   string `json:"skill_id"`
		SkillName string `json:"skill_name"`
	} `json:"origin"`
	Tokens int     `json:"tokens"`
	Status string  `json:"status"`
	Source string  `json:"source"`
	Text   *string `json:"text"`
}

type runProjectContext struct {
	Warn        string          `json:"warn"`
	Docs        []runContextDoc `json:"docs"`
	TokensTotal int             `json:"tokensTotal"`
}

type runInput struct {
	WorkspaceID string `json:"workspaceId"`
	Pull        struct {
		ID          string  `json:"id"`
		WorkspaceID string  `json:"workspaceId"`
		RepoID      string  `json:"repoId"`
		Number      int     `json:"number"`
		Title       string  `json:"title"`
		Author      string  `json:"author"`
		Branch      string  `json:"branch"`
		Base        string  `json:"base"`
		HeadSHA     string  `json:"headSha"`
		Body        *string `json:"body"`
	} `json:"pull"`
	Repo struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repo"`
	PRFiles []struct {
		Path  string `json:"path"`
		Patch text   `json:"patch"`
	} `json:"prFiles"`
	Jobs []struct {
		RunID string   `json:"runId"`
		Agent runAgent `json:"agent"`
	} `json:"jobs"`
	Skills         map[string][]runSkill        `json:"skills"`
	ProjectContext map[string]runProjectContext `json:"projectContext"`
	RepoIntel      struct {
		Callers json.RawMessage `json:"callers"`
		RepoMap json.RawMessage `json:"repoMap"`
		Rank    json.RawMessage `json:"rank"`
	} `json:"repoIntel"`
	LLM            runLLM `json:"llm"`
	MapConcurrency *int   `json:"mapConcurrency"`
	Actions        []struct {
		At     string `json:"at"`
		Cancel string `json:"cancel"`
	} `json:"actions"`
	Persist struct {
		CompleteReturns *bool `json:"completeReturns"`
	} `json:"persist"`
	StoredDiffError *string           `json:"storedDiffError"`
	SkillsError     map[string]string `json:"skillsError"`
}

type runScenario struct {
	Name            string                `json:"name"`
	Input           runInput              `json:"input"`
	Events          map[string][]runEvent `json:"events"`
	BusComplete     map[string]bool       `json:"busComplete"`
	Calls           []map[string]any      `json:"calls"`
	FinalRuns       map[string]any        `json:"finalRuns"`
	Traces          map[string]any        `json:"traces"`
	LLMCalls        []runLLMCall          `json:"llmCalls"`
	PromptLog       []map[string]any      `json:"promptLog"`
	LLMHTTPRequests []map[string]any      `json:"llmHttpRequests"`
}

// loadRunRefs maps each run id of the corpus to the agent key (sec, gen, mr) the scenario's skills,
// project context and skill errors are keyed by (corpus/runs/cases.json; the goldens do not repeat it).
func loadRunRefs(t testing.TB) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "corpus", "runs", "cases.json"))
	if err != nil {
		t.Fatalf("read the runs corpus: %v", err)
	}
	var c struct {
		Cases []struct {
			Agents []struct {
				Ref   string `json:"ref"`
				RunID string `json:"runId"`
			} `json:"agents"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("decode the runs corpus: %v", err)
	}
	refs := map[string]string{}
	for _, cs := range c.Cases {
		for _, a := range cs.Agents {
			refs[a.RunID] = a.Ref
		}
	}
	return refs
}

// ---- logical clock ---------------------------------------------------------------------------

// logicalClock is the Node recording's clock: it starts at 2026-01-01T12:00:00Z and only the fake
// ports move it (README, "Timings are not parity data"); here it is only kept plausible, every
// comparison ignores durations and stamps.
type logicalClock struct{ ms atomic.Int64 }

var clockZero = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func (c *logicalClock) Now() time.Time {
	return clockZero.Add(time.Duration(c.ms.Load()) * time.Millisecond)
}
func (c *logicalClock) bump(ms int) { c.ms.Add(int64(ms)) }

// syncBuf is a concurrency-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) lines() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Split(bytes.TrimSpace(bytes.Clone(s.b.Bytes())), []byte("\n"))
}

// ---- the harness -----------------------------------------------------------------------------

type harnessOptions struct {
	// agentConcurrency bounds the agent runs in flight; 1 reproduces Node's sequential agents.
	agentConcurrency int
}

type runHarness struct {
	t     *testing.T
	sc    *runScenario
	opts  harnessOptions
	clock *logicalClock

	refOfRun   map[string]string // run id -> agent key
	refOfAgent map[string]string // agent id -> agent key

	rawBus *runbus.Bus
	bus    *peers.Bus
	store  *fakeRunStore
	tx     *fakeRunTx
	client *recordingClient
	exec   *executor.Executor
	svc    *reviewsapp.Service

	execLogs   *syncBuf
	clientLogs *syncBuf

	mu    sync.Mutex
	calls []map[string]any

	srvOnce sync.Once
	srv     *httptest.Server
}

func newRunHarness(t *testing.T, sc *runScenario, refs map[string]string, opts harnessOptions) *runHarness {
	t.Helper()
	h := &runHarness{
		t: t, sc: sc, opts: opts, clock: &logicalClock{},
		refOfRun: map[string]string{}, refOfAgent: map[string]string{},
		execLogs: &syncBuf{}, clientLogs: &syncBuf{},
	}
	for _, j := range sc.Input.Jobs {
		ref, ok := refs[j.RunID]
		if !ok {
			t.Fatalf("run %s of scenario %s is not in the runs corpus", j.RunID, sc.Name)
		}
		h.refOfRun[j.RunID] = ref
		h.refOfAgent[j.Agent.ID] = ref
	}

	// The Node bus is built with a no-op scheduler (no TTL eviction); the same here.
	h.rawBus = runbus.New(runbus.Options{Now: h.clock.Now, AfterFunc: func(time.Duration, func()) {}})
	h.bus = peers.NewBus(h.rawBus)
	h.store = newFakeRunStore(h)
	h.tx = &fakeRunTx{h: h}
	h.client = newRecordingClient(h)

	h.exec = executor.New(executor.Deps{
		Store: h.store, Tx: h.tx, Bus: h.bus,
		Providers: peers.NewProviders(h.client, func(c peers.CallerConfig) reviewerapp.LLM {
			return llmcall.New(llmcall.Config{Client: h.client, Provider: c.Provider, Model: c.Model, RunID: c.RunID, SessionID: c.SessionID, OnUsage: c.OnUsage})
		}),
		RepoIntel:      &fakeRepoIntel{h: h},
		ProjectContext: &fakeProjectContext{h: h},
		Engine:         reviewerapp.Run,
		Clock:          h.clock.Now,
		Log:            slog.New(slog.NewJSONHandler(h.execLogs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, executor.Options{AgentConcurrency: opts.agentConcurrency, MapConcurrency: valueOr(sc.Input.MapConcurrency, 0)})
	h.svc = reviewsapp.NewService(reviewsapp.Deps{Store: h.store, Tx: h.tx, Executor: h.exec, Bus: h.bus, Now: h.clock.Now})

	for _, j := range sc.Input.Jobs {
		h.store.initRun(j.RunID)
	}
	return h
}

func valueOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// record appends a port call in the shape of the Node recording.
func (h *runHarness) record(port string, args any, extra map[string]any) {
	call := map[string]any{"port": port}
	if args != nil {
		call["args"] = args
	}
	for k, v := range extra {
		call[k] = v
	}
	h.mu.Lock()
	h.calls = append(h.calls, call)
	h.mu.Unlock()
}

func (h *runHarness) recordedCalls() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.calls...)
}

// batch is the request the executor is started with.
func (h *runHarness) batch() domain.Batch {
	in := &h.sc.Input
	b := domain.Batch{
		WorkspaceID: in.WorkspaceID,
		Pull: domain.ReviewPull{
			ID: in.Pull.ID, WorkspaceID: in.Pull.WorkspaceID, RepoID: in.Pull.RepoID, Number: in.Pull.Number,
			Title: in.Pull.Title, Author: in.Pull.Author, Base: in.Pull.Base, HeadSHA: in.Pull.HeadSHA, Body: in.Pull.Body,
		},
		Repo: domain.ReviewRepo{ID: in.Repo.ID, Owner: in.Repo.Owner, Name: in.Repo.Name},
	}
	for _, j := range in.Jobs {
		b.Jobs = append(b.Jobs, domain.Job{RunID: j.RunID, Agent: domain.ReviewAgent{
			ID: j.Agent.ID, Name: j.Agent.Name, Provider: j.Agent.Provider, Model: j.Agent.Model,
			SystemPrompt: j.Agent.SystemPrompt, Strategy: reviewer.Strategy(j.Agent.Strategy),
			CIFailOn: reviewer.FailOn(j.Agent.CIFailOn), RepoIntel: j.Agent.RepoIntel, Version: j.Agent.Version,
		}})
	}
	return b
}

// start runs the batch in the background (executor.Start returns at once).
func (h *runHarness) start() { h.exec.Start(h.t.Context(), h.batch()) }

// waitDone blocks until every run recorded itself (its trace was saved), its bus completed, and the
// executor's goroutines ended.
func (h *runHarness) waitDone() {
	h.t.Helper()
	deadline := time.After(30 * time.Second)
	for _, j := range h.sc.Input.Jobs {
		select {
		case <-h.store.traceSaved(j.RunID):
		case <-deadline:
			h.t.Fatalf("run %s did not save its trace within 30s", j.RunID)
		}
	}
	for _, j := range h.sc.Input.Jobs {
		if !h.busCompleted(j.RunID, 10*time.Second) {
			h.t.Fatalf("the bus of run %s did not complete", j.RunID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.exec.Shutdown(ctx); err != nil {
		h.t.Fatalf("Shutdown() = %v, want every run drained", err)
	}
}

// busCompleted reports whether the run's subscription ends (the bus completed the run) in time.
func (h *runHarness) busCompleted(runID string, within time.Duration) bool {
	sub := h.bus.Subscribe(runID)
	defer sub.Close()
	timer := time.NewTimer(within)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-sub.C():
			if !ok {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

// server serves the review routes of the real service over the real run bus.
func (h *runHarness) server() *httptest.Server {
	h.srvOnce.Do(func() {
		e := httpx.New(httpx.Options{
			Log: slog.New(slog.DiscardHandler), CORSOrigins: []string{"http://localhost:3100"},
			AllowedHosts: []string{"127.0.0.1"},
		})
		reviewshttp.AddRoutes(e, h.svc, fixedWorkspace{id: h.sc.Input.WorkspaceID})
		h.srv = httptest.NewServer(e)
		h.t.Cleanup(h.srv.Close)
	})
	return h.srv
}

type fixedWorkspace struct{ id string }

func (f fixedWorkspace) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: f.id}, nil
}

// fire runs the cancel actions scheduled at the given point (the cancel route of the service).
func (h *runHarness) fire(ctx context.Context, at string) {
	for _, a := range h.sc.Input.Actions {
		if a.At != at {
			continue
		}
		h.record("action.cancel", []any{a.Cancel}, map[string]any{"at": at})
		if err := h.svc.CancelRun(ctx, h.sc.Input.WorkspaceID, a.Cancel); err != nil {
			h.t.Errorf("CancelRun(%s) at %s = %v", a.Cancel, at, err)
		}
	}
}

// hasAction reports whether an action is scheduled at the given point.
func (h *runHarness) hasAction(at string) bool {
	for _, a := range h.sc.Input.Actions {
		if a.At == at {
			return true
		}
	}
	return false
}

// ---- fake store and transactions ---------------------------------------------------------------

type runRecord struct {
	status                string
	err                   *string
	statsWritten          bool
	durationMS            int
	tokensIn, tokensOut   int
	costUSD               *float64
	findings              int
	grounding             string
	score, blockers       *int
	trace                 *domain.Trace
	traceSavedCh          chan struct{}
	traceSavedAlreadyOnce sync.Once
}

// fakeRunStore is an in-memory reviewsapp.Store for what the executor and the service use. Writes
// made through a ctx from fakeRunTx are buffered and applied only when the transaction commits. The
// methods nothing here calls are promoted from the nil embedded interface and would panic.
type fakeRunStore struct {
	reviewsapp.Store
	h    *runHarness
	mu   sync.Mutex
	runs map[string]*runRecord
}

var _ reviewsapp.Store = (*fakeRunStore)(nil)

func newFakeRunStore(h *runHarness) *fakeRunStore {
	return &fakeRunStore{h: h, runs: map[string]*runRecord{}}
}

func (s *fakeRunStore) initRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[runID] = &runRecord{status: "running", traceSavedCh: make(chan struct{})}
}

func (s *fakeRunStore) traceSaved(runID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[runID].traceSavedCh
}

// trace returns the last trace saved for the run.
func (s *fakeRunStore) trace(runID string) (domain.Trace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil || r.trace == nil {
		return domain.Trace{}, false
	}
	return *r.trace, true
}

// finalRun is the agent_runs row as the Node recording shows it (finalRuns), by the transition rules
// of handwritten/run_status_transitions.json.
func (s *fakeRunStore) finalRun(runID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	return map[string]any{
		"status": r.status, "error": r.err, "durationMs": r.durationMS, "tokensIn": r.tokensIn,
		"tokensOut": r.tokensOut, "costUsd": r.costUSD, "findingsCount": r.findings, "grounding": r.grounding,
		"score": r.score, "blockers": r.blockers,
	}
}

func (s *fakeRunStore) PullFiles(ctx context.Context, prID string) ([]reviewer.PatchFile, error) {
	h := s.h
	h.record("store.storedDiff", []any{prID}, nil)
	h.clock.bump(20)
	h.fire(ctx, "diffLoad")
	if h.sc.Input.StoredDiffError != nil {
		return nil, errors.New(*h.sc.Input.StoredDiffError)
	}
	files := make([]reviewer.PatchFile, 0, len(h.sc.Input.PRFiles))
	for _, f := range h.sc.Input.PRFiles {
		if f.Patch.b != nil {
			h.t.Errorf("scenario %s: a blob patch needs a synth spec", h.sc.Name)
			return nil, errors.New("blob patch")
		}
		files = append(files, reviewer.PatchFile{Path: f.Path, Patch: f.Patch.s})
	}
	return files, nil
}

func (s *fakeRunStore) EnabledSkills(_ context.Context, agentID string) ([]domain.ReviewSkill, error) {
	h := s.h
	h.record("skills.enabledForAgent", []any{agentID}, nil)
	h.clock.bump(2)
	ref := h.refOfAgent[agentID]
	if msg, ok := h.sc.Input.SkillsError[ref]; ok {
		return nil, errors.New(msg)
	}
	var out []domain.ReviewSkill
	for _, k := range h.sc.Input.Skills[ref] {
		out = append(out, domain.ReviewSkill{ID: k.ID, Name: k.Name, Description: k.Description, Body: k.Body, Version: k.Version})
	}
	return out, nil
}

func (s *fakeRunStore) RecordRunSkills(_ context.Context, runID string, skills []domain.ReviewSkill) error {
	rows := make([]any, len(skills))
	for i, k := range skills {
		rows[i] = map[string]any{"runId": runID, "skillId": k.ID, "skillVersion": k.Version, "order": i}
	}
	s.h.record("store.recordRunSkills", []any{runID}, map[string]any{"statements": []any{
		map[string]any{"kind": "insert", "table": "agent_run_skills", "values": rows, "onConflict": "nothing"},
	}})
	s.h.clock.bump(5)
	return nil
}

func (s *fakeRunStore) FindRun(_ context.Context, _, runID string) (domain.RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil {
		return domain.RunState{}, domain.ErrNotFound
	}
	return domain.RunState{ID: runID, Status: domain.RunStatus(r.status), Error: r.err}, nil
}

func (s *fakeRunStore) CancelIfRunning(_ context.Context, _, runID, note string) (bool, error) {
	s.h.record("store.cancelRunIfRunning", []any{runID}, nil)
	s.h.clock.bump(5)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil || r.status != "running" {
		return false, nil
	}
	r.status = "cancelled"
	r.err = &note
	return true, nil
}

// EndRun follows handwritten/run_status_transitions.json `failAgentRun`: from running the status and
// error move to the given ones; from cancelled the status stays and the error defaults to the cancel
// note; a done or failed run is untouched. The stats are written whenever the row was touched.
func (s *fakeRunStore) EndRun(_ context.Context, runID string, status domain.RunStatus, c domain.RunCompletion) error {
	values := map[string]any{
		"durationMs": c.DurationMS, "tokensIn": c.TokensIn, "tokensOut": c.TokensOut, "costUsd": c.CostUSD,
		"findingsCount": c.FindingsCount, "grounding": c.Grounding, "error": c.Error,
	}
	s.h.record("store.failAgentRun", []any{runID, string(status), values}, nil)
	s.h.clock.bump(5)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil {
		return domain.ErrNotFound
	}
	if r.status != "running" && r.status != "cancelled" {
		return nil
	}
	r.durationMS, r.tokensIn, r.tokensOut, r.costUSD = c.DurationMS, c.TokensIn, c.TokensOut, c.CostUSD
	r.findings, r.grounding, r.score, r.blockers, r.statsWritten = c.FindingsCount, c.Grounding, nil, nil, true
	if r.status == "running" {
		r.status, r.err = string(status), c.Error
		return nil
	}
	if r.err == nil {
		note := domain.CancelledNote
		r.err = &note
	}
	return nil
}

func (s *fakeRunStore) SaveTrace(_ context.Context, runID string, t domain.Trace) error {
	s.h.record("store.saveRunTrace", []any{runID}, nil)
	s.h.clock.bump(5)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil {
		return domain.ErrNotFound
	}
	r.trace = &t
	r.traceSavedAlreadyOnce.Do(func() { close(r.traceSavedCh) })
	return nil
}

// The writes of the persist transaction.

func (s *fakeRunStore) txOf(ctx context.Context) *txRecord {
	tx, _ := ctx.Value(txKey{}).(*txRecord)
	if tx == nil {
		s.h.t.Errorf("a persist write ran outside a transaction")
		return &txRecord{}
	}
	return tx
}

func (s *fakeRunStore) InsertReview(ctx context.Context, r domain.NewReview) (string, error) {
	s.txOf(ctx).add(map[string]any{"kind": "insert", "table": "reviews", "returning": true, "values": map[string]any{
		"workspaceId": r.WorkspaceID, "prId": r.PRID, "agentId": r.AgentID, "runId": r.RunID, "kind": "review",
		"verdict": string(r.Verdict), "summary": r.Summary, "score": r.Score, "model": r.Model,
	}})
	return reviewID, nil
}

// InsertFindings records the rows by the mapping the port documents (spec 7.3): kind defaults to
// finding, out_of_scope is false, skill_id is the run's skill with the cited name.
func (s *fakeRunStore) InsertFindings(ctx context.Context, rid string, findings []reviewer.Finding, skillIDs map[string]string) error {
	if len(findings) == 0 {
		return nil
	}
	rows := make([]any, len(findings))
	for i, f := range findings {
		kind := string(f.Kind)
		if kind == "" {
			kind = string(reviewer.KindFinding)
		}
		var skillID any
		if f.Skill != nil && skillIDs[*f.Skill] != "" {
			skillID = skillIDs[*f.Skill]
		}
		var trifecta any
		if f.TrifectaComponents != nil {
			trifecta = f.TrifectaComponents
		}
		rows[i] = map[string]any{
			"reviewId": rid, "file": f.File, "startLine": f.StartLine, "endLine": f.EndLine,
			"severity": string(f.Severity), "category": string(f.Category), "title": f.Title,
			"rationale": f.Rationale, "suggestion": f.Suggestion, "confidence": f.Confidence, "kind": kind,
			"trifectaComponents": trifecta, "skillName": f.Skill, "skillId": skillID, "outOfScope": false,
		}
	}
	s.txOf(ctx).add(map[string]any{"kind": "insert", "table": "findings", "returning": true, "values": rows})
	return nil
}

func (s *fakeRunStore) MarkReviewed(ctx context.Context, _, sha string) error {
	s.txOf(ctx).add(map[string]any{"kind": "update", "table": "pull_requests", "set": map[string]any{"lastReviewedSha": sha}})
	return nil
}

// CompleteRunIfRunning follows `completeAgentRunIfRunning` of the transitions table; the statement is
// recorded whatever it returns. persist.completeReturns=false makes a cancel land first.
func (s *fakeRunStore) CompleteRunIfRunning(ctx context.Context, runID string, c domain.RunCompletion) (bool, error) {
	tx := s.txOf(ctx)
	tx.add(map[string]any{"kind": "update", "table": "agent_runs", "returning": true, "set": map[string]any{
		"status": "done", "durationMs": c.DurationMS, "tokensIn": c.TokensIn, "tokensOut": c.TokensOut,
		"costUsd": c.CostUSD, "findingsCount": c.FindingsCount, "grounding": c.Grounding, "score": c.Score,
		"blockers": c.Blockers, "error": c.Error,
	}})
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil {
		return false, domain.ErrNotFound
	}
	if rc := s.h.sc.Input.Persist.CompleteReturns; rc != nil && !*rc {
		note := domain.CancelledNote
		r.status, r.err = "cancelled", &note
	}
	if r.status != "running" {
		return false, nil
	}
	tx.onCommit(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		r.status, r.err = "done", c.Error
		r.durationMS, r.tokensIn, r.tokensOut, r.costUSD = c.DurationMS, c.TokensIn, c.TokensOut, c.CostUSD
		r.findings, r.grounding, r.score, r.blockers, r.statsWritten = c.FindingsCount, c.Grounding, c.Score, c.Blockers, true
	})
	return true, nil
}

type txKey struct{}

// txRecord collects the statements of one transaction and the state changes applied on commit.
type txRecord struct {
	stmts   []any
	commits []func()
}

func (t *txRecord) add(stmt map[string]any) { t.stmts = append(t.stmts, stmt) }
func (t *txRecord) onCommit(f func())       { t.commits = append(t.commits, f) }

// fakeRunTx runs fn once; an error rolls the recorded statements back.
type fakeRunTx struct{ h *runHarness }

var _ reviewsapp.Tx = (*fakeRunTx)(nil)

func (x *fakeRunTx) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	rec := &txRecord{}
	err := fn(context.WithValue(ctx, txKey{}, rec))
	extra := map[string]any{"statements": rec.stmts}
	if rec.stmts == nil {
		extra["statements"] = []any{}
	}
	if err != nil {
		x.h.record("tx.rollback", nil, extra)
		return err
	}
	x.h.clock.bump(5)
	for _, f := range rec.commits {
		f()
	}
	x.h.record("tx.commit", nil, extra)
	return nil
}

// ---- repo intel and project context --------------------------------------------------------------

type fakeRepoIntel struct{ h *runHarness }

var _ reviewsapp.RepoIntel = (*fakeRepoIntel)(nil)

// throwing reports the error text of a `{"throw": "..."}` setting.
func throwing(raw json.RawMessage) (string, bool) {
	var t struct {
		Throw string `json:"throw"`
	}
	if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &t) == nil && t.Throw != "" {
		return t.Throw, true
	}
	return "", false
}

func (f *fakeRepoIntel) CallerSignatures(_ context.Context, repoID string, changed []string, limit int) ([]domain.CallerSignature, error) {
	f.h.record("repoIntel.getCallerSignatures", []any{repoID, changed, limit}, nil)
	f.h.clock.bump(10)
	raw := f.h.sc.Input.RepoIntel.Callers
	if msg, ok := throwing(raw); ok {
		return nil, errors.New(msg)
	}
	var rows []struct {
		File      string `json:"file"`
		Symbol    string `json:"symbol"`
		Signature string `json:"signature"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			f.h.t.Errorf("callers setting %s: %v", raw, err)
		}
	}
	out := make([]domain.CallerSignature, len(rows))
	for i, r := range rows {
		out[i] = domain.CallerSignature{File: r.File, Symbol: r.Symbol, Signature: r.Signature}
	}
	return out, nil
}

func (f *fakeRepoIntel) RepoMap(_ context.Context, repoID string, budget int) (domain.RepoMap, error) {
	// The Node recording passes the repo id only (it has its own default budget).
	f.h.record("repoIntel.getRepoMap", []any{repoID}, nil)
	f.h.clock.bump(10)
	if budget != domain.RepoMapBudget {
		f.h.t.Errorf("RepoMap budget = %d, want domain.RepoMapBudget (%d)", budget, domain.RepoMapBudget)
	}
	raw := f.h.sc.Input.RepoIntel.RepoMap
	if msg, ok := throwing(raw); ok {
		return domain.RepoMap{}, errors.New(msg)
	}
	if len(raw) == 0 {
		return domain.RepoMap{Degraded: true}, nil
	}
	var m struct {
		Text     string `json:"text"`
		Tokens   int    `json:"tokens"`
		Cached   bool   `json:"cached"`
		Degraded bool   `json:"degraded"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		f.h.t.Errorf("repoMap setting %s: %v", raw, err)
	}
	return domain.RepoMap{Text: m.Text, Tokens: m.Tokens, Cached: m.Cached, Degraded: m.Degraded}, nil
}

func (f *fakeRepoIntel) FileRank(_ context.Context, repoID string, paths []string) ([]domain.PathRank, error) {
	f.h.record("repoIntel.getFileRank", []any{repoID, paths}, nil)
	f.h.clock.bump(10)
	raw := f.h.sc.Input.RepoIntel.Rank
	if msg, ok := throwing(raw); ok {
		return nil, errors.New(msg)
	}
	var rows []struct {
		FilePath   string `json:"filePath"`
		Percentile int    `json:"percentile"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			f.h.t.Errorf("rank setting %s: %v", raw, err)
		}
	}
	out := make([]domain.PathRank, len(rows))
	for i, r := range rows {
		out[i] = domain.PathRank{Path: r.FilePath, Percentile: r.Percentile}
	}
	return out, nil
}

type fakeProjectContext struct{ h *runHarness }

var _ reviewsapp.ProjectContext = (*fakeProjectContext)(nil)

func (f *fakeProjectContext) ResolveForRun(_ context.Context, in reviewsapp.ProjectContextInput) (reviewsapp.ProjectContextResult, error) {
	h := f.h
	skills := make([]any, len(in.Skills))
	for i, s := range in.Skills {
		skills[i] = map[string]any{"id": s.ID, "name": s.Name, "enabled": true}
	}
	h.record("projectContext.resolveForRun", []any{map[string]any{
		"workspaceId": in.WorkspaceID, "repoId": in.Repo.ID,
		"repo": map[string]any{"id": in.Repo.ID, "owner": in.Repo.Owner, "name": in.Repo.Name},
		"base": in.Base, "headSha": in.HeadSHA, "agentId": in.AgentID, "skills": skills,
	}}, nil)
	h.clock.bump(30)

	cfg, ok := h.sc.Input.ProjectContext[h.refOfAgent[in.AgentID]]
	if ok && cfg.Warn != "" {
		return reviewsapp.ProjectContextResult{}, errors.New(cfg.Warn)
	}
	if !ok {
		return reviewsapp.ProjectContextResult{}, nil
	}
	docs := make([]domain.ContextDoc, len(cfg.Docs))
	runDocs := make([]pcdomain.RunDoc, len(cfg.Docs))
	for i, d := range cfg.Docs {
		docs[i] = domain.ContextDoc{
			Path: d.Path, DocType: d.DocType,
			Origin: domain.ContextOrigin{Kind: d.Origin.Kind, SkillID: d.Origin.SkillID, SkillName: d.Origin.SkillName},
			Tokens: d.Tokens, Status: domain.ContextStatus(d.Status), Source: d.Source, Text: d.Text,
		}
		runDocs[i] = pcdomain.RunDoc{Path: d.Path, Status: pcdomain.RunStatus(d.Status)}
	}
	return reviewsapp.ProjectContextResult{
		Docs: docs, TokensTotal: cfg.TokensTotal, BudgetTokens: domain.ProjectContextBudgetTokens,
		LogLine: pcdomain.RunLogLine(runDocs, cfg.TokensTotal),
	}, nil
}

// ---- the LLM client ------------------------------------------------------------------------------

type (
	startedKey struct{}
	cursorKey  struct{}
)

// stepCursor walks the attempts of one scripted structured call.
type stepCursor struct {
	step    scriptStep
	attempt int
}

// hookAdapter tells the call that the provider was reached (the cancel actions wait for it) and
// forwards everything to the real adapter.
type hookAdapter struct{ llm.Adapter }

func (a hookAdapter) Complete(ctx context.Context, key string, at llm.Attempt) (llm.AttemptResult, error) {
	if f, ok := ctx.Value(startedKey{}).(func()); ok {
		f()
	}
	return a.Adapter.Complete(ctx, key, at)
}

// scriptAdapter plays one scripted step per structured call: the step's usage entries are the
// attempts. All but the last answer with invalid JSON (so the real client re-prompts and reports
// each attempt's usage), the last returns the step's raw output or, for an error step, the error
// with `Answered` set (a 200 the provider answered unusably: its usage is still reported).
type scriptAdapter struct{}

func (scriptAdapter) RequiresKey() bool { return true }
func (scriptAdapter) Policy() llm.Policy {
	return llm.Policy{RequestTimeout: time.Minute, ListTimeout: time.Minute}
}
func (scriptAdapter) ListModels(context.Context, string) ([]llm.ModelInfo, error) { return nil, nil }
func (scriptAdapter) Complete(ctx context.Context, _ string, _ llm.Attempt) (llm.AttemptResult, error) {
	cur, ok := ctx.Value(cursorKey{}).(*stepCursor)
	if !ok {
		return llm.AttemptResult{}, errors.New("scriptAdapter: no script step in the context")
	}
	i := min(cur.attempt, len(cur.step.Usage)-1)
	cur.attempt++
	u := cur.step.Usage[i]
	res := llm.AttemptResult{Usage: &llm.Usage{TokensIn: u.TokensIn, TokensOut: u.TokensOut}, CostUSD: u.CostUSD}
	switch {
	case cur.step.Error != nil:
		res.Answered = true
		return res, errors.New(*cur.step.Error)
	case i < len(cur.step.Usage)-1:
		res.Raw = "not json"
	default:
		res.Raw = cur.step.Raw
	}
	return res, nil
}

// recordedLLMCall is one structured request as the engine made it.
type recordedLLMCall struct {
	Chunk       int
	SchemaName  string
	Model       string
	Temperature *float64
	Reprompts   int
	SessionID   string
	Messages    []llm.Message
}

// recordingClient is the *llm.Client of a scenario behind the peers.LLMClient interface. It records
// the provider resolution and every structured request, moves the logical clock, selects the script
// step of a call and fires the scenario's `llmCall:<n>` cancel actions while the call is in flight.
type recordingClient struct {
	h       *runHarness
	inner   *llm.Client
	adapter llm.Adapter
	fake    *fakeProvider

	mu    sync.Mutex
	calls []recordedLLMCall
}

var _ peers.LLMClient = (*recordingClient)(nil)

const testProviderKey = "sk-test-key"

func newRecordingClient(h *runHarness) *recordingClient {
	c := &recordingClient{h: h}
	L := h.sc.Input.LLM
	switch L.Mode {
	case "script":
		c.adapter = scriptAdapter{}
	case "http":
		c.fake = newFakeProvider(h.t, L)
		c.adapter = openaicompat.NewOpenRouter(openaicompat.Options{BaseURL: c.fake.srv.URL + "/v1"})
	case "mock":
		c.adapter = mock.New(llm.OpenRouter, mock.Options{Delay: time.Duration(L.DelayMS) * time.Millisecond})
	default:
		h.t.Fatalf("scenario %s: unknown llm mode %q", h.sc.Name, L.Mode)
	}
	keys := keyMap{}
	if L.ResolveError == "" {
		keys["OPENROUTER_API_KEY"] = testProviderKey
	}
	c.inner = llm.New(llm.Options{
		Keys:     keys,
		Adapters: map[llm.Provider]llm.Adapter{llm.OpenRouter: hookAdapter{c.adapter}},
		Logger:   slog.New(slog.NewJSONHandler(h.clientLogs, nil)),
		Prices:   nilPricer{},
		Retry:    retry.Options{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: func(time.Duration) time.Duration { return 0 }},
	})
	h.t.Cleanup(c.inner.Close)
	return c
}

func (c *recordingClient) Ready(p llm.Provider) error {
	c.h.record("llm.resolve", []any{string(p)}, nil)
	c.h.clock.bump(1)
	return c.inner.Ready(p)
}

func (c *recordingClient) Redact(s string) string { return c.inner.Redact(s) }

func (c *recordingClient) Structured(ctx context.Context, req llm.Request) (llm.Result, error) {
	h := c.h
	chunk := 0
	if req.Chunk != nil {
		chunk = req.Chunk.Index
	}
	c.mu.Lock()
	idx := len(c.calls)
	c.calls = append(c.calls, recordedLLMCall{
		Chunk: chunk, SchemaName: req.SchemaName, Model: req.Model, Temperature: req.Temperature,
		Reprompts: req.Reprompts, SessionID: req.SessionID, Messages: append([]llm.Message(nil), req.Messages...),
	})
	c.mu.Unlock()
	h.clock.bump(1500)

	if h.sc.Input.LLM.Mode == "script" {
		L := h.sc.Input.LLM
		step, ok := L.Chunks[fmt.Sprint(chunk)]
		if !ok && L.Default != nil {
			step, ok = *L.Default, true
		}
		if !ok {
			return llm.Result{}, fmt.Errorf("no script step for chunk %d", chunk)
		}
		ctx = context.WithValue(ctx, cursorKey{}, &stepCursor{step: step})
	}

	at := fmt.Sprintf("llmCall:%d", idx)
	if !h.hasAction(at) {
		return c.inner.Structured(ctx, req)
	}
	// The cancel route runs once the provider was reached, while the call is in flight (Node:
	// settle, then fire), and the call returns only after the route finished.
	started, finished, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	ctx = context.WithValue(ctx, startedKey{}, func() { once.Do(func() { close(started) }) })
	go func() {
		defer close(done)
		select {
		case <-started:
			h.fire(context.WithoutCancel(ctx), at)
		case <-finished:
		}
	}()
	res, err := c.inner.Structured(ctx, req)
	close(finished)
	<-done
	return res, err
}

// recordedCalls are the structured requests in call order.
func (c *recordingClient) recordedCalls() []recordedLLMCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]recordedLLMCall(nil), c.calls...)
}

// ---- the HTTP provider fake ----------------------------------------------------------------------

// fakeProvider is the chat-completions endpoint of an `http` scenario: it answers with the scripted
// responses in order (a 400 when none is left, as the Node fake) and records the requests.
type fakeProvider struct {
	srv *httptest.Server

	mu       sync.Mutex
	served   int
	requests []map[string]any
}

func newFakeProvider(t testing.TB, L runLLM) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body any
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.requests = append(p.requests, map[string]any{
			"path": r.URL.Path, "authorization": r.Header.Get("Authorization"), "body": body,
		})
		n := p.served
		p.served++
		p.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if n >= len(L.Responses) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"no scripted response left"}}`)
			return
		}
		resp := L.Responses[n]
		payload := map[string]any{
			"id": fmt.Sprintf("gen-%d", n+1), "object": "chat.completion", "created": 0, "model": "m/x",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": resp.Content}}},
		}
		if len(resp.Usage) > 0 {
			payload["usage"] = resp.Usage
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("fake provider: encode response: %v", err)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) recorded() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.requests...)
}
