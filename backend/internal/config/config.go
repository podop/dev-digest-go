// Package config loads the backend configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Config is the typed configuration passed from main to the rest of the backend.
type Config struct {
	Addr        string     // listen address, e.g. ":3001"
	DatabaseURL string     // Postgres connection string
	CORSOrigins []string   // browser origins allowed to call the API
	LogLevel    slog.Level // debug | info | warn | error
}

// Load reads the configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:        valueOr(getenv("API_ADDR"), ":3001"),
		DatabaseURL: getenv("DATABASE_URL"),
		CORSOrigins: splitList(valueOr(getenv("CORS_ORIGINS"), "http://localhost:3000")),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(valueOr(getenv("LOG_LEVEL"), "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	return cfg, nil
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
