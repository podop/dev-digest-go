// Package postgres persists agents, their version snapshots and their skill links with GORM.
// Every method gets its handle from pg.Conn, so it joins a transaction opened by the use case.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/agents/domain"
	pg "dev-digest-go/backend/internal/platform/postgres"
)

// agentRow maps the agents table. created_at keeps its database default and is only used for
// ordering. output_schema is jsonb passed as text; NULL is a nil pointer. The Plugin* fields are
// read-only (gorm "->"): agentColumns fills them from installed_plugins and no write touches
// agents.plugin_id, so create leaves it NULL and an update keeps the owner.
type agentRow struct {
	ID           string  `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID  string  `gorm:"column:workspace_id"`
	Name         string  `gorm:"column:name"`
	Description  string  `gorm:"column:description"`
	Provider     string  `gorm:"column:provider"`
	Model        string  `gorm:"column:model"`
	SystemPrompt string  `gorm:"column:system_prompt"`
	OutputSchema *string `gorm:"column:output_schema;type:jsonb"`
	Enabled      bool    `gorm:"column:enabled"`
	Version      int     `gorm:"column:version"`
	CreatedBy    *string `gorm:"column:created_by"`
	Strategy     string  `gorm:"column:strategy"`
	CIFailOn     string  `gorm:"column:ci_fail_on"`
	RepoIntel    bool    `gorm:"column:repo_intel"`

	PluginID      *string `gorm:"column:plugin_ref_id;->"`
	PluginName    *string `gorm:"column:plugin_ref_name;->"`
	PluginEnabled *bool   `gorm:"column:plugin_ref_enabled;->"`
}

// agentColumns selects the agent with its owner through correlated subqueries. A join would
// not do: GetForUpdate appends FOR UPDATE, which Postgres refuses on the nullable side of an
// outer join; scalar subqueries leave the lock on the agent row alone.
const agentColumns = `agents.*,
	(SELECT p.id::text FROM installed_plugins p WHERE p.id = agents.plugin_id) AS plugin_ref_id,
	(SELECT p.name FROM installed_plugins p WHERE p.id = agents.plugin_id) AS plugin_ref_name,
	(SELECT p.enabled FROM installed_plugins p WHERE p.id = agents.plugin_id) AS plugin_ref_enabled`

func (agentRow) TableName() string { return "agents" }

func (r agentRow) toDomain() domain.Agent {
	a := domain.Agent{
		ID: r.ID, Name: r.Name, Description: r.Description, Provider: r.Provider, Model: r.Model,
		SystemPrompt: r.SystemPrompt, Enabled: r.Enabled, Version: r.Version, Strategy: r.Strategy,
		CIFailOn: r.CIFailOn, RepoIntel: r.RepoIntel,
	}
	if r.OutputSchema != nil {
		a.OutputSchema = domain.NormalizeSchema(json.RawMessage(*r.OutputSchema))
	}
	if r.PluginID != nil && r.PluginName != nil && r.PluginEnabled != nil {
		a.Plugin = &domain.PluginRef{ID: *r.PluginID, Name: *r.PluginName, Enabled: *r.PluginEnabled}
	}
	return a
}

func fromDomain(workspaceID string, createdBy *string, a domain.Agent) agentRow {
	r := agentRow{
		ID: a.ID, WorkspaceID: workspaceID, Name: a.Name, Description: a.Description, Provider: a.Provider,
		Model: a.Model, SystemPrompt: a.SystemPrompt, Enabled: a.Enabled, Version: a.Version,
		CreatedBy: createdBy, Strategy: a.Strategy, CIFailOn: a.CIFailOn, RepoIntel: a.RepoIntel,
	}
	if schema := domain.NormalizeSchema(a.OutputSchema); schema != nil {
		s := string(schema)
		r.OutputSchema = &s
	}
	return r
}

// versionRow maps agent_versions; config_json is jsonb passed as text.
type versionRow struct {
	AgentID    string    `gorm:"column:agent_id;primaryKey"`
	Version    int       `gorm:"column:version;primaryKey"`
	ConfigJSON string    `gorm:"column:config_json;type:jsonb"`
	CreatedAt  time.Time `gorm:"column:created_at;->"`
}

func (versionRow) TableName() string { return "agent_versions" }

// configDoc is the JSON stored in agent_versions.config_json (the vendored AgentVersionConfig).
type configDoc struct {
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Strategy     string          `json:"strategy"`
	CIFailOn     string          `json:"ci_fail_on"`
	RepoIntel    bool            `json:"repo_intel"`
	Skills       []string        `json:"skills"`
}

func (r versionRow) toDomain() (domain.Version, error) {
	var doc configDoc
	if err := json.Unmarshal([]byte(r.ConfigJSON), &doc); err != nil {
		return domain.Version{}, fmt.Errorf("decode config of agent %s v%d: %w", r.AgentID, r.Version, err)
	}
	if doc.Skills == nil {
		doc.Skills = []string{}
	}
	return domain.Version{
		AgentID: r.AgentID,
		Version: r.Version,
		Config: domain.VersionConfig{
			Provider: doc.Provider, Model: doc.Model, SystemPrompt: doc.SystemPrompt,
			OutputSchema: domain.NormalizeSchema(doc.OutputSchema), Strategy: doc.Strategy,
			CIFailOn: doc.CIFailOn, RepoIntel: doc.RepoIntel, Skills: doc.Skills,
		},
		CreatedAt: r.CreatedAt,
	}, nil
}

// linkRow maps agent_skills.
type linkRow struct {
	AgentID string `gorm:"column:agent_id;primaryKey"`
	SkillID string `gorm:"column:skill_id;primaryKey"`
	Order   int    `gorm:"column:order"`
}

func (linkRow) TableName() string { return "agent_skills" }

// skillRow is the read-only view of the skills table the agents need to validate ids. The
// skills packages own the table; this context only asks which ids exist in a workspace.
type skillRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id"`
}

func (skillRow) TableName() string { return "skills" }

// evalCaseRow is the slice of eval_cases an agent delete needs.
type evalCaseRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id"`
	OwnerKind   string `gorm:"column:owner_kind"`
	OwnerID     string `gorm:"column:owner_id"`
}

func (evalCaseRow) TableName() string { return "eval_cases" }

// Store is the GORM-backed agent store. It implements the store port of the app package.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// List returns the workspace's agents by creation time, name in byte order, then id.
func (s *Store) List(ctx context.Context, workspaceID string) ([]domain.Agent, error) {
	var rows []agentRow
	if err := pg.Conn(ctx, s.db).Select(agentColumns).Where("agents.workspace_id = ?", workspaceID).
		Order(`created_at, name COLLATE "C", id`).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select agents: %w", err)
	}
	out := make([]domain.Agent, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// Get returns the agent of the workspace, or domain.ErrNotFound.
func (s *Store) Get(ctx context.Context, workspaceID, id string) (domain.Agent, error) {
	return s.take(pg.Conn(ctx, s.db), workspaceID, id)
}

// GetForUpdate is Get with SELECT … FOR UPDATE.
func (s *Store) GetForUpdate(ctx context.Context, workspaceID, id string) (domain.Agent, error) {
	return s.take(pg.Conn(ctx, s.db).Clauses(clause.Locking{Strength: "UPDATE"}), workspaceID, id)
}

func (s *Store) take(db *gorm.DB, workspaceID, id string) (domain.Agent, error) {
	var row agentRow
	err := db.Select(agentColumns).Where("agents.workspace_id = ? AND agents.id = ?", workspaceID, id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Agent{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Agent{}, fmt.Errorf("select agent: %w", err)
	}
	return row.toDomain(), nil
}

// Insert stores the agent and returns it with the generated id.
func (s *Store) Insert(ctx context.Context, workspaceID, createdBy string, a domain.Agent) (domain.Agent, error) {
	var by *string
	if createdBy != "" {
		by = &createdBy
	}
	row := fromDomain(workspaceID, by, a)
	if err := pg.Conn(ctx, s.db).Omit("id").Create(&row).Error; err != nil {
		return domain.Agent{}, fmt.Errorf("insert agent: %w", err)
	}
	return row.toDomain(), nil
}

// Save writes every mutable column of the agent.
func (s *Store) Save(ctx context.Context, workspaceID string, a domain.Agent) error {
	row := fromDomain(workspaceID, nil, a)
	res := pg.Conn(ctx, s.db).Model(&agentRow{}).Where("workspace_id = ? AND id = ?", workspaceID, a.ID).
		Select("Name", "Description", "Provider", "Model", "SystemPrompt", "OutputSchema", "Enabled", "Version",
			"Strategy", "CIFailOn", "RepoIntel").Updates(&row)
	if res.Error != nil {
		return fmt.Errorf("update agent: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes the agent; foreign keys cascade to its versions, links, context attachments,
// eval suite runs and CI installations, and null the agent of reviews and agent runs.
func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	res := pg.Conn(ctx, s.db).Where("workspace_id = ? AND id = ?", workspaceID, id).Delete(&agentRow{})
	if res.Error != nil {
		return fmt.Errorf("delete agent: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteEvalCases removes the eval cases the agent owns in the workspace.
func (s *Store) DeleteEvalCases(ctx context.Context, workspaceID, agentID string) error {
	err := pg.Conn(ctx, s.db).Where("workspace_id = ? AND owner_kind = 'agent' AND owner_id = ?", workspaceID, agentID).
		Delete(&evalCaseRow{}).Error
	if err != nil {
		return fmt.Errorf("delete eval cases: %w", err)
	}
	return nil
}

// AddVersion records the snapshot; an existing (agent, version) is left as it is.
func (s *Store) AddVersion(ctx context.Context, agentID string, version int, cfg domain.VersionConfig) error {
	doc, err := json.Marshal(configDoc{
		Provider: cfg.Provider, Model: cfg.Model, SystemPrompt: cfg.SystemPrompt, OutputSchema: cfg.OutputSchema,
		Strategy: cfg.Strategy, CIFailOn: cfg.CIFailOn, RepoIntel: cfg.RepoIntel, Skills: nonNil(cfg.Skills),
	})
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	row := versionRow{AgentID: agentID, Version: version, ConfigJSON: string(doc)}
	err = pg.Conn(ctx, s.db).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	if err != nil {
		return fmt.Errorf("insert agent version: %w", err)
	}
	return nil
}

// Versions returns the agent's snapshots, newest first.
func (s *Store) Versions(ctx context.Context, agentID string) ([]domain.Version, error) {
	var rows []versionRow
	if err := pg.Conn(ctx, s.db).Where("agent_id = ?", agentID).Order("version DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select agent versions: %w", err)
	}
	out := make([]domain.Version, len(rows))
	for i, r := range rows {
		v, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Version returns one snapshot, or domain.ErrVersionNotFound.
func (s *Store) Version(ctx context.Context, agentID string, version int) (domain.Version, error) {
	var row versionRow
	err := pg.Conn(ctx, s.db).Where("agent_id = ? AND version = ?", agentID, version).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Version{}, domain.ErrVersionNotFound
	}
	if err != nil {
		return domain.Version{}, fmt.Errorf("select agent version: %w", err)
	}
	return row.toDomain()
}

// SkillLinks returns the agent's links by order.
func (s *Store) SkillLinks(ctx context.Context, agentID string) ([]domain.SkillLink, error) {
	var rows []linkRow
	if err := pg.Conn(ctx, s.db).Where("agent_id = ?", agentID).Order(`"order", skill_id`).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select agent skills: %w", err)
	}
	out := make([]domain.SkillLink, len(rows))
	for i, r := range rows {
		out[i] = domain.SkillLink{AgentID: r.AgentID, SkillID: r.SkillID, Order: r.Order}
	}
	return out, nil
}

// KnownSkillIDs returns which of ids are skills of the workspace.
func (s *Store) KnownSkillIDs(ctx context.Context, workspaceID string, ids []string) (map[string]bool, error) {
	known := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return known, nil
	}
	var found []string
	if err := pg.Conn(ctx, s.db).Model(&skillRow{}).Where("workspace_id = ? AND id IN ?", workspaceID, ids).
		Pluck("id", &found).Error; err != nil {
		return nil, fmt.Errorf("select skills: %w", err)
	}
	for _, id := range found {
		known[id] = true
	}
	return known, nil
}

// ReplaceSkills deletes the agent's links and inserts ids with order = index.
func (s *Store) ReplaceSkills(ctx context.Context, agentID string, ids []string) error {
	db := pg.Conn(ctx, s.db)
	if err := db.Where("agent_id = ?", agentID).Delete(&linkRow{}).Error; err != nil {
		return fmt.Errorf("delete agent skills: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]linkRow, len(ids))
	for i, id := range ids {
		rows[i] = linkRow{AgentID: agentID, SkillID: id, Order: i}
	}
	if err := db.Create(&rows).Error; err != nil {
		return fmt.Errorf("insert agent skills: %w", err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
