// Command server runs the DevDigest Go API.
//
// Usage:
//
//	server              start the API
//	server healthcheck  exit 0 when the local API answers GET /health (Docker HEALTHCHECK)
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/health"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/postgres"
)

func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(getenv)
	if len(args) > 1 && args[1] == "healthcheck" {
		addr := ":3001"
		if err == nil {
			addr = cfg.Addr
		}
		return healthcheck(ctx, addr)
	}
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	db, err := postgres.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	e := httpx.New(cfg.CORSOrigins, log)
	health.AddRoutes(e, sqlDB, log)

	log.InfoContext(ctx, "api listening", "addr", cfg.Addr)
	sc := echo.StartConfig{
		Address:         cfg.Addr,
		HideBanner:      true,
		HidePort:        true,
		GracefulTimeout: 10 * time.Second,
		BeforeServeFunc: func(s *http.Server) error {
			s.ReadHeaderTimeout = 5 * time.Second
			s.IdleTimeout = 120 * time.Second
			return nil
		},
	}
	if err := sc.Start(ctx, e); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	log.Info("api stopped")
	return nil
}

// healthcheck calls the local /health endpoint; used by the container HEALTHCHECK.
func healthcheck(ctx context.Context, addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("healthcheck: bad address %q: %w", addr, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
	}
	return nil
}
