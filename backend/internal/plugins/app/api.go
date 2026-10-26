package app

import (
	"context"

	"dev-digest-go/backend/internal/plugins/domain"
)

// Wire codes of the plugin refusals (spec section 7). Every other failure uses the default code of
// its apperr kind: not_found, validation_error, internal_error.
const (
	// CodeInvalidPlugin is the 422 of a rejected upload; details.issues lists up to
	// domain.MaxIssues rules, the message is the first.
	CodeInvalidPlugin = "invalid_plugin"
	// CodePluginConflict is the 409 of an install over an existing plugin or skill name;
	// details are {plugin: bool, skills: [name]}.
	CodePluginConflict = "plugin_conflict"
	// CodeNothingToExport is the 422 of an export that selects no agent and no skill.
	CodeNothingToExport = "nothing_to_export"
	// CodeExportTooLarge is the 422 of an export over domain.MaxInflatedBytes.
	CodeExportTooLarge = "export_too_large"
	// CodeConfirmationMismatch is the 422 of a removal whose confirm is not the plugin's name.
	CodeConfirmationMismatch = "confirmation_mismatch"
	// CodePluginDisabled is the 409 of a run start that needs an agent or skill of a disabled
	// plugin (FR13); the run contexts return it, not this package.
	CodePluginDisabled = "plugin_disabled"
)

// ExportInput is a POST /plugins/export request after the transport checked its shape (unknown
// keys, uuid ids, list sizes) and applied the defaults. The service checks the rules: Name with
// domain.CheckExportName, Description with domain.ExportDescriptionMax.
type ExportInput struct {
	// Name is the plugin name; "" takes domain.DefaultName of the workspace name.
	Name string
	// Description is the manifest description; "" is none.
	Description string
	AgentIDs    []string
	SkillIDs    []string
	// EvalCases (default true) and Learnings (default false) are the include flags.
	EvalCases bool
	Learnings bool
}

// ExportResult is the export answer: the download file name and the .zip bytes, and the record
// stored as the last export.
type ExportResult struct {
	FileName string
	Archive  []byte
	Last     domain.LastExport
}

// ImportInput is an import request: the request's Content-Type, the raw body (already capped by
// the transport at domain.ArchiveMaxZipBytes for a .zip and domain.MaxInflatedBytes for JSON)
// and the `filename` query value ("" when absent), which the service checks with
// domain.CheckFileName.
type ImportInput struct {
	Media    string
	Body     []byte
	FileName string
}

// Listing is the GET /plugins answer: the plugins newest first and the last export or nil.
type Listing struct {
	Plugins    []domain.Plugin
	LastExport *domain.LastExport
}

// API is the plugins use cases as transport sees them; *Service implements it. Failures are apperr
// errors that httpx renders: 404 not_found (unknown plugin, `details.ids` for unknown export ids),
// 422 validation_error (name, description, file name, confirmation shape), invalid_plugin,
// nothing_to_export, export_too_large, confirmation_mismatch, 409 plugin_conflict, 415 for an
// unsupported media type, 500 internal_error. Logs carry plugin id, name and counts only.
type API interface {
	// List returns the installed plugins and the last export.
	List(ctx context.Context, workspaceID string) (Listing, error)
	// Export reads the selection, builds the bundle, zips it and records the last export for userID.
	Export(ctx context.Context, workspaceID, userID string, in ExportInput) (ExportResult, error)
	// Preview parses and validates the upload and reports what installing it would do and
	// which names are taken. It stores nothing.
	Preview(ctx context.Context, workspaceID string, in ImportInput) (domain.PreviewDoc, error)
	// Install runs the same checks, refuses with 409 when the preview has a conflict, then
	// installs the plugin disabled.
	Install(ctx context.Context, workspaceID string, in ImportInput) (domain.Installed, error)
	// SetEnabled enables or disables a plugin and returns its row.
	SetEnabled(ctx context.Context, workspaceID, id string, enabled bool) (domain.Plugin, error)
	// Remove deletes the plugin and what it owns once confirm equals its name exactly.
	Remove(ctx context.Context, workspaceID, id, confirm string) (domain.Removed, error)
}
