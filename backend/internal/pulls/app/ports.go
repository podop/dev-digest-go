package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/pulls/domain"
)

// ErrNoToken is returned by the GitHub port when no GitHub token is configured.
var ErrNoToken = errors.New("github token is not configured")

// GitHubError is a non-2xx answer of GitHub. Message is GitHub's own error message.
type GitHubError struct {
	Status  int
	Message string
	// RateLimited is true when GitHub said a rate limit was hit: the primary limit (remaining 0),
	// or a 403/429 that carries Retry-After or says "secondary rate limit".
	RateLimited bool
	// RateLimitReset is when GitHub says the primary limit renews; the zero time when it said nothing valid.
	RateLimitReset time.Time
	// RetryAfter is the time GitHub's Retry-After header says to wait until (the secondary limit);
	// the zero time when the answer had none.
	RetryAfter time.Time
}

func (e *GitHubError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Message)
}

// store is the persistence the use cases need. Every method that takes a workspace id only
// sees that workspace's rows.
type store interface {
	// FindRepo returns domain.ErrRepoNotFound for an unknown repo.
	FindRepo(ctx context.Context, workspaceID, repoID string) (domain.Repo, error)
	MarkPolled(ctx context.Context, workspaceID, repoID string, at time.Time) error
	// FindPull returns domain.ErrPullNotFound for an unknown pull request.
	FindPull(ctx context.Context, workspaceID, prID string) (domain.Pull, error)
	// ListPulls returns the repo's PRs by updated_at descending (nulls last), then number descending.
	ListPulls(ctx context.Context, workspaceID, repoID string) ([]domain.Pull, error)
	// UpsertListed inserts new PRs and refreshes title, head_sha, status, updated_at, merged_at,
	// closed_at and draft of existing ones; it never deletes.
	UpsertListed(ctx context.Context, workspaceID, repoID string, listed []domain.Listed) error
	SetDiffStats(ctx context.Context, prID string, stats domain.DiffStats) error
	// ReplaceDetail writes body, stats, files and commits in one transaction.
	ReplaceDetail(ctx context.Context, prID string, mirror domain.DetailMirror) error
	ListFiles(ctx context.Context, prID string) ([]domain.File, error)
	ListCommits(ctx context.Context, prID string) ([]domain.Commit, error)
	// Rollups returns an entry for every requested PR id.
	Rollups(ctx context.Context, workspaceID string, prIDs []string) (map[string]domain.Rollup, error)
}

// gitHub is the GitHub access the use cases need. Every method fails with ErrNoToken when no
// token is configured and with a *GitHubError for a non-2xx answer. Reads retry transient
// failures inside the adapter; PostComment never retries.
type gitHub interface {
	ListPullRequests(ctx context.Context, owner, name string) ([]domain.Listed, error)
	GetPullRequest(ctx context.Context, owner, name string, number int) (domain.Fetched, error)
	GetIssue(ctx context.Context, owner, name string, number int) (domain.Issue, error)
	ListReviewComments(ctx context.Context, owner, name string, number int) ([]domain.ReviewComment, error)
	CreateReviewComment(ctx context.Context, owner, name string, number int, in domain.CommentInput) (domain.ReviewComment, error)
}

// PollTargets lists the repositories the background poll refreshes: every repository of every
// workspace, grouped by workspace.
type PollTargets interface {
	ListPollTargets(ctx context.Context) ([]domain.PollTarget, error)
}

// IntervalSource gives the background-poll interval of a workspace (its Settings
// polling_interval_min). The poller reads it before every cycle, falls back to
// domain.DefaultPollInterval on an error or a non-positive value and clamps it to 1 min .. 24 h.
type IntervalSource interface {
	Interval(ctx context.Context, workspaceID string) (time.Duration, error)
}

// PageLister reads one page (1-based) of a repository's PRs, newest update first. It fails with
// ErrNoToken when no token is configured and with a *GitHubError for a non-2xx answer. The poller's
// instance must not retry (a retry would break the 10 requests per repo bound).
type PageLister interface {
	ListPullRequestsPage(ctx context.Context, owner, name string, page, perPage int) ([]domain.Listed, error)
}

// StatsFetcher reads the diff stats of one PR with a single GitHub request (the same fields the
// PR detail fetch reads). It fails with ErrNoToken when no token is configured and with a
// *GitHubError for a non-2xx answer. Like PageLister, the poller's instance must not retry.
type StatsFetcher interface {
	GetPullStats(ctx context.Context, owner, name string, number int) (domain.DiffStats, error)
}

// PollStore is the persistence of the background poll.
type PollStore interface {
	UpsertListed(ctx context.Context, workspaceID, repoID string, listed []domain.Listed) error
	MarkPolled(ctx context.Context, workspaceID, repoID string, at time.Time) error
	// ListWithoutDiffStats returns at most limit PRs of the repo whose stats are still missing
	// (domain.NeedsDiffStats) and that were updated at or after updatedSince, most recently updated
	// first. Only ID and Number of the returned pulls are set.
	ListWithoutDiffStats(ctx context.Context, workspaceID, repoID string, updatedSince time.Time, limit int) ([]domain.Pull, error)
	SetDiffStats(ctx context.Context, prID string, stats domain.DiffStats) error
}

// CycleObserver is told after a workspace's background-poll cycle that polled successfully: at
// least one repo had its PR pages read and was marked polled (a cycle cut short by a rate limit
// after that still counts; a missing token or a cancelled ctx does not). It runs in the poller's
// goroutine, so it must return promptly once ctx is cancelled. The poller recovers its panics.
type CycleObserver interface {
	AfterCycle(ctx context.Context, workspaceID string)
}
