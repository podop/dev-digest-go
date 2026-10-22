package postgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/reviews/domain"
)

// CreateMultiAgentRun inserts the parent row of a 2+ agent request.
func (s *Store) CreateMultiAgentRun(ctx context.Context, workspaceID, prID string) (string, error) {
	row := multiAgentRunRow{WorkspaceID: workspaceID, PRID: prID}
	if err := s.conn(ctx).Create(&row).Error; err != nil {
		return "", notFoundOnForeignKey("insert multi-agent run", err)
	}
	return row.ID, nil
}

// CreateRun inserts a running agent_runs row of source "local"; ran_at is the database's now().
func (s *Store) CreateRun(ctx context.Context, r domain.NewRun) (string, error) {
	status := string(domain.StatusRunning)
	row := runRow{
		WorkspaceID:     r.WorkspaceID,
		AgentID:         &r.AgentID,
		PRID:            &r.PRID,
		Provider:        &r.Provider,
		Model:           &r.Model,
		Status:          &status,
		Source:          domain.SourceLocal,
		MultiAgentRunID: r.MultiAgentRunID,
	}
	if err := s.conn(ctx).Create(&row).Error; err != nil {
		return "", notFoundOnForeignKey("insert run", err)
	}
	return row.ID, nil
}

// RecordRunSkills writes agent_run_skills in prompt order; a skill already recorded for the run
// is left as it is.
func (s *Store) RecordRunSkills(ctx context.Context, runID string, skills []domain.ReviewSkill) error {
	if len(skills) == 0 {
		return nil
	}
	rows := make([]runSkillRow, len(skills))
	for i, sk := range skills {
		rows[i] = runSkillRow{RunID: runID, SkillID: sk.ID, SkillVersion: sk.Version, Order: i}
	}
	err := s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	if err != nil {
		return notFoundOnForeignKey("insert run skills", err)
	}
	return nil
}

// DeleteRun deletes a run of the workspace. Its trace, skills and review (reviews.run_id is
// ON DELETE CASCADE) and the review's findings go with it, in this one statement.
func (s *Store) DeleteRun(ctx context.Context, workspaceID, runID string) (bool, error) {
	res := s.conn(ctx).Where("id = ? AND workspace_id = ?", runID, workspaceID).Delete(&runRow{})
	if res.Error != nil {
		return false, fmt.Errorf("delete run: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// CancelIfRunning moves a running run of the workspace to cancelled with the note.
func (s *Store) CancelIfRunning(ctx context.Context, workspaceID, runID, note string) (bool, error) {
	res := s.conn(ctx).Model(&runRow{}).
		Where("id = ? AND workspace_id = ? AND status = ?", runID, workspaceID, string(domain.StatusRunning)).
		Updates(map[string]any{"status": string(domain.StatusCancelled), "error": clean(note)})
	if res.Error != nil {
		return false, fmt.Errorf("cancel run: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// completionColumns are the agent_runs columns a run's final record sets.
func completionColumns(c domain.RunCompletion) map[string]any {
	return map[string]any{
		"duration_ms":    c.DurationMS,
		"tokens_in":      c.TokensIn,
		"tokens_out":     c.TokensOut,
		"cost_usd":       c.CostUSD,
		"findings_count": c.FindingsCount,
		"grounding":      clean(c.Grounding),
		"score":          c.Score,
		"blockers":       c.Blockers,
	}
}

// errorValue is the error note as a statement argument: NULL when nil.
func errorValue(e *string) any {
	if e == nil {
		return nil
	}
	return clean(*e)
}

// CompleteRunIfRunning sets a running run to done with its record, in the caller's transaction;
// false (and nothing written) when the run is no longer running.
func (s *Store) CompleteRunIfRunning(ctx context.Context, runID string, c domain.RunCompletion) (bool, error) {
	set := completionColumns(c)
	set["status"] = string(domain.StatusDone)
	set["error"] = errorValue(c.Error)
	res := s.conn(ctx).Model(&runRow{}).
		Where("id = ? AND status = ?", runID, string(domain.StatusRunning)).
		Updates(set)
	if res.Error != nil {
		return false, fmt.Errorf("complete run: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// EndRun records the end of a failed or cancelled run (port doc of app.RunStore). The usage and
// duration record is written to every run that is not done, so a cancelled run keeps what it
// spent. The status and the error move to the given ones only from running; a run the cancel
// route already cancelled keeps its status and its note (CancelledNote when it has none), and a
// failed run keeps its error. A done run is not touched at all.
func (s *Store) EndRun(ctx context.Context, runID string, status domain.RunStatus, c domain.RunCompletion) error {
	running, cancelled := string(domain.StatusRunning), string(domain.StatusCancelled)
	set := completionColumns(c)
	set["status"] = gorm.Expr("CASE WHEN status = ? THEN ?::text ELSE status END", running, string(status))
	set["error"] = gorm.Expr(
		"CASE WHEN status = ? THEN ?::text WHEN status = ? THEN COALESCE(NULLIF(error, ''), ?::text) ELSE error END",
		running, errorValue(c.Error), cancelled, domain.CancelledNote)
	res := s.conn(ctx).Model(&runRow{}).
		Where("id = ? AND status IS DISTINCT FROM ?", runID, string(domain.StatusDone)).
		Updates(set)
	if res.Error != nil {
		return fmt.Errorf("end run: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}
	// Nothing matched: the run is gone or done.
	var n int64
	if err := s.conn(ctx).Model(&runRow{}).Where("id = ?", runID).Count(&n).Error; err != nil {
		return fmt.Errorf("end run: look up run: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("end run %s: %w", runID, domain.ErrNotFound)
	}
	return nil
}

// FailInterrupted sets every running run to failed, leaving the error as it is, and returns how
// many it changed.
func (s *Store) FailInterrupted(ctx context.Context) (int, error) {
	res := s.conn(ctx).Model(&runRow{}).
		Where("status = ?", string(domain.StatusRunning)).
		Update("status", string(domain.StatusFailed))
	if res.Error != nil {
		return 0, fmt.Errorf("fail interrupted runs: %w", res.Error)
	}
	return int(res.RowsAffected), nil
}

// SaveTrace upserts the run's trace document.
func (s *Store) SaveTrace(ctx context.Context, runID string, t domain.Trace) error {
	doc, err := EncodeTrace(t)
	if err != nil {
		return fmt.Errorf("encode trace: %w", err)
	}
	row := traceRow{RunID: runID, Trace: doc}
	err = s.conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "run_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"trace"}),
	}).Create(&row).Error
	if err != nil {
		return notFoundOnForeignKey("save trace", err)
	}
	return nil
}
