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
	got, err := StrictSchema(json.RawMessage(sampleSchema))
	if err != nil {
		t.Fatalf("StrictSchema() error = %v", err)
	}
	s := string(got)
	for _, bad := range []string{"minItems", "maxItems"} {
		if strings.Contains(s, bad) {
			t.Errorf("StrictSchema() = %s, want no %s", s, bad)
		}
	}
	for _, want := range []string{`"description":"rules"`, `"additionalProperties":false`, `"required":["candidates"]`,
		`"required":["rule","category","confidence","line","note"]`, `"minLength":2`} {
		if !strings.Contains(s, want) {
			t.Errorf("StrictSchema() = %s, want it to contain %s", s, want)
		}
	}
	// Property order is the caller's, not alphabetical.
	if i, j := strings.Index(s, `"rule":`), strings.Index(s, `"category":`); i < 0 || j < i {
		t.Errorf("StrictSchema() = %s, want rule before category", s)
	}
	if _, err := StrictSchema(json.RawMessage(`[1]`)); err == nil {
		t.Error("StrictSchema([1]) error = nil, want not-an-object error")
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
			"- candidates.1.note: Expected string, received number",
		}},
		{"array limits", `{"candidates":[]}`, []string{"- candidates: Array must contain at least 1 element(s)"}},
		{"array too long", `{"candidates":[{"rule":"ab"},{"rule":"ab"},{"rule":"ab"},{"rule":"ab"}]}`, []string{"- candidates: Array must contain at most 3 element(s)"}},
		{"wrong type", `{"candidates":"x"}`, []string{"- candidates: Expected array, received string"}},
		// Zod's z.number().int() names the type "number" for a non-number and "integer" only for a float.
		{"integer given a string", `{"candidates":[{"rule":"ab","line":"2"}]}`, []string{"- candidates.0.line: Expected number, received string"}},
		{"integer given null", `{"candidates":[{"rule":"ab","line":null}]}`, []string{"- candidates.0.line: Expected number, received null"}},
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

func TestValidator_nullable(t *testing.T) {
	t.Parallel()
	v, err := newValidator(json.RawMessage(`{"type":"object","properties":{
	  "s":{"type":"string","nullable":true},
	  "n":{"type":"number","nullable":true,"minimum":0},
	  "b":{"type":"boolean","nullable":true},
	  "e":{"type":"string","enum":["a","b"],"nullable":true},
	  "off":{"type":"string","nullable":false},
	  "plain":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"nullable string, number, boolean and enum accept null", `{"s":null,"n":null,"b":null,"e":null}`, nil},
		{"nullable keeps checking other values", `{"s":"x","n":2,"b":true,"e":"a"}`, nil},
		{"nullable still checks the type", `{"s":1,"b":"x"}`, []string{
			"- s: Expected string, received number",
			"- b: Expected boolean, received string",
		}},
		{"nullable still checks limits", `{"n":-1}`, []string{"- n: Number must be greater than or equal to 0"}},
		{"nullable still checks the enum", `{"e":"z"}`, []string{"- e: Invalid enum value. Expected 'a' | 'b', received 'z'"}},
		{"plain string rejects null", `{"plain":null}`, []string{"- plain: Expected string, received null"}},
		{"nullable false rejects null", `{"off":null}`, []string{"- off: Expected string, received null"}},
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

func TestStrictSchema_keepsNullable(t *testing.T) {
	t.Parallel()
	got, err := StrictSchema(json.RawMessage(`{"type":"object","properties":{"s":{"type":"string","nullable":true}}}`))
	if err != nil {
		t.Fatalf("StrictSchema() error = %v", err)
	}
	if want := `{"type":"object","properties":{"s":{"type":"string","nullable":true}},"required":["s"],"additionalProperties":false}`; string(got) != want {
		t.Errorf("StrictSchema() = %s, want %s", got, want)
	}
}
