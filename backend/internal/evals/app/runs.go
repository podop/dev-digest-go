package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Codes of the run use cases (spec section 7.2).
const (
	codeNoEvalCases       = "no_eval_cases"
	codeRunActive         = "eval_run_active"
	codeHostAgentNotFound = "host_agent_not_found"

	msgRunNotFound = "Eval run not found"
)

// RunDeps are the ports of the run service.
type RunDeps struct {
	Cases     CaseStore
	Runs      RunStore
	Snapshots Snapshots
	Launcher  Launcher
	// Now anchors the period windows; nil means time.Now.
	Now Clock
	// Log is nil to discard.
	Log *slog.Logger
}

// RunService implements Runs.
type RunService struct {
	cases     CaseStore
	runs      RunStore
	snapshots Snapshots
	launcher  Launcher
	now       Clock
	log       *slog.Logger
}

var _ Runs = (*RunService)(nil)

// NewRunService builds the run use cases.
func NewRunService(d RunDeps) *RunService {
	s := &RunService{cases: d.Cases, runs: d.Runs, snapshots: d.Snapshots, launcher: d.Launcher, now: d.Now, log: d.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

// startOutcome is how an agent start ended other than by a failure: a started run, or the reason
// it was skipped (run-all reports skips, a single start turns them into errors).
type startOutcome struct {
	run  domain.Run
	skip domain.SkipReason // "" when started
}

// begin persists the run as running and launches it. active is true when the owner already has a
// running run (nothing was created).
func (s *RunService) begin(ctx context.Context, nr domain.NewRun) (run domain.Run, active bool, err error) {
	run, err = s.runs.CreateRun(ctx, nr)
	if errors.Is(err, domain.ErrRunActive) {
		return domain.Run{}, true, nil
	}
	if err != nil {
		return domain.Run{}, false, fmt.Errorf("create run: %w", err)
	}
	s.launcher.Launch(ctx, run)
	return run, false, nil
}

// tryStartAgent is the agent start path of spec 7.5.1: no cases and an active run come back as a
// skip, an unknown agent is the 404.
func (s *RunService) tryStartAgent(ctx context.Context, workspaceID, agentID string) (startOutcome, error) {
	agent, err := s.snapshots.Agent(ctx, workspaceID, agentID)
	if errors.Is(err, domain.ErrNotFound) {
		return startOutcome{}, apperr.NotFound(msgAgentNotFound)
	}
	if err != nil {
		return startOutcome{}, fmt.Errorf("read agent: %w", err)
	}
	caseIDs, err := s.cases.CaseIDs(ctx, workspaceID, domain.OwnerAgent, agentID)
	if err != nil {
		return startOutcome{}, fmt.Errorf("read case ids: %w", err)
	}
	if len(caseIDs) == 0 {
		return startOutcome{skip: domain.SkipNoCases}, nil
	}
	run, active, err := s.begin(ctx, domain.NewRun{
		WorkspaceID:  workspaceID,
		OwnerKind:    domain.OwnerAgent,
		AgentID:      agentID,
		AgentVersion: agent.Version,
		AgentName:    agent.Name,
		Config:       agent.Config,
		CaseIDs:      caseIDs,
	})
	if err != nil {
		return startOutcome{}, err
	}
	if active {
		return startOutcome{skip: domain.SkipRunActive}, nil
	}
	return startOutcome{run: run}, nil
}

// StartAgent implements Runs.
func (s *RunService) StartAgent(ctx context.Context, workspaceID, agentID string) (domain.Run, error) {
	out, err := s.tryStartAgent(ctx, workspaceID, agentID)
	if err != nil {
		return domain.Run{}, err
	}
	switch out.skip {
	case domain.SkipNoCases:
		return domain.Run{}, apperr.Validation("This agent has no eval cases to run").WithCode(codeNoEvalCases)
	case domain.SkipRunActive:
		return domain.Run{}, apperr.Conflict("An eval run is already in progress for this agent").WithCode(codeRunActive)
	}
	return out.run, nil
}

// StartSkill implements Runs: the skill's cases run on the host agent with exactly this skill
// attached (the host's own linked skills are dropped).
func (s *RunService) StartSkill(ctx context.Context, workspaceID, skillID, hostAgentID string) (domain.Run, error) {
	skill, err := s.snapshots.Skill(ctx, workspaceID, skillID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Run{}, apperr.NotFound(msgSkillNotFound)
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("read skill: %w", err)
	}
	host, err := s.snapshots.Agent(ctx, workspaceID, hostAgentID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Run{}, apperr.Validation("The host agent was not found in this workspace").WithCode(codeHostAgentNotFound)
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("read host agent: %w", err)
	}
	caseIDs, err := s.cases.CaseIDs(ctx, workspaceID, domain.OwnerSkill, skillID)
	if err != nil {
		return domain.Run{}, fmt.Errorf("read case ids: %w", err)
	}
	if len(caseIDs) == 0 {
		return domain.Run{}, apperr.Validation("This skill has no eval cases to run").WithCode(codeNoEvalCases)
	}
	cfg := host.Config
	cfg.Skills = []domain.SkillSnapshot{skill}
	version := skill.Version
	run, active, err := s.begin(ctx, domain.NewRun{
		WorkspaceID:  workspaceID,
		OwnerKind:    domain.OwnerSkill,
		SkillID:      &skillID,
		SkillVersion: &version,
		AgentID:      hostAgentID,
		AgentVersion: host.Version,
		AgentName:    host.Name,
		Config:       cfg,
		CaseIDs:      caseIDs,
	})
	if err != nil {
		return domain.Run{}, err
	}
	if active {
		return domain.Run{}, apperr.Conflict("An eval run is already in progress for this skill").WithCode(codeRunActive)
	}
	return run, nil
}

// FailInterrupted implements Runs.
func (s *RunService) FailInterrupted(ctx context.Context) (int, error) {
	n, err := s.runs.FailInterrupted(ctx)
	if err != nil {
		return 0, fmt.Errorf("fail interrupted runs: %w", err)
	}
	s.log.Info("eval runs reaped", "count", n)
	return n, nil
}
