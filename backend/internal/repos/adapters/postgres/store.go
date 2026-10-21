// Package postgres persists repos with GORM.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

// repoRow maps the repos table. created_at keeps its database default and is only used for
// ordering.
type repoRow struct {
	ID            string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID   string     `gorm:"column:workspace_id"`
	Owner         string     `gorm:"column:owner"`
	Name          string     `gorm:"column:name"`
	FullName      string     `gorm:"column:full_name"`
	DefaultBranch string     `gorm:"column:default_branch"`
	ClonePath     *string    `gorm:"column:clone_path"`
	LastPolledAt  *time.Time `gorm:"column:last_polled_at"`
	CreatedBy     *string    `gorm:"column:created_by"`
}

func (repoRow) TableName() string { return "repos" }

func (r repoRow) toDomain() domain.Repo {
	return domain.Repo{
		ID:            r.ID,
		WorkspaceID:   r.WorkspaceID,
		Owner:         r.Owner,
		Name:          r.Name,
		FullName:      r.FullName,
		DefaultBranch: r.DefaultBranch,
		ClonePath:     r.ClonePath,
		LastPolledAt:  r.LastPolledAt,
		CreatedBy:     r.CreatedBy,
	}
}

// Store is the GORM-backed repo store. It implements the stores of the app package.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// Add inserts the repo with ON CONFLICT (workspace_id, full_name) DO NOTHING and reselects the
// row. The insert reports whether this call created it, so of N concurrent adds of one
// full name exactly one sees created=true: a concurrent insert blocks on the unique index
// until the other transaction commits, then does nothing.
func (s *Store) Add(ctx context.Context, in app.NewRepo) (domain.Repo, bool, error) {
	db := s.db.WithContext(ctx)
	row := repoRow{
		WorkspaceID:   in.WorkspaceID,
		Owner:         in.Ref.Owner,
		Name:          in.Ref.Name,
		FullName:      in.Ref.FullName(),
		DefaultBranch: domain.DefaultBranch,
	}
	if in.CreatedBy != "" {
		row.CreatedBy = &in.CreatedBy
	}
	res := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "workspace_id"}, {Name: "full_name"}},
		DoNothing: true,
	}).Omit("id").Create(&row)
	if res.Error != nil {
		return domain.Repo{}, false, fmt.Errorf("insert repo: %w", res.Error)
	}
	created := res.RowsAffected == 1

	var got repoRow
	if err := db.Where("workspace_id = ? AND full_name = ?", in.WorkspaceID, row.FullName).Take(&got).Error; err != nil {
		return domain.Repo{}, false, fmt.Errorf("reselect repo: %w", err)
	}
	return got.toDomain(), created, nil
}

// List returns the workspace's repos by creation time ascending.
func (s *Store) List(ctx context.Context, workspaceID string) ([]domain.Repo, error) {
	var rows []repoRow
	if err := s.db.WithContext(ctx).Where("workspace_id = ?", workspaceID).
		Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select repos: %w", err)
	}
	repos := make([]domain.Repo, len(rows))
	for i, r := range rows {
		repos[i] = r.toDomain()
	}
	return repos, nil
}

// Get returns the repo of the workspace, or domain.ErrNotFound.
func (s *Store) Get(ctx context.Context, workspaceID, id string) (domain.Repo, error) {
	var row repoRow
	err := s.db.WithContext(ctx).Where("workspace_id = ? AND id = ?", workspaceID, id).Take(&row).Error
	return rowOrNotFound(row, err)
}

// GetByID returns the repo regardless of workspace, or domain.ErrNotFound. It is for job
// handlers, whose payload comes from our own queue.
func (s *Store) GetByID(ctx context.Context, id string) (domain.Repo, error) {
	var row repoRow
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	return rowOrNotFound(row, err)
}

// Delete removes the repo of the workspace; foreign keys cascade to its dependents.
func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	res := s.db.WithContext(ctx).Where("workspace_id = ? AND id = ?", workspaceID, id).Delete(&repoRow{})
	return affected(res, "delete repo")
}

// SetCloned stores the clone directory and the poll time.
func (s *Store) SetCloned(ctx context.Context, id, clonePath string, at time.Time) error {
	res := s.db.WithContext(ctx).Model(&repoRow{}).Where("id = ?", id).
		Updates(map[string]any{"clone_path": clonePath, "last_polled_at": at})
	return affected(res, "set clone path")
}

// SetPolled stores the poll time.
func (s *Store) SetPolled(ctx context.Context, id string, at time.Time) error {
	res := s.db.WithContext(ctx).Model(&repoRow{}).Where("id = ?", id).Update("last_polled_at", at)
	return affected(res, "set last polled")
}

func rowOrNotFound(row repoRow, err error) (domain.Repo, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Repo{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("select repo: %w", err)
	}
	return row.toDomain(), nil
}

func affected(res *gorm.DB, what string) error {
	if res.Error != nil {
		return fmt.Errorf("%s: %w", what, res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}
