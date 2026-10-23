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

// SlugOwner implements app.Store.
func (s *Store) SlugOwner(ctx context.Context, repo, slug string) (string, bool, error) {
	var agentID string
	err := s.conn(ctx).Table("ci_installations").Select("agent_id").
		Where("repo = ? AND slug = ?", repo, slug).Take(&agentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select slug owner: %w", err)
	}
	return agentID, true, nil
}

// UpsertInstallation implements app.Store. Only slug and manifest_version change on a repeated
// export; installed_at keeps the time of the first one. The statement runs in a savepoint when
// the caller is in a transaction, so a lost race on (repo, slug) does not abort it.
func (s *Store) UpsertInstallation(ctx context.Context, in domain.NewInstallation) error {
	if err := checkInstallation(in); err != nil {
		return err
	}
	row := installationInsertRow{
		AgentID:         in.AgentID,
		Repo:            in.Repo,
		TargetType:      domain.TargetGHA,
		Slug:            in.Slug,
		ManifestVersion: in.ManifestVersion,
	}
	err := s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "agent_id"}, {Name: "repo"}, {Name: "target_type"}},
			DoUpdates: clause.Assignments(map[string]any{
				"slug":             row.Slug,
				"manifest_version": row.ManifestVersion,
			}),
		}).Create(&row).Error
	})
	if isUniqueViolation(err) {
		// (repo, slug) belongs to another agent: the pre-check lost a race.
		return fmt.Errorf("upsert ci installation: %w", app.ErrSlugTaken)
	}
	if err != nil {
		return fmt.Errorf("upsert ci installation: %w", err)
	}
	return nil
}
