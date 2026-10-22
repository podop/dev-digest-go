package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dev-digest-go/backend/internal/reviews/domain"
)

// learnPR is the pull-request context of a finding.
type learnPR struct {
	RepoID   string `gorm:"column:repo_id"`
	PRNumber int    `gorm:"column:pr_number"`
}

// FindLearnTarget returns a finding of the workspace with its repository and pull request number.
func (s *Store) FindLearnTarget(ctx context.Context, workspaceID, findingID string) (domain.LearnTarget, error) {
	var pr learnPR
	err := s.conn(ctx).Table("findings").
		Select("pull_requests.repo_id AS repo_id, pull_requests.number AS pr_number").
		Joins("JOIN reviews ON reviews.id = findings.review_id").
		Joins("JOIN pull_requests ON pull_requests.id = reviews.pr_id").
		Where("findings.id = ? AND pull_requests.workspace_id = ?", findingID, workspaceID).
		Take(&pr).Error
	if isNoRows(err) {
		return domain.LearnTarget{}, fmt.Errorf("finding %s: %w", findingID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.LearnTarget{}, fmt.Errorf("select finding context: %w", err)
	}
	f, err := s.findingRecord(ctx, findingID)
	if err != nil {
		return domain.LearnTarget{}, err
	}
	return domain.LearnTarget{Finding: f, RepoID: pr.RepoID, PRNumber: pr.PRNumber}, nil
}

// LockLearn takes the transaction-scoped advisory lock of a finding's Learn. The lock is held
// until the surrounding transaction ends, so it only serialises callers inside Tx.InTx.
func (s *Store) LockLearn(ctx context.Context, findingID string) error {
	// An advisory lock has no builder form.
	err := s.conn(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", domain.LearnLockKey(findingID)).Error
	if err != nil {
		return fmt.Errorf("lock learn: %w", err)
	}
	return nil
}

// learnSources is the jsonb value a memory's sources must contain for a finding.
func learnSources(findingID string) string {
	doc, _ := json.Marshal([]map[string]string{{"context": domain.LearnContext(findingID)}})
	return string(doc)
}

// memoryRef is a memory entry with its sources.
type memoryRef struct {
	ID      string `gorm:"column:id"`
	Sources jsonb  `gorm:"column:sources"`
}

// LearnedMemoryID returns the oldest learning entry of the workspace that came from the finding.
func (s *Store) LearnedMemoryID(ctx context.Context, workspaceID, findingID string) (string, bool, error) {
	var ref memoryRef
	// jsonb containment has no builder form.
	err := s.conn(ctx).Table("memory").Select("id").
		Where("workspace_id = ? AND kind = 'learning' AND sources @> ?::jsonb", workspaceID, learnSources(findingID)).
		Order("created_at, id").Limit(1).Take(&ref).Error
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select learned memory: %w", err)
	}
	return ref.ID, true, nil
}

// InsertLearnedMemory inserts a learning entry (scope repo, no embedding) and returns its id.
func (s *Store) InsertLearnedMemory(ctx context.Context, m domain.NewMemory) (string, error) {
	sources, err := json.Marshal([]map[string]any{{"pr": m.PRNumber, "context": domain.LearnContext(m.FindingID)}})
	if err != nil {
		return "", fmt.Errorf("encode memory sources: %w", err)
	}
	confidence := m.Confidence
	row := memoryRow{
		WorkspaceID: m.WorkspaceID, RepoID: &m.RepoID, Scope: "repo", Kind: "learning",
		Content: clean(m.Content), Confidence: &confidence, Sources: sources,
	}
	if err := s.conn(ctx).Create(&row).Error; err != nil {
		return "", fmt.Errorf("insert learned memory: %w", err)
	}
	return row.ID, nil
}

// memoryIDsByFinding maps each finding to the oldest learning entry of the workspace that came
// from it; findings without one are absent.
func (s *Store) memoryIDsByFinding(ctx context.Context, workspaceID string, findingIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(findingIDs) == 0 {
		return out, nil
	}
	marks := make([]string, len(findingIDs))
	args := make([]any, 0, len(findingIDs)+1)
	args = append(args, workspaceID)
	for i, id := range findingIDs {
		marks[i] = "?::jsonb"
		args = append(args, learnSources(id))
	}
	var rows []memoryRef
	// jsonb containment against several keys has no builder form.
	err := s.conn(ctx).Table("memory").Select("id, sources").
		Where("workspace_id = ? AND kind = 'learning' AND sources @> ANY (ARRAY["+strings.Join(marks, ", ")+"])", args...).
		Order("created_at, id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select learned memory: %w", err)
	}
	for _, r := range rows {
		for _, c := range sourceContexts(r.Sources) {
			if id, ok := strings.CutPrefix(c, domain.LearnContextPrefix); ok {
				if _, seen := out[id]; !seen {
					out[id] = r.ID
				}
			}
		}
	}
	return out, nil
}

// sourceContexts lists the "context" strings of a memory's sources; any other shape yields none.
func sourceContexts(sources jsonb) []string {
	var entries []map[string]any
	if json.Unmarshal(sources, &entries) != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if c, ok := e["context"].(string); ok {
			out = append(out, c)
		}
	}
	return out
}
