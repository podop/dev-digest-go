// Package app holds the pull request use cases: the PR list with GitHub sync, diff-stat backfill
// and rollups, the PR detail with its local mirror, review comments, and the manual poll.
// Reads are local-first: a GitHub failure never fails a read, stored data is served instead.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	gl    GitLab // nil: every GitLab repository reads as not connected
	log   *slog.Logger
	now   func() time.Time
}

// NewService builds a Service. now is the clock (nil means time.Now). A repository is routed to
// GitHub or, with WithGitLab, to GitLab by the provider read with its row (FR29): a GitLab
// repository never reaches gh and a GitHub repository never reaches the GitLab port.
func NewService(store store, gh gitHub, log *slog.Logger, now func() time.Time, opts ...Option) *Service {
	if now == nil {
		now = time.Now
	}
	s := &Service{store: store, gh: gh, log: log, now: now}
	for _, o := range opts {
		o(s)
	}
	return s
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

	var (
		listed   []domain.Listed
		hasToken bool
	)
	msg := "github pull request sync skipped; serving stored pull requests"
	if repo.IsGitLab() {
		listed, err = s.syncListGitLab(ctx, workspaceID, repo)
		hasToken = gitLabReadable(err)
		msg = "gitlab merge request sync skipped; serving stored merge requests"
	} else {
		listed, err = s.gh.ListPullRequests(ctx, repo.Owner, repo.Name)
		hasToken = !errors.Is(err, ErrNoToken)
	}
	switch {
	case err != nil:
		s.log.WarnContext(ctx, msg, "repo_id", repo.ID, "err", err)
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
	if repo.IsGitLab() {
		s.backfillDiffStatsGitLab(ctx, repo, pulls)
		return
	}
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
	var listed []domain.Listed
	if repo.IsGitLab() {
		listed, err = s.syncListGitLab(ctx, workspaceID, repo)
		if err != nil {
			return PollResult{}, gitLabPollError(repo, err)
		}
	} else {
		listed, err = s.gh.ListPullRequests(ctx, repo.Owner, repo.Name)
		if errors.Is(err, ErrNoToken) {
			return PollResult{}, apperr.Config("GITHUB_TOKEN is not configured").WithCause(err)
		}
		if err != nil {
			return PollResult{}, apperr.ExternalService("GitHub request failed").WithCause(err)
		}
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
	var (
		fetched domain.Fetched
		err     error
	)
	if repo.IsGitLab() {
		fetched, err = s.fetchGitLabDetail(ctx, repo, pr.Number)
	} else {
		fetched, err = s.gh.GetPullRequest(ctx, repo.Owner, repo.Name, pr.Number)
	}
	if err != nil {
		return domain.Detail{}, err
	}
	files := domain.MergeFiles(fetched.Files)
	if repo.IsGitLab() {
		fetched.DiffStats = s.gitLabDetailStats(ctx, repo, fetched, files)
	}
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
	var (
		issue domain.Issue
		err   error
	)
	if repo.IsGitLab() {
		issue, err = s.gitLab().Issue(ctx, repo, n)
	} else {
		issue, err = s.gh.GetIssue(ctx, repo.Owner, repo.Name, n)
	}
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
	if repo.IsGitLab() {
		return []domain.ReviewComment{}, nil // no GitLab review comments yet and no GitHub request (FR29)
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
	if repo.IsGitLab() {
		return domain.ReviewComment{}, apperr.InvalidInput("Posting to GitLab merge requests is not available yet.").
			WithCode("gitlab_unavailable")
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

// gitLab returns the GitLab port; without one every call fails with ErrNotConnected, so no
// request is sent.
func (s *Service) gitLab() GitLab {
	if s.gl == nil {
		return disconnectedGitLab{}
	}
	return s.gl
}

// disconnectedGitLab is the GitLab port of a Service built without WithGitLab.
type disconnectedGitLab struct{}

func (disconnectedGitLab) Project(context.Context, domain.Repo) (domain.GitLabProject, error) {
	return domain.GitLabProject{}, ErrNotConnected
}

func (disconnectedGitLab) ListPage(context.Context, domain.Repo, int, int) ([]domain.Listed, bool, error) {
	return nil, false, ErrNotConnected
}

func (disconnectedGitLab) Get(context.Context, domain.Repo, int) (domain.Fetched, error) {
	return domain.Fetched{}, ErrNotConnected
}

func (disconnectedGitLab) Issue(context.Context, domain.Repo, int) (domain.Issue, error) {
	return domain.Issue{}, ErrNotConnected
}

func (disconnectedGitLab) Stats(context.Context, domain.Repo, []int) (map[int]domain.DiffStats, error) {
	return nil, ErrNotConnected
}

// gitLabReadable reports whether more GitLab requests for the repository make sense after err: not
// when the host has no connection, the connection has no token, or the instance rate-limits.
func gitLabReadable(err error) bool {
	if errors.Is(err, ErrNotConnected) {
		return false
	}
	var nt *NoTokenError
	if errors.As(err, &nt) {
		return false
	}
	var ge *GitLabError
	if errors.As(err, &ge) {
		if _, limited := ge.RateLimit(); limited {
			return false
		}
	}
	return true
}

// syncListGitLab reads the project (FR47) and then one page of its merge requests (FR21). A failed
// project fetch is a failed sync: no list request follows.
func (s *Service) syncListGitLab(ctx context.Context, workspaceID string, repo domain.Repo) ([]domain.Listed, error) {
	gl := s.gitLab()
	project, err := gl.Project(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("project of repo %s: %w", repo.ID, err)
	}
	s.updateRepoPath(ctx, workspaceID, repo, project)
	// A manual sync reads the first page only (FR21): `more` has no use here.
	listed, _, err := gl.ListPage(ctx, repo, 1, GitLabPageSize)
	if err != nil {
		return nil, fmt.Errorf("merge requests of repo %s: %w", repo.ID, err)
	}
	return listed, nil
}

// updateRepoPath follows a rename (FR47, followProjectPath): a refused or colliding path, or a
// store failure, keeps the stored path and logs one WARN; the sync goes on by project id and the
// next fetch tries again. An ended ctx fails the list request that follows.
func (s *Service) updateRepoPath(ctx context.Context, workspaceID string, repo domain.Repo, p domain.GitLabProject) {
	_ = followProjectPath(ctx, s.log, s.store, workspaceID, repo, p)
}

// gitLabPollError maps a failed GitLab list sync of a manual poll (spec §7): a host without a
// connection is 422 repo_host_not_connected, a connection without a token 500 config_error, any
// other failure (rate limit included) 502 external_service.
func gitLabPollError(repo domain.Repo, err error) error {
	if errors.Is(err, ErrNotConnected) {
		return NotConnectedError(repo).WithCause(err)
	}
	var nt *NoTokenError
	if errors.As(err, &nt) {
		return apperr.Config("GitLab connection " + nt.Conn + " has no token").WithCause(err)
	}
	return apperr.ExternalService("GitLab request failed").WithCause(err)
}

// NotConnectedError is the 422 repo_host_not_connected answer for a GitLab repository whose host
// has no connection (FR48), with details {host, repo_id}.
func NotConnectedError(repo domain.Repo) *apperr.Error {
	return apperr.Validation("No GitLab connection for " + repo.Host + ". Add one in Settings → GitLab Integration.").
		WithCode("repo_host_not_connected").
		WithDetails(map[string]string{"host": repo.Host, "repo_id": repo.ID})
}

// fetchGitLabDetail reads a merge request with its files and commits inside GitLabDetailTimeout.
// Commits come newest first from GitLab; they are returned oldest first like GitHub's.
func (s *Service) fetchGitLabDetail(ctx context.Context, repo domain.Repo, number int) (domain.Fetched, error) {
	dctx, cancel := context.WithTimeout(ctx, GitLabDetailTimeout)
	defer cancel()
	fetched, err := s.gitLab().Get(dctx, repo, number)
	if err != nil {
		return domain.Fetched{}, err
	}
	fetched.Commits = slices.Clone(fetched.Commits)
	slices.Reverse(fetched.Commits)
	return fetched, nil
}

// gitLabDetailStats decides the stats of a fetched merge request (FR24): the fetch's own when it
// has any, else the diff-stats source (FR26), else the sums of the merged files. A failing source
// is logged once and falls back to the sums.
func (s *Service) gitLabDetailStats(ctx context.Context, repo domain.Repo, fetched domain.Fetched, files []domain.File) domain.DiffStats {
	if !domain.NeedsDiffStats(fetched.DiffStats) {
		return fetched.DiffStats
	}
	stats, err := s.gitLab().Stats(ctx, repo, []int{fetched.Number})
	if err != nil {
		s.log.WarnContext(ctx, "gitlab diff stats not read; counting from files", "number", fetched.Number, "err", err)
	} else if st, ok := stats[fetched.Number]; ok && !domain.NeedsDiffStats(st) {
		return st
	}
	return domain.FileTotals(files)
}

// backfillDiffStatsGitLab fills the stats of at most BackfillLimit stored merge requests that still
// have none with one GraphQL query per GitLabStatsBatch numbers (FR26). A failed batch is logged
// once and leaves its merge requests unset for the next list request; a merge request without a
// summary stays unset too.
func (s *Service) backfillDiffStatsGitLab(ctx context.Context, repo domain.Repo, pulls []domain.Pull) {
	var pending []int // indexes into pulls
	for i, p := range pulls {
		if domain.NeedsDiffStats(domain.DiffStats{Additions: p.Additions, Deletions: p.Deletions, FilesCount: p.FilesCount}) {
			pending = append(pending, i)
			if len(pending) == BackfillLimit {
				break
			}
		}
	}
	for batch := range slices.Chunk(pending, domain.GitLabStatsBatch) {
		if ctx.Err() != nil {
			return
		}
		numbers := make([]int, len(batch))
		for j, i := range batch {
			numbers[j] = pulls[i].Number
		}
		stats, err := s.gitLab().Stats(ctx, repo, numbers)
		if err != nil {
			s.log.WarnContext(ctx, "gitlab diff-stat backfill skipped", "repo_id", repo.ID, "merge_requests", len(numbers), "err", err)
			continue
		}
		for _, i := range batch {
			p := &pulls[i]
			st, ok := stats[p.Number]
			if !ok || domain.NeedsDiffStats(st) {
				continue
			}
			if err := s.store.SetDiffStats(ctx, p.ID, st); err != nil {
				s.log.WarnContext(ctx, "gitlab diff-stat backfill not stored", "number", p.Number, "err", err)
				continue
			}
			p.Additions, p.Deletions, p.FilesCount = st.Additions, st.Deletions, st.FilesCount
		}
	}
}
