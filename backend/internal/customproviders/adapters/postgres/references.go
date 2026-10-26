package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/customproviders/app"
	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm/custom"
	pg "dev-digest-go/backend/internal/platform/postgres"
)

// References implements app.References: it reads the agents and the settings rows that name a
// provider. It only reads the tables; the locks that make the answer reliable are taken by the
// delete before it asks (the provider row FOR UPDATE, which agent writes hold FOR SHARE).
type References struct {
	db *gorm.DB
}

var _ app.References = (*References)(nil)

// NewReferences builds References on a GORM handle.
func NewReferences(db *gorm.DB) *References { return &References{db: db} }

// agentRef is a row of the usage query.
type agentRef struct {
	ID   string `gorm:"column:id"`
	Name string `gorm:"column:name"`
}

// Find returns the agents (enabled or not) whose provider is id, the feature_models entries that
// name it and whether default_model does. Agents come ordered by name then id (byte order), the
// feature ids sorted. A settings value that is not the expected JSON is skipped (best effort, A8).
func (r *References) Find(ctx context.Context, workspaceID, id string) (domain.Usage, error) {
	if _, ok := custom.ParseID(id); !ok {
		return domain.Usage{}, nil
	}
	conn := pg.Conn(ctx, r.db)
	var agents []agentRef
	err := conn.Raw(`SELECT id::text AS id, name FROM agents WHERE workspace_id = ? AND provider = ?
		ORDER BY name COLLATE "C", id::text COLLATE "C"`, workspaceID, id).Scan(&agents).Error
	if err != nil {
		return domain.Usage{}, fmt.Errorf("select agents of custom provider: %w", err)
	}
	bag, err := r.settings(ctx, workspaceID)
	if err != nil {
		return domain.Usage{}, err
	}
	u := domain.Usage{}
	for _, a := range agents {
		u.Agents = append(u.Agents, domain.AgentRef{ID: a.ID, Name: a.Name})
	}
	for _, raw := range bag["feature_models"] {
		var features map[string]choice
		if json.Unmarshal(raw, &features) != nil {
			continue
		}
		for feature, c := range features {
			if c.Provider == id && !slices.Contains(u.FeatureModels, feature) {
				u.FeatureModels = append(u.FeatureModels, feature)
			}
		}
	}
	slices.Sort(u.FeatureModels)
	for _, raw := range bag["default_model"] {
		var c choice
		if json.Unmarshal(raw, &c) == nil && c.Provider == id {
			u.DefaultModel = true
		}
	}
	return u, nil
}

// DefaultModel returns the model of default_model when its provider is id.
func (r *References) DefaultModel(ctx context.Context, workspaceID, id string) (string, bool, error) {
	if _, ok := custom.ParseID(id); !ok {
		return "", false, nil
	}
	bag, err := r.settings(ctx, workspaceID)
	if err != nil {
		return "", false, err
	}
	for _, raw := range bag["default_model"] {
		var c choice
		if json.Unmarshal(raw, &c) == nil && c.Provider == id && c.Model != "" {
			return c.Model, true, nil
		}
	}
	return "", false, nil
}

// choice is the {provider, model} value of default_model and of a feature_models entry.
type choice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// settingRow is a row of the settings table as the two keys are read.
type settingRow struct {
	Key   string `gorm:"column:key"`
	Value string `gorm:"column:value"`
}

// settings returns the raw values of default_model and feature_models by key. A workspace can hold
// a row per user for a key, so a key may have several values; all of them count.
func (r *References) settings(ctx context.Context, workspaceID string) (map[string][]json.RawMessage, error) {
	var rows []settingRow
	err := pg.Conn(ctx, r.db).Raw(`SELECT key, value::text AS value FROM settings
		WHERE workspace_id = ? AND key IN ('default_model', 'feature_models') AND value IS NOT NULL
		ORDER BY key, id::text`, workspaceID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select model settings: %w", err)
	}
	bag := make(map[string][]json.RawMessage, 2)
	for _, row := range rows {
		bag[row.Key] = append(bag[row.Key], json.RawMessage(row.Value))
	}
	return bag, nil
}
