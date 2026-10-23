//go:build devdigest_runner

package runnerbin_test

import (
	"regexp"
	"testing"

	"dev-digest-go/backend/internal/ci/adapters/runnerbin"
)

func TestDefault_TaggedBuildCarriesTheRunner(t *testing.T) {
	got, ok := runnerbin.Default().Runner()
	if !ok {
		t.Fatal("Default().Runner() ok = false with the devdigest_runner tag, want true")
	}
	if !regexp.MustCompile(`^([0-9a-f]{12}|dev)$`).MatchString(got.BuildID) {
		t.Errorf("BuildID = %q, want 12 hex or dev", got.BuildID)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got.SHA256) {
		t.Errorf("SHA256 = %q, want 64 lowercase hex", got.SHA256)
	}
}
