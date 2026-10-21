// Package health exposes the liveness and readiness endpoints of the Node API.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
)

// pinger is what readiness needs from the database (*sql.DB satisfies it).
type pinger interface {
	PingContext(ctx context.Context) error
}

// AddRoutes registers GET /health → {"status":"ok"} and GET /health/ready →
// {"ready":true} or 503 {"ready":false}; both bodies match the Node API.
func AddRoutes(e *echo.Echo, db pinger, log *slog.Logger) {
	e.GET("/health", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	e.GET("/health/ready", func(c *echo.Context) error {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			log.WarnContext(ctx, "readiness: database ping failed", "err", err)
			return c.JSON(http.StatusServiceUnavailable, map[string]bool{"ready": false})
		}
		return c.JSON(http.StatusOK, map[string]bool{"ready": true})
	})
}
