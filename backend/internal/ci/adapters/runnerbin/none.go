//go:build !devdigest_runner

package runnerbin

// Default returns a Bundle without a runner: this build was not made by the image build, so the
// CI export answers 500 runner_bundle_missing.
func Default() *Bundle { return New(nil, "") }
