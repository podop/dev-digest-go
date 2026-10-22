package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviews/domain"
)

func ptr[T any](v T) *T { return &v }

func TestPlanTargets(t *testing.T) {
	twentyOne := make([]string, 21)
	for i := range twentyOne {
		twentyOne[i] = "a"
	}
	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = strings.Repeat("x", i+1)
	}
	tests := []struct {
		name     string
		agentID  string
		all      bool
		agentIDs []string
		want     domain.TargetPlan
		wantMsg  string
	}{
		{name: "agentId", agentID: "a1", want: domain.TargetPlan{Mode: domain.TargetAgent, AgentID: "a1"}},
		{name: "all", all: true, want: domain.TargetPlan{Mode: domain.TargetAll}},
		{name: "agentId with all resolves as all (EC4003)", agentID: "a1", all: true, want: domain.TargetPlan{Mode: domain.TargetAll}},
		{name: "agentIds keeps first-occurrence order", agentIDs: []string{"b", "a", "b", "c", "a"}, want: domain.TargetPlan{Mode: domain.TargetAgentIDs, AgentIDs: []string{"b", "a", "c"}}},
		{name: "duplicates are one agent (EC4001)", agentIDs: []string{"a", "a"}, want: domain.TargetPlan{Mode: domain.TargetAgentIDs, AgentIDs: []string{"a"}}},
		{name: "20 ids is the limit", agentIDs: twenty, want: domain.TargetPlan{Mode: domain.TargetAgentIDs, AgentIDs: twenty}},
		{name: "21 copies are counted before dedupe (EC4002)", agentIDs: twentyOne, wantMsg: "agentIds must contain 1–20 ids"},
		{name: "empty agentIds (EC4003)", agentIDs: []string{}, wantMsg: "agentIds must contain 1–20 ids"},
		{name: "agentIds with all (EC4003)", all: true, agentIDs: []string{"a"}, wantMsg: "Provide exactly one of agentId, all or agentIds"},
		{name: "agentIds with agentId", agentID: "a1", agentIDs: []string{"a"}, wantMsg: "Provide exactly one of agentId, all or agentIds"},
		{name: "empty agentIds with agentId reports the conflict first", agentID: "a1", agentIDs: []string{}, wantMsg: "Provide exactly one of agentId, all or agentIds"},
		{name: "nothing named", wantMsg: "Provide agentId, all:true or agentIds"},
		{name: "all:false alone (EC4003)", all: false, wantMsg: "Provide agentId, all:true or agentIds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.PlanTargets(tc.agentID, tc.all, tc.agentIDs)
			if tc.wantMsg != "" {
				var inv *domain.InvalidRunRequestError
				if !errors.As(err, &inv) || !errors.Is(err, domain.ErrInvalidRunRequest) {
					t.Fatalf("PlanTargets() error = %v, want *InvalidRunRequestError", err)
				}
				if err.Error() != tc.wantMsg {
					t.Errorf("PlanTargets() message = %q, want %q", err.Error(), tc.wantMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanTargets() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PlanTargets() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTargetPlan_CreatesMultiAgentRun(t *testing.T) {
	tests := []struct {
		name  string
		plan  domain.TargetPlan
		count int
		want  bool
	}{
		{"two distinct agentIds", domain.TargetPlan{Mode: domain.TargetAgentIDs}, 2, true},
		{"one agentId after dedupe (EC4001)", domain.TargetPlan{Mode: domain.TargetAgentIDs}, 1, false},
		{"all with many agents", domain.TargetPlan{Mode: domain.TargetAll}, 3, false},
		{"single agentId", domain.TargetPlan{Mode: domain.TargetAgent}, 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.plan.CreatesMultiAgentRun(tc.count); got != tc.want {
				t.Errorf("CreatesMultiAgentRun(%d) = %v, want %v", tc.count, got, tc.want)
			}
		})
	}
}
