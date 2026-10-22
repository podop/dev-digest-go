// Package app holds the use cases of the reviews context: starting, following and cancelling
// review runs, reading runs, reviews and traces, acting on findings, the multi-agent read model
// and Learn. The background execution of a batch lives in the executor subpackage.
//
// This file holds the ports the use cases and the executor depend on; api.go holds the use
// cases' own interface, which transport depends on.
package app

import (
	"context"
	"encoding/json"
	"time"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// Tx runs fn in one database transaction. A store joins the transaction only through the ctx fn
// receives (postgres.Conn); an error from fn rolls back. The persist step of a run, the creation
// of a batch's rows and Learn each run in one.
type Tx interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Executor runs a batch of agent runs in the background and returns at once. The implementation
// (executor.Executor) detaches from ctx's cancellation (context.WithoutCancel) and owns the
// goroutines; failures are recorded on the runs, never returned.
type Executor interface {
	Start(ctx context.Context, b domain.Batch)
}

// Engine is the review engine's entry point (reviewer/app.Run); the executor takes it as a
// dependency so tests can script the outcome.
type Engine func(ctx context.Context, in reviewerapp.Input, llm reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error)

// Subscription is a live view of one run's events: the buffered events first, then the live
// ones, with no gap and no duplicate. C is closed when the run completes or the subscriber falls
// more than domain.SubscriberBacklog events behind. Close releases it and is safe to call twice.
type Subscription interface {
	C() <-chan domain.RunEvent
	Close()
}

// Bus is the in-memory run bus (platform/runbus behind an adapter in adapters/peers). Every
// method is safe for concurrent use and none blocks on a subscriber.
type Bus interface {
	// Publish numbers and stamps an event. After Complete it only appends to the retained
	// buffer. data is nil or a JSON document.
	Publish(runID string, kind reviewer.EventKind, msg string, data json.RawMessage) domain.RunEvent
	// Subscribe replays the buffer, then follows the live events.
	Subscribe(runID string) Subscription
	// Has reports whether the bus holds the run (its buffer has not been evicted).
	Has(runID string) bool
	// Buffer returns a copy of the run's events so far.
	Buffer(runID string) []domain.RunEvent
	// Complete ends every subscriber; the buffer and cancel flag are kept for domain.BusTTL.
	Complete(runID string)
	// Cancel raises the run's sticky cancel flag and fires its listeners.
	Cancel(runID string)
	IsCancelled(runID string) bool
	// OnCancel registers fn to run when the run is cancelled (at once when it already is) and
	// returns the function that withdraws it.
	OnCancel(runID string, fn func()) (stop func())
	// CancelAll raises the cancel flag of every live (not completed) run and returns their ids.
	CancelAll() []string
	// CompleteAll completes every run that is not completed yet.
	CompleteAll()
}

// LLMBinding says which model a run's LLM port talks to.
type LLMBinding struct {
	Provider string
	Model    string
	// RunID is the correlation id of the prompt log line.
	RunID string
	// SessionID is the OpenRouter session id "<owner>/<name>#<number>:<agent name>".
	SessionID string
	// OnUsage receives the usage of every response, schema-invalid attempts included, as it
	// arrives; the executor sums it so a failed or cancelled run still records what it spent.
	// It may be called from several goroutines.
	OnUsage func(reviewer.Usage)
}

// Providers resolves an agent's LLM provider.
type Providers interface {
	// Resolve returns the engine's LLM port bound to the run. It fails with an error whose text
	// is "<KEY_NAME> is not configured" when the provider's key is missing (not with the mock
	// override), without making a call.
	Resolve(ctx context.Context, b LLMBinding) (reviewerapp.LLM, error)
	// Redact scrubs provider keys out of a text; run notes pass through it.
	Redact(s string) string
}

// RepoIntel is the part of the repo-intel read port a review prompt uses. With the feature flag
// off or no index each operation answers empty; only real failures are errors.
type RepoIntel interface {
	// CallerSignatures returns up to limit caller rows of the changed files (bounded to 30 s).
	CallerSignatures(ctx context.Context, repoID string, changed []string, limit int) ([]domain.CallerSignature, error)
	// RepoMap returns the cached repo map for the token budget.
	RepoMap(ctx context.Context, repoID string, budget int) (domain.RepoMap, error)
	// FileRank returns the percentile of each path that has a rank.
	FileRank(ctx context.Context, repoID string, paths []string) ([]domain.PathRank, error)
}

// ProjectContextInput identifies the documents a run attaches (spec 7.12).
type ProjectContextInput struct {
	WorkspaceID string
	Repo        domain.ReviewRepo
	// Base is the PR's base branch name and HeadSHA its head commit: repo documents are read at
	// the base commit resolved from them.
	Base, HeadSHA string
	AgentID       string
	// Skills are the agent's enabled skills in link order.
	Skills []domain.ReviewSkill
}

// ProjectContextResult is the resolved documents of a run.
type ProjectContextResult struct {
	// Docs lists every attached document with its status, in prompt order. Empty = nothing is
	// attached: the run has no project_context trace, no event and no prompt section.
	Docs         []domain.ContextDoc
	TokensTotal  int
	BudgetTokens int
	// LogLine is the run's single event "project context: <included> included, <skipped>
	// skipped · ~<tokens> tokens"; empty when Docs is empty.
	LogLine string
}

// ProjectContext resolves a run's project-context documents.
type ProjectContext interface {
	// ResolveForRun reads the documents. An unexpected failure is an error; the executor then
	// publishes the "project context unavailable" warning and goes on without documents.
	ResolveForRun(ctx context.Context, in ProjectContextInput) (ProjectContextResult, error)
}

// IntentRequest is what the executor asks the intent layer for, once per review request, after
// the "Diff ready" event and before the first agent run (Phase 5 spec 7.5). The derivation is
// never forced: a stored derivation of the same key is reused.
type IntentRequest struct {
	WorkspaceID string
	// Pull is the pull request as read at request time (title, body, branch, head SHA); Repo its
	// repository (owner and name).
	Pull domain.ReviewPull
	Repo domain.ReviewRepo
	// ChangedFiles are the parsed stored diff's file paths in diff order.
	ChangedFiles []string
	// DiffExcerpt is the raw stored diff text; the layer cuts it to what it needs.
	DiffExcerpt string
}

// IntentResult is the outcome of an intent request. Exactly one of the two fields is set.
type IntentResult struct {
	// Intent is non-nil when the intent is usable (a stored derivation or a fresh one); it feeds
	// every agent run of the request.
	Intent *reviewer.Intent
	// Trace is the trace.intent value of every run of the request: status used (from the stored
	// row, cache_hit set) when Intent is non-nil, else status unavailable with its warning. It is
	// never nil in a result of IntentResolver.Resolve.
	Trace *domain.IntentTrace
}

// IntentEmit receives the intent layer's run events (kind info or result, see reviewer.EventInfo
// and reviewer.EventResult; data is nil or a JSON document, for example {"warning":"intent_unavailable"})
// while it works. The executor publishes each one to every queued run of the request. It is
// called from the derivation's goroutine, possibly after Resolve's caller stopped waiting, and
// must be safe for concurrent use.
type IntentEmit func(kind, msg string, data json.RawMessage)

// IntentResolver derives or reuses the PR intent of a review request (implemented by
// adapters/peers over the intent context's service). The executor calls it only when it is
// configured (executor.Deps.Intent non-nil = REVIEW_INTENT_ENABLED on); nil keeps Phase 4
// behaviour exactly.
type IntentResolver interface {
	// Resolve never fails: any failure (key computation, ticket, doc or LLM error, the 120 s
	// budget) becomes an unavailable result and a warning event, and the review goes on without
	// intent. ctx ends when every run of the request has been cancelled (a single cancel does
	// not end it): the derivation is then aborted and nothing is stored, and Resolve returns an
	// unavailable result. Concurrent requests of the same pull request and key share one
	// derivation.
	Resolve(ctx context.Context, req IntentRequest, emit IntentEmit) IntentResult
}

// Store is the persistence of the context: one implementation (adapters/postgres) of the four
// parts. Every method takes its handle from ctx, so it joins the transaction of Tx.InTx. A method
// that takes a workspace id sees that workspace's rows only; the others act on ids that an
// earlier workspace-scoped call returned. Unless a method says otherwise, a missing row is
// domain.ErrNotFound.
type Store interface {
	Lookups
	RunStore
	ReviewStore
	MultiAgentStore
}

// Lookups reads the rows of other contexts a review needs. The adapter reads their tables with
// its own row types.
type Lookups interface {
	// FindPull returns a pull request of the workspace.
	FindPull(ctx context.Context, workspaceID, prID string) (domain.ReviewPull, error)
	// FindRepo returns the repository of a pull request.
	FindRepo(ctx context.Context, repoID string) (domain.ReviewRepo, error)
	// FindAgent returns an agent of the workspace, enabled or not; ErrNotFound also for an id
	// that is not a UUID.
	FindAgent(ctx context.Context, workspaceID, agentID string) (domain.ReviewAgent, error)
	// EnabledAgents returns the enabled agents ordered by name (COLLATE "C"), then id; empty is
	// not an error.
	EnabledAgents(ctx context.Context, workspaceID string) ([]domain.ReviewAgent, error)
	// EnabledSkills returns the agent's linked, enabled skills in link order.
	EnabledSkills(ctx context.Context, agentID string) ([]domain.ReviewSkill, error)
	// PullFiles returns the pull request's stored files that have a patch, in path byte order
	// (COLLATE "C"): the source of the review diff. A pull request without files yields empty.
	PullFiles(ctx context.Context, prID string) ([]reviewer.PatchFile, error)
	// MarkReviewed sets pull_requests.last_reviewed_sha.
	MarkReviewed(ctx context.Context, prID, sha string) error
}

// RunStore keeps agent runs, their skills and their traces.
type RunStore interface {
	// CreateMultiAgentRun inserts the parent row of a 2+ agent request and returns its id.
	CreateMultiAgentRun(ctx context.Context, workspaceID, prID string) (string, error)
	// CreateRun inserts an agent_runs row (status running, source domain.SourceLocal) and
	// returns its id.
	CreateRun(ctx context.Context, r domain.NewRun) (string, error)
	// FindRun returns a run of the workspace.
	FindRun(ctx context.Context, workspaceID, runID string) (domain.RunState, error)
	// DeleteRun deletes a run with its trace, review and findings (cascades); false when the
	// run does not exist in the workspace.
	DeleteRun(ctx context.Context, workspaceID, runID string) (bool, error)
	// CancelIfRunning sets a running run to cancelled with the error note; false when the run
	// is not running or not in the workspace.
	CancelIfRunning(ctx context.Context, workspaceID, runID, note string) (bool, error)
	// ActiveRuns returns the pull request's running runs by ran_at, then id ascending.
	ActiveRuns(ctx context.Context, workspaceID, prID string) ([]domain.ActiveRun, error)
	// ListRuns returns every run of the pull request, newest ran_at first, ties by id ascending.
	ListRuns(ctx context.Context, workspaceID, prID string) ([]domain.RunSummary, error)
	// RecordRunSkills writes agent_run_skills: the skills at their exact versions, order 0..n-1.
	RecordRunSkills(ctx context.Context, runID string, skills []domain.ReviewSkill) error
	// CompleteRunIfRunning sets a running run to done with its record; false when the run is no
	// longer running (a cancel won), in which case the caller rolls its transaction back.
	CompleteRunIfRunning(ctx context.Context, runID string, c domain.RunCompletion) (bool, error)
	// EndRun records a failed or cancelled run: the record is always written; the status moves
	// to the given one (failed or cancelled) and the error to c.Error only from running. A run
	// already cancelled stays cancelled, keeping its error (default domain.CancelledNote); a
	// done run is not changed at all. A run that is gone is ErrNotFound.
	EndRun(ctx context.Context, runID string, status domain.RunStatus, c domain.RunCompletion) error
	// FailInterrupted sets every running run to failed, leaving error untouched, and returns how
	// many it changed.
	FailInterrupted(ctx context.Context) (int, error)
	// SaveTrace upserts the run's trace; ErrNotFound when the run was deleted meanwhile.
	SaveTrace(ctx context.Context, runID string, t domain.Trace) error
	// FindTrace returns the trace of a run of the workspace, decoded strictly; ErrNotFound when
	// the run has none, ErrTraceInvalid when the stored document violates the contract.
	FindTrace(ctx context.Context, workspaceID, runID string) (domain.Trace, error)
}

// ReviewStore keeps reviews and findings.
type ReviewStore interface {
	// InsertReview inserts a review (kind review) and returns its id; ErrNotFound when the run
	// or agent was deleted meanwhile.
	InsertReview(ctx context.Context, r domain.NewReview) (string, error)
	// InsertFindings inserts the kept findings (spec 7.3 mapping: kind defaults to finding,
	// out_of_scope = the finding's flag, false when nil, skill_id from skillIDs[skill name]; the
	// model's id and evidence are not stored).
	InsertFindings(ctx context.Context, reviewID string, findings []reviewer.Finding, skillIDs map[string]string) error
	// ReviewsForPull returns the pull request's reviews newest created_at first, each with its
	// findings in insertion order, the agent's name and, with a run, the run's usage.
	ReviewsForPull(ctx context.Context, prID string) ([]domain.ReviewRecord, error)
	// DeleteReview deletes a review of the workspace with its findings; false when none.
	DeleteReview(ctx context.Context, workspaceID, reviewID string) (bool, error)
	// AcceptFinding sets accepted_at and clears dismissed_at; DismissFinding does the reverse.
	// Both return the updated finding (eval_case_id set, memory_id nil).
	AcceptFinding(ctx context.Context, workspaceID, findingID string, at time.Time) (domain.FindingRecord, error)
	DismissFinding(ctx context.Context, workspaceID, findingID string, at time.Time) (domain.FindingRecord, error)
}

// MultiAgentStore keeps the multi-agent read model and Learn.
type MultiAgentStore interface {
	// FindMultiAgentRun returns the header of a multi-agent run of the workspace.
	FindMultiAgentRun(ctx context.Context, workspaceID, id string) (domain.MultiAgentHeader, error)
	// MultiAgentColumns returns one column per run linked to the parent, in no order, with the
	// findings of the run's newest review (memory_id set when Learn stored the finding).
	MultiAgentColumns(ctx context.Context, workspaceID, id string) ([]domain.AgentColumn, error)
	// RepoInWorkspace reports whether the repository belongs to the workspace.
	RepoInWorkspace(ctx context.Context, workspaceID, repoID string) (bool, error)
	// ListMultiAgentRuns returns the repo's newest multi-agent runs, newest ran_at first, then
	// id, at most limit.
	ListMultiAgentRuns(ctx context.Context, workspaceID, repoID string, limit int) ([]domain.MultiAgentSummary, error)
	// AgentEstimates returns, per agent with a done run that has a duration or a cost, the
	// averages over its newest domain.EstimateSampleSize such runs (any pull request, any
	// source), ordered by agent id.
	AgentEstimates(ctx context.Context, workspaceID string) ([]domain.AgentRunEstimate, error)
	// FindLearnTarget returns a finding of the workspace with its pull-request context.
	FindLearnTarget(ctx context.Context, workspaceID, findingID string) (domain.LearnTarget, error)
	// LockLearn takes the transaction-scoped advisory lock "learn:<finding id>"; it must run
	// inside Tx.InTx.
	LockLearn(ctx context.Context, findingID string) error
	// LearnedMemoryID returns the oldest learning memory entry of the workspace whose sources
	// contain {"context":"finding:<id>"}; ok is false when there is none.
	LearnedMemoryID(ctx context.Context, workspaceID, findingID string) (id string, ok bool, err error)
	// InsertLearnedMemory inserts the entry and returns its id.
	InsertLearnedMemory(ctx context.Context, m domain.NewMemory) (string, error)
}
