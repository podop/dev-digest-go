//go:build devdigest_runner

package runnerbin

import (
	_ "embed"
	"strings"
)

// Both files are written by the image build (backend/Dockerfile) before the server is compiled
// with -tags devdigest_runner; they are not in git.
var (
	//go:embed devdigest-runner-linux-amd64
	embeddedExe []byte
	//go:embed build-id
	embeddedBuildID string
)

// Default returns the runner embedded in this server binary.
func Default() *Bundle { return New(embeddedExe, strings.TrimSpace(embeddedBuildID)) }
