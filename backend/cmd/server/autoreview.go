package main

import (
	"context"
	"fmt"
	"time"

	autoreviewapp "dev-digest-go/backend/internal/autoreview/app"
	autoreviewdomain "dev-digest-go/backend/internal/autoreview/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	pullsdomain "dev-digest-go/backend/internal/pulls/domain"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	settingsapp "dev-digest-go/backend/internal/settings/app"
	spendapp "dev-digest-go/backend/internal/spend/app"
	spenddomain "dev-digest-go/backend/internal/spend/domain"
)

// The adapters below bind the automatic-reviews context (FR3, FR7, FR12) to settings, pulls,
// reviews, spend and secrets. None of those contexts is imported by autoreview/app: only this
// composition root knows both sides.

// autoSettings adapts the settings bag to autoreview's Settings port.
type autoSettings struct{ settings settingsBag }

// AutomaticReviews reads the stored value as the full object (defaults filled) and turns enabled_at
// into a time. A stored value that cannot be read is already the defaults, so only the bag read can
// fail; an enabled_at that does not parse reads as not armed (nil), the conservative answer.
func (a autoSettings) AutomaticReviews(ctx context.Context, workspaceID string) (autoreviewdomain.Setting, error) {
	bag, err := a.settings.Get(ctx, workspaceID)
	if err != nil {
		return autoreviewdomain.Setting{}, fmt.Errorf("read settings: %w", err)
	}
	v := settingsapp.AutomaticReviewsFromStored(bag[settingsapp.AutomaticReviewsKey])
	out := autoreviewdomain.Setting{
		Enabled:      v.Enabled,
		AgentIDs:     v.AgentIDs,
		OnNewPR:      v.OnNewPR,
		OnNewCommits: v.OnNewCommits,
	}
	if v.EnabledAt != nil {
		if at, err := time.Parse(time.RFC3339Nano, *v.EnabledAt); err == nil {
			out.EnabledAt = &at
		}
	}
	return out, nil
}

// autoInterval gives the poller's effective interval: the workspace's polling_interval_min, clamped
// exactly as the poller clamps it (a missing value is the default), so the status line and the
// poller agree.
type autoInterval struct{ intervals settingsIntervals }

// Interval returns the clamped interval.
func (a autoInterval) Interval(ctx context.Context, workspaceID string) (time.Duration, error) {
	d, err := a.intervals.Interval(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	return pullsdomain.ClampPollInterval(d), nil
}

// autoStarter adapts the reviews context's AutoStarter to autoreview's Starter port.
type autoStarter struct{ reviews reviewsapp.AutoStarter }

// StartAuto starts the agents on the PR with source "auto" and returns the number of runs created.
// A reached cap (the reviews start's own 402) becomes autoreviewapp.ErrCapReached; any other error
// passes through.
func (s autoStarter) StartAuto(ctx context.Context, workspaceID, prID string, agentIDs []string) (int, error) {
	res, err := s.reviews.StartAuto(ctx, workspaceID, prID, agentIDs)
	if err != nil {
		return 0, capError(err)
	}
	return len(res.Runs), nil
}

// capError wraps a 402 apperr in autoreviewapp.ErrCapReached (keeping the cause) and returns any
// other error unchanged.
func capError(err error) error {
	if ae, ok := apperr.As(err); ok && ae.Kind == apperr.KindPaymentRequired {
		return fmt.Errorf("%w: %w", autoreviewapp.ErrCapReached, err)
	}
	return err
}

// autoBudget adapts spend's Admit (operation auto_review_start) to autoreview's Budget port.
type autoBudget struct{ spend spendAdmitter }

// spendAdmitter is the cap check of an LLM entry point (spendapp.API.Admit).
type spendAdmitter interface {
	Admit(ctx context.Context, workspaceID, op string) error
}

// Admit is nil when new work may start, ErrCapReached-wrapped when the cap is reached, and any other
// error when the check failed.
func (b autoBudget) Admit(ctx context.Context, workspaceID string) error {
	if err := b.spend.Admit(ctx, workspaceID, spendapp.OpAutoReviewStart); err != nil {
		return capError(err)
	}
	return nil
}

// autoCaps adapts spend's Status to autoreview's Caps port: the cap state for the status line, read
// without Admit so that no refusal is logged (A9).
type autoCaps struct{ spend spendStatuser }

// spendStatuser is the month status of spend (spendapp.API.Status).
type spendStatuser interface {
	Status(ctx context.Context, workspaceID string) (spenddomain.Status, error)
}

// CapReached reports whether a cap is set and reached; false without a cap.
func (c autoCaps) CapReached(ctx context.Context, workspaceID string) (bool, error) {
	st, err := c.spend.Status(ctx, workspaceID)
	if err != nil {
		return false, fmt.Errorf("read spend status: %w", err)
	}
	return st.Reached(), nil
}

// autoTokens adapts the secrets store to autoreview's Tokens port. The GitHub token is one value for
// the whole local install, so the workspace does not matter.
type autoTokens struct{ token func() string }

// HasGitHubToken reports whether a GitHub token is stored.
func (t autoTokens) HasGitHubToken(context.Context, string) (bool, error) {
	return t.token() != "", nil
}
