package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
)

func agentRun(id, agent string, st domain.RunStatus) domain.Run {
	return domain.Run{ID: id, OwnerKind: domain.OwnerAgent, AgentID: agent, Status: st}
}

func TestAssertComparable(t *testing.T) {
	skillRun := func(id, skill string, st domain.RunStatus) domain.Run {
		return domain.Run{ID: id, OwnerKind: domain.OwnerSkill, SkillID: sp(skill), AgentID: "host", Status: st}
	}
	tests := []struct {
		name    string
		a, b    domain.Run
		wantMsg string // "" = comparable
	}{
		{"same agent", agentRun("1", "ag", domain.RunCompleted), agentRun("2", "ag", domain.RunFailed), ""},
		{"same skill", skillRun("1", "sk", domain.RunCompleted), skillRun("2", "sk", domain.RunCompleted), ""},
		{
			"skill runs on different host agents are comparable",
			skillRun("1", "sk", domain.RunCompleted),
			func() domain.Run { r := skillRun("2", "sk", domain.RunCompleted); r.AgentID = "other"; return r }(), "",
		},
		{
			"owner kinds", agentRun("1", "ag", domain.RunCompleted), skillRun("2", "sk", domain.RunCompleted),
			"Runs of different owner kinds cannot be compared",
		},
		{"agents", agentRun("1", "a", domain.RunCompleted), agentRun("2", "b", domain.RunCompleted), "Runs of different agents cannot be compared"},
		{"skills", skillRun("1", "a", domain.RunCompleted), skillRun("2", "b", domain.RunCompleted), "Runs of different skills cannot be compared"},
		{"a running", agentRun("1", "ag", domain.RunRunning), agentRun("2", "ag", domain.RunCompleted), "A run that has not finished cannot be compared"},
		{"b running", agentRun("1", "ag", domain.RunCompleted), agentRun("2", "ag", domain.RunRunning), "A run that has not finished cannot be compared"},
		{
			"owner is checked before running", agentRun("1", "a", domain.RunRunning), agentRun("2", "b", domain.RunCompleted),
			"Runs of different agents cannot be compared",
		},
		{
			"kind is checked before everything", agentRun("1", "a", domain.RunRunning), skillRun("2", "s", domain.RunRunning),
			"Runs of different owner kinds cannot be compared",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.AssertComparable(tc.a, tc.b)
			if tc.wantMsg == "" {
				if err != nil {
					t.Errorf("AssertComparable = %v, want nil", err)
				}
				return
			}
			re := ruleErr(t, err)
			if re.Kind != domain.RuleUnprocessable || re.Code != "runs_not_comparable" || re.Message != tc.wantMsg || re.Details != nil {
				t.Errorf("AssertComparable error = %+v, want 422 runs_not_comparable %q", re, tc.wantMsg)
			}
		})
	}
}

func res(id, name string, st domain.CaseStatus) domain.CaseResult {
	return domain.CaseResult{CaseID: id, CaseName: name, Status: st}
}

func TestCompareRuns_Cases(t *testing.T) {
	a := domain.CompareSide{
		Run: agentRun("A", "ag", domain.RunCompleted),
		Results: []domain.CaseResult{
			res("c1", "one", domain.CasePassed), res("c2", "two", domain.CaseFailed), res("c3", "three", domain.CaseError),
		},
	}
	b := domain.CompareSide{
		Run: agentRun("B", "ag", domain.RunCompleted),
		Results: []domain.CaseResult{
			res("c4", "four", domain.CasePassed), res("c2", "two", domain.CasePassed), res("c1", "one", domain.CasePassed),
		},
	}
	got, err := domain.CompareRuns(a, b)
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	want := []domain.CompareCase{
		{CaseID: "c1", CaseName: "one", A: domain.CasePassed, B: domain.CasePassed},
		{CaseID: "c2", CaseName: "two", A: domain.CaseFailed, B: domain.CasePassed, Flipped: true},
		{CaseID: "c3", CaseName: "three", A: domain.CaseError, B: domain.CaseAbsent, Flipped: true},
		{CaseID: "c4", CaseName: "four", A: domain.CaseAbsent, B: domain.CasePassed, Flipped: true},
	}
	if diff := cmp.Diff(want, got.Cases); diff != "" {
		t.Errorf("CompareRuns cases mismatch (-want +got):\n%s", diff)
	}
	if got.A.ID != "A" || got.B.ID != "B" {
		t.Errorf("CompareRuns A, B = %s, %s, want A, B", got.A.ID, got.B.ID)
	}
}

func TestCompareRuns_EmptyIsNonNil(t *testing.T) {
	a := domain.CompareSide{Run: agentRun("A", "ag", domain.RunCompleted)}
	b := domain.CompareSide{Run: agentRun("B", "ag", domain.RunFailed)}
	got, err := domain.CompareRuns(a, b)
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if got.Cases == nil || got.ConfigDiff.SkillsAdded == nil || got.ConfigDiff.SkillsRemoved == nil || got.ConfigDiff.SystemPrompt == nil {
		t.Errorf("CompareRuns slices = %#v, want all non-nil", got)
	}
	if got.ConfigDiff.Model != nil || got.ConfigDiff.Strategy != nil {
		t.Errorf("CompareRuns model, strategy = %v, %v, want nil, nil", got.ConfigDiff.Model, got.ConfigDiff.Strategy)
	}
}

func TestCompareRuns_NotComparable(t *testing.T) {
	_, err := domain.CompareRuns(
		domain.CompareSide{Run: agentRun("A", "x", domain.RunCompleted)},
		domain.CompareSide{Run: agentRun("B", "y", domain.RunCompleted)},
	)
	if re := ruleErr(t, err); re.Code != "runs_not_comparable" {
		t.Errorf("CompareRuns error code = %s, want runs_not_comparable", re.Code)
	}
}

func TestCompareRuns_Delta(t *testing.T) {
	mk := func(id string, m domain.Metrics) domain.CompareSide {
		r := agentRun(id, "ag", domain.RunCompleted)
		r.Metrics = m
		return domain.CompareSide{Run: r}
	}
	tests := []struct {
		name string
		a, b domain.Metrics
		want domain.Delta
	}{
		{
			"all present",
			domain.Metrics{Recall: fp(0.5), Precision: fp(0.9), CitationAccuracy: fp(1), CostUSD: fp(0.012345)},
			domain.Metrics{Recall: fp(0.75), Precision: fp(0.8), CitationAccuracy: fp(1), CostUSD: fp(0.0123456)},
			domain.Delta{Recall: fp(0.25), Precision: fp(-0.1), CitationAccuracy: fp(0), CostUSD: fp(0.000001)},
		},
		{
			"nil on a side",
			domain.Metrics{Recall: nil, Precision: fp(0.5), CostUSD: nil},
			domain.Metrics{Recall: fp(0.5), Precision: nil, CostUSD: fp(1)},
			domain.Delta{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.CompareRuns(mk("A", tc.a), mk("B", tc.b))
			if err != nil {
				t.Fatalf("CompareRuns: %v", err)
			}
			if diff := cmp.Diff(tc.want, got.Delta); diff != "" {
				t.Errorf("CompareRuns delta mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCompareRuns_ConfigDiff(t *testing.T) {
	skills := func(names ...string) []domain.SkillSnapshot {
		var out []domain.SkillSnapshot
		for _, n := range names {
			out = append(out, domain.SkillSnapshot{ID: n + "-id", Name: n})
		}
		return out
	}
	tests := []struct {
		name string
		a, b domain.Config
		want domain.ConfigDiff
	}{
		{
			"identical",
			domain.Config{Provider: "openrouter", Model: "m", SystemPrompt: "p", Strategy: "auto", Skills: skills("s1")},
			domain.Config{Provider: "openrouter", Model: "m", SystemPrompt: "p", Strategy: "auto", Skills: skills("s1")},
			domain.ConfigDiff{SystemPrompt: []domain.DiffLine{same("p")}, SkillsAdded: []string{}, SkillsRemoved: []string{}},
		},
		{
			"model bump shows the model ids",
			domain.Config{Provider: "openrouter", Model: "m1"}, domain.Config{Provider: "openrouter", Model: "m2"},
			domain.ConfigDiff{SystemPrompt: []domain.DiffLine{}, Model: &domain.ValueChange{A: "m1", B: "m2"}, SkillsAdded: []string{}, SkillsRemoved: []string{}},
		},
		{
			"provider change shows provider/model even with the same model",
			domain.Config{Provider: "openrouter", Model: "m"}, domain.Config{Provider: "other", Model: "m"},
			domain.ConfigDiff{SystemPrompt: []domain.DiffLine{}, Model: &domain.ValueChange{A: "openrouter/m", B: "other/m"}, SkillsAdded: []string{}, SkillsRemoved: []string{}},
		},
		{
			"strategy, prompt and skills",
			domain.Config{SystemPrompt: "a\nb", Strategy: "auto", Skills: skills("keep", "gone", "dup")},
			domain.Config{SystemPrompt: "a\nc", Strategy: "map_reduce", Skills: skills("new", "keep", "new2", "new")},
			domain.ConfigDiff{
				SystemPrompt:  []domain.DiffLine{same("a"), del("b"), add("c")},
				Strategy:      &domain.ValueChange{A: "auto", B: "map_reduce"},
				SkillsAdded:   []string{"new", "new2", "new"},
				SkillsRemoved: []string{"gone", "dup"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := domain.CompareSide{Run: agentRun("A", "ag", domain.RunCompleted)}
			b := domain.CompareSide{Run: agentRun("B", "ag", domain.RunCompleted)}
			a.Run.Config, b.Run.Config = tc.a, tc.b
			got, err := domain.CompareRuns(a, b)
			if err != nil {
				t.Fatalf("CompareRuns: %v", err)
			}
			if diff := cmp.Diff(tc.want, got.ConfigDiff); diff != "" {
				t.Errorf("CompareRuns config diff mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
