package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// TreeEntry is one blob of a new tree (mode 100644): either inline Content or the SHA of an
// existing blob.
type TreeEntry struct {
	Path    string
	Content *string
	SHA     string
}

// CommitInfo is a Git commit as far as a publish needs it.
type CommitInfo struct {
	SHA     string
	TreeSHA string
}

// PullRef is a pull request found or created by a publish.
type PullRef struct {
	Number  int
	HTMLURL string
}

// NewPull is the body of a pull request creation.
type NewPull struct {
	Title string
	Head  string
	Base  string
	Body  string
}

// treeBody and treeNode are the wire shape of POST /git/trees.
type treeBody struct {
	BaseTree string     `json:"base_tree"`
	Tree     []treeNode `json:"tree"`
}

type treeNode struct {
	Path    string  `json:"path"`
	Mode    string  `json:"mode"`
	Type    string  `json:"type"`
	Content *string `json:"content,omitempty"`
	SHA     string  `json:"sha,omitempty"`
}

// shaResponse is the part of a Git Data answer a publish reads.
type shaResponse struct {
	SHA    string `json:"sha"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
}

// branchPath returns the repository path of refs/heads/<branch> under prefix ("ref" or "refs");
// the slashes of a branch name stay.
func branchPath(owner, name, prefix, branch string) string {
	return repoPath(owner, name) + "/git/" + prefix + "/heads/" + escapePath(branch)
}

// GetRef reads refs/heads/<branch>: its commit SHA, found false when the branch is absent (404).
func (c *Client) GetRef(ctx context.Context, owner, name, branch string) (sha string, found bool, err error) {
	token, err := c.currentToken()
	if err != nil {
		return "", false, err
	}
	out, err := getJSON[shaResponse](ctx, c.noRedirect(), token, branchPath(owner, name, "ref", branch), nil)
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Status == http.StatusNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	if out.Object.SHA == "" {
		return "", false, errors.New("github: ref response without a commit sha")
	}
	return out.Object.SHA, true, nil
}

// GetCommit reads a commit's tree.
func (c *Client) GetCommit(ctx context.Context, owner, name, sha string) (CommitInfo, error) {
	token, err := c.currentToken()
	if err != nil {
		return CommitInfo{}, err
	}
	out, err := getJSON[shaResponse](ctx, c.noRedirect(), token, repoPath(owner, name)+"/git/commits/"+url.PathEscape(sha), nil)
	if err != nil {
		return CommitInfo{}, err
	}
	if out.SHA == "" || out.Tree.SHA == "" {
		return CommitInfo{}, errors.New("github: commit response without a sha or tree")
	}
	return CommitInfo{SHA: out.SHA, TreeSHA: out.Tree.SHA}, nil
}

// CreateBlob stores base64 content as a blob and returns its SHA. Not retried.
func (c *Client) CreateBlob(ctx context.Context, owner, name, contentBase64 string) (string, error) {
	body := struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}{contentBase64, "base64"}
	return c.createSHA(ctx, repoPath(owner, name)+"/git/blobs", body)
}

// CreateTree creates a tree on top of baseTree and returns its SHA. Not retried.
func (c *Client) CreateTree(ctx context.Context, owner, name, baseTree string, entries []TreeEntry) (string, error) {
	nodes := make([]treeNode, 0, len(entries))
	for _, e := range entries {
		if (e.Content == nil) == (e.SHA == "") {
			return "", fmt.Errorf("github: tree entry %q needs exactly one of content and sha", e.Path)
		}
		nodes = append(nodes, treeNode{Path: e.Path, Mode: "100644", Type: "blob", Content: e.Content, SHA: e.SHA})
	}
	return c.createSHA(ctx, repoPath(owner, name)+"/git/trees", treeBody{BaseTree: baseTree, Tree: nodes})
}

// CreateCommit creates a commit and returns its SHA. Not retried.
func (c *Client) CreateCommit(ctx context.Context, owner, name, message, tree string, parents []string) (string, error) {
	if parents == nil {
		parents = []string{}
	}
	body := struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}{message, tree, parents}
	return c.createSHA(ctx, repoPath(owner, name)+"/git/commits", body)
}

// UpdateRef force-moves refs/heads/<branch> to sha. Not retried.
func (c *Client) UpdateRef(ctx context.Context, owner, name, branch, sha string) error {
	token, err := c.currentToken()
	if err != nil {
		return err
	}
	body := struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}{sha, true}
	return c.noRedirect().do(ctx, token, http.MethodPatch, branchPath(owner, name, "refs", branch), nil, body, nil)
}

// CreateRef creates refs/heads/<branch> at sha. Not retried.
func (c *Client) CreateRef(ctx context.Context, owner, name, branch, sha string) error {
	token, err := c.currentToken()
	if err != nil {
		return err
	}
	body := struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}{"refs/heads/" + branch, sha}
	return c.noRedirect().do(ctx, token, http.MethodPost, repoPath(owner, name)+"/git/refs", nil, body, nil)
}

// pullRefJSON is the part of a pull request answer a publish reads.
type pullRefJSON struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// FindOpenPull finds the open pull request whose head is <owner>:<branch>; found false when none.
func (c *Client) FindOpenPull(ctx context.Context, owner, name, branch string) (pr PullRef, found bool, err error) {
	token, err := c.currentToken()
	if err != nil {
		return PullRef{}, false, err
	}
	q := url.Values{"state": {"open"}, "head": {owner + ":" + branch}, "per_page": {"1"}}
	list, err := getJSON[[]pullRefJSON](ctx, c.noRedirect(), token, repoPath(owner, name)+"/pulls", q)
	if err != nil {
		return PullRef{}, false, err
	}
	if len(list) == 0 {
		return PullRef{}, false, nil
	}
	return PullRef{Number: list[0].Number, HTMLURL: list[0].HTMLURL}, true, nil
}

// CreatePull opens a pull request. Not retried.
func (c *Client) CreatePull(ctx context.Context, owner, name string, in NewPull) (PullRef, error) {
	token, err := c.currentToken()
	if err != nil {
		return PullRef{}, err
	}
	body := struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}{in.Title, in.Head, in.Base, in.Body}
	var out pullRefJSON
	if err := c.noRedirect().do(ctx, token, http.MethodPost, repoPath(owner, name)+"/pulls", nil, body, &out); err != nil {
		return PullRef{}, err
	}
	return PullRef{Number: out.Number, HTMLURL: out.HTMLURL}, nil
}

// createSHA POSTs body once (no retry) and returns the sha of the created object.
func (c *Client) createSHA(ctx context.Context, path string, body any) (string, error) {
	token, err := c.currentToken()
	if err != nil {
		return "", err
	}
	var out shaResponse
	if err := c.noRedirect().do(ctx, token, http.MethodPost, path, nil, body, &out); err != nil {
		return "", err
	}
	if out.SHA == "" {
		return "", errors.New("github: create response without a sha")
	}
	return out.SHA, nil
}
