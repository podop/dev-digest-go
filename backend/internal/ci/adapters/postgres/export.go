package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
)

// The two partial unique indexes of ci_installations (migration 0012): an ON CONFLICT on a partial
// index must repeat its predicate, or PostgreSQL finds no arbiter index (42P10).
var (
	notGitLabCI = clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "target_type <> 'gitlab_ci'"}}}
	onlyGitLab  = clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "target_type = 'gitlab_ci'"}}}
)

// SlugOwner implements app.Store: the owner of slug among the GitHub installations of repo.
func (s *Store) SlugOwner(ctx context.Context, repo, slug string) (string, bool, error) {
	var agentID string
	err := s.conn(ctx).Table("ci_installations").Select("agent_id").
		Where("host = ? AND repo = ? AND slug = ?", githubHost, repo, slug).Take(&agentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select slug owner: %w", err)
	}
	return agentID, true, nil
}

// UpsertInstallation implements app.Store. Only slug, manifest_version and provider change on a
// repeated export (the provider is the agent's provider at that export, FR37); installed_at keeps
// the time of the first one. The statement runs in a savepoint when
// the caller is in a transaction, so a lost race on (host, repo, slug) does not abort it.
func (s *Store) UpsertInstallation(ctx context.Context, in domain.NewInstallation) error {
	if err := checkInstallation(in); err != nil {
		return err
	}
	row := installationInsertRow{
		AgentID:         in.AgentID,
		Host:            githubHost,
		Repo:            in.Repo,
		TargetType:      domain.TargetGHA,
		Slug:            in.Slug,
		ManifestVersion: in.ManifestVersion,
		Provider:        in.Provider,
	}
	err := s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:     []clause.Column{{Name: "agent_id"}, {Name: "host"}, {Name: "repo"}, {Name: "target_type"}},
			TargetWhere: notGitLabCI,
			DoUpdates: clause.Assignments(map[string]any{
				"slug":             row.Slug,
				"manifest_version": row.ManifestVersion,
				"provider":         row.Provider,
			}),
		}).Create(&row).Error
	})
	if isUniqueViolation(err) {
		// (host, repo, slug) belongs to another agent: the pre-check lost a race.
		return fmt.Errorf("upsert ci installation: %w", app.ErrSlugTaken)
	}
	if err != nil {
		return fmt.Errorf("upsert ci installation: %w", err)
	}
	return nil
}

// GitLabSlugOwner implements app.Store: the owner of slug among the gitlab_ci installations of the
// project (host key, project id).
func (s *Store) GitLabSlugOwner(ctx context.Context, host string, projectID int64, slug string) (string, bool, error) {
	var agentID string
	err := s.conn(ctx).Table("ci_installations").Select("agent_id").
		Where("host = ? AND project_id = ? AND target_type = ? AND slug = ?", host, projectID, domain.TargetGitLabCI, slug).
		Take(&agentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select gitlab slug owner: %w", err)
	}
	return agentID, true, nil
}

// UpsertGitLabInstallation implements app.Store. The identity is (agent, host, project id, target
// gitlab_ci); a repeated export sets repo (the project's current path, FR13), slug, manifest_version
// and provider and keeps installed_at and the row's host and project id. A unique violation on
// (host, project_id, slug) is ErrSlugTaken. Like UpsertInstallation it runs in a savepoint.
func (s *Store) UpsertGitLabInstallation(ctx context.Context, in app.NewGitLabInstallation) error {
	if err := checkGitLabInstallation(in); err != nil {
		return err
	}
	row := installationInsertRow{
		AgentID:         in.AgentID,
		Host:            in.Host,
		ProjectID:       &in.ProjectID,
		Repo:            in.Repo,
		TargetType:      domain.TargetGitLabCI,
		Slug:            in.Slug,
		ManifestVersion: in.ManifestVersion,
		Provider:        in.Provider,
	}
	err := s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:     []clause.Column{{Name: "agent_id"}, {Name: "host"}, {Name: "project_id"}, {Name: "target_type"}},
			TargetWhere: onlyGitLab,
			DoUpdates: clause.Assignments(map[string]any{
				"repo":             row.Repo,
				"slug":             row.Slug,
				"manifest_version": row.ManifestVersion,
				"provider":         row.Provider,
			}),
		}).Create(&row).Error
	})
	if isUniqueViolation(err) {
		// (host, project_id, slug) belongs to another agent: the pre-check lost a race.
		return fmt.Errorf("upsert gitlab ci installation: %w", app.ErrSlugTaken)
	}
	if err != nil {
		return fmt.Errorf("upsert gitlab ci installation: %w", err)
	}
	return nil
}
