// Package config loads the backend configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// LLMProviderMock is the only accepted non-empty value of LLM_PROVIDER_OVERRIDE.
	LLMProviderMock = "mock"
	// DefaultProjectContextGlobs is the PROJECT_CONTEXT_GLOBS default.
	DefaultProjectContextGlobs = "**/{specs,docs,insights}/**/*.md"
	// maxMockDelayMS is the upper bound of LLM_MOCK_DELAY_MS.
	maxMockDelayMS = 60_000

	// DefaultReviewAgentConcurrency and MaxReviewAgentConcurrency bound REVIEW_AGENT_CONCURRENCY.
	DefaultReviewAgentConcurrency = 4
	MaxReviewAgentConcurrency     = 20
	// DefaultReviewMapConcurrency and MaxReviewMapConcurrency bound REVIEW_MAP_CONCURRENCY.
	DefaultReviewMapConcurrency = 3
	MaxReviewMapConcurrency     = 16
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
	// RepoIntelEnabled is REPO_INTEL_ENABLED, off only on "false" (after trimming). It gates only
	// the repo-intel read port; indexing and GET /repos/:id/index-state ignore it.
	RepoIntelEnabled bool
	// ReviewIntentEnabled is REVIEW_INTENT_ENABLED, off only on "false" (after trimming; empty or
	// unset is on, Phase 5 spec 7.14). It gates only the review's intent pre-work (the executor
	// hook); the intent routes, including POST /pulls/:id/intent/refresh, work either way. The
	// zero value is off: a hand-built Config (the component tests' newConfig) runs reviews as
	// Phase 4 did.
	ReviewIntentEnabled bool

	LLMProviderOverride string        // LLM_PROVIDER_OVERRIDE: "" or LLMProviderMock
	LLMMockDelay        time.Duration // LLM_MOCK_DELAY_MS, 0 to 60 s
	ProjectContextGlobs []string      // PROJECT_CONTEXT_GLOBS, never empty

	// ReviewAgentConcurrency is REVIEW_AGENT_CONCURRENCY: how many agents of one review request
	// run at once (1-MaxReviewAgentConcurrency, default DefaultReviewAgentConcurrency).
	ReviewAgentConcurrency int
	// ReviewMapConcurrency is REVIEW_MAP_CONCURRENCY: how many map-reduce chunks of one agent run
	// are in flight at once (1-MaxReviewMapConcurrency, default DefaultReviewMapConcurrency).
	ReviewMapConcurrency int
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
//
// LLM_PROVIDER_OVERRIDE is empty (default) or "mock", which replaces every LLM provider by a
// deterministic mock; any other value is an error. LLM_MOCK_DELAY_MS (integer 0-60000, default
// 0) delays each mock call. PROJECT_CONTEXT_GLOBS is a comma-separated glob list (commas inside
// {...} belong to the glob; blank entries are dropped); unset or empty gives
// DefaultProjectContextGlobs. REVIEW_AGENT_CONCURRENCY (integer 1-20, default 4) and
// REVIEW_MAP_CONCURRENCY (integer 1-16, default 3) bound the parallelism of a review request; an
// empty value counts as unset and any other value is an error naming the variable.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:             valueOr(getenv("API_ADDR"), ":3001"),
		DatabaseURL:      getenv("DATABASE_URL"),
		CORSOrigins:      splitList(valueOr(getenv("CORS_ORIGINS"), "http://localhost:3000")),
		AllowedHosts:     splitList(valueOr(getenv("ALLOWED_HOSTS"), "localhost,127.0.0.1,[::1],backend")),
		RateLimitEnabled: enabled(getenv("RATE_LIMIT_ENABLED")),
		AutoMigrate:      enabled(getenv("DB_AUTO_MIGRATE")),
		AutoSeed:         enabled(getenv("DB_AUTO_SEED")),
		RepoIntelEnabled: enabled(getenv("REPO_INTEL_ENABLED")),
		// Spec 7.14: on unless the trimmed value is exactly "false".
		ReviewIntentEnabled: enabled(getenv("REVIEW_INTENT_ENABLED")),
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
	switch override := strings.TrimSpace(getenv("LLM_PROVIDER_OVERRIDE")); override {
	case "", LLMProviderMock:
		cfg.LLMProviderOverride = override
	default:
		return Config{}, fmt.Errorf("LLM_PROVIDER_OVERRIDE: %q is not supported (use empty or %q)", override, LLMProviderMock)
	}
	delay, err := parseMockDelay(getenv("LLM_MOCK_DELAY_MS"))
	if err != nil {
		return Config{}, err
	}
	cfg.LLMMockDelay = delay
	if cfg.ReviewAgentConcurrency, err = parseBoundedInt(
		"REVIEW_AGENT_CONCURRENCY", getenv("REVIEW_AGENT_CONCURRENCY"), DefaultReviewAgentConcurrency, MaxReviewAgentConcurrency,
	); err != nil {
		return Config{}, err
	}
	if cfg.ReviewMapConcurrency, err = parseBoundedInt(
		"REVIEW_MAP_CONCURRENCY", getenv("REVIEW_MAP_CONCURRENCY"), DefaultReviewMapConcurrency, MaxReviewMapConcurrency,
	); err != nil {
		return Config{}, err
	}
	cfg.ProjectContextGlobs = splitGlobs(valueOr(getenv("PROJECT_CONTEXT_GLOBS"), DefaultProjectContextGlobs))
	if len(cfg.ProjectContextGlobs) == 0 {
		// Only reachable through a value like "," — fall back rather than list nothing.
		cfg.ProjectContextGlobs = []string{DefaultProjectContextGlobs}
	}

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

// parseMockDelay reads LLM_MOCK_DELAY_MS: an integer of 0 to 60000 milliseconds, empty = 0.
func parseMockDelay(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms < 0 || ms > maxMockDelayMS {
		return 0, fmt.Errorf("LLM_MOCK_DELAY_MS: %q is not an integer from 0 to %d", v, maxMockDelayMS)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// parseBoundedInt reads the integer variable name from v: empty (after trimming) = fallback,
// otherwise an integer from 1 to max.
func parseBoundedInt(name, v string, fallback, max int) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("%s: %q is not an integer from 1 to %d", name, v, max)
	}
	return n, nil
}

// splitGlobs splits a glob list at the commas outside braces, trims every entry and drops
// blank ones.
func splitGlobs(s string) []string {
	var out []string
	depth, start := 0, 0
	add := func(end int) {
		if p := strings.TrimSpace(s[start:end]); p != "" {
			out = append(out, p)
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				add(i)
				start = i + 1
			}
		}
	}
	add(len(s))
	return out
}
