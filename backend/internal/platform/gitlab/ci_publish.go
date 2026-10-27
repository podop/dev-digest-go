package gitlab

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dev-digest-go/backend/internal/platform/retry"
)

// Limits of the CI publish calls (spec C FR11, FR12).
const (
	// MaxCommitActions bounds the files of one commit; an export commits a handful.
	MaxCommitActions = 100
	// maxOpenMRs is the per_page of the open-MR lookup; FR12 takes the first result.
	maxOpenMRs = 20
)

// Branch is a repository branch and the commit it points at.
type Branch struct {
	Name    string
	HeadSHA string
}

// Branch calls GET /projects/:id/repository/branches/<URL-encoded name> (spec C FR11 step 3).
// found is false on a 404: the branch does not exist (a missing project answers 404 too; the
// caller has just read the project, FR11 step 1).
func (c *Client) Branch(ctx context.Context, conn Conn, projectID int64, name string) (b Branch, found bool, err error) {
	base, err := projectPath(projectID)
	if err != nil {
		return Branch{}, false, err
	}
	if !validBranchName(name) {
		return Branch{}, false, fmt.Errorf("gitlab branch: invalid branch name")
	}
	raw, err := getJSON[struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}](ctx, c, conn, "branch", base+"/repository/branches/"+url.PathEscape(name), nil)
	if err != nil {
		if se, ok := asStatus(err); ok && se.IsNotFound() {
			return Branch{}, false, nil
		}
		return Branch{}, false, err
	}
	if !validCommitSHA(raw.Commit.ID) {
		return Branch{}, false, fmt.Errorf("gitlab branch: %w", ErrNotAPI)
	}
	return Branch{Name: raw.Name, HeadSHA: raw.Commit.ID}, true, nil
}

// FileMeta is the metadata of one repository file at a ref, from the headers of a HEAD request.
type FileMeta struct {
	// SHA256 is the SHA-256 of the file content as lower-case hex (header X-Gitlab-Content-Sha256).
	SHA256 string
	// Size is the content size in bytes (header X-Gitlab-Size); 0 when GitLab sends none.
	Size int64
}

// FileMeta calls HEAD /projects/:id/repository/files/<URL-encoded file>?ref=<ref> and reads the
// content hash from its headers: no file body is transferred (spec C FR11 step 4, A2). found is
// false on a 404 (file or ref absent). A 2xx answer without a valid hash header is ErrNotAPI.
func (c *Client) FileMeta(ctx context.Context, conn Conn, projectID int64, file, ref string) (m FileMeta, found bool, err error) {
	base, err := projectPath(projectID)
	if err != nil {
		return FileMeta{}, false, err
	}
	if !validProjectPath(file) || !validRef(ref) {
		return FileMeta{}, false, fmt.Errorf("gitlab file_meta: invalid file or ref")
	}
	path := base + "/repository/files/" + url.PathEscape(file)
	h, err := c.head(ctx, conn, "file_meta", path, url.Values{"ref": {ref}})
	if err != nil {
		if se, ok := asStatus(err); ok && se.IsNotFound() {
			return FileMeta{}, false, nil
		}
		return FileMeta{}, false, err
	}
	sum := strings.ToLower(strings.TrimSpace(h.Get("X-Gitlab-Content-Sha256")))
	if b, derr := hex.DecodeString(sum); derr != nil || len(b) != 32 {
		return FileMeta{}, false, fmt.Errorf("gitlab file_meta: %w", ErrNotAPI)
	}
	m = FileMeta{SHA256: sum}
	if n, perr := strconv.ParseInt(strings.TrimSpace(h.Get("X-Gitlab-Size")), 10, 64); perr == nil && n >= 0 {
		m.Size = n
	}
	return m, true, nil
}

// CommitAction is one file of a commit. Action is "create" or "update"; Content is sent as text.
type CommitAction struct {
	Action   string
	FilePath string
	Content  string
}

// CommitInput is one Commits-API commit (spec C FR11 step 5).
type CommitInput struct {
	// Branch is the branch to commit to.
	Branch string
	// StartBranch is the branch to create Branch from when Branch does not exist; "" otherwise.
	StartBranch string
	Message     string
	Actions     []CommitAction
}

// Commit calls POST /projects/:id/repository/commits with every action as `encoding: text` (spec C
// A3) and returns the new commit's SHA. It is a write: exactly one attempt, never retried. Invalid
// input (no action, an unknown action, a bad path, content that is not UTF-8) is refused without
// a request.
func (c *Client) Commit(ctx context.Context, conn Conn, projectID int64, in CommitInput) (sha string, err error) {
	base, err := projectPath(projectID)
	if err != nil {
		return "", err
	}
	if !validBranchName(in.Branch) || (in.StartBranch != "" && !validBranchName(in.StartBranch)) ||
		strings.TrimSpace(in.Message) == "" || len(in.Actions) == 0 || len(in.Actions) > MaxCommitActions {
		return "", fmt.Errorf("gitlab commit: invalid branch, message or action count")
	}
	type actionBody struct {
		Action   string `json:"action"`
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	body := struct {
		Branch      string       `json:"branch"`
		StartBranch string       `json:"start_branch,omitempty"`
		Message     string       `json:"commit_message"`
		Actions     []actionBody `json:"actions"`
	}{Branch: in.Branch, StartBranch: in.StartBranch, Message: in.Message, Actions: make([]actionBody, len(in.Actions))}
	for i, a := range in.Actions {
		if (a.Action != "create" && a.Action != "update") || !validProjectPath(a.FilePath) || !utf8.ValidString(a.Content) {
			return "", fmt.Errorf("gitlab commit: invalid action %d", i)
		}
		body.Actions[i] = actionBody{Action: a.Action, FilePath: a.FilePath, Content: a.Content, Encoding: "text"}
	}
	out, err := postOnce[struct {
		ID string `json:"id"`
	}](ctx, c, conn, "commit", base+"/repository/commits", body)
	if err != nil {
		return "", err
	}
	if !validCommitSHA(out.ID) {
		return "", fmt.Errorf("gitlab commit: %w", ErrNotAPI)
	}
	return out.ID, nil
}

// OpenMRs calls GET /projects/:id/merge_requests?state=opened&source_branch=&target_branch=&
// order_by=created_at&sort=asc, the first page (spec C FR12: the first result is reused).
func (c *Client) OpenMRs(ctx context.Context, conn Conn, projectID int64, sourceBranch, targetBranch string) ([]MergeRequest, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return nil, err
	}
	if !validRef(sourceBranch) || !validRef(targetBranch) {
		return nil, fmt.Errorf("gitlab open_merge_requests: invalid branch")
	}
	q, err := pageQuery(1, maxOpenMRs, url.Values{
		"state": {"opened"}, "source_branch": {sourceBranch}, "target_branch": {targetBranch},
		"order_by": {"created_at"}, "sort": {"asc"},
	})
	if err != nil {
		return nil, err
	}
	raw, err := getJSON[[]apiMR](ctx, c, conn, "open_merge_requests", base+"/merge_requests", q)
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

// CreateMRInput is a new merge request (spec C FR12).
type CreateMRInput struct {
	SourceBranch       string
	TargetBranch       string
	Title              string
	Description        string
	RemoveSourceBranch bool
}

// CreateMR calls POST /projects/:id/merge_requests. It is a write: exactly one attempt. A 409
// (an MR for the branches opened meanwhile) is a *StatusError the caller answers by listing again.
func (c *Client) CreateMR(ctx context.Context, conn Conn, projectID int64, in CreateMRInput) (MergeRequest, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return MergeRequest{}, err
	}
	if !validRef(in.SourceBranch) || !validRef(in.TargetBranch) || strings.TrimSpace(in.Title) == "" ||
		!utf8.ValidString(in.Title) || !utf8.ValidString(in.Description) {
		return MergeRequest{}, fmt.Errorf("gitlab create_merge_request: invalid branch, title or description")
	}
	raw, err := postOnce[apiMR](ctx, c, conn, "create_merge_request", base+"/merge_requests", struct {
		SourceBranch       string `json:"source_branch"`
		TargetBranch       string `json:"target_branch"`
		Title              string `json:"title"`
		Description        string `json:"description"`
		RemoveSourceBranch bool   `json:"remove_source_branch"`
	}{in.SourceBranch, in.TargetBranch, in.Title, in.Description, in.RemoveSourceBranch})
	if err != nil {
		return MergeRequest{}, err
	}
	return raw.summary()
}

// validBranchName accepts a ref-like name that is not "." or "..": it is sent as one escaped path
// segment, and a dot segment must not reach the server unescaped.
func validBranchName(name string) bool {
	return validRef(name) && name != "." && name != ".."
}

// validCommitSHA accepts a full SHA-1 or SHA-256 commit id in lower-case hex.
func validCommitSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

// exchange performs one attempt of a request that carries no JSON: it sends it with hc, maps a
// transport failure (request URL and token removed) and a non-2xx, non-3xx answer to errors, and
// hands a 2xx or 3xx response to read while its context is alive. token may be empty (no header).
// It writes the one log line of the attempt.
func (c *Client) exchange(ctx context.Context, hc *http.Client, op, method, target, host, token string, attempt int, read func(*http.Response) error) (err error) {
	start := time.Now()
	status := 0
	defer func() {
		c.logAttempt(host, op, status, err, attempt, time.Since(start))
	}()
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return fmt.Errorf("gitlab %s: %w", op, ErrBaseURL)
	}
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "dev-digest-go")
	resp, err := hc.Do(req)
	if err != nil {
		// net/http parses a redirect's Location itself, before any CheckRedirect, and quotes an
		// unparsable one in its error: for the signed storage URL that is a credential.
		if strings.Contains(err.Error(), "failed to parse Location header") {
			return fmt.Errorf("gitlab %s: %w", op, ErrRedirect)
		}
		return transportError(op, err, token)
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		return statusError(resp, token)
	}
	return read(resp)
}

// head sends HEAD to the connection and returns the headers of the 2xx answer, retrying
// transient failures like a read. A redirect is ErrRedirect.
func (c *Client) head(ctx context.Context, conn Conn, op, path string, q url.Values) (http.Header, error) {
	tok, err := conn.token()
	if err != nil {
		return nil, err
	}
	target, host, err := conn.target(path, q)
	if err != nil {
		return nil, err
	}
	attempt := 0
	return retry.Value(ctx, c.policy(), func(ctx context.Context) (http.Header, error) {
		attempt++
		var h http.Header
		err := c.exchange(ctx, c.http, op, http.MethodHead, target, host, tok, attempt, func(resp *http.Response) error {
			if resp.StatusCode >= 300 {
				return fmt.Errorf("gitlab %s: %w", op, ErrRedirect)
			}
			h = resp.Header.Clone()
			return nil
		})
		return h, err
	})
}

// asStatus finds a *StatusError in err's chain.
func asStatus(err error) (*StatusError, bool) {
	var se *StatusError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}
