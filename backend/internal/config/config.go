// Package config loads the backend configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Config is the typed configuration passed from main to the rest of the backend.
//
// Secret values (provider keys, GitHub token) are deliberately not part of it:
// they are read per call through the secrets store.
type Config struct {
	Addr             string     // listen address, see the API_ADDR note on Load
	DatabaseURL      string     // Postgres connection string
	CORSOrigins      []string   // browser origins allowed to call the API
	AllowedHosts     []string   // ALLOWED_HOSTS: Host header names the API answers to (DNS-rebinding guard)
	LogLevel         slog.Level // debug | info | warn | error
	DataDir          string     // absolute; holds secrets.json and the clone root
	CloneDir         string     // absolute clone root, default <DataDir>/workspace
	RateLimitEnabled bool       // RATE_LIMIT_ENABLED, off only on "false"
	AutoMigrate      bool       // DB_AUTO_MIGRATE, off only on "false"
	AutoSeed         bool       // DB_AUTO_SEED, off only on "false"
}

// Load reads the configuration through getenv (os.Getenv in production).
//
// API_ADDR defaults to ":3001", which listens on every interface. The default is mandated by
// the spec because the container must accept connections from outside its own network
// namespace; docker-compose.yml then publishes the port on 127.0.0.1 only. Outside a
// container (go run ./cmd/server, a bare binary) set API_ADDR=127.0.0.1:3001: the API has no
// authentication and can overwrite the stored GitHub token and LLM keys, so it must not be
// reachable from the local network.
//
// ALLOWED_HOSTS (comma list, default "localhost,127.0.0.1,[::1],backend") names the Host
// headers the API answers to; anything else gets 403, which stops DNS-rebinding pages. Add the
// service name or domain under which the API is reached when that is not one of the defaults.
// A value without any entry (e.g. ",") is an error, so the guard cannot be switched off by
// accident.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:             valueOr(getenv("API_ADDR"), ":3001"),
		DatabaseURL:      getenv("DATABASE_URL"),
		CORSOrigins:      splitList(valueOr(getenv("CORS_ORIGINS"), "http://localhost:3000")),
		AllowedHosts:     splitList(valueOr(getenv("ALLOWED_HOSTS"), "localhost,127.0.0.1,[::1],backend")),
		RateLimitEnabled: enabled(getenv("RATE_LIMIT_ENABLED")),
		AutoMigrate:      enabled(getenv("DB_AUTO_MIGRATE")),
		AutoSeed:         enabled(getenv("DB_AUTO_SEED")),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if len(cfg.AllowedHosts) == 0 {
		// Only reachable through a value like "," — it would silently switch the guard off.
		return Config{}, errors.New("ALLOWED_HOSTS is set but names no host")
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(valueOr(getenv("LOG_LEVEL"), "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}

	dataDir := strings.TrimSpace(getenv("DEVDIGEST_DATA_DIR"))
	if dataDir == "" {
		home := strings.TrimSpace(getenv("HOME"))
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				return Config{}, fmt.Errorf("DEVDIGEST_DATA_DIR is not set and the home directory is unknown: %w", err)
			}
		}
		dataDir = filepath.Join(home, ".devdigest")
	}
	var err error
	if cfg.DataDir, err = filepath.Abs(dataDir); err != nil {
		return Config{}, fmt.Errorf("DEVDIGEST_DATA_DIR: %w", err)
	}

	cloneDir := strings.TrimSpace(getenv("DEVDIGEST_CLONE_DIR"))
	if cloneDir == "" {
		cloneDir = filepath.Join(cfg.DataDir, "workspace")
	}
	if cfg.CloneDir, err = filepath.Abs(cloneDir); err != nil {
		return Config{}, fmt.Errorf("DEVDIGEST_CLONE_DIR: %w", err)
	}
	return cfg, nil
}

// enabled reports whether a switch is on: everything but the exact value "false" is on.
func enabled(v string) bool {
	return strings.TrimSpace(v) != "false"
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
