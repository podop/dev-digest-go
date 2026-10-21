package httpx

import (
	"net/http"

	"github.com/labstack/echo/v5"
)

// corsMiddleware allows exactly the configured origins, with credentials. Unlike Echo's CORS
// middleware it accepts an empty list (no origin allowed). A disallowed or absent Origin is
// served normally without CORS headers. A preflight (OPTIONS with Origin and
// Access-Control-Request-Method) is answered here with 204 on any path, before routing.
func corsMiddleware(origins []string) echo.MiddlewareFunc {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			h := c.Response().Header()
			h.Add("Vary", "Origin")

			origin := req.Header.Get("Origin")
			_, ok := allowed[origin]
			ok = ok && origin != ""
			if ok {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
			}

			isPreflight := req.Method == http.MethodOptions && origin != "" &&
				req.Header.Get("Access-Control-Request-Method") != ""
			if !isPreflight {
				return next(c)
			}
			if ok {
				h.Set("Access-Control-Allow-Methods", "GET,HEAD,PUT,PATCH,POST,DELETE")
				if reqHeaders := req.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
					h.Set("Access-Control-Allow-Headers", reqHeaders)
					h.Add("Vary", "Access-Control-Request-Headers")
				}
			}
			return c.NoContent(http.StatusNoContent)
		}
	}
}
