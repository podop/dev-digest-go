// Package domain is the vocabulary of the evals context (Phase 6): eval cases, eval suite runs and
// their per-case results, the compare and dashboard read models, and the inputs and results that
// cross the context's ports. It imports only the standard library and carries no JSON or database
// tags: the wire shapes live in transport/http and the row shapes in adapters/postgres.
//
// The pure rules (case building and checking, edit merging, error text, scoring, run input,
// compare, line diff, dashboards, periods) are added next to this file by later steps; this file
// fixes the shared types, the constants of spec sections 7.1 and 7.4-7.5 and the sentinel errors.
package domain

import (
	"errors"
	"time"
)

// Errors of the context. Stores return them; use cases map them to application errors.
var (
	// ErrNotFound is a row that does not exist in the workspace.
	ErrNotFound = errors.New("not found")
	// ErrRunActive is the violation of the one-running-run-per-owner unique index at insert.
	ErrRunActive = errors.New("an eval run is already in progress")
	// ErrRowGone is the foreign-key violation (SQLState 23503) of recording a case result: the
	// case or the run was deleted meanwhile. Nothing is recorded.
	ErrRowGone = errors.New("eval case or run was deleted")
	// ErrCaseExists is matched (errors.Is) by *CaseExistsError.
	ErrCaseExists = errors.New("an eval case already exists for this finding")
)

// CaseExistsError is the unique violation on eval_cases.source_finding_id (SQLState 23505): a case
// for the finding exists already. CaseID is the winning case.
type CaseExistsError struct{ CaseID string }

// Error implements error.
func (e *CaseExistsError) Error() string { return ErrCaseExists.Error() }

// Is makes errors.Is(err, ErrCaseExists) true.
func (e *CaseExistsError) Is(target error) bool { return target == ErrCaseExists }

// Constants of the context (spec sections 7.1, 7.4 and 7.5).
const (
	// CaseNameMax is the longest case name, also the cut of a name built from a finding.
	CaseNameMax = 120
	// DiffMaxChars is the longest stored diff, in UTF-16 code units.
	DiffMaxChars = 200_000
	// CasesPerOwnerMax is the most cases one agent or skill can have.
	CasesPerOwnerMax = 200
	// ExpectationFileMax, ExpectationTitleMax, PRTitleMax and PRDescriptionMax are the schema
	// limits of the case inputs, in UTF-16 code units.
	ExpectationFileMax  = 1000
	ExpectationTitleMax = 300
	PRTitleMax          = 300
	PRDescriptionMax    = 20_000
	// DiffFilesDetailMax is the most paths expectation_file_not_in_diff carries; RangesDetailMax the
	// most ranges expectation_lines_not_in_diff carries.
	DiffFilesDetailMax = 50
	RangesDetailMax    = 50
	// ErrorTextMax is the cut of the error text stored on a case result or a run.
	ErrorTextMax = 500
	// UnknownError is the error text of an empty message.
	UnknownError = "Unknown error"
	// InterruptedError is the error of a run a shutdown or a restart cut short.
	InterruptedError = "interrupted"
	// EveryCaseErrored is the run error when every outcome errored and no case message exists.
	EveryCaseErrored = "every case errored"
	// CaseConcurrency is the cases of one run in flight; CaseBudget the time one case may take;
	// ShutdownDrain how long shutdown waits for runs to record themselves.
	CaseConcurrency = 3
	CaseBudget      = 120 * time.Second
	ShutdownDrain   = 5 * time.Second
	// RunListDefault and RunListMax are the default and maximum limit of a run list.
	RunListDefault = 20
	RunListMax     = 100
	// SparklineMax, RecentRunsMax, TrendMax and LatestWindow are the dashboard sizes: sparkline
	// points per agent, recent runs of the overview, trend points with a period, and finished runs
	// read for latest/previous/trend without a period.
	SparklineMax  = 10
	RecentRunsMax = 20
	TrendMax      = 100
	LatestWindow  = 20
	// RegressionThreshold is the rounded delta at or below which a metric raises the alert.
	RegressionThreshold = -0.02
)

// OwnerKind says who owns a case or a suite run.
type OwnerKind string

// The owner kinds.
const (
	OwnerAgent OwnerKind = "agent"
	OwnerSkill OwnerKind = "skill"
)

// ExpectationKind is what a case expects of the review.
type ExpectationKind string

// The expectation kinds.
const (
	MustFind    ExpectationKind = "must_find"
	MustNotFlag ExpectationKind = "must_not_flag"
)

// CaseStatus is the outcome of one case in one run.
type CaseStatus string

// The case statuses. CaseAbsent appears only in a compare, for a case a run does not contain.
const (
	CasePassed CaseStatus = "passed"
	CaseFailed CaseStatus = "failed"
	CaseError  CaseStatus = "error"
	CaseAbsent CaseStatus = "absent"
)

// RunStatus is the status of a suite run.
type RunStatus string

// The run statuses.
const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
)

// FindingMatch labels a finding of a result against the case expectation.
type FindingMatch string

// The match labels.
const (
	MatchHit       FindingMatch = "hit"
	MatchViolation FindingMatch = "violation"
	MatchUnlabeled FindingMatch = "unlabeled"
)

// Metric names a score of a run, in the order the regression alert lists them.
type Metric string

// The metrics.
const (
	MetricRecall           Metric = "recall"
	MetricPrecision        Metric = "precision"
	MetricCitationAccuracy Metric = "citation_accuracy"
)

// Period is the window of a run list or dashboard; the empty period is absent.
type Period string

// The periods.
const (
	PeriodNone Period = ""
	Period7d   Period = "7d"
	Period30d  Period = "30d"
	Period90d  Period = "90d"
	PeriodAll  Period = "all"
)

// SkipReason is why run-all did not start an agent.
type SkipReason string

// The skip reasons.
const (
	SkipNoCases   SkipReason = "no_cases"
	SkipRunActive SkipReason = "run_active"
)

// DiffOp is the operation of one line of the prompt line diff.
type DiffOp string

// The line diff operations.
const (
	OpSame DiffOp = "same"
	OpAdd  DiffOp = "add"
	OpDel  DiffOp = "del"
)

// Expectation is what a case expects. Severity, Category and Title are nil when not set.
type Expectation struct {
	Kind               ExpectationKind
	File               string
	StartLine, EndLine int
	Severity, Category *string
	Title              *string
}

// InputMeta is the pull request metadata a case reviews with. Repo, PRNumber and HeadSHA are nil
// on a manual case; PRDescription is nil when none.
type InputMeta struct {
	PRTitle       string
	PRDescription *string
	Repo          *string
	PRNumber      *int
	HeadSHA       *string
}

// Case is a stored eval case. InputDiff is "" for a NULL column.
type Case struct {
	ID, WorkspaceID string
	OwnerKind       OwnerKind
	OwnerID         string
	Name            string
	Notes           *string
	Expectation     Expectation
	InputDiff       string
	InputMeta       InputMeta
	// SourceFindingID, SourceReviewID and SourcePRID are set on a case made from a finding.
	SourceFindingID, SourceReviewID, SourcePRID *string
	// ContentEditedAt is set at the first edit that changes the case's content; never cleared.
	ContentEditedAt *time.Time
	CreatedAt       time.Time
}

// NewCase is a case to insert; the store assigns the id and the creation time.
type NewCase struct {
	WorkspaceID string
	OwnerKind   OwnerKind
	OwnerID     string
	Name        string
	Notes       *string
	Expectation Expectation
	InputDiff   string
	InputMeta   InputMeta
	// Sources are set on a case made from a finding.
	SourceFindingID, SourceReviewID, SourcePRID *string
}

// LastResult is a case's result in the newest finished run that contains it (spec 7.4.5).
type LastResult struct {
	RunID  string
	Status CaseStatus
	RanAt  time.Time
}

// CaseView is a case with its last result (nil when it has none).
type CaseView struct {
	Case       Case
	LastResult *LastResult
}

// CreateInput is a validated EvalCaseCreateInput (spec 7.4.2). PRTitle and PRDescription are nil
// when absent or null.
type CreateInput struct {
	// Name is already trimmed by the schema.
	Name                   string
	Notes                  *string
	Expectation            Expectation
	InputDiff              string
	PRTitle, PRDescription *string
}

// Field is one field of a PATCH body: Set false = absent (keep), Set true with a nil Value =
// explicit null (clear), else the new value.
type Field[T any] struct {
	Set   bool
	Value *T
}

// UpdateInput is a validated AgentEvalCaseUpdateInput (spec 7.4.3). A nil pointer is absent; the
// fields that accept null are Field values.
type UpdateInput struct {
	Name                   *string
	Notes                  Field[string]
	Kind                   *ExpectationKind
	File                   *string
	StartLine, EndLine     *int
	Severity, Category     Field[string]
	Title                  Field[string]
	InputDiff              *string
	PRTitle, PRDescription Field[string]
}

// FindingSource is what turning a decided finding into a case reads (spec 7.4.1 step 1): the
// finding of the workspace joined to its review, PR, repo and the PR's stored patch for the
// finding's file.
type FindingSource struct {
	FindingID, ReviewID, PRID string
	// AgentID is the review's agent; nil when the review has none.
	AgentID                 *string
	AcceptedAt, DismissedAt *time.Time
	File                    string
	StartLine, EndLine      int
	Severity, Category      string
	Title                   string
	// Patch is the PR's stored patch of File; nil when there is none.
	Patch        *string
	PRTitle      string
	PRBody       *string
	RepoFullName string
	PRNumber     int
	HeadSHA      string
}

// SkillSnapshot is a skill frozen into a run's configuration.
type SkillSnapshot struct {
	ID, Name, Description, Body string
	Version                     int
}

// Config is the frozen configuration of a run (the jsonb column config). Strategy is the agent's
// stored strategy text; an unknown one runs as auto.
type Config struct {
	Provider, Model, SystemPrompt string
	Strategy                      string
	Skills                        []SkillSnapshot
}

// AgentSnapshot is an agent as a run freezes it: Config.Skills are its enabled linked skills in
// link order.
type AgentSnapshot struct {
	AgentID string
	Version int
	Name    string
	Config  Config
}

// ResultFinding is a kept finding of a case result with its label.
type ResultFinding struct {
	File               string
	StartLine, EndLine int
	Severity, Category *string
	Title              *string
	Match              FindingMatch
}

// CaseResult is one case's result in a run, as the run detail and the compare read it.
// Status is CaseError for a NULL column; DurationMS is 0 for a NULL column.
type CaseResult struct {
	CaseID, CaseName string
	Expectation      Expectation
	Status           CaseStatus
	Findings         []ResultFinding
	DroppedCount     int
	DurationMS       int
	CostUSD          *float64
	Error            *string
}

// NewResult is a case result to record (table eval_runs).
type NewResult struct {
	RunID, CaseID string
	Status        CaseStatus
	Findings      []ResultFinding
	DroppedCount  int
	// HitCount, ViolationCount and UnlabeledCount are the label counts of Findings.
	HitCount, ViolationCount, UnlabeledCount int
	DurationMS                               int
	CostUSD                                  *float64
	Error                                    *string
}

// Metrics are the scores of a run; nil while running and on a zero denominator.
type Metrics struct {
	Recall, Precision, CitationAccuracy *float64
	UnlabeledFindings                   int
	CostUSD                             *float64
}

// Run is a stored suite run. SkillID and SkillVersion are set on a skill run; AgentID,
// AgentVersion and AgentName describe the (host) agent frozen at start.
type Run struct {
	ID, WorkspaceID string
	OwnerKind       OwnerKind
	SkillID         *string
	SkillVersion    *int
	AgentID         string
	AgentVersion    int
	AgentName       string
	Config          Config
	CaseIDs         []string
	Status          RunStatus
	StartedAt       time.Time
	FinishedAt      *time.Time
	CasesTotal      int
	CasesFinished   int
	CasesPassed     int
	CasesErrored    int
	Metrics         Metrics
	DurationMS      *int
	Error           *string
}

// NewRun is a run to insert as running; CasesTotal is len(CaseIDs).
type NewRun struct {
	WorkspaceID  string
	OwnerKind    OwnerKind
	SkillID      *string
	SkillVersion *int
	AgentID      string
	AgentVersion int
	AgentName    string
	Config       Config
	CaseIDs      []string
}

// RunFinish is the final state of a run, written only while it is still running (spec 7.5.7).
type RunFinish struct {
	Status                    RunStatus
	Error                     *string
	FinishedAt                time.Time
	DurationMS                int
	Metrics                   Metrics
	CasesPassed, CasesErrored int
}

// RunDetail is a run with its results in execution order.
type RunDetail struct {
	Run     Run
	Results []CaseResult
}

// Skip is an agent run-all did not start.
type Skip struct {
	AgentID string
	Reason  SkipReason
}

// RunAllResult is the answer to run-all: started runs and skips, in agent list order.
type RunAllResult struct {
	Started []Run
	Skipped []Skip
}

// Delta is B - A per metric (spec 7.5.9); nil when a side is nil.
type Delta struct {
	Recall, Precision, CitationAccuracy, CostUSD *float64
}

// CompareCase is one case of a compare; the side that lacks it is CaseAbsent.
type CompareCase struct {
	CaseID, CaseName string
	A, B             CaseStatus
	Flipped          bool
}

// DiffLine is one line of the prompt line diff.
type DiffLine struct {
	Op   DiffOp
	Line string
}

// ValueChange is an A and B value that differ.
type ValueChange struct{ A, B string }

// ConfigDiff is the difference of two run configurations; Model and Strategy are nil when equal.
type ConfigDiff struct {
	SystemPrompt               []DiffLine
	Model, Strategy            *ValueChange
	SkillsAdded, SkillsRemoved []string
}

// Compare is the comparison of two finished runs of one owner.
type Compare struct {
	A, B       Run
	Delta      Delta
	Cases      []CompareCase
	ConfigDiff ConfigDiff
}

// AgentRef is an agent of the dashboards: its current name and model.
type AgentRef struct{ ID, Name, Model string }

// SparkPoint is one point of an agent's sparkline.
type SparkPoint struct {
	RunID                               string
	Recall, Precision, CitationAccuracy *float64
}

// AgentOverview is one agent of the overview.
type AgentOverview struct {
	AgentRef
	CasesTotal int
	Latest     *Run
	// Sparkline is oldest first, at most SparklineMax.
	Sparkline []SparkPoint
}

// RecentRun is a run of the overview's recent list with the agent's current name.
type RecentRun struct {
	Run       Run
	AgentName string
}

// Overview is the answer of GET /eval/dashboard.
type Overview struct {
	Agents     []AgentOverview
	RecentRuns []RecentRun
}

// MetricDelta is one metric of the regression alert.
type MetricDelta struct {
	Metric Metric
	Delta  float64
}

// AgentDashboard is the answer of GET /agents/:id/eval/dashboard. Alert is nil when none.
type AgentDashboard struct {
	AgentID          string
	CasesTotal       int
	Latest, Previous *Run
	// Trend is oldest first.
	Trend []Run
	Alert []MetricDelta
}
