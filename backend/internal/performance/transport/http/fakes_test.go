package performancehttp_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/performance/app"
	"dev-digest-go/backend/internal/performance/domain"
	performancehttp "dev-digest-go/backend/internal/performance/transport/http"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	agentID     = "7e1d0000-0000-4000-8000-000000000001"
	uiOrigin    = "http://localhost:3100"
)

// call is one recorded use-case call.
type call struct {
	Method, WorkspaceID, AgentID string
	Query                        domain.PeriodQuery
}

// fakeAPI implements app.API with canned answers and records its calls.
type fakeAPI struct {
	mu    sync.Mutex
	calls []call

	err   error
	perf  domain.AgentPerf
	stats domain.AgentStats
}

var _ app.API = (*fakeAPI)(nil)

func (f *fakeAPI) record(c call) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeAPI) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakeAPI) Dashboard(_ context.Context, ws string, q domain.PeriodQuery) (domain.AgentPerf, error) {
	f.record(call{Method: "Dashboard", WorkspaceID: ws, Query: q})
	return f.perf, f.err
}

func (f *fakeAPI) Stats(_ context.Context, ws, id string, q domain.PeriodQuery) (domain.AgentStats, error) {
	f.record(call{Method: "Stats", WorkspaceID: ws, AgentID: id, Query: q})
	return f.stats, f.err
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

// newServer serves the performance routes of api; extra registers more routes first (to prove
// which route wins).
func newServer(t *testing.T, api app.API, extra ...func(*echo.Echo)) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{"127.0.0.1"},
	})
	for _, add := range extra {
		add(e)
	}
	performancehttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

// response is a finished exchange.
type response struct {
	Status int
	Body   string
}

func get(t *testing.T, srv *httptest.Server, path string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of GET %s: %v", path, err)
	}
	return response{Status: resp.StatusCode, Body: string(b)}
}
