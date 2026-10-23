package domain

import "time"

// Background-poll rules (leaderboard spec FR3-FR5).
const (
	// DefaultPollInterval is used when a workspace has no usable polling_interval_min.
	DefaultPollInterval = 5 * time.Minute
	MinPollInterval     = time.Minute
	MaxPollInterval     = 24 * time.Hour
	// PollHorizon is how far back the first background poll of a repository looks.
	PollHorizon = 62 * 24 * time.Hour
	// MaxRateLimitWait caps how long a reported rate-limit reset can pause the poller; GitHub's
	// windows last at most an hour, so a later reset is a bogus header.
	MaxRateLimitWait = 2 * time.Hour
)

// PollTarget is a repository the background poll refreshes, with the workspace that owns it.
type PollTarget struct {
	WorkspaceID string
	Repo        Repo
}

// ClampPollInterval turns a workspace's polling interval into the one the poller uses: a missing
// or non-positive value reads as the default, anything else is clamped to 1 min .. 24 h.
func ClampPollInterval(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultPollInterval
	case d < MinPollInterval:
		return MinPollInterval
	case d > MaxPollInterval:
		return MaxPollInterval
	default:
		return d
	}
}

// ReachedCutoff reports whether a listed PR was not updated since cutoff, which ends the paging
// (the list is newest-update-first). A PR without an update time never ends it.
func ReachedCutoff(l Listed, cutoff time.Time) bool {
	return l.UpdatedAt != nil && l.UpdatedAt.Before(cutoff)
}

// ResumeAfterRateLimit returns when a workspace may poll again after a rate limit: the later of
// its next due time and GitHub's reset (a zero reset is unknown and counts as "not later"; a
// reset beyond MaxRateLimitWait from start is cut to it).
func ResumeAfterRateLimit(start, nextDue, reset time.Time) time.Time {
	if reset.IsZero() {
		return nextDue
	}
	if limit := start.Add(MaxRateLimitWait); reset.After(limit) {
		reset = limit
	}
	if reset.After(nextDue) {
		return reset
	}
	return nextDue
}
