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

// RepoProvider says which code host a repository lives on.
type RepoProvider string

// The providers of ExportRepo.
const (
	ProviderGitHub RepoProvider = "github"
	ProviderGitLab RepoProvider = "gitlab"
)

// RepoQuery is how an export names its repository (GitLab C FR1). A non-empty ID wins and FullName
// is then ignored.
type RepoQuery struct {
	// ID is the UUID of a repository of the workspace; "" = resolve FullName.
	ID string
	// FullName is the exact "owner/name" (a GitLab path keeps its groups in Owner).
	FullName string
}

// ExportRepo is a repository of the workspace as an export needs it, on either host. Its text
// forms carry no secret (the struct holds none).
type ExportRepo struct {
	ID       string
	Provider RepoProvider
	// Host is the host key ("github.com" for GitHub, the connection's host key for GitLab).
	Host                  string
	Owner, Name, FullName string
	DefaultBranch         string
	// ProjectID is the GitLab project id; 0 for GitHub.
	ProjectID int64
	// WebURL is the stored browser address of a GitLab repository ("" for GitHub). pr_url is built
	// from it, never from a response URL (FR12).
	WebURL string
	// ClonePath is the absolute directory of the local clone; "" = no clone yet.
	ClonePath string
}

// AmbiguousRepoError is returned by RepoDirectory.Find when FullName matches GitLab repositories
// on several hosts and no GitHub one (FR1): the export answers 422 repo_ambiguous with IDs.
type AmbiguousRepoError struct {
	// IDs are the repository ids of the candidates, in the directory's order.
	IDs []string
}

func (e *AmbiguousRepoError) Error() string { return "repository name exists on several hosts" }

// RepoDirectory reads the repositories imported into the workspace (peer adapter).
type RepoDirectory interface {
	// Find resolves q (FR1). ID present: that repository of the workspace, whatever its host;
	// false = not in the workspace. Otherwise FullName: a GitHub repository with that exact
	// full_name wins; else the one GitLab repository with it; several GitLab repositories on
	// different hosts are an *AmbiguousRepoError; none is false. A GitLab repository is returned
	// whether or not its host has a connection (the export asks GitLabConns).
	Find(ctx context.Context, workspaceID string, q RepoQuery) (ExportRepo, bool, error)
	// FollowGitLabPath stores the project's current path_with_namespace and the web address built
	// from the connection (A FR47: full_name, owner, name, web_url; the project id is the
	// identity) and returns the repository as stored afterwards. A path another repository of the
	// workspace already holds is not stored and the repository is returned unchanged. It refuses
	// (returns an error) a path that is not a plain GitLab path.
	FollowGitLabPath(ctx context.Context, workspaceID string, repo ExportRepo, pathWithNamespace string) (ExportRepo, error)
}

// GitLabConn is the connection of a GitLab host as an export needs it: no address, no token.
type GitLabConn struct {
	// Name is the connection's name (the text of the 500 config_error).
	Name string
	// HasToken reports whether a token is stored for the connection.
	HasToken bool
}

// GitLabConns finds the GitLab connection of a host key (A FR48). It reads local settings only and
// never calls GitLab.
type GitLabConns interface {
	// Resolve returns the connection whose host key is host; false = the host has none (the
	// export answers 422 repo_host_not_connected). An error is a failure to read the connections.
	Resolve(ctx context.Context, host string) (GitLabConn, bool, error)
}

// CloneFiles asks a repository's local clone about files (the files preview of a gitlab_ci
// export, FR5). It makes no network call.
type CloneFiles interface {
	// Exists reports whether the regular file at rel (slash-separated, relative to the clone
	// directory clonePath) exists; an unreadable or missing clone is false, nil. An error is
	// reserved for failures that make the answer unknown.
	Exists(ctx context.Context, clonePath, rel string) (bool, error)
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
	// ListInstallations returns the workspace's gha and gitlab_ci installations of its agents (GitLab rows carry
	// Host, ProjectID and the bound repository's current path), newest
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
	// FindGitLabInstallation returns the agent's gitlab_ci installation of the project (host key
	// and project id: the identity of a GitLab repository, FR13) with its newest run, with Host,
	// ProjectID and TargetType set.
	FindGitLabInstallation(ctx context.Context, workspaceID, agentID, host string, projectID int64) (domain.Installation, bool, error)
	// GitLabSlugOwner returns the agent that owns slug in the project (host key, project id);
	// false when nobody does.
	GitLabSlugOwner(ctx context.Context, host string, projectID int64, slug string) (agentID string, found bool, err error)
	// UpsertGitLabInstallation inserts or updates on (agent_id, host, project_id, target
	// gitlab_ci), setting repo (the current full_name), slug, manifest_version and provider; the
	// row always names its host and project id. A unique violation on (host, project_id, slug)
	// is ErrSlugTaken. ErrNotStorable as for UpsertInstallation.
	UpsertGitLabInstallation(ctx context.Context, in NewGitLabInstallation) error
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

// NewGitLabInstallation is the upsert of an installation on (agent, host, project id, target
// gitlab_ci) after an open_pr export (FR13).
type NewGitLabInstallation struct {
	AgentID string
	// Host is the host key of the repository; ProjectID is the GitLab project id.
	Host      string
	ProjectID int64
	// Repo is the repository's current full_name (path_with_namespace).
	Repo            string
	Slug            string
	ManifestVersion int
	// Provider is the agent's provider at export time (custom providers FR37).
	Provider string
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

// The steps of a GitLab publish, as GitLabError.Step names them (log field `step`).
const (
	StepProject  = "project"
	StepBranch   = "branch"
	StepFileMeta = "file_meta"
	StepCommit   = "commit"
	StepOpenMRs  = "open_merge_requests"
	StepCreateMR = "create_merge_request"
)

// GitLabError is a failed GitLab call as the ports report it. Its text never carries the token, a
// response body or request details.
type GitLabError struct {
	// Status is the HTTP status; 0 for a failure without a response.
	Status int
	// Transport is true for a failure before a response (dial, TLS, timeout, truncated body); a
	// cancelled context is not reported as Transport.
	Transport bool
	// Step is the publish step that failed (one of the Step constants).
	Step string
	// Cause is the underlying error, already scrubbed of the token; may be nil.
	Cause error
}

func (e *GitLabError) Error() string {
	if e.Transport {
		return "gitlab " + e.Step + ": transport error"
	}
	return fmt.Sprintf("gitlab %s: HTTP %d", e.Step, e.Status)
}

// Unwrap returns the cause.
func (e *GitLabError) Unwrap() error { return e.Cause }

// GitLabRef names a GitLab project by the host key of its connection and its project id. The
// adapter reads the connection (and its token) per call, so a rotated token applies to the next
// call; a connection or token that vanished since the export's check is a *GitLabError with
// Status 401 and no request is sent.
type GitLabRef struct {
	Host      string
	ProjectID int64
}

// GitLabProject is what an export reads of the project (FR11 step 1).
type GitLabProject struct {
	// PathWithNamespace is the project's current path.
	PathWithNamespace string
	// DefaultBranch is "" when GitLab reports none.
	DefaultBranch string
	// CIConfigPath is "" when the project uses the default `.gitlab-ci.yml`.
	CIConfigPath string
}

// GitLabBranch is a repository branch and the commit it points at.
type GitLabBranch struct {
	Name, HeadSHA string
}

// GitLabCommitAction is one file of a commit; Action is "create" or "update", the content is sent
// as text.
type GitLabCommitAction struct {
	Action, Path, Content string
}

// GitLabCommit is one Commits-API commit (FR11 step 5).
type GitLabCommit struct {
	// Branch is the branch to commit to.
	Branch string
	// StartBranch is the branch Branch is created from when it does not exist; "" when it exists.
	StartBranch string
	Message     string
	Actions     []GitLabCommitAction
}

// GitLabNewMR is a new merge request (FR12).
type GitLabNewMR struct {
	SourceBranch, TargetBranch, Title, Description string
	RemoveSourceBranch                             bool
}

// GitLabPublisher is the part of the GitLab REST API a gitlab_ci export calls (spec C FR11, FR12)
// over platform/gitlab: no redirect is followed, the token goes only to the connection's origin
// and every failure is a *GitLabError carrying the Step. Each method makes exactly one attempt
// (writes are never retried here); the use case repeats the whole sequence once on a transient
// failure (A4).
type GitLabPublisher interface {
	// Project reads GET /projects/:id.
	Project(ctx context.Context, ref GitLabRef) (GitLabProject, error)
	// Branch reads a branch; false = it does not exist.
	Branch(ctx context.Context, ref GitLabRef, name string) (GitLabBranch, bool, error)
	// FileSHA256 reads the lower-case hex SHA-256 of the content of path at gitRef (a branch name
	// or a commit SHA) from the headers of a HEAD request; false = the file (or the ref) is
	// absent.
	FileSHA256(ctx context.Context, ref GitLabRef, path, gitRef string) (sha256 string, found bool, err error)
	// Commit makes one commit through the Commits API.
	Commit(ctx context.Context, ref GitLabRef, in GitLabCommit) error
	// OpenMR returns the iid of the first open merge request from source to target; false = none.
	OpenMR(ctx context.Context, ref GitLabRef, source, target string) (iid int, found bool, err error)
	// CreateMR creates a merge request and returns its iid. A 409 (a merge request for the
	// branches opened meanwhile) is a *GitLabError with Status 409.
	CreateMR(ctx context.Context, ref GitLabRef, in GitLabNewMR) (iid int, err error)
}
