// Package app holds the use cases of automatic reviews: the evaluation the background poller
// triggers after a cycle, and the status of the feature. It reaches settings, pulls, reviews,
// spend and secrets only through the consumer-side ports of ports.go, bound in cmd/server.
package app

import (
	"context"

	"dev-digest-go/backend/internal/autoreview/domain"
)

// API is what the poller and transport see; *Service implements it.
type API interface {
	// AfterCycle evaluates the automatic reviews of a workspace after a background-poll cycle that
	// polled successfully (FR3). It satisfies pulls' CycleObserver: it returns nothing, logs its own
	// failures and recovers its own panic, and it returns within 5 s of ctx being cancelled (NFR5).
	// A setting that is not armed, or a disabled poll, does nothing.
	AfterCycle(ctx context.Context, workspaceID string)
	// Status answers GET /settings/automatic-reviews-status (FR12). A read error is returned
	// wrapped (500 internal_error at the edge).
	Status(ctx context.Context, workspaceID string) (domain.Status, error)
}
