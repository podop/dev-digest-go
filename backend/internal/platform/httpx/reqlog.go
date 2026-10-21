package httpx

import (
	"log/slog"
	"time"

	"github.com/labstack/echo/v5"
)

// requestLog writes one line per request: method, path (no query string), status, duration.
// Headers and bodies are never logged. It is the outermost middleware, so it renders any error
// through the error handler itself to learn the final status.
func requestLog(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			start := time.Now()
			err := next(c)
			if err != nil {
				c.Echo().HTTPErrorHandler(c, err)
				err = nil
			}
			status := 0
			if resp, uerr := echo.UnwrapResponse(c.Response()); uerr == nil {
				status = resp.Status
			}
			req := c.Request()
			log.LogAttrs(req.Context(), slog.LevelInfo, "request",
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
				slog.Int("status", status),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			)
			return err
		}
	}
}
