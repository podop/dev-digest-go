package httpx

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
)

// Default rate-limit values (Node parity).
const (
	defaultRateLimit  = 120
	defaultRateWindow = 60 * time.Second
)

// RateLimit configures the in-memory fixed-window limiter, per client IP (socket address).
// Windows start at the client's first request. /health, /health/ready, preflights, unmatched
// routes and the routes in Exempt are never counted. State is per process (single backend instance).
type RateLimit struct {
	// Enabled turns limiting on; the zero value leaves every route unlimited.
	Enabled bool
	// Limit is the global budget per window (default 120).
	Limit int
	// Window is the window length (default 60 s).
	Window time.Duration
	// Buckets give single routes their own budget instead of the global one.
	Buckets []Bucket
	// Exempt lists routes that are never counted, neither against the global budget nor a bucket
	// (long-lived streams such as the run-events SSE). /health* and preflights are always exempt.
	Exempt []Route
}

// Route names one registered route.
type Route struct {
	Method string // HTTP method, e.g. "GET"
	Path   string // registered route path, e.g. "/runs/:id/events"
}

// Bucket is a separate budget for one registered route.
type Bucket struct {
	Method string // HTTP method, e.g. "POST"
	Path   string // registered route path, e.g. "/settings/test-connection"
	Limit  int    // requests per window
	// Name, when set, names the counter: buckets with the same Name share one counter, so several
	// routes can draw on one budget. Empty keeps one counter per "METHOD path".
	Name string
	// Key derives the client part of the counter key from the request. Nil keys by client IP.
	// A constant key makes the counter shared by all clients (single-tenant workspace key).
	Key func(*echo.Context) string
}

// counterName is the counter's name part of the key: the shared Name, or one counter per route.
// Named counters carry a prefix so a Name can never collide with "global" or a "METHOD path" counter.
func (b Bucket) counterName() string {
	if b.Name != "" {
		return "bucket:" + b.Name
	}
	return b.Method + " " + b.Path
}

// rateLimit returns the limiter middleware. It runs after routing, so it can tell registered
// routes (counted) from unmatched ones (not counted) and find a route's bucket.
func rateLimit(cfg RateLimit, now func() time.Time) echo.MiddlewareFunc {
	if cfg.Limit <= 0 {
		cfg.Limit = defaultRateLimit
	}
	if cfg.Window <= 0 {
		cfg.Window = defaultRateWindow
	}
	buckets := make(map[string]Bucket, len(cfg.Buckets))
	for _, b := range cfg.Buckets {
		buckets[b.Method+" "+b.Path] = b
	}
	exempt := make(map[string]struct{}, len(cfg.Exempt))
	for _, r := range cfg.Exempt {
		exempt[r.Method+" "+r.Path] = struct{}{}
	}
	l := &limiter{now: now, window: cfg.Window, entries: make(map[string]*windowState)}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			ri := c.RouteInfo()
			if ri.Path == "" || ri.Path == "/health" || ri.Path == "/health/ready" {
				return next(c)
			}
			if _, ok := exempt[ri.Method+" "+ri.Path]; ok {
				return next(c)
			}
			name, limit := "global", cfg.Limit
			var keyOf func(*echo.Context) string
			if b, ok := buckets[ri.Method+" "+ri.Path]; ok {
				name, limit, keyOf = b.counterName(), b.Limit, b.Key
			}
			var client string
			if keyOf != nil {
				client = keyOf(c)
			} else if client = c.RealIP(); client == "" {
				client = "unknown"
			}
			ok, remaining := l.hit(name+"|"+client, limit)
			if ok {
				return next(c)
			}
			secs := int((remaining + time.Second - 1) / time.Second)
			secs = max(secs, 1)
			c.Response().Header().Set("Retry-After", strconv.Itoa(secs))
			return echo.NewHTTPError(http.StatusTooManyRequests, "Rate limit exceeded, retry in "+humanSeconds(secs))
		}
	}
}

// humanSeconds renders a wait the way the Node limiter does: "1 minute" for a full window,
// "<n> seconds" otherwise.
func humanSeconds(secs int) string {
	switch secs {
	case 60:
		return "1 minute"
	case 1:
		return "1 second"
	default:
		return fmt.Sprintf("%d seconds", secs)
	}
}

type windowState struct {
	start time.Time
	count int
}

type limiter struct {
	now    func() time.Time
	window time.Duration

	mu        sync.Mutex
	entries   map[string]*windowState
	lastSweep time.Time
}

// hit counts one request against key. When the budget is spent it reports the time left in the
// current window.
func (l *limiter) hit(key string, limit int) (allowed bool, remaining time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= l.window { // drop expired windows so the map cannot grow forever
		for k, w := range l.entries {
			if now.Sub(w.start) >= l.window {
				delete(l.entries, k)
			}
		}
		l.lastSweep = now
	}

	w, ok := l.entries[key]
	if !ok || now.Sub(w.start) >= l.window {
		w = &windowState{start: now}
		l.entries[key] = w
	}
	w.count++
	if w.count > limit {
		return false, w.start.Add(l.window).Sub(now)
	}
	return true, 0
}
