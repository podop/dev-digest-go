package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm/clause"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// findingInsertBatch bounds the rows of one INSERT (17 bound columns per row, far below the
// 65,535 parameter limit).
const findingInsertBatch = 500

// kindReview is reviews.kind of a review a run produced.
const kindReview = "review"

// insertReviewSQL inserts a review of kind "review". agent_id is read from agents in the same
// statement, so a run whose agent was deleted meanwhile (DELETE /agents/:id, a plugin Remove)
// stores its review with agent_id NULL instead of failing the reviews_agent_id foreign key
// (spec D19). FOR KEY SHARE locks the agent row until the transaction ends: a delete that is
// in flight makes the subquery wait and then see no row, and a delete that comes later waits for
// this transaction and its ON DELETE SET NULL then clears the new review too. The pull request
// and run foreign keys still reject a missing row (SQLSTATE 23503, mapped to not found).
const insertReviewSQL = `INSERT INTO reviews (workspace_id, pr_id, agent_id, run_id, kind, verdict, summary, score, model)
VALUES (?, ?, (SELECT id FROM agents WHERE id = ?::uuid FOR KEY SHARE), ?, ?, ?, ?, ?, ?)
RETURNING id`

// InsertReview inserts a review of kind "review"; a vanished agent leaves agent_id NULL.
func (s *Store) InsertReview(ctx context.Context, r domain.NewReview) (string, error) {
	var id string
	err := s.conn(ctx).Raw(insertReviewSQL,
		r.WorkspaceID, r.PRID, r.AgentID, r.RunID, kindReview,
		string(r.Verdict), clean(r.Summary), r.Score, clean(r.Model),
	).Scan(&id).Error
	if err != nil {
		return "", notFoundOnForeignKey("insert review", err)
	}
	return id, nil
}

// InsertFindings inserts the findings of a review in the given order, which is also the order a
// read returns them in. kind defaults to "finding", out_of_scope is the engine's boolean (nil, a
// review without a PR intent, is false), skill_id is the id of the run's skill with the cited
// name; the model's own id and evidence are not stored. A cited skill that was deleted while the
// run ran (a plugin Remove) leaves skill_id NULL and keeps skill_name, as the foreign key's
// ON DELETE SET NULL would (spec D19).
func (s *Store) InsertFindings(ctx context.Context, reviewID string, findings []reviewer.Finding, skillIDs map[string]string) error {
	if len(findings) == 0 {
		return nil
	}
	skillIDs, err := s.existingSkills(ctx, findings, skillIDs)
	if err != nil {
		return err
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

// existingSkills keeps the entries of skillIDs that the findings cite and whose skill row still
// exists. The rows are locked FOR KEY SHARE until the transaction ends: a delete in flight makes
// the read wait and then miss the row, and a later delete waits for this transaction and sets the
// new findings' skill_id to NULL itself.
func (s *Store) existingSkills(ctx context.Context, findings []reviewer.Finding, skillIDs map[string]string) (map[string]string, error) {
	cited := make(map[string]struct{})
	for _, f := range findings {
		if f.Skill != nil && skillIDs[*f.Skill] != "" {
			cited[skillIDs[*f.Skill]] = struct{}{}
		}
	}
	if len(cited) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(cited))
	for id := range cited {
		ids = append(ids, id)
	}
	var found []string
	err := s.conn(ctx).Table("skills").Where("id IN ?", ids).Order("id").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).Pluck("id", &found).Error
	if err != nil {
		return nil, fmt.Errorf("check finding skills: %w", err)
	}
	alive := make(map[string]struct{}, len(found))
	for _, id := range found {
		alive[id] = struct{}{}
	}
	kept := make(map[string]string, len(skillIDs))
	for name, id := range skillIDs {
		if _, ok := alive[id]; ok {
			kept[name] = id
		}
	}
	return kept, nil
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
		OutOfScope: f.OutOfScope != nil && *f.OutOfScope,
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
