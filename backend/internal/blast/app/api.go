package app

import (
	"context"

	"dev-digest-go/backend/internal/blast/domain"
)

// API is the blast use cases as transport sees them; *Service implements it. Failures are apperr
// errors that httpx renders; ids are UUIDs by the time they get here.
type API interface {
	// Radius builds the blast radius of a pull request from one BlastRadius port read over its
	// stored changed paths and writes the info log line "blast radius read from index" (spec
	// section 7.15); 404 "Pull request not found"; a database error is 500 internal_error. A pull
	// request without files answers an empty degraded result with reason no_data.
	Radius(ctx context.Context, workspaceID, prID string) (domain.BlastRadius, error)
	// History returns the prior merged pull requests that touched the same files, newest first, at
	// most domain.HistoryMax; 404 "Pull request not found". Every GitHub failure (no token
	// included) and a pull request without files yield an empty list, the former with the warn
	// log "prior PR history unavailable; returning an empty list".
	History(ctx context.Context, workspaceID, prID string) ([]domain.PriorPR, error)
}

// Provider is what other contexts read of the blast radius (the brief's peers adapter); *Service
// implements it too.
type Provider interface {
	// RadiusFor builds the blast radius of a repository's changed paths without a pull request
	// lookup, writing the same info log line as Radius. Errors are the port's.
	RadiusFor(ctx context.Context, repoID string, changed []string) (domain.BlastRadius, error)
}
