// Package postgres mirrors background jobs into the `jobs` table with GORM.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/jobs"
)

// jobRow maps the jobs table. Payload is JSON text; Error is NULL until a job fails.
type jobRow struct {
	ID          string `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID string `gorm:"column:workspace_id"`
	Kind        string `gorm:"column:kind"`
	Payload     string `gorm:"column:payload;type:jsonb"`
	Status      string `gorm:"column:status"`
}

func (jobRow) TableName() string { return "jobs" }

// Store implements jobs.Store.
type Store struct {
	db *gorm.DB
}

var _ jobs.Store = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// Insert adds a `queued` row and returns its generated id.
func (s *Store) Insert(ctx context.Context, workspaceID, kind string, payload json.RawMessage) (string, error) {
	row := jobRow{WorkspaceID: workspaceID, Kind: kind, Payload: string(payload), Status: string(jobs.StatusQueued)}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return "", fmt.Errorf("insert job: %w", err)
	}
	return row.ID, nil
}

// MarkRunning sets status `running` and started_at.
func (s *Store) MarkRunning(ctx context.Context, id string, startedAt time.Time) error {
	return s.update(ctx, id, map[string]any{"status": string(jobs.StatusRunning), "started_at": startedAt})
}

// SetAttempts records the retry number reached so far.
func (s *Store) SetAttempts(ctx context.Context, id string, attempts int) error {
	return s.update(ctx, id, map[string]any{"attempts": attempts})
}

// Finish sets the final status, attempts, finished_at and the error text (NULL when empty).
func (s *Store) Finish(ctx context.Context, id string, status jobs.Status, attempts int, errMsg string, finishedAt time.Time) error {
	var errCol any
	if errMsg != "" {
		errCol = errMsg
	}
	return s.update(ctx, id, map[string]any{
		"status": string(status), "attempts": attempts, "finished_at": finishedAt, "error": errCol,
	})
}

func (s *Store) update(ctx context.Context, id string, set map[string]any) error {
	res := s.db.WithContext(ctx).Model(&jobRow{}).Where("id = ?", id).Updates(set)
	if res.Error != nil {
		return fmt.Errorf("update job %s: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update job %s: %w", id, jobs.ErrNotFound)
	}
	return nil
}
