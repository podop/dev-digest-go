package app

import (
	"context"

	"dev-digest-go/backend/internal/reviews/domain"
)

// StartRequest is a validated POST /pulls/:id/review body. Zero values mean absent, except
// AgentIDs: nil is absent, a non-nil empty slice is present and empty. Only truthiness of AgentID
// and All counts, as in the Node API.
type StartRequest struct {
	AgentID  string
	All      bool
	AgentIDs []string
}

// RunTarget is one run a review request created.
type RunTarget struct {
	RunID, AgentID, AgentName string
}

// StartResult is the answer to a review request. The wire response adds "reviews": [].
type StartResult struct {
	PRID string
	// MultiAgentRunID is set when the request named 2 or more distinct agentIds.
	MultiAgentRunID *string
	// Runs are in target order; empty for all:true with no enabled agent.
	Runs []RunTarget
}

// LearnResult is the answer to Learn: the finding with its memory id.
type LearnResult struct {
	Finding  domain.FindingRecord
	MemoryID string
}

// API is the reviews use cases as transport sees them; *Service implements it. Every method
// takes the workspace of the request. Failures are apperr errors (not found, invalid input,
// internal) that httpx renders; ids are UUIDs by the time they get here.
type API interface {
	// Start validates and resolves the targets, creates the rows in one transaction and starts the
	// batch in the background. Order of checks: targets (400 invalid_run_request, then 404 "Agent
	// not found") -> the pull request (404 "Pull request not found") -> its repo (404 "Repo not
	// found") -> writes. all:true with no enabled agent returns no runs and starts nothing.
	Start(ctx context.Context, workspaceID, prID string, req StartRequest) (StartResult, error)
	// ActiveRuns returns the pull request's running runs; an unknown pull request yields none.
	ActiveRuns(ctx context.Context, workspaceID, prID string) ([]domain.ActiveRun, error)
	// ListRuns returns the pull request's runs, newest first; an unknown pull request yields none.
	ListRuns(ctx context.Context, workspaceID, prID string) ([]domain.RunSummary, error)
	// DeleteRun deletes a run; false (not an error) when it does not exist.
	DeleteRun(ctx context.Context, workspaceID, runID string) (bool, error)
	// CancelRun publishes "Cancellation requested — stopping…", raises the cancel flag, sets a
	// running row to cancelled ("Cancelled by user") and completes the bus; 404 "Run not found".
	CancelRun(ctx context.Context, workspaceID, runID string) error
	// Events opens a run's event stream; 404 "Run not found" before any stream. A run the bus
	// holds, or one that is still running in the database, streams live; a finished run the bus
	// no longer holds yields one synthetic event and then ends. The caller must Close it.
	Events(ctx context.Context, workspaceID, runID string) (Subscription, error)
	// Trace returns a run's stored trace; 404 "Run trace not found"; a stored document that fails
	// validation is logged without its content and answered as 500 internal_error.
	Trace(ctx context.Context, workspaceID, runID string) (domain.Trace, error)
	// Reviews returns the pull request's reviews newest first; 404 "Pull request not found".
	Reviews(ctx context.Context, workspaceID, prID string) ([]domain.ReviewRecord, error)
	// DeleteReview deletes a review with its findings; 404 "Review not found".
	DeleteReview(ctx context.Context, workspaceID, reviewID string) error
	// AcceptFinding and DismissFinding set the triage timestamps; 404 "Finding not found".
	AcceptFinding(ctx context.Context, workspaceID, findingID string) (domain.FindingRecord, error)
	DismissFinding(ctx context.Context, workspaceID, findingID string) (domain.FindingRecord, error)
	// MultiAgentRun returns the comparison read model; 404 code multi_agent_run_not_found
	// "Multi-agent run not found".
	MultiAgentRun(ctx context.Context, workspaceID, id string) (domain.MultiAgentRun, error)
	// ListMultiAgentRuns returns the repo's newest runs; limit nil = domain.MultiAgentListLimit,
	// outside 1-20 is 400 invalid_limit "limit must be between 1 and 20"; 404 "Repo not found".
	ListMultiAgentRuns(ctx context.Context, workspaceID, repoID string, limit *int) ([]domain.MultiAgentSummary, error)
	// Estimates returns the per-agent averages.
	Estimates(ctx context.Context, workspaceID string) ([]domain.AgentRunEstimate, error)
	// Learn stores the finding as one memory entry (once, also under concurrent calls); 404
	// "Finding not found".
	Learn(ctx context.Context, workspaceID, findingID string) (LearnResult, error)
}

// AutoStarter is what the automatic-review trigger needs (bound in cmd/ through its own consumer-side
// port); *Service implements it. It is not part of API: no route exposes it.
type AutoStarter interface {
	// StartAuto is Start for the automatic-review trigger: it reviews with the named agents
	// (1-domain.MaxAgentIDs, same checks and order as Start, same spend-cap check) and records every
	// run with source "auto". A reached cap is Budget's 402 *apperr.Error, returned unchanged.
	StartAuto(ctx context.Context, workspaceID, prID string, agentIDs []string) (StartResult, error)
}

// Lifecycle is what the composition root calls on the use cases outside any request.
type Lifecycle interface {
	// FailInterrupted sets every run left running by a previous process to failed and returns how
	// many; the caller treats an error as non-fatal.
	FailInterrupted(ctx context.Context) (int, error)
}
