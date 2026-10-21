package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/httpx"
)

func TestAppErrorStatus(t *testing.T) {
	tests := []struct {
		kind       string
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{"invalid_input", http.StatusBadRequest, "invalid_input", "bad value"},
		{"not_found", http.StatusNotFound, "not_found", "no such thing"},
		{"conflict", http.StatusConflict, "conflict", "already there"},
		{"payload_too_large", http.StatusRequestEntityTooLarge, "payload_too_large", "too big"},
		{"validation", http.StatusUnprocessableEntity, "validation_error", "invalid"},
		{"internal", http.StatusInternalServerError, "internal_error", "broken"},
		{"config", http.StatusInternalServerError, "config_error", "missing setting"},
		{"external_service", http.StatusBadGateway, "external_service", "upstream down"},
		{"custom_code", http.StatusBadGateway, "github_unavailable", "GitHub unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			newTestEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apperr/"+tc.kind, nil))

			if rec.Code != tc.wantStatus {
				t.Errorf("GET /apperr/%s status = %d, want %d", tc.kind, rec.Code, tc.wantStatus)
			}
			var raw map[string]map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("GET /apperr/%s: decode body %q: %v", tc.kind, rec.Body.String(), err)
			}
			want := map[string]map[string]any{"error": {"code": tc.wantCode, "message": tc.wantMsg}}
			if diff := cmp.Diff(want, raw); diff != "" {
				t.Errorf("GET /apperr/%s body mismatch (-want +got):\n%s", tc.kind, diff)
			}
		})
	}
}

func TestAppErrorCauseNotExposed(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apperr/external_service", nil))
	if strings.Contains(rec.Body.String(), "10.0.0.1") {
		t.Errorf("response body %q leaks the error cause", rec.Body.String())
	}
}

func TestAppErrorUnknownKindIsInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apperr/whatever", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `{"error":{"code":"internal_error","message":"Internal error"}}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestAppErrorWrappedKeepsDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestEcho(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apperr-wrapped", nil))

	var got httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	want := httpx.ErrorBody{Error: httpx.ErrorDetail{
		Code: "conflict", Message: "already added", Details: map[string]any{"repo": "a/b"},
	}}
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}

func TestUnexpectedErrorIsLoggedNotEchoed(t *testing.T) {
	var logs bytes.Buffer
	e := newTestEchoLog(t, slog.New(slog.NewJSONHandler(&logs, nil)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /boom status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "leaked") {
		t.Errorf("GET /boom body %q echoes the internal message", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "db password leaked in message") {
		t.Errorf("GET /boom logs %q do not contain the internal message", logs.String())
	}
}
