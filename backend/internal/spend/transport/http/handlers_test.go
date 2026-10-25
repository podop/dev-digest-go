package spendhttp_test

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

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/spend/app"
	"dev-digest-go/backend/internal/spend/domain"
	spendhttp "dev-digest-go/backend/internal/spend/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

const workspaceID = "11111111-2222-4333-8444-555555555555"

type fakeAPI struct {
	status domain.Status
	err    error
	gotWS  []string
}

var _ app.API = (*fakeAPI)(nil)

func (f *fakeAPI) Status(_ context.Context, ws string) (domain.Status, error) {
	f.gotWS = append(f.gotWS, ws)
	return f.status, f.err
}

func (f *fakeAPI) Admit(context.Context, string, string) error { return nil }

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func serve(t *testing.T, api app.API) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		AllowedHosts: []string{"127.0.0.1"},
	})
	spendhttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path string, host string) (status int, body string) {
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

func micros(v domain.Micros) *domain.Micros { return &v }

func TestStatus_Shape(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		status domain.Status
		want   string
	}{
		{
			"no cap: cap and remaining are null",
			domain.Status{MonthStart: month, Spent: 12_340_000},
			`{"month_start":"2026-10-01T00:00:00.000Z","spent_usd":12.34,"cap_usd":null,"remaining_usd":null,"cap_reached":false,"unpriced_count":0}`,
		},
		{
			"under the cap",
			domain.Status{MonthStart: month, Spent: 12_340_000, Cap: micros(50_000_000)},
			`{"month_start":"2026-10-01T00:00:00.000Z","spent_usd":12.34,"cap_usd":50,"remaining_usd":37.66,"cap_reached":false,"unpriced_count":0}`,
		},
		{
			"over the cap: remaining is 0, not negative",
			domain.Status{MonthStart: month, Spent: 50_120_000, Cap: micros(50_000_000)},
			`{"month_start":"2026-10-01T00:00:00.000Z","spent_usd":50.12,"cap_usd":50,"remaining_usd":0,"cap_reached":true,"unpriced_count":0}`,
		},
		{
			"cap 0 is reached from the start",
			domain.Status{MonthStart: month, Cap: micros(0)},
			`{"month_start":"2026-10-01T00:00:00.000Z","spent_usd":0,"cap_usd":0,"remaining_usd":0,"cap_reached":true,"unpriced_count":0}`,
		},
		{
			"six decimals on the wire",
			domain.Status{MonthStart: month, Spent: 1, Cap: micros(1_000_001)},
			`{"month_start":"2026-10-01T00:00:00.000Z","spent_usd":0.000001,"cap_usd":1.000001,"remaining_usd":1,"cap_reached":false,"unpriced_count":0}`,
		},
		{
			"unpriced calls are counted, not summed",
			domain.Status{MonthStart: month, Spent: 12_340_000, Unpriced: 3},
			`{"month_start":"2026-10-01T00:00:00.000Z","spent_usd":12.34,"cap_usd":null,"remaining_usd":null,"cap_reached":false,"unpriced_count":3}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{status: tc.status}
			status, body := do(t, serve(t, api), http.MethodGet, "/settings/spend", "")
			if status != http.StatusOK {
				t.Fatalf("GET /settings/spend status = %d, want 200 (body %s)", status, body)
			}
			if body != tc.want {
				t.Errorf("GET /settings/spend body = %s, want %s", body, tc.want)
			}
			if diff := cmp.Diff([]string{workspaceID}, api.gotWS); diff != "" {
				t.Errorf("Status() workspaces mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStatus_ReadErrorIs500Envelope(t *testing.T) {
	api := &fakeAPI{err: errors.New("read month spend: db down")}
	status, body := do(t, serve(t, api), http.MethodGet, "/settings/spend", "")
	if status != http.StatusInternalServerError {
		t.Fatalf("GET /settings/spend status = %d, want 500 (body %s)", status, body)
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
	srv := serve(t, &fakeAPI{})
	status, body := do(t, srv, http.MethodGet, "/settings/spend", "evil.example")
	if status != http.StatusForbidden {
		t.Errorf("GET /settings/spend with a foreign Host: status = %d, want 403 (body %s)", status, body)
	}
}
