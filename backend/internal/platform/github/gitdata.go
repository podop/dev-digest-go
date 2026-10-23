package github

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by the Git Data, pull request and Actions operations until
// they are implemented.
var ErrNotImplemented = errors.New("github: operation not implemented")

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

// GetRef reads refs/heads/<branch>: its commit SHA, found false when the branch is absent (404).
func (c *Client) GetRef(ctx context.Context, owner, name, branch string) (sha string, found bool, err error) {
	return "", false, ErrNotImplemented
}

// GetCommit reads a commit's tree.
func (c *Client) GetCommit(ctx context.Context, owner, name, sha string) (CommitInfo, error) {
	return CommitInfo{}, ErrNotImplemented
}

// CreateBlob stores base64 content as a blob and returns its SHA. Not retried.
func (c *Client) CreateBlob(ctx context.Context, owner, name, contentBase64 string) (string, error) {
	return "", ErrNotImplemented
}

// CreateTree creates a tree on top of baseTree and returns its SHA. Not retried.
func (c *Client) CreateTree(ctx context.Context, owner, name, baseTree string, entries []TreeEntry) (string, error) {
	return "", ErrNotImplemented
}

// CreateCommit creates a commit and returns its SHA. Not retried.
func (c *Client) CreateCommit(ctx context.Context, owner, name, message, tree string, parents []string) (string, error) {
	return "", ErrNotImplemented
}

// UpdateRef force-moves refs/heads/<branch> to sha. Not retried.
func (c *Client) UpdateRef(ctx context.Context, owner, name, branch, sha string) error {
	return ErrNotImplemented
}

// CreateRef creates refs/heads/<branch> at sha. Not retried.
func (c *Client) CreateRef(ctx context.Context, owner, name, branch, sha string) error {
	return ErrNotImplemented
}

// FindOpenPull finds the open pull request whose head is <owner>:<branch>; found false when none.
func (c *Client) FindOpenPull(ctx context.Context, owner, name, branch string) (pr PullRef, found bool, err error) {
	return PullRef{}, false, ErrNotImplemented
}

// CreatePull opens a pull request. Not retried.
func (c *Client) CreatePull(ctx context.Context, owner, name string, in NewPull) (PullRef, error) {
	return PullRef{}, ErrNotImplemented
}
