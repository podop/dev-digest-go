package main

import (
	"context"
	"encoding/json"
	"fmt"

	reposapp "dev-digest-go/backend/internal/repos/app"
	settingsapp "dev-digest-go/backend/internal/settings/app"
	workspaceapp "dev-digest-go/backend/internal/workspace/app"
	workspacedomain "dev-digest-go/backend/internal/workspace/domain"
)

// settingsRW reads and writes the settings of a workspace (*settingsapp.Service). The workspace
// context never imports settings: only this composition root knows both.
type settingsRW interface {
	settingsBag
	Update(ctx context.Context, workspaceID, userID string, entries settingsapp.Bag) (settingsapp.Bag, error)
}

// workspaceNames adapts the settings use case to the workspace NameStore port: the display name is
// the Settings row `workspace_name` (spec D23). The lookup key `workspaces.name` is never touched.
type workspaceNames struct{ settings settingsRW }

// WorkspaceName returns the stored text. A row that is not a JSON string (null, a number) is
// found with an empty name, which the use case treats as invalid and answers as the default.
func (n workspaceNames) WorkspaceName(ctx context.Context, workspaceID string) (string, bool, error) {
	bag, err := n.settings.Get(ctx, workspaceID)
	if err != nil {
		return "", false, fmt.Errorf("read settings: %w", err)
	}
	raw, ok := bag[workspacedomain.NameKey]
	if !ok {
		return "", false, nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", true, nil
	}
	return name, true, nil
}

// SetWorkspaceName upserts the row for the user the request acts as.
func (n workspaceNames) SetWorkspaceName(ctx context.Context, workspaceID, userID, name string) error {
	raw, err := json.Marshal(name)
	if err != nil {
		return fmt.Errorf("encode workspace name: %w", err)
	}
	if _, err := n.settings.Update(ctx, workspaceID, userID, settingsapp.Bag{workspacedomain.NameKey: raw}); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

// reposOverview adapts the repos use case to the workspace RepoOverview port.
type reposOverview struct{ repos *reposapp.Service }

// Overview returns the clone root and the workspace's repositories.
func (r reposOverview) Overview(ctx context.Context, workspaceID string) (workspaceapp.ReposSnapshot, error) {
	ov, err := r.repos.Overview(ctx, workspaceID)
	if err != nil {
		return workspaceapp.ReposSnapshot{}, err
	}
	snap := workspaceapp.ReposSnapshot{CloneDir: ov.CloneDir, Repos: make([]workspaceapp.RepoInfo, len(ov.Repos))}
	for i, repo := range ov.Repos {
		snap.Repos[i] = workspaceapp.RepoInfo{
			ID: repo.ID, FullName: repo.FullName, ClonePath: repo.ClonePath, LastPolledAt: repo.LastPolledAt,
		}
	}
	return snap, nil
}

// workspaceSettings adapts the settings use case to the SettingsSource and RetentionSource ports.
type workspaceSettings struct{ settings settingsBag }

// Settings returns every stored setting as raw JSON.
func (s workspaceSettings) Settings(ctx context.Context, workspaceID string) (map[string]json.RawMessage, error) {
	bag, err := s.settings.Get(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	return bag, nil
}

// Retention decodes the stored retention_days: absent is the default, null keeps forever, a value
// outside the choices wraps workspacedomain.ErrInvalidRetention.
func (s workspaceSettings) Retention(ctx context.Context, workspaceID string) (workspacedomain.Retention, error) {
	bag, err := s.settings.Get(ctx, workspaceID)
	if err != nil {
		return workspacedomain.Retention{}, fmt.Errorf("read settings: %w", err)
	}
	raw, ok := bag[workspacedomain.RetentionKey]
	return workspacedomain.DecodeRetention(raw, ok)
}
