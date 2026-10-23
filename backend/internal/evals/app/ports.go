// Package app holds the use cases of the evals context: eval cases (create from a finding or by
// hand, list, edit, delete) and eval runs (start, run-all, lists, detail, compare, dashboards,
// the startup reap). The background execution of a run lives in the executor subpackage.
//
// This file holds the ports the use cases depend on; api.go holds the use cases' own interfaces,
// which transport depends on.
package app

import (
	"context"
	"time"

	"dev-digest-go/backend/internal/evals/domain"
)

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// CaseStore is the persistence of eval cases. Every method that takes a workspace id sees that
// workspace's rows only; a missing row is domain.ErrNotFound unless a method says otherwise.
// The implementation reads the agents and skills tables with its own row types.
type CaseStore interface {
	// OwnerExists reports whether the agent or skill is in the workspace.
	OwnerExists(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) (bool, error)
	// CountCases returns the owner's number of cases.
	CountCases(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) (int, error)
	// CaseIDs returns the owner's case ids ordered by created_at, id (snapshot order).
	CaseIDs(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) ([]string, error)
	// AgentCaseCounts returns the number of agent-owned cases per agent id (overview).
	AgentCaseCounts(ctx context.Context, workspaceID string) (map[string]int, error)
	// ListCases returns the owner's cases with their last result, created_at desc then id desc.
	ListCases(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) ([]domain.CaseView, error)
	// GetCase returns one case of the workspace (the executor and the views read it).
	GetCase(ctx context.Context, workspaceID, caseID string) (domain.Case, error)
	// GetCaseView returns one case with its last result.
	GetCaseView(ctx context.Context, workspaceID, caseID string) (domain.CaseView, error)
	// CaseBySourceFinding returns the id of the case made from the finding; ok false when none.
	CaseBySourceFinding(ctx context.Context, workspaceID, findingID string) (caseID string, ok bool, err error)
	// CreateCase inserts a case. A unique violation on source_finding_id is *domain.CaseExistsError
	// carrying the winning case's id.
	CreateCase(ctx context.Context, c domain.NewCase) (domain.Case, error)
	// UpdateCase reads the case under a row lock (FOR UPDATE), calls fn with it and stores the
	// returned case in the same transaction. An error from fn rolls back and is returned as is;
	// fn returning the case unchanged writes nothing.
	UpdateCase(ctx context.Context, workspaceID, caseID string, fn func(domain.Case) (domain.Case, error)) (domain.Case, error)
	// DeleteCase deletes the case; its results are deleted with it (cascade).
	DeleteCase(ctx context.Context, workspaceID, caseID string) error
}

// RunStore is the persistence of eval suite runs and case results.
type RunStore interface {
	// CreateRun inserts the run as running with cases_finished 0. A violation of the
	// one-running-run-per-owner index is domain.ErrRunActive.
	CreateRun(ctx context.Context, r domain.NewRun) (domain.Run, error)
	// GetRun returns one run of the workspace.
	GetRun(ctx context.Context, workspaceID, runID string) (domain.Run, error)
	// RunResults returns the run's results in execution order (ran_at, id); a NULL status is
	// domain.CaseError and a NULL duration 0.
	RunResults(ctx context.Context, workspaceID, runID string) ([]domain.CaseResult, error)
	// ListAgentRuns returns runs with owner_kind agent of the agent (never the skill runs it
	// hosts), started_at desc then id desc, at most limit; since non-nil keeps started_at >= since.
	ListAgentRuns(ctx context.Context, workspaceID, agentID string, limit int, since *time.Time) ([]domain.Run, error)
	// ListSkillRuns returns runs with owner_kind skill of the skill, same order, at most limit.
	ListSkillRuns(ctx context.Context, workspaceID, skillID string, limit int) ([]domain.Run, error)
	// FinishedAgentRuns returns the agent's finished (completed or failed) agent-owned runs,
	// newest started_at first, at most limit; since non-nil keeps started_at >= since.
	FinishedAgentRuns(ctx context.Context, workspaceID, agentID string, limit int, since *time.Time) ([]domain.Run, error)
	// DashboardAgents returns the agents of the workspace that own a case or an agent-owned run,
	// ordered by name then id (database default collation).
	DashboardAgents(ctx context.Context, workspaceID string) ([]domain.AgentRef, error)
	// RecentAgentRuns returns the newest agent-owned runs of the workspace, any status, with the
	// agents' current names.
	RecentAgentRuns(ctx context.Context, workspaceID string, limit int) ([]domain.RecentRun, error)
	// RecordResult inserts the result and adds 1 to cases_finished and, by status, to
	// cases_passed (passed) or cases_errored (error) in one transaction. A foreign-key violation
	// records nothing and is domain.ErrRowGone.
	RecordResult(ctx context.Context, workspaceID string, r domain.NewResult) error
	// DropRunCase subtracts 1 from the run's cases_total, never below 0 (a case deleted meanwhile).
	DropRunCase(ctx context.Context, workspaceID, runID string) error
	// FinishRun writes the final state WHERE status = 'running' and reports whether a row changed.
	FinishRun(ctx context.Context, workspaceID, runID string, f domain.RunFinish) (bool, error)
	// FailInterrupted sets every running run to failed with error "interrupted" and finished_at
	// now, in every workspace, and returns how many.
	FailInterrupted(ctx context.Context) (int, error)
}

// Snapshots reads the agents and skills a run freezes (the adapter reads their tables).
type Snapshots interface {
	// Agent returns the agent with its enabled linked skills in link order; domain.ErrNotFound
	// when it is not in the workspace.
	Agent(ctx context.Context, workspaceID, agentID string) (domain.AgentSnapshot, error)
	// Skill returns the skill, enabled or not; domain.ErrNotFound when not in the workspace.
	Skill(ctx context.Context, workspaceID, skillID string) (domain.SkillSnapshot, error)
	// AgentIDs returns every agent id of the workspace, disabled included, in the order of
	// GET /agents (name COLLATE "C", then id).
	AgentIDs(ctx context.Context, workspaceID string) ([]string, error)
}

// Findings reads the finding a case is made from.
type Findings interface {
	// Source returns the finding of the workspace with its review, PR, repo and stored patch;
	// domain.ErrNotFound when it is not in the workspace.
	Source(ctx context.Context, workspaceID, findingID string) (domain.FindingSource, error)
}

// Launcher starts the background execution of a run that was just created and returns at once.
// The implementation (executor.Executor) detaches from ctx's cancellation, owns the goroutines
// and records failures on the run, never returns them. After its Shutdown a launch is refused
// and the run stays running until the next start reaps it.
type Launcher interface {
	Launch(ctx context.Context, run domain.Run)
}
