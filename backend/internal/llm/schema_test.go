package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const sampleSchema = `{
  "type": "object",
  "required": ["candidates"],
  "properties": {
    "candidates": {
      "type": "array",
      "minItems": 1,
      "maxItems": 3,
      "description": "rules",
      "items": {
        "type": "object",
        "properties": {
          "rule": {"type": "string", "minLength": 2, "maxLength": 5},
          "category": {"type": "string", "enum": ["structure", "naming"]},
          "confidence": {"type": "number", "minimum": 0, "maximum": 1},
          "line": {"type": "integer"},
          "note": {"anyOf": [{"type": "string"}, {"type": "null"}]}
        },
        "required": ["rule"]
      }
    }
  }
}`

func TestStrictSchema(t *testing.T) {
	t.Parallel()
	got, err := strictSchema(json.RawMessage(sampleSchema))
	if err != nil {
		t.Fatalf("strictSchema() error = %v", err)
	}
	s := string(got)
	for _, bad := range []string{"minItems", "maxItems"} {
		if strings.Contains(s, bad) {
			t.Errorf("strictSchema() = %s, want no %s", s, bad)
		}
	}
	for _, want := range []string{`"description":"rules"`, `"additionalProperties":false`, `"required":["candidates"]`,
		`"required":["rule","category","confidence","line","note"]`, `"minLength":2`} {
		if !strings.Contains(s, want) {
			t.Errorf("strictSchema() = %s, want it to contain %s", s, want)
		}
	}
	// Property order is the caller's, not alphabetical.
	if i, j := strings.Index(s, `"rule":`), strings.Index(s, `"category":`); i < 0 || j < i {
		t.Errorf("strictSchema() = %s, want rule before category", s)
	}
	if _, err := strictSchema(json.RawMessage(`[1]`)); err == nil {
		t.Error("strictSchema([1]) error = nil, want not-an-object error")
	}
}

func TestValidator(t *testing.T) {
	t.Parallel()
	v, err := newValidator(json.RawMessage(sampleSchema))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"valid", `{"candidates":[{"rule":"abc","category":"naming","confidence":0.5,"line":3,"note":null}]}`, nil},
		{"extra keys ignored", `{"candidates":[{"rule":"abc","zzz":1}],"other":true}`, nil},
		{"root not object", `[]`, []string{"- (root): Expected object, received array"}},
		{"missing required", `{}`, []string{"- candidates: Required"}},
		{"nested path and messages", `{"candidates":[{"rule":"a"},{"rule":"abcdefg","category":"x","confidence":2,"line":1.5,"note":3}]}`, []string{
			"- candidates.0.rule: String must contain at least 2 character(s)",
			"- candidates.1.rule: String must contain at most 5 character(s)",
			"- candidates.1.category: Invalid enum value. Expected 'structure' | 'naming', received 'x'",
			"- candidates.1.confidence: Number must be less than or equal to 1",
			"- candidates.1.line: Expected integer, received float",
			"- candidates.1.note: Invalid input",
		}},
		{"array limits", `{"candidates":[]}`, []string{"- candidates: Array must contain at least 1 element(s)"}},
		{"array too long", `{"candidates":[{"rule":"ab"},{"rule":"ab"},{"rule":"ab"},{"rule":"ab"}]}`, []string{"- candidates: Array must contain at most 3 element(s)"}},
		{"wrong type", `{"candidates":"x"}`, []string{"- candidates: Expected array, received string"}},
		{"utf16 length", `{"candidates":[{"rule":"😀😀😀"}]}`, []string{"- candidates.0.rule: String must contain at most 5 character(s)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, is := range v.validate(tc.in) {
				got = append(got, is.String())
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("validate(%s) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestValidator_ref(t *testing.T) {
	t.Parallel()
	v, err := newValidator(json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"#/$defs/s"}},"required":["a"],"$defs":{"s":{"type":"string","minLength":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := v.validate(`{"a":"x"}`); len(got) != 0 {
		t.Errorf("validate(valid) = %v, want none", got)
	}
	got := v.validate(`{"a":""}`)
	if len(got) != 1 || got[0].String() != "- a: String must contain at least 1 character(s)" {
		t.Errorf("validate(empty) = %v, want the minLength issue", got)
	}
}
