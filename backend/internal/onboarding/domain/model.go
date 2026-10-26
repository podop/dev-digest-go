// Package domain is the vocabulary of the onboarding context (Phase 5, spec section 7.10): the
// repository and index facts a generation starts from, the model's raw output, and the stored
// tour with its staleness. The pure rules (secret redaction, command safety, excerpts and
// signals, prompt and trim, tour normalisation, staleness, the output schema) are added next to
// this file; it fixes only the shared types and constants. It imports only the standard library
// and carries no JSON or database tags: the wire and stored shapes live in transport/http and
// adapters/postgres.
package domain

import (
	"errors"
	"time"
)

// ErrNotFound is returned by the store for a repository that is not in the workspace.
var ErrNotFound = errors.New("onboarding: not found")

// The model call (spec section 7.10.4 and the table of 7.12).
const (
	// PromptVersion is stored in every tour; a stored tour with another value is stale.
	PromptVersion = 1
	// Language is the tour's output language.
	Language = "en"
	// FeatureModel is the settings feature whose model the call uses (default
	// openrouter/deepseek/deepseek-v4-flash); Feature labels the Phase 2 client's prompt log line.
	FeatureModel = "onboarding"
	Feature      = FeatureModel
	// SchemaName names the response schema.
	SchemaName = "OnboardingTour"
	// Temperature, MaxOutputTokens and Reprompts are the call's parameters.
	Temperature     = 0.2
	MaxOutputTokens = 4_000
	Reprompts       = 1
	// GenerationBudget bounds one generation: reads, model call and storage.
	GenerationBudget = 120 * time.Second
	// InputBudgetTokens is the estimated-token budget of the prompt (system plus user).
	InputBudgetTokens = 24_000
)

// Inputs (spec sections 7.10.1 and 7.10.2).
const (
	// RepoMapBudget is the token budget of the repo-map read.
	RepoMapBudget = 1_500
	// TopFilesLimit is the number of ranked files read and scanned for TODO lines.
	TopFilesLimit = 200
	// MaxBlobBytes is the size cap of one blob read.
	MaxBlobBytes = 524_288
	// MaxBlobReaders is the number of blob reads in flight per list.
	MaxBlobReaders = 8
)

// Limits of the tour (the vendored contract's constants/onboarding limits: maximums are
// enforced on read; minimums at normalisation, a section below its minimum is stored empty).
const (
	MaxSummary       = 800
	MinNodes         = 2
	MaxNodes         = 12
	MaxEdges         = 20
	MaxNodeID        = 64
	MaxNodeLabel     = 60
	MinCriticalPaths = 3
	MaxCriticalPaths = 6
	MinReadingPath   = 3
	MaxReadingPath   = 7
	MinRunSteps      = 1
	MaxRunSteps      = 8
	MaxCommand       = 200
	MaxComment       = 120
	MaxReason        = 120
	MaxFirstTasks    = 3
	MaxTaskTitle     = 80
	MaxPath          = 512
)

// NodeKind is the kind of an architecture node.
type NodeKind string

// The node kinds.
const (
	KindEntry    NodeKind = "entry"
	KindModule   NodeKind = "module"
	KindStore    NodeKind = "store"
	KindExternal NodeKind = "external"
)

// Complexity is the size of a first task.
type Complexity string

// The complexities.
const (
	ComplexityLow    Complexity = "low"
	ComplexityMedium Complexity = "medium"
	ComplexityHigh   Complexity = "high"
)

// StaleReason says why a stored tour is out of date.
type StaleReason string

// The stale reasons; the empty reason means not stale.
const (
	StaleNone          StaleReason = ""
	StaleIndexChanged  StaleReason = "index_changed"
	StalePromptChanged StaleReason = "prompt_changed"
)

// Repo is the repository of a generation.
type Repo struct {
	ID          string
	WorkspaceID string
	// Owner and Name are the repository's clone location, the pair the git readers take: for a
	// GitHub repository its owner and name, for a GitLab one the clone key (codehost.CloneKey) —
	// the store sets them, so the use case forwards them unchanged.
	Owner, Name string
	// FullName is "<owner>/<name>" as stored (GitLab: the project path).
	FullName      string
	DefaultBranch string
	// Provider ("github" | "gitlab"; "" reads as github), Host (the host key) and ProjectID (the
	// GitLab project id; 0 = none).
	Provider  string
	Host      string
	ProjectID int64
}

// IndexState is what a generation needs of the repository's index state. A repo without a state
// row, or one whose row cannot be read, is the synthesised degraded state: FilesIndexed 0 and an
// empty LastIndexedSHA.
type IndexState struct {
	FilesIndexed   int
	LastIndexedSHA string
}

// Ready reports whether a tour can be generated: files are indexed and the indexed commit is
// known.
func (s IndexState) Ready() bool { return s.FilesIndexed > 0 && s.LastIndexedSHA != "" }

// Node is a box of the architecture diagram.
type Node struct {
	ID, Label string
	Kind      NodeKind
}

// Edge is a directed edge between node ids.
type Edge struct{ From, To string }

// Architecture is the diagram with its summary. Nodes and Edges are empty (never nil) when fewer
// than MinNodes nodes survived normalisation.
type Architecture struct {
	Summary string
	Nodes   []Node
	Edges   []Edge
}

// PathItem is a file with the reason it matters (critical paths, reading path).
type PathItem struct{ Path, Reason string }

// RunStep is a command to run the project locally; Comment is "" when none.
type RunStep struct{ Command, Comment string }

// FirstTask is a starter task on an indexed file or folder.
type FirstTask struct {
	Title, Path string
	Complexity  Complexity
}

// Tour is the stored and returned onboarding tour. Every list is non-nil.
type Tour struct {
	RepoID string
	// GeneratedAt is stored as ISO-8601 UTC with milliseconds.
	GeneratedAt  time.Time
	IndexedSHA   string
	FilesIndexed int
	Provider     string
	Model        string
	TokensIn     int
	TokensOut    int
	// CostUSD is the call's total; nil when unpriced.
	CostUSD       *float64
	PromptVersion int
	// Language is always domain.Language.
	Language      string
	Architecture  Architecture
	CriticalPaths []PathItem
	RunSteps      []RunStep
	ReadingPath   []PathItem
	FirstTasks    []FirstTask
}

// State is the answer of a read or a generation: a nil Tour is {"status":"none"}; otherwise
// {"status":"ready"} with Stale and, when stale, StaleReason.
type State struct {
	Tour        *Tour
	Stale       bool
	StaleReason StaleReason
}

// LLMOutput is the model's answer to the OnboardingTour schema, before normalisation.
type LLMOutput struct {
	Architecture  LLMArchitecture
	CriticalPaths []LLMPath
	RunSteps      []LLMRunStep
	ReadingPath   []LLMPath
	FirstTasks    []LLMTask
}

// LLMArchitecture is the diagram as the model wrote it.
type LLMArchitecture struct {
	Summary string
	Nodes   []LLMNode
	Edges   []Edge
}

// LLMNode is a node as the model wrote it; Kind is one of the NodeKind values (the schema
// enforces it).
type LLMNode struct{ ID, Label, Kind string }

// LLMPath is a path with a reason as the model wrote it.
type LLMPath struct{ Path, Reason string }

// LLMRunStep is a run step as the model wrote it; Comment is "" when absent.
type LLMRunStep struct{ Command, Comment string }

// LLMTask is a first task as the model wrote it; Complexity is one of the Complexity values.
type LLMTask struct{ Title, Path, Complexity string }
