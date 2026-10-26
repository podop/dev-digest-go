package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/plugins/domain"
)

// Export reads the selection, builds the bundle, zips it and records the last export. The name
// and the description are checked before anything is read. A failure to record the last export
// after the archive was built does not fail the download: it is logged as a warning.
func (s *Service) Export(ctx context.Context, workspaceID, userID string, in ExportInput) (ExportResult, error) {
	name, err := s.exportName(ctx, workspaceID, in.Name)
	if err != nil {
		return ExportResult{}, err
	}
	if domain.UTF16Len(in.Description) > domain.ExportDescriptionMax {
		return ExportResult{}, fieldError("description",
			fmt.Sprintf("Description must be at most %d characters", domain.ExportDescriptionMax), nil)
	}
	var description *string
	if in.Description != "" {
		description = &in.Description
	}

	data, missing, err := s.d.Reader.ReadExport(ctx, workspaceID, ExportSelection{
		AgentIDs: in.AgentIDs, SkillIDs: in.SkillIDs, EvalCases: in.EvalCases, Learnings: in.Learnings,
	})
	if err != nil {
		return ExportResult{}, fmt.Errorf("read plugin export: %w", err)
	}
	if len(missing) > 0 {
		return ExportResult{}, apperr.NotFound("Unknown agent or skill ids").
			WithDetails(map[string][]string{"ids": missing})
	}
	if len(data.Agents) == 0 && len(data.Skills) == 0 {
		return ExportResult{}, apperr.Validation("Select at least one agent or skill to export").
			WithCode(CodeNothingToExport)
	}

	now := s.now()
	bundle := domain.BuildBundle(data, domain.Meta{Name: name, Description: description, Version: s.d.Version, Now: now})
	archive, err := domain.EncodeArchive(bundle)
	if err != nil {
		if errors.Is(err, domain.ErrExportTooLarge) {
			return ExportResult{}, apperr.Validation("The exported plugin is larger than 20 MiB").
				WithCode(CodeExportTooLarge).WithCause(err)
		}
		return ExportResult{}, fmt.Errorf("encode plugin archive: %w", err)
	}

	fileName := domain.ExportFileName(name, now)
	last := domain.NewLastExport(fileName, bundle)
	s.recordLastExport(ctx, workspaceID, userID, name, last)
	s.log.InfoContext(ctx, "plugin exported", "plugin_name", name, "file_name", fileName,
		"agents", last.Counts.Agents, "skills", last.Counts.Skills,
		"eval_cases", last.Counts.EvalCases, "learnings", last.Counts.Learnings)
	return ExportResult{FileName: fileName, Archive: archive, Last: last}, nil
}

// exportName is the checked plugin name of an export: the given one, or the default built from
// the workspace name.
func (s *Service) exportName(ctx context.Context, workspaceID, given string) (string, error) {
	if given != "" {
		if err := domain.CheckExportName(given); err != nil {
			return "", fieldError("name",
				fmt.Sprintf("Name must be 1 to %d characters of lower-case letters, digits and single dashes", domain.ExportNameMax), err)
		}
		return given, nil
	}
	ws, found, err := s.d.WorkspaceNames.WorkspaceName(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("read workspace name: %w", err)
	}
	if !found || ws == "" {
		ws = defaultWorkspaceName
	}
	return domain.DefaultName(ws), nil
}

// recordLastExport stores the export as the last one; a failure is a warning, never an error.
func (s *Service) recordLastExport(ctx context.Context, workspaceID, userID, name string, last domain.LastExport) {
	raw, err := domain.EncodeLastExport(last)
	if err == nil {
		err = s.d.LastExports.SetLastExport(ctx, workspaceID, userID, raw)
	}
	if err != nil {
		s.log.WarnContext(ctx, "plugin export: last export not recorded", "plugin_name", name,
			"agents", last.Counts.Agents, "skills", last.Counts.Skills,
			"eval_cases", last.Counts.EvalCases, "learnings", last.Counts.Learnings, "err", err)
	}
}
