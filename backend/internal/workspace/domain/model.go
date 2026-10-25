package domain

import (
	"encoding/json"
	"time"
)

// Stats are the counts of what a workspace holds (spec FR11).
type Stats struct {
	Agents    int64
	Skills    int64
	EvalCases int64
	Learnings int64
	Repos     int64
	Runs      int64
}

// Deleted are the counts a reset removed (spec section 7), plus the pull requests whose reviewed
// marker it cleared.
type Deleted struct {
	AgentRuns       int64
	RunTraces       int64
	Reviews         int64
	Findings        int64
	MultiAgentRuns  int64
	EvalSuiteRuns   int64
	EvalRuns        int64
	CIRuns          int64
	ComposedReviews int64
	Learnings       int64
	PRsUnmarked     int64
}

// ExportData is what the store reads for an export: the workspace's configuration records, each
// list in a stable order (agents in creation order). IDs are used only to link records inside
// the bundle (an eval case's owner); BuildBundle never writes them out. Nullable JSON columns
// are raw JSON, nil for SQL NULL.
type ExportData struct {
	Agents       []AgentRecord
	Skills       []SkillRecord
	EvalCases    []EvalCaseRecord
	Conventions  []ConventionRecord
	Learnings    []LearningRecord
	ContextFiles []ContextFileRecord
	// Settings are all stored settings of the workspace; BuildBundle keeps only the allow-list.
	Settings map[string]json.RawMessage
}

// AgentRecord is one agent. SkillNames are the linked skills' names in link order.
type AgentRecord struct {
	ID           string
	Name         string
	Description  string
	Provider     string
	Model        string
	SystemPrompt string
	OutputSchema json.RawMessage
	Enabled      bool
	Strategy     string
	CIFailOn     string
	RepoIntel    bool
	SkillNames   []string
}

// SkillRecord is one skill with all its versions.
type SkillRecord struct {
	ID            string
	Name          string
	Description   string
	Type          string
	Source        string
	Body          string
	Enabled       bool
	EvidenceFiles json.RawMessage
	Versions      []SkillVersionRecord
}

// SkillVersionRecord is one stored version of a skill.
type SkillVersionRecord struct {
	Version     int
	Body        string
	Description *string
	Message     *string
	CreatedAt   time.Time
}

// OwnerKind values of an eval case.
const (
	OwnerAgent = "agent"
	OwnerSkill = "skill"
)

// EvalCaseRecord is one eval case; OwnerID is the ID of an AgentRecord or SkillRecord by OwnerKind.
type EvalCaseRecord struct {
	Name           string
	OwnerKind      string
	OwnerID        string
	InputDiff      *string
	InputFiles     json.RawMessage
	InputMeta      json.RawMessage
	ExpectedOutput json.RawMessage
	Notes          *string
}

// ConventionRecord is one convention; the evidence fields come from its first evidence entry.
type ConventionRecord struct {
	Repo            string
	Rule            string
	Category        string
	Status          string
	Confidence      *float64
	EvidencePath    *string
	EvidenceSnippet *string
}

// LearningRecord is one memory row without its embedding and sources. Repo is the repository's
// full name, nil for a learning without one.
type LearningRecord struct {
	Scope      string
	Kind       string
	Content    string
	Confidence *float64
	Repo       *string
}

// ContextFileRecord is one project-context file of a repository (by full name).
type ContextFileRecord struct {
	Repo    string
	Path    string
	Content string
}
