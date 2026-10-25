package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/workspace/domain"
)

// Wire codes of the reset refusals; the UI constants WORKSPACE_RESET_MISMATCH_CODE and
// WORKSPACE_RESET_ACTIVE_CODE carry the same text.
const (
	CodeConfirmationMismatch = "confirmation_mismatch"
	CodeRunActive            = "run_active"
)

// validationMessage is the message of every 422 `validation_error` (httpx.ValidationMessage; the
// app layer does not import the HTTP package).
const validationMessage = "Request validation failed"

// nameProblem is the issue text of a rejected workspace name.
const nameProblem = "Workspace name must be 1 to 80 characters without control characters"

// Clock returns the current time. The service reads it for the export instant.
type Clock func() time.Time

// Info is the workspace as GET /workspace answers it.
type Info struct {
	WorkspaceID string
	Name        string
	DataDir     string
	CloneDir    string
	Repos       []RepoInfo
}

// ExportFile is the export answer: the suggested download file name and the bundle.
type ExportFile struct {
	FileName string
	Bundle   domain.Bundle
}

// Config holds the dependencies of Service. DataDir is the absolute data directory shown by
// Info; Version is the server build version written to the manifest ("dev" when empty).
type Config struct {
	Names    NameStore
	Repos    RepoOverview
	Settings SettingsSource
	Reader   Reader
	History  History
	Clock    Clock
	Log      *slog.Logger
	DataDir  string
	Version  string
}

// Service implements the workspace use cases: info, rename, stats, export and the reset of the
// run history.
type Service struct {
	cfg Config
}

// NewService builds the service from its ports.
func NewService(cfg Config) *Service { return &Service{cfg: cfg} }

// Info returns the workspace: its display name (domain.DefaultName when none or an invalid one
// is stored), the data directory, the clone root and the repositories.
func (s *Service) Info(ctx context.Context, workspaceID string) (Info, error) {
	name, err := s.name(ctx, workspaceID)
	if err != nil {
		return Info{}, err
	}
	snap, err := s.cfg.Repos.Overview(ctx, workspaceID)
	if err != nil {
		return Info{}, fmt.Errorf("read workspace repos: %w", err)
	}
	return Info{
		WorkspaceID: workspaceID,
		Name:        name,
		DataDir:     s.cfg.DataDir,
		CloneDir:    snap.CloneDir,
		Repos:       snap.Repos,
	}, nil
}

// Rename stores the trimmed name and answers the new Info. A name that fails
// domain.NormalizeName is a 422 validation error whose cause is domain.ErrInvalidName. Only the
// display name changes: the lookup key `workspaces.name` is not touched (FR2).
func (s *Service) Rename(ctx context.Context, workspaceID, userID, name string) (Info, error) {
	normalized, err := domain.NormalizeName(name)
	if err != nil {
		return Info{}, nameError(err)
	}
	if err := s.cfg.Names.SetWorkspaceName(ctx, workspaceID, userID, normalized); err != nil {
		return Info{}, fmt.Errorf("store workspace name: %w", err)
	}
	return s.Info(ctx, workspaceID)
}

// Stats returns the counts of what the workspace holds.
func (s *Service) Stats(ctx context.Context, workspaceID string) (domain.Stats, error) {
	st, err := s.cfg.Reader.Stats(ctx, workspaceID)
	if err != nil {
		return domain.Stats{}, fmt.Errorf("read workspace stats: %w", err)
	}
	return st, nil
}

// Export builds the workspace bundle and its file name. The raw settings go to the bundle
// builder, which applies the allow-list (FR14) and the automatic_reviews rule (D26).
func (s *Service) Export(ctx context.Context, workspaceID string) (ExportFile, error) {
	name, err := s.name(ctx, workspaceID)
	if err != nil {
		return ExportFile{}, err
	}
	data, err := s.cfg.Reader.ExportData(ctx, workspaceID)
	if err != nil {
		return ExportFile{}, fmt.Errorf("read export data: %w", err)
	}
	settings, err := s.cfg.Settings.Settings(ctx, workspaceID)
	if err != nil {
		return ExportFile{}, fmt.Errorf("read export settings: %w", err)
	}
	data.Settings = settings
	now := s.cfg.Clock().UTC()
	return ExportFile{
		FileName: domain.ExportFileName(name, now),
		Bundle:   domain.BuildBundle(data, domain.Meta{Name: name, Version: s.cfg.Version, Now: now}),
	}, nil
}

// Reset deletes the workspace's run history after the caller typed the current name exactly
// (case-sensitive, untrimmed). A different text is a 422 confirmation_mismatch; running agent or
// eval suite runs are a 409 run_active naming the count. Both leave all data untouched. A
// success writes one INFO line with the counts only (NFR7).
func (s *Service) Reset(ctx context.Context, workspaceID, confirm string) (domain.Deleted, error) {
	name, err := s.name(ctx, workspaceID)
	if err != nil {
		return domain.Deleted{}, err
	}
	if confirm != name {
		return domain.Deleted{}, apperr.Validation("Type the workspace name exactly to confirm").
			WithCode(CodeConfirmationMismatch)
	}
	deleted, active, err := s.cfg.History.Reset(ctx, workspaceID)
	if err != nil {
		return domain.Deleted{}, fmt.Errorf("reset run history: %w", err)
	}
	if active > 0 {
		return domain.Deleted{}, apperr.Conflict(
			fmt.Sprintf("%d runs are in progress — wait for them to finish or cancel them", active)).
			WithCode(CodeRunActive)
	}
	s.cfg.Log.InfoContext(ctx, "workspace run history reset",
		"agent_runs", deleted.AgentRuns,
		"run_traces", deleted.RunTraces,
		"reviews", deleted.Reviews,
		"findings", deleted.Findings,
		"multi_agent_runs", deleted.MultiAgentRuns,
		"eval_suite_runs", deleted.EvalSuiteRuns,
		"eval_runs", deleted.EvalRuns,
		"ci_runs", deleted.CIRuns,
		"composed_reviews", deleted.ComposedReviews,
		"learnings", deleted.Learnings,
		"prs_unmarked", deleted.PRsUnmarked,
	)
	return deleted, nil
}

// name is the effective display name: the stored one when it is valid, else domain.DefaultName.
func (s *Service) name(ctx context.Context, workspaceID string) (string, error) {
	stored, found, err := s.cfg.Names.WorkspaceName(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("read workspace name: %w", err)
	}
	if !found {
		return domain.DefaultName, nil
	}
	name, err := domain.NormalizeName(stored)
	if err != nil {
		return domain.DefaultName, nil
	}
	return name, nil
}

// nameError is the 422 for a rejected name, in the shape of the other request validation
// errors: one issue at path ["name"].
func nameError(cause error) *apperr.Error {
	issue := map[string]any{"code": "custom", "path": []any{"name"}, "message": nameProblem}
	detail := map[string]any{
		"keyword":      "custom",
		"instancePath": "/name",
		"schemaPath":   "#/name/custom",
		"message":      nameProblem,
		"params":       map[string]any{"issue": issue},
	}
	return apperr.Validation(validationMessage).WithDetails([]any{detail}).WithCause(cause)
}
