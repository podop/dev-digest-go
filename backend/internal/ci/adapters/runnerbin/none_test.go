//go:build !devdigest_runner

package runnerbin_test

import (
	"testing"

	"dev-digest-go/backend/internal/ci/adapters/runnerbin"
)

func TestDefault_UntaggedBuildHasNoRunner(t *testing.T) {
	if _, ok := runnerbin.Default().Runner(); ok {
		t.Error("Default().Runner() ok = true in a build without the devdigest_runner tag, want false")
	}
}
