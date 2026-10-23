// Package domain holds the pure rules of the Go CI runner (spec 7.8), ported from the Node
// agent-runner (dev-digest at d1c9093): the PR context, the diff strip and parse, the CI gate,
// the review payload and the result artifact. It imports the standard library and reviewer/domain
// only, so the runner's import closure stays the review engine and the LLM layer.
//
// Texts that reach the job log or the artifact are the Node texts; where the Node code leans on
// JavaScript semantics (Number(), String(), JSON.stringify) the Go code reproduces them.
package domain

// RunnerError is a configuration or pre-flight failure of the runner (missing or invalid
// manifest, missing skill file, unresolvable CI context, oversize input). Every one of them is
// handled the same way: exit 1, no artifact, nothing posted. Its text is the job-log message.
type RunnerError struct {
	Message string
}

func (e *RunnerError) Error() string { return e.Message }

// NewRunnerError returns a RunnerError with the given message.
func NewRunnerError(message string) *RunnerError { return &RunnerError{Message: message} }
