// Package httpx builds the Echo instance and the API-wide HTTP behavior shared by all modules.
package httpx

import (
	"io"
	"log/slog"
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
	// server answers to; any other Host header gets 403. Empty switches the check off.
	AllowedHosts []string
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
	e.Pre(requestLog(log), securityHeaders(), hostCheck(opts.AllowedHosts),
		corsMiddleware(opts.CORSOrigins), originCheck(opts.CORSOrigins))
	e.Use(middleware.Recover())
	if opts.RateLimit.Enabled {
		e.Use(rateLimit(opts.RateLimit, now))
	}
	e.Use(bodyRules())
	return e
}
