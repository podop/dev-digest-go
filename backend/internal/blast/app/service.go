package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"dev-digest-go/backend/internal/blast/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Options are the optional dependencies of Service.
type Options struct {
	// Log receives the content-free lines of spec section 7.15; nil discards them.
	Log *slog.Logger
	// Redact scrubs secrets from an error text before it is logged; nil logs the text as is.
	Redact func(string) string
}

// Service implements API and Provider. It writes nothing and makes no model call.
type Service struct {
	store  Store
	intel  RepoIntel
	gh     GitHub
	log    *slog.Logger
	redact func(string) string
}

var (
	_ API      = (*Service)(nil)
	_ Provider = (*Service)(nil)
)

// NewService builds a Service. A nil store, intel or gh is a programming error and panics at
// startup.
func NewService(store Store, intel RepoIntel, gh GitHub, opts Options) *Service {
	if store == nil || intel == nil || gh == nil {
		panic("blast/app: NewService needs a store, a repo-intel reader and a GitHub reader")
	}
	s := &Service{store: store, intel: intel, gh: gh, log: opts.Log, redact: opts.Redact}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.redact == nil {
		s.redact = func(text string) string { return text }
	}
	return s
}

// Radius builds the blast radius of a pull request of the workspace.
func (s *Service) Radius(ctx context.Context, workspaceID, prID string) (domain.BlastRadius, error) {
	pull, err := s.store.FindPull(ctx, workspaceID, prID)
	if err != nil {
		return domain.BlastRadius{}, notFoundOr(err, "load pull request")
	}
	paths, err := s.store.ChangedPaths(ctx, prID)
	if err != nil {
		return domain.BlastRadius{}, fmt.Errorf("load changed paths: %w", err)
	}
	return s.RadiusFor(ctx, pull.RepoID, paths)
}

// RadiusFor builds the blast radius of a repository's changed paths from one port read and logs
// it. The read happens for an empty path list too: the reader answers it degraded.
func (s *Service) RadiusFor(ctx context.Context, repoID string, changed []string) (domain.BlastRadius, error) {
	res, err := s.intel.BlastRadius(ctx, repoID, changed)
	if err != nil {
		return domain.BlastRadius{}, fmt.Errorf("read blast radius: %w", err)
	}
	blast := domain.BuildRadius(res)
	attrs := []any{"repoId", repoID, "changedFiles", len(changed), "degraded", blast.Degraded}
	if blast.Reason != "" {
		attrs = append(attrs, "reason", string(blast.Reason))
	}
	attrs = append(attrs, "callers", len(res.Callers))
	s.log.InfoContext(ctx, "blast radius read from index", attrs...)
	return blast, nil
}

// History returns the prior merged pull requests that touched the same files. Every GitHub
// failure is a warning and an empty list. A GitLab pull request has no history here (FR29): the
// list is empty and no request goes to GitHub.
func (s *Service) History(ctx context.Context, workspaceID, prID string) ([]domain.PriorPR, error) {
	pull, err := s.store.FindPull(ctx, workspaceID, prID)
	if err != nil {
		return nil, notFoundOr(err, "load pull request")
	}
	if pull.IsGitLab() {
		return []domain.PriorPR{}, nil
	}
	paths, err := s.store.ChangedPaths(ctx, prID)
	if err != nil {
		return nil, fmt.Errorf("load changed paths: %w", err)
	}
	if len(paths) == 0 {
		return []domain.PriorPR{}, nil
	}
	merged, err := s.gh.ListMergedPulls(ctx, pull.Owner, pull.Name, domain.HistoryListLimit)
	if err != nil {
		s.log.WarnContext(ctx, "prior PR history unavailable; returning an empty list",
			"err", s.redact(err.Error()), "repoId", pull.RepoID)
		return []domain.PriorPR{}, nil
	}
	return domain.SelectPriorPRs(merged, pull.Number, paths), nil
}

// notFoundOr turns domain.ErrNotFound into the 404 of the routes; any other failure is wrapped
// with what failed and left to httpx (500).
func notFoundOr(err error, what string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("Pull request not found").WithCause(err)
	}
	return fmt.Errorf("%s: %w", what, err)
}
