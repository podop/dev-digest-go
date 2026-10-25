package domain

import "time"

// State is the state of the status line (FR12).
type State string

// The states, in order of precedence: the first that holds wins.
const (
	StateOff           State = "off"
	StatePollDisabled  State = "poll_disabled"
	StateNoGitHubToken State = "no_github_token"
	StateNoAgents      State = "no_agents"
	StateNoTriggers    State = "no_triggers"
	StatePausedCap     State = "paused_spend_cap"
	StateActive        State = "active"
)

// Facts are the inputs of State.
type Facts struct {
	// Enabled is the setting's `enabled` flag.
	Enabled bool
	// PollEnabled is false when the background poll is switched off for the deployment.
	PollEnabled    bool
	HasGitHubToken bool
	// EffectiveAgents is the number of selected agents that exist and are enabled.
	EffectiveAgents int
	OnNewPR         bool
	OnNewCommits    bool
	// CapReached is true when the spend cap does not allow new LLM work (false without a cap, A9).
	CapReached bool
}

// StateOf derives the status state with the precedence off > poll_disabled > no_github_token >
// no_agents > no_triggers > paused_spend_cap > active (FR12).
func StateOf(f Facts) State {
	switch {
	case !f.Enabled:
		return StateOff
	case !f.PollEnabled:
		return StatePollDisabled
	case !f.HasGitHubToken:
		return StateNoGitHubToken
	case f.EffectiveAgents == 0:
		return StateNoAgents
	case !f.OnNewPR && !f.OnNewCommits:
		return StateNoTriggers
	case f.CapReached:
		return StatePausedCap
	default:
		return StateActive
	}
}

// Status is GET /settings/automatic-reviews-status (spec 7.2). The values are derived on read.
type Status struct {
	State State
	// LastPollAt is the newest last-polled time of the workspace's repositories; nil when none was polled.
	LastPollAt *time.Time
	// PRsTracked counts the workspace's stored open PRs, drafts included (D12).
	PRsTracked int
	// PollIntervalMin is the effective poll interval in minutes after clamping.
	PollIntervalMin int
}

// NewStatus builds the status; interval is the effective (clamped) poll interval, which is
// reported in whole minutes.
func NewStatus(state State, lastPollAt *time.Time, prsTracked int, interval time.Duration) Status {
	return Status{
		State:           state,
		LastPollAt:      lastPollAt,
		PRsTracked:      prsTracked,
		PollIntervalMin: int(interval / time.Minute),
	}
}
