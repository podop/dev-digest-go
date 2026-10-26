// Package domain is the vocabulary of the brief context (Phase 5, spec section 7.9): the facts a
// generation gathers about a pull request, the model's raw output, and the stored brief with its
// missing-input list. The pure rules (facts to prompt, trimming, normalisation and grounding,
// missing inputs, staleness, the output schema) are added next to this file; it fixes only the
// shared types and constants. It imports only the standard library and the review engine's
// domain package (the shared kernel: Intent, Severity, JS text helpers), platform/codehost's
// provider names, and carries no JSON or
// database tags: the wire and stored shapes live in transport/http and adapters/postgres.
package domain

import (
	"errors"
	"time"

	"dev-digest-go/backend/internal/platform/codehost"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Errors of the context. Use cases map them to application errors.
var (
	// ErrNotFound is returned by the store for a pull request that is not in the workspace.
	ErrNotFound = errors.New("brief: not found")
)

// The model call (spec section 7.9.4 and the table of 7.12).
const (
	// PromptVersion is stored in every brief; a stored brief with another value is stale.
	PromptVersion = "v1"
	// Feature is the prompt log's feature label; the correlation id is
	// "brief:<pr id>:<first 12 of the head SHA>".
	Feature = "brief"
	// FeatureModel is the settings feature whose model the call uses (default openai/gpt-4.1).
	FeatureModel = "risk_brief"
	// SchemaName names the response schema.
	SchemaName = "PrBriefLlm"
	// Temperature, MaxOutputTokens and Reprompts are the call's parameters.
	Temperature     = 0.2
	MaxOutputTokens = 8_000
	Reprompts       = 1
	// GenerationBudget bounds one generation: facts, model call and storage.
	GenerationBudget = 120 * time.Second
	// IssueTimeout bounds the linked-issue read.
	IssueTimeout = 10 * time.Second
	// InputBudgetTokens is the estimated-token budget of the prompt (system plus user).
	InputBudgetTokens = 8_000
)

// Limits of the prompt facts (spec section 7.9.1).
const (
	MaxDescription  = 4_000
	MaxIssueBody    = 3_000
	MaxFindingTitle = 120
	MaxFiles        = 100
	MaxRanges       = 20
	MaxFindings     = 30
	MaxCallers      = 30
)

// Limits of the stored brief (spec sections 7.9.5 and 7.3: the contract's maximums, enforced when
// a model output is normalised and again when a stored brief is read).
const (
	MaxSummary         = 600
	MaxRisks           = 6
	MaxFocus           = 8
	MaxRiskKind        = 60
	MaxRiskTitle       = 120
	MaxRiskExplanation = 600
	MaxFocusReason     = 160
	DefaultRiskKind    = "other"
)

// RiskSeverity is a risk's severity.
type RiskSeverity string

// The risk severities.
const (
	RiskHigh   RiskSeverity = "high"
	RiskMedium RiskSeverity = "medium"
	RiskLow    RiskSeverity = "low"
)

// Pull is the pull request of a generation with its repository.
type Pull struct {
	ID          string
	WorkspaceID string
	RepoID      string
	// Owner and Name are the repository's.
	Owner, Name string
	// Provider ("github" | "gitlab"; "" reads as github), Host (the host key) and ProjectID (the
	// GitLab project id; 0 = none) locate the repository: GitHub reads apply to GitHub only
	// (FR29) and the clone location comes from codehost.CloneKey.
	Provider  string
	Host      string
	ProjectID int64
	Number    int
	Title     string
	// Body is the PR description; nil or blank means none.
	Body *string
	// Base is the base branch name and HeadSHA the head commit as stored now.
	Base    string
	HeadSHA string
}

// IsGitLab reports whether the pull request belongs to a GitLab project.
func (p Pull) IsGitLab() bool { return p.Provider == codehost.GitLab }

// StoredFile is a stored file of the pull request (pr_files).
type StoredFile struct {
	Path                 string
	Additions, Deletions int
	// Patch is the file's unified-diff text; "" when none is stored. Only its hunk headers are
	// read (changed line ranges); no hunk body reaches a prompt.
	Patch string
}

// FindingFact is one finding of a current review as the prompt and the grounding see it.
type FindingFact struct {
	Severity  reviewer.Severity
	File      string
	StartLine int
	EndLine   int
	Title     string
}

// ReviewFacts is one current review (the newest kind='review' review of an agent; NULL agents form
// one group) with its findings, dismissed ones included, ordered by file, start line, id.
type ReviewFacts struct {
	ReviewID string
	// AgentID is nil for a review without an agent.
	AgentID  *string
	Findings []FindingFact
}

// DocStatus is the outcome for one attached project document.
type DocStatus string

// The document statuses (project-context run resolution); OverBudget also covers the documents
// the prompt trim drops.
const (
	DocIncluded   DocStatus = "included"
	DocMissing    DocStatus = "missing"
	DocTooLarge   DocStatus = "too_large"
	DocOverBudget DocStatus = "over_budget"
	DocUnreadable DocStatus = "unreadable"
)

// Document is a project spec document attached to a reviewed agent or one of its skills.
type Document struct {
	Path   string
	Status DocStatus
	// Text is set for included documents only.
	Text string
}

// Issue is the linked GitHub issue.
type Issue struct {
	Number int
	Title  string
	// Body is nil when the issue has none.
	Body *string
}

// BlastSymbol is a changed symbol of the blast snapshot.
type BlastSymbol struct {
	Name, File, Kind string
}

// BlastCaller is a caller of the blast snapshot.
type BlastCaller struct {
	Name, File string
	Line       int
}

// BlastGroup is the impact of one changed symbol.
type BlastGroup struct {
	Symbol            string
	Callers           []BlastCaller
	EndpointsAffected []string
	CronsAffected     []string
}

// Blast is the blast radius as the brief snapshots it (the blast context's read model, mapped by
// the brief's peers adapter). Only a usable (not degraded) blast is stored or prompted.
type Blast struct {
	ChangedSymbols []BlastSymbol
	Downstream     []BlastGroup
	Summary        string
	Degraded       bool
	// Reason is the degraded reason ("flag_off", "index_failed", "index_partial",
	// "repo_too_large", "no_data"); "" when none.
	Reason string
}

// Risk is a risk area of the brief.
type Risk struct {
	Kind        string
	Title       string
	Explanation string
	Severity    RiskSeverity
	// FileRefs are "path:start" or "path:start-end"; at least one.
	FileRefs []string
}

// FocusItem is a "read this first" pointer.
type FocusItem struct {
	File string
	// Line is 1-based.
	Line   int
	Reason string
}

// MissingInputName is an input a brief may lack.
type MissingInputName string

// The input names.
const (
	InputDescription MissingInputName = "description"
	InputIntent      MissingInputName = "intent"
	InputBlast       MissingInputName = "blast"
	InputSpecs       MissingInputName = "specs"
	InputLinkedIssue MissingInputName = "linked_issue"
)

// MissingReason says why an input is missing.
type MissingReason string

// The reasons.
const (
	ReasonEmpty        MissingReason = "empty"
	ReasonNotDerived   MissingReason = "not_derived"
	ReasonDegraded     MissingReason = "degraded"
	ReasonNoReviewRun  MissingReason = "no_review_run"
	ReasonNoneAttached MissingReason = "none_attached"
	ReasonOverBudget   MissingReason = "over_budget"
	ReasonUnreadable   MissingReason = "unreadable"
	ReasonNotLinked    MissingReason = "not_linked"
	ReasonFetchFailed  MissingReason = "fetch_failed"
)

// MissingInput is an input the brief was generated without. Detail is "" when none ("<n> of
// <attached>" for documents, the degraded reason for the blast).
type MissingInput struct {
	Input  MissingInputName
	Reason MissingReason
	Detail string
}

// Brief is the stored and returned PR brief. History is never part of it.
type Brief struct {
	Summary     string
	Risks       []Risk
	ReviewFocus []FocusItem
	// Intent is the snapshot of the stored intent; nil when there was none.
	Intent *reviewer.Intent
	// Blast is the usable blast; nil when it was missing or degraded.
	Blast         *Blast
	MissingInputs []MissingInput
	// SpecsUsed are the paths of the documents of the trimmed prompt.
	SpecsUsed []string
	HeadSHA   string
	// GeneratedAt is stored as ISO-8601 UTC with milliseconds.
	GeneratedAt   time.Time
	PromptVersion string
	Provider      string
	Model         string
	TokensIn      int
	TokensOut     int
	// CostUSD is the call's total; nil when unpriced; not rounded.
	CostUSD *float64
	// ModelRequests is 2 when the call took two attempts, else 1.
	ModelRequests int
}

// State is the answer of a read or a generation: the stored brief (nil = none) and whether it is
// out of date. Stale is false without a brief.
type State struct {
	Brief *Brief
	Stale bool
}

// LLMOutput is the model's answer to the PrBriefLlm schema, before normalisation.
type LLMOutput struct {
	Summary     string
	Risks       []LLMRisk
	ReviewFocus []LLMFocus
}

// LLMRisk is a risk as the model wrote it.
type LLMRisk struct {
	Kind, Title, Explanation string
	// Severity is "high", "medium" or "low" (the schema enforces it).
	Severity string
	FileRefs []string
}

// LLMFocus is a focus item as the model wrote it; Line is any JSON number.
type LLMFocus struct {
	File   string
	Line   float64
	Reason string
}
