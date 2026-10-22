// Package postgres persists scans and conventions with GORM. Every method gets its handle from
// platformpg.Conn, so a store called inside a use case's transaction joins it.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

// uniqueViolation is the SQLSTATE of a unique index violation.
const uniqueViolation = "23505"

// statusOrder sorts accepted, then pending, then rejected conventions.
const statusOrder = "CASE status WHEN 'accepted' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END"

// Store is the GORM-backed store of the conventions context. It implements the stores and the
// repo lookup of the app package.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

// Get returns the repo of the workspace, or domain.ErrNotFound.
func (s *Store) Get(ctx context.Context, workspaceID, repoID string) (app.Repo, error) {
	var row repoRow
	err := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, repoID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return app.Repo{}, domain.ErrNotFound
	}
	if err != nil {
		return app.Repo{}, fmt.Errorf("select repo: %w", err)
	}
	repo := app.Repo{ID: row.ID, FullName: row.FullName}
	if row.ClonePath != nil {
		repo.ClonePath = *row.ClonePath
	}
	return repo, nil
}

// LatestScan returns the repo's newest scan by start time, or domain.ErrNotFound.
func (s *Store) LatestScan(ctx context.Context, repoID string) (domain.Scan, error) {
	var row scanRow
	err := s.conn(ctx).Where("repo_id = ?", repoID).Order("started_at DESC, id").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Scan{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Scan{}, fmt.Errorf("select latest scan: %w", err)
	}
	return row.toDomain()
}

// List returns every convention of the repo ordered by status (accepted, pending, rejected),
// confidence descending with NULL last, then creation time ascending.
func (s *Store) List(ctx context.Context, repoID string) ([]domain.Convention, error) {
	var rows []conventionRow
	err := s.conn(ctx).Where("repo_id = ?", repoID).
		Order(statusOrder + ", confidence DESC NULLS LAST, created_at ASC, id").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select conventions: %w", err)
	}
	out := make([]domain.Convention, len(rows))
	for i, r := range rows {
		if out[i], err = r.toDomain(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FailStaleScans fails the repo's running scans that started before the given time.
func (s *Store) FailStaleScans(ctx context.Context, repoID string, before time.Time, reason string, at time.Time) error {
	err := s.conn(ctx).Model(&scanRow{}).
		Where("repo_id = ? AND status = ? AND started_at < ?", repoID, string(domain.ScanRunning), before).
		Updates(map[string]any{"status": string(domain.ScanFailed), "error": reason, "finished_at": at}).Error
	if err != nil {
		return fmt.Errorf("fail stale scans: %w", err)
	}
	return nil
}

// StartScan inserts a running scan. The partial unique index convention_scans_one_running_uq
// makes a second running scan of the repo a unique violation, reported as domain.ErrScanRunning.
func (s *Store) StartScan(ctx context.Context, workspaceID, repoID string, at time.Time) (domain.Scan, error) {
	row := scanRow{
		WorkspaceID: workspaceID, RepoID: repoID, Status: string(domain.ScanRunning),
		SampledFiles: jsonb("[]"), Dropped: jsonb("[]"), StartedAt: at,
	}
	if err := s.conn(ctx).Omit("id").Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return domain.Scan{}, domain.ErrScanRunning
		}
		return domain.Scan{}, fmt.Errorf("insert scan: %w", err)
	}
	return row.toDomain()
}

// FailScan fails a scan that is still running and records the files sampled so far; a scan that
// is gone or no longer running is left alone.
func (s *Store) FailScan(ctx context.Context, scanID, reason string, sampled []string, at time.Time) error {
	files, err := marshalStrings(sampled)
	if err != nil {
		return err
	}
	err = s.conn(ctx).Model(&scanRow{}).
		Where("id = ? AND status = ?", scanID, string(domain.ScanRunning)).
		Updates(map[string]any{
			"status": string(domain.ScanFailed), "error": reason, "finished_at": at, "sampled_files": files,
		}).Error
	if err != nil {
		return fmt.Errorf("fail scan %s: %w", scanID, err)
	}
	return nil
}

// FailRunningScans fails every running scan and returns how many.
func (s *Store) FailRunningScans(ctx context.Context, reason string, at time.Time) (int, error) {
	res := s.conn(ctx).Model(&scanRow{}).Where("status = ?", string(domain.ScanRunning)).
		Updates(map[string]any{"status": string(domain.ScanFailed), "error": reason, "finished_at": at})
	if res.Error != nil {
		return 0, fmt.Errorf("fail running scans: %w", res.Error)
	}
	return int(res.RowsAffected), nil
}

// CompleteScan, in one transaction, deletes the repo's pending unedited conventions, inserts the
// kept ones as pending conventions of the scan and sets the scan done. The scan update comes
// first and only matches a running scan, so a scan that was failed meanwhile (or whose repo is
// gone) changes nothing and yields domain.ErrNotFound.
func (s *Store) CompleteScan(ctx context.Context, scan app.ScanRef, kept []domain.KeptConvention, sum app.Summary, at time.Time) error {
	files, err := marshalStrings(sum.SampledFiles)
	if err != nil {
		return err
	}
	dropped, err := marshalDropped(sum.Dropped)
	if err != nil {
		return err
	}
	rows := make([]conventionRow, len(kept))
	for i, k := range kept {
		evidence, err := marshalEvidence(k.Evidence)
		if err != nil {
			return err
		}
		confidence := k.Confidence
		scanID := scan.ID
		// One transaction gives every row the same now(); a microsecond step per row keeps the
		// order the model proposed them in.
		stamp := at.Add(time.Duration(i) * time.Microsecond)
		rows[i] = conventionRow{
			WorkspaceID: scan.WorkspaceID, RepoID: scan.RepoID, Rule: k.Rule, Confidence: &confidence,
			ScanID: &scanID, Category: k.Category, Evidence: evidence, Status: string(domain.StatusPending),
			CreatedAt: stamp, UpdatedAt: stamp,
		}
	}

	return s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&scanRow{}).Where("id = ? AND status = ?", scan.ID, string(domain.ScanRunning)).
			Updates(map[string]any{
				"status": string(domain.ScanDone), "finished_at": at, "sampled_files": files,
				"proposed": sum.Proposed, "kept": sum.Kept, "dropped": dropped, "model": sum.Model,
				"tokens_in": sum.TokensIn, "tokens_out": sum.TokensOut, "cost_usd": sum.CostUSD,
			})
		if res.Error != nil {
			return fmt.Errorf("finish scan %s: %w", scan.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			return domain.ErrNotFound
		}
		err := tx.Where("repo_id = ? AND status = ? AND edited = ?", scan.RepoID, string(domain.StatusPending), false).
			Delete(&conventionRow{}).Error
		if err != nil {
			return fmt.Errorf("delete pending conventions: %w", err)
		}
		if len(rows) > 0 {
			if err := tx.Omit("id").Create(&rows).Error; err != nil {
				return fmt.Errorf("insert conventions: %w", err)
			}
		}
		return nil
	})
}

// Update loads the convention of the workspace under a row lock, applies fn and stores the
// status, rule, category, edited flag and update time it returns.
func (s *Store) Update(ctx context.Context, workspaceID, id string, fn func(domain.Convention) (domain.Convention, error)) (domain.Convention, error) {
	var saved domain.Convention
	err := s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		var row conventionRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("workspace_id = ? AND id = ?", workspaceID, id).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("select convention: %w", err)
		}
		current, err := row.toDomain()
		if err != nil {
			return err
		}
		next, err := fn(current)
		if err != nil {
			return err
		}
		err = tx.Model(&conventionRow{}).Where("id = ?", id).Updates(map[string]any{
			"status": string(next.Status), "rule": next.Rule, "category": next.Category,
			"edited": next.Edited, "updated_at": next.UpdatedAt,
		}).Error
		if err != nil {
			return fmt.Errorf("update convention: %w", err)
		}
		saved = next
		return nil
	})
	if err != nil {
		return domain.Convention{}, err
	}
	return saved, nil
}

// AcceptedIDs returns, among ids, those that are accepted conventions of the repo.
func (s *Store) AcceptedIDs(ctx context.Context, workspaceID, repoID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []string
	err := s.conn(ctx).Model(&conventionRow{}).
		Where("workspace_id = ? AND repo_id = ? AND status = ? AND id IN ?", workspaceID, repoID, string(domain.StatusAccepted), ids).
		Pluck("id", &out).Error
	if err != nil {
		return nil, fmt.Errorf("select accepted conventions: %w", err)
	}
	return out, nil
}

// SetSkill points the given conventions of the repo at the skill.
func (s *Store) SetSkill(ctx context.Context, workspaceID, repoID string, ids []string, skillID string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	err := s.conn(ctx).Model(&conventionRow{}).
		Where("workspace_id = ? AND repo_id = ? AND id IN ?", workspaceID, repoID, ids).
		Updates(map[string]any{"skill_id": skillID, "updated_at": at}).Error
	if err != nil {
		return fmt.Errorf("set skill of conventions: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err carries SQLSTATE 23505, without importing the driver.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == uniqueViolation
}
