package app

import (
	"context"
	"fmt"

	"dev-digest-go/backend/internal/evals/domain"
)

// RunAll implements Runs (spec 7.5.3): every agent of the workspace, disabled ones included, in
// the order of the agents list. An agent without cases or with a running run is a skip; any other
// failure (an agent deleted meanwhile is a 404) aborts the call, and the runs already launched
// stay launched.
func (s *RunService) RunAll(ctx context.Context, workspaceID string) (domain.RunAllResult, error) {
	ids, err := s.snapshots.AgentIDs(ctx, workspaceID)
	if err != nil {
		return domain.RunAllResult{}, fmt.Errorf("list agents: %w", err)
	}
	res := domain.RunAllResult{Started: []domain.Run{}, Skipped: []domain.Skip{}}
	for _, id := range ids {
		out, err := s.tryStartAgent(ctx, workspaceID, id)
		if err != nil {
			return domain.RunAllResult{}, err
		}
		if out.skip != "" {
			res.Skipped = append(res.Skipped, domain.Skip{AgentID: id, Reason: out.skip})
			continue
		}
		res.Started = append(res.Started, out.run)
	}
	return res, nil
}
