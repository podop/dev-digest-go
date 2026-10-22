package domain_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/onboarding/domain"
)

// nodeSchema is Node's toJsonSchema(OnboardingLlmOutput, 'OnboardingTour').schema printed with
// JSON.stringify (testdata/onboarding_schema.node.json, written by node_gen.mts).
func nodeSchema(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/onboarding_schema.node.json")
	if err != nil {
		t.Fatalf("read Node golden: %v", err)
	}
	return bytes.TrimSpace(b)
}

func TestProviderSchemaJSON_EqualsNode(t *testing.T) {
	if got, want := []byte(domain.ProviderSchemaJSON), nodeSchema(t); !bytes.Equal(got, want) {
		t.Errorf("ProviderSchemaJSON differs from the Node golden\n got %s\nwant %s", got, want)
	}
}

// The schema answers are validated against, run through the Phase 2 strict transform, is the
// document the Node server sends.
func TestSchemaJSON_StrictFormEqualsNode(t *testing.T) {
	got, err := llm.StrictSchema(domain.SchemaJSON)
	if err != nil {
		t.Fatalf("StrictSchema(SchemaJSON): %v", err)
	}
	if want := nodeSchema(t); !bytes.Equal(got, want) {
		t.Errorf("StrictSchema(SchemaJSON) differs from the Node golden\n got %s\nwant %s", got, want)
	}
}

func TestSchemaJSON_KeepsCommentOptional(t *testing.T) {
	var doc struct {
		Properties struct {
			RunSteps struct {
				Items struct {
					Required []string `json:"required"`
				} `json:"items"`
			} `json:"run_steps"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(domain.SchemaJSON, &doc); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"command"}, doc.Properties.RunSteps.Items.Required); diff != "" {
		t.Errorf("run_steps item required mismatch (-want +got):\n%s", diff)
	}
}

func TestParseOutput(t *testing.T) {
	answer := `{"architecture":{"summary":"s","nodes":[{"id":"a","label":"A","kind":"entry"}],"edges":[{"from":"a","to":"a"}]},
	"critical_paths":[{"path":"p","reason":"r"}],"run_steps":[{"command":"c"},{"command":"d","comment":"e"}],
	"reading_path":[{"path":"q","reason":"s"}],"first_tasks":[{"title":"t","path":"p","complexity":"low"}],"extra":1}`
	got, err := domain.ParseOutput([]byte(answer))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.LLMOutput{
		Architecture:  domain.LLMArchitecture{Summary: "s", Nodes: []domain.LLMNode{{ID: "a", Label: "A", Kind: "entry"}}, Edges: []domain.Edge{{From: "a", To: "a"}}},
		CriticalPaths: []domain.LLMPath{{Path: "p", Reason: "r"}},
		RunSteps:      []domain.LLMRunStep{{Command: "c"}, {Command: "d", Comment: "e"}},
		ReadingPath:   []domain.LLMPath{{Path: "q", Reason: "s"}},
		FirstTasks:    []domain.LLMTask{{Title: "t", Path: "p", Complexity: "low"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ParseOutput mismatch (-want +got):\n%s", diff)
	}
	if _, err := domain.ParseOutput([]byte(`{"architecture":`)); err == nil {
		t.Error("ParseOutput accepted truncated JSON")
	}
}
