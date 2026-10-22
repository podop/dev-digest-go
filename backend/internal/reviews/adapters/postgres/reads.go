package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// ---- Lookups ----

// FindPull returns a pull request of the workspace.
func (s *Store) FindPull(ctx context.Context, workspaceID, prID string) (domain.ReviewPull, error) {
	var row pullRow
	err := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, prID).Take(&row).Error
	if isNoRows(err) {
		return domain.ReviewPull{}, fmt.Errorf("pull request %s: %w", prID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.ReviewPull{}, fmt.Errorf("select pull request: %w", err)
	}
	return domain.ReviewPull{
		ID: row.ID, WorkspaceID: row.WorkspaceID, RepoID: row.RepoID, Number: row.Number,
		Title: row.Title, Author: row.Author, Base: row.Base, Branch: row.Branch, HeadSHA: row.HeadSHA, Body: row.Body,
	}, nil
}

// FindRepo returns a repository by id.
func (s *Store) FindRepo(ctx context.Context, repoID string) (domain.ReviewRepo, error) {
	var row repoRow
	err := s.conn(ctx).Where("id = ?", repoID).Take(&row).Error
	if isNoRows(err) {
		return domain.ReviewRepo{}, fmt.Errorf("repo %s: %w", repoID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.ReviewRepo{}, fmt.Errorf("select repo: %w", err)
	}
	return domain.ReviewRepo{ID: row.ID, Owner: row.Owner, Name: row.Name}, nil
}

func (r agentRow) toDomain() domain.ReviewAgent {
	return domain.ReviewAgent{
		ID: r.ID, Name: r.Name, Provider: r.Provider, Model: r.Model, SystemPrompt: r.SystemPrompt,
		Strategy: reviewer.Strategy(r.Strategy), CIFailOn: reviewer.FailOn(r.CIFailOn),
		RepoIntel: r.RepoIntel, Version: r.Version,
	}
}

// FindAgent returns an agent of the workspace, enabled or not. An id that is not a UUID is
// ErrNotFound without a query.
func (s *Store) FindAgent(ctx context.Context, workspaceID, agentID string) (domain.ReviewAgent, error) {
	if !isUUID(agentID) {
		return domain.ReviewAgent{}, fmt.Errorf("agent %q: %w", agentID, domain.ErrNotFound)
	}
	var row agentRow
	err := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, agentID).Take(&row).Error
	if isNoRows(err) {
		return domain.ReviewAgent{}, fmt.Errorf("agent %s: %w", agentID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.ReviewAgent{}, fmt.Errorf("select agent: %w", err)
	}
	return row.toDomain(), nil
}

// EnabledAgents returns the workspace's enabled agents by name (byte order), then id.
func (s *Store) EnabledAgents(ctx context.Context, workspaceID string) ([]domain.ReviewAgent, error) {
	var rows []agentRow
	err := s.conn(ctx).Where("workspace_id = ? AND enabled", workspaceID).
		Order(`name COLLATE "C", id`).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select enabled agents: %w", err)
	}
	out := make([]domain.ReviewAgent, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// EnabledSkills returns the agent's linked skills that are enabled, in link order.
func (s *Store) EnabledSkills(ctx context.Context, agentID string) ([]domain.ReviewSkill, error) {
	var rows []skillRow
	err := s.conn(ctx).Table("agent_skills").
		Select("skills.id, skills.name, skills.description, skills.body, skills.version").
		Joins("JOIN skills ON skills.id = agent_skills.skill_id").
		Where("agent_skills.agent_id = ? AND skills.enabled", agentID).
		Order(`agent_skills."order", skills.id`).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select enabled skills: %w", err)
	}
	out := make([]domain.ReviewSkill, len(rows))
	for i, r := range rows {
		out[i] = domain.ReviewSkill{ID: r.ID, Name: r.Name, Description: r.Description, Body: r.Body, Version: r.Version}
	}
	return out, nil
}

// PullFiles returns the pull request's files that have a patch, in path byte order.
func (s *Store) PullFiles(ctx context.Context, prID string) ([]reviewer.PatchFile, error) {
	var rows []prFileRow
	err := s.conn(ctx).Table("pr_files").Select("path, patch").
		Where("pr_id = ? AND patch IS NOT NULL AND patch <> ''", prID).
		Order(`path COLLATE "C"`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select pull request files: %w", err)
	}
	out := make([]reviewer.PatchFile, len(rows))
	for i, r := range rows {
		out[i] = reviewer.PatchFile{Path: r.Path, Patch: r.Patch}
	}
	return out, nil
}

// ---- runs ----

// runStateRow is the status of a run.
type runStateRow struct {
	ID     string  `gorm:"column:id"`
	Status *string `gorm:"column:status"`
	Error  *string `gorm:"column:error"`
}

// FindRun returns the status and failure note of a run of the workspace.
func (s *Store) FindRun(ctx context.Context, workspaceID, runID string) (domain.RunState, error) {
	var row runStateRow
	err := s.conn(ctx).Table("agent_runs").Select("id, status, error").
		Where("id = ? AND workspace_id = ?", runID, workspaceID).Take(&row).Error
	if isNoRows(err) {
		return domain.RunState{}, fmt.Errorf("run %s: %w", runID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.RunState{}, fmt.Errorf("select run: %w", err)
	}
	return domain.RunState{ID: row.ID, Status: statusOf(row.Status), Error: row.Error}, nil
}

func statusOf(s *string) domain.RunStatus {
	if s == nil {
		return ""
	}
	return domain.RunStatus(*s)
}

// activeRunRow is a running run with its agent's name.
type activeRunRow struct {
	RunID     string     `gorm:"column:run_id"`
	AgentID   *string    `gorm:"column:agent_id"`
	AgentName *string    `gorm:"column:agent_name"`
	RanAt     *time.Time `gorm:"column:ran_at"`
}

// ActiveRuns returns the pull request's running runs by ran_at, then id ascending.
func (s *Store) ActiveRuns(ctx context.Context, workspaceID, prID string) ([]domain.ActiveRun, error) {
	var rows []activeRunRow
	err := s.conn(ctx).Table("agent_runs").
		Select("agent_runs.id AS run_id, agent_runs.agent_id, agents.name AS agent_name, agent_runs.ran_at").
		Joins("LEFT JOIN agents ON agents.id = agent_runs.agent_id").
		Where("agent_runs.workspace_id = ? AND agent_runs.pr_id = ? AND agent_runs.status = ?",
			workspaceID, prID, string(domain.StatusRunning)).
		Order("agent_runs.ran_at ASC, agent_runs.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select active runs: %w", err)
	}
	out := make([]domain.ActiveRun, len(rows))
	for i, r := range rows {
		out[i] = domain.ActiveRun{RunID: r.RunID, AgentID: r.AgentID, AgentName: r.AgentName, RanAt: utcPtr(r.RanAt)}
	}
	return out, nil
}

// runSummaryRow is one run of a pull request's history.
type runSummaryRow struct {
	RunID         string     `gorm:"column:run_id"`
	AgentID       *string    `gorm:"column:agent_id"`
	AgentName     *string    `gorm:"column:agent_name"`
	Provider      *string    `gorm:"column:provider"`
	Model         *string    `gorm:"column:model"`
	Status        *string    `gorm:"column:status"`
	Error         *string    `gorm:"column:error"`
	DurationMS    *int       `gorm:"column:duration_ms"`
	TokensIn      *int       `gorm:"column:tokens_in"`
	TokensOut     *int       `gorm:"column:tokens_out"`
	CostUSD       *float64   `gorm:"column:cost_usd"`
	FindingsCount *int       `gorm:"column:findings_count"`
	Grounding     *string    `gorm:"column:grounding"`
	RanAt         *time.Time `gorm:"column:ran_at"`
	Score         *int       `gorm:"column:score"`
	Blockers      *int       `gorm:"column:blockers"`
}

// ListRuns returns every run of the pull request, newest ran_at first, ties by id ascending.
func (s *Store) ListRuns(ctx context.Context, workspaceID, prID string) ([]domain.RunSummary, error) {
	var rows []runSummaryRow
	err := s.conn(ctx).Table("agent_runs").
		Select("agent_runs.id AS run_id, agent_runs.agent_id, agents.name AS agent_name, agent_runs.provider, "+
			"agent_runs.model, agent_runs.status, agent_runs.error, agent_runs.duration_ms, agent_runs.tokens_in, "+
			"agent_runs.tokens_out, agent_runs.cost_usd, agent_runs.findings_count, agent_runs.grounding, "+
			"agent_runs.ran_at, agent_runs.score, agent_runs.blockers").
		Joins("LEFT JOIN agents ON agents.id = agent_runs.agent_id").
		Where("agent_runs.workspace_id = ? AND agent_runs.pr_id = ?", workspaceID, prID).
		Order("agent_runs.ran_at DESC, agent_runs.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select runs: %w", err)
	}
	out := make([]domain.RunSummary, len(rows))
	for i, r := range rows {
		out[i] = domain.RunSummary{
			RunID: r.RunID, AgentID: r.AgentID, AgentName: r.AgentName, Provider: r.Provider, Model: r.Model,
			Status: statusOf(r.Status), Error: r.Error, DurationMS: r.DurationMS, TokensIn: r.TokensIn,
			TokensOut: r.TokensOut, CostUSD: r.CostUSD, FindingsCount: r.FindingsCount, Grounding: r.Grounding,
			RanAt: utcPtr(r.RanAt), Score: r.Score, Blockers: r.Blockers,
		}
	}
	return out, nil
}

// FindTrace returns the trace of a run of the workspace, decoded strictly.
func (s *Store) FindTrace(ctx context.Context, workspaceID, runID string) (domain.Trace, error) {
	var row traceRow
	err := s.conn(ctx).Table("run_traces").Select("run_traces.run_id, run_traces.trace").
		Joins("JOIN agent_runs ON agent_runs.id = run_traces.run_id").
		Where("run_traces.run_id = ? AND agent_runs.workspace_id = ?", runID, workspaceID).
		Take(&row).Error
	if isNoRows(err) {
		return domain.Trace{}, fmt.Errorf("trace of run %s: %w", runID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Trace{}, fmt.Errorf("select trace: %w", err)
	}
	return DecodeTrace(row.Trace)
}

// ---- reviews and findings ----

func (r findingRow) toDomain(evalCaseID, memoryID *string) (domain.FindingRecord, error) {
	var components []string
	if len(r.TrifectaComponents) > 0 {
		if err := json.Unmarshal(r.TrifectaComponents, &components); err != nil {
			return domain.FindingRecord{}, fmt.Errorf("decode trifecta_components of finding %s: %w", r.ID, err)
		}
	}
	return domain.FindingRecord{
		ID: r.ID, Severity: reviewer.Severity(r.Severity), Category: reviewer.Category(r.Category),
		Title: r.Title, File: r.File, StartLine: r.StartLine, EndLine: r.EndLine, Rationale: r.Rationale,
		Suggestion: r.Suggestion, Confidence: r.Confidence, Kind: reviewer.Kind(r.Kind),
		TrifectaComponents: components, Skill: r.SkillName, OutOfScope: r.OutOfScope, ReviewID: r.ReviewID,
		AcceptedAt: utcPtr(r.AcceptedAt), DismissedAt: utcPtr(r.DismissedAt),
		EvalCaseID: evalCaseID, MemoryID: memoryID,
	}, nil
}

// evalCaseIDs maps each finding to the eval case created from it; findings without one are absent.
func (s *Store) evalCaseIDs(ctx context.Context, findingIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(findingIDs) == 0 {
		return out, nil
	}
	var rows []evalCaseRow
	err := s.conn(ctx).Table("eval_cases").Select("id, source_finding_id").
		Where("source_finding_id IN ?", findingIDs).Order("created_at, id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select eval cases: %w", err)
	}
	for _, r := range rows {
		if _, seen := out[r.SourceFindingID]; !seen {
			out[r.SourceFindingID] = r.ID
		}
	}
	return out, nil
}

func lookup(m map[string]string, id string) *string {
	if v, ok := m[id]; ok {
		return &v
	}
	return nil
}

// findingRecord reads one finding with its eval case (no memory id).
func (s *Store) findingRecord(ctx context.Context, findingID string) (domain.FindingRecord, error) {
	var row findingRow
	err := s.conn(ctx).Where("id = ?", findingID).Take(&row).Error
	if isNoRows(err) {
		return domain.FindingRecord{}, fmt.Errorf("finding %s: %w", findingID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.FindingRecord{}, fmt.Errorf("select finding: %w", err)
	}
	cases, err := s.evalCaseIDs(ctx, []string{findingID})
	if err != nil {
		return domain.FindingRecord{}, err
	}
	return row.toDomain(lookup(cases, findingID), nil)
}

// findingsOfReviews reads the findings of the given reviews, grouped by review, each group in
// insertion order. The findings table keeps no creation column or sequence, so the order is the
// physical order of the rows (ctid): an INSERT of one review is one append, as in the Node
// server, whose unordered query returned the same order. A later UPDATE (accept, dismiss) writes
// a new row version, which can move that finding after its siblings.
func (s *Store) findingsOfReviews(ctx context.Context, reviewIDs []string) (map[string][]findingRow, error) {
	out := map[string][]findingRow{}
	if len(reviewIDs) == 0 {
		return out, nil
	}
	var rows []findingRow
	if err := s.conn(ctx).Where("review_id IN ?", reviewIDs).Order("ctid").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select findings: %w", err)
	}
	for _, r := range rows {
		out[r.ReviewID] = append(out[r.ReviewID], r)
	}
	return out, nil
}

// reviewListRow is a review with its agent's name and its run's usage.
type reviewListRow struct {
	ID           string    `gorm:"column:id"`
	PRID         string    `gorm:"column:pr_id"`
	Kind         string    `gorm:"column:kind"`
	AgentID      *string   `gorm:"column:agent_id"`
	RunID        *string   `gorm:"column:run_id"`
	AgentName    *string   `gorm:"column:agent_name"`
	Verdict      *string   `gorm:"column:verdict"`
	Summary      *string   `gorm:"column:summary"`
	Score        *int      `gorm:"column:score"`
	Model        *string   `gorm:"column:model"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	RunCostUSD   *float64  `gorm:"column:run_cost_usd"`
	RunTokensIn  *int      `gorm:"column:run_tokens_in"`
	RunTokensOut *int      `gorm:"column:run_tokens_out"`
}

// ReviewsForPull returns the pull request's reviews newest first (ties by id), each with its
// findings in insertion order, its agent's name and, when it has a run, the run's usage.
func (s *Store) ReviewsForPull(ctx context.Context, prID string) ([]domain.ReviewRecord, error) {
	var rows []reviewListRow
	err := s.conn(ctx).Table("reviews").
		Select("reviews.id, reviews.pr_id, reviews.kind, reviews.agent_id, reviews.run_id, "+
			"agents.name AS agent_name, reviews.verdict, reviews.summary, reviews.score, reviews.model, "+
			"reviews.created_at, agent_runs.cost_usd AS run_cost_usd, agent_runs.tokens_in AS run_tokens_in, "+
			"agent_runs.tokens_out AS run_tokens_out").
		Joins("LEFT JOIN agents ON agents.id = reviews.agent_id").
		Joins("LEFT JOIN agent_runs ON agent_runs.id = reviews.run_id").
		Where("reviews.pr_id = ?", prID).
		Order("reviews.created_at DESC, reviews.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select reviews: %w", err)
	}
	if len(rows) == 0 {
		return []domain.ReviewRecord{}, nil
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	byReview, err := s.findingsOfReviews(ctx, ids)
	if err != nil {
		return nil, err
	}
	var findingIDs []string
	for _, fs := range byReview {
		for _, f := range fs {
			findingIDs = append(findingIDs, f.ID)
		}
	}
	cases, err := s.evalCaseIDs(ctx, findingIDs)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ReviewRecord, len(rows))
	for i, r := range rows {
		rec := domain.ReviewRecord{
			ID: r.ID, PRID: r.PRID, Kind: r.Kind, AgentID: r.AgentID, RunID: r.RunID, AgentName: r.AgentName,
			Verdict: r.Verdict, Summary: r.Summary, Score: r.Score, Model: r.Model,
			Findings:  make([]domain.FindingRecord, 0, len(byReview[r.ID])),
			CreatedAt: utc(r.CreatedAt),
		}
		if r.RunID != nil {
			rec.Usage = &domain.RunUsage{CostUSD: r.RunCostUSD, TokensIn: r.RunTokensIn, TokensOut: r.RunTokensOut}
		}
		for _, f := range byReview[r.ID] {
			fr, err := f.toDomain(lookup(cases, f.ID), nil)
			if err != nil {
				return nil, err
			}
			rec.Findings = append(rec.Findings, fr)
		}
		out[i] = rec
	}
	return out, nil
}
