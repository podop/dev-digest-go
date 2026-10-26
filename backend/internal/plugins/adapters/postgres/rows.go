package postgres

import (
	"encoding/json"
	"time"
)

// The write models of the tables an install fills. Each lists only the columns the install
// writes; the others keep their database defaults. The agents and skills contexts own the tables
// and their meaning; the shapes follow what their stores write on create.

type pluginRow struct {
	ID          string  `gorm:"column:id;primaryKey"`
	WorkspaceID string  `gorm:"column:workspace_id"`
	Name        string  `gorm:"column:name"`
	Version     *string `gorm:"column:version"`
	Description *string `gorm:"column:description"`
	Source      *string `gorm:"column:source"`
	Enabled     bool    `gorm:"column:enabled"`
}

func (pluginRow) TableName() string { return "installed_plugins" }

type agentRow struct {
	ID           string    `gorm:"column:id;primaryKey"`
	WorkspaceID  string    `gorm:"column:workspace_id"`
	Name         string    `gorm:"column:name"`
	Description  string    `gorm:"column:description"`
	Provider     string    `gorm:"column:provider"`
	Model        string    `gorm:"column:model"`
	SystemPrompt string    `gorm:"column:system_prompt"`
	OutputSchema *string   `gorm:"column:output_schema;type:jsonb"`
	Enabled      bool      `gorm:"column:enabled"`
	Version      int       `gorm:"column:version"`
	Strategy     string    `gorm:"column:strategy"`
	CIFailOn     string    `gorm:"column:ci_fail_on"`
	RepoIntel    bool      `gorm:"column:repo_intel"`
	PluginID     string    `gorm:"column:plugin_id"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (agentRow) TableName() string { return "agents" }

type agentVersionRow struct {
	AgentID    string `gorm:"column:agent_id;primaryKey"`
	Version    int    `gorm:"column:version;primaryKey"`
	ConfigJSON string `gorm:"column:config_json;type:jsonb"`
}

func (agentVersionRow) TableName() string { return "agent_versions" }

// agentConfig is the JSON of agent_versions.config_json (the vendored AgentVersionConfig).
type agentConfig struct {
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	SystemPrompt string          `json:"system_prompt"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Strategy     string          `json:"strategy"`
	CIFailOn     string          `json:"ci_fail_on"`
	RepoIntel    bool            `json:"repo_intel"`
	Skills       []string        `json:"skills"`
}

type skillRow struct {
	ID            string    `gorm:"column:id;primaryKey"`
	WorkspaceID   string    `gorm:"column:workspace_id"`
	Name          string    `gorm:"column:name"`
	Description   string    `gorm:"column:description"`
	Type          string    `gorm:"column:type"`
	Source        string    `gorm:"column:source"`
	SourceRef     *string   `gorm:"column:source_ref"`
	Body          string    `gorm:"column:body"`
	Enabled       bool      `gorm:"column:enabled"`
	Version       int       `gorm:"column:version"`
	EvidenceFiles *string   `gorm:"column:evidence_files;type:jsonb"`
	PluginID      string    `gorm:"column:plugin_id"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

func (skillRow) TableName() string { return "skills" }

type skillVersionRow struct {
	SkillID     string  `gorm:"column:skill_id;primaryKey"`
	Version     int     `gorm:"column:version;primaryKey"`
	Body        string  `gorm:"column:body"`
	Description *string `gorm:"column:description"`
	Message     *string `gorm:"column:message"`
}

func (skillVersionRow) TableName() string { return "skill_versions" }

type linkRow struct {
	AgentID string `gorm:"column:agent_id;primaryKey"`
	SkillID string `gorm:"column:skill_id;primaryKey"`
	Order   int    `gorm:"column:order"`
}

func (linkRow) TableName() string { return "agent_skills" }

type evalCaseRow struct {
	ID             string    `gorm:"column:id;primaryKey"`
	WorkspaceID    string    `gorm:"column:workspace_id"`
	OwnerKind      string    `gorm:"column:owner_kind"`
	OwnerID        string    `gorm:"column:owner_id"`
	Name           string    `gorm:"column:name"`
	InputDiff      *string   `gorm:"column:input_diff"`
	InputFiles     *string   `gorm:"column:input_files;type:jsonb"`
	InputMeta      *string   `gorm:"column:input_meta;type:jsonb"`
	ExpectedOutput *string   `gorm:"column:expected_output;type:jsonb"`
	Notes          *string   `gorm:"column:notes"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (evalCaseRow) TableName() string { return "eval_cases" }

// memoryRow is a learning: no repository (repo_id stays NULL), no embedding, no sources.
type memoryRow struct {
	ID          string    `gorm:"column:id;primaryKey"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	Scope       string    `gorm:"column:scope"`
	Kind        string    `gorm:"column:kind"`
	Content     string    `gorm:"column:content"`
	Confidence  *float64  `gorm:"column:confidence"`
	PluginID    string    `gorm:"column:plugin_id"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (memoryRow) TableName() string { return "memory" }
