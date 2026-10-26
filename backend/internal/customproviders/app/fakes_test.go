package app_test

import (
	"context"
	"errors"
	"slices"
	"sync"

	"dev-digest-go/backend/internal/customproviders/app"
	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/secrets"
)

// fakeStore is an in-memory app.Store. InTx serializes transactions, snapshots the data and
// restores it when fn fails or commitErr is set, like a rolled-back transaction.
type fakeStore struct {
	mu        sync.Mutex // guards data and events
	txMu      sync.Mutex // one transaction at a time
	data      map[string]map[string]domain.Provider
	events    []string
	commitErr error
	getErr    error
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: map[string]map[string]domain.Provider{}}
}

func clone(p domain.Provider) domain.Provider {
	p.ExtraHeaderNames = slices.Clone(p.ExtraHeaderNames)
	p.Models = slices.Clone(p.Models)
	return p
}

func (s *fakeStore) snapshot() map[string]map[string]domain.Provider {
	out := map[string]map[string]domain.Provider{}
	for ws, m := range s.data {
		out[ws] = map[string]domain.Provider{}
		for id, p := range m {
			out[ws][id] = clone(p)
		}
	}
	return out
}

func (s *fakeStore) record(e string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *fakeStore) eventLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func (s *fakeStore) InTx(ctx context.Context, fn func(context.Context) error) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	s.mu.Lock()
	before := s.snapshot()
	s.mu.Unlock()
	err := fn(ctx)
	if err == nil {
		err = s.commitErr
	}
	if err != nil {
		s.mu.Lock()
		s.data = before
		s.mu.Unlock()
	}
	return err
}

func (s *fakeStore) List(_ context.Context, ws string) ([]domain.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Provider
	for _, p := range s.data[ws] {
		out = append(out, clone(p))
	}
	return out, nil
}

func (s *fakeStore) Get(_ context.Context, ws, id string) (domain.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return domain.Provider{}, s.getErr
	}
	p, ok := s.data[ws][id]
	if !ok {
		return domain.Provider{}, domain.ErrNotFound
	}
	return clone(p), nil
}

func (s *fakeStore) GetForUpdate(ctx context.Context, ws, id string) (domain.Provider, error) {
	s.record("get_for_update " + id)
	return s.Get(ctx, ws, id)
}

func (s *fakeStore) Exists(_ context.Context, ws, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return false, s.getErr
	}
	_, ok := s.data[ws][id]
	return ok, nil
}

func (s *fakeStore) LockDefined(ctx context.Context, ws, id string) (bool, error) {
	s.record("lock_defined " + id)
	return s.Exists(ctx, ws, id)
}

func (s *fakeStore) Insert(_ context.Context, ws string, p domain.Provider, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[ws][p.ID]; ok {
		return domain.ErrExists
	}
	if len(s.data[ws]) >= limit {
		return domain.ErrLimit
	}
	if s.data[ws] == nil {
		s.data[ws] = map[string]domain.Provider{}
	}
	s.data[ws][p.ID] = clone(p)
	s.events = append(s.events, "insert "+p.ID)
	return nil
}

func (s *fakeStore) Replace(_ context.Context, ws string, p domain.Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.data[ws][p.ID]
	if !ok {
		return domain.ErrNotFound
	}
	p.CreatedAt = cur.CreatedAt
	s.data[ws][p.ID] = clone(p)
	s.events = append(s.events, "replace "+p.ID)
	return nil
}

func (s *fakeStore) Delete(_ context.Context, ws, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[ws][id]; !ok {
		return domain.ErrNotFound
	}
	delete(s.data[ws], id)
	s.events = append(s.events, "delete "+id)
	return nil
}

func (s *fakeStore) has(ws, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[ws][id]
	return ok
}

func (s *fakeStore) count(ws string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data[ws])
}

// seed stores providers directly.
func (s *fakeStore) seed(ws string, ps ...domain.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[ws] == nil {
		s.data[ws] = map[string]domain.Provider{}
	}
	for _, p := range ps {
		s.data[ws][p.ID] = clone(p)
	}
}

// fakeSecrets is an in-memory app.Secrets with failure injection.
type fakeSecrets struct {
	mu         sync.Mutex
	data       map[string]secrets.CustomSecret
	calls      []string
	setErr     error
	restoreErr error
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{data: map[string]secrets.CustomSecret{}} }

func (f *fakeSecrets) Custom(ws, id string) secrets.CustomSecret {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data[ws+"/"+id]
}

func (f *fakeSecrets) SetCustom(ws, id string, sec secrets.CustomSecret) (secrets.CustomSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return secrets.CustomSecret{}, f.setErr
	}
	f.calls = append(f.calls, "set "+id)
	prev := f.data[ws+"/"+id]
	if sec.IsZero() {
		delete(f.data, ws+"/"+id)
	} else {
		f.data[ws+"/"+id] = sec
	}
	return prev, nil
}

func (f *fakeSecrets) DeleteCustom(ws, id string) (secrets.CustomSecret, error) {
	f.mu.Lock()
	if f.setErr != nil {
		f.mu.Unlock()
		return secrets.CustomSecret{}, f.setErr
	}
	f.calls = append(f.calls, "delete "+id)
	prev := f.data[ws+"/"+id]
	delete(f.data, ws+"/"+id)
	f.mu.Unlock()
	return prev, nil
}

func (f *fakeSecrets) RestoreCustom(ws, id string, prev secrets.CustomSecret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "restore "+id)
	if f.restoreErr != nil {
		return f.restoreErr
	}
	if prev.IsZero() {
		delete(f.data, ws+"/"+id)
	} else {
		f.data[ws+"/"+id] = prev
	}
	return nil
}

func (f *fakeSecrets) put(ws, id string, sec secrets.CustomSecret) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[ws+"/"+id] = sec
}

func (f *fakeSecrets) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// fakeReferences is an app.References over fixed answers; it records the order of its calls
// together with the store's events.
type fakeReferences struct {
	usage       map[string]domain.Usage
	defaults    map[string]string
	err         error
	defaultErr  error
	store       *fakeStore
	findCalls   int
	findCtxInTx bool
}

func (r *fakeReferences) Find(_ context.Context, _, id string) (domain.Usage, error) {
	r.findCalls++
	if r.store != nil {
		r.store.record("references " + id)
	}
	return r.usage[id], r.err
}

func (r *fakeReferences) DefaultModel(_ context.Context, _, id string) (string, bool, error) {
	if r.defaultErr != nil {
		return "", false, r.defaultErr
	}
	m, ok := r.defaults[id]
	return m, ok, nil
}

// fakeAdapter is an llm.Adapter that counts its calls.
type fakeAdapter struct {
	mu            sync.Mutex
	list          []llm.ModelInfo
	listErr       error
	block         bool // ListModels waits for its ctx
	policy        llm.Policy
	listCalls     int
	completeCalls int
	keys          []string
}

func (a *fakeAdapter) RequiresKey() bool  { return false }
func (a *fakeAdapter) Policy() llm.Policy { return a.policy }
func (a *fakeAdapter) Complete(context.Context, string, llm.Attempt) (llm.AttemptResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completeCalls++
	return llm.AttemptResult{}, errors.New("unexpected completion")
}

func (a *fakeAdapter) ListModels(ctx context.Context, key string) ([]llm.ModelInfo, error) {
	a.mu.Lock()
	a.listCalls++
	a.keys = append(a.keys, key)
	list, err, block := slices.Clone(a.list), a.listErr, a.block
	a.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return list, err
}

// fakeAdapters is an app.Adapters that hands out one fakeAdapter and records what it was built from.
type fakeAdapters struct {
	adapter  *fakeAdapter
	buildErr error
	built    []builtWith
}

type builtWith struct {
	Provider domain.Provider
	Secret   secrets.CustomSecret
}

func (f *fakeAdapters) Build(p domain.Provider, sec secrets.CustomSecret) (llm.Adapter, error) {
	f.built = append(f.built, builtWith{p, sec})
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	return f.adapter, nil
}

// statusErr is an adapter error with the HTTP status of the gateway's answer.
type statusErr struct {
	code int
	msg  string
}

func (e *statusErr) Error() string   { return e.msg }
func (e *statusErr) StatusCode() int { return e.code }

var _ app.Store = (*fakeStore)(nil)
var _ app.Secrets = (*fakeSecrets)(nil)
var _ app.References = (*fakeReferences)(nil)
var _ app.Adapters = (*fakeAdapters)(nil)
