// Package app holds the pull request use cases: the PR list with GitHub sync, diff-stat backfill
// and rollups, the PR detail with its local mirror, review comments, and the manual poll.
// Reads are local-first: a GitHub failure never fails a read, stored data is served instead.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/pulls/domain"
)

// Backfill limits of the PR list (Node parity).
const (
	BackfillLimit       = 10 // PR detail fetches per list request
	BackfillConcurrency = 4  // parallel detail fetches
)

// Service runs the pull request use cases.
type Service struct {
	store store
	gh    gitHub
	log   *slog.Logger
	now   func() time.Time
}

// NewService builds a Service. now is the clock (nil means time.Now).
func NewService(store store, gh gitHub, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, gh: gh, log: log, now: now}
}

// ListItem is one row of the PR list.
type ListItem struct {
	Pull   domain.Pull
	Status string // derived by domain.DeriveStatus
	Rollup domain.Rollup
}

// ListForRepo syncs the repo's PR list from GitHub when a token is configured, backfills diff
// stats for a few stored PRs and returns every stored PR with its derived status and rollups.
// A missing token or a GitHub failure is logged and the stored rows are served.
func (s *Service) ListForRepo(ctx context.Context, workspaceID, repoID string) ([]ListItem, error) {
	repo, err := s.findRepo(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}

	listed, err := s.gh.ListPullRequests(ctx, repo.Owner, repo.Name)
	hasToken := !errors.Is(err, ErrNoToken)
	switch {
	case err != nil:
		s.log.WarnContext(ctx, "github pull request sync skipped; serving stored pull requests", "repo_id", repo.ID, "err", err)
	default:
		if err := s.store.UpsertListed(ctx, workspaceID, repo.ID, listed); err != nil {
			return nil, fmt.Errorf("sync pull requests of repo %s: %w", repo.ID, err)
		}
	}

	pulls, err := s.store.ListPulls(ctx, workspaceID, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("list pull requests of repo %s: %w", repo.ID, err)
	}
	if hasToken {
		s.backfillDiffStats(ctx, repo, pulls)
	}

	ids := make([]string, len(pulls))
	for i, p := range pulls {
		ids[i] = p.ID
	}
	rollups, err := s.store.Rollups(ctx, workspaceID, ids)
	if err != nil {
		return nil, fmt.Errorf("rollups of repo %s: %w", repo.ID, err)
	}
	now := s.now()
	items := make([]ListItem, len(pulls))
	for i, p := range pulls {
		items[i] = ListItem{
			Pull:   p,
			Status: domain.DeriveStatus(p.Status, p.LastReviewedSHA, p.HeadSHA, p.UpdatedAt, now),
			Rollup: rollups[p.ID],
		}
	}
	return items, nil
}

// backfillDiffStats fills the stats of at most BackfillLimit stored PRs that still have none,
// BackfillConcurrency at a time, updating pulls in place. Failures are logged and skipped.
func (s *Service) backfillDiffStats(ctx context.Context, repo domain.Repo, pulls []domain.Pull) {
	var pending []int
	for i, p := range pulls {
		if domain.NeedsDiffStats(domain.DiffStats{Additions: p.Additions, Deletions: p.Deletions, FilesCount: p.FilesCount}) {
			pending = append(pending, i)
			if len(pending) == BackfillLimit {
				break
			}
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, BackfillConcurrency)
	for _, i := range pending {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			p := &pulls[i] // each goroutine writes only its own element
			fetched, err := s.gh.GetPullRequest(ctx, repo.Owner, repo.Name, p.Number)
			if err != nil {
				s.log.WarnContext(ctx, "pull request diff-stat backfill skipped", "number", p.Number, "err", err)
				return
			}
			if err := s.store.SetDiffStats(ctx, p.ID, fetched.DiffStats); err != nil {
				s.log.WarnContext(ctx, "pull request diff-stat backfill not stored", "number", p.Number, "err", err)
				return
			}
			p.Additions, p.Deletions, p.FilesCount = fetched.Additions, fetched.Deletions, fetched.FilesCount
		})
	}
	wg.Wait()
}

// PollResult is the outcome of a manual poll.
type PollResult struct {
	Synced int // number of PRs GitHub returned
}

// Poll syncs the repo's PR list from GitHub and sets its last_polled_at. Unlike the list it
// reports GitHub problems: no token is a config error, any other failure is an external-service
// error.
func (s *Service) Poll(ctx context.Context, workspaceID, repoID string) (PollResult, error) {
	repo, err := s.findRepo(ctx, workspaceID, repoID)
	if err != nil {
		return PollResult{}, err
	}
	listed, err := s.gh.ListPullRequests(ctx, repo.Owner, repo.Name)
	if errors.Is(err, ErrNoToken) {
		return PollResult{}, apperr.Config("GITHUB_TOKEN is not configured").WithCause(err)
	}
	if err != nil {
		return PollResult{}, apperr.ExternalService("GitHub request failed").WithCause(err)
	}
	if err := s.store.UpsertListed(ctx, workspaceID, repo.ID, listed); err != nil {
		return PollResult{}, fmt.Errorf("sync pull requests of repo %s: %w", repo.ID, err)
	}
	if err := s.store.MarkPolled(ctx, workspaceID, repo.ID, s.now()); err != nil {
		if errors.Is(err, domain.ErrRepoNotFound) {
			return PollResult{}, apperr.NotFound("Repo not found")
		}
		return PollResult{}, fmt.Errorf("mark repo %s polled: %w", repo.ID, err)
	}
	return PollResult{Synced: len(listed)}, nil
}

// Detail returns a pull request with its files, commits and linked issue. It fetches the PR from
// GitHub and mirrors files, commits, body and stats into the store in one transaction; on any
// failure, or without a token, it serves the stored mirror.
func (s *Service) Detail(ctx context.Context, workspaceID, prID string) (domain.Detail, error) {
	pr, repo, err := s.resolvePull(ctx, workspaceID, prID)
	if err != nil {
		return domain.Detail{}, err
	}

	fresh, err := s.refreshDetail(ctx, pr, repo)
	if err == nil {
		return fresh, nil
	}
	s.log.WarnContext(ctx, "pull request detail refresh failed; serving stored detail", "pr_id", pr.ID, "err", err)

	files, err := s.store.ListFiles(ctx, pr.ID)
	if err != nil {
		return domain.Detail{}, fmt.Errorf("list files of pull request %s: %w", pr.ID, err)
	}
	commits, err := s.store.ListCommits(ctx, pr.ID)
	if err != nil {
		return domain.Detail{}, fmt.Errorf("list commits of pull request %s: %w", pr.ID, err)
	}
	return domain.Detail{Pull: pr, Files: files, Commits: commits}, nil
}

func (s *Service) refreshDetail(ctx context.Context, pr domain.Pull, repo domain.Repo) (domain.Detail, error) {
	fetched, err := s.gh.GetPullRequest(ctx, repo.Owner, repo.Name, pr.Number)
	if err != nil {
		return domain.Detail{}, err
	}
	files := domain.MergeFiles(fetched.Files)
	issue := s.linkedIssue(ctx, repo, fetched.Body)
	err = s.store.ReplaceDetail(ctx, pr.ID, domain.DetailMirror{
		Body: fetched.Body, DiffStats: fetched.DiffStats, Files: files, Commits: fetched.Commits,
	})
	if err != nil {
		return domain.Detail{}, fmt.Errorf("mirror pull request detail: %w", err)
	}
	return domain.Detail{
		Pull: domain.Pull{
			ID: pr.ID, RepoID: pr.RepoID, Number: fetched.Number, Title: fetched.Title,
			Author: fetched.Author, Branch: fetched.Branch, Base: fetched.Base, HeadSHA: fetched.HeadSHA,
			Additions: fetched.Additions, Deletions: fetched.Deletions, FilesCount: fetched.FilesCount,
			Status: fetched.Status, Body: fetched.Body, OpenedAt: fetched.OpenedAt, UpdatedAt: fetched.UpdatedAt,
		},
		Files:       files,
		Commits:     fetched.Commits,
		LinkedIssue: issue,
		FromGitHub:  true,
	}, nil
}

// linkedIssue resolves the issue named by the first "#<digits>" of the PR body. Any failure
// leaves it unresolved.
func (s *Service) linkedIssue(ctx context.Context, repo domain.Repo, body *string) *domain.Issue {
	if body == nil {
		return nil
	}
	n, ok := domain.LinkedIssueNumber(*body)
	if !ok {
		return nil
	}
	issue, err := s.gh.GetIssue(ctx, repo.Owner, repo.Name, n)
	if err != nil {
		s.log.WarnContext(ctx, "linked issue not resolved", "issue", n, "err", err)
		return nil
	}
	return &issue
}

// Comments proxies one page of the PR's GitHub review comments. Without a token or on any
// GitHub failure it returns no comments.
func (s *Service) Comments(ctx context.Context, workspaceID, prID string) ([]domain.ReviewComment, error) {
	pr, repo, err := s.resolvePull(ctx, workspaceID, prID)
	if err != nil {
		return nil, err
	}
	comments, err := s.gh.ListReviewComments(ctx, repo.Owner, repo.Name, pr.Number)
	if err != nil {
		s.log.WarnContext(ctx, "review comments not fetched; serving none", "pr_id", pr.ID, "err", err)
		return []domain.ReviewComment{}, nil
	}
	return comments, nil
}

// CommentRequest is a comment to post. With InReplyTo set it replies to that comment and the
// other fields are not sent to GitHub.
type CommentRequest struct {
	Path      string
	Line      int
	Side      string // LEFT | RIGHT; empty means RIGHT
	Body      string
	InReplyTo *int64
}

// PostComment posts an inline review comment, pinned to the stored head sha. It is never
// retried. Without a token the error is github_unavailable; any GitHub failure is
// github_comment_failed with GitHub's message.
func (s *Service) PostComment(ctx context.Context, workspaceID, prID string, req CommentRequest) (domain.ReviewComment, error) {
	pr, repo, err := s.resolvePull(ctx, workspaceID, prID)
	if err != nil {
		return domain.ReviewComment{}, err
	}
	created, err := s.gh.CreateReviewComment(ctx, repo.Owner, repo.Name, pr.Number, domain.CommentInput{
		CommitID: pr.HeadSHA, Path: req.Path, Line: req.Line, Side: req.Side, Body: req.Body, InReplyTo: req.InReplyTo,
	})
	if errors.Is(err, ErrNoToken) {
		return domain.ReviewComment{}, apperr.InvalidInput("Connect a GitHub token to post comments.").
			WithCode("github_unavailable").WithCause(err)
	}
	if err != nil {
		msg := "Failed to post the comment to GitHub."
		var ge *GitHubError
		if errors.As(err, &ge) && ge.Message != "" {
			msg = ge.Message
		}
		return domain.ReviewComment{}, apperr.InvalidInput(msg).
			WithCode("github_comment_failed").
			WithDetails(map[string]string{"cause": err.Error()}).
			WithCause(err)
	}
	return created, nil
}

func (s *Service) findRepo(ctx context.Context, workspaceID, repoID string) (domain.Repo, error) {
	repo, err := s.store.FindRepo(ctx, workspaceID, repoID)
	if errors.Is(err, domain.ErrRepoNotFound) {
		return domain.Repo{}, apperr.NotFound("Repo not found")
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("find repo %s: %w", repoID, err)
	}
	return repo, nil
}

func (s *Service) resolvePull(ctx context.Context, workspaceID, prID string) (domain.Pull, domain.Repo, error) {
	pr, err := s.store.FindPull(ctx, workspaceID, prID)
	if errors.Is(err, domain.ErrPullNotFound) {
		return domain.Pull{}, domain.Repo{}, apperr.NotFound("Pull request not found")
	}
	if err != nil {
		return domain.Pull{}, domain.Repo{}, fmt.Errorf("find pull request %s: %w", prID, err)
	}
	repo, err := s.findRepo(ctx, workspaceID, pr.RepoID)
	if err != nil {
		return domain.Pull{}, domain.Repo{}, err
	}
	return pr, repo, nil
}
