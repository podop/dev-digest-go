// Package app holds the use cases of the blast context: the blast radius of a pull request (read
// through the repo-intel BlastRadius port) and the prior merged pull requests that touched the
// same files (read from GitHub). It writes nothing and makes no LLM call.
//
// This file holds the ports the use cases depend on; api.go holds the use cases' own interfaces.
package app

import (
	"context"

	"dev-digest-go/backend/internal/blast/domain"
)

// Store reads the pull request rows the use cases need.
type Store interface {
	// FindPull returns a pull request of the workspace with its repository's owner and name;
	// domain.ErrNotFound when it is not in the workspace.
	FindPull(ctx context.Context, workspaceID, prID string) (domain.Pull, error)
	// ChangedPaths returns the paths of the pull request's stored files in path byte order
	// (COLLATE "C"), distinct; a pull request without files yields empty.
	ChangedPaths(ctx context.Context, prID string) ([]string, error)
}

// RepoIntel is the part of the repo-intel read port the blast radius uses (the peers adapter over
// repointel/app.Reader.BlastRadius). It never fails for a missing, partial or failed index or a
// switched-off flag: those are degraded results; only a real failure (a database error) is an
// error.
type RepoIntel interface {
	BlastRadius(ctx context.Context, repoID string, changed []string) (domain.PortResult, error)
}

// GitHub is the GitHub read the history uses (the peers adapter over platform/github). Any error,
// "GITHUB_TOKEN is not configured" included, makes the history empty.
type GitHub interface {
	// ListMergedPulls returns the repository's recently closed pull requests that were merged,
	// with their files; one page of at most limit entries.
	ListMergedPulls(ctx context.Context, owner, name string, limit int) ([]domain.MergedPR, error)
}
