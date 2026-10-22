package seed

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The lengths are the UTF-8 byte lengths of the Node constants (seed-prompts.ts,
// seed-skills.ts), taken when the JSON was generated; a hand edit of the data breaks them.
func TestLoadKnowledge_MatchesTheNodeSeed(t *testing.T) {
	k, err := loadKnowledge()
	if err != nil {
		t.Fatalf("loadKnowledge() error = %v", err)
	}

	type agent struct {
		Name, Description string
		PromptBytes       int
		Skills            []string
	}
	var gotAgents []agent
	for _, a := range k.agents {
		gotAgents = append(gotAgents, agent{a.Name, a.Description, len(a.SystemPrompt), a.Skills})
	}
	wantAgents := []agent{
		{"General Reviewer", "Reviews a PR diff for bugs, correctness, and clarity.", 4343,
			[]string{"pr-quality-rubric", "no-then-chains"}},
		{"Security Reviewer", "Flags secrets, injection, SSRF and the lethal trifecta before merge.", 5304,
			[]string{"secret-leakage-gate", "lethal-trifecta", "phantom-api-gate"}},
		{"Performance Reviewer", "Catches N+1 queries, missing indexes, and hot-path allocations.", 6034,
			[]string{"n-plus-one-gate"}},
		{"Test Quality Reviewer", "Checks tests for uncovered branches, missing corner cases, over-mocking and flakiness.", 5202,
			[]string{"test-coverage-nudge", "mock-discipline", "corner-case-hunter"}},
	}
	if diff := cmp.Diff(wantAgents, gotAgents); diff != "" {
		t.Errorf("loadKnowledge() agents mismatch (-want +got):\n%s", diff)
	}

	type skill struct {
		Name, Type       string
		Enabled          bool
		DescBytes, Bytes int
	}
	var gotSkills []skill
	for _, s := range k.skills {
		gotSkills = append(gotSkills, skill{s.Name, s.Type, s.Enabled, len(s.Description), len(s.Body)})
	}
	wantSkills := []skill{
		{"pr-quality-rubric", "rubric", true, 156, 920},
		{"no-then-chains", "convention", true, 120, 724},
		{"secret-leakage-gate", "security", true, 139, 880},
		{"lethal-trifecta", "security", true, 140, 922},
		{"phantom-api-gate", "security", false, 144, 643},
		{"n-plus-one-gate", "rubric", true, 142, 785},
		{"test-coverage-nudge", "rubric", true, 123, 699},
		{"mock-discipline", "convention", true, 151, 874},
		{"corner-case-hunter", "rubric", true, 159, 1061},
	}
	if diff := cmp.Diff(wantSkills, gotSkills); diff != "" {
		t.Errorf("loadKnowledge() skills mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadKnowledge_PromptsHaveNoTrailingNewline(t *testing.T) {
	k, err := loadKnowledge()
	if err != nil {
		t.Fatalf("loadKnowledge() error = %v", err)
	}
	for _, a := range k.agents {
		if p := a.SystemPrompt; p == "" || p[len(p)-1] == '\n' {
			t.Errorf("agent %s prompt ends with a newline or is empty, want neither", a.Name)
		}
	}
}
