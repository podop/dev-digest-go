// Package retry re-runs an operation with exponential backoff and jitter while
// its errors are transient (HTTP 429, 5xx, connection resets).
package retry

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"syscall"
	"time"
)

// Defaults match the Node backend (resilience.ts): 3 retries, 250 ms base,
// 8 s cap, up to one base of jitter.
const (
	DefaultRetries   = 3
	DefaultBaseDelay = 250 * time.Millisecond
	DefaultMaxDelay  = 8 * time.Second
)

// Options configures Do. The zero value means no retries; use Default for the
// standard policy.
type Options struct {
	// Retries is the number of re-runs after the first attempt (0 = run once).
	Retries int
	// BaseDelay is the first backoff step; the nth retry waits
	// min(MaxDelay, BaseDelay*2^n) plus jitter. Zero means DefaultBaseDelay.
	BaseDelay time.Duration
	// MaxDelay caps the exponential part. Zero means DefaultMaxDelay.
	MaxDelay time.Duration
	// IsRetryable decides whether an error is worth another attempt.
	// Nil means IsTransient.
	IsRetryable func(error) bool
	// OnRetry, if set, is called before each wait with the 1-based retry
	// number and the error that caused it.
	OnRetry func(attempt int, err error)
	// Jitter returns a random duration in [0, max). Nil uses math/rand/v2.
	Jitter func(max time.Duration) time.Duration
}

// Default returns the standard policy: 3 retries on transient errors.
func Default() Options {
	return Options{Retries: DefaultRetries, BaseDelay: DefaultBaseDelay, MaxDelay: DefaultMaxDelay}
}

// Do runs fn until it succeeds, fails with a non-retryable error, runs out of
// retries, or ctx ends. It returns the last error from fn; when ctx ends while
// waiting, the result joins ctx.Err() with that last error.
func Do(ctx context.Context, opts Options, fn func(context.Context) error) error {
	_, err := Value(ctx, opts, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}

// Value is Do for operations that return a value.
func Value[T any](ctx context.Context, opts Options, fn func(context.Context) (T, error)) (T, error) {
	base := opts.BaseDelay
	if base <= 0 {
		base = DefaultBaseDelay
	}
	maxDelay := opts.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultMaxDelay
	}
	retryable := opts.IsRetryable
	if retryable == nil {
		retryable = IsTransient
	}
	jitter := opts.Jitter
	if jitter == nil {
		jitter = func(max time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(max))) }
	}

	for attempt := 0; ; attempt++ {
		v, err := fn(ctx)
		if err == nil {
			return v, nil
		}
		if attempt >= opts.Retries || ctx.Err() != nil || !retryable(err) {
			return v, err
		}
		if opts.OnRetry != nil {
			opts.OnRetry(attempt+1, err)
		}
		delay := min(maxDelay, base<<attempt) + jitter(base)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			var zero T
			return zero, errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
	}
}

// statusCoder is implemented by errors that carry an HTTP status
// (github.StatusError, echo.HTTPError).
type statusCoder interface{ StatusCode() int }

// IsTransient reports whether err is worth retrying: HTTP 429 or >= 500, a
// connection reset or timeout, a DNS failure, or a truncated response. Context
// cancellation and deadlines (our own timeouts) are not transient; other
// statuses are not either.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var sc statusCoder
	if errors.As(err, &sc) {
		code := sc.StatusCode()
		return code == 429 || code >= 500
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
