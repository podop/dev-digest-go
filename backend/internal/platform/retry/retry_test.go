package retry_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"dev-digest-go/backend/internal/platform/retry"
)

type statusErr int

func (e statusErr) Error() string   { return fmt.Sprintf("status %d", int(e)) }
func (e statusErr) StatusCode() int { return int(e) }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func TestIsTransient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", statusErr(429), true},
		{"500", statusErr(500), true},
		{"503", statusErr(503), true},
		{"wrapped 502", fmt.Errorf("list: %w", statusErr(502)), true},
		{"404", statusErr(404), false},
		{"422", statusErr(422), false},
		{"401", statusErr(401), false},
		{"connection reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{"connect timeout", syscall.ETIMEDOUT, true},
		{"dns", &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"net timeout", timeoutErr{}, true},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"wrapped deadline", fmt.Errorf("get: %w", context.DeadlineExceeded), false},
		{"canceled", context.Canceled, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := retry.IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

func noJitter(time.Duration) time.Duration { return 0 }

func TestDo_succeedsAfterTransientFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		var delays []time.Duration
		last := time.Now()
		opts := retry.Default()
		opts.Jitter = noJitter
		err := retry.Do(t.Context(), opts, func(context.Context) error {
			now := time.Now()
			if calls > 0 {
				delays = append(delays, now.Sub(last))
			}
			last = now
			calls++
			if calls < 3 {
				return statusErr(503)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Do() = %v, want nil", err)
		}
		if calls != 3 {
			t.Errorf("fn calls = %d, want 3", calls)
		}
		want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}
		if len(delays) != len(want) || delays[0] != want[0] || delays[1] != want[1] {
			t.Errorf("backoff delays = %v, want %v", delays, want)
		}
	})
}

func TestDo_stopsAfterRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		var retried []int
		opts := retry.Default()
		opts.OnRetry = func(attempt int, _ error) { retried = append(retried, attempt) }
		err := retry.Do(t.Context(), opts, func(context.Context) error {
			calls++
			return statusErr(503)
		})
		if !errors.Is(err, statusErr(503)) {
			t.Errorf("Do() = %v, want the last 503", err)
		}
		if calls != 4 {
			t.Errorf("fn calls = %d, want 4 (1 + 3 retries)", calls)
		}
		if len(retried) != 3 || retried[0] != 1 || retried[2] != 3 {
			t.Errorf("OnRetry attempts = %v, want [1 2 3]", retried)
		}
	})
}

func TestDo_nonTransientRunsOnce(t *testing.T) {
	t.Parallel()
	for _, err := range []error{statusErr(404), context.DeadlineExceeded, errors.New("boom")} {
		calls := 0
		got := retry.Do(t.Context(), retry.Default(), func(context.Context) error {
			calls++
			return err
		})
		if !errors.Is(got, err) {
			t.Errorf("Do(%v) = %v, want the same error", err, got)
		}
		if calls != 1 {
			t.Errorf("Do(%v): fn calls = %d, want 1", err, calls)
		}
	}
}

func TestDo_zeroOptionsRunsOnce(t *testing.T) {
	t.Parallel()
	calls := 0
	_ = retry.Do(t.Context(), retry.Options{}, func(context.Context) error {
		calls++
		return statusErr(503)
	})
	if calls != 1 {
		t.Errorf("fn calls with zero Options = %d, want 1", calls)
	}
}

func TestDo_delayIsCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var delays []time.Duration
		last := time.Now()
		first := true
		opts := retry.Options{Retries: 8, BaseDelay: time.Second, MaxDelay: 4 * time.Second, Jitter: noJitter}
		_ = retry.Do(t.Context(), opts, func(context.Context) error {
			if !first {
				delays = append(delays, time.Since(last))
			}
			first = false
			last = time.Now()
			return statusErr(500)
		})
		want := []time.Duration{1, 2, 4, 4, 4, 4, 4, 4}
		for i, w := range want {
			if delays[i] != w*time.Second {
				t.Fatalf("delay[%d] = %v, want %v (all: %v)", i, delays[i], w*time.Second, delays)
			}
		}
	})
}

func TestDo_jitterIsAdded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		calls := 0
		opts := retry.Options{Retries: 1, Jitter: func(max time.Duration) time.Duration {
			if max != retry.DefaultBaseDelay {
				t.Errorf("jitter max = %v, want %v", max, retry.DefaultBaseDelay)
			}
			return 100 * time.Millisecond
		}}
		_ = retry.Do(t.Context(), opts, func(context.Context) error {
			calls++
			return statusErr(429)
		})
		if got, want := time.Since(start), 350*time.Millisecond; got != want {
			t.Errorf("total wait = %v, want %v", got, want)
		}
	})
}

func TestDo_defaultJitterWithinBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		_ = retry.Do(t.Context(), retry.Options{Retries: 1}, func(context.Context) error { return statusErr(500) })
		got := time.Since(start)
		if got < 250*time.Millisecond || got >= 500*time.Millisecond {
			t.Errorf("wait with default jitter = %v, want in [250ms, 500ms)", got)
		}
	})
}

func TestDo_ctxCancelStopsWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		done := make(chan error, 1)
		go func() {
			done <- retry.Do(ctx, retry.Default(), func(context.Context) error {
				calls++
				return statusErr(503)
			})
		}()
		synctest.Wait() // fn ran once, Do is waiting for the backoff timer
		cancel()
		err := <-done
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Do() = %v, want context.Canceled", err)
		}
		if !errors.Is(err, statusErr(503)) {
			t.Errorf("Do() = %v, want it to keep the last 503", err)
		}
		if calls != 1 {
			t.Errorf("fn calls = %d, want 1", calls)
		}
	})
}

func TestDo_cancelledDuringFnIsNotRetried(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	err := retry.Do(ctx, retry.Default(), func(context.Context) error {
		calls++
		cancel()
		return statusErr(503)
	})
	if !errors.Is(err, statusErr(503)) || calls != 1 {
		t.Errorf("Do() = %v after %d calls, want the 503 after 1 call", err, calls)
	}
}

func TestValue_returnsResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		opts := retry.Default()
		opts.Jitter = noJitter
		got, err := retry.Value(t.Context(), opts, func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", statusErr(500)
			}
			return "ok", nil
		})
		if err != nil || got != "ok" {
			t.Errorf("Value() = (%q, %v), want (\"ok\", nil)", got, err)
		}
	})
}

func TestDo_customIsRetryable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		opts := retry.Options{Retries: 2, IsRetryable: func(err error) bool { return err.Error() == "again" }, Jitter: noJitter}
		_ = retry.Do(t.Context(), opts, func(context.Context) error {
			calls++
			return errors.New("again")
		})
		if calls != 3 {
			t.Errorf("fn calls = %d, want 3", calls)
		}
	})
}
