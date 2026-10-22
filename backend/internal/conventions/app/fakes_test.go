package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/llm"
)

const (
	ws     = "ws-1"
	repoID = "repo-1"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// memStore is an in-memory implementation of the scan and job stores.
type memStore struct {
	mu          sync.Mutex
	scans       []*domain.Scan
	conventions []*domain.Convention
	seq         int
	// completed records the kept rules and summary of CompleteScan calls.
	completed []completion
	// failWrites records the context error seen by FailScan writes.
	failCtxErr []error
	startErr   error
}

type completion struct {
	Ref  app.ScanRef
	Kept []domain.KeptConvention
	Sum  app.Summary
}

func (m *memStore) nextID(prefix string) string {
	m.seq++
	return fmt.Sprintf("%s-%d", prefix, m.seq)
}

func (m *memStore) scan(id string) *domain.Scan {
	for _, s := range m.scans {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (m *memStore) LatestScan(_ context.Context, repo string) (domain.Scan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *domain.Scan
	for _, s := range m.scans {
		if s.RepoID == repo && (best == nil || s.StartedAt.After(best.StartedAt)) {
			best = s
		}
	}
	if best == nil {
		return domain.Scan{}, domain.ErrNotFound
	}
	return *best, nil
}

func (m *memStore) List(_ context.Context, repo string) ([]domain.Convention, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Convention
	for _, c := range m.conventions {
		if c.RepoID == repo {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (m *memStore) FailStaleScans(_ context.Context, repo string, before time.Time, reason string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.scans {
		if s.RepoID == repo && s.Status == domain.ScanRunning && s.StartedAt.Before(before) {
			s.Status, s.Error, s.FinishedAt = domain.ScanFailed, &reason, &at
		}
	}
	return nil
}

func (m *memStore) StartScan(_ context.Context, workspace, repo string, at time.Time) (domain.Scan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startErr != nil {
		return domain.Scan{}, m.startErr
	}
	for _, s := range m.scans {
		if s.RepoID == repo && s.Status == domain.ScanRunning {
			return domain.Scan{}, domain.ErrScanRunning
		}
	}
	s := &domain.Scan{ID: m.nextID("scan"), WorkspaceID: workspace, RepoID: repo, Status: domain.ScanRunning,
		SampledFiles: []string{}, StartedAt: at}
	m.scans = append(m.scans, s)
	return *s, nil
}

func (m *memStore) FailScan(ctx context.Context, id, reason string, sampled []string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failCtxErr = append(m.failCtxErr, ctx.Err())
	if s := m.scan(id); s != nil && s.Status == domain.ScanRunning {
		s.Status, s.Error, s.FinishedAt = domain.ScanFailed, &reason, &at
		s.SampledFiles = slices.Clone(sampled)
	}
	return nil
}

func (m *memStore) FailRunningScans(_ context.Context, reason string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.scans {
		if s.Status == domain.ScanRunning {
			s.Status, s.Error, s.FinishedAt = domain.ScanFailed, &reason, &at
			n++
		}
	}
	return n, nil
}

func (m *memStore) CompleteScan(_ context.Context, ref app.ScanRef, kept []domain.KeptConvention, sum app.Summary, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.scan(ref.ID)
	if s == nil || s.Status != domain.ScanRunning {
		return domain.ErrNotFound
	}
	m.completed = append(m.completed, completion{ref, kept, sum})
	s.Status, s.FinishedAt = domain.ScanDone, &at
	s.SampledFiles, s.Proposed, s.Kept, s.Dropped = sum.SampledFiles, sum.Proposed, sum.Kept, sum.Dropped
	model := sum.Model
	s.Model, s.CostUSD = &model, sum.CostUSD
	return nil
}

func (m *memStore) Update(_ context.Context, workspace, id string, fn func(domain.Convention) (domain.Convention, error)) (domain.Convention, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conventions {
		if c.WorkspaceID == workspace && c.ID == id {
			next, err := fn(*c)
			if err != nil {
				return domain.Convention{}, err
			}
			*c = next
			return next, nil
		}
	}
	return domain.Convention{}, domain.ErrNotFound
}

func (m *memStore) AcceptedIDs(_ context.Context, workspace, repo string, ids []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, c := range m.conventions {
		if c.WorkspaceID == workspace && c.RepoID == repo && c.Status == domain.StatusAccepted && slices.Contains(ids, c.ID) {
			out = append(out, c.ID)
		}
	}
	return out, nil
}

func (m *memStore) SetSkill(_ context.Context, workspace, repo string, ids []string, skillID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conventions {
		if c.WorkspaceID == workspace && c.RepoID == repo && slices.Contains(ids, c.ID) {
			c.SkillID, c.UpdatedAt = &skillID, at
		}
	}
	return nil
}

func (m *memStore) add(c domain.Convention) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ID == "" {
		c.ID = m.nextID("conv")
	}
	if c.WorkspaceID == "" {
		c.WorkspaceID = ws
	}
	if c.RepoID == "" {
		c.RepoID = repoID
	}
	m.conventions = append(m.conventions, &c)
}

func (m *memStore) running(repo string) *domain.Scan {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.scans {
		if s.RepoID == repo && s.Status == domain.ScanRunning {
			c := *s
			return &c
		}
	}
	return nil
}

func (m *memStore) lastScan(t interface{ Fatalf(string, ...any) }) domain.Scan {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.scans) == 0 {
		t.Fatalf("no scan stored")
	}
	return *m.scans[len(m.scans)-1]
}

// memRepos is the repo lookup.
type memRepos struct{ repos map[string]app.Repo }

func (r memRepos) Get(_ context.Context, workspace, id string) (app.Repo, error) {
	repo, ok := r.repos[id]
	if !ok || workspace != ws {
		return app.Repo{}, domain.ErrNotFound
	}
	return repo, nil
}

// queue records enqueued jobs.
type queue struct {
	mu   sync.Mutex
	jobs []queued
	err  error
}

type queued struct {
	Workspace, Kind string
	Payload         any
}

func (q *queue) Enqueue(_ context.Context, workspace, kind string, payload any) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return "", q.err
	}
	q.jobs = append(q.jobs, queued{workspace, kind, payload})
	return "job-1", nil
}

// redactKey replaces one secret with [REDACTED].
type redactKey string

func (k redactKey) Redact(s string) string { return strings.ReplaceAll(s, string(k), "[REDACTED]") }

// memFiles is an in-memory clone: path to text. Missing is the "clone directory is gone" case.
type memFiles struct {
	files   map[string]string
	missing bool
	// reads counts Read calls per path.
	mu    sync.Mutex
	reads map[string]int
}

func (f *memFiles) List(_ context.Context, _ string) ([]string, error) {
	if f.missing {
		return nil, fmt.Errorf("walk: %w", domain.ErrCloneMissing)
	}
	paths := make([]string, 0, len(f.files))
	for p := range f.files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths, nil
}

func (f *memFiles) Read(_, rel string) (string, bool) {
	f.mu.Lock()
	if f.reads == nil {
		f.reads = map[string]int{}
	}
	f.reads[rel]++
	f.mu.Unlock()
	text, ok := f.files[rel]
	return text, ok
}

// settings is the feature-model port.
type settings struct {
	bag map[string]json.RawMessage
	err error
}

func (s settings) Get(context.Context, string) (map[string]json.RawMessage, error) {
	return s.bag, s.err
}

// modelFake is a scripted structuredLLM.
type modelFake struct {
	mu    sync.Mutex
	reqs  []llm.Request
	reply func(ctx context.Context, req llm.Request) (llm.Result, error)
}

func (m *modelFake) Structured(ctx context.Context, req llm.Request) (llm.Result, error) {
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	return m.reply(ctx, req)
}

func (m *modelFake) requests() []llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.reqs)
}

func answer(data string) func(context.Context, llm.Request) (llm.Result, error) {
	return func(_ context.Context, req llm.Request) (llm.Result, error) {
		cost := 0.0123
		return llm.Result{Data: json.RawMessage(data), Model: req.Model, TokensIn: 1000, TokensOut: 200, CostUSD: &cost, Attempts: 1}, nil
	}
}

func failWith(err error) func(context.Context, llm.Request) (llm.Result, error) {
	return func(context.Context, llm.Request) (llm.Result, error) { return llm.Result{}, err }
}

// discard is a logger that drops everything.
var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

var errBoom = errors.New("boom")

// contextCancelled returns an already cancelled context.
func contextCancelled(t interface{ Context() context.Context }) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx, cancel
}

// cmpEmpty treats a nil and an empty slice as equal.
func cmpEmpty() cmp.Option { return cmpopts.EquateEmpty() }

type rankCall struct {
	repoID string
	n      int
}

// fakeRanker answers ConventionSamples with fixed paths or an error and records the call.
type fakeRanker struct {
	paths []string
	err   error
	calls int
	last  rankCall
}

func (r *fakeRanker) ConventionSamples(_ context.Context, repoID string, n int) ([]string, error) {
	r.calls++
	r.last = rankCall{repoID, n}
	return slices.Clone(r.paths), r.err
}
