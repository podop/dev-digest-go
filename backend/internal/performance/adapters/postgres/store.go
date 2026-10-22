// Package postgres reads the data of the performance context with GORM. Nothing here writes: the
// store runs SELECTs over agents, agent_runs, reviews, findings, agent_run_skills, skills,
// run_traces and pull_requests, all with row types declared in this package, and every query is
// workspace-scoped through the one counted-run filter. Every method takes its handle from
// platformpg.Conn, so a store called inside Tx.InTx joins the caller's transaction.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/performance/app"
	"dev-digest-go/backend/internal/performance/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

// Store is the GORM-backed implementation of app.Reader.
type Store struct {
	db *gorm.DB
}

var _ app.Reader = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

// countedStatuses and countedSources are the values a counted run may have.
var (
	countedStatuses = []string{string(domain.RunDone), string(domain.RunFailed), string(domain.RunCancelled)}
	countedSources  = []string{string(domain.SourceLocal), string(domain.SourceCI)}
)

// counted restricts a query that has agent_runs aliased as r to the counted runs of the window:
// workspace, status, source, ran_at inside the window ([from, to] for presets, [from, to) for
// custom ranges) and, when agentID is not empty, that agent's runs.
func counted(q *gorm.DB, workspaceID string, win domain.PeriodWindow, agentID string) *gorm.DB {
	q = q.Where("r.workspace_id = ? AND r.status IN ? AND r.source IN ? AND r.ran_at >= ?",
		workspaceID, countedStatuses, countedSources, win.From)
	if win.ToInclusive {
		q = q.Where("r.ran_at <= ?", win.To)
	} else {
		q = q.Where("r.ran_at < ?", win.To)
	}
	if agentID != "" {
		q = q.Where("r.agent_id = ?", agentID)
	}
	return q
}

// ListAgents implements app.Reader.
func (s *Store) ListAgents(ctx context.Context, workspaceID string) ([]domain.AgentRef, error) {
	var rows []agentRow
	err := s.conn(ctx).Table("agents").Select("id, name").Where("workspace_id = ?", workspaceID).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select agents: %w", err)
	}
	out := make([]domain.AgentRef, len(rows))
	for i, r := range rows {
		out[i] = domain.AgentRef{ID: r.ID, Name: r.Name}
	}
	return out, nil
}

// FindAgent implements app.Reader.
func (s *Store) FindAgent(ctx context.Context, workspaceID, agentID string) (domain.AgentRef, error) {
	var row agentRow
	err := s.conn(ctx).Table("agents").Select("id, name").
		Where("workspace_id = ? AND id = ?", workspaceID, agentID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.AgentRef{}, fmt.Errorf("agent %s: %w", agentID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.AgentRef{}, fmt.Errorf("select agent: %w", err)
	}
	return domain.AgentRef{ID: row.ID, Name: row.Name}, nil
}
