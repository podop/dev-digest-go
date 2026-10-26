package app

import (
	"context"
	"fmt"

	"dev-digest-go/backend/internal/ci/domain"
)

// RepoStore reads the installations for the per-repository listing.
type RepoStore interface {
	// ListRepoInstallations returns the workspace's gha installations of its agents, each with the
	// agent's name and its newest run (by ran_at) of the workspace; nil NewestRun = none. The order
	// is not part of the contract: the domain groups and orders.
	ListRepoInstallations(ctx context.Context, workspaceID string) ([]domain.RepoInstallation, error)
}

// InstallationsService implements RepoInstallations.
type InstallationsService struct {
	store RepoStore
}

var _ RepoInstallations = (*InstallationsService)(nil)

// NewInstallationsService builds the service. A nil store is a wiring bug and panics at startup.
func NewInstallationsService(store RepoStore) *InstallationsService {
	if store == nil {
		panic("ci app: InstallationsService needs a store")
	}
	return &InstallationsService{store: store}
}

// ListByRepo groups the workspace's gha installations by repository (spec FR1).
func (s *InstallationsService) ListByRepo(ctx context.Context, workspaceID string) ([]domain.RepoGroup, error) {
	list, err := s.store.ListRepoInstallations(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list installations by repo: %w", err)
	}
	return domain.GroupByRepo(list), nil
}
