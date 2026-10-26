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
	ci_installations.repo AS repo,
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
	ci_runs.manifest_version AS manifest_version`

// workspaceRuns starts a query over the CI runs of a workspace: ci_runs joined to their agent run
// of the workspace (inner), the agent and the installation (outer; both may be gone).
func (s *Store) workspaceRuns(ctx context.Context, workspaceID string) *gorm.DB {
	return s.conn(ctx).Table("ci_runs").
		Joins("JOIN agent_runs ON agent_runs.id = ci_runs.agent_run_id").
		Joins("LEFT JOIN agents ON agents.id = agent_runs.agent_id").
		Joins("LEFT JOIN ci_installations ON ci_installations.id = ci_runs.ci_installation_id").
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

// ListInstallations implements app.Store.
func (s *Store) ListInstallations(ctx context.Context, workspaceID string, f app.InstallationFilter) ([]domain.Installation, error) {
	q := s.installations(ctx, workspaceID)
	if f.AgentID != "" {
		q = q.Where("ci_installations.agent_id = ?", f.AgentID)
	}
	return s.readInstallations(ctx, workspaceID, q)
}

// FindInstallation implements app.Store.
func (s *Store) FindInstallation(ctx context.Context, workspaceID, agentID, repo string) (domain.Installation, bool, error) {
	q := s.installations(ctx, workspaceID).
		Where("ci_installations.agent_id = ? AND ci_installations.repo = ?", agentID, repo)
	found, err := s.readInstallations(ctx, workspaceID, q)
	if err != nil || len(found) == 0 {
		return domain.Installation{}, false, err
	}
	return found[0], true, nil
}

// installations starts a query over the gha installations of the workspace's agents.
func (s *Store) installations(ctx context.Context, workspaceID string) *gorm.DB {
	return s.conn(ctx).Table("ci_installations").
		Joins("JOIN agents ON agents.id = ci_installations.agent_id").
		Where("agents.workspace_id = ? AND ci_installations.target_type = ?", workspaceID, domain.TargetGHA)
}

// readInstallations runs q newest installed_at first and attaches each installation's newest run.
func (s *Store) readInstallations(ctx context.Context, workspaceID string, q *gorm.DB) ([]domain.Installation, error) {
	var rows []installationRow
	err := q.Select("ci_installations.*").Order("ci_installations.installed_at DESC").Order("ci_installations.id").Find(&rows).Error
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

// repoInstallationRow is a gha installation with its agent's name.
type repoInstallationRow struct {
	ID          string    `gorm:"column:id"`
	AgentID     string    `gorm:"column:agent_id"`
	AgentName   string    `gorm:"column:agent_name"`
	Repo        string    `gorm:"column:repo"`
	Slug        *string   `gorm:"column:slug"`
	InstalledAt time.Time `gorm:"column:installed_at"`
}

// ListRepoInstallations implements app.RepoStore: two queries, the installations of the workspace's
// agents and the newest run of each. The order (repository, agent name in byte order, installed_at,
// id) is the order the domain keeps.
func (s *Store) ListRepoInstallations(ctx context.Context, workspaceID string) ([]domain.RepoInstallation, error) {
	var rows []repoInstallationRow
	err := s.installations(ctx, workspaceID).
		Select("ci_installations.id, ci_installations.agent_id, agents.name AS agent_name, ci_installations.repo, ci_installations.slug, ci_installations.installed_at").
		Order(`ci_installations.repo COLLATE "C"`).Order(`agents.name COLLATE "C"`).
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
			InstalledAt: r.InstalledAt.UTC(),
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
