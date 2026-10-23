package peers

import (
	"context"
	"fmt"

	agentsdomain "dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	reposdomain "dev-digest-go/backend/internal/repos/domain"
	skillsdomain "dev-digest-go/backend/internal/skills/domain"
)

// AgentService is the part of the agents use cases the adapter reads.
type AgentService interface {
	// Get answers a not-found apperr for an unknown agent or one of another workspace.
	Get(ctx context.Context, workspaceID, id string) (agentsdomain.Agent, error)
	// SkillLinks returns the agent's links by order.
	SkillLinks(ctx context.Context, workspaceID, id string) ([]agentsdomain.SkillLink, error)
}

// SkillService is the part of the skills use cases the adapter reads.
type SkillService interface {
	// Get answers a not-found apperr for an unknown skill or one of another workspace.
	Get(ctx context.Context, workspaceID, id string) (skillsdomain.Skill, error)
}

// RepoService is the part of the repos use cases the adapter reads.
type RepoService interface {
	List(ctx context.Context, workspaceID string) ([]reposdomain.Repo, error)
}

// Agents implements app.AgentDirectory over the agents and skills use cases.
type Agents struct {
	agents AgentService
	skills SkillService
}

var _ app.AgentDirectory = (*Agents)(nil)

// NewAgents builds the directory.
func NewAgents(agents AgentService, skills SkillService) *Agents {
	return &Agents{agents: agents, skills: skills}
}

// Get returns the agent with its linked and enabled skills in link order, the set a live review
// of the agent carries. A link whose skill is gone is left out.
func (d *Agents) Get(ctx context.Context, workspaceID, agentID string) (domain.ExportAgent, bool, error) {
	a, found, err := d.agent(ctx, workspaceID, agentID)
	if err != nil || !found {
		return domain.ExportAgent{}, false, err
	}
	links, err := d.agents.SkillLinks(ctx, workspaceID, agentID)
	if err != nil {
		return domain.ExportAgent{}, false, fmt.Errorf("list skill links of agent %s: %w", agentID, err)
	}
	skills := make([]domain.ExportSkill, 0, len(links))
	for _, l := range links {
		s, err := d.skills.Get(ctx, workspaceID, l.SkillID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return domain.ExportAgent{}, false, fmt.Errorf("get skill %s: %w", l.SkillID, err)
		}
		if s.Enabled {
			skills = append(skills, domain.ExportSkill{Name: s.Name, Body: s.Body})
		}
	}
	return domain.ExportAgent{
		ID: a.ID, Name: a.Name, Provider: a.Provider, Model: a.Model, SystemPrompt: a.SystemPrompt,
		Strategy: a.Strategy, CIFailOn: a.CIFailOn, Version: a.Version, Skills: skills,
	}, true, nil
}

// Exists reports whether the workspace has the agent.
func (d *Agents) Exists(ctx context.Context, workspaceID, agentID string) (bool, error) {
	_, found, err := d.agent(ctx, workspaceID, agentID)
	return found, err
}

func (d *Agents) agent(ctx context.Context, workspaceID, agentID string) (agentsdomain.Agent, bool, error) {
	a, err := d.agents.Get(ctx, workspaceID, agentID)
	if isNotFound(err) {
		return agentsdomain.Agent{}, false, nil
	}
	if err != nil {
		return agentsdomain.Agent{}, false, err
	}
	return a, true, nil
}

func isNotFound(err error) bool {
	ae, ok := apperr.As(err)
	return ok && ae.Kind == apperr.KindNotFound
}

// Repos implements app.RepoDirectory over the repos use cases.
type Repos struct{ repos RepoService }

var _ app.RepoDirectory = (*Repos)(nil)

// NewRepos builds the directory.
func NewRepos(repos RepoService) *Repos { return &Repos{repos: repos} }

// FindByFullName matches the exact "owner/name" among the workspace's repositories.
func (d *Repos) FindByFullName(ctx context.Context, workspaceID, fullName string) (domain.ImportedRepo, bool, error) {
	list, err := d.repos.List(ctx, workspaceID)
	if err != nil {
		return domain.ImportedRepo{}, false, fmt.Errorf("list repos: %w", err)
	}
	for _, r := range list {
		if r.FullName == fullName {
			return domain.ImportedRepo{Owner: r.Owner, Name: r.Name, FullName: r.FullName, DefaultBranch: r.DefaultBranch}, true, nil
		}
	}
	return domain.ImportedRepo{}, false, nil
}
