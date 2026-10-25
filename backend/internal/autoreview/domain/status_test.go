package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/autoreview/domain"
)

func TestStateOf(t *testing.T) {
	ok := func() domain.Facts {
		return domain.Facts{Enabled: true, PollEnabled: true, HasGitHubToken: true, EffectiveAgents: 1, OnNewPR: true, OnNewCommits: true}
	}
	tests := []struct {
		name string
		mod  func(*domain.Facts)
		want domain.State
	}{
		{"active", func(*domain.Facts) {}, domain.StateActive},
		{"off", func(f *domain.Facts) { f.Enabled = false }, domain.StateOff},
		{"poll disabled", func(f *domain.Facts) { f.PollEnabled = false }, domain.StatePollDisabled},
		{"no token", func(f *domain.Facts) { f.HasGitHubToken = false }, domain.StateNoGitHubToken},
		{"no agents", func(f *domain.Facts) { f.EffectiveAgents = 0 }, domain.StateNoAgents},
		{"one trigger is enough", func(f *domain.Facts) { f.OnNewCommits = false }, domain.StateActive},
		{"no triggers", func(f *domain.Facts) { f.OnNewPR, f.OnNewCommits = false, false }, domain.StateNoTriggers},
		{"cap reached", func(f *domain.Facts) { f.CapReached = true }, domain.StatePausedCap},
		{"legacy true: enabled, nothing selected (EC16)", func(f *domain.Facts) { f.EffectiveAgents = 0 }, domain.StateNoAgents},
		// precedence: off > poll_disabled > no_github_token > no_agents > no_triggers > paused_spend_cap
		{"off wins over everything", func(f *domain.Facts) {
			*f = domain.Facts{CapReached: true}
		}, domain.StateOff},
		{"poll disabled wins over token, agents, triggers, cap", func(f *domain.Facts) {
			*f = domain.Facts{Enabled: true, CapReached: true}
		}, domain.StatePollDisabled},
		{"no token wins over agents, triggers, cap", func(f *domain.Facts) {
			*f = domain.Facts{Enabled: true, PollEnabled: true, CapReached: true}
		}, domain.StateNoGitHubToken},
		{"no agents wins over triggers and cap", func(f *domain.Facts) {
			*f = domain.Facts{Enabled: true, PollEnabled: true, HasGitHubToken: true, CapReached: true}
		}, domain.StateNoAgents},
		{"no triggers wins over cap", func(f *domain.Facts) {
			f.OnNewPR, f.OnNewCommits, f.CapReached = false, false, true
		}, domain.StateNoTriggers},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := ok()
			tc.mod(&f)
			if got := domain.StateOf(f); got != tc.want {
				t.Errorf("StateOf(%+v) = %q, want %q", f, got, tc.want)
			}
		})
	}
}

func TestNewStatus(t *testing.T) {
	polled := t0
	tests := []struct {
		name     string
		interval time.Duration
		want     int
	}{
		{"five minutes", 5 * time.Minute, 5},
		{"one hour", time.Hour, 60},
		{"max clamp", 24 * time.Hour, 1440},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.NewStatus(domain.StateActive, &polled, 7, tc.interval)
			want := domain.Status{State: domain.StateActive, LastPollAt: &polled, PRsTracked: 7, PollIntervalMin: tc.want}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("NewStatus(..., %v) mismatch (-want +got):\n%s", tc.interval, diff)
			}
		})
	}
	if got := domain.NewStatus(domain.StateOff, nil, 0, time.Minute); got.LastPollAt != nil {
		t.Errorf("NewStatus(nil last poll).LastPollAt = %v, want nil", got.LastPollAt)
	}
}
