package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Version is one diff version of a merge request: the commits its diff was computed between.
type Version struct {
	ID       int64
	BaseSHA  string
	StartSHA string
	HeadSHA  string
}

type apiVersion struct {
	ID             int64  `json:"id"`
	BaseCommitSHA  string `json:"base_commit_sha"`
	StartCommitSHA string `json:"start_commit_sha"`
	HeadCommitSHA  string `json:"head_commit_sha"`
}

// Versions calls GET /projects/:id/merge_requests/:iid/versions with per_page=1 (spec B FR7, A2).
// GitLab lists the newest version first, so the result has at most one entry: the latest. It is
// empty while GitLab has not prepared a diff yet.
func (c *Client) Versions(ctx context.Context, conn Conn, projectID int64, iid int) ([]Version, error) {
	path, err := mrPath(projectID, iid, "/versions")
	if err != nil {
		return nil, err
	}
	q, err := pageQuery(1, 1, nil)
	if err != nil {
		return nil, err
	}
	raw, err := getJSON[[]apiVersion](ctx, c, conn, "versions", path, q)
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(raw))
	for _, v := range raw {
		out = append(out, Version{ID: v.ID, BaseSHA: v.BaseCommitSHA, StartSHA: v.StartCommitSHA, HeadSHA: v.HeadCommitSHA})
	}
	return out, nil
}

// MergedMergeRequest is a merged merge request as the prior-MRs read needs it (spec B FR24).
type MergedMergeRequest struct {
	IID   int
	Title string
	// Author is the username, "unknown" when GitLab has no author.
	Author string
	// MergedAt is the timestamp exactly as GitLab sent it; "" when it sent null (A8).
	MergedAt string
}

// MergedMergeRequests calls GET /projects/:id/merge_requests?state=merged&order_by=updated_at&
// sort=desc&per_page=<perPage> and reads that one page (perPage 1..100). The caller reads the
// files of each with DiffPaths.
func (c *Client) MergedMergeRequests(ctx context.Context, conn Conn, projectID int64, perPage int) ([]MergedMergeRequest, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return nil, err
	}
	q, err := pageQuery(1, perPage, url.Values{"state": {"merged"}, "order_by": {"updated_at"}, "sort": {"desc"}})
	if err != nil {
		return nil, err
	}
	raw, err := getJSON[[]struct {
		IID      int     `json:"iid"`
		Title    string  `json:"title"`
		MergedAt *string `json:"merged_at"`
		Author   *struct {
			Username string `json:"username"`
		} `json:"author"`
	}](ctx, c, conn, "merged_merge_requests", base+"/merge_requests", q)
	if err != nil {
		return nil, err
	}
	out := make([]MergedMergeRequest, len(raw))
	for i, m := range raw {
		if m.IID <= 0 {
			return nil, fmt.Errorf("gitlab merged merge requests: %w", ErrNotAPI)
		}
		out[i] = MergedMergeRequest{IID: m.IID, Title: m.Title, Author: "unknown"}
		if m.Author != nil && m.Author.Username != "" {
			out[i].Author = m.Author.Username
		}
		if m.MergedAt != nil {
			out[i].MergedAt = *m.MergedAt
		}
	}
	return out, nil
}

// DiffPaths calls GET /projects/:id/merge_requests/:iid/diffs?per_page=100&page=1 and returns
// the distinct paths of that first page in first-seen order: each entry's new_path, its old_path
// for a deleted file (spec B FR24). It does not read further pages.
func (c *Client) DiffPaths(ctx context.Context, conn Conn, projectID int64, iid int) ([]string, error) {
	path, err := mrPath(projectID, iid, "/diffs")
	if err != nil {
		return nil, err
	}
	q, err := pageQuery(1, MaxPerPage, nil)
	if err != nil {
		return nil, err
	}
	raw, err := getJSON[[]apiDiff](ctx, c, conn, "diff_paths", path, q)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, d := range raw {
		p := d.NewPath
		if d.DeletedFile || p == "" {
			p = d.OldPath
		}
		if _, dup := seen[p]; p == "" || dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

// RawFile calls GET /projects/<project>/repository/files/<URL-encoded file>/raw?ref=<ref> and
// returns the file's bytes. project is a numeric project id or a full project path
// ("group/sub/name"; it is URL-encoded here). At most limit bytes are accepted: a longer file is
// ErrTooLarge and none of it is returned, a missing file is a *StatusError with Status 404
// (spec B FR29, FR30). The ref and the file are sent as given; an empty or control-character
// value, or a project or file path with an empty, "." or ".." segment, is refused without a
// request.
func (c *Client) RawFile(ctx context.Context, conn Conn, project, file, ref string, limit int) ([]byte, error) {
	if !validProjectPath(project) || !validProjectPath(file) || !validRef(ref) {
		return nil, fmt.Errorf("gitlab raw_file: invalid project, file or ref")
	}
	// A numeric id keeps its plain form; a path is escaped as one segment (slashes as %2F).
	if _, err := strconv.ParseInt(project, 10, 64); err != nil {
		project = url.PathEscape(project)
	}
	path := "/projects/" + project + "/repository/files/" + url.PathEscape(file) + "/raw"
	return getRaw(ctx, c, conn, "raw_file", path, url.Values{"ref": {ref}}, limit)
}

// validRef accepts a non-empty ref (branch, tag or SHA) without control characters. It travels
// as a query value, so no other character can change the path.
func validRef(ref string) bool {
	if ref == "" || len(ref) > 1024 {
		return false
	}
	return !strings.ContainsFunc(ref, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
