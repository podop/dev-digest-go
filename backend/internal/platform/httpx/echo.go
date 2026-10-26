// Package httpx builds the Echo instance and the API-wide HTTP behavior shared by all modules.
package httpx

import (
	"io"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

// Options configures New.
type Options struct {
	// Log receives the request log line and unexpected errors. A nil Log discards everything.
	Log *slog.Logger
	// CORSOrigins is the exact list of allowed browser origins; empty allows none.
	CORSOrigins []string
	// AllowedHosts lists the host names (no port, case-insensitive, IPv6 in brackets) the
	// server answers to; any other Host header gets 403. Empty switches the check off; a
	// non-empty list without a well-formed entry refuses every request.
	AllowedHosts []string
	// BodyLimits raises the request body limit (1 MiB by default) of single routes. Keys are
	// "METHOD /route/path" exactly as the route is registered, e.g. "POST /skills/import/preview"
	// or "PUT /repos/:id/context/files"; values are the largest accepted body in bytes.
	BodyLimits map[string]int64
	// RawBodies makes single routes take a body that is not JSON, e.g. an uploaded archive.
	// Keys are "METHOD /route/path" exactly as the route is registered; the value maps each
	// accepted media type (lower case, no parameters, e.g. "application/zip") to the largest
	// accepted body of that type in bytes. On such a route any other Content-Type, or none,
	// is a 415 unsupported_media_type, a body over its type's limit is a 413 payload_too_large,
	// and the limit replaces the default one (it is not capped by it). The handler reads the
	// body with ReadRawBody. The Host, Origin, CORS and rate-limit checks apply as everywhere;
	// every route not listed keeps the JSON-only rules.
	RawBodies map[string]map[string]int64
	// RateLimit configures the fixed-window limiter; the zero value disables it.
	RateLimit RateLimit
	// Now is the clock of the rate limiter; nil means time.Now.
	Now func() time.Time
}

// New returns an Echo instance with the API-wide behavior of the Node server:
//   - pre-routing (every request, including 404, 405 and preflights): request log line,
//     security headers, the Host allowlist, CORS, the Origin check of state-changing methods;
//   - after routing: panic recovery, rate limiting, request body rules;
//   - the error envelope, GET routes answering HEAD, and the socket address as client IP.
func New(opts Options) *echo.Echo {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	e := echo.NewWithConfig(echo.Config{
		Logger:           log,
		HTTPErrorHandler: errorHandler(log),
		Router: echo.NewRouter(echo.RouterConfig{
			AutoHandleHEAD: true,
			// Echo answers OPTIONS on a known path with 204; here it is an unregistered method
			// like any other (404 not_found). Real preflights are answered by corsMiddleware.
			OptionsMethodHandler: func(*echo.Context) error { return echo.ErrMethodNotAllowed },
		}),
		IPExtractor: echo.ExtractIPDirect(),
	})
	e.Pre(requestLog(log), securityHeaders(), hostCheck(opts.AllowedHosts, log),
		corsMiddleware(opts.CORSOrigins), originCheck(opts.CORSOrigins))
	e.Use(middleware.Recover())
	if opts.RateLimit.Enabled {
		e.Use(rateLimit(opts.RateLimit, now))
	}
	e.Use(bodyRules(maps.Clone(opts.BodyLimits), cloneRawBodies(opts.RawBodies)))
	return e
}

// cloneRawBodies copies the table so later changes by the caller do not reach the middleware,
// and folds the media types to lower case to match mediaType.
func cloneRawBodies(in map[string]map[string]int64) map[string]map[string]int64 {
	out := make(map[string]map[string]int64, len(in))
	for route, types := range in {
		m := make(map[string]int64, len(types))
		for media, limit := range types {
			m[strings.ToLower(media)] = limit
		}
		out[route] = m
	}
	return out
}
