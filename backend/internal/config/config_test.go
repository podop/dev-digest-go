package config_test

import (
	"log/slog"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
)

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
				Addr:             ":3001",
				DatabaseURL:      "postgres://db",
				CORSOrigins:      []string{"http://localhost:3000"},
				AllowedHosts:     []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:         slog.LevelInfo,
				DataDir:          "/home/u/.devdigest",
				CloneDir:         "/home/u/.devdigest/workspace",
				RateLimitEnabled: true,
				AutoMigrate:      true,
				AutoSeed:         true,
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
				Addr:             ":9000",
				DatabaseURL:      "postgres://db",
				CORSOrigins:      []string{"http://a:1", "http://b:2"},
				AllowedHosts:     []string{"api.local", "Localhost"},
				LogLevel:         slog.LevelDebug,
				DataDir:          "/data",
				CloneDir:         "/data/workspace",
				RateLimitEnabled: false,
				AutoMigrate:      false,
				AutoSeed:         false,
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
				Addr:             ":3001",
				DatabaseURL:      "postgres://db",
				CORSOrigins:      []string{"http://localhost:3000"},
				AllowedHosts:     []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:         slog.LevelInfo,
				DataDir:          "/data/x",
				CloneDir:         "/clones",
				RateLimitEnabled: true,
				AutoMigrate:      true,
				AutoSeed:         true,
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
				Addr:             ":3001",
				DatabaseURL:      "postgres://db",
				CORSOrigins:      []string{"http://localhost:3000"},
				AllowedHosts:     []string{"localhost", "127.0.0.1", "[::1]", "backend"},
				LogLevel:         slog.LevelInfo,
				DataDir:          "/data",
				CloneDir:         "/data/workspace",
				RateLimitEnabled: true,
				AutoMigrate:      true,
				AutoSeed:         true,
			},
		},
		{name: "missing database url", env: map[string]string{}, wantErr: true},
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
