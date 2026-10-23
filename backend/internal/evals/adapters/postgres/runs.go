package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/evals/domain"
)

// CreateRun inserts the run as running with cases_finished 0. A violation of the one-running-run-
// per-owner partial unique index is domain.ErrRunActive; an owner deleted since the snapshot
// (foreign key) is domain.ErrNotFound. The insert runs in its own transaction (a savepoint inside
// an open one) so a refusal does not abort the caller's transaction.
func (s *Store) CreateRun(ctx context.Context, r domain.NewRun) (domain.Run, error) {
	row, err := newRunRow(r)
	if err != nil {
		return domain.Run{}, err
	}
	err = s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Omit("id").Create(&row).Error
	})
	switch {
	case hasState(err, uniqueViolation):
		return domain.Run{}, domain.ErrRunActive
	case hasState(err, foreignKeyViolation):
		return domain.Run{}, domain.ErrNotFound
	case err != nil:
		return domain.Run{}, fmt.Errorf("insert run: %w", err)
	}
	return row.toDomain()
}

// RecordResult inserts the result and adds 1 to cases_finished and, by status, to cases_passed or
// cases_errored in one transaction. A foreign-key violation (the case or the run was deleted
// meanwhile) records nothing and is domain.ErrRowGone.
func (s *Store) RecordResult(ctx context.Context, workspaceID string, r domain.NewResult) error {
	if !isUUID(r.RunID) || !isUUID(r.CaseID) {
		return domain.ErrRowGone
	}
	findings, err := encodeFindings(r.Findings)
	if err != nil {
		return err
	}
	rec := resultRecord{
		RunID: r.RunID, CaseID: r.CaseID, Status: string(r.Status), Pass: r.Status == domain.CasePassed,
		ActualOutput: findings, DroppedCount: r.DroppedCount,
		HitCount: r.HitCount, ViolationCount: r.ViolationCount, UnlabeledCount: r.UnlabeledCount,
		DurationMS: r.DurationMS, CostUSD: r.CostUSD, Error: cleanPtr(r.Error),
	}
	passed, errored := 0, 0
	switch r.Status {
	case domain.CasePassed:
		passed = 1
	case domain.CaseError:
		errored = 1
	}
	err = s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Omit("id").Create(&rec).Error; err != nil {
			return err
		}
		res := tx.Model(&runRow{}).Where("id = ? AND workspace_id = ?", r.RunID, workspaceID).Updates(map[string]any{
			"cases_finished": gorm.Expr("cases_finished + 1"),
			"cases_passed":   gorm.Expr("cases_passed + ?", passed),
			"cases_errored":  gorm.Expr("cases_errored + ?", errored),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// The run is not in this workspace: nothing may be recorded against it.
			return domain.ErrRowGone
		}
		return nil
	})
	switch {
	case errors.Is(err, domain.ErrRowGone), hasState(err, foreignKeyViolation):
		return domain.ErrRowGone
	case err != nil:
		return fmt.Errorf("record result: %w", err)
	}
	return nil
}

// DropRunCase subtracts 1 from the run's cases_total, never below 0.
func (s *Store) DropRunCase(ctx context.Context, workspaceID, runID string) error {
	if !isUUID(runID) {
		return nil
	}
	err := s.conn(ctx).Model(&runRow{}).Where("id = ? AND workspace_id = ?", runID, workspaceID).
		Update("cases_total", gorm.Expr("GREATEST(cases_total - 1, 0)")).Error
	if err != nil {
		return fmt.Errorf("drop run case: %w", err)
	}
	return nil
}

// FinishRun writes the final state WHERE status = 'running' and reports whether a row changed.
func (s *Store) FinishRun(ctx context.Context, workspaceID, runID string, f domain.RunFinish) (bool, error) {
	if !isUUID(runID) {
		return false, nil
	}
	m := f.Metrics
	res := s.conn(ctx).Model(&runRow{}).
		Where("id = ? AND workspace_id = ? AND status = ?", runID, workspaceID, string(domain.RunRunning)).
		Updates(map[string]any{
			"status":             string(f.Status),
			"error":              cleanPtr(f.Error),
			"finished_at":        f.FinishedAt,
			"duration_ms":        f.DurationMS,
			"recall":             m.Recall,
			"precision":          m.Precision,
			"citation_accuracy":  m.CitationAccuracy,
			"cases_passed":       f.CasesPassed,
			"cases_errored":      f.CasesErrored,
			"unlabeled_findings": m.UnlabeledFindings,
			"cost_usd":           m.CostUSD,
		})
	if res.Error != nil {
		return false, fmt.Errorf("finish run: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// FailInterrupted sets every running run to failed with error "interrupted" and finished_at now,
// in every workspace, and returns how many.
func (s *Store) FailInterrupted(ctx context.Context) (int, error) {
	res := s.conn(ctx).Model(&runRow{}).Where("status = ?", string(domain.RunRunning)).
		Updates(map[string]any{
			"status":      string(domain.RunFailed),
			"error":       domain.InterruptedError,
			"finished_at": gorm.Expr("now()"),
		})
	if res.Error != nil {
		return 0, fmt.Errorf("fail interrupted runs: %w", res.Error)
	}
	return int(res.RowsAffected), nil
}
