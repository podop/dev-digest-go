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

// DueAt is when a workspace's next cycle is due: the start of its last cycle plus the polling
// interval it has now (spec D17). It is worked out on every tick, never stored, so a changed
// interval applies within one tick. A rate limit no longer moves it: it pauses a host (HostPauses).
func DueAt(lastStart time.Time, interval time.Duration) time.Time {
	return lastStart.Add(interval)
}

// IsDue reports whether a cycle due at due may start now. Ticks are tick apart, so a cycle that
// began a hair after its tick would otherwise miss the next one; half a tick of slack keeps a
// 1-minute interval at one minute.
func IsDue(now, due time.Time, tick time.Duration) bool {
	return !now.Add(tick / 2).Before(due)
}

// HostPauses holds the rate-limit pauses of the code hosts the poller talks to, by pause key
// (Repo.PauseKey): GitHub and each GitLab instance have their own (spec FR27, P9). A pause stops
// polling the repositories of that host only. It is owned by one goroutine and not safe for
// concurrent use.
type HostPauses struct {
	until map[string]time.Time
}

// NewHostPauses returns an empty set.
func NewHostPauses() *HostPauses { return &HostPauses{until: map[string]time.Time{}} }

// Pause pauses the host until resume. A later pause never shortens an earlier one.
func (h *HostPauses) Pause(key string, resume time.Time) {
	if cur, ok := h.until[key]; ok && !resume.After(cur) {
		return
	}
	h.until[key] = resume
}

// Paused reports whether the host is paused at now (it resumes at the resume time itself).
func (h *HostPauses) Paused(key string, now time.Time) bool {
	resume, ok := h.until[key]
	return ok && now.Before(resume)
}

// ResumeAt returns when the host resumes; ok is false when it has no pause.
func (h *HostPauses) ResumeAt(key string) (time.Time, bool) {
	resume, ok := h.until[key]
	return resume, ok
}

// Prune drops the pauses that ended by now.
func (h *HostPauses) Prune(now time.Time) {
	for key, resume := range h.until {
		if !now.Before(resume) {
			delete(h.until, key)
		}
	}
}
