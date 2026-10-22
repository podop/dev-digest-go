package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/conventions/domain"
)

// The digest is that of the schema the Node server derives from its Zod schema
// with zodResponseFormat (generated from the original source).
func TestExtractionSchemaJSON_MatchesNode(t *testing.T) {
	sum := sha256.Sum256(domain.ExtractionSchemaJSON)
	const want = "52d7007e225e248a3b67dd8b1340dcbf0cfa69cbc5c137dda96f63aa7e43a7f6"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("ExtractionSchemaJSON sha256 = %s, want %s", got, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal(domain.ExtractionSchemaJSON, &decoded); err != nil {
		t.Fatalf("ExtractionSchemaJSON is not valid JSON: %v", err)
	}
	if decoded["additionalProperties"] != false {
		t.Errorf("ExtractionSchemaJSON additionalProperties = %v, want false", decoded["additionalProperties"])
	}
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func TestValidateExtraction_Valid(t *testing.T) {
	v := decode(t, `{"candidates":[{"category":"naming","rule":"Use X","confidence":0.5,"extra":1,
	  "evidence":[{"path":"a.ts","start_line":3,"end_line":4,"snippet":"s"}]}]}`)
	got, issues := domain.ValidateExtraction(v)
	if len(issues) != 0 {
		t.Fatalf("ValidateExtraction issues = %v, want none", issues)
	}
	want := domain.Extraction{Candidates: []domain.Candidate{{
		Category: "naming", Rule: "Use X", Confidence: 0.5,
		Evidence: []domain.ClaimedEvidence{{Path: "a.ts", StartLine: 3, EndLine: 4, Snippet: "s"}},
	}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ValidateExtraction mismatch (-want +got):\n%s", diff)
	}
}

func TestValidateExtraction_Empty(t *testing.T) {
	got, issues := domain.ValidateExtraction(decode(t, `{"candidates":[]}`))
	if len(issues) != 0 || len(got.Candidates) != 0 {
		t.Errorf("ValidateExtraction(empty) = %v, %v; want no candidates, no issues", got, issues)
	}
}

func TestValidateExtraction_Issues(t *testing.T) {
	const cat = "Expected 'naming' | 'structure' | 'error-handling' | 'async' | 'types' | 'imports' | 'testing' | 'api' | 'data-access' | 'style' | 'other'"
	tests := []struct {
		name string
		in   string
		want []domain.Issue
	}{
		{"candidates is a string", `{"candidates":"x"}`, []domain.Issue{{Path: "candidates", Message: "Expected array, received string"}}},
		{"candidates missing", `{}`, []domain.Issue{{Path: "candidates", Message: "Required"}}},
		{"root is an array", `[]`, []domain.Issue{{Path: "", Message: "Expected object, received array"}}},
		{"root is null", `null`, []domain.Issue{{Path: "", Message: "Expected object, received null"}}},
		{"item is a number", `{"candidates":[1]}`, []domain.Issue{{Path: "candidates.0", Message: "Expected object, received number"}}},
		{
			"every field wrong",
			`{"candidates":[{"category":"bogus","rule":3,"confidence":1.7,"evidence":[{"path":null,"start_line":1.5,"end_line":"2"}]}]}`,
			[]domain.Issue{
				{Path: "candidates.0.category", Message: "Invalid enum value. " + cat + ", received 'bogus'"},
				{Path: "candidates.0.rule", Message: "Expected string, received number"},
				{Path: "candidates.0.confidence", Message: "Number must be less than or equal to 1"},
				{Path: "candidates.0.evidence.0.path", Message: "Expected string, received null"},
				{Path: "candidates.0.evidence.0.start_line", Message: "Expected integer, received float"},
				{Path: "candidates.0.evidence.0.end_line", Message: "Expected number, received string"},
				{Path: "candidates.0.evidence.0.snippet", Message: "Required"},
			},
		},
		{
			"missing fields and negative confidence",
			`{"candidates":[{"category":5,"confidence":-1,"evidence":"x"}]}`,
			[]domain.Issue{
				{Path: "candidates.0.category", Message: cat + ", received number"},
				{Path: "candidates.0.rule", Message: "Required"},
				{Path: "candidates.0.confidence", Message: "Number must be greater than or equal to 0"},
				{Path: "candidates.0.evidence", Message: "Expected array, received string"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, issues := domain.ValidateExtraction(decode(t, tc.in))
			if diff := cmp.Diff(tc.want, issues); diff != "" {
				t.Errorf("ValidateExtraction(%s) issues mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}
