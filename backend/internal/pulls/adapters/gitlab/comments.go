package gitlab

import (
	"context"
	"errors"
	"fmt"

	glclient "dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

// discussionsPerPage is the page size of the discussions read (spec B FR9, D27): the largest
// GitLab allows, so a merge request with up to 100 threads needs one request.
const discussionsPerPage = 100

var _ app.GitLabComments = (*Adapter)(nil)

// Ready resolves the repository's connection without sending a request: nil when a connection
// with a token exists, else app.ErrNotConnected or *app.NoTokenError.
func (a *Adapter) Ready(ctx context.Context, repo domain.Repo) error {
	_, err := a.resolve(ctx, repo)
	return err
}

// Discussions reads page `page` (1-based) of the merge request's discussions, 100 per page. more
// is true when the page was full, so a next page may exist (GitLab's next-page links are not read).
func (a *Adapter) Discussions(ctx context.Context, repo domain.Repo, number, page int) ([]domain.Discussion, bool, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return nil, false, err
	}
	raw, err := a.api.DiscussionsPage(ctx, t.conn, repo.ProjectID, number, page, discussionsPerPage)
	if err != nil {
		return nil, false, commentErr(ctx, t, err)
	}
	out := make([]domain.Discussion, len(raw))
	for i, d := range raw {
		out[i] = discussion(d)
	}
	return out, len(raw) >= discussionsPerPage, nil
}

// MergeRequestRefs reads the merge request's diff_refs; fields GitLab omits are "".
func (a *Adapter) MergeRequestRefs(ctx context.Context, repo domain.Repo, number int) (domain.DiffRefs, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return domain.DiffRefs{}, err
	}
	d, err := a.api.MergeRequest(ctx, t.conn, repo.ProjectID, number)
	if err != nil {
		return domain.DiffRefs{}, commentErr(ctx, t, err)
	}
	return domain.DiffRefs{BaseSHA: d.DiffRefs.BaseSHA, StartSHA: d.DiffRefs.StartSHA, HeadSHA: d.DiffRefs.HeadSHA}, nil
}

// LatestVersion reads the newest diff version of the merge request. ok is false while GitLab has
// no version yet (the diff is not ready).
func (a *Adapter) LatestVersion(ctx context.Context, repo domain.Repo, number int) (domain.DiffRefs, bool, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return domain.DiffRefs{}, false, err
	}
	vs, err := a.api.Versions(ctx, t.conn, repo.ProjectID, number)
	if err != nil {
		return domain.DiffRefs{}, false, commentErr(ctx, t, err)
	}
	if len(vs) == 0 {
		return domain.DiffRefs{}, false, nil
	}
	v := vs[0]
	return domain.DiffRefs{BaseSHA: v.BaseSHA, StartSHA: v.StartSHA, HeadSHA: v.HeadSHA}, true, nil
}

// CreateDiscussion posts a new positioned diff discussion and returns it with its first note. The
// client sends exactly one request: a lost answer may hide a created discussion, so it is never
// retried.
func (a *Adapter) CreateDiscussion(ctx context.Context, repo domain.Repo, number int, body string, pos domain.Position) (domain.Discussion, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return domain.Discussion{}, err
	}
	d, err := a.api.CreateDiscussion(ctx, t.conn, repo.ProjectID, number, body, glclient.Position{
		Type: pos.Type, BaseSHA: pos.BaseSHA, StartSHA: pos.StartSHA, HeadSHA: pos.HeadSHA,
		OldPath: pos.OldPath, NewPath: pos.NewPath, OldLine: pos.OldLine, NewLine: pos.NewLine,
	})
	if err != nil {
		return domain.Discussion{}, commentErr(ctx, t, err)
	}
	return discussion(d), nil
}

// CreateNote posts a reply into the discussion discussionID, with one request and no retry.
func (a *Adapter) CreateNote(ctx context.Context, repo domain.Repo, number int, discussionID, body string) (domain.Note, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return domain.Note{}, err
	}
	n, err := a.api.CreateNote(ctx, t.conn, repo.ProjectID, number, discussionID, body)
	if err != nil {
		return domain.Note{}, commentErr(ctx, t, err)
	}
	return note(n), nil
}

// commentErr converts a client error for the comments port: a non-2xx answer is *app.GitLabError
// (mapErr), a redirect app.ErrGitLabRedirect, and every other failure without an answer (timeout,
// connection or TLS error, refused address, a 2xx body that is not the expected JSON)
// app.ErrGitLabUnavailable, the cause kept for logs. The client has scrubbed the token from every
// message. An error after the caller's own context ended is returned as it is, so a cancelled
// request is not reported as an outage. A *app.GitLabError carries the connection's display name.
func commentErr(ctx context.Context, t target, err error) error {
	err = mapErr(err)
	var ge *app.GitLabError
	switch {
	case errors.As(err, &ge):
		ge.Conn = t.name
		return err
	case ctx.Err() != nil:
		return err
	case errors.Is(err, glclient.ErrRedirect):
		return app.ErrGitLabRedirect
	default:
		return fmt.Errorf("%w: %w", app.ErrGitLabUnavailable, err)
	}
}

func discussion(d glclient.Discussion) domain.Discussion {
	out := domain.Discussion{ID: d.ID, Notes: make([]domain.Note, len(d.Notes))}
	for i, n := range d.Notes {
		out.Notes[i] = note(n)
	}
	return out
}

func note(n glclient.Note) domain.Note {
	out := domain.Note{
		ID: n.ID, Type: n.Type, System: n.System, Body: n.Body, Author: n.Author, CreatedAt: n.CreatedAt,
	}
	if p := n.Position; p != nil {
		out.Position = &domain.Position{
			Type: p.Type, BaseSHA: p.BaseSHA, StartSHA: p.StartSHA, HeadSHA: p.HeadSHA,
			OldPath: p.OldPath, NewPath: p.NewPath, OldLine: p.OldLine, NewLine: p.NewLine,
		}
	}
	return out
}
