package app

import (
	"context"
	"errors"

	"dev-digest-go/backend/internal/pulls/domain"
)

// ErrGitLabRedirect is returned by the GitLab comments port when the instance answered with a
// redirect, which is never followed (spec B FR10 reason "redirect").
var ErrGitLabRedirect = errors.New("gitlab answered with a redirect")

// ErrGitLabUnavailable is returned by the GitLab comments port when no usable answer arrived: a
// timeout, a connection or TLS error, or a 2xx answer that is not the expected JSON (spec B FR10
// reason "unavailable").
var ErrGitLabUnavailable = errors.New("gitlab unavailable")

// GitLabComments is the GitLab access of the merge request comment use cases (spec B FR1–FR11).
// Like GitLab, the adapter resolves the connection by repo.Host on every call and addresses the
// project by repo.ProjectID. Every method fails with ErrNotConnected or *NoTokenError before any
// request, and otherwise with *GitLabError (non-2xx; RetryAfterSeconds and InsufficientScope set),
// ErrGitLabRedirect or ErrGitLabUnavailable. Reads retry transient failures inside the adapter;
// CreateDiscussion and CreateNote send exactly one request and never retry (plan B P4).
type GitLabComments interface {
	// Ready resolves the repository's connection without sending a request: nil when a connection
	// with a token exists, else ErrNotConnected or *NoTokenError.
	Ready(ctx context.Context, repo domain.Repo) error
	// Discussions reads one page (1-based) of the merge request's discussions. more is true when
	// the page was full, so a next page may exist (page numbers only, never X-Next-Page, SCR1).
	Discussions(ctx context.Context, repo domain.Repo, number, page int) (ds []domain.Discussion, more bool, err error)
	// MergeRequestRefs reads the merge request's diff_refs (FR7); fields GitLab omits are "".
	MergeRequestRefs(ctx context.Context, repo domain.Repo, number int) (domain.DiffRefs, error)
	// LatestVersion reads the newest merge request diff version (per_page=1). ok is false when
	// the merge request has no version yet (the caller answers diff_not_ready).
	LatestVersion(ctx context.Context, repo domain.Repo, number int) (refs domain.DiffRefs, ok bool, err error)
	// CreateDiscussion posts a new positioned diff discussion with body (already passed through
	// notebody.Apply by the caller) and returns it with its first note. One request, no retry.
	CreateDiscussion(ctx context.Context, repo domain.Repo, number int, body string, pos domain.Position) (domain.Discussion, error)
	// CreateNote posts a reply note into the discussion discussionID. One request, no retry.
	CreateNote(ctx context.Context, repo domain.Repo, number int, discussionID, body string) (domain.Note, error)
}
