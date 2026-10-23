package runnerbin_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/ci/adapters/runnerbin"
)

func TestBundle_Runner(t *testing.T) {
	exe := bytes.Repeat([]byte("\x7fELF runner bytes "), 1000)
	got, ok := runnerbin.New(exe, "0123456789ab").Runner()
	if !ok {
		t.Fatal("Runner() ok = false, want true")
	}
	if !strings.HasSuffix(got.Encoded, "\n") || strings.Count(got.Encoded, "\n") != 1 {
		t.Errorf("Encoded must be one line ending in a newline, got %d newlines", strings.Count(got.Encoded, "\n"))
	}
	zipped, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(got.Encoded, "\n"))
	if err != nil {
		t.Fatalf("Encoded is not standard base64: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		t.Fatalf("Encoded is not gzip: %v", err)
	}
	back, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	if !bytes.Equal(back, exe) {
		t.Error("decoded runner differs from the executable")
	}
	sum := sha256.Sum256(exe)
	if want := hex.EncodeToString(sum[:]); got.SHA256 != want {
		t.Errorf("SHA256 = %s, want %s (of the executable)", got.SHA256, want)
	}
	if got.BuildID != "0123456789ab" {
		t.Errorf("BuildID = %q, want 0123456789ab", got.BuildID)
	}
}

func TestBundle_RunnerIsStableAndReproducible(t *testing.T) {
	exe := []byte("same executable")
	a, _ := runnerbin.New(exe, "dev").Runner()
	b := runnerbin.New(exe, "dev")
	first, _ := b.Runner()
	second, _ := b.Runner()
	if a != first || first != second {
		t.Error("Runner() of equal executables must return equal artifacts, every time")
	}
}

func TestBundle_NoRunner(t *testing.T) {
	for name, exe := range map[string][]byte{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			if _, ok := runnerbin.New(exe, "dev").Runner(); ok {
				t.Error("Runner() ok = true for an empty executable, want false")
			}
		})
	}
}
