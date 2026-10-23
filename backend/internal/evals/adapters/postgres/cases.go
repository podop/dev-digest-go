// Package postgres persists the evals context with GORM: eval cases, suite runs and their case
// results, and the read-only views of agents, skills and findings that cases and runs freeze.
// Every method gets its handle from platformpg.Conn, so a store called inside a transaction joins
// it. Text and JSON written to the database pass through clean / marshal, which make hostile
// input (U+0000, invalid UTF-8) storable instead of failing the statement.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

var (
	_ app.CaseStore = (*Store)(nil)
	_ app.RunStore  = (*Store)(nil)
	_ app.Snapshots = (*Store)(nil)
	_ app.Findings  = (*Store)(nil)
)

// Store is the GORM-backed store of the evals context. One value serves the case store, the run
// store, the snapshots, the finding source and the executor's store.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

// ownerCases scopes a query on eval_cases to one owner of a workspace.
func ownerCases(tx *gorm.DB, workspaceID string, kind domain.OwnerKind, ownerID string) *gorm.DB {
	return tx.Model(&caseRow{}).Where("workspace_id = ? AND owner_kind = ? AND owner_id = ?", workspaceID, string(kind), ownerID)
}

// OwnerExists reports whether the agent or skill is in the workspace.
func (s *Store) OwnerExists(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) (bool, error) {
	if !isUUID(ownerID) {
		return false, nil
	}
	table := "agents"
	if kind == domain.OwnerSkill {
		table = "skills"
	}
	var n int64
	err := s.conn(ctx).Table(table).Where("workspace_id = ? AND id = ?", workspaceID, ownerID).Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("select %s owner: %w", kind, err)
	}
	return n > 0, nil
}

// CountCases returns the owner's number of cases.
func (s *Store) CountCases(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) (int, error) {
	if !isUUID(ownerID) {
		return 0, nil
	}
	var n int64
	if err := ownerCases(s.conn(ctx), workspaceID, kind, ownerID).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count cases: %w", err)
	}
	return int(n), nil
}

// CaseIDs returns the owner's case ids ordered by created_at, id (snapshot order).
func (s *Store) CaseIDs(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) ([]string, error) {
	ids := []string{}
	if !isUUID(ownerID) {
		return ids, nil
	}
	err := ownerCases(s.conn(ctx), workspaceID, kind, ownerID).Order("created_at, id").Pluck("id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("select case ids: %w", err)
	}
	return ids, nil
}

// AgentCaseCounts returns the number of agent-owned cases per agent id.
func (s *Store) AgentCaseCounts(ctx context.Context, workspaceID string) (map[string]int, error) {
	var rows []struct {
		OwnerID string `gorm:"column:owner_id"`
		N       int    `gorm:"column:n"`
	}
	err := s.conn(ctx).Model(&caseRow{}).
		Select("owner_id, count(*)::int AS n").
		Where("workspace_id = ? AND owner_kind = ?", workspaceID, string(domain.OwnerAgent)).
		Group("owner_id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("count agent cases: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.OwnerID] = r.N
	}
	return out, nil
}

// lastResults returns, per case, its result in the latest finished suite run that contains it.
func (s *Store) lastResults(ctx context.Context, caseIDs []string) (map[string]*domain.LastResult, error) {
	out := map[string]*domain.LastResult{}
	if len(caseIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		CaseID string    `gorm:"column:case_id"`
		RunID  string    `gorm:"column:run_id"`
		Status string    `gorm:"column:status"`
		RanAt  time.Time `gorm:"column:ran_at"`
	}
	err := s.conn(ctx).Table("eval_runs").
		Select("DISTINCT ON (eval_runs.case_id) eval_runs.case_id AS case_id, eval_runs.run_id AS run_id, eval_runs.status AS status, eval_runs.ran_at AS ran_at").
		Joins("JOIN eval_suite_runs ON eval_suite_runs.id = eval_runs.run_id").
		Where("eval_runs.case_id IN ? AND eval_suite_runs.status IN ? AND eval_runs.status IS NOT NULL", caseIDs, finishedStatuses).
		Order("eval_runs.case_id, eval_suite_runs.finished_at DESC, eval_suite_runs.started_at DESC, eval_suite_runs.id DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select last results: %w", err)
	}
	for _, r := range rows {
		out[r.CaseID] = &domain.LastResult{RunID: r.RunID, Status: domain.CaseStatus(r.Status), RanAt: r.RanAt}
	}
	return out, nil
}

// views pairs the cases with their last results.
func (s *Store) views(ctx context.Context, rows []caseRow) ([]domain.CaseView, error) {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	last, err := s.lastResults(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.CaseView, len(rows))
	for i, r := range rows {
		c, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out[i] = domain.CaseView{Case: c, LastResult: last[r.ID]}
	}
	return out, nil
}

// ListCases returns the owner's cases with their last result, created_at desc then id desc.
func (s *Store) ListCases(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) ([]domain.CaseView, error) {
	if !isUUID(ownerID) {
		return []domain.CaseView{}, nil
	}
	var rows []caseRow
	if err := ownerCases(s.conn(ctx), workspaceID, kind, ownerID).Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select cases: %w", err)
	}
	return s.views(ctx, rows)
}

func (s *Store) caseRow(ctx context.Context, workspaceID, caseID string) (caseRow, error) {
	if !isUUID(caseID) {
		return caseRow{}, domain.ErrNotFound
	}
	var row caseRow
	err := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, caseID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return caseRow{}, domain.ErrNotFound
	}
	if err != nil {
		return caseRow{}, fmt.Errorf("select case: %w", err)
	}
	return row, nil
}

// GetCase returns one case of the workspace.
func (s *Store) GetCase(ctx context.Context, workspaceID, caseID string) (domain.Case, error) {
	row, err := s.caseRow(ctx, workspaceID, caseID)
	if err != nil {
		return domain.Case{}, err
	}
	return row.toDomain()
}

// GetCaseView returns one case with its last result.
func (s *Store) GetCaseView(ctx context.Context, workspaceID, caseID string) (domain.CaseView, error) {
	row, err := s.caseRow(ctx, workspaceID, caseID)
	if err != nil {
		return domain.CaseView{}, err
	}
	views, err := s.views(ctx, []caseRow{row})
	if err != nil {
		return domain.CaseView{}, err
	}
	return views[0], nil
}

// CaseBySourceFinding returns the id of the case made from the finding; ok false when none.
func (s *Store) CaseBySourceFinding(ctx context.Context, workspaceID, findingID string) (string, bool, error) {
	if !isUUID(findingID) {
		return "", false, nil
	}
	ids := []string{}
	err := s.conn(ctx).Model(&caseRow{}).
		Where("workspace_id = ? AND source_finding_id = ?", workspaceID, findingID).Limit(1).Pluck("id", &ids).Error
	if err != nil {
		return "", false, fmt.Errorf("select case by finding: %w", err)
	}
	if len(ids) == 0 {
		return "", false, nil
	}
	return ids[0], true, nil
}

// CreateCase inserts a case. A unique violation on source_finding_id is *domain.CaseExistsError
// carrying the winning case's id. The insert runs in its own transaction (a savepoint inside an
// open one) so a lost race does not abort the caller's transaction.
func (s *Store) CreateCase(ctx context.Context, c domain.NewCase) (domain.Case, error) {
	row, err := newCaseRow(c)
	if err != nil {
		return domain.Case{}, err
	}
	err = s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Omit("id").Create(&row).Error
	})
	if err != nil {
		if hasState(err, uniqueViolation) && c.SourceFindingID != nil {
			if id, ok, qerr := s.CaseBySourceFinding(ctx, c.WorkspaceID, *c.SourceFindingID); qerr == nil && ok {
				return domain.Case{}, &domain.CaseExistsError{CaseID: id}
			}
		}
		return domain.Case{}, fmt.Errorf("insert case: %w", err)
	}
	return row.toDomain()
}

// UpdateCase reads the case under a row lock, calls fn with it and stores the returned case in
// the same transaction. Only name, notes, the expectation, the diff and the input metadata can
// change, and content_edited_at is set once, never cleared. A case fn returns unchanged writes
// nothing.
func (s *Store) UpdateCase(ctx context.Context, workspaceID, caseID string, fn func(domain.Case) (domain.Case, error)) (domain.Case, error) {
	if !isUUID(caseID) {
		return domain.Case{}, domain.ErrNotFound
	}
	var result domain.Case
	err := s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		var row caseRow
		err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("workspace_id = ? AND id = ?", workspaceID, caseID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock case: %w", err)
		}
		before, err := row.toDomain()
		if err != nil {
			return err
		}
		after, err := fn(before)
		if err != nil {
			return err
		}
		set, err := caseChanges(before, after)
		if err != nil {
			return err
		}
		if len(set) > 0 {
			if err := tx.Model(&caseRow{}).Where("id = ?", row.ID).Updates(set).Error; err != nil {
				return fmt.Errorf("update case: %w", err)
			}
			if err := tx.Where("id = ?", row.ID).Take(&row).Error; err != nil {
				return fmt.Errorf("reload case: %w", err)
			}
			if before, err = row.toDomain(); err != nil {
				return err
			}
		}
		result = before
		return nil
	})
	if err != nil {
		return domain.Case{}, err
	}
	return result, nil
}

// caseChanges is the column set that differs between the stored case and the one fn returned.
func caseChanges(before, after domain.Case) (map[string]any, error) {
	set := map[string]any{}
	if after.Name != before.Name {
		set["name"] = clean(after.Name)
	}
	if !reflect.DeepEqual(after.Notes, before.Notes) {
		set["notes"] = cleanPtr(after.Notes)
	}
	if !reflect.DeepEqual(after.Expectation, before.Expectation) {
		exp := after.Expectation
		exp.File = clean(exp.File)
		exp.Severity, exp.Category, exp.Title = cleanPtr(exp.Severity), cleanPtr(exp.Category), cleanPtr(exp.Title)
		raw, err := encodeExpectation(exp)
		if err != nil {
			return nil, err
		}
		set["expected_output"] = raw
	}
	if after.InputDiff != before.InputDiff {
		set["input_diff"] = clean(after.InputDiff)
	}
	if !reflect.DeepEqual(after.InputMeta, before.InputMeta) {
		raw, err := encodeMeta(after.InputMeta)
		if err != nil {
			return nil, err
		}
		set["input_meta"] = raw
	}
	// Set once: an already stamped case keeps its first edit time.
	if before.ContentEditedAt == nil && after.ContentEditedAt != nil {
		set["content_edited_at"] = *after.ContentEditedAt
	}
	return set, nil
}

// DeleteCase deletes the case; its results are deleted with it (cascade).
func (s *Store) DeleteCase(ctx context.Context, workspaceID, caseID string) error {
	if !isUUID(caseID) {
		return domain.ErrNotFound
	}
	res := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, caseID).Delete(&caseRow{})
	if res.Error != nil {
		return fmt.Errorf("delete case: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}
