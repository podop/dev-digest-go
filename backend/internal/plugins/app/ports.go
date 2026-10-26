// Package app holds the use cases of the plugins context: the six routes of Settings >
// Integrations (list, export, preview, install, enable/disable, remove). It reaches the database,
// the settings and the workspace name only through the consumer-side ports below, which
// `cmd/server` wires; the plugins adapter that implements ExportReader, Names, Installer and
// Plugins writes other contexts' tables (agents, skills, eval cases, memory) itself.
package app

import (
	"context"
	"encoding/json"

	"dev-digest-go/backend/internal/plugins/domain"
)

// ExportSelection says what an export reads. Both id lists empty means every agent and every
// skill of the workspace; AgentIDs selects those agents plus every skill linked to them; SkillIDs
// selects those skills; both give the union. Ids are UUIDs already checked for shape.
type ExportSelection struct {
	AgentIDs []string
	SkillIDs []string
	// EvalCases asks for the eval cases of the selected agents and skills; Learnings asks for
	// the workspace's learnings (all memory rows of kind learning, with their repository's full name).
	EvalCases bool
	Learnings bool
}

// ExportReader reads the records of an export.
type ExportReader interface {
	// ReadExport returns the selected records in creation order (an agent's SkillNames in link
	// order, only skills of the workspace) and missing, the requested ids that are no agent or
	// skill of the workspace (in request order). With a non-empty missing the data may be empty.
	// Lists the selection does not ask for (eval cases, learnings) stay empty.
	ReadExport(ctx context.Context, workspaceID string, sel ExportSelection) (data domain.ExportData, missing []string, err error)
}

// Names reads what already exists, for the conflict check (FR8).
type Names interface {
	// Conflicts reports whether a plugin named pluginName is installed in the workspace and which
	// of skillNames are skill names of the workspace; Skills keeps the order of skillNames.
	Conflicts(ctx context.Context, workspaceID, pluginName string, skillNames []string) (domain.Conflicts, error)
}

// CustomProviders reads which custom providers the workspace defines (FR15). The plugins context
// never creates or reads a definition; it only learns whether an id exists, to warn in the dry run.
type CustomProviders interface {
	// CustomDefined returns the ids among ids (`custom:<slug>`, well-formed) that the workspace
	// defines, in the order of ids. An id that is not defined is left out.
	CustomDefined(ctx context.Context, workspaceID string, ids []string) (defined []string, err error)
}

// Installer creates an installed plugin with everything it owns.
type Installer interface {
	// Install creates, in one all-or-nothing transaction, the plugin (disabled) and every agent,
	// skill (with its version 1), eval case and learning of the plan, all owned by the plugin.
	// A unique-name violation (two installs at once, or a skill name taken meanwhile) is a
	// *domain.ConflictError built from a re-read; any other failure leaves nothing behind.
	Install(ctx context.Context, workspaceID string, in domain.Install) (domain.Installed, error)
}

// Plugins reads and changes installed plugins.
type Plugins interface {
	// List returns the workspace's plugins, newest first, with the counts of what each owns.
	List(ctx context.Context, workspaceID string) ([]domain.Plugin, error)
	// SetEnabled sets the flag and returns the plugin as List does; domain.ErrNotFound for an
	// unknown id (a malformed id is not found either).
	SetEnabled(ctx context.Context, workspaceID, id string, enabled bool) (domain.Plugin, error)
	// Remove deletes the plugin and everything it owns in one transaction: it locks the plugin,
	// calls check with the plugin's name (a non-nil error aborts and is returned unchanged),
	// counts the CI installations of its agents, deletes the eval cases of its agents and skills,
	// its learnings, agents, skills, then the plugin. domain.ErrNotFound for an unknown id.
	Remove(ctx context.Context, workspaceID, id string, check func(name string) error) (domain.Removed, error)
}

// LastExports keeps the last export, the Settings row domain.LastExportKey.
type LastExports interface {
	// LastExport returns the stored value as raw JSON; found is false when no row exists.
	LastExport(ctx context.Context, workspaceID string) (raw json.RawMessage, found bool, err error)
	// SetLastExport stores the value for the user the request acts as.
	SetLastExport(ctx context.Context, workspaceID, userID string, raw json.RawMessage) error
}

// WorkspaceName reads the workspace's display name (the Settings row `workspace_name`), the base
// of the default export name.
type WorkspaceName interface {
	// WorkspaceName returns the stored text as stored; found is false when no row exists. The
	// caller treats an empty text like a missing one.
	WorkspaceName(ctx context.Context, workspaceID string) (name string, found bool, err error)
}
