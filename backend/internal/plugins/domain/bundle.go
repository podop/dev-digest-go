// Package domain holds the rules of the plugins context: the plugin bundle (the
// devdigest-plugin/v1 document), the bounded reader of an uploaded .zip or .json, the bundle
// rules, the import preview and install plan, and the export. It imports only the standard
// library and no other domain: every limit it shares with the skills, agents and evals contexts
// is copied here and pinned by the drift test in cmd/server. The Bundle types carry json tags
// because they are the file format itself (like the workspace export); the HTTP bodies live in
// transport/http.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Bundle format and file name.
const (
	// BundleFormat is the envelope format; it is also the workspace export's.
	BundleFormat = "devdigest-plugin/v1"
	// KindPlugin and KindWorkspace are the manifest kinds; an absent kind means KindPlugin.
	KindPlugin    = "plugin"
	KindWorkspace = "workspace"
	// BundleFile is the one entry of a plugin archive.
	BundleFile = "devdigest-plugin.json"
	// TimeLayout is the wire timestamp layout: UTC with milliseconds.
	TimeLayout = "2006-01-02T15:04:05.000Z"
	// DefaultVersion is the manifest version when the server build has none.
	DefaultVersion = "dev"
	// LastExportKey is the Settings key that stores the last export (LastExport).
	LastExportKey = "plugin_last_export"
)

// Limits of the upload (spec section 7, D14).
const (
	// ArchiveMaxEntries is the most entries an archive may list.
	ArchiveMaxEntries = 500
	// ArchiveMaxZipBytes is the largest accepted .zip upload (the route's raw body limit).
	ArchiveMaxZipBytes = 5 << 20
	// MaxInflatedBytes is the most bytes ever inflated from an archive, and the largest accepted
	// application/json body and exported bundle: 20 MiB.
	MaxInflatedBytes = 20 << 20
)

// Limits of the bundle rules (spec section 7, D14).
const (
	MaxAgents    = 100
	MaxSkills    = 200
	MaxEvalCases = 1000
	MaxLearnings = 5000
	// MaxIssues is the most issues a rejected bundle lists.
	MaxIssues = 20

	PluginNameMax      = 80
	PluginVersionMax   = 64
	FileNameMax        = 255
	AgentNameMax       = 200
	AgentModelMax      = 200
	AgentPromptMax     = 100_000
	LearningContentMax = 20_000

	// ExportNameMax, ExportDescriptionMax, ExportAgentIDsMax and ExportSkillIDsMax bound the
	// POST /plugins/export body.
	ExportNameMax        = 64
	ExportDescriptionMax = 300
	ExportAgentIDsMax    = 200
	ExportSkillIDsMax    = 500
)

// Limits copied from other contexts. cmd/server's drift test compares each with its origin;
// lengths are UTF-16 code units, as the contracts count them.
const (
	// SkillNameMax, SkillDescriptionMax and SkillBodyMax are skills/domain's limits.
	SkillNameMax        = 64
	SkillDescriptionMax = 300
	SkillBodyMax        = 20_000
	// CaseNameMax, CasesPerOwnerMax and CaseDiffMax are evals/domain's CaseNameMax,
	// CasesPerOwnerMax and DiffMaxChars (SCR4).
	CaseNameMax      = 120
	CasesPerOwnerMax = 200
	CaseDiffMax      = 200_000
	// DefaultStrategy and DefaultCIFailOn are agents/domain's defaults (A3).
	DefaultStrategy = "single-pass"
	DefaultCIFailOn = "critical"
)

// The enums of the bundle (A2). cmd/server's drift test compares the first five with the
// agents, skills and evals contexts.
var (
	agentProviders  = []string{"openai", "anthropic", "openrouter"}
	agentStrategies = []string{"single-pass", "map-reduce", "auto"}
	agentCIFailOns  = []string{"never", "critical", "warning", "any"}
	skillTypes      = []string{"rubric", "convention", "security", "custom"}
	skillSources    = []string{"manual", "imported_file", "imported_url", "extracted", "community"}
	caseOwnerKinds  = []string{CaseOwnerSkill, CaseOwnerAgent}
	learningScopes  = []string{"repo", "global", "team"}
	learningKinds   = []string{"decision", "convention", "preference", "fact", "learning"}
)

// Eval case owner kinds.
const (
	CaseOwnerSkill = "skill"
	CaseOwnerAgent = "agent"
)

// AgentProviders, AgentStrategies, AgentCIFailOns, SkillTypes, SkillSources, CaseOwnerKinds,
// LearningScopes and LearningKinds list the accepted values, in contract order. Each call
// returns a copy.
func AgentProviders() []string  { return slices.Clone(agentProviders) }
func AgentStrategies() []string { return slices.Clone(agentStrategies) }
func AgentCIFailOns() []string  { return slices.Clone(agentCIFailOns) }
func SkillTypes() []string      { return slices.Clone(skillTypes) }
func SkillSources() []string    { return slices.Clone(skillSources) }
func CaseOwnerKinds() []string  { return slices.Clone(caseOwnerKinds) }
func LearningScopes() []string  { return slices.Clone(learningScopes) }
func LearningKinds() []string   { return slices.Clone(learningKinds) }

// Bundle is the plugin document: a superset of the workspace export's bundle. The plugin export
// leaves out versions, context_files and settings (omitempty) and writes conventions as [].
// Conventions, ContextFiles, skill Versions and Settings are never installed, so they stay raw.
type Bundle struct {
	Manifest     Manifest                   `json:"manifest"`
	Agents       []BundleAgent              `json:"agents"`
	Skills       []BundleSkill              `json:"skills"`
	EvalCases    []BundleCase               `json:"eval_cases"`
	Conventions  []json.RawMessage          `json:"conventions"`
	Learnings    []BundleLearning           `json:"learnings"`
	ContextFiles []json.RawMessage          `json:"context_files,omitempty"`
	Settings     map[string]json.RawMessage `json:"settings,omitempty"`
}

// Manifest describes the bundle. Kind is written by the export; an absent kind is read as
// KindPlugin.
type Manifest struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Format      string         `json:"format"`
	Kind        string         `json:"kind,omitempty"`
	ExportedAt  string         `json:"exported_at"`
	Description *string        `json:"description"`
	Counts      ManifestCounts `json:"counts"`
}

// ManifestCounts are the sizes of the bundle's lists. An import never trusts them: the preview
// and the install count the lists.
type ManifestCounts struct {
	Agents       int `json:"agents"`
	Skills       int `json:"skills"`
	EvalCases    int `json:"eval_cases"`
	Conventions  int `json:"conventions"`
	Learnings    int `json:"learnings"`
	ContextFiles int `json:"context_files,omitempty"`
}

// BundleAgent is an agent. Name is unique within the bundle; Skills name bundle skills in link
// order. OutputSchema nil is JSON null.
type BundleAgent struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Enabled      bool            `json:"enabled"`
	Skills       []string        `json:"skills"`
	Strategy     string          `json:"strategy"`
	CIFailOn     string          `json:"ci_fail_on"`
	RepoIntel    bool            `json:"repo_intel"`
}

// BundleSkill is a skill with its current body. Versions exists only in a workspace export and
// is ignored on import. EvidenceFiles nil is JSON null.
type BundleSkill struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Type          string            `json:"type"`
	Source        string            `json:"source"`
	Body          string            `json:"body"`
	Enabled       bool              `json:"enabled"`
	EvidenceFiles []string          `json:"evidence_files"`
	Versions      []json.RawMessage `json:"versions,omitempty"`
}

// BundleCase is an eval case; OwnerRef is the owning agent's bundle name or the skill's name.
// The JSON fields are null, an object or an array (A12); nil is JSON null.
type BundleCase struct {
	Name           string          `json:"name"`
	OwnerKind      string          `json:"owner_kind"`
	OwnerRef       string          `json:"owner_ref"`
	InputDiff      *string         `json:"input_diff"`
	InputFiles     json.RawMessage `json:"input_files"`
	InputMeta      json.RawMessage `json:"input_meta"`
	ExpectedOutput json.RawMessage `json:"expected_output"`
	Notes          *string         `json:"notes"`
}

// BundleLearning is a learning. Repo is the repository's full name or nil; an import ignores it.
type BundleLearning struct {
	Scope      string   `json:"scope"`
	Kind       string   `json:"kind"`
	Content    string   `json:"content"`
	Confidence *float64 `json:"confidence"`
	Repo       *string  `json:"repo"`
}

// Errors of the context. The service maps them to application errors; ErrInvalid and ErrConflict
// are matched with errors.Is against *InvalidError and *ConflictError.
var (
	// ErrInvalid means the upload is not an acceptable plugin (422 invalid_plugin).
	ErrInvalid = errors.New("invalid plugin")
	// ErrConflict means the plugin name or a skill name already exists (409 plugin_conflict).
	ErrConflict = errors.New("plugin conflict")
	// ErrNotFound means no plugin matches the id in the workspace (404 not_found).
	ErrNotFound = errors.New("plugin not found")
	// ErrConfirmationMismatch means the typed confirmation is not the plugin's name (422).
	ErrConfirmationMismatch = errors.New("confirmation does not match the plugin name")
	// ErrUnsupportedMedia means the media type is neither application/zip nor application/json.
	ErrUnsupportedMedia = errors.New("unsupported media type")
	// ErrExportTooLarge means the exported bundle would exceed MaxInflatedBytes (422 export_too_large).
	ErrExportTooLarge = errors.New("exported bundle is too large")
	// ErrInvalidName, ErrInvalidFileName and ErrInvalidExportName reject a plugin name, an upload
	// file name and an export name.
	ErrInvalidName       = errors.New("invalid plugin name")
	ErrInvalidFileName   = errors.New("invalid file name")
	ErrInvalidExportName = errors.New("invalid export name")
)

// InvalidError lists the broken rules of an upload, the first one first (at most MaxIssues).
// errors.Is(err, ErrInvalid) is true.
type InvalidError struct{ Issues []string }

// Error returns the first broken rule.
func (e *InvalidError) Error() string {
	if len(e.Issues) == 0 {
		return ErrInvalid.Error()
	}
	return e.Issues[0]
}

// Is makes errors.Is(err, ErrInvalid) true.
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

// Message is the first broken rule, the message of the 422.
func (e *InvalidError) Message() string { return e.Error() }

func invalid(format string, args ...any) *InvalidError {
	return &InvalidError{Issues: []string{fmt.Sprintf(format, args...)}}
}

// Conflicts say what already exists in the workspace: a plugin of the bundle's name and the
// bundle's skill names that are taken. Skills is never nil and keeps the bundle's order.
type Conflicts struct {
	Plugin bool
	Skills []string
}

// Any reports whether there is a conflict.
func (c Conflicts) Any() bool { return c.Plugin || len(c.Skills) > 0 }

// ConflictError is an install refused for conflicts (409 plugin_conflict). errors.Is(err,
// ErrConflict) is true.
type ConflictError struct{ Conflicts Conflicts }

// Error implements error.
func (e *ConflictError) Error() string {
	var parts []string
	if e.Conflicts.Plugin {
		parts = append(parts, "a plugin of this name is already installed")
	}
	if n := len(e.Conflicts.Skills); n > 0 {
		parts = append(parts, fmt.Sprintf("%d skill name(s) already exist", n))
	}
	if len(parts) == 0 {
		return ErrConflict.Error()
	}
	return strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrConflict) true.
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }
