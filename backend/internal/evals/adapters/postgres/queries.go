package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/evals/domain"
)

// workspaceRuns scopes a query on eval_suite_runs to a workspace.
func (s *Store) workspaceRuns(ctx context.Context, workspaceID string) *gorm.DB {
	return s.conn(ctx).Model(&runRow{}).Where("workspace_id = ?", workspaceID)
}

// ownerRuns narrows a run query to the runs an agent started itself (never the skill runs it
// hosts) or to a skill's runs.
func ownerRuns(tx *gorm.DB, kind domain.OwnerKind, ownerID string) *gorm.DB {
	if kind == domain.OwnerSkill {
		return tx.Where("owner_kind = ? AND skill_id = ?", string(domain.OwnerSkill), ownerID)
	}
	return tx.Where("owner_kind = ? AND agent_id = ?", string(domain.OwnerAgent), ownerID)
}

// GetRun returns one run of the workspace, its frozen configuration included.
func (s *Store) GetRun(ctx context.Context, workspaceID, runID string) (domain.Run, error) {
	if !isUUID(runID) {
		return domain.Run{}, domain.ErrNotFound
	}
	var row runRow
	err := s.workspaceRuns(ctx, workspaceID).Where("id = ?", runID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Run{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("select run: %w", err)
	}
	return row.toDomain()
}

// RunResults returns the run's results in execution order (ran_at, id), each with its case's
// name and expectation; a NULL status is domain.CaseError and a NULL duration 0.
func (s *Store) RunResults(ctx context.Context, workspaceID, runID string) ([]domain.CaseResult, error) {
	if !isUUID(runID) {
		return []domain.CaseResult{}, nil
	}
	var rows []resultRow
	err := s.conn(ctx).Table("eval_runs").
		Select(`eval_runs.case_id AS case_id, eval_cases.name AS name, eval_cases.expected_output AS expected_output,
			eval_runs.status AS status, eval_runs.actual_output AS actual_output, eval_runs.dropped_count AS dropped_count,
			eval_runs.duration_ms AS duration_ms, eval_runs.cost_usd AS cost_usd, eval_runs.error AS error`).
		Joins("JOIN eval_suite_runs ON eval_suite_runs.id = eval_runs.run_id").
		Joins("JOIN eval_cases ON eval_cases.id = eval_runs.case_id").
		Where("eval_runs.run_id = ? AND eval_suite_runs.workspace_id = ?", runID, workspaceID).
		Order("eval_runs.ran_at, eval_runs.id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select run results: %w", err)
	}
	out := make([]domain.CaseResult, len(rows))
	for i, r := range rows {
		if out[i], err = r.toDomain(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListAgentRuns returns runs with owner_kind agent of the agent, started_at desc then id desc, at
// most limit; since non-nil keeps started_at >= since.
func (s *Store) ListAgentRuns(ctx context.Context, workspaceID, agentID string, limit int, since *time.Time) ([]domain.Run, error) {
	return s.runList(ctx, workspaceID, domain.OwnerAgent, agentID, limit, since, false)
}

// ListSkillRuns returns runs with owner_kind skill of the skill, same order, at most limit.
func (s *Store) ListSkillRuns(ctx context.Context, workspaceID, skillID string, limit int) ([]domain.Run, error) {
	return s.runList(ctx, workspaceID, domain.OwnerSkill, skillID, limit, nil, false)
}

// FinishedAgentRuns returns the agent's finished (completed or failed) agent-owned runs, newest
// started_at first, at most limit; since non-nil keeps started_at >= since.
func (s *Store) FinishedAgentRuns(ctx context.Context, workspaceID, agentID string, limit int, since *time.Time) ([]domain.Run, error) {
	return s.runList(ctx, workspaceID, domain.OwnerAgent, agentID, limit, since, true)
}

func (s *Store) runList(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string, limit int, since *time.Time, finishedOnly bool) ([]domain.Run, error) {
	if !isUUID(ownerID) || limit <= 0 {
		return []domain.Run{}, nil
	}
	tx := ownerRuns(s.workspaceRuns(ctx, workspaceID), kind, ownerID)
	if finishedOnly {
		tx = tx.Where("status IN ?", finishedStatuses)
	}
	if since != nil {
		tx = tx.Where("started_at >= ?", *since)
	}
	var rows []runRow
	if err := tx.Order("started_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select runs: %w", err)
	}
	return runsToDomain(rows)
}

// overviewColumns are the columns of eval_suite_runs the overview needs: every field of the run
// DTO, and from config (system prompt and skill bodies make it large) only the model. case_ids is
// not read: Run.CaseIDs stays empty on these rows.
const overviewColumns = `eval_suite_runs.id, workspace_id, owner_kind, skill_id, skill_version, agent_id,
	agent_version, agent_name, jsonb_build_object('model', config->'model') AS config, status,
	started_at, finished_at, cases_total, cases_finished, cases_passed, cases_errored, recall,
	"precision", citation_accuracy, unlabeled_findings, cost_usd, duration_ms, error`

// FinishedRunsByAgent returns, for each of the agents, its newest finished (completed or failed)
// agent-owned runs, newest started_at first, at most limit per agent: what FinishedAgentRuns
// returns, for all agents in one round trip. Agents without such a run have no key. The runs
// carry only the overview columns (see overviewColumns).
func (s *Store) FinishedRunsByAgent(ctx context.Context, workspaceID string, agentIDs []string, limit int) (map[string][]domain.Run, error) {
	out := map[string][]domain.Run{}
	ids := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		if isUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || limit <= 0 {
		return out, nil
	}
	// The window ranks the narrow (id, rn) rows; the wide columns are read for the survivors only,
	// so the config of runs beyond the limit is never touched.
	ranked := s.workspaceRuns(ctx, workspaceID).
		Select("id, ROW_NUMBER() OVER (PARTITION BY agent_id ORDER BY started_at DESC, id DESC) AS rn").
		Where("owner_kind = ? AND status IN ? AND agent_id IN ?", string(domain.OwnerAgent), finishedStatuses, ids)
	var rows []runRow
	err := s.conn(ctx).Table("(?) AS ranked", ranked).
		Joins("JOIN eval_suite_runs ON eval_suite_runs.id = ranked.id").
		Select(overviewColumns).
		Where("ranked.rn <= ?", limit).
		Order("eval_suite_runs.agent_id, ranked.rn").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select finished runs by agent: %w", err)
	}
	for _, row := range rows {
		run, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		out[run.AgentID] = append(out[run.AgentID], run)
	}
	return out, nil
}

// DashboardAgents returns the agents of the workspace that own a case or an agent-owned run,
// ordered by name then id (database default collation).
func (s *Store) DashboardAgents(ctx context.Context, workspaceID string) ([]domain.AgentRef, error) {
	var rows []agentRow
	err := s.conn(ctx).Table("agents").
		Select("id, name, model").
		Where(`workspace_id = ? AND (
			EXISTS (SELECT 1 FROM eval_cases WHERE eval_cases.owner_kind = 'agent' AND eval_cases.owner_id = agents.id)
			OR EXISTS (SELECT 1 FROM eval_suite_runs WHERE eval_suite_runs.owner_kind = 'agent' AND eval_suite_runs.agent_id = agents.id))`,
			workspaceID).
		Order("name, id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select dashboard agents: %w", err)
	}
	out := make([]domain.AgentRef, len(rows))
	for i, r := range rows {
		out[i] = domain.AgentRef{ID: r.ID, Name: r.Name, Model: r.Model}
	}
	return out, nil
}

// RecentAgentRuns returns the newest agent-owned runs of the workspace, any status, with the
// agents' current names. The runs carry only the overview columns (see overviewColumns).
func (s *Store) RecentAgentRuns(ctx context.Context, workspaceID string, limit int) ([]domain.RecentRun, error) {
	if limit <= 0 {
		return []domain.RecentRun{}, nil
	}
	var rows []runRow
	err := s.workspaceRuns(ctx, workspaceID).Select(overviewColumns).Where("owner_kind = ?", string(domain.OwnerAgent)).
		Order("started_at DESC, id DESC").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select recent runs: %w", err)
	}
	runs, err := runsToDomain(rows)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	if len(runs) > 0 {
		ids := make([]string, len(runs))
		for i, r := range runs {
			ids[i] = r.AgentID
		}
		var agents []agentRow
		if err := s.conn(ctx).Table("agents").Select("id, name").Where("id IN ?", ids).Scan(&agents).Error; err != nil {
			return nil, fmt.Errorf("select recent run agents: %w", err)
		}
		for _, a := range agents {
			names[a.ID] = a.Name
		}
	}
	out := make([]domain.RecentRun, len(runs))
	for i, r := range runs {
		out[i] = domain.RecentRun{Run: r, AgentName: names[r.AgentID]}
	}
	return out, nil
}
