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
	ID           string `gorm:"column:id"`
	Name         string `gorm:"column:name"`
	Provider     string `gorm:"column:provider"`
	Model        string `gorm:"column:model"`
	SystemPrompt string `gorm:"column:system_prompt"`
	Strategy     string `gorm:"column:strategy"`
	Version      int    `gorm:"column:version"`
}

// skillRow is the part of skills a run freezes.
type skillRow struct {
	ID          string `gorm:"column:id"`
	Name        string `gorm:"column:name"`
	Description string `gorm:"column:description"`
	Body        string `gorm:"column:body"`
	Version     int    `gorm:"column:version"`
}

func (r skillRow) toDomain() domain.SkillSnapshot {
	return domain.SkillSnapshot{ID: r.ID, Name: r.Name, Description: r.Description, Body: r.Body, Version: r.Version}
}

// Agent returns the agent with its enabled linked skills in link order; domain.ErrNotFound when
// it is not in the workspace.
func (s *Store) Agent(ctx context.Context, workspaceID, agentID string) (domain.AgentSnapshot, error) {
	if !isUUID(agentID) {
		return domain.AgentSnapshot{}, domain.ErrNotFound
	}
	var agent agentRow
	err := s.conn(ctx).Table("agents").
		Select("id, name, provider, model, system_prompt, strategy, version").
		Where("workspace_id = ? AND id = ?", workspaceID, agentID).Take(&agent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.AgentSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AgentSnapshot{}, fmt.Errorf("select agent: %w", err)
	}
	// Enabled skills only, in link order: the set a live review of this agent carries.
	var skills []skillRow
	err = s.conn(ctx).Table("agent_skills").
		Select("skills.id, skills.name, skills.description, skills.body, skills.version").
		Joins("JOIN skills ON skills.id = agent_skills.skill_id").
		Where("agent_skills.agent_id = ? AND skills.enabled", agentID).
		Order(`agent_skills."order", skills.id`).Scan(&skills).Error
	if err != nil {
		return domain.AgentSnapshot{}, fmt.Errorf("select agent skills: %w", err)
	}
	snap := domain.AgentSnapshot{
		AgentID: agent.ID, Version: agent.Version, Name: agent.Name,
		Config: domain.Config{
			Provider: agent.Provider, Model: agent.Model, SystemPrompt: agent.SystemPrompt, Strategy: agent.Strategy,
			Skills: make([]domain.SkillSnapshot, len(skills)),
		},
	}
	for i, sk := range skills {
		snap.Config.Skills[i] = sk.toDomain()
	}
	return snap, nil
}

// Skill returns the skill, enabled or not; domain.ErrNotFound when not in the workspace.
func (s *Store) Skill(ctx context.Context, workspaceID, skillID string) (domain.SkillSnapshot, error) {
	if !isUUID(skillID) {
		return domain.SkillSnapshot{}, domain.ErrNotFound
	}
	var row skillRow
	err := s.conn(ctx).Table("skills").
		Select("id, name, description, body, version").
		Where("workspace_id = ? AND id = ?", workspaceID, skillID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.SkillSnapshot{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.SkillSnapshot{}, fmt.Errorf("select skill: %w", err)
	}
	return row.toDomain(), nil
}

// AgentIDs returns every agent id of the workspace, disabled included, in the order of GET
// /agents (name COLLATE "C", then id).
func (s *Store) AgentIDs(ctx context.Context, workspaceID string) ([]string, error) {
	ids := []string{}
	err := s.conn(ctx).Table("agents").Where("workspace_id = ?", workspaceID).
		Order(`name COLLATE "C", id`).Pluck("id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("select agent ids: %w", err)
	}
	return ids, nil
}
