package glapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// MR reads the merge request: GET /projects/:id/merge_requests/:iid (FR25).
func (c *Client) MR(ctx context.Context, token string, ref MRRef) (MergeRequest, error) {
	base, err := mrPath(ref, whatReview)
	if err != nil {
		return MergeRequest{}, err
	}
	raw, err := c.call(ctx, token, request{method: http.MethodGet, path: base, what: whatReview})
	if err != nil {
		return MergeRequest{}, err
	}
	var doc struct {
		IID      int `json:"iid"`
		DiffRefs *struct {
			BaseSHA  string `json:"base_sha"`
			StartSHA string `json:"start_sha"`
			HeadSHA  string `json:"head_sha"`
		} `json:"diff_refs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return MergeRequest{}, badBody(whatReview, c.prefix+base)
	}
	mr := MergeRequest{IID: doc.IID}
	if r := doc.DiffRefs; r != nil && r.BaseSHA != "" && r.StartSHA != "" && r.HeadSHA != "" {
		mr.DiffRefs = &DiffRefs{BaseSHA: r.BaseSHA, StartSHA: r.StartSHA, HeadSHA: r.HeadSHA}
	}
	return mr, nil
}

// ListDrafts lists the token user's pending draft notes on the merge request, first page of 100
// (FR25): GET /projects/:id/merge_requests/:iid/draft_notes?per_page=100.
func (c *Client) ListDrafts(ctx context.Context, token string, ref MRRef) ([]Draft, error) {
	base, err := mrPath(ref, whatReview)
	if err != nil {
		return nil, err
	}
	path := base + "/draft_notes"
	raw, err := c.call(ctx, token, request{method: http.MethodGet, path: path, query: "per_page=100", what: whatReview})
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, badBody(whatReview, c.prefix+path)
	}
	out := make([]Draft, 0, len(docs))
	for _, d := range docs {
		out = append(out, Draft{ID: d.ID})
	}
	return out, nil
}

// positionBody is the position of a positioned draft note (FR26). reviewer_state, approvals and
// every other field are absent by construction.
type positionBody struct {
	PositionType string `json:"position_type"`
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	NewPath      string `json:"new_path"`
	OldPath      string `json:"old_path"`
	NewLine      int    `json:"new_line"`
	OldLine      int    `json:"old_line,omitempty"`
}

// draftBody is the JSON of a draft note: only the note and, for an inline one, its position.
type draftBody struct {
	Note     string        `json:"note"`
	Position *positionBody `json:"position,omitempty"`
}

// CreateDraft creates a draft note: POST /projects/:id/merge_requests/:iid/draft_notes. A
// positioned draft the server refuses (400) is an *APIError the caller recognises by Status.
func (c *Client) CreateDraft(ctx context.Context, token string, ref MRRef, d NewDraft) (Draft, error) {
	base, err := mrPath(ref, whatReview)
	if err != nil {
		return Draft{}, err
	}
	path := base + "/draft_notes"
	body := draftBody{Note: d.Note}
	if p := d.Position; p != nil {
		body.Position = &positionBody{
			PositionType: "text",
			BaseSHA:      p.Refs.BaseSHA,
			StartSHA:     p.Refs.StartSHA,
			HeadSHA:      p.Refs.HeadSHA,
			NewPath:      p.NewPath,
			OldPath:      p.OldPath,
			NewLine:      p.NewLine,
			OldLine:      p.OldLine,
		}
	}
	raw, err := c.call(ctx, token, request{method: http.MethodPost, path: path, what: whatReview, body: body})
	if err != nil {
		return Draft{}, err
	}
	var doc struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.ID <= 0 {
		return Draft{}, badBody(whatReview, c.prefix+path)
	}
	return Draft{ID: doc.ID}, nil
}

// DeleteDraft deletes a draft note: DELETE …/draft_notes/:id.
func (c *Client) DeleteDraft(ctx context.Context, token string, ref MRRef, id int) error {
	path, err := draftPath(ref, id)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, token, request{method: http.MethodDelete, path: path, what: whatReview})
	return err
}

// PublishDraft publishes one draft note: PUT …/draft_notes/:id/publish, no body.
func (c *Client) PublishDraft(ctx context.Context, token string, ref MRRef, id int) error {
	path, err := draftPath(ref, id)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, token, request{method: http.MethodPut, path: path + "/publish", what: whatReview})
	return err
}

// BulkPublish publishes every pending draft of the token user on the merge request as one
// review: POST …/draft_notes/bulk_publish with no body and no parameter (NFR9).
func (c *Client) BulkPublish(ctx context.Context, token string, ref MRRef) error {
	base, err := mrPath(ref, whatReview)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, token, request{method: http.MethodPost, path: base + "/draft_notes/bulk_publish", what: whatReview})
	return err
}

// CreateNote posts one merge request note (the "MR comment" mode): POST …/notes {body}.
func (c *Client) CreateNote(ctx context.Context, token string, ref MRRef, body string) error {
	base, err := mrPath(ref, whatComment)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, token, request{method: http.MethodPost, path: base + "/notes", what: whatComment, body: struct {
		Body string `json:"body"`
	}{body}})
	return err
}

// mrPath is /projects/:id/merge_requests/:iid. The project id is the numeric CI_PROJECT_ID: a
// value with any other character is refused, so it cannot add a path segment or a query.
func mrPath(ref MRRef, what string) (string, error) {
	if ref.ProjectID == "" || len(ref.ProjectID) > 20 {
		return "", badRef(what, "the project id is not a number")
	}
	for i := range len(ref.ProjectID) {
		if b := ref.ProjectID[i]; b < '0' || b > '9' {
			return "", badRef(what, "the project id is not a number")
		}
	}
	if ref.IID <= 0 {
		return "", badRef(what, "the merge request number is not positive")
	}
	return "/projects/" + url.PathEscape(ref.ProjectID) + "/merge_requests/" + strconv.Itoa(ref.IID), nil
}

func draftPath(ref MRRef, id int) (string, error) {
	base, err := mrPath(ref, whatReview)
	if err != nil {
		return "", err
	}
	if id <= 0 {
		return "", badRef(whatReview, "the draft id is not positive")
	}
	return base + "/draft_notes/" + strconv.Itoa(id), nil
}

func badRef(what, reason string) error {
	return errors.New("GitLab API error " + what + ": " + reason)
}

func badBody(what, path string) error {
	return fmt.Errorf("GitLab API error %s (%s): the response is not the expected JSON", what, path)
}
