package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/gitlab/notebody"
	"dev-digest-go/backend/internal/pulls/domain"
)

// Limits of the GitLab comment flow (spec B D27, FR9, FR10, NFR4).
const (
	// gitLabCommentPages is how many discussion pages one GET comments reads (SCR1).
	gitLabCommentPages = 3
	// gitLabReplyPages is how many discussion pages the reply search reads (FR9).
	gitLabReplyPages = 10
	// maxCauseBytes cuts details.cause (§7).
	maxCauseBytes = 1000
	// maxRejectedRunes cuts GitLab's message in the "rejected" error (A1).
	maxRejectedRunes = 300
	// defaultRetryAfterSeconds is details.retry_after_s when GitLab sent no usable Retry-After (A5).
	defaultRetryAfterSeconds = 60
	maxRetryAfterSeconds     = 86400
)

// WithGitLabComments gives the Service its GitLab comments access. Without it a GitLab repository
// reads as not connected for comments (fail closed: no request is sent).
func WithGitLabComments(c GitLabComments) Option { return func(s *Service) { s.glc = c } }

// WithRedact sets the redactor applied to every GitLab text that reaches a response or a log line
// (all stored connection tokens, NFR4). Nil keeps the identity.
func WithRedact(redact func(string) string) Option {
	return func(s *Service) {
		if redact != nil {
			s.redact = redact
		}
	}
}

// comments returns the GitLab comments port; without one every call fails with ErrNotConnected.
func (s *Service) comments() GitLabComments {
	if s.glc == nil {
		return disconnectedComments{}
	}
	return s.glc
}

// disconnectedComments is the GitLab comments port of a Service built without WithGitLabComments.
type disconnectedComments struct{}

func (disconnectedComments) Ready(context.Context, domain.Repo) error { return ErrNotConnected }

func (disconnectedComments) Discussions(context.Context, domain.Repo, int, int) ([]domain.Discussion, bool, error) {
	return nil, false, ErrNotConnected
}

func (disconnectedComments) MergeRequestRefs(context.Context, domain.Repo, int) (domain.DiffRefs, error) {
	return domain.DiffRefs{}, ErrNotConnected
}

func (disconnectedComments) LatestVersion(context.Context, domain.Repo, int) (domain.DiffRefs, bool, error) {
	return domain.DiffRefs{}, false, ErrNotConnected
}

func (disconnectedComments) CreateDiscussion(context.Context, domain.Repo, int, string, domain.Position) (domain.Discussion, error) {
	return domain.Discussion{}, ErrNotConnected
}

func (disconnectedComments) CreateNote(context.Context, domain.Repo, int, string, string) (domain.Note, error) {
	return domain.Note{}, ErrNotConnected
}

// gitLabComments lists the diff threads of a GitLab merge request (spec B FR1–FR4): up to three
// discussion pages, at most 100 comments. A repository that is not connected, a connection without
// a token and any GitLab failure answer no comments.
func (s *Service) gitLabComments(ctx context.Context, pr domain.Pull, repo domain.Repo) []domain.ReviewComment {
	glc := s.comments()
	out := []domain.ReviewComment{}
	if err := glc.Ready(ctx, repo); err != nil {
		if !notReady(err) {
			s.log.WarnContext(ctx, "gitlab review comments not fetched; serving none", "pr_id", pr.ID, "err", s.redactErr(err))
		}
		return out
	}
	head := pr.EffectiveDiffHead()
	var all []domain.Discussion
	for page := 1; page <= gitLabCommentPages; page++ {
		ds, more, err := glc.Discussions(ctx, repo, pr.Number, page)
		if err != nil {
			s.log.WarnContext(ctx, "gitlab review comments not fetched; serving none", "pr_id", pr.ID, "page", page, "err", s.redactErr(err))
			return []domain.ReviewComment{}
		}
		all = append(all, ds...)
		out = domain.MapDiscussions(all, head, repo.WebURL, pr.Number)
		if !more || len(out) >= domain.MaxGitLabComments {
			break
		}
	}
	return out
}

// notReady reports whether err is the quiet "no connection / no token" answer of Ready.
func notReady(err error) bool {
	var nt *NoTokenError
	return errors.Is(err, ErrNotConnected) || errors.As(err, &nt)
}

// gitLabCall describes the call being made, for the error messages that name it.
type gitLabCall struct {
	create bool // the discussion create: a 400/422/500 means the position was rejected
	path   string
	line   int
}

// postGitLabComment is PostComment for a GitLab repository (spec B FR5–FR11): one INFO line per
// action (NFR6) with the outcome code and reason, never the body.
func (s *Service) postGitLabComment(ctx context.Context, pr domain.Pull, repo domain.Repo, req CommentRequest) (domain.ReviewComment, error) {
	op := "post"
	if req.InReplyTo != nil {
		op = "reply"
	}
	created, err := s.runGitLabPost(ctx, pr, repo, req)
	code, reason := "ok", ""
	if err != nil {
		code, reason = errorCodeReason(err)
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "gitlab comment", slog.String("pr_id", pr.ID),
		slog.String("operation", op), slog.String("code", code), slog.String("reason", reason))
	return created, err
}

// errorCodeReason reads the wire code and details.reason of an application error; other errors
// are internal ones.
func errorCodeReason(err error) (code, reason string) {
	ae, ok := apperr.As(err)
	if !ok {
		return "internal_error", ""
	}
	if d, ok := ae.Details.(map[string]any); ok {
		reason, _ = d["reason"].(string)
	}
	return ae.Code, reason
}

func (s *Service) runGitLabPost(ctx context.Context, pr domain.Pull, repo domain.Repo, req CommentRequest) (domain.ReviewComment, error) {
	glc := s.comments()
	// FR5 (3), (4): no GitLab request has been sent yet.
	if err := glc.Ready(ctx, repo); err != nil {
		return domain.ReviewComment{}, s.gitLabPostError(repo, err, gitLabCall{})
	}
	// FR5 (5), FR13: the rules run on the whole body, the limit on their output.
	body := notebody.Apply(req.Body)
	if notebody.Chars(body) > notebody.MaxChars {
		return domain.ReviewComment{}, gitLabCommentError(domain.CodeGitLabCommentFailed, domain.ReasonBodyTooLong,
			"The comment is longer than GitLab allows (1,000,000 characters).", "")
	}
	if req.InReplyTo != nil {
		return s.replyGitLab(ctx, glc, pr, repo, req, body)
	}
	return s.createGitLabDiscussion(ctx, glc, pr, repo, req, body)
}

// createGitLabDiscussion posts a new positioned discussion (FR6–FR8).
func (s *Service) createGitLabDiscussion(ctx context.Context, glc GitLabComments, pr domain.Pull, repo domain.Repo, req CommentRequest, body string) (domain.ReviewComment, error) {
	side := req.Side
	if side == "" {
		side = domain.SideRight
	}
	files, err := s.store.ListFiles(ctx, pr.ID)
	if err != nil {
		return domain.ReviewComment{}, fmt.Errorf("list files of pull request %s: %w", pr.ID, err)
	}
	file, ok := fileByPath(files, req.Path)
	if !ok || file.Path == "" {
		return domain.ReviewComment{}, gitLabCommentError(domain.CodeGitLabCommentFailed, domain.ReasonFileNotInDiff,
			req.Path+" is not part of this merge request's diff.", "")
	}
	oldLine, newLine, reason := domain.PositionFor(file, side, req.Line)
	// The adapter refuses an incomplete position without a request and reports it as unavailable;
	// such a position is never sent: no usable line means the line is not in the diff.
	if reason != "" || (oldLine == nil && newLine == nil) || (oldLine != nil && *oldLine < 1) || (newLine != nil && *newLine < 1) {
		return domain.ReviewComment{}, gitLabCommentError(domain.CodeGitLabCommentFailed, domain.ReasonLineNotInDiff,
			fmt.Sprintf("Line %d (%s) is not part of the diff of %s.", req.Line, side, req.Path), "")
	}

	call := gitLabCall{create: true, path: req.Path, line: req.Line}
	refs, err := s.diffRefs(ctx, glc, repo, pr.Number)
	if err != nil {
		return domain.ReviewComment{}, err
	}
	if pr.DiffHead == nil || *pr.DiffHead == "" || refs.HeadSHA != *pr.DiffHead {
		return domain.ReviewComment{}, gitLabCommentError(domain.CodeGitLabCommentFailed, domain.ReasonDiffChanged,
			"The merge request changed since this diff was loaded. Reload the PR and post again.", "")
	}

	disc, err := glc.CreateDiscussion(ctx, repo, pr.Number, body, domain.Position{
		Type: domain.PositionTypeText, BaseSHA: refs.BaseSHA, StartSHA: refs.StartSHA, HeadSHA: refs.HeadSHA,
		OldPath: file.PositionOldPath(), NewPath: file.Path, OldLine: oldLine, NewLine: newLine,
	})
	if err != nil {
		return domain.ReviewComment{}, s.gitLabPostError(repo, err, call)
	}
	if len(disc.Notes) == 0 {
		return domain.ReviewComment{}, s.gitLabPostError(repo, ErrGitLabUnavailable, call)
	}
	return createdComment(disc.Notes[0], file.Path, side, req.Line, refs.HeadSHA, repo, pr.Number), nil
}

// createdComment maps the first note of a created discussion per FR3 with the values of the
// request (FR8): not outdated, line and side as requested.
func createdComment(note domain.Note, path, side string, line int, head string, repo domain.Repo, number int) domain.ReviewComment {
	root := note
	root.Type, root.System = "DiffNote", false
	root.Position = &domain.Position{Type: domain.PositionTypeText, NewPath: path, HeadSHA: head}
	c := domain.ReviewComment{ID: note.ID, Path: path, Body: note.Body, User: note.Author, CreatedAt: note.CreatedAt}
	if mapped := domain.MapDiscussions([]domain.Discussion{{Notes: []domain.Note{root}}}, head, repo.WebURL, number); len(mapped) == 1 {
		c = mapped[0]
	}
	l := line
	c.Side, c.IsOutdated, c.InReplyToID = side, false, nil
	c.Line, c.OriginalLine = &l, new(line)
	return c
}

// diffRefs reads the merge request's diff refs (FR7): its own when complete, else the newest diff
// version's. Neither available is diff_not_ready.
func (s *Service) diffRefs(ctx context.Context, glc GitLabComments, repo domain.Repo, number int) (domain.DiffRefs, error) {
	call := gitLabCall{}
	refs, err := glc.MergeRequestRefs(ctx, repo, number)
	if err != nil {
		return domain.DiffRefs{}, s.gitLabPostError(repo, err, call)
	}
	if refs.Complete() {
		return refs, nil
	}
	refs, ok, err := glc.LatestVersion(ctx, repo, number)
	if err != nil {
		return domain.DiffRefs{}, s.gitLabPostError(repo, err, call)
	}
	if !ok || !refs.Complete() {
		return domain.DiffRefs{}, gitLabCommentError(domain.CodeGitLabCommentFailed, domain.ReasonDiffNotReady,
			"GitLab is still preparing this merge request's diff. Try again in a minute.", "")
	}
	return refs, nil
}

// replyGitLab posts a reply note into the discussion that holds the note InReplyTo (FR9).
func (s *Service) replyGitLab(ctx context.Context, glc GitLabComments, pr domain.Pull, repo domain.Repo, req CommentRequest, body string) (domain.ReviewComment, error) {
	target := *req.InReplyTo
	disc, found, err := s.findDiscussion(ctx, glc, repo, pr.Number, target)
	if err != nil {
		return domain.ReviewComment{}, err
	}
	if !found || !isDiscussionID(disc.ID) {
		return domain.ReviewComment{}, gitLabCommentError(domain.CodeGitLabCommentFailed, domain.ReasonReplyTargetNotFound,
			"The comment you are replying to was not found on GitLab.", "")
	}
	note, err := glc.CreateNote(ctx, repo, pr.Number, disc.ID, body)
	if err != nil {
		return domain.ReviewComment{}, s.gitLabPostError(repo, err, gitLabCall{})
	}
	return repliedComment(disc, note, req, pr, repo), nil
}

// findDiscussion reads discussion pages (at most gitLabReplyPages) until one holds a non-system
// note with the id.
func (s *Service) findDiscussion(ctx context.Context, glc GitLabComments, repo domain.Repo, number int, noteID int64) (domain.Discussion, bool, error) {
	if noteID <= 0 {
		return domain.Discussion{}, false, nil
	}
	for page := 1; page <= gitLabReplyPages; page++ {
		ds, more, err := glc.Discussions(ctx, repo, number, page)
		if err != nil {
			return domain.Discussion{}, false, s.gitLabPostError(repo, err, gitLabCall{})
		}
		for _, d := range ds {
			for _, n := range d.Notes {
				if !n.System && n.ID == noteID {
					return d, true, nil
				}
			}
		}
		if !more {
			break
		}
	}
	return domain.Discussion{}, false, nil
}

// repliedComment maps the created reply per FR3: path, side and line come from the discussion's
// root; in_reply_to_id is the root's note id. A discussion that is not a text diff thread (the
// reader never lists those) falls back to the request's own values.
func repliedComment(d domain.Discussion, note domain.Note, req CommentRequest, pr domain.Pull, repo domain.Repo) domain.ReviewComment {
	withNote := domain.Discussion{ID: d.ID, Notes: append(append([]domain.Note(nil), d.Notes...), note)}
	for _, c := range domain.MapDiscussions([]domain.Discussion{withNote}, pr.EffectiveDiffHead(), repo.WebURL, pr.Number) {
		if c.ID == note.ID {
			return c
		}
	}
	side := req.Side
	if side == "" {
		side = domain.SideRight
	}
	c := domain.ReviewComment{
		ID: note.ID, Path: req.Path, Side: side, Body: note.Body, User: note.Author, CreatedAt: note.CreatedAt,
		Line: new(req.Line), OriginalLine: new(req.Line),
	}
	if len(d.Notes) > 0 {
		rootID := d.Notes[0].ID
		c.InReplyToID = &rootID
	}
	return c
}

// isDiscussionID reports whether id is 8–64 lower-case hex characters (A3): it becomes a URL path
// segment, so anything else is refused before the request.
func isDiscussionID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func fileByPath(files []domain.File, path string) (domain.File, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return domain.File{}, false
}

// gitLabCommentError is a 400 of the comment flow: details {reason, cause}.
func gitLabCommentError(code, reason, message, cause string) *apperr.Error {
	return apperr.InvalidInput(message).WithCode(code).
		WithDetails(map[string]any{"reason": reason, "cause": cause})
}

// gitLabPostError maps a failed GitLab call of the post flow to its application error (FR10).
// A missing connection or token is the FR5 error; everything else is classified without GitLab's
// text, which only reaches a message (rejected) or the cause after redaction (NFR4).
func (s *Service) gitLabPostError(repo domain.Repo, err error, call gitLabCall) error {
	if errors.Is(err, ErrNotConnected) {
		return NotConnectedError(repo).WithCause(err)
	}
	var nt *NoTokenError
	if errors.As(err, &nt) {
		return apperr.InvalidInput("Connect a token for " + repo.Host + " in Settings → GitLab Integration to post comments.").
			WithCode(domain.CodeGitLabUnavailable)
	}

	var ge *GitLabError
	failure := domain.GitLabFailure{Create: call.create}
	switch {
	case errors.As(err, &ge):
		failure.Status = ge.Status
		failure.InsufficientScope = ge.InsufficientScope || (ge.Status == 403 && strings.Contains(ge.Message, "insufficient_scope"))
	case errors.Is(err, ErrGitLabRedirect):
		failure.Status = 302
	case errors.Is(err, ErrGitLabUnavailable):
		failure.NonJSON = true
	}
	code, reason := failure.Classify()
	cause := s.gitLabCause(err)
	name := repo.Host // fallback when the adapter could not name the connection
	if ge != nil && ge.Conn != "" {
		name = ge.Conn
	}

	var (
		msg  string
		more = map[string]any{}
	)
	switch reason {
	case domain.ReasonTokenRejected:
		msg = fmt.Sprintf("GitLab rejected the token of connection %s (401).", name)
	case domain.ReasonReadOnlyToken:
		msg = fmt.Sprintf("The token of connection %s cannot post: it needs the api scope.", name)
	case domain.ReasonForbidden:
		msg = "GitLab refused the comment (403): the token's user may lack the Reporter role, or the discussion is locked."
	case domain.ReasonNotFound:
		msg = "GitLab did not find this merge request, or the token cannot see it."
	case domain.ReasonPositionRejected:
		msg = fmt.Sprintf("GitLab could not place the comment on %s line %d. Reload the PR and try again.", call.path, call.line)
	case domain.ReasonRejected:
		msg = s.rejectedMessage(ge)
	case domain.ReasonRateLimited:
		n := s.retryAfterSeconds(ge)
		msg = fmt.Sprintf("GitLab limits how many comments can be posted per minute. Try again in %d s.", n)
		more["retry_after_s"] = n
	case domain.ReasonRedirect:
		msg = "GitLab answered with a redirect — check the connection's base URL."
	default:
		reason = domain.ReasonUnavailable
		msg = "GitLab is unavailable; the comment was not confirmed. Check the merge request before posting again."
	}
	more["reason"], more["cause"] = reason, cause
	return apperr.InvalidInput(msg).WithCode(code).WithDetails(more)
}

// rejectedMessage is GitLab's message, redacted and cut to 300 runes, else a generic one (A1).
func (s *Service) rejectedMessage(ge *GitLabError) string {
	status := 0
	if ge != nil {
		status = ge.Status
		if m := strings.TrimSpace(s.redact(ge.Message)); m != "" {
			return cutRunes(m, maxRejectedRunes)
		}
	}
	return fmt.Sprintf("GitLab refused the comment (%d).", status)
}

// retryAfterSeconds is the wait a 429 asks for: the adapter's rounded seconds, else the time
// left until Retry-After, else 60 (A5).
func (s *Service) retryAfterSeconds(ge *GitLabError) int {
	n := defaultRetryAfterSeconds
	if ge != nil {
		switch {
		case ge.RetryAfterSeconds > 0:
			n = ge.RetryAfterSeconds
		case !ge.RetryAfter.IsZero():
			if left := ge.RetryAfter.Sub(s.now()); left > 0 {
				n = int((left + time.Second - 1) / time.Second)
			}
		}
	}
	return min(n, maxRetryAfterSeconds)
}

// gitLabCause is details.cause: GitLab's message redacted and cut to 1,000 bytes, else "GitLab
// answered HTTP <status>", else the error class (§7). It never carries a response body.
func (s *Service) gitLabCause(err error) string {
	var ge *GitLabError
	switch {
	case errors.As(err, &ge):
		if m := strings.TrimSpace(s.redact(ge.Message)); m != "" {
			return cutBytes(m, maxCauseBytes)
		}
		return "GitLab answered HTTP " + strconv.Itoa(ge.Status)
	case errors.Is(err, ErrGitLabRedirect):
		return "redirect"
	case errors.Is(err, ErrGitLabUnavailable):
		return "unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return fmt.Sprintf("%T", err)
	}
}

// redactErr is err's text with every stored token removed, for log lines.
func (s *Service) redactErr(err error) string { return s.redact(err.Error()) }

// cutBytes cuts s to at most n bytes without splitting a character.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// cutRunes cuts s to at most n characters.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for range n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}
