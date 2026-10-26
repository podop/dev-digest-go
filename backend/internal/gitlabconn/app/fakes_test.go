package app_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"dev-digest-go/backend/internal/gitlabconn/app"
	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/gitlab"
)

// fakeStore is an in-memory app.Store. InTx serializes transactions, snapshots the data and
// restores it when fn fails or commitErr is set, like a rolled-back transaction.
type fakeStore struct {
	mu        sync.Mutex // guards data
	txMu      sync.Mutex // one transaction at a time
	data      map[string]domain.Connection
	commitErr error
	txs       int
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string]domain.Connection{}} }

func (s *fakeStore) InTx(ctx context.Context, fn func(context.Context) error) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	s.mu.Lock()
	s.txs++
	before := make(map[string]domain.Connection, len(s.data))
	for k, v := range s.data {
		before[k] = v
	}
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

func (s *fakeStore) List(context.Context) ([]domain.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Connection, 0, len(s.data))
	for _, c := range s.data {
		out = append(out, c)
	}
	return out, nil
}

func (s *fakeStore) Get(_ context.Context, id string) (domain.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data[id]
	if !ok {
		return domain.Connection{}, domain.ErrNotFound
	}
	return c, nil
}

func (s *fakeStore) GetForUpdate(ctx context.Context, id string) (domain.Connection, error) {
	return s.Get(ctx, id)
}

func (s *fakeStore) GetByHost(_ context.Context, host string) (domain.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.data {
		if c.Host == host {
			return c, nil
		}
	}
	return domain.Connection{}, domain.ErrNotFound
}

func (s *fakeStore) Insert(_ context.Context, c domain.Connection, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.data {
		if o.Host == c.Host {
			return domain.ErrExists
		}
	}
	if len(s.data) >= limit {
		return domain.ErrLimit
	}
	s.data[c.ID] = c
	return nil
}

func (s *fakeStore) Replace(_ context.Context, c domain.Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.data[c.ID]
	if !ok {
		return domain.ErrNotFound
	}
	cur.Name, cur.BaseURL, cur.UpdatedAt = c.Name, c.BaseURL, c.UpdatedAt
	s.data[c.ID] = cur
	return nil
}

func (s *fakeStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[id]; !ok {
		return domain.ErrNotFound
	}
	delete(s.data, id)
	return nil
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data)
}

func (s *fakeStore) put(c domain.Connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[c.ID] = c
}

// fakeSecrets is an in-memory app.Secrets; setErr and deleteErr make a write fail without
// changing anything.
type fakeSecrets struct {
	mu        sync.Mutex
	tokens    map[string]string
	setErr    error
	deleteErr error
	restores  int
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{tokens: map[string]string{}} }

func (f *fakeSecrets) GitLab(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[id]
}

func (f *fakeSecrets) SetGitLab(id, token string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return "", f.setErr
	}
	prev := f.tokens[id]
	if token == "" {
		delete(f.tokens, id)
	} else {
		f.tokens[id] = token
	}
	return prev, nil
}

func (f *fakeSecrets) DeleteGitLab(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return "", f.deleteErr
	}
	prev := f.tokens[id]
	delete(f.tokens, id)
	return prev, nil
}

func (f *fakeSecrets) RestoreGitLab(id, prev string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restores++
	if prev == "" {
		delete(f.tokens, id)
	} else {
		f.tokens[id] = prev
	}
	return nil
}

// fakeRepos is an app.RepoCounter over a fixed host -> count map.
type fakeRepos struct {
	counts map[string]int
	err    error
}

func (f *fakeRepos) CountBound(_ context.Context, hosts []string) (map[string]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]int{}
	for _, h := range hosts {
		out[h] = f.counts[h]
	}
	return out, nil
}

// call is one request the fake GitLab saw.
type call struct {
	op       string
	conn     gitlab.Conn
	deadline time.Duration // time left on the call's ctx when it started; 0 without a deadline
}

// fakeGitLab is an app.GitLab whose answers are set per method; unset methods answer
// the "all good" defaults of newFakeGitLab.
type fakeGitLab struct {
	mu    sync.Mutex
	calls []call

	user      func(ctx context.Context) (gitlab.User, error)
	tokenSelf func(ctx context.Context) (gitlab.TokenInfo, error)
	metadata  func(ctx context.Context) (gitlab.Metadata, error)
}

func newFakeGitLab() *fakeGitLab {
	return &fakeGitLab{
		user: func(context.Context) (gitlab.User, error) { return gitlab.User{Username: "ann"}, nil },
		tokenSelf: func(context.Context) (gitlab.TokenInfo, error) {
			return gitlab.TokenInfo{Scopes: []string{"api", "read_repository"}}, nil
		},
		metadata: func(context.Context) (gitlab.Metadata, error) { return gitlab.Metadata{Version: "17.5.1-ee"}, nil },
	}
}

func (f *fakeGitLab) record(ctx context.Context, op string, conn gitlab.Conn) {
	var left time.Duration
	if d, ok := ctx.Deadline(); ok {
		left = time.Until(d)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{op: op, conn: conn, deadline: left})
}

func (f *fakeGitLab) User(ctx context.Context, conn gitlab.Conn) (gitlab.User, error) {
	f.record(ctx, "user", conn)
	return f.user(ctx)
}

func (f *fakeGitLab) TokenSelf(ctx context.Context, conn gitlab.Conn) (gitlab.TokenInfo, error) {
	f.record(ctx, "token_self", conn)
	return f.tokenSelf(ctx)
}

func (f *fakeGitLab) Metadata(ctx context.Context, conn gitlab.Conn) (gitlab.Metadata, error) {
	f.record(ctx, "metadata", conn)
	return f.metadata(ctx)
}

func (f *fakeGitLab) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.op
	}
	return out
}

func (f *fakeGitLab) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// env wires a Service over the fakes with a fixed clock.
type env struct {
	svc     *app.Service
	store   *fakeStore
	secrets *fakeSecrets
	repos   *fakeRepos
	gl      *fakeGitLab
	now     time.Time
}

var errBoom = errors.New("boom")

func newEnv() *env {
	e := &env{
		store: newFakeStore(), secrets: newFakeSecrets(), repos: &fakeRepos{counts: map[string]int{}},
		gl: newFakeGitLab(), now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
	}
	e.svc = app.NewService(app.Deps{
		Store: e.store, Secrets: e.secrets, Repos: e.repos, GitLab: e.gl,
		Clock: func() time.Time { return e.now },
	})
	return e
}

// seed stores a connection and, when token is not empty, its token.
func (e *env) seed(id, name, baseURL, host, token string) domain.Connection {
	c := domain.Connection{ID: id, Name: name, BaseURL: baseURL, Host: host, CreatedAt: e.now, UpdatedAt: e.now}
	e.store.put(c)
	if token != "" {
		e.secrets.tokens[id] = token
	}
	return c
}

const (
	id1 = "11111111-1111-4111-8111-111111111111"
	id2 = "22222222-2222-4222-8222-222222222222"
	// idHex has letters, so its upper-case spelling differs from it.
	idHex = "abcdefab-abcd-4abc-8abc-abcdefabcdef"
)

const time24h = 24 * time.Hour
