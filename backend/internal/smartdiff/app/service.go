package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/smartdiff/domain"
)

// Service implements API.
type Service struct {
	store Store
}

var _ API = (*Service)(nil)

// NewService builds a Service. A nil store is a programming error and panics at startup.
func NewService(store Store) *Service {
	if store == nil {
		panic("smartdiff/app: NewService needs a store")
	}
	return &Service{store: store}
}

// Get builds the smart diff of a pull request of the workspace.
func (s *Service) Get(ctx context.Context, workspaceID, prID string) (domain.SmartDiff, error) {
	files, err := s.store.PullFiles(ctx, workspaceID, prID)
	if err != nil {
		return domain.SmartDiff{}, notFoundOr(err, "load pull request files")
	}
	findings, err := s.store.CurrentFindings(ctx, prID)
	if err != nil {
		return domain.SmartDiff{}, notFoundOr(err, "load current findings")
	}
	return domain.Build(files, findings), nil
}

// notFoundOr turns domain.ErrNotFound into the 404 of the route; any other failure is wrapped with
// what failed and left to httpx (500).
func notFoundOr(err error, what string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("Pull request not found").WithCause(err)
	}
	return fmt.Errorf("%s: %w", what, err)
}
