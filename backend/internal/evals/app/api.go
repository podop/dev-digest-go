package app

import (
	"context"

	"dev-digest-go/backend/internal/evals/domain"
)

// Cases is the eval-case use cases as transport sees them; the case service implements it. Every
// method takes the workspace of the request. Failures are apperr errors that httpx renders; ids
// are UUIDs by the time they get here. Content failures carry the codes and messages of spec
// section 7.2.
type Cases interface {
	// FromFinding creates the agent-owned case of a decided finding (spec 7.4.1): 404 "Finding not
	// found" -> 409 eval_case_exists (details.case_id) -> 422 finding_undecided, finding_no_agent,
	// patch_unavailable, patch_too_large -> 422 eval_case_limit -> insert (a lost race is 409).
	FromFinding(ctx context.Context, workspaceID, findingID string) (domain.CaseView, error)
	// List returns the owner's cases, newest first; 404 "Agent not found" / "Skill not found".
	List(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) ([]domain.CaseView, error)
	// Create makes a manual case (spec 7.4.2): the owner (404), diff normalisation, the content
	// check in its documented order (422 patch_too_large, diff_invalid, expectation_file_not_in_diff,
	// expectation_lines_not_in_diff), the owner limit (422 eval_case_limit), insert.
	Create(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string, in domain.CreateInput) (domain.CaseView, error)
	// Update merges the edit into the case under a row lock (spec 7.4.3): 404 "Eval case not
	// found"; merged end_line < start_line is 400 validation_error "end_line must be >= start_line";
	// the content codes of Create when the edit carries content; {} changes nothing.
	Update(ctx context.Context, workspaceID, caseID string, in domain.UpdateInput) (domain.CaseView, error)
	// Delete deletes the case; 404 "Eval case not found".
	Delete(ctx context.Context, workspaceID, caseID string) error
}

// Runs is the eval-run use cases as transport sees them; the run service implements it. Failures
// are apperr errors, as for Cases.
type Runs interface {
	// StartAgent snapshots the agent and its case ids, persists the run as running and launches it
	// (spec 7.5.1): 404 "Agent not found" -> 422 no_eval_cases -> 409 eval_run_active.
	StartAgent(ctx context.Context, workspaceID, agentID string) (domain.Run, error)
	// StartSkill runs the skill's cases on the host agent (spec 7.5.2): 404 "Skill not found" ->
	// 422 host_agent_not_found -> 422 no_eval_cases -> 409 eval_run_active.
	StartSkill(ctx context.Context, workspaceID, skillID, hostAgentID string) (domain.Run, error)
	// RunAll starts every agent of the workspace in list order (spec 7.5.3); no_eval_cases and
	// eval_run_active become skips; any other failure aborts with that error.
	RunAll(ctx context.Context, workspaceID string) (domain.RunAllResult, error)
	// ListAgentRuns returns the agent's runs (spec 7.5.8); limit is 1-100 by now; period
	// domain.PeriodNone and domain.PeriodAll are unbounded; 404 "Agent not found".
	ListAgentRuns(ctx context.Context, workspaceID, agentID string, limit int, period domain.Period) ([]domain.Run, error)
	// ListSkillRuns returns the skill's runs; 404 "Skill not found".
	ListSkillRuns(ctx context.Context, workspaceID, skillID string, limit int) ([]domain.Run, error)
	// Detail returns a run with its results; 404 "Eval run not found".
	Detail(ctx context.Context, workspaceID, runID string) (domain.RunDetail, error)
	// Compare compares two finished runs (spec 7.5.9): 404 "Eval run not found" (also for a
	// non-UUID id) -> 422 runs_not_comparable with the message of the first broken rule.
	Compare(ctx context.Context, workspaceID, a, b string) (domain.Compare, error)
	// Overview returns the workspace-wide dashboard (spec 7.5.10).
	Overview(ctx context.Context, workspaceID string) (domain.Overview, error)
	// AgentDashboard returns one agent's dashboard; 404 "Agent not found".
	AgentDashboard(ctx context.Context, workspaceID, agentID string, period domain.Period) (domain.AgentDashboard, error)
	// FailInterrupted sets every run left running by a previous process to failed "interrupted" and
	// returns how many; the composition root treats an error as non-fatal.
	FailInterrupted(ctx context.Context) (int, error)
}
