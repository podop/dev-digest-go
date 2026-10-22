// Package postgres reads what the smart-diff context needs with GORM: a pull request's stored
// files and the findings of its current reviews. It writes nothing. The row structs are declared
// here, per adapter: the tables belong to other contexts (pull requests, reviews, findings).
package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/smartdiff/app"
	"dev-digest-go/backend/internal/smartdiff/domain"
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
	ID string `gorm:"column:id"`
}

func (pullRow) TableName() string { return "pull_requests" }

type fileRow struct {
	Path      string `gorm:"column:path"`
	Additions int    `gorm:"column:additions"`
	Deletions int    `gorm:"column:deletions"`
}

func (fileRow) TableName() string { return "pr_files" }

type findingRow struct {
	File      string `gorm:"column:file"`
	StartLine int    `gorm:"column:start_line"`
}

// PullFiles returns the stored files of a pull request of the workspace, by path in byte order
// (the domain re-sorts them), or domain.ErrNotFound when the pull request is not in the workspace.
func (s *Store) PullFiles(ctx context.Context, workspaceID, prID string) ([]domain.PullFile, error) {
	db := s.conn(ctx)
	var pull pullRow
	err := db.Where("workspace_id = ? AND id = ?", workspaceID, prID).Take(&pull).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("pull request %s: %w", prID, domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("select pull request: %w", err)
	}
	var rows []fileRow
	if err := db.Where("pr_id = ?", prID).Order(`path COLLATE "C", id`).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select pr files: %w", err)
	}
	out := make([]domain.PullFile, len(rows))
	for i, r := range rows {
		out[i] = domain.PullFile{Path: r.Path, Additions: r.Additions, Deletions: r.Deletions}
	}
	return out, nil
}

// currentFindingsSQL selects every finding of the newest kind='review' review of each agent of the
// pull request. DISTINCT ON puts the reviews with a NULL agent in one group; the newest of a group
// is by created_at, ties by id. GORM's builder has no DISTINCT ON, hence Raw.
const currentFindingsSQL = `
SELECT f.file, f.start_line
FROM findings f
WHERE f.review_id IN (
	SELECT DISTINCT ON (r.agent_id) r.id
	FROM reviews r
	WHERE r.pr_id = ? AND r.kind = 'review'
	ORDER BY r.agent_id, r.created_at DESC, r.id ASC
)`

// CurrentFindings returns the file and first line of every finding, dismissed ones included, of
// the current reviews of the pull request; older reviews' findings are not returned.
func (s *Store) CurrentFindings(ctx context.Context, prID string) ([]domain.FindingRef, error) {
	var rows []findingRow
	if err := s.conn(ctx).Raw(currentFindingsSQL, prID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("select current findings: %w", err)
	}
	out := make([]domain.FindingRef, len(rows))
	for i, r := range rows {
		out[i] = domain.FindingRef{File: r.File, StartLine: r.StartLine}
	}
	return out, nil
}
