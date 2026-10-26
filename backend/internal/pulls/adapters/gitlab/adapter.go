// Package gitlab adapts the platform GitLab client to the pull request use cases: it resolves the
// repository's connection by host key, addresses the project by its numeric id (FR17), converts
// the client's types to domain types and its errors to the app's GitLab errors.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/codehost"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

// defaultBranch is what a project without a default branch (an empty repository) reads as.
const defaultBranch = "main"

// apiSuffix is appended to a connection's base URL to get the REST root.
const apiSuffix = "/api/v4"

// Conn is a GitLab connection as the adapter needs it. BaseURL is the connection's normalised
// base (https://host[:port][/prefix], no /api/v4); Token may be empty (no token set).
type Conn struct {
	ID      string
	Name    string
	BaseURL string
	Host    string
	Token   string
}

// Resolver finds the connection whose host key equals host (spec FR48). ok is false when there is
// none. It is read on every call, so a token change applies to the next request.
type Resolver interface {
	Resolve(ctx context.Context, host string) (conn Conn, ok bool, err error)
}

// ResolverFunc makes a function a Resolver (the composition root adapts the connection service's
// own result type with it).
type ResolverFunc func(ctx context.Context, host string) (Conn, bool, error)

// Resolve calls f.
func (f ResolverFunc) Resolve(ctx context.Context, host string) (Conn, bool, error) {
	return f(ctx, host)
}

// api is the part of *glclient.Client the adapter calls.
type api interface {
	Project(ctx context.Context, conn glclient.Conn, id int64) (glclient.Project, error)
	MergeRequestsPage(ctx context.Context, conn glclient.Conn, projectID int64, page, perPage int) ([]glclient.MergeRequest, error)
	MergeRequest(ctx context.Context, conn glclient.Conn, projectID int64, iid int) (glclient.MergeRequestDetail, error)
	Diffs(ctx context.Context, conn glclient.Conn, projectID int64, iid int) ([]glclient.File, error)
	Commits(ctx context.Context, conn glclient.Conn, projectID int64, iid int) ([]glclient.Commit, error)
	Issue(ctx context.Context, conn glclient.Conn, projectID int64, iid int) (glclient.Issue, error)
	DiffStats(ctx context.Context, conn glclient.Conn, projectID int64, iids []int) (map[int]glclient.Stats, error)
}

// Adapter implements the GitLab port of the pull request use cases (app.GitLab).
type Adapter struct {
	api api
	res Resolver
}

var _ app.GitLab = (*Adapter)(nil)

// New builds an Adapter over the platform client and the connection resolver. The background
// poller passes a client built with WithRetry(retry.Options{}) so that its per-repository request
// bound holds.
func New(c *glclient.Client, res Resolver) *Adapter { return &Adapter{api: c, res: res} }

// target is the connection of a repository, ready for the client, and its base URL.
type target struct {
	conn glclient.Conn
	base string
}

// resolve finds the repository's connection. No connection is app.ErrNotConnected, a connection
// without a token *app.NoTokenError; neither sends a request.
func (a *Adapter) resolve(ctx context.Context, repo domain.Repo) (target, error) {
	if !repo.IsGitLab() || repo.ProjectID <= 0 {
		return target{}, fmt.Errorf("repository %s is not a gitlab project", repo.ID)
	}
	c, ok, err := a.res.Resolve(ctx, repo.Host)
	if err != nil {
		return target{}, fmt.Errorf("resolve gitlab connection: %w", err)
	}
	if !ok {
		return target{}, app.ErrNotConnected
	}
	if strings.TrimSpace(c.Token) == "" {
		return target{}, &app.NoTokenError{Conn: c.Name}
	}
	base := strings.TrimRight(c.BaseURL, "/")
	return target{conn: glclient.Conn{BaseURL: base + apiSuffix, Token: c.Token}, base: base}, nil
}

// Project reads the project's current identity. The web URL is built from the connection.
func (a *Adapter) Project(ctx context.Context, repo domain.Repo) (domain.GitLabProject, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return domain.GitLabProject{}, err
	}
	p, err := a.api.Project(ctx, t.conn, repo.ProjectID)
	if err != nil {
		return domain.GitLabProject{}, mapErr(err)
	}
	branch := p.DefaultBranch
	if branch == "" {
		branch = defaultBranch
	}
	return domain.GitLabProject{
		ID: p.ID, FullName: p.PathWithNamespace, Owner: p.NamespaceFullPath, Name: p.Path,
		DefaultBranch: branch, WebURL: codehost.WebURL(t.base, p.PathWithNamespace),
	}, nil
}

// ListPage returns page `page` (1-based) of `perPage` (1..100) merge requests of any state, most
// recently updated first; merge requests without a head sha are dropped (FR22). more is true when
// the raw page held perPage merge requests, counted before that drop: a full page may be followed
// by another even when the dropped ones leave it short.
func (a *Adapter) ListPage(ctx context.Context, repo domain.Repo, page, perPage int) ([]domain.Listed, bool, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return nil, false, err
	}
	mrs, err := a.api.MergeRequestsPage(ctx, t.conn, repo.ProjectID, page, perPage)
	if err != nil {
		return nil, false, mapErr(err)
	}
	out := make([]domain.Listed, 0, len(mrs))
	for _, mr := range mrs {
		if mr.HeadSHA == "" {
			continue
		}
		out = append(out, listed(mr))
	}
	return out, len(mrs) >= perPage, nil
}

// Get fetches a merge request with its files and commits. The commits keep the client's order,
// newest first as GitLab lists them: the use case (pulls/app) puts them oldest first, as GitHub's
// adapter does. The diff stats come from the GraphQL summary, else from the sums of the files
// (FR24, FR26).
func (a *Adapter) Get(ctx context.Context, repo domain.Repo, number int) (domain.Fetched, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return domain.Fetched{}, err
	}
	d, err := a.api.MergeRequest(ctx, t.conn, repo.ProjectID, number)
	if err != nil {
		return domain.Fetched{}, mapErr(err)
	}
	files, err := a.api.Diffs(ctx, t.conn, repo.ProjectID, number)
	if err != nil {
		return domain.Fetched{}, mapErr(err)
	}
	commits, err := a.api.Commits(ctx, t.conn, repo.ProjectID, number)
	if err != nil {
		return domain.Fetched{}, mapErr(err)
	}
	out := domain.Fetched{
		Listed:  listed(d.MergeRequest),
		Body:    d.Description,
		Files:   make([]domain.File, len(files)),
		Commits: make([]domain.Commit, len(commits)),
	}
	for i, f := range files {
		out.Files[i] = domain.File{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch}
	}
	for i, c := range commits {
		out.Commits[i] = domain.Commit{SHA: c.SHA, Message: c.Message, Author: c.Author, CommittedAt: c.CommittedAt}
	}
	out.DiffStats = domain.FileTotals(out.Files)
	// A failed or empty summary keeps the file sums; the client logged the attempt.
	if stats, err := a.api.DiffStats(ctx, t.conn, repo.ProjectID, []int{number}); err == nil {
		if s, ok := stats[number]; ok {
			out.DiffStats = domain.DiffStats{Additions: s.Additions, Deletions: s.Deletions, FilesCount: s.FilesCount}
		}
	} else if ctx.Err() != nil {
		return domain.Fetched{}, ctx.Err()
	}
	return out, nil
}

// Issue fetches one issue.
func (a *Adapter) Issue(ctx context.Context, repo domain.Repo, number int) (domain.Issue, error) {
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return domain.Issue{}, err
	}
	i, err := a.api.Issue(ctx, t.conn, repo.ProjectID, number)
	if err != nil {
		return domain.Issue{}, mapErr(err)
	}
	return domain.Issue{Number: i.Number, Title: i.Title, Body: i.Body, State: i.State}, nil
}

// Stats reads the diff stats of up to domain.GitLabStatsBatch merge requests with one query. A
// merge request GitLab returns no (or an all-zero) summary for is absent from the map.
func (a *Adapter) Stats(ctx context.Context, repo domain.Repo, numbers []int) (map[int]domain.DiffStats, error) {
	if len(numbers) > domain.GitLabStatsBatch {
		return nil, fmt.Errorf("gitlab stats: %d merge requests, at most %d", len(numbers), domain.GitLabStatsBatch)
	}
	t, err := a.resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	raw, err := a.api.DiffStats(ctx, t.conn, repo.ProjectID, slices.Clone(numbers))
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[int]domain.DiffStats, len(raw))
	for n, s := range raw {
		out[n] = domain.DiffStats{Additions: s.Additions, Deletions: s.Deletions, FilesCount: s.FilesCount}
	}
	return out, nil
}

// listed maps a client merge request (status and fork flag already decided by the client, SCR1) the
// way domain.MapMergeRequest does: merged_at is kept only for a merged request, closed_at only for
// one that is not open.
func listed(mr glclient.MergeRequest) domain.Listed {
	l := domain.Listed{
		Number: mr.IID, Title: mr.Title, Author: mr.Author, Branch: mr.Branch, Base: mr.Base,
		HeadSHA: mr.HeadSHA, Status: mr.Status, OpenedAt: timePtr(mr.OpenedAt), UpdatedAt: timePtr(mr.UpdatedAt),
		Draft: mr.Draft, HeadIsFork: mr.HeadIsFork,
	}
	if mr.Status == domain.StatusMerged {
		l.MergedAt = mr.MergedAt
	}
	if mr.Status != domain.StatusOpen {
		l.ClosedAt = mr.ClosedAt
	}
	return l
}

// timePtr maps GitLab's always-present timestamps; a zero time means GitLab sent none.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// mapErr converts a client error: a non-2xx answer becomes *app.GitLabError (a 429 is a rate
// limit, ending at Retry-After or RateLimit-Reset); everything else (network, timeout, decoding,
// redirects) passes through unchanged.
func mapErr(err error) error {
	var se *glclient.StatusError
	if errors.As(err, &se) {
		return &app.GitLabError{
			Status: se.Status, Message: se.Message, RateLimited: se.Status == 429,
			RetryAfter: se.RetryAfter, RateLimitReset: se.Reset,
		}
	}
	return err
}
