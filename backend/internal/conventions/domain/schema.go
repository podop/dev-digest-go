package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ExtractionSchemaName is the structured-output name (also the mock provider's fixture key).
const ExtractionSchemaName = "ConventionExtraction"

// Categories are the convention categories of the wire contract.
var Categories = []string{
	"naming", "structure", "error-handling", "async", "types", "imports", "testing", "api",
	"data-access", "style", "other",
}

// ExtractionSchemaJSON is the strict JSON schema of ConventionExtraction, as the
// Node server derives it from its Zod schema (OpenAI's zodResponseFormat):
// every object lists all properties as required, additionalProperties is false,
// descriptions are included and array length limits are not sent.
var ExtractionSchemaJSON = json.RawMessage(`{"type":"object","properties":{"candidates":{"type":"array","items":{"type":"object","properties":{"category":{"type":"string","enum":["naming","structure","error-handling","async","types","imports","testing","api","data-access","style","other"],"description":"The area the rule is about."},"rule":{"type":"string","description":"The house rule as ONE imperative English sentence a reviewer can check in a diff, e.g. \"Route handlers return typed Result<T, ApiError> instead of throwing\"."},"confidence":{"type":"number","minimum":0,"maximum":1,"description":"How sure you are this is a deliberate, repo-wide convention (0..1). Seen once = at most 0.5."},"evidence":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string","description":"Repo-relative path exactly as in the FILE header."},"start_line":{"type":"integer","description":"First cited line number (from the numbered listing)."},"end_line":{"type":"integer","description":"Last cited line number."},"snippet":{"type":"string","description":"The cited lines copied VERBATIM, without the line-number prefix. 1–8 lines."}},"required":["path","start_line","end_line","snippet"],"additionalProperties":false},"description":"1 to 3 places that show the rule, in different files when possible."}},"required":["category","rule","confidence","evidence"],"additionalProperties":false}}},"required":["candidates"],"additionalProperties":false,"$schema":"http://json-schema.org/draft-07/schema#"}`)

// Extraction is the validated model output.
type Extraction struct {
	Candidates []Candidate
}

// Issue is one schema violation. Path is dotted ("candidates.0.rule"), empty for the root.
type Issue struct {
	Path    string
	Message string
}

// ValidateExtraction checks a decoded JSON value (as produced by encoding/json
// into an any) against the ConventionExtraction schema. Unknown keys are
// ignored. Every violation is reported, with the texts Zod uses. Integers
// beyond 2^53 are saturated.
func ValidateExtraction(v any) (Extraction, []Issue) {
	var v0 validator
	root, ok := v.(map[string]any)
	if !ok {
		v0.add(nil, "Expected object, received "+typeName(v))
		return Extraction{}, v0.issues
	}
	var out Extraction
	raw, present := root["candidates"]
	path := []string{"candidates"}
	if !present {
		v0.add(path, "Required")
		return Extraction{}, v0.issues
	}
	items, ok := raw.([]any)
	if !ok {
		v0.add(path, "Expected array, received "+typeName(raw))
		return Extraction{}, v0.issues
	}
	for i, item := range items {
		c, ok := v0.candidate(append(path, strconv.Itoa(i)), item)
		if ok {
			out.Candidates = append(out.Candidates, c)
		}
	}
	if len(v0.issues) > 0 {
		return Extraction{}, v0.issues
	}
	return out, nil
}

type validator struct{ issues []Issue }

func (v *validator) add(path []string, msg string) {
	v.issues = append(v.issues, Issue{Path: strings.Join(path, "."), Message: msg})
}

func (v *validator) candidate(path []string, item any) (Candidate, bool) {
	before := len(v.issues)
	m, ok := item.(map[string]any)
	if !ok {
		v.add(path, "Expected object, received "+typeName(item))
		return Candidate{}, false
	}
	var c Candidate
	c.Category = v.enum(path, m, "category")
	c.Rule = v.str(path, m, "rule")
	c.Confidence = v.confidence(path, m)

	ePath := append(append([]string{}, path...), "evidence")
	raw, present := m["evidence"]
	switch {
	case !present:
		v.add(ePath, "Required")
	default:
		list, isList := raw.([]any)
		if !isList {
			v.add(ePath, "Expected array, received "+typeName(raw))
			break
		}
		for i, e := range list {
			if ev, ok := v.evidence(append(append([]string{}, ePath...), strconv.Itoa(i)), e); ok {
				c.Evidence = append(c.Evidence, ev)
			}
		}
	}
	return c, len(v.issues) == before
}

func (v *validator) evidence(path []string, item any) (ClaimedEvidence, bool) {
	before := len(v.issues)
	m, ok := item.(map[string]any)
	if !ok {
		v.add(path, "Expected object, received "+typeName(item))
		return ClaimedEvidence{}, false
	}
	e := ClaimedEvidence{
		Path:      v.str(path, m, "path"),
		StartLine: v.integer(path, m, "start_line"),
		EndLine:   v.integer(path, m, "end_line"),
		Snippet:   v.str(path, m, "snippet"),
	}
	return e, len(v.issues) == before
}

func child(path []string, key string) []string {
	return append(append([]string{}, path...), key)
}

func (v *validator) str(path []string, m map[string]any, key string) string {
	raw, present := m[key]
	if !present {
		v.add(child(path, key), "Required")
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		v.add(child(path, key), "Expected string, received "+typeName(raw))
	}
	return s
}

func (v *validator) enum(path []string, m map[string]any, key string) string {
	raw, present := m[key]
	if !present {
		v.add(child(path, key), "Required")
		return ""
	}
	expected := "'" + strings.Join(Categories, "' | '") + "'"
	s, ok := raw.(string)
	if !ok {
		v.add(child(path, key), fmt.Sprintf("Expected %s, received %s", expected, typeName(raw)))
		return ""
	}
	for _, c := range Categories {
		if c == s {
			return s
		}
	}
	v.add(child(path, key), fmt.Sprintf("Invalid enum value. Expected %s, received '%s'", expected, s))
	return ""
}

func (v *validator) confidence(path []string, m map[string]any) float64 {
	raw, present := m["confidence"]
	if !present {
		v.add(child(path, "confidence"), "Required")
		return 0
	}
	n, ok := asNumber(raw)
	if !ok {
		v.add(child(path, "confidence"), "Expected number, received "+typeName(raw))
		return 0
	}
	if n < 0 {
		v.add(child(path, "confidence"), "Number must be greater than or equal to 0")
	}
	if n > 1 {
		v.add(child(path, "confidence"), "Number must be less than or equal to 1")
	}
	return n
}

func (v *validator) integer(path []string, m map[string]any, key string) int {
	raw, present := m[key]
	if !present {
		v.add(child(path, key), "Required")
		return 0
	}
	n, ok := asNumber(raw)
	if !ok {
		v.add(child(path, key), "Expected number, received "+typeName(raw))
		return 0
	}
	if n != math.Trunc(n) {
		v.add(child(path, key), "Expected integer, received float")
		return 0
	}
	const limit = 1 << 53
	return int(max(-limit, min(limit, n)))
}

// asNumber reads a decoded JSON number (float64, json.Number or an int type).
func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// typeName names a decoded JSON value the way Zod's "received" text does.
func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	if _, ok := asNumber(v); ok {
		return "number"
	}
	return "unknown"
}
