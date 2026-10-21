package httpx

import "github.com/labstack/echo/v5"

// securityHeaders sets the Helmet defaults the Node server sends on every response, errors,
// 404s, 429s and preflights included. It runs before routing so no response path misses it.
// X-Powered-By is never sent.
func securityHeaders() echo.MiddlewareFunc {
	headers := [...][2]string{
		{"Content-Security-Policy", "default-src 'self';base-uri 'self';font-src 'self' https: data:;form-action 'self';frame-ancestors 'self';img-src 'self' data:;object-src 'none';script-src 'self';script-src-attr 'none';style-src 'self' https: 'unsafe-inline';upgrade-insecure-requests"},
		{"Cross-Origin-Opener-Policy", "same-origin"},
		{"Cross-Origin-Resource-Policy", "same-origin"},
		{"Origin-Agent-Cluster", "?1"},
		{"Referrer-Policy", "no-referrer"},
		{"Strict-Transport-Security", "max-age=31536000; includeSubDomains"},
		{"X-Content-Type-Options", "nosniff"},
		{"X-DNS-Prefetch-Control", "off"},
		{"X-Download-Options", "noopen"},
		{"X-Frame-Options", "SAMEORIGIN"},
		{"X-Permitted-Cross-Domain-Policies", "none"},
		{"X-XSS-Protection", "0"},
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			h := c.Response().Header()
			for _, kv := range headers {
				h.Set(kv[0], kv[1])
			}
			h.Del("X-Powered-By")
			return next(c)
		}
	}
}
