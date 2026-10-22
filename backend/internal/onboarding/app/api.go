package app

import (
	"context"

	"dev-digest-go/backend/internal/onboarding/domain"
)

// API is the onboarding use cases as transport sees them; *Service implements it. Failures are
// apperr errors that httpx renders; ids are UUIDs by the time they get here.
type API interface {
	// Get returns the stored tour with its staleness (domain.State; a nil Tour is status none) and
	// never calls a model. The current SHA is the index state's last indexed SHA (empty: the tour is stale with
	// index_changed, as Node compares the stored SHA with null); a stored document that fails validation reads as none. A repository
	// outside the workspace is 404 code repo_not_found "Repo not found".
	Get(ctx context.Context, workspaceID, repoID string) (domain.State, error)
	// Generate reads the index and the clone, makes the model call, stores and returns the tour
	// (Stale false, Tour non-nil). Checks in order: the repository (404 repo_not_found), no
	// generation running for it (409 generation_in_progress "A tour is already being generated
	// for this repository"), index readiness (422 index_not_ready "The repository index is not
	// ready yet"), then the reads and the call: a missing key is 422 provider_not_configured "The
	// <provider> provider is not configured" with details {"provider": <provider>}; any other
	// failure is 502 generation_failed "The model call failed", an expired 120 s budget 502
	// generation_failed "Generation timed out". Provider texts never reach the response. A failed
	// generation writes nothing; the in-flight marker is released on every outcome.
	Generate(ctx context.Context, workspaceID, repoID string) (domain.State, error)
}

// Lifecycle is what the composition root calls on the use cases outside any request.
type Lifecycle interface {
	// Shutdown cancels the generations still running and waits for them (bounded by ctx), so no
	// in-flight marker survives; the shutdown order of cmd/server runs it with the review executor,
	// before Echo drains.
	Shutdown(ctx context.Context) error
}
