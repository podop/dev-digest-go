package app

import (
	"context"

	"dev-digest-go/backend/internal/performance/domain"
)

// API is the performance use cases as transport sees them; *Service implements it. The period
// query is validated by transport before any use case runs (422 validation_error). Failures are
// apperr errors that httpx renders; ids are UUIDs by the time they get here.
type API interface {
	// Dashboard answers GET /agents/performance: one row per agent of the workspace (zero runs
	// included) ordered by name, plus the deleted-agents row when the window has such runs; the
	// summary and trends fold the same facts. A database error is 500 internal_error.
	Dashboard(ctx context.Context, workspaceID string, q domain.PeriodQuery) (domain.AgentPerf, error)
	// Stats answers GET /agents/:id/stats; 404 "Agent not found" when the agent is not in the
	// workspace.
	Stats(ctx context.Context, workspaceID, agentID string, q domain.PeriodQuery) (domain.AgentStats, error)
}
