package postgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
)

// runColumns selects a ci_runs row with the columns of its agent run, agent and installation. ran_at
// and source exist on both ci_runs and agent_runs; the list reads the ci_runs ones.
const runColumns = `ci_runs.id AS id,
	ci_runs.ci_installation_id AS installation_id,
	agent_runs.agent_id AS agent_id,
	agents.name AS agent_name,
	COALESCE(repos.full_name, ci_installations.repo) AS repo,
	ci_runs.pr_number AS pr_number,
	ci_runs.ran_at AS ran_at,
	ci_runs.status AS status,
	ci_runs.findings_count AS findings_count,
	ci_runs.cost_usd AS cost_usd,
	ci_runs.github_url AS github_url,
	ci_runs.source AS source,
	agent_runs.duration_ms AS duration_ms,
	agent_runs.blockers AS blockers,
	ci_runs.head_sha AS head_sha,
	ci_runs.manifest_version AS manifest_version,
	ci_runs.host AS host,
	` + prURLColumn

const prURLColumn = `CASE WHEN ci_runs.source = 'gitlab_ci' AND ci_runs.pr_number IS NOT NULL
		AND repos.web_url IS NOT NULL AND gitlab_connections.id IS NOT NULL
		THEN rtrim(repos.web_url, '/') || '/-/merge_requests/' || ci_runs.pr_number::text
	END AS pr_url`

// repoJoin joins the repository a GitLab installation is bound to: the workspace's repo with the
// installation's host and project id (unique per workspace by repos_ws_host_project_uq). It never
// matches a GitHub installation (no project id). Its full_name is the project's current path and
// its web_url the project's address (FR37).
const repoJoin = `LEFT JOIN repos ON repos.workspace_id = agents.workspace_id AND repos.provider = 'gitlab'
	AND repos.host = ci_installations.host AND repos.project_id = ci_installations.project_id`

// workspaceRuns starts a query over the CI runs of a workspace: ci_runs joined to their agent run
// of the workspace (inner), the agent and the installation (outer; both may be gone), the bound
// repository of a GitLab installation and the connection of the run's host (both outer: the
// current path and pr_url, FR37). pr_url is built from the bound repository's stored web URL and the
// run's merge request number, never from an address GitLab returned; a GitLab run without a repo
// row, without a merge request number or of a host without a connection has none (EC34).
func (s *Store) workspaceRuns(ctx context.Context, workspaceID string) *gorm.DB {
	return s.conn(ctx).Table("ci_runs").
		Joins("JOIN agent_runs ON agent_runs.id = ci_runs.agent_run_id").
		Joins("LEFT JOIN agents ON agents.id = agent_runs.agent_id").
		Joins("LEFT JOIN ci_installations ON ci_installations.id = ci_runs.ci_installation_id").
		Joins(`LEFT JOIN repos ON repos.workspace_id = agent_runs.workspace_id AND repos.provider = 'gitlab'
			AND repos.host = ci_installations.host AND repos.project_id = ci_installations.project_id`).
		Joins("LEFT JOIN gitlab_connections ON gitlab_connections.host = ci_runs.host").
		Where("agent_runs.workspace_id = ?", workspaceID)
}

// ListRuns implements app.Store. ran_at DESC puts runs without a time first, as the Node store
// does.
func (s *Store) ListRuns(ctx context.Context, workspaceID string, f app.RunFilter) ([]domain.Run, error) {
	q := s.workspaceRuns(ctx, workspaceID).Select(runColumns)
	if f.AgentID != "" {
		q = q.Where("agent_runs.agent_id = ?", f.AgentID)
	}
	var rows []runRow
	if err := q.Order("ci_runs.ran_at DESC").Order("ci_runs.id").Limit(f.Limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select ci runs: %w", err)
	}
	out := make([]domain.Run, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// ListInstallations implements app.Store: the gha and gitlab_ci installations of the workspace's
// agents (FR37). A GitLab row carries the bound repository's current path in Repo when there is
// one. The GitHub refresh skips the gitlab_ci rows of this list (ListGitLabInstallations has them).
func (s *Store) ListInstallations(ctx context.Context, workspaceID string, f app.InstallationFilter) ([]domain.Installation, error) {
	q := s.installations(ctx, workspaceID, domain.TargetGHA, domain.TargetGitLabCI)
	if f.AgentID != "" {
		q = q.Where("ci_installations.agent_id = ?", f.AgentID)
	}
	return s.readInstallations(ctx, workspaceID, q, currentRepo)
}

// ListGitLabInstallations implements app.GitLabIngestStore: the gitlab_ci installations, Repo being
// the stored project path (the job address of a stored run is built from it, FR36).
func (s *Store) ListGitLabInstallations(ctx context.Context, workspaceID string, f app.InstallationFilter) ([]domain.Installation, error) {
	q := s.installations(ctx, workspaceID, domain.TargetGitLabCI)
	if f.AgentID != "" {
		q = q.Where("ci_installations.agent_id = ?", f.AgentID)
	}
	return s.readInstallations(ctx, workspaceID, q, storedRepo)
}

// FindInstallation implements app.Store: the agent's GitHub installation in repo.
func (s *Store) FindInstallation(ctx context.Context, workspaceID, agentID, repo string) (domain.Installation, bool, error) {
	q := s.installations(ctx, workspaceID, domain.TargetGHA).
		Where("ci_installations.host = ? AND ci_installations.agent_id = ? AND ci_installations.repo = ?", githubHost, agentID, repo)
	return s.findInstallation(ctx, workspaceID, q)
}

// FindGitLabInstallation implements app.Store: the agent's gitlab_ci installation of the project.
func (s *Store) FindGitLabInstallation(ctx context.Context, workspaceID, agentID, host string, projectID int64) (domain.Installation, bool, error) {
	q := s.installations(ctx, workspaceID, domain.TargetGitLabCI).
		Where("ci_installations.host = ? AND ci_installations.project_id = ? AND ci_installations.agent_id = ?", host, projectID, agentID)
	return s.findInstallation(ctx, workspaceID, q)
}

func (s *Store) findInstallation(ctx context.Context, workspaceID string, q *gorm.DB) (domain.Installation, bool, error) {
	found, err := s.readInstallations(ctx, workspaceID, q, storedRepo)
	if err != nil || len(found) == 0 {
		return domain.Installation{}, false, err
	}
	return found[0], true, nil
}

// The two ways a listing names an installation's repository.
const (
	// storedRepo is ci_installations.repo as written by the last export or ingest.
	storedRepo = "ci_installations.repo"
	// currentRepo is the full_name of the repository the installation is bound to (a renamed
	// GitLab project), the stored text when there is none (FR37).
	currentRepo = "COALESCE(repos.full_name, ci_installations.repo)"
)

// installations starts a query over the installations of the given targets of the workspace's
// agents, with the bound repository of a GitLab one (repoJoin).
func (s *Store) installations(ctx context.Context, workspaceID string, targets ...string) *gorm.DB {
	return s.conn(ctx).Table("ci_installations").
		Joins("JOIN agents ON agents.id = ci_installations.agent_id").
		Joins(repoJoin).
		Where("agents.workspace_id = ? AND ci_installations.target_type IN ?", workspaceID, targets)
}

// installationColumns selects a ci_installations row with repo as repoExpr.
func installationColumns(repoExpr string) string {
	return `ci_installations.id, ci_installations.agent_id, ` + repoExpr + ` AS repo, ci_installations.target_type,
	ci_installations.installed_at, ci_installations.slug, ci_installations.manifest_version,
	ci_installations.provider, ci_installations.host, ci_installations.project_id`
}

// readInstallations runs q newest installed_at first and attaches each installation's newest run.
func (s *Store) readInstallations(ctx context.Context, workspaceID string, q *gorm.DB, repoExpr string) ([]domain.Installation, error) {
	var rows []installationRow
	err := q.Select(installationColumns(repoExpr)).Order("ci_installations.installed_at DESC").Order("ci_installations.id").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select ci installations: %w", err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	last, err := s.lastRuns(ctx, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Installation, len(rows))
	for i, r := range rows {
		var run *domain.Run
		if v, ok := last[r.ID]; ok {
			run = &v
		}
		out[i] = r.toDomain(run)
	}
	return out, nil
}

// lastRuns returns the newest run (by ran_at) of the workspace for each installation id.
func (s *Store) lastRuns(ctx context.Context, workspaceID string, installationIDs []string) (map[string]domain.Run, error) {
	out := make(map[string]domain.Run, len(installationIDs))
	if len(installationIDs) == 0 {
		return out, nil
	}
	var rows []runRow
	err := s.workspaceRuns(ctx, workspaceID).
		Select("DISTINCT ON (ci_runs.ci_installation_id) "+runColumns).
		Where("ci_runs.ci_installation_id IN ?", installationIDs).
		Order("ci_runs.ci_installation_id").Order("ci_runs.ran_at DESC").Order("ci_runs.id").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select last ci runs: %w", err)
	}
	for _, r := range rows {
		if r.InstallationID != nil {
			out[*r.InstallationID] = r.toDomain()
		}
	}
	return out, nil
}

var _ app.RepoStore = (*Store)(nil)

// repoInstallationRow is a gha or gitlab_ci installation with its agent's name.
type repoInstallationRow struct {
	ID          string    `gorm:"column:id"`
	AgentID     string    `gorm:"column:agent_id"`
	AgentName   string    `gorm:"column:agent_name"`
	Repo        string    `gorm:"column:repo"`
	Slug        *string   `gorm:"column:slug"`
	InstalledAt time.Time `gorm:"column:installed_at"`
	TargetType  string    `gorm:"column:target_type"`
	Host        string    `gorm:"column:host"`
	ProjectID   *int64    `gorm:"column:project_id"`
}

// ListRepoInstallations implements app.RepoStore: two queries, the gha and gitlab_ci installations
// of the workspace's agents (a GitLab one under the bound repository's current path) and the newest
// run of each. The order (repository, host, agent name in byte order, installed_at, id) is the
// order the domain keeps.
func (s *Store) ListRepoInstallations(ctx context.Context, workspaceID string) ([]domain.RepoInstallation, error) {
	var rows []repoInstallationRow
	err := s.installations(ctx, workspaceID, domain.TargetGHA, domain.TargetGitLabCI).
		Select(`ci_installations.id, ci_installations.agent_id, agents.name AS agent_name, ` + currentRepo + ` AS repo,
			ci_installations.slug, ci_installations.installed_at, ci_installations.target_type, ci_installations.host,
			ci_installations.project_id`).
		Order(currentRepo + ` COLLATE "C"`).Order(`ci_installations.host COLLATE "C"`).Order(`agents.name COLLATE "C"`).
		Order("ci_installations.installed_at").Order("ci_installations.id").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select ci installations by repo: %w", err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	newest, err := s.newestRuns(ctx, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RepoInstallation, len(rows))
	for i, r := range rows {
		out[i] = domain.RepoInstallation{
			ID: r.ID, AgentID: r.AgentID, AgentName: r.AgentName, Repo: r.Repo, Slug: r.Slug,
			InstalledAt: r.InstalledAt.UTC(), TargetType: r.TargetType, Host: r.Host,
		}
		if r.ProjectID != nil {
			out[i].ProjectID = *r.ProjectID
		}
		if run, ok := newest[r.ID]; ok {
			out[i].NewestRun = &run
		}
	}
	return out, nil
}

// newestRuns is lastRuns with runs without a ran_at last: the repository listing wants the newest
// dated run.
func (s *Store) newestRuns(ctx context.Context, workspaceID string, installationIDs []string) (map[string]domain.Run, error) {
	out := make(map[string]domain.Run, len(installationIDs))
	if len(installationIDs) == 0 {
		return out, nil
	}
	var rows []runRow
	err := s.workspaceRuns(ctx, workspaceID).
		Select("DISTINCT ON (ci_runs.ci_installation_id) "+runColumns).
		Where("ci_runs.ci_installation_id IN ?", installationIDs).
		Order("ci_runs.ci_installation_id").Order("ci_runs.ran_at DESC NULLS LAST").Order("ci_runs.id").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select newest ci runs: %w", err)
	}
	for _, r := range rows {
		if r.InstallationID != nil {
			out[*r.InstallationID] = r.toDomain()
		}
	}
	return out, nil
}
