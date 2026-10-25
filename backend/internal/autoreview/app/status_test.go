package app_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/autoreview/domain"
)

// FR12: the state follows the precedence off > poll_disabled > no_github_token > no_agents >
// no_triggers > paused_spend_cap > active.
func TestStatus_States(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*fakes)
		want domain.State
	}{
		{"armed and healthy", func(*fakes) {}, domain.StateActive},
		{"disabled wins over everything", func(f *fakes) {
			f.setting.Enabled, f.hasToken, f.capReached, f.pollOff = false, false, true, true
		}, domain.StateOff},
		{"poll disabled", func(f *fakes) { f.pollOff = true }, domain.StatePollDisabled},
		{"poll disabled beats no token", func(f *fakes) { f.pollOff, f.hasToken = true, false }, domain.StatePollDisabled},
		{"no token", func(f *fakes) { f.hasToken = false }, domain.StateNoGitHubToken},
		{"legacy true: no agent ids", func(f *fakes) { f.setting.AgentIDs = nil }, domain.StateNoAgents},
		{"selected agents all disabled or deleted (EC9)", func(f *fakes) { f.enabled["a1"] = false }, domain.StateNoAgents},
		{"no triggers", func(f *fakes) { f.setting.OnNewPR, f.setting.OnNewCommits = false, false }, domain.StateNoTriggers},
		{"cap reached", func(f *fakes) { f.capReached = true }, domain.StatePausedCap},
		{"no token beats cap", func(f *fakes) { f.hasToken, f.capReached = false, true }, domain.StateNoGitHubToken},
		{"no triggers beats cap", func(f *fakes) {
			f.setting.OnNewPR, f.setting.OnNewCommits, f.capReached = false, false, true
		}, domain.StateNoTriggers},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			tc.mod(f)
			got, err := f.service(nil).Status(t.Context(), ws)
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got.State != tc.want {
				t.Errorf("Status().State = %s, want %s", got.State, tc.want)
			}
		})
	}
}

func TestStatus_FactsAndInterval(t *testing.T) {
	f := newFakes()
	polled := t0.Add(-3 * time.Minute)
	f.facts.LastPollAt, f.facts.PRsTracked = &polled, 7
	f.interval = 90 * time.Second
	got, err := f.service(nil).Status(t.Context(), ws)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := domain.Status{State: domain.StateActive, LastPollAt: &polled, PRsTracked: 7, PollIntervalMin: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Status() mismatch (-want +got):\n%s", diff)
	}
}

// A9: the cap is read first, through the log-free port; no cap means not paused.
func TestStatus_CapReadFirstAndNoCapIsNotPaused(t *testing.T) {
	f := newFakes()
	got, err := f.service(nil).Status(t.Context(), ws)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.State == domain.StatePausedCap {
		t.Errorf("Status().State = %s without a cap, want not paused", got.State)
	}
	if calls := f.callLog(); len(calls) == 0 || calls[0] != "cap" {
		t.Errorf("Status() calls = %v, want the cap read first", calls)
	}
	if f.budgetCalls != 0 {
		t.Errorf("Status() asked Budget.Admit %d times, want 0 (it would log a refusal)", f.budgetCalls)
	}
}

func TestStatus_NoSelectedAgentsSkipsAgentLookup(t *testing.T) {
	f := newFakes()
	f.setting.AgentIDs = nil
	if _, err := f.service(nil).Status(t.Context(), ws); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if slices.Contains(f.callLog(), "enabled_agents") {
		t.Errorf("Status() looked up agents for an empty selection: %v", f.callLog())
	}
}

func TestStatus_ReadErrorsAreWrapped(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*fakes)
	}{
		{"cap", func(f *fakes) { f.capErr = errBoom }},
		{"setting", func(f *fakes) { f.settErr = errBoom }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			tc.mod(f)
			_, err := f.service(nil).Status(t.Context(), ws)
			if !errors.Is(err, errBoom) {
				t.Errorf("Status() error = %v, want it to wrap the read error", err)
			}
		})
	}
}
