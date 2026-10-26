package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/plugins/domain"
)

// errRaced marks a unique violation inside the install transaction: the plugin name, or a skill
// name, was taken by a concurrent writer. The transaction is rolled back before it is turned into a
// *domain.ConflictError, because an aborted transaction cannot be read from.
var errRaced = errors.New("unique violation while installing")

// Install creates the plugin (disabled) and everything it owns in one transaction. The plugin row
// goes first, so two installs of one name meet on installed_plugins_workspace_id_name_key before any
// heavy work. Agents, skills, their version rows, links, eval cases and learnings are multi-row
// INSERTs. A unique violation (the plugin name, or a skill name taken meanwhile) rolls everything
// back and is answered with a re-read of what exists, as a *domain.ConflictError; any other error
// rolls back and is returned.
//
// Install must not be called with a ctx that already carries a transaction: the re-read after a
// conflict needs the aborted one gone.
func (s *Store) Install(ctx context.Context, workspaceID string, in domain.Install) (domain.Installed, error) {
	var pluginID string
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		id, err := s.install(ctx, workspaceID, in)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("install plugin: %w: %w", errRaced, err)
			}
			return fmt.Errorf("install plugin: %w", err)
		}
		pluginID = id
		return nil
	})
	if errors.Is(err, errRaced) {
		found, rerr := s.Conflicts(ctx, workspaceID, in.Name, in.SkillNames())
		if rerr != nil {
			return domain.Installed{}, fmt.Errorf("read conflicts after install: %w", rerr)
		}
		if found.Any() {
			return domain.Installed{}, &domain.ConflictError{Conflicts: found}
		}
		return domain.Installed{}, err
	}
	if err != nil {
		return domain.Installed{}, err
	}
	p, err := s.plugin(ctx, workspaceID, pluginID)
	if err != nil {
		return domain.Installed{}, fmt.Errorf("read installed plugin: %w", err)
	}
	return domain.Installed{Plugin: p, Created: in.Created()}, nil
}

// install runs the statements of an install on the transaction ctx carries and returns the new
// plugin's id.
func (s *Store) install(ctx context.Context, ws string, in domain.Install) (string, error) {
	db := pg.Conn(ctx, s.db)

	var base time.Time
	if err := db.Raw("SELECT clock_timestamp()").Scan(&base).Error; err != nil {
		return "", fmt.Errorf("read clock: %w", err)
	}
	clock := &stamps{base: base}

	pluginID, err := newUUID()
	if err != nil {
		return "", err
	}
	plugin := pluginRow{
		ID: pluginID, WorkspaceID: ws, Name: clean(in.Name), Version: optional(in.Version),
		Description: cleanPtr(in.Description), Source: optional(in.Source), Enabled: false,
	}
	if err := db.Create(&plugin).Error; err != nil {
		return "", fmt.Errorf("insert plugin: %w", err)
	}

	// Skills with their version 1 (the skills context writes both on create).
	skillIDs := make(map[string]string, len(in.Skills))
	skills := make([]skillRow, len(in.Skills))
	skillVersions := make([]skillVersionRow, len(in.Skills))
	for i, sk := range in.Skills {
		id, err := newUUID()
		if err != nil {
			return "", err
		}
		skillIDs[sk.Name] = id
		var evidence *string
		if sk.EvidenceFiles != nil {
			raw, err := json.Marshal(sk.EvidenceFiles)
			if err != nil {
				return "", fmt.Errorf("encode evidence files of skill %d: %w", i, err)
			}
			evidence = jsonText(raw)
		}
		at := clock.next()
		ref := optional(sk.SourceRef)
		skills[i] = skillRow{
			ID: id, WorkspaceID: ws, Name: clean(sk.Name), Description: clean(sk.Description), Type: sk.Type,
			Source: sk.Source, SourceRef: ref, Body: clean(sk.Body), Enabled: sk.Enabled, Version: 1,
			EvidenceFiles: evidence, PluginID: pluginID, CreatedAt: at, UpdatedAt: at,
		}
		desc, note := clean(sk.Description), clean(sk.VersionNote)
		skillVersions[i] = skillVersionRow{SkillID: id, Version: 1, Body: clean(sk.Body), Description: &desc, Message: &note}
	}
	if err := insertBatches(db, skills, "skills"); err != nil {
		return "", err
	}
	if err := insertBatches(db, skillVersions, "skill versions"); err != nil {
		return "", err
	}

	// Agents. An agent is created at version 1 with a snapshot of no skills; linking skills then
	// raises its version by one and snapshots the linked ids, exactly as the agents context does
	// (Create, then SetSkills), so an agent with skills lands at version 2.
	agentIDs := make(map[string]string, len(in.Agents))
	agents := make([]agentRow, len(in.Agents))
	var versions []agentVersionRow
	var links []linkRow
	for i, a := range in.Agents {
		id, err := newUUID()
		if err != nil {
			return "", err
		}
		agentIDs[a.Name] = id
		linked := make([]string, len(a.Skills))
		for j, name := range a.Skills {
			sid, ok := skillIDs[name]
			if !ok {
				return "", fmt.Errorf("agent %d links skill %d that the plan does not create", i, j)
			}
			linked[j] = sid
			links = append(links, linkRow{AgentID: id, SkillID: sid, Order: j})
		}
		cfg := agentConfig{
			Provider: a.Provider, Model: a.Model, SystemPrompt: clean(a.SystemPrompt), OutputSchema: schemaOrNil(a.OutputSchema),
			Strategy: a.Strategy, CIFailOn: a.CIFailOn, RepoIntel: a.RepoIntel, Skills: []string{},
		}
		v1, err := configJSON(cfg)
		if err != nil {
			return "", err
		}
		version := 1
		versions = append(versions, agentVersionRow{AgentID: id, Version: 1, ConfigJSON: v1})
		if len(linked) > 0 {
			cfg.Skills = linked
			v2, err := configJSON(cfg)
			if err != nil {
				return "", err
			}
			version = 2
			versions = append(versions, agentVersionRow{AgentID: id, Version: 2, ConfigJSON: v2})
		}
		agents[i] = agentRow{
			ID: id, WorkspaceID: ws, Name: clean(a.Name), Description: clean(a.Description), Provider: a.Provider,
			Model: a.Model, SystemPrompt: clean(a.SystemPrompt), OutputSchema: jsonText(a.OutputSchema), Enabled: a.Enabled,
			Version: version, Strategy: a.Strategy, CIFailOn: a.CIFailOn, RepoIntel: a.RepoIntel, PluginID: pluginID,
			CreatedAt: clock.next(),
		}
	}
	if err := insertBatches(db, agents, "agents"); err != nil {
		return "", err
	}
	if err := insertBatches(db, versions, "agent versions"); err != nil {
		return "", err
	}
	if err := insertBatches(db, links, "agent skills"); err != nil {
		return "", err
	}

	// Eval cases, owned by the agent or skill their owner_ref names; the owner has no foreign key.
	cases := make([]evalCaseRow, len(in.EvalCases))
	for i, c := range in.EvalCases {
		var owner string
		var ok bool
		switch c.OwnerKind {
		case domain.CaseOwnerAgent:
			owner, ok = agentIDs[c.OwnerRef]
		case domain.CaseOwnerSkill:
			owner, ok = skillIDs[c.OwnerRef]
		}
		if !ok {
			return "", fmt.Errorf("eval case %d has an owner the plan does not create", i)
		}
		id, err := newUUID()
		if err != nil {
			return "", err
		}
		cases[i] = evalCaseRow{
			ID: id, WorkspaceID: ws, OwnerKind: c.OwnerKind, OwnerID: owner, Name: clean(c.Name),
			InputDiff: cleanPtr(c.InputDiff), InputFiles: jsonText(c.InputFiles), InputMeta: jsonText(c.InputMeta),
			ExpectedOutput: jsonText(c.ExpectedOutput), Notes: cleanPtr(c.Notes), CreatedAt: clock.next(),
		}
	}
	if err := insertBatches(db, cases, "eval cases"); err != nil {
		return "", err
	}

	// Learnings: no repository (D13), no embedding.
	learnings := make([]memoryRow, len(in.Learnings))
	for i, l := range in.Learnings {
		id, err := newUUID()
		if err != nil {
			return "", err
		}
		at := clock.next()
		learnings[i] = memoryRow{
			ID: id, WorkspaceID: ws, Scope: l.Scope, Kind: l.Kind, Content: clean(l.Content),
			Confidence: l.Confidence, PluginID: pluginID, CreatedAt: at, UpdatedAt: at,
		}
	}
	if err := insertBatches(db, learnings, "learnings"); err != nil {
		return "", err
	}
	return pluginID, nil
}

// insertBatches inserts rows with multi-row INSERT statements of at most batchSize rows. An empty
// slice is no statement (GORM refuses one). what names the rows in the error.
func insertBatches[T any](db *gorm.DB, rows []T, what string) error {
	if len(rows) == 0 {
		return nil
	}
	if err := db.CreateInBatches(&rows, batchSize).Error; err != nil {
		return fmt.Errorf("insert %s: %w", what, err)
	}
	return nil
}

func schemaOrNil(raw json.RawMessage) json.RawMessage {
	if t := jsonText(raw); t != nil {
		return json.RawMessage(*t)
	}
	return nil
}

// configJSON encodes an agent snapshot as a jsonb-safe document.
func configJSON(cfg agentConfig) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encode agent config: %w", err)
	}
	doc := jsonText(raw)
	if doc == nil {
		return "", errors.New("encode agent config: empty document")
	}
	return *doc, nil
}
