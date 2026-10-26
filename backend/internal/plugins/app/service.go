package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/plugins/domain"
)

// validationMessage is the message of every 422 `validation_error` (httpx.ValidationMessage; the
// app layer does not import the HTTP package).
const validationMessage = "Request validation failed"

// defaultWorkspaceName is the workspace name the default export name is built from when no
// workspace name is stored; it is the display name the workspace context shows in that case.
const defaultWorkspaceName = "default"

// Clock returns the current time. The service reads it for the export instant and file name.
type Clock func() time.Time

// Deps are the ports and settings of Service. Every port is required except CustomProviders: nil
// means the workspace is not asked, and the dry run adds no "not configured here" warning. Clock
// defaults to time.Now and Log to a logger that discards; Version is the server build version
// written to the manifest of an export ("dev" when empty).
type Deps struct {
	Reader          ExportReader
	Names           Names
	Installer       Installer
	CustomProviders CustomProviders
	Plugins         Plugins
	LastExports     LastExports
	WorkspaceNames  WorkspaceName
	Clock           Clock
	Log             *slog.Logger
	Version         string
}

// Service implements API.
type Service struct {
	d   Deps
	now Clock
	log *slog.Logger
}

var _ API = (*Service)(nil)

// NewService builds the service from its ports.
func NewService(d Deps) *Service {
	s := &Service{d: d, now: d.Clock, log: d.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s
}

// List returns the installed plugins, newest first, and the last export.
func (s *Service) List(ctx context.Context, workspaceID string) (Listing, error) {
	plugins, err := s.d.Plugins.List(ctx, workspaceID)
	if err != nil {
		return Listing{}, fmt.Errorf("list plugins: %w", err)
	}
	if plugins == nil {
		plugins = []domain.Plugin{}
	}
	raw, found, err := s.d.LastExports.LastExport(ctx, workspaceID)
	if err != nil {
		return Listing{}, fmt.Errorf("read last plugin export: %w", err)
	}
	out := Listing{Plugins: plugins}
	if found {
		if last, ok := domain.DecodeLastExport(raw); ok {
			out.LastExport = &last
		}
	}
	return out, nil
}

// SetEnabled enables or disables a plugin.
func (s *Service) SetEnabled(ctx context.Context, workspaceID, id string, enabled bool) (domain.Plugin, error) {
	p, err := s.d.Plugins.SetEnabled(ctx, workspaceID, id, enabled)
	if err != nil {
		return domain.Plugin{}, storeError(fmt.Errorf("set plugin enabled: %w", err))
	}
	event := "plugin disabled"
	if enabled {
		event = "plugin enabled"
	}
	s.log.InfoContext(ctx, event, pluginAttrs(p)...)
	return p, nil
}

// Remove deletes a plugin and what it owns once confirm equals the plugin's name exactly. The
// confirmation is checked inside the store's transaction, on the name of the locked row.
func (s *Service) Remove(ctx context.Context, workspaceID, id, confirm string) (domain.Removed, error) {
	var name string
	removed, err := s.d.Plugins.Remove(ctx, workspaceID, id, func(n string) error {
		name = n
		return domain.ConfirmRemoval(n, confirm)
	})
	if err != nil {
		return domain.Removed{}, storeError(fmt.Errorf("remove plugin: %w", err))
	}
	s.log.InfoContext(ctx, "plugin removed",
		"plugin_id", id, "plugin_name", name,
		"agents", removed.Agents, "skills", removed.Skills, "eval_cases", removed.EvalCases,
		"learnings", removed.Learnings, "ci_installations", removed.CIInstallations)
	return removed, nil
}

// pluginAttrs are the log attributes of a plugin: id, name and counts, never content.
func pluginAttrs(p domain.Plugin) []any {
	return []any{
		"plugin_id", p.ID, "plugin_name", p.Name,
		"agents", p.Counts.Agents, "skills", p.Counts.Skills,
		"eval_cases", p.Counts.EvalCases, "learnings", p.Counts.Learnings,
	}
}

// storeError maps the sentinels of the plugin store and of the domain to application errors; any
// other error is returned unchanged (a 500 at the edge).
func storeError(err error) error {
	var (
		ce *domain.ConflictError
		ie *domain.InvalidError
	)
	switch {
	case errors.As(err, &ce):
		return conflictError(ce.Conflicts, ce)
	case errors.As(err, &ie):
		return invalidError(ie)
	case errors.Is(err, domain.ErrNotFound):
		return apperr.NotFound("Plugin not found").WithCause(err)
	case errors.Is(err, domain.ErrConfirmationMismatch):
		return apperr.Validation("Type the plugin name exactly to confirm").
			WithCode(CodeConfirmationMismatch).WithCause(domain.ErrConfirmationMismatch)
	case errors.Is(err, domain.ErrUnsupportedMedia):
		return unsupportedMedia(err)
	}
	return err
}

// invalidError is the 422 of a rejected upload: the message is the first broken rule and
// details.issues lists them all.
func invalidError(ie *domain.InvalidError) *apperr.Error {
	issues := ie.Issues
	if issues == nil {
		issues = []string{}
	}
	return apperr.Validation(ie.Message()).WithCode(CodeInvalidPlugin).
		WithDetails(map[string][]string{"issues": issues}).WithCause(ie)
}

// conflictError is the 409 of a name already taken: details are {plugin, skills}.
func conflictError(c domain.Conflicts, cause error) *apperr.Error {
	skills := c.Skills
	if skills == nil {
		skills = []string{}
	}
	return apperr.Conflict("A plugin or skill with the same name already exists").
		WithCode(CodePluginConflict).
		WithDetails(map[string]any{"plugin": c.Plugin, "skills": skills}).WithCause(cause)
}

// CodeUnsupportedMediaType is the wire code of an upload that is neither a .zip nor JSON. apperr
// has no 415 kind: the error is a 400 invalid_input whose cause is domain.ErrUnsupportedMedia, and
// transport answers it with 415 (errors.Is). The raw-body middleware refuses such requests first.
const CodeUnsupportedMediaType = "unsupported_media_type"

func unsupportedMedia(cause error) *apperr.Error {
	return apperr.InvalidInput("Unsupported Media Type").WithCode(CodeUnsupportedMediaType).WithCause(cause)
}

// fieldError is a 422 validation_error for one field, in the shape of the other request
// validation errors (one issue with its instance path).
func fieldError(field, message string, cause error) *apperr.Error {
	issue := map[string]any{"code": "custom", "path": []any{field}, "message": message}
	detail := map[string]any{
		"keyword":      "custom",
		"instancePath": "/" + field,
		"schemaPath":   "#/" + field + "/custom",
		"message":      message,
		"params":       map[string]any{"issue": issue},
	}
	return apperr.Validation(validationMessage).WithDetails([]any{detail}).WithCause(cause)
}
