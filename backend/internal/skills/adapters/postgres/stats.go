package postgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/skills/domain"
)

// countRow is one grouped count of the statistics queries.
type countRow struct {
	SkillID   string `gorm:"column:skill_id"`
	N         int    `gorm:"column:n"`
	Accepted  int    `gorm:"column:accepted"`
	Dismissed int    `gorm:"column:dismissed"`
}

// keyCountRow is one count per category or severity.
type keyCountRow struct {
	K string `gorm:"column:k"`
	N int    `gorm:"column:n"`
}

// Counters returns the statistics per skill since the given time; skillID "" means every skill
// of the workspace. Only runs that finished (status done) count as attached; findings are
// windowed by the creation time of their review. Skills without data are absent.
func (s *Store) Counters(ctx context.Context, workspaceID string, since time.Time, skillID string) (map[string]domain.Counters, error) {
	only := func(q *gorm.DB, column string) *gorm.DB {
		if skillID == "" {
			return q
		}
		return q.Where(column+" = ?", skillID)
	}

	var attached, cited, findings []countRow
	err := only(s.conn(ctx).Table("agent_run_skills AS ars").
		Select("ars.skill_id AS skill_id, count(DISTINCT ars.run_id) AS n").
		Joins("JOIN agent_runs r ON r.id = ars.run_id").
		Where("r.workspace_id = ? AND r.status = 'done' AND r.ran_at >= ?", workspaceID, since).
		Group("ars.skill_id"), "ars.skill_id").Scan(&attached).Error
	if err != nil {
		return nil, fmt.Errorf("count attached runs: %w", err)
	}
	err = only(s.conn(ctx).Table("findings AS f").
		Select("f.skill_id AS skill_id, count(DISTINCT rv.run_id) AS n").
		Joins("JOIN reviews rv ON rv.id = f.review_id").
		Joins("JOIN agent_runs r ON r.id = rv.run_id").
		Joins("JOIN agent_run_skills ars ON ars.run_id = rv.run_id AND ars.skill_id = f.skill_id").
		Where("r.workspace_id = ? AND r.status = 'done' AND r.ran_at >= ?", workspaceID, since).
		Group("f.skill_id"), "f.skill_id").Scan(&cited).Error
	if err != nil {
		return nil, fmt.Errorf("count citing runs: %w", err)
	}
	err = only(s.conn(ctx).Table("findings AS f").
		Select("f.skill_id AS skill_id, count(*) AS n, count(f.accepted_at) AS accepted, count(f.dismissed_at) AS dismissed").
		Joins("JOIN reviews rv ON rv.id = f.review_id").
		Where("rv.workspace_id = ? AND f.skill_id IS NOT NULL AND rv.created_at >= ?", workspaceID, since).
		Group("f.skill_id"), "f.skill_id").Scan(&findings).Error
	if err != nil {
		return nil, fmt.Errorf("count findings: %w", err)
	}

	out := map[string]domain.Counters{}
	for _, r := range attached {
		c := out[r.SkillID]
		c.RunsAttached = r.N
		out[r.SkillID] = c
	}
	for _, r := range cited {
		c := out[r.SkillID]
		c.RunsCited = r.N
		out[r.SkillID] = c
	}
	for _, r := range findings {
		c := out[r.SkillID]
		c.Findings, c.Accepted, c.Dismissed = r.N, r.Accepted, r.Dismissed
		out[r.SkillID] = c
	}
	return out, nil
}

// Breakdown groups the findings of the skill since the given time by category and by severity,
// each by count descending, then key ascending.
func (s *Store) Breakdown(ctx context.Context, workspaceID, skillID string, since time.Time) (domain.Breakdown, error) {
	group := func(column string) ([]domain.CountBy, error) {
		var rows []keyCountRow
		err := s.conn(ctx).Table("findings AS f").
			Select(column+" AS k, count(*) AS n").
			Joins("JOIN reviews rv ON rv.id = f.review_id").
			Where("rv.workspace_id = ? AND f.skill_id = ? AND rv.created_at >= ?", workspaceID, skillID, since).
			Group(column).Order("n DESC, k").Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		out := make([]domain.CountBy, len(rows))
		for i, r := range rows {
			out[i] = domain.CountBy{Key: r.K, Count: r.N}
		}
		return out, nil
	}
	byCategory, err := group("f.category")
	if err != nil {
		return domain.Breakdown{}, fmt.Errorf("group findings by category: %w", err)
	}
	bySeverity, err := group("f.severity")
	if err != nil {
		return domain.Breakdown{}, fmt.Errorf("group findings by severity: %w", err)
	}
	return domain.Breakdown{ByCategory: byCategory, BySeverity: bySeverity}, nil
}
