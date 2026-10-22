// Package postgres reads what the blast context needs with GORM: a pull request with its
// repository's owner and name, and the paths of its stored files. It writes nothing. The row
// structs are declared here, per adapter: the tables belong to other contexts.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/blast/app"
	"dev-digest-go/backend/internal/blast/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

// Store is the GORM-backed implementation of app.Store.
type Store struct {
	db *gorm.DB
}

var _ app.Store = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

type pullRow struct {
	ID     string `gorm:"column:id"`
	RepoID string `gorm:"column:repo_id"`
	Number int    `gorm:"column:number"`
	Owner  string `gorm:"column:owner"`
	Name   string `gorm:"column:name"`
}

type pathRow struct {
	Path string `gorm:"column:path"`
}

func (pathRow) TableName() string { return "pr_files" }

// FindPull returns a pull request of the workspace with its repository's owner and name, or
// domain.ErrNotFound when it is not in the workspace.
func (s *Store) FindPull(ctx context.Context, workspaceID, prID string) (domain.Pull, error) {
	var row pullRow
	err := s.conn(ctx).Table("pull_requests AS p").
		Select("p.id, p.repo_id, p.number, r.owner, r.name").
		Joins("JOIN repos r ON r.id = p.repo_id").
		Where("p.workspace_id = ? AND p.id = ?", workspaceID, prID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Pull{}, fmt.Errorf("pull request %s: %w", prID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Pull{}, fmt.Errorf("select pull request: %w", err)
	}
	return domain.Pull{ID: row.ID, RepoID: row.RepoID, Number: row.Number, Owner: row.Owner, Name: row.Name}, nil
}

// ChangedPaths returns the distinct paths of the pull request's stored files in byte order.
func (s *Store) ChangedPaths(ctx context.Context, prID string) ([]string, error) {
	var rows []pathRow
	err := s.conn(ctx).Select("path").Where("pr_id = ?", prID).Group("path").Order(`path COLLATE "C"`).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select pr file paths: %w", err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Path
	}
	return out, nil
}
