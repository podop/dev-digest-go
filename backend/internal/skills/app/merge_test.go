package app_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
)

const (
	repoID = "33333333-3333-4333-8333-333333333333"
	c1     = "c1000000-0000-4000-8000-000000000001"
	c2     = "c2000000-0000-4000-8000-000000000002"
	pend   = "c3000000-0000-4000-8000-000000000003"
	agentX = "a1000000-0000-4000-8000-00000000000a"
	agentY = "a2000000-0000-4000-8000-00000000000b"
	agentU = "a3000000-0000-4000-8000-00000000000c"
)

type mergeEnv struct {
	env
	merger *app.Merger
}

func newMergeEnv(t *testing.T) mergeEnv {
	t.Helper()
	e := newEnv(t)
	e.w.repos[repoID] = "o/r"
	e.w.accepted[c1], e.w.accepted[c2] = true, true
	e.w.agents[agentX] = domain.AgentRef{ID: agentX, Name: "X", Enabled: true}
	e.w.agents[agentY] = domain.AgentRef{ID: agentY, Name: "Y", Enabled: false}
	e.w.links[agentX] = []string{"t"}
	m := app.NewMerger(app.MergerDeps{
		Tx: &memTx{w: e.w}, Skills: e.svc, Lookup: &memStore{w: e.w}, Agents: fakeAgents{w: e.w}, Conventions: fakeConventions{w: e.w},
	})
	return mergeEnv{env: e, merger: m}
}

func input() app.MergeInput {
	return app.MergeInput{
		ConventionIDs: []string{c1, c2, c1}, AgentIDs: []string{agentX, agentY},
		Name: "house-rules", Type: domain.TypeConvention, Body: "rules", Enabled: true,
	}
}

func TestMerger_Merge(t *testing.T) {
	m := newMergeEnv(t)
	got, err := m.merger.Merge(t.Context(), wsID, repoID, input())
	if err != nil {
		t.Fatalf("Merge error = %v", err)
	}
	sk := got.Skill
	if sk.Source != domain.SourceExtracted || sk.SourceRef == nil || *sk.SourceRef != "conventions:o/r" ||
		sk.Type != domain.TypeConvention || !sk.Enabled || sk.UsedBy != 2 || sk.Version != 1 {
		t.Errorf("Merge skill = %+v, want extracted from conventions:o/r, enabled, used_by 2", sk)
	}
	wantAgents := []domain.AgentRef{{ID: agentX, Name: "X", Enabled: true}, {ID: agentY, Name: "Y"}}
	if diff := cmp.Diff(wantAgents, got.LinkedAgents); diff != "" {
		t.Errorf("Merge linked agents mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string][]string{agentX: {"t", sk.ID}, agentY: {sk.ID}}, m.w.links); diff != "" {
		t.Errorf("agent links mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]string{c1: sk.ID, c2: sk.ID}, m.w.marked); diff != "" {
		t.Errorf("marked conventions mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"Extracted from conventions of o/r"}, versionMessages(t, m.env, sk.ID)); diff != "" {
		t.Errorf("snapshots mismatch (-want +got):\n%s", diff)
	}
}

func TestMerger_MergeKeepsEnabledFalse(t *testing.T) {
	m := newMergeEnv(t)
	in := input()
	in.Enabled = false
	got, err := m.merger.Merge(t.Context(), wsID, repoID, in)
	if err != nil || got.Skill.Enabled {
		t.Errorf("Merge(enabled false) = enabled %v, %v, want a disabled skill", got.Skill.Enabled, err)
	}
}

func TestMerger_MergeWithoutAgentsLinksNothing(t *testing.T) {
	m := newMergeEnv(t)
	in := input()
	in.AgentIDs = nil
	got, err := m.merger.Merge(t.Context(), wsID, repoID, in)
	if err != nil {
		t.Fatalf("Merge error = %v", err)
	}
	if got.Skill.UsedBy != 0 || len(got.LinkedAgents) != 0 || got.LinkedAgents == nil {
		t.Errorf("Merge = used_by %d agents %v, want 0 and an empty, non-nil list", got.Skill.UsedBy, got.LinkedAgents)
	}
}

func TestMerger_MergeDeduplicatesAgentsCaseInsensitively(t *testing.T) {
	m := newMergeEnv(t)
	in := input()
	in.AgentIDs = []string{agentX, strings.ToUpper(agentX)}
	got, err := m.merger.Merge(t.Context(), wsID, repoID, in)
	if err != nil || len(got.LinkedAgents) != 1 || len(m.w.links[agentX]) != 2 {
		t.Errorf("Merge(duplicate agent) = %v, %v, links %v, want the agent linked once", got.LinkedAgents, err, m.w.links[agentX])
	}
}

func TestMerger_MergeFailuresLeaveNothingBehind(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *mergeEnv, *app.MergeInput)
		wantErr func(*testing.T, error)
	}{
		{
			name:   "unknown repo",
			mutate: func(_ *testing.T, m *mergeEnv, _ *app.MergeInput) { delete(m.w.repos, repoID) },
			wantErr: func(t *testing.T, err error) {
				wantAppErr(t, err, apperr.KindNotFound, "not_found", "Repo not found")
			},
		},
		{
			name:   "convention not accepted",
			mutate: func(_ *testing.T, _ *mergeEnv, in *app.MergeInput) { in.ConventionIDs = []string{c1, pend} },
			wantErr: func(t *testing.T, err error) {
				ae := wantAppErr(t, err, apperr.KindValidation, "validation_error", "Only accepted conventions of this repo can be merged into a skill")
				if diff := cmp.Diff(map[string][]string{"ids": {pend}}, ae.Details); diff != "" {
					t.Errorf("details mismatch (-want +got):\n%s", diff)
				}
			},
		},
		{
			name:   "unknown agent",
			mutate: func(_ *testing.T, _ *mergeEnv, in *app.MergeInput) { in.AgentIDs = []string{agentX, agentU, "zzz"} },
			wantErr: func(t *testing.T, err error) {
				ae := wantAppErr(t, err, apperr.KindValidation, "unknown_agent", "Unknown agent")
				if diff := cmp.Diff(map[string]string{"id": agentU}, ae.Details); diff != "" {
					t.Errorf("details mismatch (-want +got):\n%s", diff)
				}
			},
		},
		{
			name:   "duplicate skill name",
			mutate: func(t *testing.T, m *mergeEnv, _ *app.MergeInput) { m.create(t, "house-rules") },
			wantErr: func(t *testing.T, err error) {
				wantAppErr(t, err, apperr.KindConflict, "conflict", `A skill named "house-rules" already exists in this workspace`)
			},
		},
		{
			name:   "linking an agent fails after the skill was created",
			mutate: func(_ *testing.T, m *mergeEnv, _ *app.MergeInput) { m.w.failAppend = errBoom },
			wantErr: func(t *testing.T, err error) {
				if err == nil {
					t.Error("Merge error = nil, want the link failure")
				}
			},
		},
		{
			name:   "marking the conventions fails after the agents were linked",
			mutate: func(_ *testing.T, m *mergeEnv, _ *app.MergeInput) { m.w.failAttach = errBoom },
			wantErr: func(t *testing.T, err error) {
				if err == nil {
					t.Error("Merge error = nil, want the attach failure")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newMergeEnv(t)
			in := input()
			tc.mutate(t, &m, &in)
			before := len(m.w.skills)

			_, err := m.merger.Merge(t.Context(), wsID, repoID, in)
			tc.wantErr(t, err)

			if len(m.w.skills) != before {
				t.Errorf("skills after failed Merge = %d, want %d", len(m.w.skills), before)
			}
			if diff := cmp.Diff(map[string][]string{agentX: {"t"}}, m.w.links); diff != "" {
				t.Errorf("agent links after failed Merge mismatch (-want +got):\n%s", diff)
			}
			if len(m.w.marked) != 0 {
				t.Errorf("marked conventions after failed Merge = %v, want none", m.w.marked)
			}
		})
	}
}
