package peers

import (
	"context"
	"errors"
	"math"
	"unicode/utf8"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/github"
)

// Issues is the part of *github.Client the ticket reader uses.
type Issues interface {
	GetIssue(ctx context.Context, owner, name string, number int) (github.Issue, error)
}

// Tickets implements app.Tickets over the GitHub issues API.
type Tickets struct {
	gh     Issues
	redact func(string) string
}

var _ app.Tickets = (*Tickets)(nil)

// Texts of failed ticket reads that are not GitHub's own (spec 7.13, D5006).
const (
	noTokenReason  = "GITHUB_TOKEN is not configured"
	notFoundReason = "Not Found"
	// gitlabReason is the failure of a ticket of a GitLab repository: issues are read from GitHub
	// only (FR29), so none is requested.
	gitlabReason = "GitLab issues are not available yet"
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

// Read reads an issue of the repository. A ticket of a GitLab repository is a failed read with
// no request (FR29). A number that cannot name an issue (not a positive safe
// integer: JavaScript's Number of an absurdly long digit run is Infinity) fails as GitHub would
// answer it, without a request. A failure is data: "GITHUB_TOKEN is not configured" without a
// token, GitHub's message for an error status, else the error's text; the token is redacted and
// the text bounded.
func (t *Tickets) Read(ctx context.Context, repo domain.Repo, number float64) app.TicketRead {
	if repo.IsGitLab() {
		return app.TicketRead{Reason: gitlabReason}
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
