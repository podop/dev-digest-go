// Package domain is the vocabulary of the reviews context (Phase 4): runs and their lifecycle,
// reviews and findings as the API reads them, the run trace, the multi-agent read model, and the
// inputs and results that cross the context's ports. It imports only the standard library and
// the engine's domain package, and carries no JSON or database tags: the wire shapes live in
// transport and the row shapes in adapters/postgres. The run trace document is encoded and
// decoded by adapters/postgres (tracedoc.go), never serialised from these types directly.
//
// Pure rules (task line, skill blocks, callers digest, usage meter, status and event rules,
// multi-agent grouping, Learn text) are added next to this file by the domain step; this file
// fixes only the shared types and the constants of spec section 7.1.
package domain

import (
	"encoding/json"
	"errors"
	"time"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Errors of the context. Use cases map them to application errors.
var (
	// ErrNotFound is returned by the store for a row that does not exist in the workspace, and by
	// the writes of a run whose row was deleted meanwhile (SQLState 23503).
	ErrNotFound = errors.New("not found")
	// ErrTraceInvalid is returned by the store's trace read when the stored document does not
	// satisfy the RunTrace contract; the use case logs it (without the document) and answers 500.
	ErrTraceInvalid = errors.New("stored run trace is invalid")
)

// Constants of the context (spec section 7.1 and 7.5).
const (
	// MaxAgentIDs is the most agent ids one review request may name (before deduplication).
	MaxAgentIDs = 20
	// MultiAgentListLimit is the default and maximum limit of the multi-agent run list.
	MultiAgentListLimit = 20
	// EstimateSampleSize is the number of newest done runs an estimate averages over.
	EstimateSampleSize = 10
	// CallersLimit is the number of caller signatures a review prompt carries.
	CallersLimit = 10
	// RepoMapBudget is the token budget of the repo map in a review prompt.
	RepoMapBudget = 1_500
	// HotFilePercentile is the file-rank percentile from which a changed file counts as hot.
	HotFilePercentile = 95
	// ProjectContextBudgetTokens is the token budget of a run's project-context documents.
	ProjectContextBudgetTokens = 16_000
	// NoGrounding is the grounding summary of a run that produced no findings to ground.
	NoGrounding = "0/0 passed"
	// CancelledNote is the error stored on a run the user (or shutdown) cancelled.
	CancelledNote = "Cancelled by user"
	// SourceLocal is the run source of a review a user started and the trace's config.source.
	SourceLocal = "local"
	// SourceAuto is the run source of a review the automatic-review trigger started
	// (agent_runs.source and the trace's config.source).
	SourceAuto = "auto"
	// BusTTL is how long the run bus keeps a completed run's buffer and cancel flag.
	BusTTL = 5 * time.Minute
	// ShutdownDrain is how long shutdown waits for cancelled runs to record themselves.
	ShutdownDrain = 5 * time.Second
	// SubscriberBacklog is how many events a subscriber may be behind before its stream ends.
	SubscriberBacklog = 1_024
)

// RunStatus is the status of an agent run. The empty status is a NULL column (legacy rows).
type RunStatus string

// The run statuses.
const (
	StatusRunning   RunStatus = "running"
	StatusDone      RunStatus = "done"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

// RunEvent is one line of a run's Live Log as the bus numbers and stamps it.
type RunEvent struct {
	RunID string
	// Seq counts from 1 without gaps within a run.
	Seq  int
	Kind reviewer.EventKind
	Msg  string
	// T is the local wall-clock time "HH:MM:SS".
	T string
	// Data is the event's JSON payload; nil = none (the key is omitted on the wire).
	Data json.RawMessage
}

// ReviewAgent is the agent a run executes.
type ReviewAgent struct {
	ID           string
	Name         string
	Provider     string
	Model        string
	SystemPrompt string
	Strategy     reviewer.Strategy
	CIFailOn     reviewer.FailOn
	// RepoIntel is the agent's repo-intel toggle.
	RepoIntel bool
	Version   int
	// Plugin is the plugin that owns the agent; nil = created by the user. Only the named read
	// (FindAgent) fills it; the list reads already leave out the agents of a disabled plugin.
	Plugin *PluginRef
}

// PluginRef is the plugin an agent belongs to.
type PluginRef struct {
	ID      string
	Name    string
	Enabled bool
}

// ReviewPull is the pull request under review.
type ReviewPull struct {
	ID          string
	WorkspaceID string
	RepoID      string
	Number      int
	Title       string
	Author      string
	// Base is the base branch name; Branch the head branch name (pull_requests.branch);
	// HeadSHA the head commit read at request time.
	Base    string
	Branch  string
	HeadSHA string
	// Body is the PR description; nil or blank means none.
	Body *string
}

// ReviewRepo is the repository a pull request belongs to.
type ReviewRepo struct {
	ID    string
	Owner string
	Name  string
	// Provider ("github" | "gitlab"; "" reads as github), Host (the host key) and ProjectID (the
	// GitLab project id; 0 = none) locate the repository's clone: see codehost.CloneKey.
	Provider  string
	Host      string
	ProjectID int64
	// WebURL is the stored web address of a GitLab project (repos.web_url); "" for GitHub. The
	// intent derivation reads the GitLab "same project" link rule from it.
	WebURL string
}

// ReviewSkill is an enabled skill linked to an agent, as it goes into a run: rendered as a
// "### <name>" block, recorded in agent_run_skills at its exact Version and resolved from a
// finding's cited skill name.
type ReviewSkill struct {
	ID          string
	Name        string
	Description string
	Body        string
	Version     int
}

// Job is one queued agent run.
type Job struct {
	Agent ReviewAgent
	RunID string
}

// Batch is the agent runs one review request created; the executor runs them in the background.
type Batch struct {
	WorkspaceID string
	Pull        ReviewPull
	Repo        ReviewRepo
	// Jobs are in target order.
	Jobs []Job
	// Source is the source of every run of the batch (SourceLocal or SourceAuto); empty = SourceLocal.
	Source string
}

// NewRun is a new agent_runs row; it starts running with source Source (SourceLocal when empty).
type NewRun struct {
	WorkspaceID string
	// Source is SourceLocal or SourceAuto; empty = SourceLocal.
	Source   string
	AgentID  string
	PRID     string
	Provider string
	Model    string
	// MultiAgentRunID is the parent run of a 2+ agent request; nil otherwise.
	MultiAgentRunID *string
}

// RunState is the status and failure note of one run.
type RunState struct {
	ID string
	// Status is empty for a NULL column.
	Status RunStatus
	Error  *string
}

// RunCompletion is the final record of a run, written with its terminal status.
type RunCompletion struct {
	DurationMS          int
	TokensIn, TokensOut int
	// CostUSD is nil when unpriced; a run that made no call records 0.
	CostUSD       *float64
	FindingsCount int
	Grounding     string
	// Score and Blockers are set on a done run only.
	Score, Blockers *int
	// Error is the failure or cancel note; nil clears it.
	Error *string
}

// NewReview is a new reviews row (kind review).
type NewReview struct {
	WorkspaceID string
	PRID        string
	AgentID     string
	RunID       string
	Verdict     reviewer.Verdict
	Summary     string
	Score       int
	Model       string
}

// FindingRecord is a stored finding as the API reads it.
type FindingRecord struct {
	ID       string
	Severity reviewer.Severity
	Category reviewer.Category
	Title    string
	File     string
	// StartLine and EndLine are the stored range.
	StartLine, EndLine int
	Rationale          string
	Suggestion         *string
	Confidence         float64
	// Kind is the stored kind (KindFinding when the model sent none).
	Kind reviewer.Kind
	// TrifectaComponents is nil when none (wire null).
	TrifectaComponents []string
	// Skill is the cited skill name (findings.skill_name).
	Skill      *string
	OutOfScope bool
	ReviewID   string
	// AcceptedAt and DismissedAt are the triage timestamps; at most one is set.
	AcceptedAt, DismissedAt *time.Time
	// EvalCaseID is the id of the eval case whose source finding this is; nil when none.
	EvalCaseID *string
	// MemoryID is the Learn memory entry; set by the multi-agent read model and by Learn.
	MemoryID *string
}

// RunUsage is the usage of the run that produced a review.
type RunUsage struct {
	CostUSD             *float64
	TokensIn, TokensOut *int
}

// ReviewRecord is a stored review with its findings, its agent's name and its run's usage.
type ReviewRecord struct {
	ID             string
	PRID           string
	Kind           string
	AgentID, RunID *string
	// AgentName is nil when the agent is gone.
	AgentName *string
	Verdict   *string
	Summary   *string
	Score     *int
	Model     *string
	CreatedAt time.Time
	// Usage is set when RunID is set (the wire then carries cost_usd, tokens_in, tokens_out).
	Usage *RunUsage
	// Findings are in insertion order.
	Findings []FindingRecord
}

// RunSummary is one row of a pull request's run history.
type RunSummary struct {
	RunID               string
	AgentID, AgentName  *string
	Provider, Model     *string
	Status              RunStatus
	Error               *string
	DurationMS          *int
	TokensIn, TokensOut *int
	CostUSD             *float64
	FindingsCount       *int
	Grounding           *string
	RanAt               *time.Time
	Score, Blockers     *int
}

// ActiveRun is a running run of a pull request.
type ActiveRun struct {
	RunID              string
	AgentID, AgentName *string
	RanAt              *time.Time
}

// AgentColumn is one agent's result column of a multi-agent run.
type AgentColumn struct {
	RunID string
	// AgentID and AgentName are empty when the agent is gone.
	AgentID, AgentName string
	Provider, Model    *string
	// Status is never empty: the store maps a NULL status to failed.
	Status  RunStatus
	Error   *string
	Verdict *string
	// Score is the review's score, else the run's.
	Score      *int
	Summary    *string
	DurationMS *int
	CostUSD    *float64
	// Findings are the newest review's findings, with MemoryID set when Learn stored them.
	Findings []FindingRecord
}

// ConflictTake is one agent's stance on a location group.
type ConflictTake struct {
	AgentID, Persona, RunID string
	// Verdict is the best severity the agent flagged, or "ignored".
	Verdict string
	// Note is the title of the agent's best finding in the group, "" when ignored.
	Note       string
	FindingIDs []string
}

// Conflict is a location group over the done columns.
type Conflict struct {
	File string
	// Line is the smallest start line and EndLine the largest end line of the group.
	Line, EndLine int
	Title         string
	IsConflict    bool
	Takes         []ConflictTake
}

// MultiAgentHeader is the parent row of a multi-agent run joined with its pull request.
type MultiAgentHeader struct {
	ID, PRID, RepoID string
	PRNumber         *int
	PRTitle          *string
	RanAt            time.Time
}

// MultiAgentRun is the comparison read model of one multi-agent run.
type MultiAgentRun struct {
	MultiAgentHeader
	// AgentCount is the number of columns.
	AgentCount int
	// TotalDurationMS is the longest column duration (null counted as 0).
	TotalDurationMS int
	// TotalCostUSD sums the non-null column costs; nil when none.
	TotalCostUSD *float64
	Columns      []AgentColumn
	Conflicts    []Conflict
}

// MultiAgentSummary is one row of a repo's multi-agent run list.
type MultiAgentSummary struct {
	ID, PRID     string
	PRNumber     *int
	PRTitle      *string
	RanAt        time.Time
	AgentCount   int
	RunningCount int
	// TotalCostUSD sums the linked runs' costs; nil when none.
	TotalCostUSD *float64
}

// AgentRunEstimate is the average duration and cost of an agent's newest done runs.
type AgentRunEstimate struct {
	AgentID    string
	SampleSize int
	// AvgDurationMS and AvgCostUSD are nil when no sampled run has the value.
	AvgDurationMS, AvgCostUSD *float64
}

// LearnTarget is a finding with the pull-request context Learn needs.
type LearnTarget struct {
	Finding  FindingRecord
	RepoID   string
	PRNumber int
}

// NewMemory is the memory entry Learn stores: scope repo, kind learning, no embedding, sources
// [{"pr": PRNumber, "context": "finding:<FindingID>"}].
type NewMemory struct {
	WorkspaceID string
	RepoID      string
	FindingID   string
	PRNumber    int
	Content     string
	Confidence  float64
}

// CallerSignature is one caller row of the repo-intel CallerSignatures operation.
type CallerSignature struct {
	File, Symbol, Signature string
}

// RepoMap is the repo-intel repo map.
type RepoMap struct {
	Text   string
	Tokens int
	Cached bool
	// Degraded is true when the index has no usable map (flag off, not indexed).
	Degraded bool
}

// PathRank is the file-rank percentile of a path.
type PathRank struct {
	Path       string
	Percentile int
}

// ContextStatus is the outcome for one project-context document.
type ContextStatus string

// The document statuses.
const (
	ContextIncluded   ContextStatus = "included"
	ContextMissing    ContextStatus = "missing"
	ContextTooLarge   ContextStatus = "too_large"
	ContextOverBudget ContextStatus = "over_budget"
	ContextUnreadable ContextStatus = "unreadable"
)

// ContextOrigin says why a document is in a run's context: attached to the agent, or to one of
// its skills.
type ContextOrigin struct {
	// Kind is "agent" or "skill".
	Kind string
	// SkillID and SkillName are set when Kind is "skill".
	SkillID, SkillName string
}

// ContextDoc is one project-context document of a run, in prompt order.
type ContextDoc struct {
	Path    string
	DocType string
	Origin  ContextOrigin
	Tokens  int
	Status  ContextStatus
	// Source is "repo" or "store".
	Source string
	// Text is the text as sent; set for included documents only.
	Text *string
}

// ContextTrace is the trace's project_context record.
type ContextTrace struct {
	BudgetTokens int
	TokensTotal  int
	Docs         []ContextDoc
}

// LogLine is one line of the trace's log.
type LogLine struct {
	T    string
	Kind reviewer.EventKind
	Msg  string
}

// ToolCall is one chunk call of the trace (tool "review_file", args the chunk label, meta the mode).
type ToolCall struct {
	Tool, Args, Meta string
	MS               int
}

// SkillUsed is a skill at the exact version a run's prompt carried.
type SkillUsed struct {
	ID, Name string
	Version  int
}

// TraceConfig is the trace's config record.
type TraceConfig struct {
	Agent string
	// Version is the agent version as a string.
	Version  string
	Provider string
	Model    string
	// PR is the pull request number.
	PR int
	// Source is SourceLocal or SourceAuto.
	Source string
}

// TraceStats is the trace's stats record.
type TraceStats struct {
	DurationMS          int
	TokensIn, TokensOut int
	// CostUSD is the engine sum, unrounded; nil when unpriced.
	CostUSD   *float64
	Findings  int
	Grounding string
}

// Trace is the document stored per run in run_traces (spec 7.6). memory_pulled ([]) is a constant
// of Phases 4 and 5 and is left out of the type.
type Trace struct {
	// Ended marks the minimal trace of a failed, cancelled or diff-failed run: its
	// prompt_assembly holds only system (the agent's prompt, without the guard), skills null,
	// memory null, specs null and user "" — the callers, repo_map, pr_description and intent keys
	// are absent, unlike in a completed trace where they are present (null when empty).
	Ended          bool
	Config         TraceConfig
	Stats          TraceStats
	PromptAssembly reviewer.Assembly
	ToolCalls      []ToolCall
	RawOutput      string
	// SpecsRead are the paths of the included documents, in order.
	SpecsRead  []string
	Log        []LogLine
	SkillsUsed []SkillUsed
	// ProjectContext is nil when the run had no attached documents (the key is absent).
	ProjectContext *ContextTrace
	// Intent is the intent layer's contribution to the run (Phase 5 spec 7.5); nil is the null
	// "intent" key: the switch was off, or the run's diff failed before the pre-work.
	Intent *IntentTrace
}
