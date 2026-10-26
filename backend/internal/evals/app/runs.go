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
	// codePluginDisabled is the 409 of a start that needs an agent or skill of a disabled plugin
	// (plugins/app.CodePluginDisabled; duplicated like the budget operations below).
	codePluginDisabled = "plugin_disabled"

	msgRunNotFound = "Eval run not found"
)

// Budget operations of the start use cases. The strings are the spend context's
// (spend/app.OpEval*); they are duplicated here because contexts reach each other only through
// consumer-side ports wired in cmd/ (backend/AGENTS.md), so evals/app does not import spend/app.
const (
	opEvalAgentStart = "eval_agent_start"
	opEvalSkillStart = "eval_skill_start"
	opEvalRunAll     = "eval_run_all"
)

// Budget is the monthly spend cap check (spend/app.API.Admit, bound in cmd/). Admit returns nil
// when the operation may start, an *apperr.Error 402 when the cap is reached, and any other error
// when the spend could not be read; the start use cases return it unchanged and create nothing.
type Budget interface {
	Admit(ctx context.Context, workspaceID, op string) error
}

// RunDeps are the ports of the run service.
type RunDeps struct {
	Cases     CaseStore
	Runs      RunStore
	Snapshots Snapshots
	Launcher  Launcher
	// Budget gates the starts on the monthly spend cap; nil = off.
	Budget Budget
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
	budget    Budget
	now       Clock
	log       *slog.Logger
}

var _ Runs = (*RunService)(nil)

// NewRunService builds the run use cases.
func NewRunService(d RunDeps) *RunService {
	s := &RunService{cases: d.Cases, runs: d.Runs, snapshots: d.Snapshots, launcher: d.Launcher, budget: d.Budget, now: d.Now, log: d.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

// refuseDisabledPlugin is the 409 of a start that needs an item of a disabled plugin; kind is
// "Agent" or "Skill". Nil when the item has no plugin or its plugin is enabled.
func refuseDisabledPlugin(kind, name string, plugin *domain.PluginRef) error {
	if plugin == nil || plugin.Enabled {
		return nil
	}
	return apperr.Conflict(fmt.Sprintf("%s %q belongs to the disabled plugin %q", kind, name, plugin.Name)).
		WithCode(codePluginDisabled)
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
	// The owner was deleted between the snapshot and the insert (foreign key): the 404 of a start
	// that had arrived a moment later.
	if errors.Is(err, domain.ErrNotFound) {
		if nr.OwnerKind == domain.OwnerSkill {
			return domain.Run{}, false, apperr.NotFound(msgSkillNotFound)
		}
		return domain.Run{}, false, apperr.NotFound(msgAgentNotFound)
	}
	if err != nil {
		return domain.Run{}, false, fmt.Errorf("create run: %w", err)
	}
	s.launcher.Launch(ctx, run)
	return run, false, nil
}

// admit asks the budget, when there is one, whether op may start. Its error is returned as it
// is: an *apperr.Error 402 passes through, any other one becomes a 500 at the edge.
func (s *RunService) admit(ctx context.Context, workspaceID, op string) error {
	if s.budget == nil {
		return nil
	}
	return s.budget.Admit(ctx, workspaceID, op)
}

// planAgentStart is the validation half of the agent start path of spec 7.5.1: an unknown agent is
// the 404 and no cases come back as a skip. Nothing is written.
func (s *RunService) planAgentStart(ctx context.Context, workspaceID, agentID string) (domain.NewRun, domain.SkipReason, error) {
	agent, err := s.snapshots.Agent(ctx, workspaceID, agentID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.NewRun{}, "", apperr.NotFound(msgAgentNotFound)
	}
	if err != nil {
		return domain.NewRun{}, "", fmt.Errorf("read agent: %w", err)
	}
	if err := refuseDisabledPlugin("Agent", agent.Name, agent.Plugin); err != nil {
		return domain.NewRun{}, "", err
	}
	caseIDs, err := s.cases.CaseIDs(ctx, workspaceID, domain.OwnerAgent, agentID)
	if err != nil {
		return domain.NewRun{}, "", fmt.Errorf("read case ids: %w", err)
	}
	if len(caseIDs) == 0 {
		return domain.NewRun{}, domain.SkipNoCases, nil
	}
	return domain.NewRun{
		WorkspaceID:  workspaceID,
		OwnerKind:    domain.OwnerAgent,
		AgentID:      agentID,
		AgentVersion: agent.Version,
		AgentName:    agent.Name,
		Config:       agent.Config,
		CaseIDs:      caseIDs,
	}, "", nil
}

// beginAgent persists and launches a planned agent run; an owner with a running run is a skip.
func (s *RunService) beginAgent(ctx context.Context, nr domain.NewRun) (startOutcome, error) {
	run, active, err := s.begin(ctx, nr)
	if err != nil {
		return startOutcome{}, err
	}
	if active {
		return startOutcome{skip: domain.SkipRunActive}, nil
	}
	return startOutcome{run: run}, nil
}

// StartAgent implements Runs. The cap check comes after the 404 and the no-cases 422 and before
// the run row is written.
func (s *RunService) StartAgent(ctx context.Context, workspaceID, agentID string) (domain.Run, error) {
	nr, skip, err := s.planAgentStart(ctx, workspaceID, agentID)
	if err != nil {
		return domain.Run{}, err
	}
	if skip == domain.SkipNoCases {
		return domain.Run{}, apperr.Validation("This agent has no eval cases to run").WithCode(codeNoEvalCases)
	}
	if err := s.admit(ctx, workspaceID, opEvalAgentStart); err != nil {
		return domain.Run{}, err
	}
	out, err := s.beginAgent(ctx, nr)
	if err != nil {
		return domain.Run{}, err
	}
	if out.skip == domain.SkipRunActive {
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
	if err := refuseDisabledPlugin("Skill", skill.Name, skill.Plugin); err != nil {
		return domain.Run{}, err
	}
	host, err := s.snapshots.Agent(ctx, workspaceID, hostAgentID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Run{}, apperr.Validation("The host agent was not found in this workspace").WithCode(codeHostAgentNotFound)
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("read host agent: %w", err)
	}
	if err := refuseDisabledPlugin("Agent", host.Name, host.Plugin); err != nil {
		return domain.Run{}, err
	}
	caseIDs, err := s.cases.CaseIDs(ctx, workspaceID, domain.OwnerSkill, skillID)
	if err != nil {
		return domain.Run{}, fmt.Errorf("read case ids: %w", err)
	}
	if len(caseIDs) == 0 {
		return domain.Run{}, apperr.Validation("This skill has no eval cases to run").WithCode(codeNoEvalCases)
	}
	if err := s.admit(ctx, workspaceID, opEvalSkillStart); err != nil {
		return domain.Run{}, err
	}
	cfg := host.Config
	skill.Plugin = nil // the gate's reading, not part of the frozen configuration
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
