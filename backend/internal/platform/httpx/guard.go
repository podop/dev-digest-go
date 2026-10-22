package httpx

import (
	"log/slog"
	"strings"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
)

// hostCheck rejects, before routing, every request whose Host header names a host outside
// allowed (DNS-rebinding defense: a page on an attacker's domain that resolves to this server
// must not be able to talk to it). Ports are ignored. Only an empty list switches the check off:
// a list without a single well-formed entry fails closed (every request gets 403), and each
// malformed entry is logged at startup.
func hostCheck(allowed []string, log *slog.Logger) echo.MiddlewareFunc {
	set := make(map[string]struct{}, len(allowed))
	for _, h := range allowed {
		n := hostName(h)
		if n == "" {
			log.Warn("ALLOWED_HOSTS entry ignored: not a host name", "entry", h)
			continue
		}
		set[n] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		if len(allowed) == 0 {
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
// that is not a well-formed host with an optional numeric port yields "", which matches nothing.
func hostName(hostport string) string {
	h := strings.ToLower(strings.TrimSpace(hostport))
	host, port := h, ""
	switch {
	case strings.HasPrefix(h, "["):
		end := strings.Index(h, "]")
		if end < 0 {
			return ""
		}
		host, port = h[:end+1], h[end+1:]
		if port != "" {
			var ok bool
			if port, ok = strings.CutPrefix(port, ":"); !ok {
				return ""
			}
		}
	case strings.Count(h, ":") == 1:
		host, port, _ = strings.Cut(h, ":")
	case strings.Contains(h, ":"):
		return "" // an IPv6 literal must be bracketed
	}
	if !validPort(port) {
		return ""
	}
	return strings.TrimSuffix(host, ".")
}

// validPort reports whether port is empty or 1–5 ASCII digits, as a browser can send it.
func validPort(port string) bool {
	if len(port) > 5 {
		return false
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
