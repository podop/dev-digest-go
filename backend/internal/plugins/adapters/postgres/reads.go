package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/plugins/app"
	"dev-digest-go/backend/internal/plugins/domain"
)

// pluginColumns selects an installed plugin with the counts of what it owns, in one statement:
// correlated count subqueries (each served by a plugin_id partial index or the eval_cases owner
// index), no query per plugin. Eval cases are counted through the owner they have no foreign key to.
const pluginColumns = `p.id::text AS id, p.name, p.version, p.description, p.source, p.installed_at, p.enabled,
	(SELECT count(*) FROM agents a WHERE a.plugin_id = p.id) AS agents,
	(SELECT count(*) FROM skills s WHERE s.plugin_id = p.id) AS skills,
	(SELECT count(*) FROM eval_cases e WHERE e.workspace_id = p.workspace_id AND (
		(e.owner_kind = 'agent' AND e.owner_id IN (SELECT a.id FROM agents a WHERE a.plugin_id = p.id))
		OR (e.owner_kind = 'skill' AND e.owner_id IN (SELECT s.id FROM skills s WHERE s.plugin_id = p.id)))) AS eval_cases,
	(SELECT count(*) FROM memory m WHERE m.plugin_id = p.id) AS learnings`

type pluginView struct {
	ID          string    `gorm:"column:id"`
	Name        string    `gorm:"column:name"`
	Version     *string   `gorm:"column:version"`
	Description *string   `gorm:"column:description"`
	Source      *string   `gorm:"column:source"`
	InstalledAt time.Time `gorm:"column:installed_at"`
	Enabled     bool      `gorm:"column:enabled"`
	Agents      int       `gorm:"column:agents"`
	Skills      int       `gorm:"column:skills"`
	EvalCases   int       `gorm:"column:eval_cases"`
	Learnings   int       `gorm:"column:learnings"`
}

func (v pluginView) toDomain() domain.Plugin {
	return domain.Plugin{
		ID: v.ID, Name: v.Name, Version: v.Version, Description: v.Description, Source: v.Source,
		InstalledAt: v.InstalledAt, Enabled: v.Enabled,
		Counts: domain.PluginCounts{Agents: v.Agents, Skills: v.Skills, EvalCases: v.EvalCases, Learnings: v.Learnings},
	}
}

func (s *Store) pluginQuery(ctx context.Context, workspaceID string) *gorm.DB {
	return pg.Conn(ctx, s.db).Table("installed_plugins AS p").Select(pluginColumns).Where("p.workspace_id = ?", workspaceID)
}

// List returns the workspace's plugins, newest first, with what each owns.
func (s *Store) List(ctx context.Context, workspaceID string) ([]domain.Plugin, error) {
	var rows []pluginView
	if err := s.pluginQuery(ctx, workspaceID).Order("p.installed_at DESC, p.id DESC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("select plugins: %w", err)
	}
	out := make([]domain.Plugin, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// plugin returns one plugin with its counts, or domain.ErrNotFound.
func (s *Store) plugin(ctx context.Context, workspaceID, id string) (domain.Plugin, error) {
	if !isUUID(id) {
		return domain.Plugin{}, domain.ErrNotFound
	}
	var rows []pluginView
	if err := s.pluginQuery(ctx, workspaceID).Where("p.id = ?", id).Limit(1).Scan(&rows).Error; err != nil {
		return domain.Plugin{}, fmt.Errorf("select plugin: %w", err)
	}
	if len(rows) == 0 {
		return domain.Plugin{}, domain.ErrNotFound
	}
	return rows[0].toDomain(), nil
}

// SetEnabled sets the plugin's flag and returns it as List does; domain.ErrNotFound for an unknown
// or malformed id.
func (s *Store) SetEnabled(ctx context.Context, workspaceID, id string, enabled bool) (domain.Plugin, error) {
	if !isUUID(id) {
		return domain.Plugin{}, domain.ErrNotFound
	}
	var out domain.Plugin
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		res := pg.Conn(ctx, s.db).Model(&pluginRow{}).Where("workspace_id = ? AND id = ?", workspaceID, id).
			Update("enabled", enabled)
		if res.Error != nil {
			return fmt.Errorf("update plugin: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return domain.ErrNotFound
		}
		p, err := s.plugin(ctx, workspaceID, id)
		out = p
		return err
	})
	if err != nil {
		return domain.Plugin{}, err
	}
	return out, nil
}

// Conflicts reports whether a plugin named pluginName is installed in the workspace and which of
// skillNames are skill names of the workspace. Names compare exactly, as the unique constraints
// (installed_plugins_workspace_id_name_key, skills_ws_name_uq) do: case and whitespace count.
// Skills keeps the order of skillNames, each name once, and is never nil.
func (s *Store) Conflicts(ctx context.Context, workspaceID, pluginName string, skillNames []string) (domain.Conflicts, error) {
	out := domain.Conflicts{Skills: []string{}}
	db := pg.Conn(ctx, s.db)

	// A name with U+0000 cannot be stored, so it cannot clash (and a query with it would fail).
	if !strings.ContainsRune(pluginName, 0) {
		var n int64
		err := db.Table("installed_plugins").Where("workspace_id = ? AND name = ?", workspaceID, pluginName).Count(&n).Error
		if err != nil {
			return domain.Conflicts{}, fmt.Errorf("select plugin name: %w", err)
		}
		out.Plugin = n > 0
	}

	var candidates []string
	for _, n := range skillNames {
		if !strings.ContainsRune(n, 0) && !slices.Contains(candidates, n) {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) == 0 {
		return out, nil
	}
	var taken []string
	err := db.Table("skills").Where("workspace_id = ? AND name IN ?", workspaceID, candidates).Pluck("name", &taken).Error
	if err != nil {
		return domain.Conflicts{}, fmt.Errorf("select skill names: %w", err)
	}
	for _, n := range candidates {
		if slices.Contains(taken, n) {
			out.Skills = append(out.Skills, n)
		}
	}
	return out, nil
}

// ReadExport reads the selected records in one read-only repeatable-read transaction, so every
// list comes from the same snapshot. No ids selects every agent and every skill; agent ids select
// those agents and every skill linked to them; skill ids those skills; both the union. missing lists
// the requested ids (agent ids first, each once, in request order) that are no agent or skill of the
// workspace, malformed ones included; with a non-empty missing the data is empty. Agents are in
// creation order with their skill names in link order, skills in creation order; eval cases (of the
// selected agents and skills) and learnings (all of the workspace, with their repository's full
// name) are read only when asked for. Disabled and plugin-owned records are exported with their own
// flags (EC10); no id of an owner leaves the adapter except the ones that tie an eval case to its
// owner inside the data.
func (s *Store) ReadExport(ctx context.Context, workspaceID string, sel app.ExportSelection) (domain.ExportData, []string, error) {
	var data domain.ExportData
	var missing []string
	err := pg.Conn(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		var err error
		data, missing, err = readExport(tx, workspaceID, sel)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.ExportData{}, nil, fmt.Errorf("plugin export data: %w", err)
	}
	return data, missing, nil
}

func readExport(tx *gorm.DB, ws string, sel app.ExportSelection) (domain.ExportData, []string, error) {
	everything := len(sel.AgentIDs) == 0 && len(sel.SkillIDs) == 0
	agentWanted := wellFormed(sel.AgentIDs)
	skillWanted := wellFormed(sel.SkillIDs)

	var agents []agentExportRow
	if everything || len(agentWanted) > 0 {
		q := tx.Table("agents").
			Select(`id::text AS id, name, description, provider, model, system_prompt,
				output_schema::text AS output_schema, enabled, strategy, ci_fail_on, repo_intel`).
			Where("workspace_id = ?", ws)
		if !everything {
			q = q.Where("id IN ?", agentWanted)
		}
		if err := q.Order("created_at, id").Scan(&agents).Error; err != nil {
			return domain.ExportData{}, nil, fmt.Errorf("read agents: %w", err)
		}
	}
	agentIDs := make([]string, len(agents))
	for i, a := range agents {
		agentIDs[i] = a.ID
	}

	// Skills: the requested ones plus every skill linked to a selected agent.
	var skills []skillExportRow
	if everything || len(skillWanted) > 0 || len(agentIDs) > 0 {
		q := tx.Table("skills").
			Select(`id::text AS id, name, description, type, source, body, enabled, evidence_files::text AS evidence_files`).
			Where("workspace_id = ?", ws)
		if !everything {
			q = q.Where("id IN ? OR id IN (SELECT skill_id FROM agent_skills WHERE agent_id IN ?)",
				orNone(skillWanted), orNone(agentIDs))
		}
		if err := q.Order(`created_at, name COLLATE "C", id`).Scan(&skills).Error; err != nil {
			return domain.ExportData{}, nil, fmt.Errorf("read skills: %w", err)
		}
	}

	found := make(map[string]bool, len(agents)+len(skills))
	for _, a := range agents {
		found[a.ID] = true
	}
	var missing []string
	for _, id := range sel.AgentIDs {
		if !found[strings.ToLower(id)] && !slices.Contains(missing, id) {
			missing = append(missing, id)
		}
	}
	foundSkill := make(map[string]bool, len(skills))
	for _, k := range skills {
		foundSkill[k.ID] = true
	}
	for _, id := range sel.SkillIDs {
		if !foundSkill[strings.ToLower(id)] && !slices.Contains(missing, id) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return domain.ExportData{}, missing, nil
	}

	names, err := linkedSkillNames(tx, ws, agentIDs)
	if err != nil {
		return domain.ExportData{}, nil, err
	}
	var data domain.ExportData
	data.Agents = make([]domain.AgentRecord, len(agents))
	for i, a := range agents {
		data.Agents[i] = domain.AgentRecord{
			ID: a.ID, Name: a.Name, Description: a.Description, Provider: a.Provider, Model: a.Model,
			SystemPrompt: a.SystemPrompt, OutputSchema: rawJSON(a.OutputSchema), Enabled: a.Enabled,
			Strategy: a.Strategy, CIFailOn: a.CIFailOn, RepoIntel: a.RepoIntel, SkillNames: names[a.ID],
		}
	}
	data.Skills = make([]domain.SkillRecord, len(skills))
	for i, k := range skills {
		rec := domain.SkillRecord{
			ID: k.ID, Name: k.Name, Description: k.Description, Type: k.Type, Source: k.Source, Body: k.Body, Enabled: k.Enabled,
		}
		if k.EvidenceFiles != nil {
			if err := json.Unmarshal([]byte(*k.EvidenceFiles), &rec.EvidenceFiles); err != nil {
				return domain.ExportData{}, nil, fmt.Errorf("decode evidence files of skill %s: %w", k.ID, err)
			}
		}
		data.Skills[i] = rec
	}

	if sel.EvalCases {
		skillIDs := make([]string, len(skills))
		for i, k := range skills {
			skillIDs[i] = k.ID
		}
		if data.EvalCases, err = readEvalCases(tx, ws, agentIDs, skillIDs); err != nil {
			return domain.ExportData{}, nil, err
		}
	}
	if sel.Learnings {
		if data.Learnings, err = readLearnings(tx, ws); err != nil {
			return domain.ExportData{}, nil, err
		}
	}
	return data, nil, nil
}

// wellFormed lowercases the well-formed UUIDs of ids (the form Postgres returns) and drops the
// rest, which can name no row.
func wellFormed(ids []string) []string {
	var out []string
	for _, id := range ids {
		if isUUID(id) {
			out = append(out, strings.ToLower(id))
		}
	}
	return out
}

// orNone is a list safe to put behind IN: an empty list becomes one nil UUID, which matches no row.
func orNone(ids []string) []string {
	if len(ids) == 0 {
		return []string{"00000000-0000-0000-0000-000000000000"}
	}
	return ids
}

type agentExportRow struct {
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

type skillExportRow struct {
	ID            string  `gorm:"column:id"`
	Name          string  `gorm:"column:name"`
	Description   string  `gorm:"column:description"`
	Type          string  `gorm:"column:type"`
	Source        string  `gorm:"column:source"`
	Body          string  `gorm:"column:body"`
	Enabled       bool    `gorm:"column:enabled"`
	EvidenceFiles *string `gorm:"column:evidence_files"`
}

// rawJSON returns a JSON column read as text (nil for SQL NULL) as raw JSON.
func rawJSON(text *string) json.RawMessage {
	if text == nil {
		return nil
	}
	return json.RawMessage(*text)
}

type linkName struct {
	AgentID   string `gorm:"column:agent_id"`
	SkillName string `gorm:"column:skill_name"`
}

// linkedSkillNames reads the linked skills' names of all agents at once, in link order.
func linkedSkillNames(tx *gorm.DB, ws string, agentIDs []string) (map[string][]string, error) {
	names := make(map[string][]string, len(agentIDs))
	if len(agentIDs) == 0 {
		return names, nil
	}
	var links []linkName
	err := tx.Table("agent_skills AS l").
		Select("l.agent_id::text AS agent_id, s.name AS skill_name").
		Joins("JOIN skills s ON s.id = l.skill_id AND s.workspace_id = ?", ws).
		Where("l.agent_id IN ?", agentIDs).
		Order(`l.agent_id, l."order", s.name COLLATE "C", s.id`).Scan(&links).Error
	if err != nil {
		return nil, fmt.Errorf("read agent skills: %w", err)
	}
	for _, l := range links {
		names[l.AgentID] = append(names[l.AgentID], l.SkillName)
	}
	return names, nil
}

type evalCaseExportRow struct {
	Name           string  `gorm:"column:name"`
	OwnerKind      string  `gorm:"column:owner_kind"`
	OwnerID        string  `gorm:"column:owner_id"`
	InputDiff      *string `gorm:"column:input_diff"`
	InputFiles     *string `gorm:"column:input_files"`
	InputMeta      *string `gorm:"column:input_meta"`
	ExpectedOutput *string `gorm:"column:expected_output"`
	Notes          *string `gorm:"column:notes"`
}

func readEvalCases(tx *gorm.DB, ws string, agentIDs, skillIDs []string) ([]domain.EvalCaseRecord, error) {
	if len(agentIDs) == 0 && len(skillIDs) == 0 {
		return nil, nil
	}
	var rows []evalCaseExportRow
	err := tx.Table("eval_cases").
		Select(`name, owner_kind, owner_id::text AS owner_id, input_diff, input_files::text AS input_files,
			input_meta::text AS input_meta, expected_output::text AS expected_output, notes`).
		Where(`workspace_id = ? AND ((owner_kind = 'agent' AND owner_id IN ?) OR (owner_kind = 'skill' AND owner_id IN ?))`,
			ws, orNone(agentIDs), orNone(skillIDs)).
		Order("created_at, id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read eval cases: %w", err)
	}
	out := make([]domain.EvalCaseRecord, len(rows))
	for i, r := range rows {
		out[i] = domain.EvalCaseRecord{
			Name: r.Name, OwnerKind: r.OwnerKind, OwnerID: r.OwnerID, InputDiff: r.InputDiff,
			InputFiles: rawJSON(r.InputFiles), InputMeta: rawJSON(r.InputMeta),
			ExpectedOutput: rawJSON(r.ExpectedOutput), Notes: r.Notes,
		}
	}
	return out, nil
}

type learningExportRow struct {
	Scope      string   `gorm:"column:scope"`
	Kind       string   `gorm:"column:kind"`
	Content    string   `gorm:"column:content"`
	Confidence *float64 `gorm:"column:confidence"`
	Repo       *string  `gorm:"column:repo"`
}

func readLearnings(tx *gorm.DB, ws string) ([]domain.LearningRecord, error) {
	var rows []learningExportRow
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
