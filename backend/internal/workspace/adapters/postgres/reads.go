package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/workspace/app"
	"dev-digest-go/backend/internal/workspace/domain"
)

var (
	_ app.Reader     = (*Store)(nil)
	_ app.Workspaces = (*Store)(nil)
)

// statsSQL counts the six things a workspace holds in one statement (spec FR11): six scalar
// subqueries, each a COUNT over one table filtered by workspace_id (agent_runs through
// agent_runs_ws_ran_idx), so the database aggregates and one row comes back. Raw because the
// builder cannot express a FROM-less SELECT of subqueries. Eval cases and memory carry their own
// workspace_id; the repos count is the repos table. learnings are all memory rows, runs are agent
// runs of any source and status.
const statsSQL = `
SELECT
	(SELECT count(*) FROM agents       WHERE workspace_id = @ws) AS agents,
	(SELECT count(*) FROM skills       WHERE workspace_id = @ws) AS skills,
	(SELECT count(*) FROM eval_cases   WHERE workspace_id = @ws) AS eval_cases,
	(SELECT count(*) FROM memory       WHERE workspace_id = @ws) AS learnings,
	(SELECT count(*) FROM repos        WHERE workspace_id = @ws) AS repos,
	(SELECT count(*) FROM agent_runs   WHERE workspace_id = @ws) AS runs`

// statsRow maps the one row of statsSQL.
type statsRow struct {
	Agents    int64 `gorm:"column:agents"`
	Skills    int64 `gorm:"column:skills"`
	EvalCases int64 `gorm:"column:eval_cases"`
	Learnings int64 `gorm:"column:learnings"`
	Repos     int64 `gorm:"column:repos"`
	Runs      int64 `gorm:"column:runs"`
}

// Stats returns the workspace's counts.
func (s *Store) Stats(ctx context.Context, workspaceID string) (domain.Stats, error) {
	var row statsRow
	if err := platformpg.Conn(ctx, s.db).Raw(statsSQL, sql.Named("ws", workspaceID)).Scan(&row).Error; err != nil {
		return domain.Stats{}, fmt.Errorf("workspace stats: %w", err)
	}
	return domain.Stats{
		Agents: row.Agents, Skills: row.Skills, EvalCases: row.EvalCases,
		Learnings: row.Learnings, Repos: row.Repos, Runs: row.Runs,
	}, nil
}

// WorkspaceIDs lists every workspace id, in a stable order.
func (s *Store) WorkspaceIDs(ctx context.Context) ([]string, error) {
	ids := []string{}
	err := platformpg.Conn(ctx, s.db).Table("workspaces").
		Select("id::text").Order("created_at, id").Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	return ids, nil
}

// ExportData reads the configuration records of an export in one read-only repeatable-read
// transaction, so every list comes from the same snapshot. The lists are in a stable order:
// agents in creation order, names and paths compared bytewise (COLLATE "C"), skill versions
// ascending. It never selects an embedding, a learning's sources or an id other than the ones
// that link records (agent and skill ids for the eval cases' owners). Settings stay nil.
func (s *Store) ExportData(ctx context.Context, workspaceID string) (domain.ExportData, error) {
	var data domain.ExportData
	err := platformpg.Conn(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		var err error
		if data.Agents, err = readAgents(tx, workspaceID); err != nil {
			return err
		}
		if data.Skills, err = readSkills(tx, workspaceID); err != nil {
			return err
		}
		if data.EvalCases, err = readEvalCases(tx, workspaceID); err != nil {
			return err
		}
		if data.Conventions, err = readConventions(tx, workspaceID); err != nil {
			return err
		}
		if data.Learnings, err = readLearnings(tx, workspaceID); err != nil {
			return err
		}
		data.ContextFiles, err = readContextFiles(tx, workspaceID)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.ExportData{}, fmt.Errorf("workspace export data: %w", err)
	}
	return data, nil
}

// raw returns a JSON column read as text (nil for SQL NULL) as raw JSON. Columns are selected
// as col::text: the driver then always hands over a string, whatever the jsonb codec does.
func raw(text *string) json.RawMessage {
	if text == nil {
		return nil
	}
	return json.RawMessage(*text)
}

type agentRow struct {
	ID           string  `gorm:"column:id"`
	Name         string  `gorm:"column:name"`
	Description  string  `gorm:"column:description"`
	Provider     string  `gorm:"column:provider"`
	Model        string  `gorm:"column:model"`
	SystemPrompt string  `gorm:"column:system_prompt"`
	OutputSchema *string `gorm:"column:output_schema"`
	Enabled      bool    `gorm:"column:enabled"`
	Strategy     string  `gorm:"column:strategy"`
	CIFailOn     string  `gorm:"column:ci_fail_on"`
	RepoIntel    bool    `gorm:"column:repo_intel"`
}

type agentSkillRow struct {
	AgentID   string `gorm:"column:agent_id"`
	SkillName string `gorm:"column:skill_name"`
}

func readAgents(tx *gorm.DB, ws string) ([]domain.AgentRecord, error) {
	var rows []agentRow
	err := tx.Table("agents").
		Select(`id::text AS id, name, description, provider, model, system_prompt,
			output_schema::text AS output_schema, enabled, strategy, ci_fail_on, repo_intel`).
		Where("workspace_id = ?", ws).
		Order("created_at, id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}

	// Linked skill names in link order, for all agents at once (no query per agent).
	var links []agentSkillRow
	err = tx.Table("agent_skills AS l").
		Select("l.agent_id::text AS agent_id, s.name AS skill_name").
		Joins("JOIN agents a ON a.id = l.agent_id").
		Joins("JOIN skills s ON s.id = l.skill_id AND s.workspace_id = a.workspace_id").
		Where("a.workspace_id = ?", ws).
		Order(`l.agent_id, l."order", s.name COLLATE "C", s.id`).Scan(&links).Error
	if err != nil {
		return nil, fmt.Errorf("read agent skills: %w", err)
	}
	names := make(map[string][]string, len(rows))
	for _, l := range links {
		names[l.AgentID] = append(names[l.AgentID], l.SkillName)
	}

	out := make([]domain.AgentRecord, len(rows))
	for i, r := range rows {
		out[i] = domain.AgentRecord{
			ID: r.ID, Name: r.Name, Description: r.Description, Provider: r.Provider, Model: r.Model,
			SystemPrompt: r.SystemPrompt, OutputSchema: raw(r.OutputSchema), Enabled: r.Enabled,
			Strategy: r.Strategy, CIFailOn: r.CIFailOn, RepoIntel: r.RepoIntel,
			SkillNames: names[r.ID],
		}
	}
	return out, nil
}

type skillRow struct {
	ID            string  `gorm:"column:id"`
	Name          string  `gorm:"column:name"`
	Description   string  `gorm:"column:description"`
	Type          string  `gorm:"column:type"`
	Source        string  `gorm:"column:source"`
	Body          string  `gorm:"column:body"`
	Enabled       bool    `gorm:"column:enabled"`
	EvidenceFiles *string `gorm:"column:evidence_files"`
}

type skillVersionRow struct {
	SkillID     string    `gorm:"column:skill_id"`
	Version     int       `gorm:"column:version"`
	Body        string    `gorm:"column:body"`
	Description *string   `gorm:"column:description"`
	Message     *string   `gorm:"column:message"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func readSkills(tx *gorm.DB, ws string) ([]domain.SkillRecord, error) {
	var rows []skillRow
	err := tx.Table("skills").
		Select(`id::text AS id, name, description, type, source, body, enabled,
			evidence_files::text AS evidence_files`).
		Where("workspace_id = ?", ws).
		Order(`name COLLATE "C", created_at, id`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read skills: %w", err)
	}

	var versions []skillVersionRow
	err = tx.Table("skill_versions AS v").
		Select("v.skill_id::text AS skill_id, v.version, v.body, v.description, v.message, v.created_at").
		Joins("JOIN skills s ON s.id = v.skill_id").
		Where("s.workspace_id = ?", ws).
		Order("v.skill_id, v.version").Scan(&versions).Error
	if err != nil {
		return nil, fmt.Errorf("read skill versions: %w", err)
	}
	bySkill := make(map[string][]domain.SkillVersionRecord, len(rows))
	for _, v := range versions {
		bySkill[v.SkillID] = append(bySkill[v.SkillID], domain.SkillVersionRecord{
			Version: v.Version, Body: v.Body, Description: v.Description, Message: v.Message,
			CreatedAt: v.CreatedAt,
		})
	}

	out := make([]domain.SkillRecord, len(rows))
	for i, r := range rows {
		out[i] = domain.SkillRecord{
			ID: r.ID, Name: r.Name, Description: r.Description, Type: r.Type, Source: r.Source,
			Body: r.Body, Enabled: r.Enabled, EvidenceFiles: raw(r.EvidenceFiles),
			Versions: bySkill[r.ID],
		}
	}
	return out, nil
}

type evalCaseRow struct {
	Name           string  `gorm:"column:name"`
	OwnerKind      string  `gorm:"column:owner_kind"`
	OwnerID        string  `gorm:"column:owner_id"`
	InputDiff      *string `gorm:"column:input_diff"`
	InputFiles     *string `gorm:"column:input_files"`
	InputMeta      *string `gorm:"column:input_meta"`
	ExpectedOutput *string `gorm:"column:expected_output"`
	Notes          *string `gorm:"column:notes"`
}

func readEvalCases(tx *gorm.DB, ws string) ([]domain.EvalCaseRecord, error) {
	var rows []evalCaseRow
	err := tx.Table("eval_cases").
		Select(`name, owner_kind, owner_id::text AS owner_id, input_diff,
			input_files::text AS input_files, input_meta::text AS input_meta,
			expected_output::text AS expected_output, notes`).
		Where("workspace_id = ?", ws).
		Order(`name COLLATE "C", created_at, id`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read eval cases: %w", err)
	}
	out := make([]domain.EvalCaseRecord, len(rows))
	for i, r := range rows {
		out[i] = domain.EvalCaseRecord{
			Name: r.Name, OwnerKind: r.OwnerKind, OwnerID: r.OwnerID, InputDiff: r.InputDiff,
			InputFiles: raw(r.InputFiles), InputMeta: raw(r.InputMeta),
			ExpectedOutput: raw(r.ExpectedOutput), Notes: r.Notes,
		}
	}
	return out, nil
}

type conventionRow struct {
	Repo            string   `gorm:"column:repo"`
	Rule            string   `gorm:"column:rule"`
	Category        string   `gorm:"column:category"`
	Status          string   `gorm:"column:status"`
	Confidence      *float64 `gorm:"column:confidence"`
	EvidencePath    *string  `gorm:"column:evidence_path"`
	EvidenceSnippet *string  `gorm:"column:evidence_snippet"`
}

func readConventions(tx *gorm.DB, ws string) ([]domain.ConventionRecord, error) {
	var rows []conventionRow
	// The evidence fields are the first entry of the evidence array; an array without entries,
	// a non-array value or an entry without the key gives NULL.
	err := tx.Table("conventions AS c").
		Select(`r.full_name AS repo, c.rule, c.category, c.status, c.confidence,
			c.evidence->0->>'path' AS evidence_path, c.evidence->0->>'snippet' AS evidence_snippet`).
		Joins("JOIN repos r ON r.id = c.repo_id AND r.workspace_id = c.workspace_id").
		Where("c.workspace_id = ?", ws).
		Order(`r.full_name COLLATE "C", c.created_at, c.rule COLLATE "C", c.id`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read conventions: %w", err)
	}
	out := make([]domain.ConventionRecord, len(rows))
	for i, r := range rows {
		out[i] = domain.ConventionRecord(r)
	}
	return out, nil
}

type learningRow struct {
	Scope      string   `gorm:"column:scope"`
	Kind       string   `gorm:"column:kind"`
	Content    string   `gorm:"column:content"`
	Confidence *float64 `gorm:"column:confidence"`
	Repo       *string  `gorm:"column:repo"`
}

func readLearnings(tx *gorm.DB, ws string) ([]domain.LearningRecord, error) {
	var rows []learningRow
	// Explicit column list: embedding and sources are never read.
	err := tx.Table("memory AS m").
		Select("m.scope, m.kind, m.content, m.confidence, r.full_name AS repo").
		Joins("LEFT JOIN repos r ON r.id = m.repo_id AND r.workspace_id = m.workspace_id").
		Where("m.workspace_id = ?", ws).
		Order("m.created_at, m.id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read learnings: %w", err)
	}
	out := make([]domain.LearningRecord, len(rows))
	for i, r := range rows {
		out[i] = domain.LearningRecord(r)
	}
	return out, nil
}

type contextFileRow struct {
	Repo    string `gorm:"column:repo"`
	Path    string `gorm:"column:path"`
	Content string `gorm:"column:content"`
}

func readContextFiles(tx *gorm.DB, ws string) ([]domain.ContextFileRecord, error) {
	var rows []contextFileRow
	err := tx.Table("context_files AS f").
		Select("r.full_name AS repo, f.path, f.content").
		Joins("JOIN repos r ON r.id = f.repo_id").
		Where("r.workspace_id = ?", ws).
		Order(`r.full_name COLLATE "C", f.path COLLATE "C", f.id`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read context files: %w", err)
	}
	out := make([]domain.ContextFileRecord, len(rows))
	for i, r := range rows {
		out[i] = domain.ContextFileRecord(r)
	}
	return out, nil
}
