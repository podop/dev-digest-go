package app

import (
	"context"
	"errors"
	"fmt"
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
	ListPage(ctx context.Context, repo domain.Repo, page, perPage int) ([]domain.Listed, error)
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
	// It returns false, and changes nothing, when the new full_name is the stored full_name of
	// another repository of the same workspace and host.
	UpdateRepoPath(ctx context.Context, workspaceID, repoID string, p domain.GitLabProject) (updated bool, err error)
}
