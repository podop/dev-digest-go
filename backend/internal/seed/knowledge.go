package seed

import (
	"bytes"
	"context"
	_ "embed" // go:embed for the generated Node seed data
	"encoding/json"
	"fmt"
)

// The four built-in reviewer agents (with their skill links) and the nine built-in skills,
// generated from the dev-digest Node seed (db/seed-prompts.ts, db/seed-skills.ts) so their
// text is byte-identical. The regeneration command is in the Phase 2 context pack, [F9].
var (
	//go:embed data/agents.json
	agentsJSON []byte
	//go:embed data/skills.json
	skillsJSON []byte
)

const (
	// seedAgentProvider and seedAgentModel are the provider and model of every seeded agent.
	seedAgentProvider = "openrouter"
	seedAgentModel    = "deepseek/deepseek-v4-flash"
)

// Agent is a built-in agent row. Strategy, ci_fail_on, repo_intel, output_schema and
// version keep their column defaults; the agent is enabled and has no version snapshot.
type Agent struct {
	WorkspaceID  string
	Name         string
	Description  string
	Provider     string
	Model        string
	SystemPrompt string
	CreatedBy    string
}

// Skill is a built-in skill row (source manual, version 1). The store also writes its
// version-1 snapshot with the message "Created".
type Skill struct {
	WorkspaceID string
	Name        string
	Type        string
	Description string
	Body        string
	Enabled     bool
}

// agentSeed is one entry of data/agents.json; Skills are skill names in link order.
type agentSeed struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"system_prompt"`
	Skills       []string `json:"skills"`
}

// skillSeed is one entry of data/skills.json.
type skillSeed struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

type knowledge struct {
	agents []agentSeed
	skills []skillSeed
}

// loadKnowledge decodes the embedded seed data and checks that every link names a skill.
func loadKnowledge() (knowledge, error) {
	var k knowledge
	if err := decodeStrict(agentsJSON, &k.agents); err != nil {
		return knowledge{}, fmt.Errorf("decode agents.json: %w", err)
	}
	if err := decodeStrict(skillsJSON, &k.skills); err != nil {
		return knowledge{}, fmt.Errorf("decode skills.json: %w", err)
	}
	known := make(map[string]bool, len(k.skills))
	for _, s := range k.skills {
		known[s.Name] = true
	}
	for _, a := range k.agents {
		for _, name := range a.Skills {
			if !known[name] {
				return knowledge{}, fmt.Errorf("agent %q links unknown skill %q", a.Name, name)
			}
		}
	}
	return k, nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// writeKnowledge seeds the agents, skills and links (FR261). It runs only for a workspace
// created in this run, or one that has no agent and no skill at all (D213), so a restart
// never re-creates a built-in the user deleted. Within a run an agent or skill whose name
// exists is skipped, and links are written only for an agent without any link (FR262).
func (s *Seeder) writeKnowledge(ctx context.Context, tx Tx, workspaceID, userID string, workspaceInserted bool, res *Result) error {
	if !workspaceInserted {
		occupied, err := tx.HasAgentsOrSkills(ctx, workspaceID)
		if err != nil {
			return fmt.Errorf("check agents and skills: %w", err)
		}
		if occupied {
			return nil
		}
	}
	k, err := loadKnowledge()
	if err != nil {
		return err
	}

	agentIDs := make(map[string]string, len(k.agents))
	for _, a := range k.agents {
		id, inserted, err := tx.EnsureAgent(ctx, Agent{
			WorkspaceID:  workspaceID,
			Name:         a.Name,
			Description:  a.Description,
			Provider:     seedAgentProvider,
			Model:        seedAgentModel,
			SystemPrompt: a.SystemPrompt,
			CreatedBy:    userID,
		})
		if err != nil {
			return fmt.Errorf("agent %s: %w", a.Name, err)
		}
		agentIDs[a.Name] = id
		res.Agents += b2i(inserted)
	}

	skillIDs := make(map[string]string, len(k.skills))
	for _, sk := range k.skills {
		id, inserted, err := tx.EnsureSkill(ctx, Skill{
			WorkspaceID: workspaceID,
			Name:        sk.Name,
			Type:        sk.Type,
			Description: sk.Description,
			Body:        sk.Body,
			Enabled:     sk.Enabled,
		})
		if err != nil {
			return fmt.Errorf("skill %s: %w", sk.Name, err)
		}
		skillIDs[sk.Name] = id
		res.Skills += b2i(inserted)
	}

	for _, a := range k.agents {
		ids := make([]string, len(a.Skills))
		for i, name := range a.Skills {
			ids[i] = skillIDs[name]
		}
		linked, err := tx.LinkAgentSkills(ctx, agentIDs[a.Name], ids)
		if err != nil {
			return fmt.Errorf("skill links of %s: %w", a.Name, err)
		}
		res.AgentSkills += linked
	}
	return nil
}
