// Package domain is the vocabulary of the ci context (Phase 6): the export of an agent to GitHub
// Actions, the installations that export creates and the CI runs the refresh ingests back. It
// imports only the standard library and carries no JSON or database tags: the wire shapes live in
// transport and the row shapes in adapters/postgres.
//
// This file fixes the shared types, enums and constants of spec sections 7.2-7.9. The pure rules
// are added next to it by later steps and only use what is declared here: slug (slug.go),
// manifest (manifest.go), workflow and PR body (workflow.go, prbody.go), the runner encoding
// (runner.go), the result artifact and run view (artifact.go, ingest.go), GitHub error codes
// (ghcodes.go) and the workflow rules (wfrules). The parsed CiResultArtifact type is declared by
// artifact.go, not here.
package domain

import "time"

// Names and locations written into the target repository (spec 7.6.3, 7.6.7).
const (
	AgentsDir     = ".devdigest/agents"
	SkillsDir     = ".devdigest/skills"
	MemoryPath    = ".devdigest/memory.jsonl"
	RunnerPath    = ".devdigest/runner/devdigest-runner-linux-amd64.gz.b64"
	RunnerSumPath = ".devdigest/runner/devdigest-runner-linux-amd64.sha256"
	WorkflowsDir  = ".github/workflows"

	// Branch is the branch every export commits to; the PR title and the commit message are the
	// same text.
	Branch        = "devdigest/ci"
	PRTitle       = "Add DevDigest CI review"
	CommitMessage = "Add DevDigest CI review"

	// ResultFileName is the one entry of an artifact ZIP the ingest reads.
	ResultFileName = "devdigest-result.json"
)

// Slugs (spec 7.6.2).
const (
	SlugMaxLength     = 40
	AgentSlugFallback = "agent"
	SkillSlugFallback = "skill"
)

// Limits (spec 7.6.6, 7.7.3, 7.7.4, 7.7.1).
const (
	// MaxWorkflowBytes is the largest workflow checkWorkflow parses (UTF-8 bytes).
	MaxWorkflowBytes = 65_536
	// MaxArtifactZipBytes is the largest artifact ZIP downloaded or opened.
	MaxArtifactZipBytes = 1_048_576
	// MaxArtifactJSONBytes is the largest devdigest-result.json inflated.
	MaxArtifactJSONBytes = 65_536
	// RunsPerInstallation is the page size of the workflow-run list.
	RunsPerInstallation = 30
	// ArtifactsPerRun is the page size of the artifact list of one run.
	ArtifactsPerRun = 100
	// RefreshBudget is the time a refresh may spend before it stops starting GitHub calls.
	RefreshBudget = 45 * time.Second
	// GitHubCallTimeout bounds each GitHub call of a refresh (it starts before RefreshBudget ends).
	GitHubCallTimeout = 30 * time.Second
)

// TargetGHA is the only CI target that can be exported.
const TargetGHA = "gha"

// ProviderOpenRouter is the provider of the Phase 6 export and of every installation that predates
// the stored export-time provider. The other exportable providers are in providers.go.
const ProviderOpenRouter = "openrouter"

// Trigger is a pull_request activity type a workflow reacts to.
type Trigger string

// The allowed triggers, in the order a normalised list keeps (spec 7.6.1).
const (
	TriggerOpened      Trigger = "opened"
	TriggerSynchronize Trigger = "synchronize"
	TriggerReopened    Trigger = "reopened"
)

// Triggers lists the allowed triggers in normal order.
func Triggers() []Trigger {
	return []Trigger{TriggerOpened, TriggerSynchronize, TriggerReopened}
}

// PostAs says how the runner reports its review.
type PostAs string

// The posting modes (spec 7.6.5, 7.8.9).
const (
	PostAsGitHubReview PostAs = "github_review"
	PostAsPRComment    PostAs = "pr_comment"
	PostAsNone         PostAs = "none"
)

// ExportAction says what an export does with the generated files.
type ExportAction string

// The export actions.
const (
	ActionOpenPR ExportAction = "open_pr"
	ActionFiles  ExportAction = "files"
)

// ExportAgent is an agent as export sees it: the manifest source plus its linked and enabled
// skills in link order. Provider, Strategy and CIFailOn are the stored strings; the manifest
// validation (manifest.go) rejects values outside their enums.
type ExportAgent struct {
	ID, Name     string
	Provider     string
	Model        string
	SystemPrompt string
	Strategy     string
	CIFailOn     string
	// Version is the agent's current version, the deployed manifest version.
	Version int
	Skills  []ExportSkill
	// Plugin is the installed plugin that owns the agent; nil for a user-created agent.
	Plugin *PluginRef
	// Custom is the definition of the agent's custom provider (FR27); set only when Provider is
	// "custom:<slug>". It never holds the base URL, the key or a header value.
	Custom *CustomProviderConfig
	// Pricing is the price of the agent's model: the manual price of a custom provider's model, or
	// the price book's price of an openai / anthropic model at export time (FR27, FR35). Nil when
	// the model has none. It is ignored for openrouter.
	Pricing *Pricing
}

// Pricing is the price of a model in USD per 1M input and output tokens (the manifest key
// `pricing`, FR27, FR35).
type Pricing struct {
	PromptPerM, CompletionPerM float64
}

// CustomProviderConfig is what the manifest carries of a custom provider (FR27).
type CustomProviderConfig struct {
	// Protocol is "openai" or "anthropic"; AuthMode is "bearer" or "header".
	Protocol, AuthMode string
	// AuthHeader names the credential header of the header auth mode; nil for bearer.
	AuthHeader *string
	// ExtraHeaders are the extra header names in order, at most MaxExtraHeaders.
	ExtraHeaders []string
	// StructuredOutput is "strict", "json_object" or "prompt_only".
	StructuredOutput string
}

// PluginRef names the plugin that owns an exported agent and whether it is enabled.
type PluginRef struct {
	Name    string
	Enabled bool
}

// ExportSkill is a skill linked to an exported agent.
type ExportSkill struct {
	Name, Body string
}

// ImportedRepo is a repository imported into the workspace.
type ImportedRepo struct {
	Owner, Name, FullName, DefaultBranch string
}

// File is one generated file of an export.
type File struct {
	Path, Contents string
	Editable       bool
}

// Export is the result of an export. Installation and PRURL are nil for the files action.
type Export struct {
	Installation *Installation
	Files        []File
	PRURL        *string
	// RequiredSecrets and RequiredVariables are the repository secrets and variables the user
	// must add (FR29); see RequiredSecrets and RequiredVariables.
	RequiredSecrets, RequiredVariables []string
	// GitLab is the gitlab_ci object of the answer (spec C 7.1); nil for a gha export.
	GitLab *GitLabExport
}

// GitLabExport is what a gitlab_ci export tells besides its files (spec C 7.1 `gitlab_ci`).
type GitLabExport struct {
	// JobFile is the path of the job file, GitLabJobPath(slug).
	JobFile string
	// RootConfig is what the export does about the root CI configuration.
	RootConfig RootConfig
	// ConfigPath is ".gitlab-ci.yml" or the project's ci_config_path.
	ConfigPath string
	// IncludeSnippet is IncludeSnippet(slug).
	IncludeSnippet string
	// TriggersNote is the FR4 note; "" is sent as null.
	TriggersNote string
}

// ManifestIssue is one validation problem of a manifest (details.issues of manifest_invalid).
type ManifestIssue struct {
	Path, Message string
}

// CI run statuses stored in ci_runs.status (spec 7.7.5).
const (
	RunSucceeded  = "succeeded"
	RunNoFindings = "no_findings"
	RunFailed     = "failed"
	RunRunning    = "running"
)

// RunSource is ci_runs.source of an ingested run.
const RunSource = "gha"

// Installation is a persisted CI installation with its newest run. The workflow path and the
// wire shape are derived from Slug by the view code, not stored.
type Installation struct {
	ID, AgentID, Repo, TargetType string
	InstalledAt                   time.Time
	// Slug and ManifestVersion are nil for rows that predate the slug.
	Slug            *string
	ManifestVersion *int
	// Provider is the agent's provider at the last open_pr export (FR37): a built-in id or
	// "custom:<slug>". The ingest records its runs with it.
	Provider string
	LastRun  *Run
	// Host is the host key of the repository ("github.com" for GitHub); "" = a store that does not
	// report it (the wire then sends "github.com"). ProjectID is the GitLab project id, 0 for a
	// GitHub installation (spec C FR13).
	Host      string
	ProjectID int64
}

// Run is a ci_runs row joined to its agent run, installation and agent, as the lists read it.
// Every field except ID may be absent (the columns are nullable). Repo is the installation's
// repository; the view code falls back to the one in GithubURL.
type Run struct {
	ID              string
	InstallationID  *string
	PRNumber        *int
	RanAt           *time.Time
	Status          *string
	FindingsCount   *int
	CostUSD         *float64
	GithubURL       *string
	Source          *string
	AgentName       *string
	DurationMS      *int
	AgentID         *string
	Repo            *string
	Blockers        *int
	HeadSHA         *string
	ManifestVersion *int
	// Host is the host key of the run's repository ("" = github.com); PRURL is the merge request
	// link of a GitLab run (spec C FR37), nil for GitHub and for a host that is not connected.
	Host  string
	PRURL *string
}

// NewInstallation is the upsert of an installation on (agent, repo, target gha).
type NewInstallation struct {
	AgentID, Repo, Slug string
	ManifestVersion     int
	// Provider is the agent's provider at export time (FR37); a repeated export overwrites it.
	Provider string
}

// RunKey identifies a workflow run attempt on GitHub.
type RunKey struct {
	GithubRunID int64
	Attempt     int
}

// NewRun is one verified workflow run ready to be stored (spec 7.7.5): the ci_runs row and the
// agent_runs row it links to are written in one transaction.
type NewRun struct {
	WorkspaceID, InstallationID, AgentID string
	GithubRunID                          int64
	RunAttempt                           int
	HeadSHA, GithubURL                   string
	RanAt                                time.Time
	PRNumber, ManifestVersion            *int
	// Status is RunSucceeded, RunNoFindings or RunFailed.
	Status        string
	Model         *string
	CostUSD       *float64
	DurationMS    *int
	FindingsCount int
	Blockers      int
	// Error is the failure note of a RunFailed run.
	Error *string
	// FromArtifact is true when the run was built from a verified result artifact: only such a
	// run is a billable call that belongs in the cost ledger (a run without an artifact reports
	// no cost and appends no entry).
	FromArtifact bool
}

// WorkflowRun is a completed workflow run as GitHub lists it (spec 7.9). CreatedAt is the raw
// timestamp text: the ingest falls back to the current time when it does not parse.
type WorkflowRun struct {
	ID           int64
	RunAttempt   int
	HeadSHA      string
	Path         string
	Event        string
	Conclusion   *string
	HTMLURL      string
	RepositoryID int64
	// HeadRepositoryID is the repository of the run's head commit; 0 when GitHub names none.
	HeadRepositoryID int64
	// PRNumbers are the pull requests of the run in GitHub's order.
	PRNumbers []int
	CreatedAt string
}

// RunArtifact is an artifact of a workflow run as GitHub lists it. CreatedAt is raw text.
type RunArtifact struct {
	ID          int64
	Name        string
	SizeInBytes int64
	Expired     bool
	CreatedAt   string
}

// RefreshState is the outcome of one installation in a refresh.
type RefreshState string

// The refresh states (spec 7.3, 7.7.1).
const (
	StateOK           RefreshState = "ok"
	StateNotInstalled RefreshState = "not_installed"
	StateError        RefreshState = "error"
	StateNotChecked   RefreshState = "not_checked"
)

// ErrorCode is the error_code of an installation in state error.
type ErrorCode string

// The GitHub error codes (spec 7.7.1).
const (
	CodePermissionDenied ErrorCode = "github_permission_denied"
	CodeRateLimited      ErrorCode = "github_rate_limited"
	CodeUnavailable      ErrorCode = "github_unavailable"
)

// SkipReason says why a workflow run was not stored.
type SkipReason string

// The skip reasons (spec 7.7.3, 7.7.4).
const (
	SkipExpired  SkipReason = "artifact_expired"
	SkipTooLarge SkipReason = "artifact_too_large"
	SkipInvalid  SkipReason = "artifact_invalid"
	SkipMismatch SkipReason = "artifact_mismatch"
)

// SkippedRun is a workflow run the refresh inspected and did not store.
type SkippedRun struct {
	GithubRunID int64
	Reason      SkipReason
}

// InstallationResult is the refresh outcome of one installation. ErrorCode is empty unless State
// is StateError.
type InstallationResult struct {
	InstallationID, Repo string
	// Host is the host key of the installation's repository (spec C 7.1).
	Host      string
	State     RefreshState
	ErrorCode ErrorCode
	Ingested  int
	Skipped   []SkippedRun
}

// RefreshResult is the outcome of a refresh: Ingested is the sum over the installations and
// Degraded is true when any is in state error or not_checked.
type RefreshResult struct {
	Ingested      int
	Degraded      bool
	Installations []InstallationResult
}
