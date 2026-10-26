package gitlab_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
)

// fakeNetStub serves handler on httptest's in-memory network so the bubble's clock drives every
// timeout.
func fakeNetStub(t *testing.T, handler http.HandlerFunc) (gitlab.Conn, *http.Client) {
	t.Helper()
	srv := httptest.NewTestServer(t, handler)
	hc := srv.Client() // starts the in-memory server; srv.URL is set after this
	return gitlab.Conn{BaseURL: srv.URL + "/api/v4", Token: testToken}, hc
}

func blockUntilGone(w http.ResponseWriter, r *http.Request) {
	<-r.Context().Done()
}

func TestTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		opts    gitlab.Options
		ctxWait time.Duration // caller deadline, 0 = none
		want    time.Duration
	}{
		{"default 30 s per call", gitlab.Options{}, 0, 30 * time.Second},
		{"configured timeout", gitlab.Options{Timeout: 5 * time.Second}, 0, 5 * time.Second},
		{"test call 15 s from the caller", gitlab.Options{}, gitlab.TestTimeout, 15 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				conn, hc := fakeNetStub(t, blockUntilGone)
				tc.opts.HTTP = hc
				tc.opts.Retry = &retry.Options{Retries: 3, BaseDelay: time.Second} // timeouts are not retried
				c := gitlab.New(tc.opts)
				ctx := t.Context()
				if tc.ctxWait > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.ctxWait)
					defer cancel()
				}
				start := time.Now()
				_, err := c.User(ctx, conn)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("User() error = %v, want a deadline error", err)
				}
				if took := time.Since(start); took != tc.want {
					t.Errorf("User() gave up after %v, want %v", took, tc.want)
				}
			})
		})
	}
}

func TestDiffs_DetailTimeoutBoundsThePageLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pages := 0
		conn, hc := fakeNetStub(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(20 * time.Second): // each page is inside the 30 s call limit
			case <-r.Context().Done():
				return
			}
			pages++
			writeJSON(w, 200, diffEntries(pages, 100))
		})
		c := gitlab.New(gitlab.Options{HTTP: hc, Retry: &retry.Options{}})
		start := time.Now()
		_, err := c.Diffs(t.Context(), conn, 42, 5)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Diffs() error = %v, want a deadline error", err)
		}
		if took := time.Since(start); took != gitlab.DefaultDetailTimeout {
			t.Errorf("Diffs() gave up after %v, want %v", took, gitlab.DefaultDetailTimeout)
		}
		if pages != 4 {
			t.Errorf("%d pages served, want 4 (the 5th is cut at 90 s)", pages)
		}
	})
}

func TestRetryBackoffRunsOnTheFakeClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		conn, hc := fakeNetStub(t, func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls <= 3 {
				writeJSON(w, 503, map[string]any{"message": "down"})
				return
			}
			writeJSON(w, 200, map[string]any{"username": "alice"})
		})
		c := gitlab.New(gitlab.Options{HTTP: hc, Retry: &retry.Options{
			Retries: 3, BaseDelay: time.Second, MaxDelay: 8 * time.Second,
			Jitter: func(time.Duration) time.Duration { return 0 },
		}})
		start := time.Now()
		u, err := c.User(t.Context(), conn)
		if err != nil || u.Username != "alice" {
			t.Fatalf("User() = %+v, %v, want alice", u, err)
		}
		if took := time.Since(start); took != 7*time.Second { // 1 + 2 + 4
			t.Errorf("User() took %v, want 7s of backoff", took)
		}
	})
}
