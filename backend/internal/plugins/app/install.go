package app

import (
	"context"
	"fmt"

	"dev-digest-go/backend/internal/plugins/domain"
)

// checked is an upload that passed every rule before anything is stored.
type checked struct {
	prepared domain.Prepared
	found    domain.Conflicts
}

// check runs the steps shared by Preview and Install: the file name, the parse, the validation
// and the lookup of taken names. It writes nothing.
func (s *Service) check(ctx context.Context, workspaceID string, in ImportInput) (checked, error) {
	if in.FileName != "" {
		if err := domain.CheckFileName(in.FileName); err != nil {
			return checked{}, fieldError("filename",
				fmt.Sprintf("File name must be 1 to %d characters without control characters", domain.FileNameMax), err)
		}
	}
	up, err := domain.ParseUpload(in.Media, in.Body)
	if err != nil {
		return checked{}, storeError(err)
	}
	prepared, err := domain.Prepare(up, in.FileName)
	if err != nil {
		return checked{}, storeError(err)
	}
	plan := prepared.Plan
	found, err := s.d.Names.Conflicts(ctx, workspaceID, plan.Name, plan.SkillNames())
	if err != nil {
		return checked{}, fmt.Errorf("check plugin names: %w", err)
	}
	return checked{prepared: prepared, found: found}, nil
}

// Preview reports what installing the upload would do and which names are taken. It stores nothing.
func (s *Service) Preview(ctx context.Context, workspaceID string, in ImportInput) (domain.PreviewDoc, error) {
	c, err := s.check(ctx, workspaceID, in)
	if err != nil {
		return domain.PreviewDoc{}, err
	}
	return c.prepared.Preview(c.found), nil
}

// Install runs the preview's checks, refuses with 409 plugin_conflict before writing when a name is
// taken, and installs the plugin (disabled). A conflict the store finds at write time (two
// installs at once) is the same 409.
func (s *Service) Install(ctx context.Context, workspaceID string, in ImportInput) (domain.Installed, error) {
	c, err := s.check(ctx, workspaceID, in)
	if err != nil {
		return domain.Installed{}, err
	}
	if c.found.Any() {
		return domain.Installed{}, conflictError(c.found, &domain.ConflictError{Conflicts: c.found})
	}
	installed, err := s.d.Installer.Install(ctx, workspaceID, c.prepared.Plan)
	if err != nil {
		return domain.Installed{}, storeError(fmt.Errorf("install plugin: %w", err))
	}
	s.log.InfoContext(ctx, "plugin installed",
		"plugin_id", installed.Plugin.ID, "plugin_name", installed.Plugin.Name,
		"agents", installed.Created.Agents, "skills", installed.Created.Skills,
		"eval_cases", installed.Created.EvalCases, "learnings", installed.Created.Learnings)
	return installed, nil
}
