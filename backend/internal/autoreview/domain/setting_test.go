package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/autoreview/domain"
)

var t0 = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func armed() domain.Setting {
	return domain.Setting{Enabled: true, AgentIDs: []string{"a1"}, OnNewPR: true, OnNewCommits: true, EnabledAt: &t0}
}

func TestSettingArmed(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*domain.Setting)
		want bool
	}{
		{"all set", func(*domain.Setting) {}, true},
		{"disabled", func(s *domain.Setting) { s.Enabled = false }, false},
		{"no agents", func(s *domain.Setting) { s.AgentIDs = nil }, false},
		{"only new PR", func(s *domain.Setting) { s.OnNewCommits = false }, true},
		{"only new commits", func(s *domain.Setting) { s.OnNewPR = false }, true},
		{"no triggers", func(s *domain.Setting) { s.OnNewPR, s.OnNewCommits = false, false }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := armed()
			tc.mod(&s)
			if got := s.Armed(); got != tc.want {
				t.Errorf("Setting.Armed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSettingTicked(t *testing.T) {
	s := domain.Setting{OnNewPR: true}
	tests := []struct {
		event domain.Event
		want  bool
	}{
		{domain.EventNewPR, true},
		{domain.EventNewCommits, false},
		{domain.Event("other"), false},
	}
	for _, tc := range tests {
		if got := s.Ticked(tc.event); got != tc.want {
			t.Errorf("Setting.Ticked(%q) = %v, want %v", tc.event, got, tc.want)
		}
	}
}

func TestEffectiveAgents(t *testing.T) {
	tests := []struct {
		name              string
		selected, enabled []string
		want              []string
	}{
		{"keeps setting order, not the enabled order", []string{"c", "a", "b"}, []string{"a", "b", "c"}, []string{"c", "a", "b"}},
		{"drops disabled and deleted", []string{"a", "gone", "off", "b"}, []string{"a", "b"}, []string{"a", "b"}},
		{"drops duplicates keeping the first", []string{"a", "b", "a"}, []string{"a", "b"}, []string{"a", "b"}},
		{"none available", []string{"x"}, nil, []string{}},
		{"none selected", nil, []string{"a"}, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.EffectiveAgents(tc.selected, tc.enabled)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("EffectiveAgents(%v, %v) mismatch (-want +got):\n%s", tc.selected, tc.enabled, diff)
			}
		})
	}
}
