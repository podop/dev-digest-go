package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// journal records the order of calls across the fakes of one test.
type journal struct {
	mu    sync.Mutex
	calls []string
}

func (j *journal) add(call string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls = append(j.calls, call)
}

func (j *journal) all() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.calls...)
}

type txKey struct{}

// txState is the transaction a fakeTx call opened; it holds the advisory locks taken inside.
type txState struct{ unlock []func() }

// fakeTx runs fn and releases the advisory locks of the fake store afterwards. fail, when set,
// is returned after fn ran, standing for a rolled-back transaction.
type fakeTx struct {
	j    *journal
	fail error
}

func (t *fakeTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	st := &txState{}
	t.j.add("tx begin")
	err := fn(context.WithValue(ctx, txKey{}, st))
	for _, u := range st.unlock {
		u()
	}
	if err == nil {
		err = t.fail
	}
	t.j.add("tx end")
	return err
}

func inTx(ctx context.Context) *txState {
	st, _ := ctx.Value(txKey{}).(*txState)
	return st
}

// fakeStore is an in-memory app.Store; only the methods the use cases call are implemented, the
// embedded nil interface panics on any other.
type fakeStore struct {
	app.Store
	j *journal

	mu sync.Mutex

	pulls   map[string]domain.ReviewPull // by id
	repos   map[string]domain.ReviewRepo
	agents  map[string]domain.ReviewAgent // by id, enabled or not
	enabled []domain.ReviewAgent

	createdRuns []domain.NewRun
	parents     int
	createErr   error
	runs        map[string]domain.RunState
	deleteRunOK bool
	cancelled   []string // runIDs CancelIfRunning saw
	cancelNote  string
	failedN     int
	failedErr   error

	trace    domain.Trace
	traceErr error

	reviews      []domain.ReviewRecord
	deletedRev   map[string]bool
	findings     map[string]domain.FindingRecord
	actAt        time.Time
	header       domain.MultiAgentHeader
	headerErr    error
	columns      []domain.AgentColumn
	repoOK       bool
	listedLimit  int
	estimates    []domain.AgentRunEstimate
	learnTarget  domain.LearnTarget
	learnErr     error
	locks        map[string]*sync.Mutex
	memories     map[string]string // finding id -> memory id
	insertedMems []domain.NewMemory
}

func newFakeStore(j *journal) *fakeStore {
	return &fakeStore{
		j:          j,
		pulls:      map[string]domain.ReviewPull{},
		repos:      map[string]domain.ReviewRepo{},
		agents:     map[string]domain.ReviewAgent{},
		runs:       map[string]domain.RunState{},
		deletedRev: map[string]bool{},
		findings:   map[string]domain.FindingRecord{},
		locks:      map[string]*sync.Mutex{},
		memories:   map[string]string{},
	}
}

func (s *fakeStore) FindPull(_ context.Context, _, prID string) (domain.ReviewPull, error) {
	p, ok := s.pulls[prID]
	if !ok {
		return domain.ReviewPull{}, domain.ErrNotFound
	}
	return p, nil
}

func (s *fakeStore) FindRepo(_ context.Context, repoID string) (domain.ReviewRepo, error) {
	r, ok := s.repos[repoID]
	if !ok {
		return domain.ReviewRepo{}, domain.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) FindAgent(_ context.Context, _, agentID string) (domain.ReviewAgent, error) {
	a, ok := s.agents[agentID]
	if !ok {
		return domain.ReviewAgent{}, domain.ErrNotFound
	}
	return a, nil
}

func (s *fakeStore) EnabledAgents(context.Context, string) ([]domain.ReviewAgent, error) {
	return s.enabled, nil
}

func (s *fakeStore) CreateMultiAgentRun(ctx context.Context, _, _ string) (string, error) {
	if inTx(ctx) == nil {
		return "", errors.New("CreateMultiAgentRun outside a transaction")
	}
	s.parents++
	s.j.add("create parent")
	return "parent-1", nil
}

func (s *fakeStore) CreateRun(ctx context.Context, r domain.NewRun) (string, error) {
	if inTx(ctx) == nil {
		return "", errors.New("CreateRun outside a transaction")
	}
	if s.createErr != nil {
		return "", s.createErr
	}
	s.createdRuns = append(s.createdRuns, r)
	s.j.add("create run " + r.AgentID)
	return "run-" + r.AgentID, nil
}

func (s *fakeStore) FindRun(_ context.Context, _, runID string) (domain.RunState, error) {
	r, ok := s.runs[runID]
	if !ok {
		return domain.RunState{}, domain.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) DeleteRun(ctx context.Context, _, runID string) (bool, error) {
	if inTx(ctx) == nil {
		return false, errors.New("DeleteRun outside a transaction")
	}
	s.j.add("delete run")
	return s.deleteRunOK, nil
}

func (s *fakeStore) CancelIfRunning(_ context.Context, _, runID, note string) (bool, error) {
	s.j.add("store cancel")
	s.cancelled = append(s.cancelled, runID)
	s.cancelNote = note
	return true, nil
}

func (s *fakeStore) ActiveRuns(context.Context, string, string) ([]domain.ActiveRun, error) {
	return nil, nil
}

func (s *fakeStore) ListRuns(context.Context, string, string) ([]domain.RunSummary, error) {
	return nil, nil
}

func (s *fakeStore) FailInterrupted(context.Context) (int, error) { return s.failedN, s.failedErr }

func (s *fakeStore) FindTrace(context.Context, string, string) (domain.Trace, error) {
	return s.trace, s.traceErr
}

func (s *fakeStore) ReviewsForPull(context.Context, string) ([]domain.ReviewRecord, error) {
	return s.reviews, nil
}

func (s *fakeStore) DeleteReview(_ context.Context, _, reviewID string) (bool, error) {
	return s.deletedRev[reviewID], nil
}

func (s *fakeStore) AcceptFinding(_ context.Context, _, findingID string, at time.Time) (domain.FindingRecord, error) {
	f, ok := s.findings[findingID]
	if !ok {
		return domain.FindingRecord{}, domain.ErrNotFound
	}
	f.AcceptedAt = &at
	return f, nil
}

func (s *fakeStore) DismissFinding(_ context.Context, _, findingID string, at time.Time) (domain.FindingRecord, error) {
	f, ok := s.findings[findingID]
	if !ok {
		return domain.FindingRecord{}, domain.ErrNotFound
	}
	f.DismissedAt = &at
	return f, nil
}

func (s *fakeStore) FindMultiAgentRun(context.Context, string, string) (domain.MultiAgentHeader, error) {
	return s.header, s.headerErr
}

func (s *fakeStore) MultiAgentColumns(context.Context, string, string) ([]domain.AgentColumn, error) {
	return s.columns, nil
}

func (s *fakeStore) RepoInWorkspace(context.Context, string, string) (bool, error) {
	return s.repoOK, nil
}

func (s *fakeStore) ListMultiAgentRuns(_ context.Context, _, _ string, limit int) ([]domain.MultiAgentSummary, error) {
	s.listedLimit = limit
	return nil, nil
}

func (s *fakeStore) AgentEstimates(context.Context, string) ([]domain.AgentRunEstimate, error) {
	return s.estimates, nil
}

func (s *fakeStore) FindLearnTarget(context.Context, string, string) (domain.LearnTarget, error) {
	return s.learnTarget, s.learnErr
}

// LockLearn blocks until no other transaction holds the finding's lock; the fake Tx releases it
// when the transaction ends.
func (s *fakeStore) LockLearn(ctx context.Context, findingID string) error {
	st := inTx(ctx)
	if st == nil {
		return errors.New("LockLearn outside a transaction")
	}
	s.mu.Lock()
	m, ok := s.locks[findingID]
	if !ok {
		m = &sync.Mutex{}
		s.locks[findingID] = m
	}
	s.mu.Unlock()
	m.Lock()
	st.unlock = append(st.unlock, m.Unlock)
	s.j.add("lock learn")
	return nil
}

func (s *fakeStore) LearnedMemoryID(_ context.Context, _, findingID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.j.add("lookup memory")
	id, ok := s.memories[findingID]
	return id, ok, nil
}

func (s *fakeStore) InsertLearnedMemory(_ context.Context, m domain.NewMemory) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.j.add("insert memory")
	s.insertedMems = append(s.insertedMems, m)
	id := "mem-" + m.FindingID
	s.memories[m.FindingID] = id
	return id, nil
}

// fakeExecutor records the batches it was started with.
type fakeExecutor struct {
	j       *journal
	mu      sync.Mutex
	batches []domain.Batch
}

func (e *fakeExecutor) Start(_ context.Context, b domain.Batch) {
	e.j.add("executor start")
	e.mu.Lock()
	defer e.mu.Unlock()
	e.batches = append(e.batches, b)
}

// fakeBus records the calls the use cases make; only those are implemented.
type fakeBus struct {
	app.Bus
	j         *journal
	held      map[string]bool
	published []string
	subbed    []string
}

func (b *fakeBus) Publish(runID string, kind reviewer.EventKind, msg string, _ json.RawMessage) domain.RunEvent {
	b.j.add("bus publish " + string(kind))
	b.published = append(b.published, msg)
	return domain.RunEvent{RunID: runID, Kind: kind, Msg: msg}
}
func (b *fakeBus) Cancel(string)      { b.j.add("bus cancel") }
func (b *fakeBus) Complete(string)    { b.j.add("bus complete") }
func (b *fakeBus) Has(id string) bool { return b.held[id] }
func (b *fakeBus) Subscribe(id string) app.Subscription {
	b.subbed = append(b.subbed, id)
	return liveSub{}
}

type liveSub struct{}

func (liveSub) C() <-chan domain.RunEvent { return make(chan domain.RunEvent) }
func (liveSub) Close()                    {}

// harness wires a Service to the fakes.
type harness struct {
	t     *testing.T
	j     *journal
	store *fakeStore
	tx    *fakeTx
	exec  *fakeExecutor
	bus   *fakeBus
	svc   *app.Service
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	j := &journal{}
	h := &harness{
		t: t, j: j, store: newFakeStore(j), tx: &fakeTx{j: j},
		exec: &fakeExecutor{j: j}, bus: &fakeBus{j: j, held: map[string]bool{}},
		now: time.Date(2026, 10, 22, 12, 30, 45, 0, time.UTC),
	}
	h.svc = app.NewService(app.Deps{
		Store: h.store, Tx: h.tx, Executor: h.exec, Bus: h.bus,
		Now: func() time.Time { return h.now },
	})
	return h
}

// withPull registers pull request "pr" of repo "repo".
func (h *harness) withPull() {
	h.store.pulls["pr"] = domain.ReviewPull{ID: "pr", RepoID: "repo", Number: 482, HeadSHA: "abc"}
	h.store.repos["repo"] = domain.ReviewRepo{ID: "repo", Owner: "o", Name: "n"}
}

// withAgent registers an agent; enabled agents also join the all-targets list in call order.
func (h *harness) withAgent(id, name string, enabled bool) domain.ReviewAgent {
	a := domain.ReviewAgent{ID: id, Name: name, Provider: "openrouter", Model: "m-" + id}
	h.store.agents[id] = a
	if enabled {
		h.store.enabled = append(h.store.enabled, a)
	}
	return a
}
