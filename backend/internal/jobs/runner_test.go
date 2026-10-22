package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/jobs"
	"dev-digest-go/backend/internal/platform/logging"
)

// row is the fake store's copy of one jobs row.
type row struct {
	Kind     string
	Payload  string
	Status   jobs.Status
	Attempts int
	Error    string
	Started  bool
	Finished bool
}

// fakeStore is an in-memory jobs.Store that also tracks the peak number of running rows.
type fakeStore struct {
	mu        sync.Mutex
	rows      map[string]*row
	order     []string
	peak      int
	insertErr error
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]*row{}} }

func (s *fakeStore) Insert(_ context.Context, _, kind string, payload json.RawMessage) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertErr != nil {
		return "", s.insertErr
	}
	id := "job-" + strconv.Itoa(len(s.rows)+1)
	s.rows[id] = &row{Kind: kind, Payload: string(payload), Status: jobs.StatusQueued}
	s.order = append(s.order, id)
	return id, nil
}

func (s *fakeStore) MarkRunning(_ context.Context, id string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id].Status, s.rows[id].Started = jobs.StatusRunning, true
	running := 0
	for _, r := range s.rows {
		if r.Status == jobs.StatusRunning {
			running++
		}
	}
	s.peak = max(s.peak, running)
	return nil
}

func (s *fakeStore) SetAttempts(_ context.Context, id string, attempts int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id].Attempts = attempts
	return nil
}

func (s *fakeStore) Finish(_ context.Context, id string, status jobs.Status, attempts int, errMsg string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[id]
	r.Status, r.Attempts, r.Error, r.Finished = status, attempts, errMsg, true
	return nil
}

func (s *fakeStore) get(id string) row {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.rows[id]
}

func (s *fakeStore) peakRunning() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

type statusErr int

func (e statusErr) Error() string   { return fmt.Sprintf("status %d", int(e)) }
func (e statusErr) StatusCode() int { return int(e) }

// enqueue fails the test when Enqueue errors.
func enqueue(t *testing.T, r *jobs.Runner, kind string, payload any) string {
	t.Helper()
	id, err := r.Enqueue(t.Context(), "ws-1", kind, payload)
	if err != nil {
		t.Fatalf("Enqueue(%q) error = %v", kind, err)
	}
	return id
}

func TestRunner_Success(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		var got json.RawMessage
		r.Register("clone", func(_ context.Context, p json.RawMessage) error { got = p; return nil })

		id := enqueue(t, r, "clone", map[string]string{"repo": "o/r"})
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}

		want := row{Kind: "clone", Payload: `{"repo":"o/r"}`, Status: jobs.StatusDone, Attempts: 1, Started: true, Finished: true}
		if diff := cmp.Diff(want, store.get(id)); diff != "" {
			t.Errorf("row after success mismatch (-want +got):\n%s", diff)
		}
		if string(got) != want.Payload {
			t.Errorf("handler payload = %s, want %s", got, want.Payload)
		}
	})
}

func TestRunner_EnqueueReturnsBeforeTheWorkRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		release := make(chan struct{})
		r.Register("clone", func(ctx context.Context, _ json.RawMessage) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})

		id := enqueue(t, r, "clone", nil)
		synctest.Wait()
		if got := store.get(id).Status; got != jobs.StatusRunning {
			t.Errorf("status while handler blocks = %q, want running", got)
		}
		close(release)
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := store.get(id).Status; got != jobs.StatusDone {
			t.Errorf("status after release = %q, want done", got)
		}
	})
}

func TestRunner_NonTransientFailureIsNotRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		calls := 0
		r.Register("clone", func(context.Context, json.RawMessage) error {
			calls++
			return errors.New("git clone: repository not found")
		})

		id := enqueue(t, r, "clone", nil)
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}

		want := row{Kind: "clone", Payload: "null", Status: jobs.StatusFailed, Attempts: 0,
			Error: "git clone: repository not found", Started: true, Finished: true}
		if diff := cmp.Diff(want, store.get(id)); diff != "" {
			t.Errorf("row after failure mismatch (-want +got):\n%s", diff)
		}
		if calls != 1 {
			t.Errorf("handler calls = %d, want 1", calls)
		}
	})
}

func TestRunner_TransientFailures(t *testing.T) {
	tests := []struct {
		name         string
		failures     int // leading calls that fail with HTTP 503
		wantCalls    int
		wantStatus   jobs.Status
		wantAttempts int
	}{
		{name: "recovers on the second try", failures: 1, wantCalls: 2, wantStatus: jobs.StatusDone, wantAttempts: 1},
		{name: "recovers on the last retry", failures: 2, wantCalls: 3, wantStatus: jobs.StatusDone, wantAttempts: 1},
		{name: "gives up after two retries", failures: 3, wantCalls: 3, wantStatus: jobs.StatusFailed, wantAttempts: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeStore()
				r := jobs.New(store, jobs.Options{})
				calls := 0
				r.Register("sync", func(context.Context, json.RawMessage) error {
					calls++
					if calls <= tc.failures {
						return statusErr(503)
					}
					return nil
				})

				id := enqueue(t, r, "sync", nil)
				if err := r.Wait(t.Context()); err != nil {
					t.Fatal(err)
				}

				got := store.get(id)
				if calls != tc.wantCalls || got.Status != tc.wantStatus || got.Attempts != tc.wantAttempts {
					t.Errorf("calls, status, attempts = %d, %q, %d; want %d, %q, %d",
						calls, got.Status, got.Attempts, tc.wantCalls, tc.wantStatus, tc.wantAttempts)
				}
			})
		})
	}
}

func TestRunner_RetriesDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{Retries: -1})
		calls := 0
		r.Register("sync", func(context.Context, json.RawMessage) error { calls++; return statusErr(503) })

		id := enqueue(t, r, "sync", nil)
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := store.get(id); calls != 1 || got.Status != jobs.StatusFailed {
			t.Errorf("calls, status = %d, %q; want 1, failed", calls, got.Status)
		}
	})
}

// AC65: a handler that exceeds the attempt timeout ends failed with a timeout error, no retry.
func TestRunner_AttemptTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		calls := 0
		r.Register("clone", func(ctx context.Context, _ json.RawMessage) error {
			calls++
			<-ctx.Done()
			return ctx.Err()
		})

		start := time.Now()
		id := enqueue(t, r, "clone", nil)
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}

		got := store.get(id)
		if got.Status != jobs.StatusFailed || got.Error != "attempt timed out after 2m0s: context deadline exceeded" {
			t.Errorf("status, error = %q, %q; want failed with the timeout text", got.Status, got.Error)
		}
		if calls != 1 {
			t.Errorf("handler calls = %d, want 1 (a timeout is not transient)", calls)
		}
		if elapsed := time.Since(start); elapsed != jobs.DefaultTimeout {
			t.Errorf("job took %v, want %v", elapsed, jobs.DefaultTimeout)
		}
	})
}

// AC303 (runner): a kind registered with its own timeout is cut at that timeout, a kind
// registered without one at the runner default, both under the same runner.
func TestRunner_PerKindTimeout(t *testing.T) {
	const long = 300 * time.Second
	tests := []struct {
		name     string
		opts     []jobs.RegisterOption
		wantTook time.Duration
		wantErr  string
	}{
		{name: "own timeout", opts: []jobs.RegisterOption{jobs.WithTimeout(long)}, wantTook: long,
			wantErr: "attempt timed out after 5m0s: context deadline exceeded"},
		{name: "default timeout", wantTook: jobs.DefaultTimeout,
			wantErr: "attempt timed out after 2m0s: context deadline exceeded"},
		{name: "non-positive timeout keeps the default", opts: []jobs.RegisterOption{jobs.WithTimeout(0)},
			wantTook: jobs.DefaultTimeout, wantErr: "attempt timed out after 2m0s: context deadline exceeded"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeStore()
				r := jobs.New(store, jobs.Options{})
				r.Register("kind", func(ctx context.Context, _ json.RawMessage) error {
					<-ctx.Done()
					return ctx.Err()
				}, tc.opts...)

				start := time.Now()
				id := enqueue(t, r, "kind", nil)
				if err := r.Wait(t.Context()); err != nil {
					t.Fatal(err)
				}

				got := store.get(id)
				if got.Status != jobs.StatusFailed || got.Error != tc.wantErr {
					t.Errorf("status, error = %q, %q; want failed, %q", got.Status, got.Error, tc.wantErr)
				}
				if elapsed := time.Since(start); elapsed != tc.wantTook {
					t.Errorf("job took %v, want %v", elapsed, tc.wantTook)
				}
			})
		})
	}
}

// A handler registered with a longer limit survives past the default and finishes done.
func TestRunner_PerKindTimeoutOutlivesDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		r.Register("slow", func(ctx context.Context, _ json.RawMessage) error {
			select {
			case <-time.After(jobs.DefaultTimeout + 60*time.Second):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, jobs.WithTimeout(300*time.Second))

		id := enqueue(t, r, "slow", nil)
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := store.get(id).Status; got != jobs.StatusDone {
			t.Errorf("status = %q, want done", got)
		}
	})
}

func TestRunner_PanicBecomesFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		r.Register("clone", func(context.Context, json.RawMessage) error { panic("boom") })

		id := enqueue(t, r, "clone", nil)
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := store.get(id); got.Status != jobs.StatusFailed || got.Error != "handler panic: boom" {
			t.Errorf("status, error = %q, %q; want failed, handler panic: boom", got.Status, got.Error)
		}
	})
}

// AC64: five jobs against a slow handler never run more than three at once.
func TestRunner_ConcurrencyLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		r.Register("clone", func(ctx context.Context, _ json.RawMessage) error {
			select {
			case <-time.After(10 * time.Second):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})

		ids := make([]string, 5)
		for i := range ids {
			ids[i] = enqueue(t, r, "clone", nil)
		}
		synctest.Wait()
		queued := 0
		for _, id := range ids {
			if store.get(id).Status == jobs.StatusQueued {
				queued++
			}
		}
		if queued != 2 {
			t.Errorf("queued rows while three run = %d, want 2", queued)
		}
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := store.peakRunning(); got != 3 {
			t.Errorf("peak running rows = %d, want 3", got)
		}
		for _, id := range ids {
			if got := store.get(id).Status; got != jobs.StatusDone {
				t.Errorf("job %s status = %q, want done", id, got)
			}
		}
	})
}

func TestRunner_ConcurrencyOption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{Concurrency: 1})
		r.Register("clone", func(ctx context.Context, _ json.RawMessage) error {
			<-time.After(time.Second)
			return nil
		})
		for range 3 {
			enqueue(t, r, "clone", nil)
		}
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := store.peakRunning(); got != 1 {
			t.Errorf("peak running rows = %d, want 1", got)
		}
	})
}

// AC94 (runner part): shutdown aborts the running job, leaves the unstarted one queued and
// refuses new work.
func TestRunner_ShutdownKeepsQueuedRowsQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{Concurrency: 1})
		calls := 0
		r.Register("clone", func(ctx context.Context, _ json.RawMessage) error {
			calls++
			<-ctx.Done()
			return ctx.Err()
		})

		first := enqueue(t, r, "clone", nil)
		second := enqueue(t, r, "clone", nil)
		synctest.Wait()

		if err := r.Shutdown(jobs.DefaultShutdownTimeout); err != nil {
			t.Fatalf("Shutdown() = %v, want nil", err)
		}
		if got := store.get(first); got.Status != jobs.StatusFailed || got.Error != "aborted by shutdown: context canceled" || got.Attempts != 0 {
			t.Errorf("running job after shutdown = %+v, want failed, aborted by shutdown, attempts 0", got)
		}
		if got := store.get(second); got.Status != jobs.StatusQueued || got.Started {
			t.Errorf("queued job after shutdown = %+v, want untouched queued", got)
		}
		if calls != 1 {
			t.Errorf("handler calls = %d, want 1", calls)
		}
		if _, err := r.Enqueue(t.Context(), "ws-1", "clone", nil); !errors.Is(err, jobs.ErrShuttingDown) {
			t.Errorf("Enqueue after Shutdown error = %v, want ErrShuttingDown", err)
		}
		if err := r.Wait(t.Context()); err != nil {
			t.Errorf("Wait() after Shutdown = %v, want nil", err)
		}
		if err := r.Shutdown(time.Second); err != nil {
			t.Errorf("second Shutdown() = %v, want nil", err)
		}
	})
}

func TestRunner_ShutdownTimeoutLeavesRowRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		stuck := make(chan struct{})
		r.Register("clone", func(context.Context, json.RawMessage) error { <-stuck; return nil }) // ignores ctx

		id := enqueue(t, r, "clone", nil)
		synctest.Wait()

		start := time.Now()
		if err := r.Shutdown(jobs.DefaultShutdownTimeout); !errors.Is(err, jobs.ErrShutdownTimeout) {
			t.Fatalf("Shutdown() = %v, want ErrShutdownTimeout", err)
		}
		if elapsed := time.Since(start); elapsed != jobs.DefaultShutdownTimeout {
			t.Errorf("Shutdown took %v, want %v", elapsed, jobs.DefaultShutdownTimeout)
		}
		if got := store.get(id).Status; got != jobs.StatusRunning {
			t.Errorf("status of the unsettled job = %q, want running", got)
		}
		close(stuck) // let the goroutine end inside the bubble
		synctest.Wait()
	})
}

func TestRunner_NoRetryWhileShuttingDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		release := make(chan struct{})
		calls := 0
		r.Register("sync", func(context.Context, json.RawMessage) error {
			calls++
			<-release
			return statusErr(503)
		})

		id := enqueue(t, r, "sync", nil)
		synctest.Wait()
		shutdownErr := make(chan error, 1)
		go func() { shutdownErr <- r.Shutdown(jobs.DefaultShutdownTimeout) }()
		synctest.Wait() // Shutdown has cancelled the lifecycle and now waits for the handler
		close(release)

		if err := <-shutdownErr; err != nil {
			t.Fatalf("Shutdown() = %v, want nil", err)
		}
		if got := store.get(id); calls != 1 || got.Status != jobs.StatusFailed || got.Attempts != 0 {
			t.Errorf("calls, status, attempts = %d, %q, %d; want 1, failed, 0", calls, got.Status, got.Attempts)
		}
	})
}

func TestRunner_Enqueue_Errors(t *testing.T) {
	t.Run("unknown kind", func(t *testing.T) {
		store := newFakeStore()
		r := jobs.New(store, jobs.Options{})
		if _, err := r.Enqueue(t.Context(), "ws-1", "nope", nil); !errors.Is(err, jobs.ErrUnknownKind) {
			t.Errorf("Enqueue(unknown kind) error = %v, want ErrUnknownKind", err)
		}
		if len(store.rows) != 0 {
			t.Errorf("rows after unknown kind = %d, want 0", len(store.rows))
		}
	})
	t.Run("insert fails", func(t *testing.T) {
		store := newFakeStore()
		store.insertErr = errors.New("db down")
		r := jobs.New(store, jobs.Options{})
		r.Register("clone", func(context.Context, json.RawMessage) error { return nil })
		if _, err := r.Enqueue(t.Context(), "ws-1", "clone", nil); !errors.Is(err, store.insertErr) {
			t.Errorf("Enqueue() error = %v, want it to wrap the insert error", err)
		}
	})
	t.Run("payload not marshallable", func(t *testing.T) {
		r := jobs.New(newFakeStore(), jobs.Options{})
		r.Register("clone", func(context.Context, json.RawMessage) error { return nil })
		if _, err := r.Enqueue(t.Context(), "ws-1", "clone", make(chan int)); err == nil {
			t.Error("Enqueue(chan payload) error = nil, want a marshal error")
		}
	})
}

func TestRunner_Register_Panics(t *testing.T) {
	r := jobs.New(newFakeStore(), jobs.Options{})
	r.Register("clone", func(context.Context, json.RawMessage) error { return nil })
	for name, fn := range map[string]func(){
		"duplicate kind": func() { r.Register("clone", func(context.Context, json.RawMessage) error { return nil }) },
		"nil handler":    func() { r.Register("other", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("Register did not panic")
				}
			}()
			fn()
		})
	}
}

func TestRunner_WaitHonoursContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := jobs.New(newFakeStore(), jobs.Options{})
		r.Register("clone", func(ctx context.Context, _ json.RawMessage) error { <-ctx.Done(); return ctx.Err() })
		enqueue(t, r, "clone", nil)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := r.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Wait() = %v, want deadline exceeded", err)
		}
		if err := r.Shutdown(time.Second); err != nil {
			t.Fatal(err)
		}
	})
}

// AC101 / NFR9: one log line per finished job with kind, id, status, attempts and duration.
func TestRunner_LogsOneLinePerFinishedJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		r := jobs.New(newFakeStore(), jobs.Options{Logger: logging.New(&buf, slog.LevelInfo)})
		r.Register("clone", func(context.Context, json.RawMessage) error {
			time.Sleep(1500 * time.Millisecond)
			return nil
		})
		id := enqueue(t, r, "clone", nil)
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}

		var line map[string]any
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
			t.Fatalf("log output %q is not one JSON line: %v", buf.String(), err)
		}
		want := map[string]any{"kind": "clone", "job_id": id, "status": "done", "attempts": float64(1), "duration_ms": float64(1500)}
		for k, v := range want {
			if line[k] != v {
				t.Errorf("log field %s = %v, want %v", k, line[k], v)
			}
		}
	})
}
