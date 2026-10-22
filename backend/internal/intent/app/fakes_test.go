package app_test

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
)

const (
	prID    = "11111111-1111-4111-8111-111111111111"
	repoID  = "22222222-2222-4222-8222-222222222222"
	wsID    = "33333333-3333-4333-8333-333333333333"
	headSHA = "0123456789abcdef0123456789abcdef01234567"
)

var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func basePull() domain.Pull {
	return domain.Pull{
		ID: prID, RepoID: repoID, Number: 482,
		Title:   "Add rate limiting to public API endpoints",
		Body:    "Adds a token-bucket rate limiter in front of the public webhook endpoints.\n\nThe limits move into the config so ops can tune them without a deploy.",
		Branch:  "feat/rate-limit",
		HeadSHA: headSHA,
	}
}

// fakePulls serves one pull request, its repository, commits and stored changed paths.
type fakePulls struct {
	mu         sync.Mutex
	pull       *domain.Pull
	repo       *domain.Repo
	commits    []domain.Commit
	files      []string
	commitsErr error
}

func (f *fakePulls) Pull(_ context.Context, workspaceID, id string) (domain.Pull, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pull == nil || workspaceID != wsID || id != f.pull.ID {
		return domain.Pull{}, domain.ErrNotFound
	}
	return *f.pull, nil
}

func (f *fakePulls) Repo(_ context.Context, id string) (domain.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.repo == nil || id != f.repo.ID {
		return domain.Repo{}, domain.ErrNotFound
	}
	return *f.repo, nil
}

func (f *fakePulls) Commits(context.Context, string) ([]domain.Commit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits, f.commitsErr
}

func (f *fakePulls) ChangedFiles(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files, nil
}

func (f *fakePulls) setPullTitle(title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pull.Title = title
}

// fakeSettings answers with a settings bag, or fails.
type fakeSettings struct {
	mu  sync.Mutex
	bag map[string]json.RawMessage
	err error
}

func (f *fakeSettings) Get(context.Context, string) (map[string]json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bag, f.err
}

func (f *fakeSettings) setIntentModel(provider, model string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bag = map[string]json.RawMessage{
		"feature_models": json.RawMessage(`{"review_intent":{"provider":"` + provider + `","model":"` + model + `"}}`),
	}
}

func (f *fakeSettings) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// fakeLLM records the requests and answers through reply (the default answer when nil).
type fakeLLM struct {
	mu    sync.Mutex
	reqs  []llm.Request
	reply func(ctx context.Context, req llm.Request) (llm.Result, error)
}

const defaultAnswer = `{"intent":"Add a token-bucket rate limiter to the public webhook endpoints.","in_scope":["Rate limiting middleware","Limits in the config"],"out_of_scope":["Authentication changes"],"change_type":"feature"}`

func answer(data string) llm.Result {
	return llm.Result{Data: json.RawMessage(data), TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042), Attempts: 1}
}

func (f *fakeLLM) Structured(ctx context.Context, req llm.Request) (llm.Result, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	reply := f.reply
	f.mu.Unlock()
	if reply == nil {
		return answer(defaultAnswer), nil
	}
	return reply(ctx, req)
}

func (f *fakeLLM) setReply(reply func(ctx context.Context, req llm.Request) (llm.Result, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply = reply
}

func (f *fakeLLM) calls() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Request(nil), f.reqs...)
}

// blockUntilDone makes the model never answer: the call returns when its context ends.
func blockUntilDone(ctx context.Context, _ llm.Request) (llm.Result, error) {
	<-ctx.Done()
	return llm.Result{}, ctx.Err()
}

// fakeDocs answers from a table; a path that is not listed is a failed read.
type fakeDocs struct {
	mu    sync.Mutex
	reads map[string]app.DocRead
	calls []string
	// on, when set, may answer first (handled true).
	on func(ctx context.Context, path string) (app.DocRead, bool)
}

func (f *fakeDocs) Read(ctx context.Context, _ domain.Repo, sha, path string) app.DocRead {
	f.mu.Lock()
	f.calls = append(f.calls, sha+":"+path)
	on, read, ok := f.on, f.reads[path], false
	_, ok = f.reads[path]
	f.mu.Unlock()
	if on != nil {
		if r, handled := on(ctx, path); handled {
			return r
		}
	}
	if !ok {
		return app.DocRead{Reason: "not found"}
	}
	return read
}

// fakeTickets answers from a table; a number that is not listed is a failed read.
type fakeTickets struct {
	mu    sync.Mutex
	reads map[float64]app.TicketRead
	calls []float64
	on    func(ctx context.Context, n float64) (app.TicketRead, bool)
}

func (f *fakeTickets) Read(ctx context.Context, _ domain.Repo, n float64) app.TicketRead {
	f.mu.Lock()
	f.calls = append(f.calls, n)
	on, read, ok := f.on, f.reads[n], false
	_, ok = f.reads[n]
	f.mu.Unlock()
	if on != nil {
		if r, handled := on(ctx, n); handled {
			return r
		}
	}
	if !ok {
		return app.TicketRead{Reason: "Not Found"}
	}
	return read
}

// fakeStore keeps the rows in memory; Upsert rounds the cost to 6 decimals like numeric(12,6).
type fakeStore struct {
	mu      sync.Mutex
	rows    map[string]domain.Record
	upserts int
	getErr  error
	putErr  error
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]domain.Record{}} }

func (f *fakeStore) Get(_ context.Context, id string) (domain.Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return domain.Record{}, false, f.getErr
	}
	r, ok := f.rows[id]
	return r, ok, nil
}

func (f *fakeStore) Upsert(_ context.Context, rec domain.Record) (domain.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return domain.Record{}, f.putErr
	}
	if rec.CostUSD != nil {
		rec.CostUSD = ptr(math.Round(*rec.CostUSD*1e6) / 1e6)
	}
	f.upserts++
	f.rows[rec.PRID] = rec
	return rec, nil
}

func (f *fakeStore) put(rec domain.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[rec.PRID] = rec
}

func (f *fakeStore) row(id string) (domain.Record, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	return r, ok
}

func (f *fakeStore) upsertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upserts
}

// ev is one captured run event.
type ev struct{ Kind, Msg, Data string }

// capture is a Sink that records the events it receives.
type capture struct {
	mu     sync.Mutex
	events []ev
}

func (c *capture) sink(kind, msg string, data json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev{kind, msg, string(data)})
}

func (c *capture) got() []ev {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ev(nil), c.events...)
}

// world is a service over fakes with the usual pull request, repository and workspace.
type world struct {
	pulls    *fakePulls
	settings *fakeSettings
	llm      *fakeLLM
	docs     *fakeDocs
	tickets  *fakeTickets
	store    *fakeStore
	svc      *app.Service
}

func newWorld(t *testing.T, opts app.Options) *world {
	t.Helper()
	pull, repo := basePull(), domain.Repo{ID: repoID, Owner: "acme", Name: "api"}
	w := &world{
		pulls:    &fakePulls{pull: &pull, repo: &repo},
		settings: &fakeSettings{},
		llm:      &fakeLLM{},
		docs:     &fakeDocs{reads: map[string]app.DocRead{}},
		tickets:  &fakeTickets{reads: map[float64]app.TicketRead{}},
		store:    newFakeStore(),
	}
	w.svc = app.NewService(app.Deps{
		Pulls: w.pulls, Settings: w.settings, LLM: w.llm, Docs: w.docs, Tickets: w.tickets, Store: w.store,
		Clock: func() time.Time { return fixedNow },
	}, opts)
	return w
}

func (w *world) input(force bool) app.ResolveInput {
	return app.ResolveInput{
		WorkspaceID: wsID, Pull: *w.pulls.pull, Repo: *w.pulls.repo,
		ChangedFiles: []string{"src/middleware/ratelimit.ts", "src/config.ts"}, Force: force,
	}
}
