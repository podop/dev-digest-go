// Package postgres persists the automatic-review trigger with GORM: the per-(PR, head SHA)
// records of auto_review_records and the reads of other contexts' tables (pull_requests, repos,
// agents, agent_runs) that decide candidacy. Every method is workspace-scoped and takes its handle
// from platformpg.Conn, so a store called inside Tx.InTx joins the caller's transaction.
package postgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/autoreview/app"
	"dev-digest-go/backend/internal/autoreview/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

// Store is the GORM-backed implementation of app.Store.
type Store struct {
	db *gorm.DB
}

var _ app.Store = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

// closedStatuses are the stored PR statuses that are not open (pulls/domain.DeriveStatus treats
// every other stored status as open).
var closedStatuses = []string{"merged", "closed"}

// Candidates implements app.Store. One set-based statement (NFR1): the FR4 predicate on
// pull_requests (open, not draft, opened_at >= enabledAt, no record for the current head SHA), the
// has-any-record and running facts as EXISTS subqueries (unique key (pr_id, head_sha) and
// agent_runs_running_idx), ordered as domain.SortCandidates does: updated_at NULLS LAST, then id.
// Rows without an opened time or head never match (domain.Eligible). Fork and unknown-head-repository
// rows are returned too: the trigger records the forks (skipped_fork, D13) and defers the unknown.
// An unknown head repository also reads as a fork (is_fork defaults to true): Decide defers on
// HeadRepoUnknown first, and should that check ever be lost the PR still counts as a fork.
func (s *Store) Candidates(ctx context.Context, workspaceID string, enabledAt time.Time) ([]domain.Candidate, error) {
	var rows []candidateRow
	err := s.conn(ctx).Table("pull_requests AS p").
		Select(`p.id AS pr_id, r.full_name AS repo, p.number, p.head_sha,
			COALESCE(p.last_reviewed_sha, '') AS last_reviewed_sha,
			COALESCE(p.head_is_fork, true) AS is_fork, p.head_is_fork IS NULL AS head_repo_unknown,
			EXISTS (SELECT 1 FROM auto_review_records a WHERE a.pr_id = p.id) AS has_any_record,
			EXISTS (SELECT 1 FROM agent_runs ar WHERE ar.pr_id = p.id AND ar.status = 'running') AS running,
			p.updated_at`).
		Joins("JOIN repos r ON r.id = p.repo_id AND r.workspace_id = p.workspace_id").
		Where("p.workspace_id = ?", workspaceID).
		Where("p.status NOT IN ?", closedStatuses).
		Where("NOT p.draft").
		Where("p.head_sha <> ''").
		Where("p.opened_at IS NOT NULL AND p.opened_at >= ?", enabledAt.UTC()).
		Where("NOT EXISTS (SELECT 1 FROM auto_review_records h WHERE h.pr_id = p.id AND h.head_sha = p.head_sha)").
		Order("p.updated_at ASC NULLS LAST").Order("p.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select auto-review candidates: %w", err)
	}
	out := make([]domain.Candidate, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// EnabledAgents implements app.Store. Ids are compared as text so a malformed id matches nothing
// instead of failing the whole statement. An agent owned by a disabled plugin is left out (SCR1): auto
// reviews skip it silently, without the 409 a named start would answer.
func (s *Store) EnabledAgents(ctx context.Context, workspaceID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []idRow
	err := s.conn(ctx).Table("agents").Select("id").
		Where("workspace_id = ? AND enabled AND id::text IN ? AND "+
			"(plugin_id IS NULL OR EXISTS (SELECT 1 FROM installed_plugins p WHERE p.id = agents.plugin_id AND p.enabled))",
			workspaceID, ids).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select enabled agents: %w", err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out, nil
}

// StartedSince implements app.Store. It counts every `started` record, claimed-but-unsettled ones
// (zero runs) included, so a start in flight already counts against the rate limit.
func (s *Store) StartedSince(ctx context.Context, workspaceID string, since time.Time) (int, error) {
	var n int64
	err := s.conn(ctx).Model(&recordRow{}).
		Where("workspace_id = ? AND outcome = ? AND decided_at >= ?", workspaceID, string(domain.OutcomeStarted), since.UTC()).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count started auto-review records: %w", err)
	}
	return int(n), nil
}

// Claim implements app.Store: INSERT ... ON CONFLICT (pr_id, head_sha) DO NOTHING. The unique key
// is the at-most-once guard (NFR4), so it holds across evaluations, processes and restarts.
func (s *Store) Claim(ctx context.Context, rec domain.Record) (bool, error) {
	if !rec.Event.Valid() || !rec.Outcome.Valid() {
		return false, fmt.Errorf("claim auto-review record: invalid event %q or outcome %q", rec.Event, rec.Outcome)
	}
	row := fromRecord(rec)
	res := s.conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "pr_id"}, {Name: "head_sha"}},
		DoNothing: true,
	}).Create(&row)
	if res.Error != nil {
		return false, fmt.Errorf("insert auto-review record: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// Finish implements app.Store. It settles a claimed `started` record only: a record that holds a
// skip outcome is never changed. Outcome `failed` always stores zero runs.
func (s *Store) Finish(ctx context.Context, workspaceID, prID, headSHA string, outcome domain.Outcome, runs int) error {
	switch outcome {
	case domain.OutcomeStarted:
	case domain.OutcomeFailed:
		runs = 0
	default:
		return fmt.Errorf("finish auto-review record: outcome %q is not started or failed", outcome)
	}
	if runs < 0 {
		return fmt.Errorf("finish auto-review record: negative runs %d", runs)
	}
	res := s.conn(ctx).Model(&recordRow{}).
		Where("workspace_id = ? AND pr_id = ? AND head_sha = ? AND outcome = ?",
			workspaceID, prID, headSHA, string(domain.OutcomeStarted)).
		Updates(map[string]any{"outcome": string(outcome), "runs_started": runs})
	if res.Error != nil {
		return fmt.Errorf("update auto-review record: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("finish auto-review record: no claimed record for PR %s at %s", prID, headSHA)
	}
	return nil
}

// StatusFacts implements app.Store: the newest repository poll time and the number of stored open
// PRs, drafts included (D12).
func (s *Store) StatusFacts(ctx context.Context, workspaceID string) (app.PollFacts, error) {
	var last lastPollRow
	if err := s.conn(ctx).Table("repos").Select("MAX(last_polled_at) AS last_poll_at").
		Where("workspace_id = ?", workspaceID).Scan(&last).Error; err != nil {
		return app.PollFacts{}, fmt.Errorf("select last poll time: %w", err)
	}
	var n int64
	if err := s.conn(ctx).Table("pull_requests").
		Where("workspace_id = ? AND status NOT IN ?", workspaceID, closedStatuses).
		Count(&n).Error; err != nil {
		return app.PollFacts{}, fmt.Errorf("count open pull requests: %w", err)
	}
	return app.PollFacts{LastPollAt: last.LastPollAt, PRsTracked: int(n)}, nil
}
