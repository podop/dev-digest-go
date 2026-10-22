// Package app holds the use cases of the intent context: deriving a PR's intent as shared review
// pre-work and for the refresh route, reading the stored record with its staleness, and the
// single-flight that lets concurrent callers share one derivation. This file holds the ports the
// use cases depend on and the constructor inputs; api.go holds the use cases' own interfaces.
package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
)

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// Pulls reads the rows of other contexts a derivation needs: the pull request, its repository,
// its commits and its stored changed paths. The adapter (adapters/postgres) reads their tables
// with its own row types. A missing row is domain.ErrNotFound.
type Pulls interface {
	// Pull returns a pull request of the workspace.
	Pull(ctx context.Context, workspaceID, prID string) (domain.Pull, error)
	// Repo returns a repository by id.
	Repo(ctx context.Context, repoID string) (domain.Repo, error)
	// Commits returns the pull request's commits ordered by committed_at ascending (NULL last),
	// then id (spec D5009); none is not an error.
	Commits(ctx context.Context, prID string) ([]domain.Commit, error)
	// ChangedFiles returns the pull request's stored file paths in byte order (COLLATE "C");
	// none is not an error. Only the refresh route uses it: the review path brings its own.
	ChangedFiles(ctx context.Context, prID string) ([]string, error)
}

// Settings reads the workspace's settings bag (settings.Service.Get); the service resolves the
// review_intent model from it with llm.ResolveFeatureModel.
type Settings interface {
	Get(ctx context.Context, workspaceID string) (map[string]json.RawMessage, error)
}

// StructuredLLM is the part of *llm.Client the derivation uses: one structured call. A missing
// provider key is an error whose text is "<KEY_NAME> is not configured" (llm.Client scrubs keys
// out of every error text).
type StructuredLLM interface {
	Structured(ctx context.Context, req llm.Request) (llm.Result, error)
}

// DocRead is the outcome of reading a linked doc. A failure is data, not an error: it becomes a
// failed source.
type DocRead struct {
	OK bool
	// Content is the doc text (UTF-8, WHATWG replacement decoding), at most
	// domain.DocFetchMaxBytes bytes; Truncated is true when it was cut to
	// domain.DocFetchMaxBytes UTF-16 units.
	Content   string
	Truncated bool
	// Reason is the failure text when OK is false (for example "unsupported file type 'x'",
	// "unsafe path", "File 'a.md' at <sha> is too large (70000 bytes > 65536)", "not found").
	Reason string
}

// Docs reads a linked doc at the pull request's head commit (spec 7.4.3, "Doc read"): the doc
// extension and path-safety checks, the blob from the clone with the size cap, and the GitHub
// contents fallback unless the clone said the blob is too large. It honours ctx, and a ctx that
// ended is reported as a failure the caller discards.
type Docs interface {
	Read(ctx context.Context, repo domain.Repo, headSHA, path string) DocRead
}

// TicketRead is the outcome of reading a linked issue.
type TicketRead struct {
	OK bool
	// Title and Body are the issue's text; Body is "" for an issue without one.
	Title, Body string
	// Reason is the failure text when OK is false: GitHub's message with the token redacted, or
	// "GITHUB_TOKEN is not configured" (D5006).
	Reason string
}

// Tickets reads a same-repo GitHub issue (spec 7.13). A number that is not a positive safe
// integer cannot name an issue and reads as a failure. It honours ctx.
type Tickets interface {
	Read(ctx context.Context, repo domain.Repo, number float64) TicketRead
}

// Store keeps the pr_intent row, one per pull request.
type Store interface {
	// Get returns the pull request's record; ok is false when there is none (a row with a NULL
	// derived_at counts as none).
	Get(ctx context.Context, prID string) (rec domain.Record, ok bool, err error)
	// Upsert writes every column of the row (insert or update on pr_id) and returns the stored
	// row, which holds the cost rounded to 6 decimals.
	Upsert(ctx context.Context, rec domain.Record) (domain.Record, error)
}

// Deps are the collaborators of a Service; every field but Clock and Log is required.
type Deps struct {
	Pulls    Pulls
	Settings Settings
	LLM      StructuredLLM
	Docs     Docs
	Tickets  Tickets
	Store    Store
	// Clock stamps derived_at; nil = time.Now.
	Clock Clock
	// Log receives the service's own lines (content-free); nil = discard.
	Log *slog.Logger
}

// Options tune a Service. Zero values select the defaults.
type Options struct {
	// Budget bounds one derivation; 0 = domain.Budget. Tests and cmd seams shorten it.
	Budget time.Duration
}
