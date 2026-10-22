// Package domain holds the agent model and its rules: defaults, what counts as a config change
// and the ordering of linked skills. It has no I/O and no framework dependency.
package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Defaults of the optional create fields.
const (
	DefaultStrategy = "single-pass"
	DefaultCIFailOn = "critical"
	// InitialVersion is the version a new agent starts at.
	InitialVersion = 1
)

// ErrNotFound means the agent does not exist in the workspace.
var ErrNotFound = errors.New("agent not found")

// ErrVersionNotFound means the agent has no snapshot of the requested version.
var ErrVersionNotFound = errors.New("agent version not found")

// Providers lists the model providers an agent may use.
func Providers() []string { return []string{"openai", "anthropic", "openrouter"} }

// Strategies lists the review strategies.
func Strategies() []string { return []string{"single-pass", "map-reduce", "auto"} }

// CIFailOns lists the CI gate policies.
func CIFailOns() []string { return []string{"never", "critical", "warning", "any"} }

// Agent is a reviewer configuration. OutputSchema is raw JSON; nil means JSON null.
type Agent struct {
	ID           string
	Name         string
	Description  string
	Provider     string
	Model        string
	SystemPrompt string
	OutputSchema json.RawMessage
	Enabled      bool
	Version      int
	Strategy     string
	CIFailOn     string
	RepoIntel    bool
}

// CreateInput is a validated POST /agents body; nil pointers are absent optional fields.
type CreateInput struct {
	Name         string
	Description  *string
	Provider     string
	Model        string
	SystemPrompt string
	OutputSchema json.RawMessage
	Strategy     *string
	CIFailOn     *string
	RepoIntel    *bool
	Enabled      *bool
}

// New builds the agent a create request describes, with the defaults applied. The ID is left
// empty (the store assigns it) and the version is InitialVersion.
func New(in CreateInput) Agent {
	a := Agent{
		Name:         in.Name,
		Provider:     in.Provider,
		Model:        in.Model,
		SystemPrompt: in.SystemPrompt,
		OutputSchema: NormalizeSchema(in.OutputSchema),
		Enabled:      true,
		Version:      InitialVersion,
		Strategy:     DefaultStrategy,
		CIFailOn:     DefaultCIFailOn,
		RepoIntel:    true,
	}
	if in.Description != nil {
		a.Description = *in.Description
	}
	if in.Strategy != nil {
		a.Strategy = *in.Strategy
	}
	if in.CIFailOn != nil {
		a.CIFailOn = *in.CIFailOn
	}
	if in.RepoIntel != nil {
		a.RepoIntel = *in.RepoIntel
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	return a
}

// NormalizeSchema maps an absent value and the JSON literal null to nil.
func NormalizeSchema(raw json.RawMessage) json.RawMessage {
	if t := bytes.TrimSpace(raw); len(t) != 0 && !bytes.Equal(t, []byte("null")) {
		return raw
	}
	return nil
}

// Patch is a validated PUT /agents/:id body. A nil pointer leaves the field unchanged.
// SetOutputSchema tells that output_schema was present, with a value or with null.
type Patch struct {
	Name            *string
	Description     *string
	Provider        *string
	Model           *string
	SystemPrompt    *string
	Strategy        *string
	CIFailOn        *string
	RepoIntel       *bool
	Enabled         *bool
	SetOutputSchema bool
	OutputSchema    json.RawMessage
}

// Apply returns the agent with the patch applied. configChanged is true when the patch gives a
// config field a different value or provides output_schema at all; the caller then bumps the
// version and writes a snapshot. Toggling enabled alone, an all-equal patch and an empty patch
// are not config changes.
func (p Patch) Apply(a Agent) (updated Agent, configChanged bool) {
	updated = a
	set := func(dst *string, v *string) {
		if v != nil && *v != *dst {
			*dst = *v
			configChanged = true
		}
	}
	set(&updated.Name, p.Name)
	set(&updated.Description, p.Description)
	set(&updated.Provider, p.Provider)
	set(&updated.Model, p.Model)
	set(&updated.SystemPrompt, p.SystemPrompt)
	set(&updated.Strategy, p.Strategy)
	set(&updated.CIFailOn, p.CIFailOn)
	if p.RepoIntel != nil && *p.RepoIntel != updated.RepoIntel {
		updated.RepoIntel = *p.RepoIntel
		configChanged = true
	}
	if p.SetOutputSchema {
		updated.OutputSchema = NormalizeSchema(p.OutputSchema)
		configChanged = true
	}
	if p.Enabled != nil {
		updated.Enabled = *p.Enabled
	}
	return updated, configChanged
}

// VersionConfig is the immutable config snapshot of one agent version.
type VersionConfig struct {
	Provider     string
	Model        string
	SystemPrompt string
	OutputSchema json.RawMessage
	Strategy     string
	CIFailOn     string
	RepoIntel    bool
	Skills       []string
}

// Version is one recorded snapshot.
type Version struct {
	AgentID   string
	Version   int
	Config    VersionConfig
	CreatedAt time.Time
}

// Snapshot captures the agent's config together with its linked skill ids (in link order).
func (a Agent) Snapshot(skills []string) VersionConfig {
	return VersionConfig{
		Provider:     a.Provider,
		Model:        a.Model,
		SystemPrompt: a.SystemPrompt,
		OutputSchema: a.OutputSchema,
		Strategy:     a.Strategy,
		CIFailOn:     a.CIFailOn,
		RepoIntel:    a.RepoIntel,
		Skills:       append([]string{}, skills...),
	}
}

// SkillLink is one ordered link from an agent to a skill.
type SkillLink struct {
	AgentID string
	SkillID string
	Order   int
}

// WithSkillAt returns current with skillID at index order: the skill is removed first when it is
// linked already, then inserted at order clamped to the list length; a nil order appends.
func WithSkillAt(current []string, skillID string, order *int) []string {
	rest := make([]string, 0, len(current)+1)
	for _, id := range current {
		if id != skillID {
			rest = append(rest, id)
		}
	}
	at := len(rest)
	if order != nil {
		at = max(0, min(*order, len(rest)))
	}
	return slices.Insert(rest, at, skillID)
}

// Dedupe drops repeated ids, keeping the first occurrence.
func Dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// UnknownSkillIDs returns the requested ids that are not in known, in request order.
func UnknownSkillIDs(requested []string, known map[string]bool) []string {
	var unknown []string
	for _, id := range requested {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	return unknown
}

// UnknownSkillsError reports skill ids that are not skills of the agent's workspace.
type UnknownSkillsError struct {
	IDs []string
}

// Error implements error with the client-visible message.
func (e *UnknownSkillsError) Error() string {
	return "Unknown skill id(s) for this workspace: " + strings.Join(e.IDs, ", ")
}
