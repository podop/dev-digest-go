package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/autoreview/domain"
)

// ErrCapReached is returned by Budget.Admit and Starter.StartAuto when the Slice A spend cap does
// not allow new LLM work. The adapters bound in cmd/server translate spend's 402 spend_cap_reached
// into it, so this package never imports spend or reviews (FR6 d, A2).
var ErrCapReached = errors.New("spend cap reached")

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// Settings reads the workspace's `automatic_reviews` value as the full object, defaults filled.
type Settings interface {
	AutomaticReviews(ctx context.Context, workspaceID string) (domain.Setting, error)
}

// Interval gives the effective background-poll interval of a workspace, already clamped to
// 1 min .. 24 h (the same rule the poller uses).
type Interval interface {
	Interval(ctx context.Context, workspaceID string) (time.Duration, error)
}

// PollFacts are the stored facts the status line shows.
type PollFacts struct {
	// LastPollAt is the newest last-polled time of the workspace's repositories; nil when none.
	LastPollAt *time.Time
	// PRsTracked counts the workspace's stored open PRs, drafts included.
	PRsTracked int
}

// Store is the persistence of the trigger. Every method only sees the given workspace's rows.
type Store interface {
	// Candidates returns the workspace's stored PRs that are open, not draft, opened at or after
	// enabledAt and have no record for their current head SHA (domain.Eligible), with the has-record
	// and running facts, ordered as domain.SortCandidates does. One statement (NFR1).
	Candidates(ctx context.Context, workspaceID string, enabledAt time.Time) ([]domain.Candidate, error)
	// EnabledAgents returns those of ids that exist and are enabled, in any order.
	EnabledAgents(ctx context.Context, workspaceID string, ids []string) ([]string, error)
	// StartedSince counts the workspace's `started` records decided at or after since.
	StartedSince(ctx context.Context, workspaceID string, since time.Time) (int, error)
	// Claim stores rec if no record exists for (PR, head SHA) and reports whether it did; false
	// with a nil error means another evaluation decided it first (FR8). A start is claimed with
	// OutcomeStarted and zero runs before the review starts, then settled with Finish.
	Claim(ctx context.Context, rec domain.Record) (claimed bool, err error)
	// Finish settles a claimed record after the start: OutcomeStarted with the number of runs, or
	// OutcomeFailed with zero.
	Finish(ctx context.Context, workspaceID, prID, headSHA string, outcome domain.Outcome, runs int) error
	// StatusFacts returns the facts of the status line.
	StatusFacts(ctx context.Context, workspaceID string) (PollFacts, error)
}

// Starter starts the automatic review through the reviews context's start with
// StartRequest.Source "auto" (FR7).
type Starter interface {
	// StartAuto starts the agents (ids, in order) on the PR and returns the number of runs created.
	// It returns an error wrapping ErrCapReached when the start itself is refused by the cap.
	StartAuto(ctx context.Context, workspaceID, prID string, agentIDs []string) (runs int, err error)
}

// Budget is the spend-cap check before an automatic start (operation auto_review_start, which the
// adapter supplies to spend's Admit).
type Budget interface {
	// Admit returns nil when new LLM work may start, an error wrapping ErrCapReached when the cap
	// is reached, and any other error when the check failed (the evaluation fails closed).
	Admit(ctx context.Context, workspaceID string) error
}

// Caps reads the cap state for the status line without logging an Admit refusal (A9).
type Caps interface {
	// CapReached reports whether the cap is set and reached; false without a cap.
	CapReached(ctx context.Context, workspaceID string) (bool, error)
}

// Tokens tells whether the workspace has a GitHub token stored.
type Tokens interface {
	HasGitHubToken(ctx context.Context, workspaceID string) (bool, error)
}

// Deps are what *Service is built from. Every field but PollEnabled is required.
type Deps struct {
	Settings Settings
	Interval Interval
	Store    Store
	Starter  Starter
	Budget   Budget
	Caps     Caps
	Tokens   Tokens
	// PollEnabled is false when the background poll is switched off for the deployment
	// (PR_POLL_ENABLED); the status line then reads poll_disabled.
	PollEnabled bool
	Now         Clock
	Log         *slog.Logger
}
