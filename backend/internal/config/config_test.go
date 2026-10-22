package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
)

var defaultGlobs = []string{"**/{specs,docs,insights}/**/*.md"}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    config.Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": "postgres://db", "HOME": "/home/u"},
			want: config.Config{
				Addr:                   ":3001",
				DatabaseURL:            "postgres://db",
				CORSOrigins:            []string{"http://localhost:3000"},
				AllowedHosts:           []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:               slog.LevelInfo,
				DataDir:                "/home/u/.devdigest",
				CloneDir:               "/home/u/.devdigest/workspace",
				RateLimitEnabled:       true,
				AutoMigrate:            true,
				AutoSeed:               true,
				RepoIntelEnabled:       true,
				ProjectContextGlobs:    defaultGlobs,
				ReviewAgentConcurrency: 4,
				ReviewMapConcurrency:   3,
			},
		},
		{
			name: "overrides",
			env: map[string]string{
				"DATABASE_URL":       "postgres://db",
				"API_ADDR":           ":9000",
				"CORS_ORIGINS":       " http://a:1 , http://b:2 ,",
				"ALLOWED_HOSTS":      " api.local , Localhost,",
				"LOG_LEVEL":          "debug",
				"DEVDIGEST_DATA_DIR": "/data",
				"RATE_LIMIT_ENABLED": "false",
				"DB_AUTO_MIGRATE":    "false",
				"DB_AUTO_SEED":       "false",
			},
			want: config.Config{
				Addr:                   ":9000",
				DatabaseURL:            "postgres://db",
				CORSOrigins:            []string{"http://a:1", "http://b:2"},
				AllowedHosts:           []string{"api.local", "Localhost"},
				LogLevel:               slog.LevelDebug,
				DataDir:                "/data",
				CloneDir:               "/data/workspace",
				RateLimitEnabled:       false,
				AutoMigrate:            false,
				AutoSeed:               false,
				RepoIntelEnabled:       true,
				ProjectContextGlobs:    defaultGlobs,
				ReviewAgentConcurrency: 4,
				ReviewMapConcurrency:   3,
			},
		},
		{
			name: "clone dir override and cleaned data dir",
			env: map[string]string{
				"DATABASE_URL":        "postgres://db",
				"DEVDIGEST_DATA_DIR":  "/data/./x/",
				"DEVDIGEST_CLONE_DIR": "/clones",
			},
			want: config.Config{
				Addr:                   ":3001",
				DatabaseURL:            "postgres://db",
				CORSOrigins:            []string{"http://localhost:3000"},
				AllowedHosts:           []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:               slog.LevelInfo,
				DataDir:                "/data/x",
				CloneDir:               "/clones",
				RateLimitEnabled:       true,
				AutoMigrate:            true,
				AutoSeed:               true,
				RepoIntelEnabled:       true,
				ProjectContextGlobs:    defaultGlobs,
				ReviewAgentConcurrency: 4,
				ReviewMapConcurrency:   3,
			},
		},
		{
			name: "switches stay on unless exactly false",
			env: map[string]string{
				"DATABASE_URL":       "postgres://db",
				"DEVDIGEST_DATA_DIR": "/data",
				"RATE_LIMIT_ENABLED": "0",
				"DB_AUTO_MIGRATE":    "FALSE",
				"DB_AUTO_SEED":       "",
			},
			want: config.Config{
				Addr:                   ":3001",
				DatabaseURL:            "postgres://db",
				CORSOrigins:            []string{"http://localhost:3000"},
				AllowedHosts:           []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:               slog.LevelInfo,
				DataDir:                "/data",
				CloneDir:               "/data/workspace",
				RateLimitEnabled:       true,
				AutoMigrate:            true,
				AutoSeed:               true,
				RepoIntelEnabled:       true,
				ProjectContextGlobs:    defaultGlobs,
				ReviewAgentConcurrency: 4,
				ReviewMapConcurrency:   3,
			},
		},
		{
			name: "llm and context switches",
			env: map[string]string{
				"DATABASE_URL":          "postgres://db",
				"DEVDIGEST_DATA_DIR":    "/data",
				"LLM_PROVIDER_OVERRIDE": " mock ",
				"LLM_MOCK_DELAY_MS":     "1500",
				"PROJECT_CONTEXT_GLOBS": "docs/*.md, {a,b}/**/*.md ,, ",
			},
			want: config.Config{
				Addr:                   ":3001",
				DatabaseURL:            "postgres://db",
				CORSOrigins:            []string{"http://localhost:3000"},
				AllowedHosts:           []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:               slog.LevelInfo,
				DataDir:                "/data",
				CloneDir:               "/data/workspace",
				RateLimitEnabled:       true,
				AutoMigrate:            true,
				AutoSeed:               true,
				LLMProviderOverride:    "mock",
				LLMMockDelay:           1500 * time.Millisecond,
				RepoIntelEnabled:       true,
				ProjectContextGlobs:    []string{"docs/*.md", "{a,b}/**/*.md"},
				ReviewAgentConcurrency: 4,
				ReviewMapConcurrency:   3,
			},
		},
		{
			name: "mock delay bounds are inclusive",
			env: map[string]string{
				"DATABASE_URL": "postgres://db", "DEVDIGEST_DATA_DIR": "/data", "LLM_MOCK_DELAY_MS": "60000",
			},
			want: config.Config{
				Addr:                   ":3001",
				DatabaseURL:            "postgres://db",
				CORSOrigins:            []string{"http://localhost:3000"},
				AllowedHosts:           []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:               slog.LevelInfo,
				DataDir:                "/data",
				CloneDir:               "/data/workspace",
				RateLimitEnabled:       true,
				AutoMigrate:            true,
				AutoSeed:               true,
				LLMMockDelay:           60 * time.Second,
				RepoIntelEnabled:       true,
				ProjectContextGlobs:    defaultGlobs,
				ReviewAgentConcurrency: 4,
				ReviewMapConcurrency:   3,
			},
		},
		{name: "provider override other", env: map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER_OVERRIDE": "other"}, wantErr: true},
		{name: "provider override wrong case", env: map[string]string{"DATABASE_URL": "x", "LLM_PROVIDER_OVERRIDE": "Mock"}, wantErr: true},
		{name: "mock delay negative", env: map[string]string{"DATABASE_URL": "x", "LLM_MOCK_DELAY_MS": "-1"}, wantErr: true},
		{name: "mock delay too large", env: map[string]string{"DATABASE_URL": "x", "LLM_MOCK_DELAY_MS": "60001"}, wantErr: true},
		{name: "mock delay not an integer", env: map[string]string{"DATABASE_URL": "x", "LLM_MOCK_DELAY_MS": "1.5"}, wantErr: true},
		{name: "mock delay text", env: map[string]string{"DATABASE_URL": "x", "LLM_MOCK_DELAY_MS": "soon"}, wantErr: true},
		{name: "missing database url", env: map[string]string{}, wantErr: true},
		{name: "allowed hosts without an entry", env: map[string]string{"DATABASE_URL": "x", "ALLOWED_HOSTS": " , "}, wantErr: true},
		{name: "bad log level", env: map[string]string{"DATABASE_URL": "x", "LOG_LEVEL": "loud"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.Load(func(k string) string { return tc.env[k] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load(%v) error = %v, wantErr %t", tc.env, err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Load(%v) mismatch (-want +got):\n%s", tc.env, diff)
			}
		})
	}
}

func TestLoadProjectContextGlobs(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want []string
	}{
		{"unset gives the default", "", defaultGlobs},
		{"blank gives the default", "  ", defaultGlobs},
		{"only separators give the default", " , ,", defaultGlobs},
		{"two globs", "docs/*.md,{a,b}/**/*.md", []string{"docs/*.md", "{a,b}/**/*.md"}},
		{"commas inside braces kept", "{a,b,c}/*.md", []string{"{a,b,c}/*.md"}},
		{"nested braces", "{a,{b,c}}/*.md,x/*.md", []string{"{a,{b,c}}/*.md", "x/*.md"}},
		{"blank entries dropped and trimmed", " a/*.md ,, b/*.md ,", []string{"a/*.md", "b/*.md"}},
		{"unbalanced closing brace does not swallow the next comma", "a}/*.md,b/*.md", []string{"a}/*.md", "b/*.md"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"DATABASE_URL": "x", "PROJECT_CONTEXT_GLOBS": tc.env}
			got, err := config.Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatalf("Load(PROJECT_CONTEXT_GLOBS=%q) error: %v", tc.env, err)
			}
			if diff := cmp.Diff(tc.want, got.ProjectContextGlobs); diff != "" {
				t.Errorf("Load(PROJECT_CONTEXT_GLOBS=%q) globs mismatch (-want +got):\n%s", tc.env, diff)
			}
		})
	}
}

// AC3065: REPO_INTEL_ENABLED is off only when its trimmed value is exactly "false".
func TestLoadRepoIntelEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{name: "unset", want: true},
		{name: "empty", set: true, value: "", want: true},
		{name: "true", set: true, value: "true", want: true},
		{name: "zero", set: true, value: "0", want: true},
		{name: "false with spaces", set: true, value: " false ", want: false},
		{name: "false", set: true, value: "false", want: false},
		{name: "upper case FALSE", set: true, value: "FALSE", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"DATABASE_URL": "x"}
			if tc.set {
				env["REPO_INTEL_ENABLED"] = tc.value
			}
			got, err := config.Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatalf("Load(REPO_INTEL_ENABLED=%q) error: %v", tc.value, err)
			}
			if got.RepoIntelEnabled != tc.want {
				t.Errorf("Load(REPO_INTEL_ENABLED=%q).RepoIntelEnabled = %t, want %t", tc.value, got.RepoIntelEnabled, tc.want)
			}
		})
	}
}

// FR4043, EC4038: REVIEW_AGENT_CONCURRENCY (1-20, default 4) and REVIEW_MAP_CONCURRENCY (1-16,
// default 3); an empty value is unset, anything else outside the range is an error naming the
// variable.
func TestLoadReviewConcurrency(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantAgent int
		wantMap   int
		wantErr   string // variable named in the error; "" = no error
	}{
		{name: "defaults", wantAgent: 4, wantMap: 3},
		{name: "empty counts as unset", env: map[string]string{"REVIEW_AGENT_CONCURRENCY": "", "REVIEW_MAP_CONCURRENCY": "  "}, wantAgent: 4, wantMap: 3},
		{name: "lower bounds", env: map[string]string{"REVIEW_AGENT_CONCURRENCY": "1", "REVIEW_MAP_CONCURRENCY": "1"}, wantAgent: 1, wantMap: 1},
		{name: "upper bounds", env: map[string]string{"REVIEW_AGENT_CONCURRENCY": "20", "REVIEW_MAP_CONCURRENCY": "16"}, wantAgent: 20, wantMap: 16},
		{name: "trimmed values", env: map[string]string{"REVIEW_AGENT_CONCURRENCY": " 7 ", "REVIEW_MAP_CONCURRENCY": " 5 "}, wantAgent: 7, wantMap: 5},
		{name: "agent zero", env: map[string]string{"REVIEW_AGENT_CONCURRENCY": "0"}, wantErr: "REVIEW_AGENT_CONCURRENCY"},
		{name: "agent text", env: map[string]string{"REVIEW_AGENT_CONCURRENCY": "abc"}, wantErr: "REVIEW_AGENT_CONCURRENCY"},
		{name: "agent above range", env: map[string]string{"REVIEW_AGENT_CONCURRENCY": "21"}, wantErr: "REVIEW_AGENT_CONCURRENCY"},
		{name: "agent fraction", env: map[string]string{"REVIEW_AGENT_CONCURRENCY": "2.5"}, wantErr: "REVIEW_AGENT_CONCURRENCY"},
		{name: "map negative", env: map[string]string{"REVIEW_MAP_CONCURRENCY": "-1"}, wantErr: "REVIEW_MAP_CONCURRENCY"},
		{name: "map above range", env: map[string]string{"REVIEW_MAP_CONCURRENCY": "17"}, wantErr: "REVIEW_MAP_CONCURRENCY"},
		{name: "map text", env: map[string]string{"REVIEW_MAP_CONCURRENCY": "many"}, wantErr: "REVIEW_MAP_CONCURRENCY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"DATABASE_URL": "x"}
			for k, v := range tc.env {
				env[k] = v
			}
			got, err := config.Load(func(k string) string { return env[k] })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load(%v) error = %v, want one naming %s", tc.env, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(%v) error: %v", tc.env, err)
			}
			if got.ReviewAgentConcurrency != tc.wantAgent || got.ReviewMapConcurrency != tc.wantMap {
				t.Errorf("Load(%v) concurrency = agent %d, map %d; want agent %d, map %d",
					tc.env, got.ReviewAgentConcurrency, got.ReviewMapConcurrency, tc.wantAgent, tc.wantMap)
			}
		})
	}
}
