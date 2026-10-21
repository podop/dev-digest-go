// Package github adapts the platform GitHub client to the pull request use cases: it converts
// the client's types to domain types and its errors to the app's GitHub errors.
package github

import (
	"context"
	"errors"
	"time"

	ghclient "dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

// api is the part of *ghclient.Client the adapter calls.
type api interface {
	ListPullRequests(ctx context.Context, owner, name string) ([]ghclient.PullSummary, error)
	GetPullRequest(ctx context.Context, owner, name string, number int) (ghclient.PullDetail, error)
	GetIssue(ctx context.Context, owner, name string, number int) (ghclient.Issue, error)
	ListReviewComments(ctx context.Context, owner, name string, number int) ([]ghclient.ReviewComment, error)
	CreateReviewComment(ctx context.Context, owner, name string, number int, in ghclient.CommentInput) (ghclient.ReviewComment, error)
}

// Adapter implements the GitHub port of the pull request use cases.
type Adapter struct {
	api api
}

// New builds an Adapter over the platform client.
func New(c *ghclient.Client) *Adapter { return &Adapter{api: c} }

// ListPullRequests returns one page of up to 50 PRs, most recently updated first.
func (a *Adapter) ListPullRequests(ctx context.Context, owner, name string) ([]domain.Listed, error) {
	pulls, err := a.api.ListPullRequests(ctx, owner, name)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]domain.Listed, len(pulls))
	for i, p := range pulls {
		out[i] = listed(p)
	}
	return out, nil
}

// GetPullRequest fetches a PR with its files and commits.
func (a *Adapter) GetPullRequest(ctx context.Context, owner, name string, number int) (domain.Fetched, error) {
	d, err := a.api.GetPullRequest(ctx, owner, name, number)
	if err != nil {
		return domain.Fetched{}, mapErr(err)
	}
	out := domain.Fetched{
		Listed:    listed(d.PullSummary),
		Body:      d.Body,
		DiffStats: domain.DiffStats{Additions: d.Additions, Deletions: d.Deletions, FilesCount: d.FilesCount},
		Files:     make([]domain.File, len(d.Files)),
		Commits:   make([]domain.Commit, len(d.Commits)),
	}
	for i, f := range d.Files {
		out.Files[i] = domain.File{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch}
	}
	for i, c := range d.Commits {
		out.Commits[i] = domain.Commit{SHA: c.SHA, Message: c.Message, Author: c.Author, CommittedAt: c.CommittedAt}
	}
	return out, nil
}

// GetIssue fetches one issue.
func (a *Adapter) GetIssue(ctx context.Context, owner, name string, number int) (domain.Issue, error) {
	i, err := a.api.GetIssue(ctx, owner, name, number)
	if err != nil {
		return domain.Issue{}, mapErr(err)
	}
	return domain.Issue{Number: i.Number, Title: i.Title, Body: i.Body, State: i.State}, nil
}

// ListReviewComments returns one page of the PR's review comments.
func (a *Adapter) ListReviewComments(ctx context.Context, owner, name string, number int) ([]domain.ReviewComment, error) {
	comments, err := a.api.ListReviewComments(ctx, owner, name, number)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]domain.ReviewComment, len(comments))
	for i, c := range comments {
		out[i] = comment(c)
	}
	return out, nil
}

// CreateReviewComment posts a comment or a reply. The platform client never retries it.
func (a *Adapter) CreateReviewComment(ctx context.Context, owner, name string, number int, in domain.CommentInput) (domain.ReviewComment, error) {
	c, err := a.api.CreateReviewComment(ctx, owner, name, number, ghclient.CommentInput{
		CommitID: in.CommitID, Path: in.Path, Line: in.Line, Side: in.Side, Body: in.Body, InReplyTo: in.InReplyTo,
	})
	if err != nil {
		return domain.ReviewComment{}, mapErr(err)
	}
	return comment(c), nil
}

func listed(p ghclient.PullSummary) domain.Listed {
	return domain.Listed{
		Number: p.Number, Title: p.Title, Author: p.Author, Branch: p.Branch, Base: p.Base,
		HeadSHA: p.HeadSHA, Status: p.Status, OpenedAt: timePtr(p.OpenedAt), UpdatedAt: timePtr(p.UpdatedAt),
	}
}

// timePtr maps GitHub's always-present timestamps; a zero time means GitHub sent none.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func comment(c ghclient.ReviewComment) domain.ReviewComment {
	return domain.ReviewComment{
		ID: c.ID, Path: c.Path, Line: c.Line, OriginalLine: c.OriginalLine, Side: c.Side, Body: c.Body,
		User: c.User, CreatedAt: c.CreatedAt, HTMLURL: c.HTMLURL, InReplyToID: c.InReplyToID, IsOutdated: c.IsOutdated,
	}
}

// mapErr converts client errors to the app's: the missing token and non-2xx answers get their
// own types, everything else (network, timeout, decoding) passes through unchanged.
func mapErr(err error) error {
	if errors.Is(err, ghclient.ErrNoToken) {
		return app.ErrNoToken
	}
	var se *ghclient.StatusError
	if errors.As(err, &se) {
		return &app.GitHubError{Status: se.Status, Message: se.Message}
	}
	return err
}
