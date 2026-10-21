// Command server runs the DevDigest Go API.
//
// Usage:
//
//	server              start the API (migrate and seed first, unless switched off)
//	server migrate up|down|status
//	server seed
//	server healthcheck  exit 0 when the local API answers GET /health (Docker HEALTHCHECK)
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/logging"
)

func main() {
	if err := run(context.Background(), os.Args, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	cmd := ""
	if len(args) > 1 {
		cmd = args[1]
	}
	cfg, err := config.Load(getenv)
	if cmd == "healthcheck" {
		addr := ":3001"
		if err == nil {
			addr = cfg.Addr
		}
		return healthcheck(ctx, addr)
	}
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	switch cmd {
	case "", "serve":
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serve(ctx, cfg, withGetenv(getenv), withLogOutput(stdout))
	case "migrate":
		return runMigrate(ctx, cfg, args[2:], stdout, logging.New(stdout, cfg.LogLevel))
	case "seed":
		return runSeed(ctx, cfg, stdout, logging.New(stdout, cfg.LogLevel))
	default:
		return fmt.Errorf("unknown command %q; %s", cmd, usage)
	}
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
