package domain

import "time"

// State is the state of the status line (FR12).
type State string

// The states, in order of precedence: the first that holds wins.
const (
	StateOff           State = "off"
	StatePollDisabled  State = "poll_disabled"
	StateNoGitHubToken State = "no_github_token"
	// StateNoCodeHostToken is answered in place of StateNoGitHubToken once the workspace has a
	// GitLab repository (FR28).
	StateNoCodeHostToken State = "no_code_host_token"
	StateNoAgents        State = "no_agents"
	StateNoTriggers      State = "no_triggers"
	StatePausedCap       State = "paused_spend_cap"
	StateActive          State = "active"
)

// Facts are the inputs of State.
type Facts struct {
	// Enabled is the setting's `enabled` flag.
	Enabled bool
	// PollEnabled is false when the background poll is switched off for the deployment.
	PollEnabled bool
	// CodeHost says whether the workspace's repositories can be polled (FR28).
	CodeHost CodeHostAccess
	// EffectiveAgents is the number of selected agents that exist and are enabled.
	EffectiveAgents int
	OnNewPR         bool
	OnNewCommits    bool
	// CapReached is true when the spend cap does not allow new LLM work (false without a cap, A9).
	CapReached bool
}

// CodeHostAccess are the token facts of a workspace's repositories across code hosts (FR28).
type CodeHostAccess struct {
	// GitLabRepos is true when the workspace has at least one GitLab repository.
	GitLabRepos bool
	// GitHubToken is true when a GitHub token is stored.
	GitHubToken bool
	// RepoWithToken is true when at least one repository of the workspace has a token for its host:
	// the GitHub token for a GitHub repository, the connection's token for a GitLab repository
	// (a repository without a connection has none).
	RepoWithToken bool
}

// Missing reports whether no repository can be polled for lack of a token. A workspace without
// GitLab repositories keeps the GitHub rule (the stored GitHub token, even with no repository);
// with one, the question is whether any repository of the workspace has a token for its host.
func (a CodeHostAccess) Missing() bool {
	if a.GitLabRepos {
		return !a.RepoWithToken
	}
	return !a.GitHubToken
}

// StateOf derives the status state with the precedence off > poll_disabled > no_github_token
// (no_code_host_token with a GitLab repository, FR28) > no_agents > no_triggers > paused_spend_cap >
// active (FR12).
func StateOf(f Facts) State {
	switch {
	case !f.Enabled:
		return StateOff
	case !f.PollEnabled:
		return StatePollDisabled
	case f.CodeHost.Missing() && f.CodeHost.GitLabRepos:
		return StateNoCodeHostToken
	case f.CodeHost.Missing():
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
