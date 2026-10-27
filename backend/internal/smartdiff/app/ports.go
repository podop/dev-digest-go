// Package app holds the smart-diff use case: it reads a pull request's stored files and the
// findings of its current reviews and builds the grouped read model. It makes no LLM and no
// GitHub call and writes nothing.
//
// This file holds the port the use case depends on; api.go holds the use case's own interface,
// which transport depends on.
package app

import (
	"context"

	"dev-digest-go/backend/internal/smartdiff/domain"
)

// Store is the persistence the use case reads. The adapter reads the tables of other contexts
// with its own row types.
type Store interface {
	// PullFiles returns the stored files of a pull request of the workspace in no particular
	// order (the domain sorts them); a pull request without files yields empty. A pull request
	// that is not in the workspace is domain.ErrNotFound.
	PullFiles(ctx context.Context, workspaceID, prID string) ([]domain.PullFile, error)
	// RepoProvider returns the provider ("github" or "gitlab") of the repository of a pull request
	// of the workspace; a pull request that is not in the workspace is domain.ErrNotFound.
	RepoProvider(ctx context.Context, workspaceID, prID string) (string, error)
	// CurrentFindings returns every finding, dismissed ones included, of the newest
	// kind='review' review of each agent of the pull request (reviews with a NULL agent form one
	// group; newest by created_at, ties by id). Older reviews' findings are not returned.
	CurrentFindings(ctx context.Context, prID string) ([]domain.FindingRef, error)
}
