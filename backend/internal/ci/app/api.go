package app

import (
	"context"

	"dev-digest-go/backend/internal/ci/domain"
)

// ExportRequest is a validated POST /agents/:id/export-ci body after the schema defaults:
// Action open_pr, PostAs github_review, Triggers opened, synchronize, reopened. Target has no
// default: omitted, the use case picks it by the repository's host (GitLab C FR2). Base of the body
// is dropped (the repository's default branch is used).
type ExportRequest struct {
	// Repo is the exact "owner/name".
	Repo string
	// RepoID is the UUID of a repository of the workspace; when set it wins and Repo is ignored
	// (GitLab C FR1); "" = resolve Repo.
	RepoID string
	// Target is "gha", "gitlab_ci" or "" (by the repository's host); any other value is 422
	// unsupported_target, checked by the use case.
	Target   string
	Action   domain.ExportAction
	PostAs   domain.PostAs
	Triggers []string
	// Workflow is the edited workflow; nil (absent or null) = generate it. An empty string is
	// text like any other and is refused by the workflow rules as invalid_yaml (Node `??`).
	Workflow *string
	// Runner is where the generated job runs (FR25): github_hosted or self_hosted; empty =
	// github_hosted. It only shapes the generated workflow; an edited Workflow is used as sent.
	Runner domain.Runner
}

// Exporter is the export use cases as transport sees them; *ExportService implements it. Failures
// are apperr errors that httpx renders; ids are UUIDs by the time they get here.
type Exporter interface {
	// Export runs the checks of spec 7.6.1 in order (404 agent_not_found, 409 plugin_disabled, 422
	// unsupported_target for a target other than gha / gitlab_ci,
	// 422 provider_not_configured for an undefined custom provider (every other provider is
	// exportable, FR24), 422 invalid_triggers, 404 repo_not_found, 422 repo_ambiguous, 422
	// unsupported_target for a target that does not fit the repository's host, 409
	// ci_slug_conflict, 422
	// skill_slug_conflict, 422 workflow_insecure, 500 runner_bundle_missing, 422
	// manifest_invalid), then answers the files (action files: no GitHub call, no write) or, for
	// open_pr, checks the token (500 config_error), publishes (422 github_permission_denied, 502
	// github_unavailable), upserts the installation with the agent's provider (409
	// ci_slug_conflict) and answers with it. Both actions answer RequiredSecrets and
	// RequiredVariables (FR29).
	//
	// A GitLab repository (target gitlab_ci, GitLab C FR3) follows the same order with 422
	// repo_host_not_connected right after the host match, the GitLab job file checked by the GitLab
	// rules, 500 config_error "GitLab connection <name> has no token" for open_pr without a token,
	// 422 gitlab_permission_denied / 502 gitlab_unavailable for publish failures, and
	// Export.GitLab set.
	Export(ctx context.Context, workspaceID, agentID string, req ExportRequest) (domain.Export, error)
	// ListInstallations returns the agent's installations, newest first, each with its last run;
	// 404 agent_not_found.
	ListInstallations(ctx context.Context, workspaceID, agentID string) ([]domain.Installation, error)
}

// Ingest is the CI run use cases as transport sees them; *IngestService implements it.
type Ingest interface {
	// ListRuns returns the workspace's CI runs, newest first; f.Limit is 1-200.
	ListRuns(ctx context.Context, workspaceID string, f RunFilter) ([]domain.Run, error)
	// Refresh reads the newest completed workflow runs of the workspace's installations (of one
	// agent when agentID is not empty) and stores the verified results (spec 7.7). 404
	// agent_not_found, 500 config_error without a token, 500 internal_error on a database
	// failure; GitHub failures are reported per installation, not as errors.
	Refresh(ctx context.Context, workspaceID, agentID string) (domain.RefreshResult, error)
}

// RepoInstallations is the per-repository installation listing as transport sees it;
// *InstallationsService implements it.
type RepoInstallations interface {
	// ListByRepo returns the workspace's gha installations grouped by repository (byte order of the
	// name), each group with the newest run of any of its installations; nil when there are none.
	ListByRepo(ctx context.Context, workspaceID string) ([]domain.RepoGroup, error)
}
