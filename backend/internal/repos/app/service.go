// Package app holds the use cases of the repos context: adding, listing, refreshing and
// deleting repositories, the workspace overview, and the clone / refresh background jobs.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/repos/domain"
)

// Job kinds enqueued by the service and handled by Jobs.Clone and Jobs.Refresh.
const (
	KindClone   = "clone"
	KindRefresh = "refresh"
)

// ClonePayload is the payload of a clone job.
type ClonePayload struct {
	RepoID string `json:"repoId"`
	Owner  string `json:"owner"`
	Name   string `json:"name"`
}

// RefreshPayload is the payload of a refresh job.
type RefreshPayload struct {
	RepoID string `json:"repoId"`
}

// NewRepo is the data of a repo to insert.
type NewRepo struct {
	WorkspaceID string
	CreatedBy   string
	// Ref is owner and name; for GitLab the namespace full path and the project path.
	Ref domain.Ref
	// Provider is codehost.GitHub or codehost.GitLab.
	Provider string
	// Host is the host key: codehost.GitHubHost, or the GitLab connection's host key.
	Host string
	// ProjectID and WebURL are set for GitLab only.
	ProjectID int64
	WebURL    string
	// DefaultBranch is the branch to start on; "" means domain.DefaultBranch.
	DefaultBranch string
}

// repoStore is what the service needs from persistence.
type repoStore interface {
	// Add inserts the repo unless it exists; created is false then and the existing repo is
	// returned. A GitHub repo is the same repo when (workspace, host, full name) matches. A
	// GitLab repo is the same repo when (workspace, host, project id) matches; failing that, a
	// repo with the same (workspace, host, full name) and another project id (a stale path) is
	// returned, created=false, and the caller tells the two apart by ProjectID. Concurrent
	// calls for the same key yield exactly one created=true.
	Add(ctx context.Context, in NewRepo) (repo domain.Repo, created bool, err error)
	// UpdatePath sets full_name, owner, name and web_url of the workspace's GitLab repo (spec
	// FR47). When p.FullName is the stored full name of another repo of the same workspace and
	// host it changes nothing and returns that repo's id as skippedFor; otherwise skippedFor is
	// "". domain.ErrNotFound if the repo does not exist in the workspace.
	UpdatePath(ctx context.Context, workspaceID, id string, p ProjectPath) (skippedFor string, err error)
	// List returns the workspace's repos by creation time ascending.
	List(ctx context.Context, workspaceID string) ([]domain.Repo, error)
	// Get returns domain.ErrNotFound for an unknown id or a repo of another workspace.
	Get(ctx context.Context, workspaceID, id string) (domain.Repo, error)
	// Delete removes the repo (the database cascades to its pull requests, files and
	// commits); domain.ErrNotFound if it does not exist in the workspace.
	Delete(ctx context.Context, workspaceID, id string) error
}

// enqueuer is the job queue port: it records the job and returns at once.
type enqueuer interface {
	Enqueue(ctx context.Context, workspaceID, kind string, payload any) (jobID string, err error)
}

// Service implements the repos use cases.
type Service struct {
	store    repoStore
	jobs     enqueuer
	cloneDir string
	conns    Connections
	projects GitLabProjects
	log      *slog.Logger
}

// ServiceOption tunes a Service.
type ServiceOption func(*Service)

// WithGitLab enables adding GitLab repos: conns binds a URL's host to a connection, projects
// reads the project. Without it every GitLab URL answers repo_host_not_connected.
func WithGitLab(conns Connections, projects GitLabProjects) ServiceOption {
	return func(s *Service) { s.conns, s.projects = conns, projects }
}

// WithLog sets the logger of warnings; the default discards them.
func WithLog(log *slog.Logger) ServiceOption {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

// NewService builds a Service. cloneDir is the absolute clone root reported by Overview.
func NewService(store repoStore, jobs enqueuer, cloneDir string, opts ...ServiceOption) *Service {
	s := &Service{store: store, jobs: jobs, cloneDir: cloneDir, log: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Add registers the repo for the workspace and enqueues its clone. When the repo already
// exists it is returned with created=false and nothing is enqueued. A GitHub URL makes no
// GitHub call; a GitLab URL is bound to its connection and its project is read once (see
// addGitLab). An enqueue failure is returned as an error and leaves the inserted row in place.
func (s *Service) Add(ctx context.Context, workspaceID, userID string, parsed domain.Parsed) (repo domain.Repo, created bool, err error) {
	switch parsed.Provider {
	case codehost.GitHub:
		ref := parsed.Ref()
		repo, created, err = s.store.Add(ctx, NewRepo{
			WorkspaceID: workspaceID, CreatedBy: userID, Ref: ref,
			Provider: codehost.GitHub, Host: codehost.GitHubHost,
		})
		if err != nil {
			return domain.Repo{}, false, fmt.Errorf("add repo %s: %w", ref.FullName(), err)
		}
	case codehost.GitLab:
		repo, created, err = s.addGitLab(ctx, workspaceID, userID, parsed)
		if err != nil {
			return domain.Repo{}, false, err
		}
	default:
		return domain.Repo{}, false, fmt.Errorf("add repo: provider %q: %w", parsed.Provider, domain.ErrInvalidURL)
	}
	if !created {
		return repo, false, nil
	}
	payload := ClonePayload{RepoID: repo.ID, Owner: repo.Owner, Name: repo.Name}
	if _, err := s.jobs.Enqueue(ctx, workspaceID, KindClone, payload); err != nil {
		return domain.Repo{}, false, fmt.Errorf("enqueue clone of %s: %w", repo.FullName, err)
	}
	return repo, true, nil
}

// List returns the workspace's repos, oldest first.
func (s *Service) List(ctx context.Context, workspaceID string) ([]domain.Repo, error) {
	repos, err := s.store.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	return repos, nil
}

// Refresh enqueues a clone job for a repo without a clone and a refresh job otherwise, and
// returns without waiting for it. An unknown id is a not-found error.
func (s *Service) Refresh(ctx context.Context, workspaceID, id string) error {
	repo, err := s.store.Get(ctx, workspaceID, id)
	if err != nil {
		return notFoundOr(err, "load repo "+id)
	}
	kind, payload := KindRefresh, any(RefreshPayload{RepoID: repo.ID})
	if !repo.Cloned() {
		kind, payload = KindClone, ClonePayload{RepoID: repo.ID, Owner: repo.Owner, Name: repo.Name}
	}
	if _, err := s.jobs.Enqueue(ctx, workspaceID, kind, payload); err != nil {
		return fmt.Errorf("enqueue %s of %s: %w", kind, repo.FullName, err)
	}
	return nil
}

// Delete removes the repo; the clone directory stays on disk. An unknown id is a not-found
// error.
func (s *Service) Delete(ctx context.Context, workspaceID, id string) error {
	if err := s.store.Delete(ctx, workspaceID, id); err != nil {
		return notFoundOr(err, "delete repo "+id)
	}
	return nil
}

// Overview is the workspace summary: where clones live and which repos are cloned.
type Overview struct {
	WorkspaceID string
	CloneDir    string
	Repos       []domain.Repo
}

// Overview returns the workspace summary.
func (s *Service) Overview(ctx context.Context, workspaceID string) (Overview, error) {
	repos, err := s.List(ctx, workspaceID)
	if err != nil {
		return Overview{}, err
	}
	return Overview{WorkspaceID: workspaceID, CloneDir: s.cloneDir, Repos: repos}, nil
}

// notFoundOr maps domain.ErrNotFound to the client-facing not-found error and wraps the rest.
func notFoundOr(err error, what string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("Repo not found").WithCause(err)
	}
	return fmt.Errorf("%s: %w", what, err)
}
