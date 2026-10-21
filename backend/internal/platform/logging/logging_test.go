package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/logging"
)

func logLine(t *testing.T, level slog.Level, log func(*slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	log(logging.New(&buf, level))
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("log output is not one JSON object: %v\n%s", err, buf.String())
	}
	return got
}

func TestNew_redactsSensitiveFields(t *testing.T) {
	t.Parallel()
	const secretValue = "sk-zzz-do-not-log"
	tests := []struct {
		name string
		args []any
		path []string // where the sensitive value sits in the JSON line
	}{
		{"key", []any{"key", secretValue}, []string{"key"}},
		{"token", []any{"token", secretValue}, []string{"token"}},
		{"secret", []any{"secret", secretValue}, []string{"secret"}},
		{"password", []any{"password", secretValue}, []string{"password"}},
		{"apiKey", []any{"apiKey", secretValue}, []string{"apiKey"}},
		{"api_key", []any{"api_key", secretValue}, []string{"api_key"}},
		{"Authorization", []any{"Authorization", "Bearer " + secretValue}, []string{"Authorization"}},
		{"cookie", []any{"cookie", "sid=" + secretValue}, []string{"cookie"}},
		{"X-Api-Key", []any{"X-Api-Key", secretValue}, []string{"X-Api-Key"}},
		{"x_api_key", []any{"x_api_key", secretValue}, []string{"x_api_key"}},
		{"grouped", []any{slog.Group("req", "token", secretValue)}, []string{"req", "token"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logging.New(&buf, slog.LevelInfo).Info("hello", tc.args...)
			if strings.Contains(buf.String(), secretValue) {
				t.Fatalf("log line leaks the secret: %s", buf.String())
			}
			var line map[string]any
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatal(err)
			}
			var cur any = line
			for _, p := range tc.path {
				cur = cur.(map[string]any)[p]
			}
			if cur != logging.Redacted {
				t.Errorf("field %v = %v, want %q", tc.path, cur, logging.Redacted)
			}
		})
	}
}

func TestNew_redactsHeadersAndMaps(t *testing.T) {
	t.Parallel()
	const secretValue = "abc-secret-value"
	h := http.Header{}
	h.Set("Authorization", "Bearer "+secretValue)
	h.Set("Cookie", "sid="+secretValue)
	h.Set("X-Api-Key", secretValue)
	h.Set("Accept", "application/json")

	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelInfo)
	log.Info("req",
		"headers", h,
		"strs", map[string]string{"password": secretValue, "ok": "1"},
		"anys", map[string]any{"nested": map[string]any{"token": secretValue}, "n": 1},
	)
	if strings.Contains(buf.String(), secretValue) {
		t.Fatalf("log line leaks the secret: %s", buf.String())
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"Authorization": []any{logging.Redacted},
		"Cookie":        []any{logging.Redacted},
		"X-Api-Key":     []any{logging.Redacted},
		"Accept":        []any{"application/json"},
	}
	if diff := cmp.Diff(want, line["headers"]); diff != "" {
		t.Errorf("headers mismatch (-want +got):\n%s", diff)
	}
	wantStrs := map[string]any{"password": logging.Redacted, "ok": "1"}
	if diff := cmp.Diff(wantStrs, line["strs"]); diff != "" {
		t.Errorf("string map mismatch (-want +got):\n%s", diff)
	}
	wantAnys := map[string]any{"nested": map[string]any{"token": logging.Redacted}, "n": float64(1)}
	if diff := cmp.Diff(wantAnys, line["anys"]); diff != "" {
		t.Errorf("any map mismatch (-want +got):\n%s", diff)
	}
	if h.Get("Authorization") != "Bearer "+secretValue {
		t.Error("redaction mutated the caller's header")
	}
}

func TestNew_keepsOtherFieldsAndJSONShape(t *testing.T) {
	t.Parallel()
	got := logLine(t, slog.LevelInfo, func(l *slog.Logger) {
		l.Info("request", "method", "GET", "path", "/health", "status", 200, "duration_ms", 3, "keyword", "kept")
	})
	delete(got, "time")
	want := map[string]any{
		"level": "INFO", "msg": "request", "method": "GET", "path": "/health",
		"status": float64(200), "duration_ms": float64(3), "keyword": "kept",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("log line mismatch (-want +got):\n%s", diff)
	}
}

func TestNew_respectsLevel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := logging.New(&buf, slog.LevelWarn)
	log.Info("quiet")
	log.Warn("loud")
	if out := buf.String(); strings.Contains(out, "quiet") || !strings.Contains(out, "loud") {
		t.Errorf("level filter output = %q, want only the warn line", out)
	}
}
