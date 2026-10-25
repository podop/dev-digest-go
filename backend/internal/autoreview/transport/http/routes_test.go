package autoreviewhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/autoreview/domain"
	autoreviewhttp "dev-digest-go/backend/internal/autoreview/transport/http"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	path        = "/settings/automatic-reviews-status"
)

type fakeAPI struct {
	status domain.Status
	err    error
	gotWS  []string
}

func (f *fakeAPI) Status(_ context.Context, ws string) (domain.Status, error) {
	f.gotWS = append(f.gotWS, ws)
	return f.status, f.err
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func serve(t *testing.T, api *fakeAPI) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		AllowedHosts: []string{"127.0.0.1"},
	})
	autoreviewhttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, host string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if host != "" {
		req.Host = host
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
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func TestStatus_Shape(t *testing.T) {
	polled := time.Date(2026, 10, 25, 9, 30, 15, 123_000_000, time.UTC)
	// The same instant in another zone must still be written as UTC.
	polledLocal := polled.In(time.FixedZone("x", 3*3600))
	tests := []struct {
		name   string
		status domain.Status
		want   string
	}{
		{
			"never polled: last_poll_at is null",
			domain.NewStatus(domain.StateOff, nil, 0, 5*time.Minute),
			`{"state":"off","last_poll_at":null,"prs_tracked":0,"poll_interval_min":5}`,
		},
		{
			"polled: ISO UTC with milliseconds",
			domain.NewStatus(domain.StateActive, &polled, 12, 15*time.Minute),
			`{"state":"active","last_poll_at":"2026-10-25T09:30:15.123Z","prs_tracked":12,"poll_interval_min":15}`,
		},
		{
			"non-UTC time is converted to UTC",
			domain.NewStatus(domain.StateActive, &polledLocal, 1, time.Minute),
			`{"state":"active","last_poll_at":"2026-10-25T09:30:15.123Z","prs_tracked":1,"poll_interval_min":1}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{status: tc.status}
			status, body := do(t, serve(t, api), http.MethodGet, "")
			if status != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200 (body %s)", path, status, body)
			}
			if body != tc.want {
				t.Errorf("GET %s body = %s, want %s", path, body, tc.want)
			}
			if diff := cmp.Diff([]string{workspaceID}, api.gotWS); diff != "" {
				t.Errorf("Status() workspaces mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStatus_EachStateString(t *testing.T) {
	states := []domain.State{
		domain.StateOff, domain.StatePollDisabled, domain.StateNoGitHubToken, domain.StateNoAgents,
		domain.StateNoTriggers, domain.StatePausedCap, domain.StateActive,
	}
	want := []string{
		"off", "poll_disabled", "no_github_token", "no_agents", "no_triggers", "paused_spend_cap", "active",
	}
	for i, st := range states {
		t.Run(want[i], func(t *testing.T) {
			api := &fakeAPI{status: domain.NewStatus(st, nil, 3, 5*time.Minute)}
			status, body := do(t, serve(t, api), http.MethodGet, "")
			if status != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200 (body %s)", path, status, body)
			}
			var got struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("decode %q: %v", body, err)
			}
			if got.State != want[i] {
				t.Errorf("state for %q = %q, want %q", st, got.State, want[i])
			}
		})
	}
}

func TestStatus_ReadErrorIs500Envelope(t *testing.T) {
	api := &fakeAPI{err: errors.New("read poll facts: db down")}
	status, body := do(t, serve(t, api), http.MethodGet, "")
	if status != http.StatusInternalServerError {
		t.Fatalf("GET %s status = %d, want 500 (body %s)", path, status, body)
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if env.Error.Code != "internal_error" {
		t.Errorf("error code = %q, want internal_error (body %s)", env.Error.Code, body)
	}
	if strings.Contains(body, "db down") {
		t.Errorf("500 body %s leaks the internal error", body)
	}
}

func TestStatus_GlobalGuardsApply(t *testing.T) {
	api := &fakeAPI{}
	srv := serve(t, api)
	status, body := do(t, srv, http.MethodGet, "evil.example")
	if status != http.StatusForbidden {
		t.Errorf("GET %s with a foreign Host: status = %d, want 403 (body %s)", path, status, body)
	}
	if len(api.gotWS) != 0 {
		t.Errorf("Status() called %d times behind a refused Host, want 0", len(api.gotWS))
	}
}
