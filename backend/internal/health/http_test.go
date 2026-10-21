package health_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/health"
	"dev-digest-go/backend/internal/platform/httpx"
)

type fakePinger struct{ err error }

func (f fakePinger) PingContext(context.Context) error { return f.err }

func TestRoutes(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		pingErr    error
		wantStatus int
		wantBody   string
	}{
		{name: "health", path: "/health", wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "health ignores db", path: "/health", pingErr: errors.New("down"), wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "ready", path: "/health/ready", wantStatus: http.StatusOK, wantBody: `{"ready":true}`},
		{name: "ready db down", path: "/health/ready", pingErr: errors.New("down"), wantStatus: http.StatusServiceUnavailable, wantBody: `{"ready":false}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := slog.New(slog.DiscardHandler)
			e := httpx.New(nil, log)
			health.AddRoutes(e, fakePinger{err: tc.pingErr}, log)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Errorf("GET %s status = %d, want %d", tc.path, rec.Code, tc.wantStatus)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
				t.Errorf("GET %s body = %s, want %s", tc.path, got, tc.wantBody)
			}
		})
	}
}
