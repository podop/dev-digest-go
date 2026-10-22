package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/skills/domain"
)

// AgentLinker is the agents context as the merge needs it: appending a skill to an agent's links
// (which bumps the agent's version). It is called with the merge's transaction context.
type AgentLinker interface {
	AppendSkill(ctx context.Context, workspaceID, agentID, skillID string) error
}

// ConventionGate is the conventions context as the merge needs it. Both methods are called with
// the merge's transaction context.
type ConventionGate interface {
	// NotAccepted returns the ids that are not an accepted convention of the repo.
	NotAccepted(ctx context.Context, workspaceID, repoID string, ids []string) ([]string, error)
	// AttachSkill sets skill_id on the conventions and bumps their updated_at.
	AttachSkill(ctx context.Context, workspaceID, repoID string, ids []string, skillID string) error
}

// RepoRef is the part of a repo the merge needs.
type RepoRef struct {
	ID       string
	FullName string
}

// mergeLookup is the read-only lookups of the merge, on the skills side's own row structs.
type mergeLookup interface {
	// FindRepo returns domain.ErrRepoNotFound for an unknown id or a repo of another workspace.
	FindRepo(ctx context.Context, workspaceID, repoID string) (RepoRef, error)
	// FindAgents returns the agents of the workspace among ids, in any order; unknown ids are
	// simply absent.
	FindAgents(ctx context.Context, workspaceID string, ids []string) ([]domain.AgentRef, error)
}

// MergerDeps are the collaborators of Merger.
type MergerDeps struct {
	Tx          TxRunner
	Skills      *Service
	Lookup      mergeLookup
	Agents      AgentLinker
	Conventions ConventionGate
}

// Merger turns accepted conventions into a skill linked to agents.
type Merger struct {
	d MergerDeps
}

// NewMerger builds a Merger.
func NewMerger(d MergerDeps) *Merger { return &Merger{d: d} }

// MergeInput is a validated POST /repos/:id/conventions/skill body with its defaults applied.
type MergeInput struct {
	ConventionIDs []string
	AgentIDs      []string
	Name          string
	Description   string
	Type          domain.SkillType
	Body          string
	Enabled       bool
}

// MergeResult is the created skill (used_by = the number of linked agents) and the agents it
// was linked to, in the order they were sent.
type MergeResult struct {
	Skill        domain.Skill
	LinkedAgents []domain.AgentRef
}

// Merge creates an extracted skill from the accepted conventions of a repo, links it to the
// agents and marks the conventions, all in one transaction: any failure leaves no skill, link or
// convention change. It checks, in order: the repo (404), the conventions (422 validation_error
// with details.ids), the agents (422 unknown_agent with details.id), then the duplicate name
// (409 conflict) when the skill is created.
func (m *Merger) Merge(ctx context.Context, workspaceID, repoID string, in MergeInput) (MergeResult, error) {
	var out MergeResult
	err := m.d.Tx.InTx(ctx, func(ctx context.Context) error {
		repo, err := m.d.Lookup.FindRepo(ctx, workspaceID, repoID)
		if errors.Is(err, domain.ErrRepoNotFound) {
			return apperr.NotFound("Repo not found")
		}
		if err != nil {
			return fmt.Errorf("find repo: %w", err)
		}

		conventionIDs := dedupe(in.ConventionIDs)
		bad, err := m.d.Conventions.NotAccepted(ctx, workspaceID, repoID, conventionIDs)
		if err != nil {
			return fmt.Errorf("check conventions: %w", err)
		}
		if len(bad) > 0 {
			return apperr.Validation("Only accepted conventions of this repo can be merged into a skill").
				WithDetails(map[string][]string{"ids": bad})
		}

		agents, err := m.resolveAgents(ctx, workspaceID, dedupe(in.AgentIDs))
		if err != nil {
			return err
		}

		skill, err := m.d.Skills.createExtracted(ctx, workspaceID, repo.FullName, CreateInput{
			Name: in.Name, Description: in.Description, Type: in.Type, Body: in.Body,
		}, in.Enabled)
		if err != nil {
			return err
		}
		for _, a := range agents {
			if err := m.d.Agents.AppendSkill(ctx, workspaceID, a.ID, skill.ID); err != nil {
				return fmt.Errorf("link skill to agent %s: %w", a.ID, err)
			}
		}
		if err := m.d.Conventions.AttachSkill(ctx, workspaceID, repoID, conventionIDs, skill.ID); err != nil {
			return fmt.Errorf("attach skill to conventions: %w", err)
		}
		skill.UsedBy = len(agents)
		out = MergeResult{Skill: skill, LinkedAgents: agents}
		return nil
	})
	if err != nil {
		return MergeResult{}, err
	}
	return out, nil
}

// resolveAgents returns the refs of ids in the order given, or 422 unknown_agent for the first
// id that is not an agent of the workspace.
func (m *Merger) resolveAgents(ctx context.Context, workspaceID string, ids []string) ([]domain.AgentRef, error) {
	if len(ids) == 0 {
		return []domain.AgentRef{}, nil
	}
	found, err := m.d.Lookup.FindAgents(ctx, workspaceID, ids)
	if err != nil {
		return nil, fmt.Errorf("find agents: %w", err)
	}
	byID := make(map[string]domain.AgentRef, len(found))
	for _, a := range found {
		byID[strings.ToLower(a.ID)] = a
	}
	refs := make([]domain.AgentRef, 0, len(ids))
	for _, id := range ids {
		a, ok := byID[id]
		if !ok {
			return nil, apperr.Validation("Unknown agent").WithCode("unknown_agent").WithDetails(map[string]string{"id": id})
		}
		refs = append(refs, a)
	}
	return refs, nil
}

// dedupe lower-cases UUIDs and drops repeats, keeping the first occurrence's position.
func dedupe(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(id)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
