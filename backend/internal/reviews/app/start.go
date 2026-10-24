package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/reviews/domain"
)

// Start validates the request, resolves its target agents, creates the run rows in one
// transaction and hands the batch to the executor, which runs it in the background.
func (s *Service) Start(ctx context.Context, workspaceID, prID string, req StartRequest) (StartResult, error) {
	plan, err := domain.PlanTargets(req.AgentID, req.All, req.AgentIDs)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidRunRequest) {
			return StartResult{}, apperr.InvalidInput(err.Error()).WithCode("invalid_run_request")
		}
		return StartResult{}, fmt.Errorf("plan review targets: %w", err)
	}
	agents, err := s.resolveTargets(ctx, workspaceID, plan)
	if err != nil {
		return StartResult{}, err
	}
	pull, err := s.store.FindPull(ctx, workspaceID, prID)
	if err != nil {
		return StartResult{}, notFound(err, "Pull request not found", "find pull request "+prID)
	}
	repo, err := s.store.FindRepo(ctx, pull.RepoID)
	if err != nil {
		return StartResult{}, notFound(err, "Repo not found", "find repo "+pull.RepoID)
	}
	// The cap is checked once, after every 422/404 and before any write or executor start.
	if s.budget != nil {
		if err := s.budget.Admit(ctx, workspaceID, opReviewStart); err != nil {
			return StartResult{}, err
		}
	}
	res := StartResult{PRID: prID, Runs: []RunTarget{}}
	if len(agents) == 0 {
		return res, nil
	}

	jobs := make([]domain.Job, 0, len(agents))
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		var parent *string
		if plan.CreatesMultiAgentRun(len(agents)) {
			id, err := s.store.CreateMultiAgentRun(ctx, workspaceID, prID)
			if err != nil {
				return fmt.Errorf("create multi-agent run: %w", err)
			}
			parent = &id
		}
		res.MultiAgentRunID = parent
		for _, a := range agents {
			runID, err := s.store.CreateRun(ctx, domain.NewRun{
				WorkspaceID:     workspaceID,
				AgentID:         a.ID,
				PRID:            prID,
				Provider:        a.Provider,
				Model:           a.Model,
				MultiAgentRunID: parent,
			})
			if err != nil {
				return fmt.Errorf("create run of agent %s: %w", a.ID, err)
			}
			jobs = append(jobs, domain.Job{Agent: a, RunID: runID})
		}
		return nil
	})
	if err != nil {
		return StartResult{}, err
	}
	for _, j := range jobs {
		res.Runs = append(res.Runs, RunTarget{RunID: j.RunID, AgentID: j.Agent.ID, AgentName: j.Agent.Name})
	}
	if res.MultiAgentRunID != nil {
		s.log.InfoContext(ctx, "reviews: multi-agent run created",
			"multiAgentRunID", *res.MultiAgentRunID, "prID", prID, "agentCount", len(jobs))
	}

	s.executor.Start(ctx, domain.Batch{WorkspaceID: workspaceID, Pull: pull, Repo: repo, Jobs: jobs})
	return res, nil
}

// resolveTargets returns the agents of the plan in run order: enabled agents for all (as the
// store orders them), the named ones in first-occurrence order otherwise (disabled allowed).
func (s *Service) resolveTargets(ctx context.Context, workspaceID string, plan domain.TargetPlan) ([]domain.ReviewAgent, error) {
	switch plan.Mode {
	case domain.TargetAll:
		agents, err := s.store.EnabledAgents(ctx, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("list enabled agents: %w", err)
		}
		return agents, nil
	case domain.TargetAgent:
		a, err := s.findAgent(ctx, workspaceID, plan.AgentID)
		if err != nil {
			return nil, err
		}
		return []domain.ReviewAgent{a}, nil
	case domain.TargetAgentIDs:
		agents := make([]domain.ReviewAgent, 0, len(plan.AgentIDs))
		for _, id := range plan.AgentIDs {
			a, err := s.findAgent(ctx, workspaceID, id)
			if err != nil {
				return nil, err
			}
			agents = append(agents, a)
		}
		return agents, nil
	}
	return nil, apperr.Internal("Internal error").WithCause(fmt.Errorf("unknown target mode %d", plan.Mode))
}

func (s *Service) findAgent(ctx context.Context, workspaceID, id string) (domain.ReviewAgent, error) {
	a, err := s.store.FindAgent(ctx, workspaceID, id)
	if err != nil {
		return domain.ReviewAgent{}, notFound(err, "Agent not found", "find agent "+id)
	}
	return a, nil
}
