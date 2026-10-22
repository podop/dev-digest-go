// Package app is the review engine's use case: Run (engine.go) picks single pass or map-reduce
// for a parsed diff, assembles the prompt of every chunk, asks the model through the LLM port,
// reduces the partial reviews, grounds the findings against the diff and scores the result. It
// does no I/O of its own: the model, the progress log and the cancellation check are injected.
//
// This file holds the engine's input, options and ports.
package app

import (
	"context"
	"errors"

	"dev-digest-go/backend/internal/reviewer/domain"
)

// ErrCancelled is returned by Run when Options.Cancelled reports true at a checkpoint (before
// each chunk call). A cancelled ctx surfaces as the LLM port's own error instead; the caller
// decides which of the two it is by asking whether its run was cancelled.
var ErrCancelled = errors.New("review cancelled")

// Input is everything one engine run needs. The caller resolves skills, documents and repo-intel
// context beforehand; the engine only assembles and reviews.
type Input struct {
	// SystemPrompt is the agent's system prompt (trusted); the engine appends the injection guard.
	SystemPrompt string
	// Diff is the parsed diff of the pull request.
	Diff domain.Diff
	// Strategy is the agent's strategy.
	Strategy domain.Strategy
	// Skills are the rendered skill blocks ("### <name>" ...), in link order; nil = none.
	Skills []string
	// Specs are the included project-context documents, in prompt order; nil = none.
	Specs []domain.ProjectDoc
	// Callers is the callers digest and RepoMap the repo skeleton; "" = omit the section.
	Callers, RepoMap string
	// PRDescription is the PR body; the engine cuts it and omits it when blank.
	PRDescription string
	// Intent is the PR intent of the request, shared read-only by every run and call of it; nil =
	// none (the switch is off or the intent is unavailable): no "## PR intent" section, and the
	// scope policy resets every out_of_scope flag. The engine renders it and never modifies it.
	Intent *domain.Intent
	// Task is the task line, including the hot-file note when there is one.
	Task string
}

// Options tune one engine run.
type Options struct {
	// MapConcurrency is the number of chunk calls in flight; 0 = domain.DefaultMapConcurrency.
	MapConcurrency int
	// Cancelled is polled before each chunk call; true makes Run return ErrCancelled. Nil = never.
	Cancelled func() bool
}

// Call is the part of a model call the engine builds: the messages, the content-free section
// description for the prompt log, and the call's place among the review's calls.
type Call struct {
	// Messages are the system message and the user message.
	Messages []domain.Message
	// Sections describe the messages for the prompt_assembled log line, in render order.
	Sections []domain.SectionMeta
	// Chunk is the call's position; Chunk.Index is 0-based.
	Chunk domain.ChunkRef
}

// CallResult is one validated model answer.
type CallResult struct {
	// Review is the answer, validated against the Review schema.
	Review domain.Review
	// Raw is the model's last raw output.
	Raw string
	// Usage sums every attempt of the call (re-prompts included).
	Usage domain.Usage
}

// LLM makes the structured Review call. The implementation (reviewer/adapters/llmcall) is bound
// to one run: it knows the provider, model, run id, OpenRouter session id and the per-attempt
// usage observer, so none of them travel through the engine.
type LLM interface {
	// Review sends the call and returns the validated answer. It honours ctx (the run's context,
	// cancelled when the run is cancelled) and returns an error when the call fails, including
	// after the schema failed on the last re-prompt.
	Review(ctx context.Context, c Call) (CallResult, error)
}

// Events receives the engine's progress lines (spec 7.5 step 7); the run log publishes them on
// the run bus. It is called from the goroutines of concurrent chunk calls and must be safe for
// concurrent use.
type Events interface {
	// Emit reports one line. data is nil or a struct (never a map: the receiver marshals it and
	// the struct's field order is the JSON key order), for example OversizeWarning or FileData.
	Emit(kind domain.EventKind, msg string, data any)
}

// OversizeWarning is the data of the "warning: <label> diff (...) exceeds maxDiffChars" event.
type OversizeWarning struct {
	Warning   string `json:"warning"` // "diff_oversize"
	File      string `json:"file"`
	Parts     int    `json:"parts"`
	Truncated bool   `json:"truncated"`
}

// FileData is the data of the per-chunk tool event.
type FileData struct {
	File string `json:"file"`
}
