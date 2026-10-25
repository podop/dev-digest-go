package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/retry"
)

// PullSummary is a pull request as GitHub lists it. Diff stats are not part of
// the list payload.
type PullSummary struct {
	Number    int
	Title     string
	Author    string // "unknown" when GitHub has no user
	Branch    string // head ref
	Base      string // base ref
	HeadSHA   string
	Status    string // open | closed | merged
	OpenedAt  time.Time
	UpdatedAt time.Time
	MergedAt  *time.Time // nil unless merged
	ClosedAt  *time.Time // nil while open; set for closed and merged PRs
	Draft     bool
	// HeadIsFork is true when the head branch lives in another repository than the PR's base
	// repository, and also when GitHub names no head repository (a deleted fork) or no base one:
	// anything that cannot be shown to be the same repository counts as a fork.
	HeadIsFork bool
}

// File is one changed file of a pull request. Patch is nil when GitHub gives
// none (binary or too large).
type File struct {
	Path      string
	Additions int
	Deletions int
	Patch     *string
}

// Commit is one commit of a pull request.
type Commit struct {
	SHA         string
	Message     string
	Author      string
	CommittedAt *time.Time
}

// PullDetail is a pull request with its files (at most MaxFiles, merged by
// path) and commits (at most MaxCommits).
type PullDetail struct {
	PullSummary
	Body       *string
	Additions  int
	Deletions  int
	FilesCount int // GitHub's changed_files, which may exceed len(Files)
	Files      []File
	Commits    []Commit
}

// Issue is a GitHub issue.
type Issue struct {
	Number int
	Title  string
	Body   *string
	State  string
}

type apiUser struct {
	Login string `json:"login"`
}

type apiRepo struct {
	FullName string `json:"full_name"`
}

type apiPull struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	User   *apiUser `json:"user"`
	Head   struct {
		Ref  string   `json:"ref"`
		SHA  string   `json:"sha"`
		Repo *apiRepo `json:"repo"` // null when the head repository was deleted
	} `json:"head"`
	Base struct {
		Ref  string   `json:"ref"`
		Repo *apiRepo `json:"repo"`
	} `json:"base"`
	State        string     `json:"state"`
	MergedAt     *time.Time `json:"merged_at"`
	ClosedAt     *time.Time `json:"closed_at"`
	Draft        bool       `json:"draft"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Body         *string    `json:"body"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	ChangedFiles int        `json:"changed_files"`
}

type apiIssue struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Body   *string `json:"body"`
	State  string  `json:"state"`
}

type apiFile struct {
	Filename  string  `json:"filename"`
	Additions int     `json:"additions"`
	Deletions int     `json:"deletions"`
	Patch     *string `json:"patch"`
}

type apiCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  *struct {
			Name string     `json:"name"`
			Date *time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *apiUser `json:"author"`
}

func login(u *apiUser) string {
	if u == nil || u.Login == "" {
		return "unknown"
	}
	return u.Login
}

func mapStatus(state string, mergedAt *time.Time) string {
	switch {
	case mergedAt != nil:
		return "merged"
	case state == "closed":
		return "closed"
	default:
		return "open"
	}
}

// headIsFork reports whether the head branch is in another repository than the base. Both names
// must be present and equal (GitHub names are case-insensitive); anything else is a fork.
func (p apiPull) headIsFork() bool {
	h, b := p.Head.Repo, p.Base.Repo
	if h == nil || b == nil || h.FullName == "" || b.FullName == "" {
		return true
	}
	return !strings.EqualFold(h.FullName, b.FullName)
}

func (p apiPull) summary() PullSummary {
	return PullSummary{
		Number:    p.Number,
		Title:     p.Title,
		Author:    login(p.User),
		Branch:    p.Head.Ref,
		Base:      p.Base.Ref,
		HeadSHA:   p.Head.SHA,
		Status:    mapStatus(p.State, p.MergedAt),
		OpenedAt:  p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		MergedAt:  p.MergedAt,
		ClosedAt:  p.ClosedAt,
		Draft:     p.Draft,

		HeadIsFork: p.headIsFork(),
	}
}

// ListPullRequests returns one page of up to 50 pull requests of any state,
// most recently updated first.
func (c *Client) ListPullRequests(ctx context.Context, owner, name string) ([]PullSummary, error) {
	return c.ListPullRequestsPage(ctx, owner, name, 1, 50)
}

// ListPullRequestsPage returns page `page` (1-based) of `perPage` (1..100) pull
// requests of any state, most recently updated first. A page shorter than
// perPage is the last one.
func (c *Client) ListPullRequestsPage(ctx context.Context, owner, name string, page, perPage int) ([]PullSummary, error) {
	if page < 1 || perPage < 1 || perPage > PageSize {
		return nil, fmt.Errorf("github list pull requests: page %d and per_page %d out of range (page >= 1, per_page 1..%d)", page, perPage, PageSize)
	}
	token, err := c.currentToken()
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {strconv.Itoa(perPage)},
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	pulls, err := getJSON[[]apiPull](ctx, c, token, repoPath(owner, name)+"/pulls", q)
	if err != nil {
		return nil, err
	}
	out := make([]PullSummary, len(pulls))
	for i, p := range pulls {
		out[i] = p.summary()
	}
	return out, nil
}

// GetPullRequest fetches a pull request with up to MaxFiles files and
// MaxCommits commits, all within DetailTimeout. Files with the same path are
// merged into one entry.
func (c *Client) GetPullRequest(ctx context.Context, owner, name string, number int) (PullDetail, error) {
	token, err := c.currentToken()
	if err != nil {
		return PullDetail{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.detailTimeout)
	defer cancel()

	base := repoPath(owner, name) + "/pulls/" + strconv.Itoa(number)
	pr, err := getJSON[apiPull](ctx, c, token, base, nil)
	if err != nil {
		return PullDetail{}, err
	}
	files, err := paginateCapped[apiFile](ctx, c, token, base+"/files", MaxFiles)
	if err != nil {
		return PullDetail{}, err
	}
	commits, err := paginateCapped[apiCommit](ctx, c, token, base+"/commits", MaxCommits)
	if err != nil {
		return PullDetail{}, err
	}

	detail := PullDetail{
		PullSummary: pr.summary(),
		Body:        pr.Body,
		Additions:   pr.Additions,
		Deletions:   pr.Deletions,
		FilesCount:  pr.ChangedFiles,
	}
	raw := make([]File, len(files))
	for i, f := range files {
		raw[i] = File{Path: f.Filename, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch}
	}
	detail.Files = MergeSamePath(raw)
	detail.Commits = make([]Commit, len(commits))
	for i, cm := range commits {
		author := login(cm.Author)
		if cm.Commit.Author != nil && cm.Commit.Author.Name != "" {
			author = cm.Commit.Author.Name
		}
		var at *time.Time
		if cm.Commit.Author != nil {
			at = cm.Commit.Author.Date
		}
		detail.Commits[i] = Commit{SHA: cm.SHA, Message: cm.Commit.Message, Author: author, CommittedAt: at}
	}
	return detail, nil
}

// PullStats are the diff stats GitHub reports on a pull request.
type PullStats struct {
	Additions  int
	Deletions  int
	FilesCount int
}

// GetPullStats fetches a pull request's diff stats with one request: the same additions,
// deletions and changed_files fields GetPullRequest reads, without its files and commits pages.
func (c *Client) GetPullStats(ctx context.Context, owner, name string, number int) (PullStats, error) {
	token, err := c.currentToken()
	if err != nil {
		return PullStats{}, err
	}
	pr, err := getJSON[apiPull](ctx, c, token, repoPath(owner, name)+"/pulls/"+strconv.Itoa(number), nil)
	if err != nil {
		return PullStats{}, err
	}
	return PullStats{Additions: pr.Additions, Deletions: pr.Deletions, FilesCount: pr.ChangedFiles}, nil
}

// paginateCapped reads pages of PageSize until a short page or max items.
func paginateCapped[T any](ctx context.Context, c *Client, token, path string, max int) ([]T, error) {
	var out []T
	for page := 1; len(out) < max; page++ {
		q := url.Values{"per_page": {strconv.Itoa(PageSize)}, "page": {strconv.Itoa(page)}}
		items, err := getJSON[[]T](ctx, c, token, path, q)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) < PageSize {
			break
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// MergeSamePath folds entries with the same path into one, in first-seen order:
// additions and deletions are summed and non-empty patches joined with a
// newline. GitHub lists a file/symlink type change as two entries.
func MergeSamePath(files []File) []File {
	out := make([]File, 0, len(files))
	index := make(map[string]int, len(files))
	for _, f := range files {
		i, seen := index[f.Path]
		if !seen {
			index[f.Path] = len(out)
			out = append(out, f)
			continue
		}
		prev := &out[i]
		prev.Additions += f.Additions
		prev.Deletions += f.Deletions
		var patches []string
		for _, p := range []*string{prev.Patch, f.Patch} {
			if p != nil && *p != "" {
				patches = append(patches, *p)
			}
		}
		if len(patches) > 0 {
			joined := patches[0]
			for _, p := range patches[1:] {
				joined += "\n" + p
			}
			prev.Patch = &joined
		}
	}
	return out
}

// GetIssue fetches one issue.
func (c *Client) GetIssue(ctx context.Context, owner, name string, number int) (Issue, error) {
	token, err := c.currentToken()
	if err != nil {
		return Issue{}, err
	}
	raw, err := getJSON[apiIssue](ctx, c, token, repoPath(owner, name)+"/issues/"+strconv.Itoa(number), nil)
	if err != nil {
		return Issue{}, err
	}
	return Issue{Number: raw.Number, Title: raw.Title, Body: raw.Body, State: raw.State}, nil
}

// CurrentLogin returns the login of the token's owner (GET /user). It is CurrentUser reduced to
// the login, so it does not follow redirects either.
func (c *Client) CurrentLogin(ctx context.Context) (string, error) {
	u, err := c.CurrentUser(ctx)
	if err != nil {
		return "", err
	}
	return u.Login, nil
}

// User is the token's owner as GET /user reports it.
type User struct {
	Login string
	// Scopes is the X-OAuth-Scopes list of a classic token; empty when the header is present but
	// empty. Only meaningful when ScopesReported.
	Scopes []string
	// ScopesReported is false when GitHub sent no X-OAuth-Scopes header (fine-grained tokens).
	ScopesReported bool
}

// CurrentUser returns the token's owner and its OAuth scopes (GET /user). It never follows a
// redirect, so the token cannot reach another host.
func (c *Client) CurrentUser(ctx context.Context) (User, error) {
	token, err := c.currentToken()
	if err != nil {
		return User{}, err
	}
	strict := c.noRedirect()
	type result struct {
		user   apiUser
		header http.Header
	}
	res, err := retry.Value(ctx, strict.retry, func(ctx context.Context) (result, error) {
		var r result
		h, err := strict.doHeader(ctx, token, http.MethodGet, "/user", nil, nil, &r.user)
		r.header = h
		return r, err
	})
	if err != nil {
		return User{}, err
	}
	u := User{Login: res.user.Login, Scopes: []string{}}
	if vals, ok := res.header["X-Oauth-Scopes"]; ok {
		u.ScopesReported = true
		for _, part := range strings.Split(strings.Join(vals, ","), ",") {
			if part = strings.TrimSpace(part); part != "" {
				u.Scopes = append(u.Scopes, part)
			}
		}
	}
	return u, nil
}
