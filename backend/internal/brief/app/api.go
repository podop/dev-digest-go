package app

import (
	"context"

	"dev-digest-go/backend/internal/brief/domain"
)

// API is the brief use cases as transport sees them; *Service implements it. Failures are apperr
// errors that httpx renders; ids are UUIDs by the time they get here.
type API interface {
	// Get returns the stored brief and its staleness and never calls a model: stale is true when
	// the stored head SHA differs from the pull request's or the stored prompt version is not
	// domain.PromptVersion; a stored document that fails validation reads as no brief. 404
	// "Pull request not found".
	Get(ctx context.Context, workspaceID, prID string) (domain.State, error)
	// Generate gathers the facts, makes the model call, stores and returns the brief (Stale false).
	// Checks in order: the pull request (404 "Pull request not found"), no generation running for
	// it (409 generation_in_progress "A brief is already being generated for this pull request"),
	// stored files (422 no_changed_files "The pull request has no changed files"), then the call:
	// a missing key is 422 provider_not_configured "The <provider> provider is not configured"
	// with details {"provider": <provider>}; any other failure is 502 generation_failed "The model
	// call failed", an expired 120 s budget 502 generation_failed "Generation timed out". Provider
	// texts never reach the response. A failed generation writes nothing and keeps the previous
	// brief; the in-flight marker is released on every outcome.
	Generate(ctx context.Context, workspaceID, prID string) (domain.State, error)
}

// Lifecycle is what the composition root calls on the use cases outside any request.
type Lifecycle interface {
	// Shutdown cancels the generations still running and waits for them (bounded by ctx), so no
	// in-flight marker survives; the shutdown order of cmd/server runs it with the review executor,
	// before Echo drains.
	Shutdown(ctx context.Context) error
}
