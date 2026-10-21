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
			env:  map[string]string{"DATABASE_URL": "postgres://db"},
			want: config.Config{
				Addr:        ":3001",
				DatabaseURL: "postgres://db",
				CORSOrigins: []string{"http://localhost:3000"},
				LogLevel:    slog.LevelInfo,
			},
		},
		{
			name: "overrides",
			env: map[string]string{
				"DATABASE_URL": "postgres://db",
				"API_ADDR":     ":9000",
				"CORS_ORIGINS": " http://a:1 , http://b:2 ,",
				"LOG_LEVEL":    "debug",
			},
			want: config.Config{
				Addr:        ":9000",
				DatabaseURL: "postgres://db",
				CORSOrigins: []string{"http://a:1", "http://b:2"},
				LogLevel:    slog.LevelDebug,
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
