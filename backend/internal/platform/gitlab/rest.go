package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// User is the token's owner as GET /user reports it.
type User struct {
	Username string
}

// User calls GET /user (FR10). A 2xx answer without a username is ErrNotAPI.
func (c *Client) User(ctx context.Context, conn Conn) (User, error) {
	raw, err := getJSON[struct {
		Username string `json:"username"`
	}](ctx, c, conn, "user", "/user", nil)
	if err != nil {
		return User{}, err
	}
	if raw.Username == "" {
		return User{}, fmt.Errorf("gitlab user: %w", ErrNotAPI)
	}
	return User{Username: raw.Username}, nil
}

// TokenInfo is what GET /personal_access_tokens/self reports.
type TokenInfo struct {
	// Scopes are the token's scopes; nil when the answer had none.
	Scopes []string
	// ExpiresAt is the expiry date as GitLab writes it (YYYY-MM-DD); "" for a token without one.
	ExpiresAt string
}

// TokenSelf calls GET /personal_access_tokens/self (FR10). Any failure is the caller's
// "scopes unknown".
func (c *Client) TokenSelf(ctx context.Context, conn Conn) (TokenInfo, error) {
	raw, err := getJSON[struct {
		Scopes    []string `json:"scopes"`
		ExpiresAt *string  `json:"expires_at"`
	}](ctx, c, conn, "token_self", "/personal_access_tokens/self", nil)
	if err != nil {
		return TokenInfo{}, err
	}
	info := TokenInfo{Scopes: raw.Scopes}
	if raw.ExpiresAt != nil {
		info.ExpiresAt = *raw.ExpiresAt
	}
	return info, nil
}

// Metadata is what GET /metadata reports.
type Metadata struct {
	Version string
}

// Metadata calls GET /metadata (FR10). Any failure is the caller's "version unknown".
func (c *Client) Metadata(ctx context.Context, conn Conn) (Metadata, error) {
	raw, err := getJSON[struct {
		Version string `json:"version"`
	}](ctx, c, conn, "metadata", "/metadata", nil)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{Version: raw.Version}, nil
}

// Project is a GitLab project as FR14 reads it.
type Project struct {
	ID                int64
	PathWithNamespace string
	Path              string
	NamespaceFullPath string
	// DefaultBranch is "" when GitLab reports none (an empty project); FR14 stores "main" then.
	DefaultBranch string
	WebURL        string
}

type apiProject struct {
	ID                int64   `json:"id"`
	Path              string  `json:"path"`
	PathWithNamespace string  `json:"path_with_namespace"`
	DefaultBranch     *string `json:"default_branch"`
	WebURL            string  `json:"web_url"`
	Namespace         *struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

func (p apiProject) project() (Project, error) {
	if p.ID <= 0 || p.PathWithNamespace == "" || p.Path == "" {
		return Project{}, fmt.Errorf("gitlab project: %w", ErrNotAPI)
	}
	out := Project{ID: p.ID, PathWithNamespace: p.PathWithNamespace, Path: p.Path, WebURL: p.WebURL}
	if p.DefaultBranch != nil {
		out.DefaultBranch = *p.DefaultBranch
	}
	if p.Namespace != nil {
		out.NamespaceFullPath = p.Namespace.FullPath
	}
	return out, nil
}

// ProjectByPath calls GET /projects/<URL-encoded path> (FR14). A path with an empty, "." or ".."
// segment, a control character or a backslash is refused without a request: it could leave the
// REST root.
func (c *Client) ProjectByPath(ctx context.Context, conn Conn, path string) (Project, error) {
	if !validProjectPath(path) {
		return Project{}, fmt.Errorf("gitlab project: invalid project path")
	}
	raw, err := getJSON[apiProject](ctx, c, conn, "project", "/projects/"+url.PathEscape(path), nil)
	if err != nil {
		return Project{}, err
	}
	return raw.project()
}

// Project calls GET /projects/:id (before every list sync, A: renames are followed by id).
func (c *Client) Project(ctx context.Context, conn Conn, id int64) (Project, error) {
	if id <= 0 {
		return Project{}, fmt.Errorf("gitlab project: id %d out of range", id)
	}
	raw, err := getJSON[apiProject](ctx, c, conn, "project", "/projects/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return Project{}, err
	}
	return raw.project()
}

func validProjectPath(p string) bool {
	if p == "" || len(p) > 2048 {
		return false
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// MergeRequest is a merge request as the list reports it (FR22).
type MergeRequest struct {
	IID       int
	Title     string
	Author    string // "unknown" when GitLab has no author
	Branch    string // source branch
	Base      string // target branch
	HeadSHA   string // "" when GitLab reports none; FR22 skips such an MR
	Status    string // open | closed | merged
	OpenedAt  time.Time
	UpdatedAt time.Time
	MergedAt  *time.Time
	ClosedAt  *time.Time
	Draft     bool
	// HeadIsFork is true when the source project differs from the target project, and also when
	// either id is missing: anything that cannot be shown to be the same project is a fork (SCR1).
	HeadIsFork bool
	WebURL     string
}

// MergeRequestDetail is a single merge request with its description.
type MergeRequestDetail struct {
	MergeRequest
	Description *string
}

type apiMR struct {
	IID             int        `json:"iid"`
	Title           string     `json:"title"`
	Description     *string    `json:"description"`
	State           string     `json:"state"`
	Draft           bool       `json:"draft"`
	SourceBranch    string     `json:"source_branch"`
	TargetBranch    string     `json:"target_branch"`
	SHA             string     `json:"sha"`
	SourceProjectID *int64     `json:"source_project_id"`
	TargetProjectID *int64     `json:"target_project_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	MergedAt        *time.Time `json:"merged_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	WebURL          string     `json:"web_url"`
	Author          *struct {
		Username string `json:"username"`
	} `json:"author"`
}

func mapState(state string) string {
	switch state {
	case "merged":
		return "merged"
	case "closed", "locked":
		return "closed"
	default: // opened
		return "open"
	}
}

func (m apiMR) summary() (MergeRequest, error) {
	if m.IID <= 0 {
		return MergeRequest{}, fmt.Errorf("gitlab merge request: %w", ErrNotAPI)
	}
	author := "unknown"
	if m.Author != nil && m.Author.Username != "" {
		author = m.Author.Username
	}
	fork := m.SourceProjectID == nil || m.TargetProjectID == nil ||
		*m.SourceProjectID <= 0 || *m.TargetProjectID <= 0 ||
		*m.SourceProjectID != *m.TargetProjectID
	return MergeRequest{
		IID:        m.IID,
		Title:      m.Title,
		Author:     author,
		Branch:     m.SourceBranch,
		Base:       m.TargetBranch,
		HeadSHA:    m.SHA,
		Status:     mapState(m.State),
		OpenedAt:   m.CreatedAt,
		UpdatedAt:  m.UpdatedAt,
		MergedAt:   m.MergedAt,
		ClosedAt:   m.ClosedAt,
		Draft:      m.Draft,
		HeadIsFork: fork,
		WebURL:     m.WebURL,
	}, nil
}

func projectPath(id int64) (string, error) {
	if id <= 0 {
		return "", fmt.Errorf("gitlab: project id %d out of range", id)
	}
	return "/projects/" + strconv.FormatInt(id, 10), nil
}

// pageQuery is the paging query for page numbers; GitLab's own next-page links are not read.
func pageQuery(page, perPage int, q url.Values) (url.Values, error) {
	if page < 1 || page > MaxPage || perPage < 1 || perPage > MaxPerPage {
		return nil, fmt.Errorf("gitlab: page %d and per_page %d out of range (page 1..%d, per_page 1..%d)",
			page, perPage, MaxPage, MaxPerPage)
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	return q, nil
}

// MergeRequestsPage calls GET /projects/:id/merge_requests with state=all, order_by=updated_at,
// sort=desc for page `page` (1-based) of `perPage` (1..100; FR21 uses 50). A page shorter than
// perPage is the last one.
func (c *Client) MergeRequestsPage(ctx context.Context, conn Conn, projectID int64, page, perPage int) ([]MergeRequest, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return nil, err
	}
	q, err := pageQuery(page, perPage, url.Values{"state": {"all"}, "order_by": {"updated_at"}, "sort": {"desc"}})
	if err != nil {
		return nil, err
	}
	raw, err := getJSON[[]apiMR](ctx, c, conn, "merge_requests", base+"/merge_requests", q)
	if err != nil {
		return nil, err
	}
	out := make([]MergeRequest, len(raw))
	for i, m := range raw {
		if out[i], err = m.summary(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MergeRequest calls GET /projects/:id/merge_requests/:iid.
func (c *Client) MergeRequest(ctx context.Context, conn Conn, projectID int64, iid int) (MergeRequestDetail, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return MergeRequestDetail{}, err
	}
	if iid <= 0 {
		return MergeRequestDetail{}, fmt.Errorf("gitlab: merge request iid %d out of range", iid)
	}
	raw, err := getJSON[apiMR](ctx, c, conn, "merge_request", base+"/merge_requests/"+strconv.Itoa(iid), nil)
	if err != nil {
		return MergeRequestDetail{}, err
	}
	mr, err := raw.summary()
	if err != nil {
		return MergeRequestDetail{}, err
	}
	if mr.IID != iid {
		return MergeRequestDetail{}, fmt.Errorf("gitlab merge request: %w", ErrNotAPI)
	}
	return MergeRequestDetail{MergeRequest: mr, Description: raw.Description}, nil
}

// File is one changed file of a merge request. Patch is nil when GitLab gives none (binary,
// collapsed, too large or empty diff); the counts are then 0.
type File struct {
	Path      string
	Additions int
	Deletions int
	Patch     *string
}

type apiDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	DeletedFile bool   `json:"deleted_file"`
	Collapsed   bool   `json:"collapsed"`
	TooLarge    bool   `json:"too_large"`
}

func (d apiDiff) file() (File, bool) {
	path := d.NewPath
	if d.DeletedFile || path == "" {
		path = d.OldPath
	}
	if path == "" {
		return File{}, false
	}
	f := File{Path: path}
	if d.Collapsed || d.TooLarge || d.Diff == "" {
		return f, true
	}
	patch := d.Diff
	f.Patch = &patch
	f.Additions, f.Deletions = countLines(d.Diff)
	return f, true
}

// countLines counts the added and removed lines of a diff: lines starting with + or -, hunk
// headers (@@) and "\ No newline" markers excluded. GitLab diffs carry no ---/+++ file header.
func countLines(diff string) (added, removed int) {
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

// Diffs calls GET /projects/:id/merge_requests/:iid/diffs page by page (100 per page) until a
// short page or MaxFiles entries, within the detail timeout. Entries with the same path are
// merged into one.
func (c *Client) Diffs(ctx context.Context, conn Conn, projectID int64, iid int) ([]File, error) {
	path, err := mrPath(projectID, iid, "/diffs")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.detailTimeout)
	defer cancel()
	raw, err := paginateCapped[apiDiff](ctx, c, conn, "diffs", path, MaxFiles)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(raw))
	for _, d := range raw {
		if f, ok := d.file(); ok {
			files = append(files, f)
		}
	}
	return mergeSamePath(files), nil
}

// Commit is one commit of a merge request.
type Commit struct {
	SHA         string
	Message     string
	Author      string // "unknown" when GitLab has no author name
	CommittedAt *time.Time
}

type apiCommit struct {
	ID            string     `json:"id"`
	Message       string     `json:"message"`
	AuthorName    string     `json:"author_name"`
	CommittedDate *time.Time `json:"committed_date"`
}

// Commits calls GET /projects/:id/merge_requests/:iid/commits page by page (100 per page) until
// a short page or MaxCommits entries, within the detail timeout, in the order GitLab lists them.
func (c *Client) Commits(ctx context.Context, conn Conn, projectID int64, iid int) ([]Commit, error) {
	path, err := mrPath(projectID, iid, "/commits")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.detailTimeout)
	defer cancel()
	raw, err := paginateCapped[apiCommit](ctx, c, conn, "commits", path, MaxCommits)
	if err != nil {
		return nil, err
	}
	out := make([]Commit, len(raw))
	for i, cm := range raw {
		author := cm.AuthorName
		if author == "" {
			author = "unknown"
		}
		out[i] = Commit{SHA: cm.ID, Message: cm.Message, Author: author, CommittedAt: cm.CommittedDate}
	}
	return out, nil
}

func mrPath(projectID int64, iid int, suffix string) (string, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return "", err
	}
	if iid <= 0 {
		return "", fmt.Errorf("gitlab: merge request iid %d out of range", iid)
	}
	return base + "/merge_requests/" + strconv.Itoa(iid) + suffix, nil
}

// paginateCapped reads pages of MaxPerPage by page number until a short page or max items.
func paginateCapped[T any](ctx context.Context, c *Client, conn Conn, op, path string, max int) ([]T, error) {
	var out []T
	for page := 1; len(out) < max; page++ {
		q, err := pageQuery(page, MaxPerPage, nil)
		if err != nil {
			return nil, err
		}
		items, err := getJSON[[]T](ctx, c, conn, op, path, q)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) < MaxPerPage {
			break
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// mergeSamePath folds entries with the same path into one, in first-seen order: counts are
// summed and non-empty patches joined with a newline.
func mergeSamePath(files []File) []File {
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
			joined := strings.Join(patches, "\n")
			prev.Patch = &joined
		}
	}
	return out
}

// Issue is a GitLab issue (FR25).
type Issue struct {
	Number int // iid
	Title  string
	Body   *string
	State  string // open | closed
}

// Issue calls GET /projects/:id/issues/:iid.
func (c *Client) Issue(ctx context.Context, conn Conn, projectID int64, iid int) (Issue, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return Issue{}, err
	}
	if iid <= 0 {
		return Issue{}, fmt.Errorf("gitlab: issue iid %d out of range", iid)
	}
	raw, err := getJSON[struct {
		IID         int     `json:"iid"`
		Title       string  `json:"title"`
		Description *string `json:"description"`
		State       string  `json:"state"`
	}](ctx, c, conn, "issue", base+"/issues/"+strconv.Itoa(iid), nil)
	if err != nil {
		return Issue{}, err
	}
	if raw.IID != iid {
		return Issue{}, fmt.Errorf("gitlab issue: %w", ErrNotAPI)
	}
	state := "closed"
	if raw.State == "opened" {
		state = "open"
	}
	return Issue{Number: raw.IID, Title: raw.Title, Body: raw.Description, State: state}, nil
}
