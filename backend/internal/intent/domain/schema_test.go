package domain_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
)

// nodeSchema is the output of Node's toJsonSchema(IntentClassification, 'IntentClassification')
// (reviewer-core llm/structured.ts, OpenAI's zodResponseFormat) for dev-digest d1c9093, printed
// with JSON.stringify; testdata/intent_schema.node.json (generator testdata/node_gen.mts).
func nodeSchema(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/intent_schema.node.json")
	if err != nil {
		t.Fatalf("read Node golden: %v", err)
	}
	return bytes.TrimSpace(b)
}

func TestClassificationSchemaJSON_EqualsNode(t *testing.T) {
	if got, want := []byte(domain.ClassificationSchemaJSON), nodeSchema(t); !bytes.Equal(got, want) {
		t.Errorf("ClassificationSchemaJSON differs from the Node golden\n got %s\nwant %s", got, want)
	}
}

// P13: llm.StrictSchema of the schema is the document the Node server sends.
func TestClassificationSchemaJSON_StrictFormEqualsNode(t *testing.T) {
	got, err := llm.StrictSchema(domain.ClassificationSchemaJSON)
	if err != nil {
		t.Fatalf("StrictSchema(ClassificationSchemaJSON): %v", err)
	}
	if want := nodeSchema(t); !bytes.Equal(got, want) {
		t.Errorf("StrictSchema(ClassificationSchemaJSON) differs from the Node golden\n got %s\nwant %s", got, want)
	}
}

func TestClassificationSchemaJSON_Contract(t *testing.T) {
	var doc struct {
		Required   []string `json:"required"`
		Properties struct {
			ChangeType struct {
				Enum []string `json:"enum"`
			} `json:"change_type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(domain.ClassificationSchemaJSON, &doc); err != nil {
		t.Fatalf("ClassificationSchemaJSON is not valid JSON: %v", err)
	}
	if diff := cmp.Diff([]string{"intent", "in_scope", "out_of_scope", "change_type"}, doc.Required); diff != "" {
		t.Errorf("required mismatch (-want +got):\n%s", diff)
	}
	// The IntentChangeType enum of the shared contract (frontend/src/vendor/shared/contracts/brief.ts).
	wantEnum := []string{"feature", "bugfix", "refactor", "docs", "test", "chore", "security", "perf", "mixed"}
	if diff := cmp.Diff(wantEnum, doc.Properties.ChangeType.Enum); diff != "" {
		t.Errorf("change_type enum mismatch (-want +got):\n%s", diff)
	}
}
