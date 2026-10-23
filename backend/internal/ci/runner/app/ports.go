// Package app is the Go CI runner's use case (spec 7.8): Run loads the agent manifest and skills
// the export wrote into the repository, fetches the pull request's diff, runs the same review
// engine local reviews and evals run, writes the result artifact, posts the review and returns the
// process exit code. It does no I/O of its own: environment, files, GitHub, the model and the job
// log are injected, so the runner's import closure stays the engine, the LLM layer and a YAML
// parser (no Echo, GORM, database driver, git or secrets-file code).
//
// This file holds the ports; api.go holds the entry point and its inputs.
package app

import (
	"context"
	"errors"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ErrFileTooLarge is returned by Files.ReadFile when the file is larger than the cap.
var ErrFileTooLarge = errors.New("file too large")

// Env is the process environment (spec 7.8.3). It is the only channel for secrets.
type Env interface {
	// Lookup returns the value of a variable and whether it is set; a set empty value is
	// ("", true), which matters for variables that default only when unset.
	Lookup(key string) (string, bool)
	// Cwd returns the working directory, the base of the default DEVDIGEST_DIR and result path.
	Cwd() string
}

// Files is the file system the runner reads its configuration from and writes the artifact to.
type Files interface {
	// ReadFile returns the content of path; ErrFileTooLarge when it is larger than max bytes.
	ReadFile(path string, max int64) ([]byte, error)
	// ReadDir returns the names of the entries of a directory.
	ReadDir(path string) ([]string, error)
	// WriteFile writes data to path, replacing it.
	WriteFile(path string, data []byte) error
}

// PullRef names the pull request under review.
type PullRef struct {
	Owner, Repo string
	Number      int
}

// InlineComment is an inline comment of a review.
type InlineComment struct {
	Path string
	Line int
	Body string
}

// ReviewPost is the body of a pull request review. Event is APPROVE, REQUEST_CHANGES or COMMENT;
// the use case maps APPROVE to COMMENT before the call.
type ReviewPost struct {
	Body     string
	Event    string
	Comments []InlineComment
}

// APIError is a non-2xx GitHub answer. Its text is the complete job-log message of spec 7.8.5 and
// 7.8.9, "GitHub API error ... (<url>): <status> <body cut to 1,000 bytes, token redacted>".
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// GitHub is the part of the GitHub REST API the runner calls (adapters/ghapi). The token travels
// with every call and never appears in a returned error. Redirects are not followed.
type GitHub interface {
	// FetchDiff returns the unified diff of the pull request (the diff media type), at most 64 MiB;
	// a larger body or a non-2xx answer is an error.
	FetchDiff(ctx context.Context, token string, pr PullRef) (string, error)
	// PostReview posts a pull request review; a non-2xx answer is an *APIError.
	PostReview(ctx context.Context, token string, pr PullRef, post ReviewPost) error
	// PostComment posts an issue comment on the pull request; a non-2xx answer is an *APIError.
	PostComment(ctx context.Context, token string, pr PullRef, body string) error
}

// LLM builds the engine's LLM port for a model (adapters over the OpenRouter client, with the
// runner's fixed endpoint, retries and timeouts). It is bound to the OPENROUTER_API_KEY of the
// environment; an empty key fails at the first call, not here.
type LLM interface {
	For(model string) reviewerapp.LLM
}

// Out is the job log. Lines carry no environment value other than those spec 7.8.9 lists, no
// token or key, and no diff or prompt text.
type Out interface {
	Stdout(line string)
	Stderr(line string)
}

// Engine is the review engine's entry point (reviewer/app.Run); tests script it.
type Engine func(ctx context.Context, in reviewerapp.Input, llm reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error)
