package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// SchemaJSON is the JSON Schema of the model's answer, PrBriefLlm (spec 7.12; Node
// brief/domain/prompt.ts BriefLlmOutput through toJsonSchema). The Zod schema is deliberately
// loose: lengths and counts are asked for in the descriptions and enforced by NormalizeBrief,
// because strict json_schema modes ignore minimum and maximum and a re-ask costs a second call.
// Every property is required (the Zod object has no optional key), so the document already is the
// strict form the providers receive: llm.StrictSchema(SchemaJSON) equals it, and as JSON it equals
// the Node output byte for byte, property order included (golden testdata/prbrief_schema.node.json).
// The answer is validated against it as written; "line" is any number.
var SchemaJSON = json.RawMessage(buildSchema())

func buildSchema() string {
	str := func(v string) string { // JSON text of a string, without HTML escaping
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return strings.TrimSuffix(b.String(), "\n")
	}
	n := strconv.Itoa
	describe := func(d string) string { return `,"description":` + str(d) }

	risk := `{"type":"object","properties":{` +
		`"kind":{"type":"string"` + describe("Short category such as security, data, compat, perf, concurrency, test-gap.") + `},` +
		`"title":{"type":"string"` + describe("At most "+n(MaxRiskTitle)+" characters.") + `},` +
		`"explanation":{"type":"string"` + describe("What can go wrong and what to check, at most "+n(MaxRiskExplanation)+" characters.") + `},` +
		`"severity":{"type":"string","enum":["high","medium","low"]},` +
		`"file_refs":{"type":"array","items":{"type":"string"}` +
		describe("One or more `path:start` or `path:start-end`, path copied exactly from the file lists, lines from the changed ranges.") + `}` +
		`},"required":["kind","title","explanation","severity","file_refs"],"additionalProperties":false}`
	focus := `{"type":"object","properties":{` +
		`"file":{"type":"string"` + describe("A path copied exactly from the file lists.") + `},` +
		`"line":{"type":"number"` + describe("A 1-based line inside a changed range, a finding line or a caller line of that file.") + `},` +
		`"reason":{"type":"string"` + describe("Why to look here, at most "+n(MaxFocusReason)+" characters.") + `}` +
		`},"required":["file","line","reason"],"additionalProperties":false}`

	return `{"type":"object","properties":{` +
		`"summary":{"type":"string"` + describe("What the PR does and why, at most "+n(MaxSummary)+" characters.") + `},` +
		`"risks":{"type":"array","items":` + risk + describe("At most "+n(MaxRisks)+" risks, most severe first.") + `},` +
		`"review_focus":{"type":"array","items":` + focus + describe("At most "+n(MaxFocus)+" places, in the order a reviewer should read them.") + `}` +
		`},"required":["summary","risks","review_focus"],"additionalProperties":false,` +
		`"$schema":"http://json-schema.org/draft-07/schema#"}`
}

// ParseLLMOutput decodes a validated answer (llm.Result.Data) into an LLMOutput. The answer has
// passed SchemaJSON; an error here means a malformed document, not a rule of the contract.
func ParseLLMOutput(data json.RawMessage) (LLMOutput, error) {
	var w struct {
		Summary string `json:"summary"`
		Risks   []struct {
			Kind        string   `json:"kind"`
			Title       string   `json:"title"`
			Explanation string   `json:"explanation"`
			Severity    string   `json:"severity"`
			FileRefs    []string `json:"file_refs"`
		} `json:"risks"`
		ReviewFocus []struct {
			File   string  `json:"file"`
			Line   float64 `json:"line"`
			Reason string  `json:"reason"`
		} `json:"review_focus"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return LLMOutput{}, fmt.Errorf("decode brief output: %w", err)
	}
	out := LLMOutput{Summary: w.Summary}
	for _, r := range w.Risks {
		out.Risks = append(out.Risks, LLMRisk{Kind: r.Kind, Title: r.Title, Explanation: r.Explanation, Severity: r.Severity, FileRefs: r.FileRefs})
	}
	for _, f := range w.ReviewFocus {
		out.ReviewFocus = append(out.ReviewFocus, LLMFocus{File: f.File, Line: f.Line, Reason: f.Reason})
	}
	return out, nil
}
