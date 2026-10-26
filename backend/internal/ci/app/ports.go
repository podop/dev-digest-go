// Package app holds the use cases of the ci context: exporting an agent to GitHub Actions
// (export.go) and ingesting the results of its workflow runs (ingest.go). Logs go to an injected
// *slog.Logger and carry ids, counts and enum labels only, never the token or artifact bytes.
//
// This file holds the ports the use cases depend on; api.go holds the use cases' own interfaces,
// which transport depends on.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/ci/domain"
)

// Clock returns the current time (injectable for tests and the refresh budget).
type Clock func() time.Time

// Sentinels of the ports.
var (
	// ErrSlugTaken is returned by Store.UpsertInstallation when (repo, slug) belongs to another
	// agent: a lost race against the pre-check.
	ErrSlugTaken = errors.New("ci slug taken")
	// ErrNotStorable is returned by Store.RecordRun and Store.UpsertInstallation, before any
	// write, when a value cannot be held by its column: an integer outside the column's range, a
	// cost that overflows numeric(12,6), or text with a NUL or invalid UTF-8. The ingest maps it
	// to the skip reason artifact_invalid, so a hostile artifact cannot make every refresh fail
	// with a database error.
	ErrNotStorable = errors.New("ci value not storable")
	// ErrArtifactTooLarge is returned by GitHub.DownloadArtifact when the body exceeds maxBytes.
	ErrArtifactTooLarge = errors.New("artifact too large")
	// ErrEntryTooLarge is returned by ArtifactReader.Entry when the entry is declared or inflates
	// larger than the limit.
	ErrEntryTooLarge = errors.New("zip entry too large")
	// ErrEntryInvalid is returned by ArtifactReader.Entry when the bytes are not a readable ZIP or
	// do not hold exactly one entry of that name.
	ErrEntryInvalid = errors.New("zip entry invalid")
)

// AgentDirectory reads the agents of the workspace (peer adapter over the agents store).
type AgentDirectory interface {
	// Get returns the agent with its enabled skills in link order; false = not an agent of the
	// workspace.
	Get(ctx context.Context, workspaceID, agentID string) (domain.ExportAgent, bool, error)
	// Exists reports whether the workspace has the agent.
	Exists(ctx context.Context, workspaceID, agentID string) (bool, error)
}

// RepoDirectory reads the repositories imported into the workspace (peer adapter).
type RepoDirectory interface {
	// FindByFullName matches the exact "owner/name"; false = not imported.
	FindByFullName(ctx context.Context, workspaceID, fullName string) (domain.ImportedRepo, bool, error)
}

// CustomExport is what an export needs of a defined custom provider (FR24, FR27, FR29): the
// definition without the base URL, the key and any header value, whether a key is stored, and the
// manual price of the agent's model.
type CustomExport struct {
	// Config is the manifest's view of the definition; len(Config.ExtraHeaders) is the header
	// count of the workflow and of the required secrets.
	Config domain.CustomProviderConfig
	// KeyConfigured reports whether the workspace stores a key for the provider (FR29: only then
	// is DEVDIGEST_<S>_API_KEY a required secret).
	KeyConfigured bool
	// Pricing is the manual price of the agent's model, nil when the model has none.
	Pricing *domain.Pricing
}

// CustomProviders reads the workspace's custom providers (peer adapter over the customproviders
// context). A nil port in ExportDeps makes every custom id undefined.
type CustomProviders interface {
	// ForExport returns the export view of the provider with the public id "custom:<slug>" for
	// the agent's model; false = not defined in the workspace.
	ForExport(ctx context.Context, workspaceID, providerID, model string) (CustomExport, bool, error)
}

// ModelPrices is the server price book as an export reads it (FR35). A nil port in ExportDeps
// means no built-in model has a price.
type ModelPrices interface {
	// PerM returns the price of an openai / anthropic model in USD per 1M input and output tokens
	// as of now; false = the book has no price for the model (the manifest then has no `pricing`
	// key and the export still succeeds).
	PerM(provider, model string) (domain.Pricing, bool)
}

// RunnerArtifact is the Go CI runner as the export ships it (spec 7.8.2).
type RunnerArtifact struct {
	// Encoded is the file content of RunnerPath: gzip of the executable, standard base64 on one
	// line, then "\n".
	Encoded string
	// SHA256 is the lowercase hex SHA-256 of the executable (not of Encoded).
	SHA256 string
	// BuildID is the source commit (12 hex) or "dev".
	BuildID string
}

// RunnerBundle is the runner embedded in the server binary (adapters/runnerbin).
type RunnerBundle interface {
	// Runner returns the encoded runner, computed once; false when the server was built without
	// one (export then answers 500 runner_bundle_missing).
	Runner() (RunnerArtifact, bool)
}

// InstallationFilter narrows Store.ListInstallations; the zero value matches every installation.
type InstallationFilter struct {
	AgentID string
}

// RunFilter narrows Store.ListRuns. Limit is already validated (1-200).
type RunFilter struct {
	AgentID string
	Limit   int
}

// Store persists installations and CI runs. Every method joins the transaction of its ctx when
// there is one. Failures are database errors; the use cases surface them as 500 internal_error.
type Store interface {
	// ListInstallations returns the workspace's gha installations of its agents, newest
	// installed_at first, each with its newest run (by ran_at) of the workspace.
	ListInstallations(ctx context.Context, workspaceID string, f InstallationFilter) ([]domain.Installation, error)
	// FindInstallation returns the agent's installation in repo with its newest run.
	FindInstallation(ctx context.Context, workspaceID, agentID, repo string) (domain.Installation, bool, error)
	// SlugOwner returns the agent that owns slug in repo; false when nobody does.
	SlugOwner(ctx context.Context, repo, slug string) (agentID string, found bool, err error)
	// UpsertInstallation inserts or updates on (agent_id, repo, target gha), setting slug,
	// manifest_version and provider (FR37: the agent's provider at export time, overwritten by a
	// re-export); a unique violation on (repo, slug) is ErrSlugTaken.
	UpsertInstallation(ctx context.Context, in domain.NewInstallation) error
	// StoredRunKeys returns which of keys are already stored.
	StoredRunKeys(ctx context.Context, keys []domain.RunKey) (map[domain.RunKey]bool, error)
	// RecordRun writes the ci_runs and agent_runs rows in one transaction with ON CONFLICT
	// (github_run_id, run_attempt) DO NOTHING; Stored is false when another refresh stored the run
	// first. It joins the transaction of ctx, so the caller can write more rows with it (the
	// ledger entry). ErrNotStorable (checked before anything is written) when a value cannot be
	// held.
	RecordRun(ctx context.Context, run domain.NewRun) (Recorded, error)
	// ListRuns returns the runs of the workspace, newest ran_at first, at most f.Limit.
	ListRuns(ctx context.Context, workspaceID string, f RunFilter) ([]domain.Run, error)
}

// Recorded is the outcome of Store.RecordRun.
type Recorded struct {
	// AgentRunID is the id of the agent_runs row written for the run; empty unless Stored.
	AgentRunID string
	// Stored is false when the run (same run id and attempt) was already stored.
	Stored bool
	// Provider is the provider the agent_runs row was stored with: the installation's stored
	// export-time provider read in the same transaction (FR37); empty unless Stored. The ledger
	// entry uses it, so both rows agree even when a re-export commits in between. Empty from a
	// store that does not report it: the ingest then uses the provider of the installation it
	// listed.
	Provider string
}

// CIRun is a newly stored CI run as the cost ledger needs it (spec FR6). It carries ids and the
// artifact's reported figures only, never the artifact itself.
type CIRun struct {
	WorkspaceID string
	// AgentRunID is the agent_runs row written for the run: the entry's informational reference.
	AgentRunID string
	// GithubRunID and Attempt identify the CI run; they make the entry's dedupe key.
	GithubRunID int64
	Attempt     int
	// OccurredAt is the run time the ingest stores for the run.
	OccurredAt time.Time
	Provider   string
	Model      *string
	// CostUSD is the artifact's reported total; nil when the artifact reports none (unknown).
	CostUSD *float64
}

// Ledger appends the cost ledger entry of a CI run. A nil Ledger in IngestDeps switches the ledger
// off. The append joins the transaction of ctx and an error aborts it; appending the same run
// again is a no-op.
type Ledger interface {
	RecordCIRun(ctx context.Context, run CIRun) error
}

// Tx runs fn in one database transaction: the stores and the Ledger called with the context fn
// receives join it, and an error from fn rolls everything back.
type Tx interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// RepoRef names a GitHub repository.
type RepoRef struct {
	Owner, Name string
}

// GitHubError is a failed GitHub call as the ports report it. Its text never carries the token,
// a response body or request details.
type GitHubError struct {
	// Status is the HTTP status; 0 for a failure without a response.
	Status int
	// RateLimited is true when the response carried x-ratelimit-remaining: 0.
	RateLimited bool
	// Transport is true for a failure before a response (dial, TLS, timeout, cancelled).
	Transport bool
	// Cause is the underlying error, already scrubbed of the token; may be nil.
	Cause error
}

func (e *GitHubError) Error() string {
	if e.Transport {
		return "github: transport error"
	}
	return fmt.Sprintf("github: HTTP %d", e.Status)
}

// Unwrap returns the cause.
func (e *GitHubError) Unwrap() error { return e.Cause }

// CommitFile is one file of a publish. AsBlob uploads Contents as a base64 blob first and
// references it by SHA (the runner file); other files go inline.
type CommitFile struct {
	Path, Contents string
	AsBlob         bool
}

// CommitFilesInput is one publish (spec 7.6.7): one commit on Branch, whose parent is the
// branch head when it exists, else the head of Base, with every file at mode 100644; then the
// branch ref is created or force-updated.
type CommitFilesInput struct {
	Branch, Base, Message string
	Files                 []CommitFile
}

// OpenPullRequestInput is a new pull request.
type OpenPullRequestInput struct {
	Title, Head, Base, Body string
}

// GitHub is the part of the GitHub REST API the ci use cases call (spec 7.9), over the strict
// client of platform/github: the token is read per call, nothing follows a redirect with the
// token, and every failure is a *GitHubError. Writes make one attempt each, except that
// CommitFiles retries its whole sequence once on a transient error.
type GitHub interface {
	// Configured reports whether a GitHub token is set; the use cases check it before the first
	// call (500 config_error). A token that vanishes between the check and a call surfaces as a
	// *GitHubError.
	Configured() bool
	// CommitFiles runs get ref, get commit, create blob/tree/commit and update or create ref.
	CommitFiles(ctx context.Context, repo RepoRef, in CommitFilesInput) error
	// FindOpenPR returns the URL of the first open pull request whose head is
	// "<owner>:<branch>"; false when there is none.
	FindOpenPR(ctx context.Context, repo RepoRef, branch string) (url string, found bool, err error)
	// OpenPullRequest creates a pull request and returns its html_url.
	OpenPullRequest(ctx context.Context, repo RepoRef, in OpenPullRequestInput) (url string, err error)
	// ListWorkflowRuns lists the completed pull_request runs of a workflow file, newest first, at
	// most perPage; a missing workflow file is a *GitHubError with Status 404.
	ListWorkflowRuns(ctx context.Context, repo RepoRef, workflowFile string, perPage int) ([]domain.WorkflowRun, error)
	// ListRunArtifacts returns the first page (domain.ArtifactsPerRun) of a run's artifacts.
	ListRunArtifacts(ctx context.Context, repo RepoRef, runID int64) ([]domain.RunArtifact, error)
	// DownloadArtifact returns the ZIP of an artifact: the redirect target is fetched without
	// credentials, and ErrArtifactTooLarge is returned when the body exceeds maxBytes (read with
	// a cap of maxBytes+1). An expired artifact is a *GitHubError with Status 410.
	DownloadArtifact(ctx context.Context, repo RepoRef, artifactID int64, maxBytes int64) ([]byte, error)
}

// ArtifactReader reads one entry out of a downloaded ZIP (adapters/zipentry). It never writes to
// disk and never inflates more than maxBytes+1 bytes.
type ArtifactReader interface {
	// Entry returns the bytes of the one entry called name. ErrEntryTooLarge when its declared
	// size or its inflated size exceeds maxBytes; ErrEntryInvalid when the ZIP is unreadable or
	// has no entry or more than one entry of that name.
	Entry(zip []byte, name string, maxBytes int64) ([]byte, error)
}
