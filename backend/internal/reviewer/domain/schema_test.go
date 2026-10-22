package domain_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/reviewer/domain"
)

// nodeReviewSchema is the output of Node's toJsonSchema(Review, 'Review') (reviewer-core
// llm/structured.ts, OpenAI's zodResponseFormat) for the Zod contract of dev-digest commit d1c9093,
// printed with JSON.stringify; testdata/review_schema.node.json.
func nodeReviewSchema(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/review_schema.node.json")
	if err != nil {
		t.Fatalf("read Node golden: %v", err)
	}
	return bytes.TrimSpace(b)
}

func TestReviewProviderSchemaJSON_EqualsNode(t *testing.T) {
	want := nodeReviewSchema(t)
	if got := []byte(domain.ReviewProviderSchemaJSON); !bytes.Equal(got, want) {
		t.Errorf("ReviewProviderSchemaJSON differs from the Node golden\n got %s\nwant %s", got, want)
	}
}

// P7: llm.StrictSchema of the hand-written schema is the document the Node server sends.
func TestReviewSchemaJSON_StrictFormEqualsNode(t *testing.T) {
	got, err := llm.StrictSchema(domain.ReviewSchemaJSON)
	if err != nil {
		t.Fatalf("StrictSchema(ReviewSchemaJSON): %v", err)
	}
	want := nodeReviewSchema(t)
	if !bytes.Equal(got, want) {
		t.Errorf("StrictSchema(ReviewSchemaJSON) differs from the Node golden\n got %s\nwant %s", got, want)
	}
}

func TestReviewSchemaJSON_IsValidAndKeepsOptionalFieldsOptional(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"ReviewSchemaJSON":           domain.ReviewSchemaJSON,
		"ReviewValidationSchemaJSON": domain.ReviewValidationSchemaJSON,
	} {
		var doc struct {
			Properties struct {
				Findings struct {
					Items struct {
						Required []string `json:"required"`
					} `json:"items"`
				} `json:"findings"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", name, err)
		}
		wantTop := []string{"verdict", "summary", "score", "findings"}
		wantFinding := []string{"id", "severity", "category", "title", "file", "start_line", "end_line", "rationale", "confidence"}
		if diff := cmp.Diff(wantTop, doc.Required); diff != "" {
			t.Errorf("%s required mismatch (-want +got):\n%s", name, diff)
		}
		if diff := cmp.Diff(wantFinding, doc.Properties.Findings.Items.Required); diff != "" {
			t.Errorf("%s finding required mismatch (-want +got):\n%s", name, diff)
		}
	}
}

// The validation variant has no "nullable" keyword, so a validator that ignores it still accepts null.
func TestReviewValidationSchemaJSON_UsesAnyOfForNull(t *testing.T) {
	if bytes.Contains(domain.ReviewValidationSchemaJSON, []byte(`"nullable"`)) {
		t.Errorf("ReviewValidationSchemaJSON contains \"nullable\"")
	}
	if !bytes.Contains(domain.ReviewValidationSchemaJSON, []byte(`"suggestion":{"anyOf":[{"type":"string"},{"type":"null"}]}`)) {
		t.Errorf("ReviewValidationSchemaJSON does not write suggestion as anyOf with null")
	}
}

func TestReviewSchemaName(t *testing.T) {
	if domain.ReviewSchemaName != "Review" {
		t.Errorf("ReviewSchemaName = %q, want Review", domain.ReviewSchemaName)
	}
}
