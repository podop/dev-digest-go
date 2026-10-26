package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/pulls/domain"
)

// ErrNotConnected is returned by the GitLab port when no GitLab connection has the repository's
// host key (spec FR48). The port sends no request then. The caller knows the host and the
// repository and builds the 422 repo_host_not_connected details from them.
var ErrNotConnected = errors.New("no gitlab connection for the host")

// NoTokenError is returned by the GitLab port when the repository's connection has no token.
// Conn is the connection's display name (message "GitLab connection <name> has no token").
type NoTokenError struct {
	Conn string
}

func (e *NoTokenError) Error() string {
	return fmt.Sprintf("gitlab connection %s has no token", e.Conn)
}

// GitLabError is a non-2xx answer of a GitLab instance. Message is already scrubbed of tokens.
type GitLabError struct {
	Status  int
	Message string
	// RateLimited is true for a 429.
	RateLimited bool
	// RetryAfter is the time GitLab's Retry-After header says to wait until; zero when absent.
	RetryAfter time.Time
	// RateLimitReset is when GitLab's RateLimit-Reset header says the window renews; zero when absent.
	RateLimitReset time.Time
}

func (e *GitLabError) Error() string {
	return fmt.Sprintf("gitlab: HTTP %d: %s", e.Status, e.Message)
}

// RateLimit reports whether the error is a rate limit and when it ends: Retry-After when sent,
// else RateLimit-Reset, else the zero time (the caller then waits for the next due time, A4).
func (e *GitLabError) RateLimit() (reset time.Time, limited bool) {
	if !e.RateLimited && e.Status != statusTooManyRequests {
		return time.Time{}, false
	}
	if !e.RetryAfter.IsZero() {
		return e.RetryAfter, true
	}
	return e.RateLimitReset, true
}

// GitLab is the GitLab access the use cases and the poller need. The adapter resolves the
// connection by repo.Host on every call, addresses the project by repo.ProjectID (FR17) and fails
// with ErrNotConnected, *NoTokenError or *GitLabError. Reads retry transient failures inside the
// adapter, except in the poller's instance, which must not retry (a retry would break the per-repo
// request bound).
type GitLab interface {
	// Project reads the project's current identity (GET /projects/<id>); the first request of
	// every list sync (FR47).
	Project(ctx context.Context, repo domain.Repo) (domain.GitLabProject, error)
	// ListPage reads one page (1-based) of the project's merge requests, newest update first,
	// already mapped (domain.MapMergeRequests); merge requests without a head sha are dropped.
	// more is true when the answer held a full page of perPage merge requests before that drop, so
	// a next page may exist: a caller that pages must stop on !more, never on len(listed).
	ListPage(ctx context.Context, repo domain.Repo, page, perPage int) (listed []domain.Listed, more bool, err error)
	// Get reads one merge request with its files (up to 3,000) and commits (up to 250).
	Get(ctx context.Context, repo domain.Repo, number int) (domain.Fetched, error)
	// Issue reads the linked issue; the caller omits it on any error.
	Issue(ctx context.Context, repo domain.Repo, number int) (domain.Issue, error)
	// Stats reads the diff stats of up to domain.GitLabStatsBatch merge requests (by iid) with one
	// query. A merge request without a summary, or with an all-zero one, is absent from the map.
	Stats(ctx context.Context, repo domain.Repo, numbers []int) (map[int]domain.DiffStats, error)
}

// RepoPathUpdater stores a GitLab project's current path (spec FR47).
type RepoPathUpdater interface {
	// UpdateRepoPath sets full_name, owner, name and web URL of the workspace's repository from p.
	// When the new full_name is the stored full_name of another repository of the same workspace
	// and host, nothing changes and that repository's id is returned ("" = stored). It logs
	// nothing: followProjectPath reports the collision.
	UpdateRepoPath(ctx context.Context, workspaceID, repoID string, p domain.GitLabProject) (skippedFor string, err error)
}

// followProjectPath stores the project's current path when it differs from the stored one (spec
// FR47, rename-follow). The stored path is kept, and the sync goes on by project id, when
//   - the fetched path is not a plain project path that agrees with its own parts (the add rule,
//     domain.GitLabProject.CheckPath): one WARN with the repository id and the project id, no path;
//   - another repository of the workspace and host holds the new path: one WARN with both
//     repository ids;
//   - the store fails: one WARN with the error.
//
// The next sync tries again. Only a ended ctx is returned.
func followProjectPath(ctx context.Context, log *slog.Logger, store RepoPathUpdater, workspaceID string, repo domain.Repo, p domain.GitLabProject) error {
	if p.FullName == "" || p.FullName == repo.Owner+"/"+repo.Name || (p.Owner == repo.Owner && p.Name == repo.Name) {
		return nil
	}
	if err := p.CheckPath(); err != nil {
		log.WarnContext(ctx, "gitlab project path refused; keeping the stored path", "repo_id", repo.ID, "project_id", p.ID)
		return nil
	}
	heldBy, err := store.UpdateRepoPath(ctx, workspaceID, repo.ID, p)
	switch {
	case err != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.WarnContext(ctx, "gitlab project path not stored; keeping the stored path", "repo_id", repo.ID, "err", err)
	case heldBy != "":
		log.WarnContext(ctx, "gitlab project path is taken in the workspace; keeping the stored path",
			"repo_id", repo.ID, "other_repo_id", heldBy)
	}
	return nil
}
