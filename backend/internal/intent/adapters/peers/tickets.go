package peers

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/gitlab"
)

// Issues is the part of *github.Client the ticket reader uses.
type Issues interface {
	GetIssue(ctx context.Context, owner, name string, number int) (github.Issue, error)
}

// Tickets implements app.Tickets over the GitHub issues API and, for a GitLab repository, over
// the GitLab issues API of the repository's connection (WithGitLab).
type Tickets struct {
	gh     Issues
	redact func(string) string
	gl     gitLabSide
}

var _ app.Tickets = (*Tickets)(nil)

// Texts of failed ticket reads that are not GitHub's own (spec 7.13, D5006).
const (
	noTokenReason  = "GITHUB_TOKEN is not configured"
	notFoundReason = "Not Found"
	// glNotFoundReason is GitLab's own text for an issue that is absent or cannot be seen; a number
	// that cannot name an issue reads the same, without a request.
	glNotFoundReason   = "404 Not found"
	glUnreadableReason = "GitLab connections could not be read"
	// maxReasonBytes bounds a stored failure text, as GitHub's messages are bounded.
	maxReasonBytes = 1000
)

// NewTickets wraps the GitHub client. redact scrubs secrets out of a failure text (the LLM
// client's Redact removes provider keys and the GitHub token); nil leaves texts as they are. A
// missing client is a wiring error and panics.
func NewTickets(gh Issues, redact func(string) string) *Tickets {
	if gh == nil {
		panic("intent/adapters/peers: NewTickets needs a GitHub client")
	}
	return &Tickets{gh: gh, redact: redact}
}

// WithGitLab enables the tickets of GitLab repositories: reads is the GitLab client and resolve
// finds the connection by a repository's host key. Without it a GitLab ticket is a failed read
// ("No GitLab connection for <host>") with no request. It returns t for chaining.
func (t *Tickets) WithGitLab(reads GitLabReads, resolve gitlab.ResolveFunc) *Tickets {
	t.gl = gitLabSide{reads: reads, resolve: resolve}
	return t
}

// Read reads an issue of the repository. A ticket of a GitLab repository is read from GitLab
// only (readGitLab; never a GitHub request, FR23). A number that cannot name an issue (not a positive safe
// integer: JavaScript's Number of an absurdly long digit run is Infinity) fails as GitHub would
// answer it, without a request. A failure is data: "GITHUB_TOKEN is not configured" without a
// token, GitHub's message for an error status, else the error's text; the token is redacted and
// the text bounded.
func (t *Tickets) Read(ctx context.Context, repo domain.Repo, number float64) app.TicketRead {
	if repo.IsGitLab() {
		return t.readGitLab(ctx, repo, number)
	}
	if !(number >= 1 && number <= 1<<53-1) || number != math.Trunc(number) {
		return app.TicketRead{Reason: notFoundReason}
	}
	issue, err := t.gh.GetIssue(ctx, repo.Owner, repo.Name, int(number))
	if err != nil {
		return app.TicketRead{Reason: t.reason(err)}
	}
	body := ""
	if issue.Body != nil {
		body = *issue.Body
	}
	return app.TicketRead{OK: true, Title: issue.Title, Body: body}
}

// readGitLab reads GET /projects/<project id>/issues/<n> with the repository's connection (FR27).
// A connection that is absent or has no token fails with no request. A failure is GitLab's
// message ("404 Not found" for a missing or invisible issue) or the error's text, with the
// connection's token and the redactor's secrets removed, cut to 1000 bytes.
func (t *Tickets) readGitLab(ctx context.Context, repo domain.Repo, number float64) app.TicketRead {
	b, state := t.gl.connection(ctx, repo)
	switch state {
	case connNone:
		return app.TicketRead{Reason: "No GitLab connection for " + repo.Host}
	case connNoToken:
		return app.TicketRead{Reason: "GitLab connection " + b.Name + " has no token"}
	case connUnreadable:
		return app.TicketRead{Reason: glUnreadableReason}
	}
	if !(number >= 1 && number <= 1<<53-1) || number != math.Trunc(number) {
		return app.TicketRead{Reason: glNotFoundReason}
	}
	issue, err := t.gl.reads.Issue(ctx, b.Conn(), repo.ProjectID, int(number))
	if err != nil {
		return app.TicketRead{Reason: t.glReason(err, b.Token)}
	}
	body := ""
	if issue.Body != nil {
		body = *issue.Body
	}
	return app.TicketRead{OK: true, Title: issue.Title, Body: body}
}

// glReason is the stored text of a failed GitLab read.
func (t *Tickets) glReason(err error, token string) string {
	text := err.Error()
	var status *gitlab.StatusError
	if errors.As(err, &status) {
		text = status.Message
	}
	if tok := strings.TrimSpace(token); tok != "" {
		text = strings.ReplaceAll(text, tok, "[REDACTED]")
	}
	if t.redact != nil {
		text = t.redact(text)
	}
	return cutBytes(text, maxReasonBytes)
}

func (t *Tickets) reason(err error) string {
	if errors.Is(err, github.ErrNoToken) {
		return noTokenReason
	}
	text := err.Error()
	var status *github.StatusError
	if errors.As(err, &status) {
		text = status.Message
	}
	if t.redact != nil {
		text = t.redact(text)
	}
	return cutBytes(text, maxReasonBytes)
}

// cutBytes cuts s to at most max bytes without splitting a character.
func cutBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
