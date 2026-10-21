package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ReviewComment is a pull request review comment, normalized from GitHub's payload.
type ReviewComment struct {
	ID           int64
	Path         string
	Line         *int // nil once the comment can no longer be placed on the diff
	OriginalLine *int
	Side         string // LEFT only if GitHub says LEFT, otherwise RIGHT
	Body         string
	User         string // login, or "unknown"
	CreatedAt    time.Time
	HTMLURL      string
	InReplyToID  *int64
	IsOutdated   bool // Line == nil
}

// CommentInput describes a comment to create. With InReplyTo set, the comment
// is a reply to that comment id and CommitID, Path, Line and Side are ignored.
type CommentInput struct {
	CommitID  string
	Path      string
	Line      int
	Side      string // LEFT | RIGHT, default RIGHT
	Body      string
	InReplyTo *int64
}

type apiReviewComment struct {
	ID           int64     `json:"id"`
	Path         string    `json:"path"`
	Line         *int      `json:"line"`
	OriginalLine *int      `json:"original_line"`
	Side         *string   `json:"side"`
	Body         string    `json:"body"`
	User         *apiUser  `json:"user"`
	CreatedAt    time.Time `json:"created_at"`
	HTMLURL      string    `json:"html_url"`
	InReplyToID  *int64    `json:"in_reply_to_id"`
}

func (c apiReviewComment) normalize() ReviewComment {
	side := "RIGHT"
	if c.Side != nil && *c.Side == "LEFT" {
		side = "LEFT"
	}
	return ReviewComment{
		ID:           c.ID,
		Path:         c.Path,
		Line:         c.Line,
		OriginalLine: c.OriginalLine,
		Side:         side,
		Body:         c.Body,
		User:         login(c.User),
		CreatedAt:    c.CreatedAt,
		HTMLURL:      c.HTMLURL,
		InReplyToID:  c.InReplyToID,
		IsOutdated:   c.Line == nil,
	}
}

// ListReviewComments returns one page (up to 100) of the pull request's review
// comments in GitHub's order.
func (c *Client) ListReviewComments(ctx context.Context, owner, name string, number int) ([]ReviewComment, error) {
	token, err := c.currentToken()
	if err != nil {
		return nil, err
	}
	path := repoPath(owner, name) + "/pulls/" + strconv.Itoa(number) + "/comments"
	raw, err := getJSON[[]apiReviewComment](ctx, c, token, path, url.Values{"per_page": {"100"}})
	if err != nil {
		return nil, err
	}
	out := make([]ReviewComment, len(raw))
	for i, rc := range raw {
		out[i] = rc.normalize()
	}
	return out, nil
}

// CreateReviewComment posts a review comment or a reply. It is never retried:
// a repeated POST could create a duplicate comment.
func (c *Client) CreateReviewComment(ctx context.Context, owner, name string, number int, in CommentInput) (ReviewComment, error) {
	token, err := c.currentToken()
	if err != nil {
		return ReviewComment{}, err
	}
	base := repoPath(owner, name) + "/pulls/" + strconv.Itoa(number) + "/comments"
	var path string
	var body map[string]any
	if in.InReplyTo != nil {
		path = base + "/" + strconv.FormatInt(*in.InReplyTo, 10) + "/replies"
		body = map[string]any{"body": in.Body}
	} else {
		side := in.Side
		if side == "" {
			side = "RIGHT"
		}
		path = base
		body = map[string]any{
			"body":      in.Body,
			"commit_id": in.CommitID,
			"path":      in.Path,
			"line":      in.Line,
			"side":      side,
		}
	}
	var created apiReviewComment
	if err := c.do(ctx, token, http.MethodPost, path, nil, body, &created); err != nil {
		return ReviewComment{}, err
	}
	return created.normalize(), nil
}
