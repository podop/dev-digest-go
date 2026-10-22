package config_test

import (
	"log/slog"
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
				Addr:                ":3001",
				DatabaseURL:         "postgres://db",
				CORSOrigins:         []string{"http://localhost:3000"},
				AllowedHosts:        []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:            slog.LevelInfo,
				DataDir:             "/home/u/.devdigest",
				CloneDir:            "/home/u/.devdigest/workspace",
				RateLimitEnabled:    true,
				AutoMigrate:         true,
				AutoSeed:            true,
				ProjectContextGlobs: defaultGlobs,
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
				Addr:                ":9000",
				DatabaseURL:         "postgres://db",
				CORSOrigins:         []string{"http://a:1", "http://b:2"},
				AllowedHosts:        []string{"api.local", "Localhost"},
				LogLevel:            slog.LevelDebug,
				DataDir:             "/data",
				CloneDir:            "/data/workspace",
				RateLimitEnabled:    false,
				AutoMigrate:         false,
				AutoSeed:            false,
				ProjectContextGlobs: defaultGlobs,
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
				Addr:                ":3001",
				DatabaseURL:         "postgres://db",
				CORSOrigins:         []string{"http://localhost:3000"},
				AllowedHosts:        []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:            slog.LevelInfo,
				DataDir:             "/data/x",
				CloneDir:            "/clones",
				RateLimitEnabled:    true,
				AutoMigrate:         true,
				AutoSeed:            true,
				ProjectContextGlobs: defaultGlobs,
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
				Addr:                ":3001",
				DatabaseURL:         "postgres://db",
				CORSOrigins:         []string{"http://localhost:3000"},
				AllowedHosts:        []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:            slog.LevelInfo,
				DataDir:             "/data",
				CloneDir:            "/data/workspace",
				RateLimitEnabled:    true,
				AutoMigrate:         true,
				AutoSeed:            true,
				ProjectContextGlobs: defaultGlobs,
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
				Addr:                ":3001",
				DatabaseURL:         "postgres://db",
				CORSOrigins:         []string{"http://localhost:3000"},
				AllowedHosts:        []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:            slog.LevelInfo,
				DataDir:             "/data",
				CloneDir:            "/data/workspace",
				RateLimitEnabled:    true,
				AutoMigrate:         true,
				AutoSeed:            true,
				LLMProviderOverride: "mock",
				LLMMockDelay:        1500 * time.Millisecond,
				ProjectContextGlobs: []string{"docs/*.md", "{a,b}/**/*.md"},
			},
		},
		{
			name: "mock delay bounds are inclusive",
			env: map[string]string{
				"DATABASE_URL": "postgres://db", "DEVDIGEST_DATA_DIR": "/data", "LLM_MOCK_DELAY_MS": "60000",
			},
			want: config.Config{
				Addr:                ":3001",
				DatabaseURL:         "postgres://db",
				CORSOrigins:         []string{"http://localhost:3000"},
				AllowedHosts:        []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:            slog.LevelInfo,
				DataDir:             "/data",
				CloneDir:            "/data/workspace",
				RateLimitEnabled:    true,
				AutoMigrate:         true,
				AutoSeed:            true,
				LLMMockDelay:        60 * time.Second,
				ProjectContextGlobs: defaultGlobs,
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
