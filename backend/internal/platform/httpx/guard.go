package httpx

import (
	"strings"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
)

// hostCheck rejects, before routing, every request whose Host header names a host outside
// allowed (DNS-rebinding defense: a page on an attacker's domain that resolves to this server
// must not be able to talk to it). Ports are ignored. An empty list switches the check off.
func hostCheck(allowed []string) echo.MiddlewareFunc {
	set := make(map[string]struct{}, len(allowed))
	for _, h := range allowed {
		if n := hostName(h); n != "" {
			set[n] = struct{}{}
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		if len(set) == 0 {
			return next
		}
		return func(c *echo.Context) error {
			if _, ok := set[hostName(c.Request().Host)]; !ok {
				return apperr.Forbidden("Host not allowed")
			}
			return next(c)
		}
	}
}

// hostName reduces a Host header value (or a configured entry) to its lower-case host name
// without port. IPv6 literals keep their brackets, so "[::1]:3001" becomes "[::1]". A value
// that is not a well-formed host yields "", which matches nothing.
func hostName(hostport string) string {
	h := strings.ToLower(strings.TrimSpace(hostport))
	switch {
	case strings.HasPrefix(h, "["):
		end := strings.Index(h, "]")
		if end < 0 {
			return ""
		}
		if rest := h[end+1:]; rest != "" && !strings.HasPrefix(rest, ":") {
			return ""
		}
		return h[:end+1]
	case strings.Count(h, ":") == 1:
		h = h[:strings.Index(h, ":")]
	case strings.Contains(h, ":"):
		return "" // an IPv6 literal must be bracketed
	}
	return strings.TrimSuffix(h, ".")
}

// originCheck refuses state-changing requests (POST, PUT, PATCH, DELETE) that carry an Origin
// header outside origins, with 403. Browsers always send Origin on such requests, so this stops
// cross-site form posts and no-cors fetches that CORS alone would still let the server process.
// Requests without Origin (curl, MCP, server-to-server) and safe methods are not affected.
func originCheck(origins []string) echo.MiddlewareFunc {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if !methodHasBody(req.Method) {
				return next(c)
			}
			values, present := req.Header["Origin"]
			if !present {
				return next(c)
			}
			if len(values) == 1 {
				if _, ok := allowed[values[0]]; ok {
					return next(c)
				}
			}
			return apperr.Forbidden("Origin not allowed")
		}
	}
}
