// Package abouthttp serves GET /about (spec FR20): build and runtime facts the Settings About
// tab shows. It is transport-only: the values are fixed at startup by the composition root and
// handed in as Info, so there is no use case, port or storage behind it.
package abouthttp

import (
	"net/http"

	"github.com/labstack/echo/v5"
)

// ConfigFormat is the plugin config format reported as config_format (spec D14).
const ConfigFormat = "devdigest-plugin/v1"

// runnerKind is the runner.kind of the Go backend; the UI contract accepts only this literal.
const runnerKind = "go"

// Info is what the composition root knows about this process.
type Info struct {
	// Version is the build-time version ("dev" when not set by -ldflags).
	Version string
	// BuildID is DEVDIGEST_BUILD_ID or "dev".
	BuildID string
	// RunnerVersion is the runner protocol version reported as runner.version.
	RunnerVersion string
	// DataDir is the absolute data directory.
	DataDir string
	// SecretsFile is the absolute path of the secrets file.
	SecretsFile string
}

// AddRoutes registers GET /about. Registering it on the Echo built by httpx.New puts it behind
// the same Host, Origin and rate-limit guards as every other route.
func AddRoutes(e *echo.Echo, info Info) {
	body := toResponse(info)
	e.GET("/about", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, body)
	})
}

// The wire shape of the About contract (frontend/src/vendor/shared/contracts/platform.ts).
// Struct field order is the key order of the JSON.

type aboutResponse struct {
	Version      string         `json:"version"`
	BuildID      string         `json:"build_id"`
	Runner       runnerResponse `json:"runner"`
	ConfigFormat string         `json:"config_format"`
	DataDir      string         `json:"data_dir"`
	SecretsFile  string         `json:"secrets_file"`
}

type runnerResponse struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

func toResponse(info Info) aboutResponse {
	return aboutResponse{
		Version:      info.Version,
		BuildID:      info.BuildID,
		Runner:       runnerResponse{Kind: runnerKind, Version: info.RunnerVersion},
		ConfigFormat: ConfigFormat,
		DataDir:      info.DataDir,
		SecretsFile:  info.SecretsFile,
	}
}
