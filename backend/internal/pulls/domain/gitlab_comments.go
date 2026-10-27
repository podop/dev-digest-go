package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Error codes of the GitLab comment flow (GitLab B FR5, FR10). Every failure inside the post flow
// is an HTTP 400 carrying one of these codes and a Reason* in details.
const (
	CodeGitLabUnavailable      = "gitlab_unavailable"
	CodeGitLabCommentFailed    = "gitlab_comment_failed"
	CodeGitLabPermissionDenied = "gitlab_permission_denied"
	CodeGitLabRateLimited      = "gitlab_rate_limited"
)

// Reasons of the GitLab comment flow (details.reason).
const (
	ReasonTokenRejected       = "token_rejected"
	ReasonReadOnlyToken       = "read_only_token"
	ReasonForbidden           = "forbidden"
	ReasonNotFound            = "not_found"
	ReasonPositionRejected    = "position_rejected"
	ReasonRejected            = "rejected"
	ReasonRateLimited         = "rate_limited"
	ReasonRedirect            = "redirect"
	ReasonUnavailable         = "unavailable"
	ReasonFileNotInDiff       = "file_not_in_diff"
	ReasonLineNotInDiff       = "line_not_in_diff"
	ReasonDiffNotReady        = "diff_not_ready"
	ReasonDiffChanged         = "diff_changed"
	ReasonReplyTargetNotFound = "reply_target_not_found"
	ReasonBodyTooLong         = "body_too_long"
)

// MaxGitLabComments is how many comments one GET /pulls/:id/comments returns for a GitLab merge
// request (B FR2, D27).
const MaxGitLabComments = 100

// Sides of a review comment.
const (
	SideRight = "RIGHT"
	SideLeft  = "LEFT"
)

// Note types GitLab sends.
const noteTypeDiff = "DiffNote"

// PositionTypeText is the only position type that becomes a comment (B FR2).
const PositionTypeText = "text"

// Position is where a GitLab diff note sits. The lines are nil when GitLab sent none.
type Position struct {
	Type     string // text | image | file
	BaseSHA  string
	StartSHA string
	HeadSHA  string
	OldPath  string
	NewPath  string
	OldLine  *int
	NewLine  *int
}

// Note is one note of a GitLab discussion, with plain values; the adapter decodes the REST
// payload and fills it. Position is nil for a note without one.
type Note struct {
	ID        int64
	Type      string // DiffNote | DiscussionNote | ""
	System    bool
	Body      string
	Author    string // author.username; empty when GitLab sent none
	CreatedAt time.Time
	Position  *Position
}

// Discussion is a GitLab discussion thread: an id (hex) and its notes, the first one the root.
type Discussion struct {
	ID    string
	Notes []Note
}

// DiffRefs are the three SHAs a diff position is anchored to.
type DiffRefs struct {
	BaseSHA  string
	StartSHA string
	HeadSHA  string
}

// Complete reports whether all three SHAs are present (B FR7).
func (r DiffRefs) Complete() bool {
	return r.BaseSHA != "" && r.StartSHA != "" && r.HeadSHA != ""
}

// MapDiscussions maps GitLab discussions of merge request n to review comments (B FR2–FR4).
// A discussion is kept when its first note is a non-system DiffNote with a text position; every
// non-system note of a kept discussion becomes one comment, in GitLab's order, at most
// MaxGitLabComments. diffHead is the head the diff on screen belongs to (Pull.EffectiveDiffHead):
// a root whose position head differs from it is outdated and has no line. webURL is the stored
// repository web URL the html_url is built from.
func MapDiscussions(ds []Discussion, diffHead, webURL string, n int) []ReviewComment {
	out := make([]ReviewComment, 0, min(len(ds), MaxGitLabComments))
	for _, d := range ds {
		if len(out) >= MaxGitLabComments {
			break
		}
		root, ok := diffRoot(d)
		if !ok {
			continue
		}
		pos := root.Position
		c := ReviewComment{Path: pos.NewPath, Side: SideLeft}
		if c.Path == "" {
			c.Path = pos.OldPath
		}
		switch {
		case pos.NewLine != nil:
			c.Side = SideRight
			c.OriginalLine = copyInt(pos.NewLine)
		case pos.OldLine != nil:
			c.OriginalLine = copyInt(pos.OldLine)
		}
		c.IsOutdated = pos.HeadSHA != diffHead
		if !c.IsOutdated {
			c.Line = copyInt(c.OriginalLine)
		}
		for _, note := range d.Notes {
			if len(out) >= MaxGitLabComments {
				break
			}
			if note.System {
				continue
			}
			item := ReviewComment{
				ID: note.ID, Path: c.Path, Side: c.Side, Body: note.Body,
				User: noteUser(note), CreatedAt: note.CreatedAt,
				HTMLURL:      noteURL(webURL, n, note.ID),
				Line:         copyInt(c.Line),
				OriginalLine: copyInt(c.OriginalLine),
				IsOutdated:   c.IsOutdated,
			}
			if note.ID != root.ID {
				id := root.ID
				item.InReplyToID = &id
			}
			out = append(out, item)
		}
	}
	return out
}

// diffRoot returns the first note of a discussion when it is a kept root: a non-system DiffNote
// with a text position that names a path. Anything else (system or general discussion, image or
// file position, malformed note) is dropped.
func diffRoot(d Discussion) (Note, bool) {
	if len(d.Notes) == 0 {
		return Note{}, false
	}
	root := d.Notes[0]
	if root.System || root.Type != noteTypeDiff || root.Position == nil {
		return Note{}, false
	}
	if root.Position.Type != PositionTypeText {
		return Note{}, false
	}
	if root.Position.NewPath == "" && root.Position.OldPath == "" {
		return Note{}, false
	}
	return root, true
}

func noteUser(n Note) string {
	if n.Author == "" {
		return "unknown"
	}
	return n.Author
}

// noteURL is GitLab's own link form for a note: <MR web URL>#note_<id>. It is empty when the
// repository has no stored web URL.
func noteURL(webURL string, n int, id int64) string {
	webURL = strings.TrimRight(webURL, "/")
	if webURL == "" {
		return ""
	}
	return webURL + "/-/merge_requests/" + strconv.Itoa(n) + "#note_" + strconv.FormatInt(id, 10)
}

func copyInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// PositionOldPath is the old_path of a diff position for the file: its previous path when GitLab
// reported one, else its path (B FR8, FR35).
func (f File) PositionOldPath() string {
	if f.PreviousPath != nil && *f.PreviousPath != "" {
		return *f.PreviousPath
	}
	return f.Path
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// PositionFor finds the diff line a new comment on (side, line) means in the file's stored patch
// and returns the position lines GitLab needs (B FR6): an added line gives only new, a removed
// line only old, an unchanged line both. side is "RIGHT" (empty reads as RIGHT) or "LEFT"; line
// is the new number for RIGHT and the old number for LEFT. When no diff line matches (a line
// between hunks, no stored patch, an unknown side) both lines are nil and reason is
// ReasonLineNotInDiff. The caller reports ReasonFileNotInDiff for a path that has no stored file.
func PositionFor(f File, side string, line int) (oldLine, newLine *int, reason string) {
	right := side == "" || side == SideRight
	if (!right && side != SideLeft) || line <= 0 || f.Patch == nil || *f.Patch == "" {
		return nil, nil, ReasonLineNotInDiff
	}
	var (
		oldNo, newNo       int // number of the next line on each side
		oldLeft, newLeft   int // lines the current hunk still owes
		o, nw              int
		found              bool
		foundOld, foundNew *int
	)
	patch := *f.Patch
	for patch != "" && !found {
		var l string
		l, patch, _ = strings.Cut(patch, "\n")
		if oldLeft <= 0 && newLeft <= 0 {
			// Between hunks: only a hunk header is a line of structure; the rest is ignored.
			m := hunkHeader.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			var ok bool
			if oldNo, oldLeft, newNo, newLeft, ok = parseHunk(m); !ok {
				oldLeft, newLeft = 0, 0
			}
			continue
		}
		l = strings.TrimSuffix(l, "\r")
		kind := byte(' ')
		if l != "" {
			kind = l[0]
		}
		switch kind {
		case '\\': // "\ No newline at end of file"
			continue
		case '+':
			if newLeft <= 0 {
				oldLeft, newLeft = 0, 0
				continue
			}
			o, nw = 0, newNo
			newNo++
			newLeft--
			if right && nw == line {
				foundNew, found = &nw, true
			}
		case '-':
			if oldLeft <= 0 {
				oldLeft, newLeft = 0, 0
				continue
			}
			o, nw = oldNo, 0
			oldNo++
			oldLeft--
			if !right && o == line {
				foundOld, found = &o, true
			}
		default: // ' ' and a context line whose leading space was trimmed
			if oldLeft <= 0 || newLeft <= 0 {
				oldLeft, newLeft = 0, 0
				continue
			}
			o, nw = oldNo, newNo
			oldNo++
			newNo++
			oldLeft--
			newLeft--
			if (right && nw == line) || (!right && o == line) {
				foundOld, foundNew, found = &o, &nw, true
			}
		}
	}
	if !found {
		return nil, nil, ReasonLineNotInDiff
	}
	return copyInt(foundOld), copyInt(foundNew), ""
}

// parseHunk reads the numbers of a hunk header match; a missing count is 1. ok is false when a
// number does not fit an int.
func parseHunk(m []string) (oldStart, oldCount, newStart, newCount int, ok bool) {
	num := func(s string, def int) (int, bool) {
		if s == "" {
			return def, true
		}
		v, err := strconv.Atoi(s)
		return v, err == nil
	}
	var ok1, ok2, ok3, ok4 bool
	oldStart, ok1 = num(m[1], 0)
	oldCount, ok2 = num(m[2], 1)
	newStart, ok3 = num(m[3], 0)
	newCount, ok4 = num(m[4], 1)
	return oldStart, oldCount, newStart, newCount, ok1 && ok2 && ok3 && ok4
}

// GitLabFailure is one failed GitLab call of the post flow, described without GitLab's text.
type GitLabFailure struct {
	// Status is the HTTP status; 0 when no HTTP answer came (timeout, connection or TLS error).
	Status int
	// InsufficientScope is set for a 403 whose error is insufficient_scope (a read_api token).
	InsufficientScope bool
	// Create is set for the discussion create call, the only call a rejected position can fail.
	Create bool
	// NonJSON is set for a 2xx answer whose body is not the expected JSON.
	NonJSON bool
}

// Classify maps the failure to the error code and reason of B FR10.
func (f GitLabFailure) Classify() (code, reason string) {
	s := f.Status
	switch {
	case f.NonJSON, s < 200:
		return CodeGitLabCommentFailed, ReasonUnavailable
	case s == 401:
		return CodeGitLabPermissionDenied, ReasonTokenRejected
	case s == 403 && f.InsufficientScope:
		return CodeGitLabPermissionDenied, ReasonReadOnlyToken
	case s == 403:
		return CodeGitLabPermissionDenied, ReasonForbidden
	case s == 404:
		return CodeGitLabCommentFailed, ReasonNotFound
	case s == 429:
		return CodeGitLabRateLimited, ReasonRateLimited
	case f.Create && (s == 400 || s == 422 || s == 500):
		return CodeGitLabCommentFailed, ReasonPositionRejected
	case s >= 300 && s < 400:
		return CodeGitLabCommentFailed, ReasonRedirect
	case s >= 400 && s < 500:
		return CodeGitLabCommentFailed, ReasonRejected
	default: // other 5xx and a 2xx that should not reach here: the outcome is unknown
		return CodeGitLabCommentFailed, ReasonUnavailable
	}
}
