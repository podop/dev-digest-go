package wire_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/llm/adapters/internal/wire"
)

func TestDo(t *testing.T) {
	t.Parallel()
	const key = "k-secret-1"
	tests := []struct {
		name       string
		status     int
		body       string
		withMsg    bool
		wantErr    string
		wantStatus int
	}{
		{"ok", 200, `{"v":"x"}`, true, "", 0},
		{"status with message scrubbed", 429, `{"error":{"message":"bad ` + key + `"}}`, true, "P op returned 429: bad [REDACTED]", 429},
		{"status without message", 500, `{"error":{"message":"secret detail"}}`, false, "P op returned 500", 500},
		{"unreadable", 200, `nope`, true, "P op returned an unreadable response", 0},
		{"long message is cut", 400, `{"error":{"message":"` + strings.Repeat("é", 400) + `"}}`, true, "", 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			hc := srv.Client()
			var out struct{ V string }
			err := wire.Do(t.Context(), hc, wire.Call{Provider: "P", Op: "op", Method: http.MethodPost, URL: srv.URL + "/x", Body: map[string]int{"a": 1}, Key: key, ErrorMessage: tc.withMsg}, &out)
			switch {
			case tc.status == 200 && tc.wantErr == "":
				if err != nil || out.V != "x" {
					t.Errorf("Do() = %v, %q; want nil, x", err, out.V)
				}
			case tc.wantErr != "":
				if err == nil || err.Error() != tc.wantErr {
					t.Errorf("Do() error = %v, want %q", err, tc.wantErr)
				}
			}
			var se *wire.StatusError
			if tc.wantStatus != 0 {
				if !errors.As(err, &se) || se.StatusCode() != tc.wantStatus {
					t.Errorf("Do() error = %v, want a StatusError with %d", err, tc.wantStatus)
				}
				if se != nil && len(se.Message) > 300 {
					t.Errorf("message length = %d, want at most 300 bytes", len(se.Message))
				}
			}
		})
	}
}

func TestDo_transportErrorHasNoKey(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := wire.Do(ctx, http.DefaultClient, wire.Call{Provider: "P", Op: "op", Method: http.MethodGet, URL: "http://127.0.0.1:1/x", Headers: map[string]string{"Authorization": "Bearer abc"}, Key: "abc"}, new(struct{}))
	if err == nil || strings.Contains(err.Error(), "abc") || !errors.Is(err, context.Canceled) {
		t.Errorf("Do() error = %v, want a cancelled error without the key", err)
	}
}
