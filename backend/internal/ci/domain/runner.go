package domain

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The runner as the export ships it (spec 7.8.2, D6003, D6008).
const (
	// RunnerBinaryName is the file name the checksum file lists; the workflow decodes the runner
	// to this name in $RUNNER_TEMP and verifies it there.
	RunnerBinaryName = "devdigest-runner"

	// MaxRunnerBytes and MaxRunnerEncodedBytes are the size budget of the executable and of the
	// encoded file (NFR6006).
	MaxRunnerBytes        = 16 << 20
	MaxRunnerEncodedBytes = 8 << 20
)

// ErrRunnerEncoding is returned by DecodeRunner for text that is not the runner encoding.
var ErrRunnerEncoding = errors.New("invalid runner encoding")

// EncodeRunner returns the content of RunnerPath: the gzip of the executable (best compression,
// no timestamp or name, so the same bytes always give the same text) as standard base64 on one
// line followed by "\n".
func EncodeRunner(exe []byte) (string, error) {
	var zipped bytes.Buffer
	zw, err := gzip.NewWriterLevel(&zipped, gzip.BestCompression)
	if err != nil {
		return "", fmt.Errorf("encode runner: %w", err)
	}
	if _, err := zw.Write(exe); err != nil {
		return "", fmt.Errorf("encode runner: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("encode runner: %w", err)
	}
	return base64.StdEncoding.EncodeToString(zipped.Bytes()) + "\n", nil
}

// DecodeRunner reverses EncodeRunner, inflating at most maxBytes bytes. It exists for the
// builders and tests that check what they ship; the workflow decodes with base64 -d | gzip -dc.
func DecodeRunner(encoded string, maxBytes int64) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(encoded, "\n"))
	if err != nil {
		return nil, fmt.Errorf("%w: base64", ErrRunnerEncoding)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip", ErrRunnerEncoding)
	}
	exe, err := io.ReadAll(io.LimitReader(zr, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip", ErrRunnerEncoding)
	}
	if int64(len(exe)) > maxBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrRunnerEncoding, maxBytes)
	}
	return exe, nil
}

// RunnerSHA256 is the lowercase hex SHA-256 of the executable.
func RunnerSHA256(exe []byte) string {
	sum := sha256.Sum256(exe)
	return hex.EncodeToString(sum[:])
}

// RunnerSumFile is the content of RunnerSumPath in sha256sum text format: the hex digest, two
// spaces, the binary's name, a newline.
func RunnerSumFile(sha256Hex string) string {
	return sha256Hex + "  " + RunnerBinaryName + "\n"
}
