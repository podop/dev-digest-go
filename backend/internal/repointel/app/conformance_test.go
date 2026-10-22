package app_test

import (
	"context"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// The consumers of this package declare their own ports; these checks keep the exported method
// signatures in step with them.
var (
	// repos: indexTrigger.
	_ interface {
		TriggerIndex(ctx context.Context, workspaceID, repoID, owner, name string) error
		TriggerRefresh(ctx context.Context, workspaceID, repoID string) error
	} = (*app.Jobs)(nil)
	// conventions: sampleRanker.
	_ interface {
		ConventionSamples(ctx context.Context, repoID string, n int) ([]string, error)
	} = (*app.Reader)(nil)
	// transport/http: the service of the two routes.
	_ interface {
		IndexState(ctx context.Context, repoID string) domain.StateView
		Resync(ctx context.Context, workspaceID, repoID string) (jobID string, err error)
	} = (*app.Service)(nil)
)
