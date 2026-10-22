package app

import (
	"context"

	"dev-digest-go/backend/internal/smartdiff/domain"
)

// API is the smart-diff use case as transport sees it; *Service implements it. Failures are
// apperr errors that httpx renders; ids are UUIDs by the time they get here.
type API interface {
	// Get builds the smart diff of a pull request; 404 "Pull request not found" when it is not in
	// the workspace. A pull request without files yields five empty groups and TotalLines 0.
	Get(ctx context.Context, workspaceID, prID string) (domain.SmartDiff, error)
}
