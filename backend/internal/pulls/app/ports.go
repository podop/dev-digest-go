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
	// UpsertListed inserts new PRs and refreshes title, head_sha, status and updated_at of
	// existing ones; it never deletes.
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
