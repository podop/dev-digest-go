package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/platform/httpx"
)

func TestRequestLogLine(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		wantPath   string
		wantStatus float64
	}{
		{"success without query", http.MethodGet, "/repos?token=hunter2", "/repos", http.StatusOK},
		{"not found", http.MethodGet, "/nope", "/nope", http.StatusNotFound},
		{"validation error", http.MethodGet, "/pulls/abc", "/pulls/abc", http.StatusUnprocessableEntity},
		{"unexpected error", http.MethodGet, "/boom", "/boom", http.StatusInternalServerError},
		{"panic", http.MethodGet, "/panic", "/panic", http.StatusInternalServerError},
		{"head", http.MethodHead, "/ok", "/ok", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			e := newEcho(t, httpx.Options{Log: slog.New(slog.NewJSONHandler(&logs, nil))})
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Header.Set("Authorization", "Bearer abc-secret")
			req.Header.Set("Cookie", "session=cookie-secret")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			var lines []map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var m map[string]any
				if err := json.Unmarshal([]byte(raw), &m); err != nil {
					t.Fatalf("log line %q is not JSON: %v", raw, err)
				}
				if m["msg"] == "request" {
					lines = append(lines, m)
				}
			}
			if len(lines) != 1 {
				t.Fatalf("%s %s wrote %d request lines, want 1 (logs %s)", tc.method, tc.target, len(lines), logs.String())
			}
			line := lines[0]
			if line["method"] != tc.method || line["path"] != tc.wantPath || line["status"] != tc.wantStatus {
				t.Errorf("request line = method %v path %v status %v, want %s %s %v",
					line["method"], line["path"], line["status"], tc.method, tc.wantPath, tc.wantStatus)
			}
			if d, ok := line["duration_ms"].(float64); !ok || d < 0 {
				t.Errorf("duration_ms = %v, want a non-negative number", line["duration_ms"])
			}
			for _, secret := range []string{"abc-secret", "cookie-secret", "hunter2"} {
				if strings.Contains(logs.String(), secret) {
					t.Errorf("logs contain %q, want it absent", secret)
				}
			}
			if int(line["status"].(float64)) != rec.Code {
				t.Errorf("logged status %v != response status %d", line["status"], rec.Code)
			}
		})
	}
}
