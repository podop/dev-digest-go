package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/reviews/domain"
)

// MultiAgentRun returns the comparison read model of a multi-agent run.
func (s *Service) MultiAgentRun(ctx context.Context, workspaceID, id string) (domain.MultiAgentRun, error) {
	header, err := s.store.FindMultiAgentRun(ctx, workspaceID, id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.MultiAgentRun{}, apperr.NotFound("Multi-agent run not found").WithCode("multi_agent_run_not_found")
	}
	if err != nil {
		return domain.MultiAgentRun{}, fmt.Errorf("find multi-agent run %s: %w", id, err)
	}
	columns, err := s.store.MultiAgentColumns(ctx, workspaceID, id)
	if err != nil {
		return domain.MultiAgentRun{}, fmt.Errorf("list columns of multi-agent run %s: %w", id, err)
	}
	return domain.BuildMultiAgentRun(header, columns), nil
}

// ListMultiAgentRuns returns the repository's newest multi-agent runs.
func (s *Service) ListMultiAgentRuns(ctx context.Context, workspaceID, repoID string, limit *int) ([]domain.MultiAgentSummary, error) {
	n, err := domain.ListLimit(limit)
	if err != nil {
		return nil, apperr.InvalidInput(err.Error()).WithCode("invalid_limit")
	}
	ok, err := s.store.RepoInWorkspace(ctx, workspaceID, repoID)
	if err != nil {
		return nil, fmt.Errorf("check repo %s: %w", repoID, err)
	}
	if !ok {
		return nil, apperr.NotFound("Repo not found")
	}
	runs, err := s.store.ListMultiAgentRuns(ctx, workspaceID, repoID, n)
	if err != nil {
		return nil, fmt.Errorf("list multi-agent runs of repo %s: %w", repoID, err)
	}
	if runs == nil {
		runs = []domain.MultiAgentSummary{}
	}
	return runs, nil
}

// Estimates returns the per-agent averages.
func (s *Service) Estimates(ctx context.Context, workspaceID string) ([]domain.AgentRunEstimate, error) {
	est, err := s.store.AgentEstimates(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("agent estimates: %w", err)
	}
	if est == nil {
		est = []domain.AgentRunEstimate{}
	}
	return est, nil
}

// Learn stores a finding as one learning memory entry. The lookup and the insert share one
// transaction under an advisory lock on the finding, so repeated or concurrent calls end with
// the same entry.
func (s *Service) Learn(ctx context.Context, workspaceID, findingID string) (LearnResult, error) {
	target, err := s.store.FindLearnTarget(ctx, workspaceID, findingID)
	if err != nil {
		return LearnResult{}, notFound(err, "Finding not found", "find finding "+findingID)
	}
	var memoryID string
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.LockLearn(ctx, findingID); err != nil {
			return fmt.Errorf("lock learn of finding %s: %w", findingID, err)
		}
		id, ok, err := s.store.LearnedMemoryID(ctx, workspaceID, findingID)
		if err != nil {
			return fmt.Errorf("look up learned memory of finding %s: %w", findingID, err)
		}
		if !ok {
			id, err = s.store.InsertLearnedMemory(ctx, domain.NewLearnMemory(workspaceID, target))
			if err != nil {
				return fmt.Errorf("insert learned memory of finding %s: %w", findingID, err)
			}
		}
		memoryID = id
		return nil
	})
	if err != nil {
		return LearnResult{}, err
	}
	s.log.InfoContext(ctx, "reviews: finding learned", "findingID", findingID, "memoryID", memoryID)
	f := target.Finding
	f.MemoryID = &memoryID
	return LearnResult{Finding: f, MemoryID: memoryID}, nil
}
