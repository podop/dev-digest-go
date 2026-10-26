package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/evals/domain"
)

// agentRow is the part of agents a run freezes.
type agentRow struct {
	ID            string  `gorm:"column:id"`
	Name          string  `gorm:"column:name"`
	Provider      string  `gorm:"column:provider"`
	Model         string  `gorm:"column:model"`
	SystemPrompt  string  `gorm:"column:system_prompt"`
	Strategy      string  `gorm:"column:strategy"`
	Version       int     `gorm:"column:version"`
	PluginName    *string `gorm:"column:plugin_name"`
	PluginEnabled *bool   `gorm:"column:plugin_enabled"`
}

// pluginRef maps the LEFT JOIN of installed_plugins: both columns are NULL for a row without a plugin.
func pluginRef(name *string, enabled *bool) *domain.PluginRef {
	if name == nil || enabled == nil {
		return nil
	}
	return &domain.PluginRef{Name: *name, Enabled: *enabled}
}

// skillRow is the part of skills a run freezes.
type skillRow struct {
	ID            string  `gorm:"column:id"`
	Name          string  `gorm:"column:name"`
	Description   string  `gorm:"column:description"`
	Body          string  `gorm:"column:body"`
	Version       int     `gorm:"column:version"`
	PluginName    *string `gorm:"column:plugin_name"`
	PluginEnabled *bool   `gorm:"column:plugin_enabled"`
}

func (r skillRow) toDomain() domain.SkillSnapshot {
	return domain.SkillSnapshot{
		ID: r.ID, Name: r.Name, Description: r.Description, Body: r.Body, Version: r.Version, Plugin: pluginRef(r.PluginName, r.PluginEnabled),
	}
}

// Agent returns the agent with its enabled linked skills in link order, leaving out the skills of
// a disabled plugin; domain.ErrNotFound when it is not in the workspace. The agent's own plugin
// comes back as Plugin and is not filtered here: the start use case refuses a disabled one.
func (s *Store) Agent(ctx context.Context, workspaceID, agentID string) (domain.AgentSnapshot, error) {
	if !isUUID(agentID) {
		return domain.AgentSnapshot{}, domain.ErrNotFound
	}
	var agent agentRow
	err := s.conn(ctx).Table("agents").
		Select("agents.id, agents.name, agents.provider, agents.model, agents.system_prompt, agents.strategy, agents.version, "+
			"ip.name AS plugin_name, ip.enabled AS plugin_enabled").
		Joins("LEFT JOIN installed_plugins ip ON ip.id = agents.plugin_id").
		Where("agents.workspace_id = ? AND agents.id = ?", workspaceID, agentID).Take(&agent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.AgentSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AgentSnapshot{}, fmt.Errorf("select agent: %w", err)
	}
	// Enabled skills only, in link order: the set a live review of this agent carries. A skill of a
	// disabled plugin is left out like a disabled skill.
	var skills []skillRow
	err = s.conn(ctx).Table("agent_skills").
		Select("skills.id, skills.name, skills.description, skills.body, skills.version").
		Joins("JOIN skills ON skills.id = agent_skills.skill_id").
		Joins("LEFT JOIN installed_plugins ip ON ip.id = skills.plugin_id").
		Where("agent_skills.agent_id = ? AND skills.enabled AND (skills.plugin_id IS NULL OR ip.enabled)", agentID).
		Order(`agent_skills."order", skills.id`).Scan(&skills).Error
	if err != nil {
		return domain.AgentSnapshot{}, fmt.Errorf("select agent skills: %w", err)
	}
	snap := domain.AgentSnapshot{
		AgentID: agent.ID, Version: agent.Version, Name: agent.Name, Plugin: pluginRef(agent.PluginName, agent.PluginEnabled),
		Config: domain.Config{
			Provider: agent.Provider, Model: agent.Model, SystemPrompt: agent.SystemPrompt, Strategy: agent.Strategy,
			Skills: make([]domain.SkillSnapshot, len(skills)),
		},
	}
	for i, sk := range skills {
		snap.Config.Skills[i] = sk.toDomain() // Plugin stays nil: these skills are enabled-plugin ones
	}
	return snap, nil
}

// Skill returns the skill, enabled or not, with its plugin (the start use case refuses a disabled
// one); domain.ErrNotFound when not in the workspace.
func (s *Store) Skill(ctx context.Context, workspaceID, skillID string) (domain.SkillSnapshot, error) {
	if !isUUID(skillID) {
		return domain.SkillSnapshot{}, domain.ErrNotFound
	}
	var row skillRow
	err := s.conn(ctx).Table("skills").
		Select("skills.id, skills.name, skills.description, skills.body, skills.version, "+
			"ip.name AS plugin_name, ip.enabled AS plugin_enabled").
		Joins("LEFT JOIN installed_plugins ip ON ip.id = skills.plugin_id").
		Where("skills.workspace_id = ? AND skills.id = ?", workspaceID, skillID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.SkillSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.SkillSnapshot{}, fmt.Errorf("select skill: %w", err)
	}
	return row.toDomain(), nil
}

// AgentIDs returns every agent id of the workspace, disabled included but not those of a disabled
// plugin, in the order of GET /agents (creation time, name COLLATE "C", then id; Node's unordered
// listIds read them in insertion order).
func (s *Store) AgentIDs(ctx context.Context, workspaceID string) ([]string, error) {
	ids := []string{}
	err := s.conn(ctx).Table("agents").
		Joins("LEFT JOIN installed_plugins ip ON ip.id = agents.plugin_id").
		Where("agents.workspace_id = ? AND (agents.plugin_id IS NULL OR ip.enabled)", workspaceID).
		Order(`agents.created_at, agents.name COLLATE "C", agents.id`).Pluck("agents.id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("select agent ids: %w", err)
	}
	return ids, nil
}
