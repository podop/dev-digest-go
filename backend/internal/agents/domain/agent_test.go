package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/agents/domain"
)

func ptr[T any](v T) *T { return &v }

func baseAgent() domain.Agent {
	return domain.Agent{
		ID: "a1", Name: "A", Description: "d", Provider: "openai", Model: "gpt-4.1", SystemPrompt: "p",
		Enabled: true, Version: 1, Strategy: "single-pass", CIFailOn: "critical", RepoIntel: true,
	}
}

func TestNew_Defaults(t *testing.T) {
	got := domain.New(domain.CreateInput{Name: "A", Provider: "openai", Model: "gpt-4.1", SystemPrompt: "p"})
	want := domain.Agent{
		Name: "A", Provider: "openai", Model: "gpt-4.1", SystemPrompt: "p",
		Enabled: true, Version: 1, Strategy: "single-pass", CIFailOn: "critical", RepoIntel: true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("New(minimal) mismatch (-want +got):\n%s", diff)
	}
}

func TestNew_Explicit(t *testing.T) {
	got := domain.New(domain.CreateInput{
		Name: "A", Description: ptr("desc"), Provider: "anthropic", Model: "m", SystemPrompt: "p",
		OutputSchema: json.RawMessage(`{"a":1}`), Strategy: ptr("auto"), CIFailOn: ptr("any"),
		RepoIntel: ptr(false), Enabled: ptr(false),
	})
	want := domain.Agent{
		Name: "A", Description: "desc", Provider: "anthropic", Model: "m", SystemPrompt: "p",
		OutputSchema: json.RawMessage(`{"a":1}`), Enabled: false, Version: 1, Strategy: "auto", CIFailOn: "any",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("New(explicit) mismatch (-want +got):\n%s", diff)
	}
}

func TestNew_NullSchemaIsNil(t *testing.T) {
	got := domain.New(domain.CreateInput{OutputSchema: json.RawMessage(`null`)})
	if got.OutputSchema != nil {
		t.Errorf("New(output_schema null).OutputSchema = %s, want nil", got.OutputSchema)
	}
}

func TestPatch_Apply(t *testing.T) {
	withSchema := baseAgent()
	withSchema.OutputSchema = json.RawMessage(`{"a":1}`)

	tests := []struct {
		name       string
		agent      domain.Agent
		patch      domain.Patch
		wantChange bool
		check      func(domain.Agent) bool
	}{
		{"empty patch", baseAgent(), domain.Patch{}, false, nil},
		{"enabled only", baseAgent(), domain.Patch{Enabled: ptr(false)}, false, func(a domain.Agent) bool { return !a.Enabled }},
		{"all equal", baseAgent(), domain.Patch{
			Name: ptr("A"), Description: ptr("d"), Provider: ptr("openai"), Model: ptr("gpt-4.1"),
			SystemPrompt: ptr("p"), Strategy: ptr("single-pass"), CIFailOn: ptr("critical"), RepoIntel: ptr(true),
		}, false, nil},
		{"new model", baseAgent(), domain.Patch{Model: ptr("gpt-5.4")}, true, func(a domain.Agent) bool { return a.Model == "gpt-5.4" }},
		{"new name", baseAgent(), domain.Patch{Name: ptr("B")}, true, nil},
		{"new description", baseAgent(), domain.Patch{Description: ptr("")}, true, nil},
		{"new provider", baseAgent(), domain.Patch{Provider: ptr("anthropic")}, true, nil},
		{"new prompt", baseAgent(), domain.Patch{SystemPrompt: ptr("q")}, true, nil},
		{"new strategy", baseAgent(), domain.Patch{Strategy: ptr("auto")}, true, nil},
		{"new ci_fail_on", baseAgent(), domain.Patch{CIFailOn: ptr("never")}, true, nil},
		{"new repo_intel", baseAgent(), domain.Patch{RepoIntel: ptr(false)}, true, func(a domain.Agent) bool { return !a.RepoIntel }},
		{"schema null on null schema", baseAgent(), domain.Patch{SetOutputSchema: true}, true, nil},
		{"equal schema", withSchema, domain.Patch{SetOutputSchema: true, OutputSchema: json.RawMessage(`{"a":1}`)}, true, nil},
		{"schema cleared", withSchema, domain.Patch{SetOutputSchema: true, OutputSchema: json.RawMessage(`null`)}, true,
			func(a domain.Agent) bool { return a.OutputSchema == nil }},
		{"enabled with config", baseAgent(), domain.Patch{Enabled: ptr(false), Model: ptr("x")}, true,
			func(a domain.Agent) bool { return !a.Enabled && a.Model == "x" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := tc.agent
			got, changed := tc.patch.Apply(tc.agent)
			if changed != tc.wantChange {
				t.Errorf("Apply() configChanged = %v, want %v", changed, tc.wantChange)
			}
			if got.Version != orig.Version {
				t.Errorf("Apply() version = %d, want unchanged %d (the caller bumps it)", got.Version, orig.Version)
			}
			if tc.check != nil && !tc.check(got) {
				t.Errorf("Apply() = %+v, field not applied", got)
			}
		})
	}
}

func TestWithSkillAt(t *testing.T) {
	two := func(n int) *int { return &n }
	tests := []struct {
		name    string
		current []string
		skill   string
		order   *int
		want    []string
	}{
		{"append", []string{"b", "a"}, "c", nil, []string{"b", "a", "c"}},
		{"at front", []string{"b", "a"}, "c", two(0), []string{"c", "b", "a"}},
		{"clamped", []string{"b", "a"}, "c", two(99), []string{"b", "a", "c"}},
		{"linked moves to end", []string{"c", "b", "a"}, "a", nil, []string{"c", "b", "a"}},
		{"linked moves to front", []string{"b", "a"}, "a", two(0), []string{"a", "b"}},
		{"empty", nil, "a", nil, []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := append([]string(nil), tc.current...)
			got := domain.WithSkillAt(tc.current, tc.skill, tc.order)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("WithSkillAt(%v, %q) mismatch (-want +got):\n%s", tc.current, tc.skill, diff)
			}
			if diff := cmp.Diff(orig, tc.current); diff != "" {
				t.Errorf("WithSkillAt modified its input (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDedupeAndUnknown(t *testing.T) {
	if diff := cmp.Diff([]string{"a", "b"}, domain.Dedupe([]string{"a", "b", "a"})); diff != "" {
		t.Errorf("Dedupe mismatch (-want +got):\n%s", diff)
	}
	got := domain.UnknownSkillIDs([]string{"a", "s", "r"}, map[string]bool{"a": true})
	if diff := cmp.Diff([]string{"s", "r"}, got); diff != "" {
		t.Errorf("UnknownSkillIDs mismatch (-want +got):\n%s", diff)
	}
	err := &domain.UnknownSkillsError{IDs: got}
	if want := "Unknown skill id(s) for this workspace: s, r"; err.Error() != want {
		t.Errorf("UnknownSkillsError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestSnapshot_CopiesSkills(t *testing.T) {
	skills := []string{"a"}
	snap := baseAgent().Snapshot(skills)
	skills[0] = "z"
	if snap.Skills[0] != "a" {
		t.Errorf("Snapshot().Skills = %v, want a copy of the input", snap.Skills)
	}
	if empty := baseAgent().Snapshot(nil); empty.Skills == nil {
		t.Error("Snapshot(nil).Skills = nil, want an empty non-nil list")
	}
}
