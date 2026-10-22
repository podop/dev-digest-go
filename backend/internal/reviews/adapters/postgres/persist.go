package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// findingInsertBatch bounds the rows of one INSERT (17 bound columns per row, far below the
// 65,535 parameter limit).
const findingInsertBatch = 500

// kindReview is reviews.kind of a review a run produced.
const kindReview = "review"

// InsertReview inserts a review of kind "review".
func (s *Store) InsertReview(ctx context.Context, r domain.NewReview) (string, error) {
	verdict, summary, model := string(r.Verdict), clean(r.Summary), clean(r.Model)
	score := r.Score
	row := reviewRow{
		WorkspaceID: r.WorkspaceID,
		PRID:        r.PRID,
		AgentID:     &r.AgentID,
		RunID:       &r.RunID,
		Kind:        kindReview,
		Verdict:     &verdict,
		Summary:     &summary,
		Score:       &score,
		Model:       &model,
	}
	if err := s.conn(ctx).Create(&row).Error; err != nil {
		return "", notFoundOnForeignKey("insert review", err)
	}
	return row.ID, nil
}

// InsertFindings inserts the findings of a review in the given order, which is also the order a
// read returns them in. kind defaults to "finding", out_of_scope is false, skill_id is the id of
// the run's skill with the cited name; the model's own id and evidence are not stored.
func (s *Store) InsertFindings(ctx context.Context, reviewID string, findings []reviewer.Finding, skillIDs map[string]string) error {
	if len(findings) == 0 {
		return nil
	}
	rows := make([]findingRow, len(findings))
	for i, f := range findings {
		row, err := newFindingRow(reviewID, f, skillIDs)
		if err != nil {
			return err
		}
		rows[i] = row
	}
	if err := s.conn(ctx).CreateInBatches(&rows, findingInsertBatch).Error; err != nil {
		return notFoundOnForeignKey("insert findings", err)
	}
	return nil
}

func newFindingRow(reviewID string, f reviewer.Finding, skillIDs map[string]string) (findingRow, error) {
	kind := string(f.Kind)
	if kind == "" {
		kind = string(reviewer.KindFinding)
	}
	row := findingRow{
		ReviewID:   reviewID,
		File:       clean(f.File),
		StartLine:  f.StartLine,
		EndLine:    f.EndLine,
		Severity:   string(f.Severity),
		Category:   string(f.Category),
		Title:      clean(f.Title),
		Rationale:  clean(f.Rationale),
		Suggestion: cleanPtr(f.Suggestion),
		Confidence: f.Confidence,
		Kind:       kind,
		OutOfScope: false,
	}
	if f.TrifectaComponents != nil {
		components := make([]string, len(f.TrifectaComponents))
		for i, c := range f.TrifectaComponents {
			components[i] = clean(c)
		}
		doc, err := json.Marshal(components)
		if err != nil {
			return findingRow{}, fmt.Errorf("encode trifecta components: %w", err)
		}
		row.TrifectaComponents = doc
	}
	if f.Skill != nil {
		name := clean(*f.Skill)
		row.SkillName = &name
		// A cited name that is not one of the run's skills stays unresolved.
		if id := skillIDs[*f.Skill]; id != "" {
			row.SkillID = &id
		}
	}
	return row, nil
}

// MarkReviewed sets the pull request's last_reviewed_sha.
func (s *Store) MarkReviewed(ctx context.Context, prID, sha string) error {
	res := s.conn(ctx).Model(&pullRow{}).Where("id = ?", prID).Update("last_reviewed_sha", clean(sha))
	if res.Error != nil {
		return fmt.Errorf("mark reviewed: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("mark reviewed %s: %w", prID, domain.ErrNotFound)
	}
	return nil
}

// DeleteReview deletes a review of the workspace; its findings go with it (ON DELETE CASCADE).
func (s *Store) DeleteReview(ctx context.Context, workspaceID, reviewID string) (bool, error) {
	res := s.conn(ctx).Where("id = ? AND workspace_id = ?", reviewID, workspaceID).Delete(&reviewRow{})
	if res.Error != nil {
		return false, fmt.Errorf("delete review: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// AcceptFinding sets accepted_at and clears dismissed_at.
func (s *Store) AcceptFinding(ctx context.Context, workspaceID, findingID string, at time.Time) (domain.FindingRecord, error) {
	return s.triage(ctx, workspaceID, findingID, map[string]any{"accepted_at": at, "dismissed_at": nil})
}

// DismissFinding sets dismissed_at and clears accepted_at.
func (s *Store) DismissFinding(ctx context.Context, workspaceID, findingID string, at time.Time) (domain.FindingRecord, error) {
	return s.triage(ctx, workspaceID, findingID, map[string]any{"dismissed_at": at, "accepted_at": nil})
}

// triage updates a finding of the workspace and returns it as the API reads it.
func (s *Store) triage(ctx context.Context, workspaceID, findingID string, set map[string]any) (domain.FindingRecord, error) {
	mine := s.conn(ctx).Model(&reviewRow{}).Select("id").Where("workspace_id = ?", workspaceID)
	res := s.conn(ctx).Model(&findingRow{}).
		Where("id = ? AND review_id IN (?)", findingID, mine).
		Updates(set)
	if res.Error != nil {
		return domain.FindingRecord{}, fmt.Errorf("update finding: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.FindingRecord{}, fmt.Errorf("finding %s: %w", findingID, domain.ErrNotFound)
	}
	return s.findingRecord(ctx, findingID)
}
