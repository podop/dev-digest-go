package app

import (
	"context"
	"time"
)

// Exit codes of the runner (spec 7.8.9): 0 only when everything succeeded and the gate did not
// trigger.
const (
	ExitOK     = 0
	ExitFailed = 1
)

// Config is fixed by the binary, not read from the environment.
type Config struct {
	// BuildID is the source commit (12 hex) the binary was built from, or "dev"; it appears in the
	// trace line.
	BuildID string
}

// Deps are the ports of one run.
type Deps struct {
	Env    Env
	Files  Files
	GitHub GitHub
	// GitLab and GitDiff serve GitLab mode (DEVDIGEST_PLATFORM=gitlab_ci) only; GitHub mode never
	// reads them. GitLab may be nil when post_as is none; GitDiff is always needed in GitLab mode.
	GitLab  GitLab
	GitDiff GitDiff
	// LLM serves an openrouter manifest.
	LLM LLM
	// Providers serves an openai, anthropic or custom manifest; nil fails those runs before any
	// GitHub call.
	Providers Providers
	Out       Out
	// Engine is the review engine; nil = reviewer/app.Run.
	Engine Engine
	// Now is the clock of the engine's wall time; nil = time.Now.
	Now func() time.Time
}

// RunFunc is the runner's entry point, implemented by Run (run.go). It reads the inputs of spec
// 7.8.3 through deps.Env, writes the job-log lines of spec 7.8.9 through deps.Out and returns
// ExitOK or ExitFailed. A failure before the artifact is written (manifest, skills, context,
// token check, diff, model call) writes no artifact and posts nothing; a posting failure keeps
// the artifact and exits ExitFailed.
type RunFunc func(ctx context.Context, cfg Config, deps Deps) int
