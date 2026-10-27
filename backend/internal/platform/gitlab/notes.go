package gitlab

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// Position places a note on a line of a merge request diff. For a read it is what GitLab
// reports on a diff note; for CreateDiscussion it is what is sent.
type Position struct {
	// Type is GitLab's position_type: "text" for a line of a text file. CreateDiscussion treats
	// "" as "text" and refuses any other value.
	Type     string
	BaseSHA  string
	StartSHA string
	HeadSHA  string
	// OldPath and NewPath are the file's paths on the two sides (equal unless renamed).
	OldPath string
	NewPath string
	// OldLine is the line number on the old side, NewLine on the new side; nil when the
	// position has none on that side (an added line has no old line, a removed line no new line).
	OldLine *int
	NewLine *int
}

// Note is one note of a discussion.
type Note struct {
	ID int64
	// Type is GitLab's note type: "DiffNote" for a note on the diff, "DiscussionNote" for a
	// reply or general thread note, "" for a plain note.
	Type string
	// System is true for GitLab's own activity notes ("added 1 commit", "changed the title").
	System bool
	Body   string
	// Author is the username, "unknown" when GitLab has no author.
	Author    string
	CreatedAt time.Time
	// Position is nil for a note without one.
	Position *Position
}

// Discussion is a thread of notes; the first note is the root.
type Discussion struct {
	ID    string
	Notes []Note
}

type apiPosition struct {
	Type     string `json:"position_type"`
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
	OldPath  string `json:"old_path"`
	NewPath  string `json:"new_path"`
	OldLine  *int   `json:"old_line"`
	NewLine  *int   `json:"new_line"`
}

type apiNote struct {
	ID        int64        `json:"id"`
	Type      *string      `json:"type"`
	System    bool         `json:"system"`
	Body      string       `json:"body"`
	CreatedAt time.Time    `json:"created_at"`
	Position  *apiPosition `json:"position"`
	Author    *struct {
		Username string `json:"username"`
	} `json:"author"`
}

type apiDiscussion struct {
	ID    string    `json:"id"`
	Notes []apiNote `json:"notes"`
}

func (n apiNote) note() (Note, error) {
	if n.ID <= 0 {
		return Note{}, fmt.Errorf("gitlab note: %w", ErrNotAPI)
	}
	out := Note{ID: n.ID, System: n.System, Body: n.Body, CreatedAt: n.CreatedAt, Author: "unknown"}
	if n.Type != nil {
		out.Type = *n.Type
	}
	if n.Author != nil && n.Author.Username != "" {
		out.Author = n.Author.Username
	}
	if p := n.Position; p != nil {
		out.Position = &Position{
			Type: p.Type, BaseSHA: p.BaseSHA, StartSHA: p.StartSHA, HeadSHA: p.HeadSHA,
			OldPath: p.OldPath, NewPath: p.NewPath, OldLine: p.OldLine, NewLine: p.NewLine,
		}
	}
	return out, nil
}

func (d apiDiscussion) discussion() (Discussion, error) {
	if d.ID == "" {
		return Discussion{}, fmt.Errorf("gitlab discussion: %w", ErrNotAPI)
	}
	out := Discussion{ID: d.ID, Notes: make([]Note, len(d.Notes))}
	for i, n := range d.Notes {
		var err error
		if out.Notes[i], err = n.note(); err != nil {
			return Discussion{}, err
		}
	}
	return out, nil
}

// DiscussionsPage calls GET /projects/:id/merge_requests/:iid/discussions for page `page`
// (1-based) of `perPage` (1..100) (spec B FR9, D27). A page shorter than perPage is the last one;
// GitLab's next-page links are not read.
func (c *Client) DiscussionsPage(ctx context.Context, conn Conn, projectID int64, iid, page, perPage int) ([]Discussion, error) {
	path, err := mrPath(projectID, iid, "/discussions")
	if err != nil {
		return nil, err
	}
	q, err := pageQuery(page, perPage, nil)
	if err != nil {
		return nil, err
	}
	raw, err := getJSON[[]apiDiscussion](ctx, c, conn, "discussions", path, q)
	if err != nil {
		return nil, err
	}
	out := make([]Discussion, len(raw))
	for i, d := range raw {
		if out[i], err = d.discussion(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// discussionID is the shape of a discussion id (A3): lower-case hex, 8..64 characters (GitLab
// uses 40). It goes into a URL path, so nothing else is allowed.
var discussionIDPattern = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// validPosition checks what CreateDiscussion needs before it sends anything.
func validPosition(p Position) bool {
	if p.Type != "" && p.Type != "text" {
		return false
	}
	if p.BaseSHA == "" || p.StartSHA == "" || p.HeadSHA == "" || p.OldPath == "" || p.NewPath == "" {
		return false
	}
	if p.OldLine == nil && p.NewLine == nil {
		return false
	}
	return (p.OldLine == nil || *p.OldLine >= 1) && (p.NewLine == nil || *p.NewLine >= 1)
}

// CreateDiscussion calls POST /projects/:id/merge_requests/:iid/discussions with a JSON body
// holding body and a text position (spec B FR8, A4). The request is sent exactly once: on a
// timeout, a transport error or a 5xx the discussion may exist already, so it is never repeated.
// The caller must pass a body that has passed the note body rules (FR13-FR17). An incomplete
// position (an empty SHA or path, no line, a line below 1, a type other than text) is refused
// without a request. A 2xx answer that is not a discussion with a note is ErrNotAPI.
func (c *Client) CreateDiscussion(ctx context.Context, conn Conn, projectID int64, iid int, body string, pos Position) (Discussion, error) {
	path, err := mrPath(projectID, iid, "/discussions")
	if err != nil {
		return Discussion{}, err
	}
	if !validPosition(pos) {
		return Discussion{}, fmt.Errorf("gitlab discussion: incomplete position")
	}
	type position struct {
		Type     string `json:"position_type"`
		BaseSHA  string `json:"base_sha"`
		StartSHA string `json:"start_sha"`
		HeadSHA  string `json:"head_sha"`
		OldPath  string `json:"old_path"`
		NewPath  string `json:"new_path"`
		OldLine  *int   `json:"old_line,omitempty"`
		NewLine  *int   `json:"new_line,omitempty"`
	}
	raw, err := postOnce[apiDiscussion](ctx, c, conn, "create_discussion", path, struct {
		Body     string   `json:"body"`
		Position position `json:"position"`
	}{body, position{"text", pos.BaseSHA, pos.StartSHA, pos.HeadSHA, pos.OldPath, pos.NewPath, pos.OldLine, pos.NewLine}})
	if err != nil {
		return Discussion{}, err
	}
	d, err := raw.discussion()
	if err != nil {
		return Discussion{}, err
	}
	if len(d.Notes) == 0 {
		return Discussion{}, fmt.Errorf("gitlab create_discussion: %w", ErrNotAPI)
	}
	return d, nil
}

// CreateNote calls POST /projects/:id/merge_requests/:iid/discussions/:discussion_id/notes with a
// JSON body holding body, a reply to the discussion (spec B FR9, A4). Like CreateDiscussion it is
// sent exactly once, and the caller passes a body that has passed the note body rules. A
// discussion id that is not 8..64 lower-case hex characters is refused without a request.
func (c *Client) CreateNote(ctx context.Context, conn Conn, projectID int64, iid int, discID, body string) (Note, error) {
	if !discussionIDPattern.MatchString(discID) {
		return Note{}, fmt.Errorf("gitlab note: invalid discussion id")
	}
	path, err := mrPath(projectID, iid, "/discussions/"+discID+"/notes")
	if err != nil {
		return Note{}, err
	}
	raw, err := postOnce[apiNote](ctx, c, conn, "create_note", path, struct {
		Body string `json:"body"`
	}{body})
	if err != nil {
		return Note{}, err
	}
	return raw.note()
}
