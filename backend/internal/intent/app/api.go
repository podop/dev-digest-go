package app

import (
	"context"
	"encoding/json"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Response is the answer of the intent routes: the stored record (nil when none) and whether it
// is stale.
type Response struct {
	Intent *domain.Record
	Stale  bool
}

// API is the intent use cases as transport sees them; *Service implements it. Failures are
// apperr errors that httpx renders; ids are UUIDs by the time they get here.
type API interface {
	// Get answers GET /pulls/:id/intent: the stored record and its staleness (spec 7.4.8: the key
	// computed now differs from the stored input_hash; false when there is no record or the key
	// cannot be computed). It makes no LLM and no GitHub call. 404 "Pull request not found".
	Get(ctx context.Context, workspaceID, prID string) (Response, error)
	// Refresh answers POST /pulls/:id/intent/refresh: it re-derives the intent ignoring the stored
	// row (joining an in-flight derivation of the same key) and returns it with Stale false.
	// 404 "Pull request not found", 404 "Repo not found"; a derivation that ends unavailable is
	// 502 code intent_unavailable "Intent derivation failed: <warning>". It works whatever
	// REVIEW_INTENT_ENABLED is.
	Refresh(ctx context.Context, workspaceID, prID string) (Response, error)
}

// Sink receives the events of a derivation (kind domain.EventInfo or domain.EventResult; data is
// nil or a JSON document) as it proceeds. A caller that joins a running derivation receives its
// events from then on. It is called from the derivation's goroutine, possibly after Resolve's
// caller stopped waiting, and must be safe for concurrent use. A nil Sink discards.
type Sink func(kind, msg string, data json.RawMessage)

// ResolveInput is one request to derive or reuse an intent.
type ResolveInput struct {
	WorkspaceID string
	Pull        domain.Pull
	Repo        domain.Repo
	// ChangedFiles and DiffExcerpt feed the fallback sources of an inferred intent. Refresh
	// passes the stored paths and no excerpt; the review path passes the diff's paths and the raw
	// diff text.
	ChangedFiles []string
	DiffExcerpt  string
	// Force skips only the stored-row check (refresh).
	Force bool
}

// Result is the outcome of Resolve.
type Result struct {
	// Used is true when Record holds the intent; false is an unavailable intent with Warning.
	Used bool
	// Record is the stored row: the one a cache hit found, or the one the derivation wrote.
	Record domain.Record
	// CacheHit is true when Record was reused without a derivation.
	CacheHit bool
	// Warning is the message of the failure when Used is false (spec 7.4.7).
	Warning string
}

// Resolver is the pre-work entry point the reviews peers adapter and the routes' own refresh
// use; *Service implements it.
type Resolver interface {
	// Resolve returns the stored record when its input_hash equals the key computed now (unless
	// Force), else derives, stores and returns a new one. It never fails: every error of the key
	// computation, the reads, the LLM call or the budget becomes an unavailable Result, and a
	// warning event "warning: intent unavailable — <message>" with data
	// {"warning":"intent_unavailable"} goes to sink. Concurrent calls with the same pull request
	// and key share one derivation (platform/inflight.Flights) bounded by the budget; it is
	// aborted when every caller that has a cancellation has cancelled (a caller whose ctx cannot
	// be cancelled never counts), and a caller that cancels stops waiting at once.
	Resolve(ctx context.Context, in ResolveInput, sink Sink) Result
	// Stored returns the pull request's stored intent for the brief (nil when there is none, a
	// read error is an error); it makes no key computation.
	Stored(ctx context.Context, prID string) (*reviewer.Intent, error)
}

// Stopper ends the service's in-flight derivations at shutdown (before Echo drains): it cancels
// every flight, waits for them within ctx, and refuses new ones afterwards (they end unavailable).
type Stopper interface {
	Shutdown(ctx context.Context) error
}
