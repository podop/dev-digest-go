// Package runnerbin holds the Go CI runner executable the server ships with its CI export. The
// image build compiles the runner (cmd/devdigest-runner) first and the server embeds it (build
// tag devdigest_runner, embed.go); every other build has none (none.go) and the export answers
// 500 runner_bundle_missing. The encoded file and its checksum are computed on first use and kept
// for the life of the process.
package runnerbin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sync"

	"dev-digest-go/backend/internal/ci/app"
)

// Bundle is the runner executable of one server build.
type Bundle struct {
	exe     []byte
	buildID string

	once     sync.Once
	artifact app.RunnerArtifact
}

var _ app.RunnerBundle = (*Bundle)(nil)

// New returns a Bundle over the executable exe, stamped with buildID (12 hex or "dev"). An empty
// exe is a build without a runner.
func New(exe []byte, buildID string) *Bundle { return &Bundle{exe: exe, buildID: buildID} }

// Runner returns the encoded runner: gzip of the executable (best compression, no name or time in
// the header, so equal executables give equal bytes), standard base64 on one line, then "\n". The
// checksum is the SHA-256 of the executable itself. False when the build has no runner.
func (b *Bundle) Runner() (app.RunnerArtifact, bool) {
	if len(b.exe) == 0 {
		return app.RunnerArtifact{}, false
	}
	b.once.Do(func() {
		sum := sha256.Sum256(b.exe)
		b.artifact = app.RunnerArtifact{Encoded: encode(b.exe), SHA256: hex.EncodeToString(sum[:]), BuildID: b.buildID}
	})
	return b.artifact, true
}

func encode(exe []byte) string {
	var zipped bytes.Buffer
	zw, err := gzip.NewWriterLevel(&zipped, gzip.BestCompression)
	if err != nil { // only for an invalid level
		panic("runnerbin: gzip level: " + err.Error())
	}
	// Writes to a bytes.Buffer cannot fail.
	_, _ = zw.Write(exe)
	_ = zw.Close()
	return base64.StdEncoding.EncodeToString(zipped.Bytes()) + "\n"
}
