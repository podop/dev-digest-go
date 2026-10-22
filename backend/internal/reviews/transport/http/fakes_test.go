package reviewshttp_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
	reviewshttp "dev-digest-go/backend/internal/reviews/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID   = "11111111-2222-4333-8444-555555555555"
	otherID       = "7e1d0000-0000-4000-8000-000000000001"
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
)

// call is one recorded use-case call.
type call struct {
	Method, WorkspaceID, ID string
}

// fakeAPI implements app.API with canned answers; every method records its call.
type fakeAPI struct {
	mu    sync.Mutex
	calls []call

	err error // returned by every method when set

	startReq     app.StartRequest
	startRes     app.StartResult
	active       []domain.ActiveRun
	runs         []domain.RunSummary
	deleted      bool
	events       func() (app.Subscription, error)
	trace        domain.Trace
	reviews      []domain.ReviewRecord
	finding      domain.FindingRecord
	multi        domain.MultiAgentRun
	multiList    []domain.MultiAgentSummary
	gotLimit     *int
	estimates    []domain.AgentRunEstimate
	learn        app.LearnResult
	cancelCalled int
}

var _ app.API = (*fakeAPI)(nil)

func (f *fakeAPI) record(method, ws, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{method, ws, id})
}

func (f *fakeAPI) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeAPI) Start(_ context.Context, ws, prID string, req app.StartRequest) (app.StartResult, error) {
	f.record("Start", ws, prID)
	f.mu.Lock()
	f.startReq = req
	f.mu.Unlock()
	return f.startRes, f.err
}

func (f *fakeAPI) ActiveRuns(_ context.Context, ws, id string) ([]domain.ActiveRun, error) {
	f.record("ActiveRuns", ws, id)
	return f.active, f.err
}

func (f *fakeAPI) ListRuns(_ context.Context, ws, id string) ([]domain.RunSummary, error) {
	f.record("ListRuns", ws, id)
	return f.runs, f.err
}

func (f *fakeAPI) DeleteRun(_ context.Context, ws, id string) (bool, error) {
	f.record("DeleteRun", ws, id)
	return f.deleted, f.err
}

func (f *fakeAPI) CancelRun(_ context.Context, ws, id string) error {
	f.record("CancelRun", ws, id)
	return f.err
}

func (f *fakeAPI) Events(_ context.Context, ws, id string) (app.Subscription, error) {
	f.record("Events", ws, id)
	if f.err != nil {
		return nil, f.err
	}
	return f.events()
}

func (f *fakeAPI) Trace(_ context.Context, ws, id string) (domain.Trace, error) {
	f.record("Trace", ws, id)
	return f.trace, f.err
}

func (f *fakeAPI) Reviews(_ context.Context, ws, id string) ([]domain.ReviewRecord, error) {
	f.record("Reviews", ws, id)
	return f.reviews, f.err
}

func (f *fakeAPI) DeleteReview(_ context.Context, ws, id string) error {
	f.record("DeleteReview", ws, id)
	return f.err
}

func (f *fakeAPI) AcceptFinding(_ context.Context, ws, id string) (domain.FindingRecord, error) {
	f.record("AcceptFinding", ws, id)
	return f.finding, f.err
}

func (f *fakeAPI) DismissFinding(_ context.Context, ws, id string) (domain.FindingRecord, error) {
	f.record("DismissFinding", ws, id)
	return f.finding, f.err
}

func (f *fakeAPI) MultiAgentRun(_ context.Context, ws, id string) (domain.MultiAgentRun, error) {
	f.record("MultiAgentRun", ws, id)
	return f.multi, f.err
}

func (f *fakeAPI) ListMultiAgentRuns(_ context.Context, ws, id string, limit *int) ([]domain.MultiAgentSummary, error) {
	f.record("ListMultiAgentRuns", ws, id)
	f.mu.Lock()
	f.gotLimit = limit
	f.mu.Unlock()
	return f.multiList, f.err
}

func (f *fakeAPI) Estimates(_ context.Context, ws string) ([]domain.AgentRunEstimate, error) {
	f.record("Estimates", ws, "")
	return f.estimates, f.err
}

func (f *fakeAPI) Learn(_ context.Context, ws, id string) (app.LearnResult, error) {
	f.record("Learn", ws, id)
	return f.learn, f.err
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

// newServer serves the review routes of api on a real socket (SSE needs one).
func newServer(t *testing.T, api *fakeAPI) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{"127.0.0.1"},
	})
	reviewshttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

// response is a finished exchange.
type response struct {
	Status int
	Header http.Header
	Body   string
}

// send performs the request and reads the whole response (the stream must end by itself).
func send(t *testing.T, srv *httptest.Server, method, path, body string, headers ...string) response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s %s: %v", method, path, err)
	}
	return response{Status: resp.StatusCode, Header: resp.Header, Body: string(b)}
}

// chanSub is a subscription over a channel the test feeds; closed counts Close calls.
type chanSub struct {
	ch     chan domain.RunEvent
	mu     sync.Mutex
	closed int
	done   chan struct{}
	once   sync.Once
}

func newChanSub(events ...domain.RunEvent) *chanSub {
	s := &chanSub{ch: make(chan domain.RunEvent, len(events)+16), done: make(chan struct{})}
	for _, e := range events {
		s.ch <- e
	}
	return s
}

func (s *chanSub) C() <-chan domain.RunEvent { return s.ch }

func (s *chanSub) Close() {
	s.mu.Lock()
	s.closed++
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}

func (s *chanSub) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return ctx, cancel
}
