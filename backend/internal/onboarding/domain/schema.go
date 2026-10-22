package domain

// The structured-output schema OnboardingTour (spec 7.12): the Go twin of the loose Zod object of
// Node's onboarding/domain/prompt.ts. It is deliberately loose: lengths and counts are asked for
// in the descriptions but enforced by NormalizeTour (strict json_schema modes ignore min/max, and
// a retry costs a second call).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// SchemaJSON is the schema an answer is validated against, as Zod validates it: `comment` of a
// run step is optional. llm.StrictSchema turns it into exactly ProviderSchemaJSON.
var SchemaJSON = json.RawMessage(buildSchema(false))

// ProviderSchemaJSON is the strict schema providers receive: every property required,
// additionalProperties false. It equals, as JSON and byte for byte, Node's
// toJsonSchema(OnboardingLlmOutput, 'OnboardingTour').schema, `comment` included in "required".
var ProviderSchemaJSON = json.RawMessage(buildSchema(true))

func buildSchema(strict bool) string {
	str := func(v string) string { // JSON text of a string, without HTML escaping
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return strings.TrimSuffix(b.String(), "\n")
	}
	text := func(desc string) string { return `{"type":"string","description":` + str(desc) + `}` }
	obj := func(props, required string) string {
		return `{"type":"object","properties":{` + props + `},"required":[` + required + `],"additionalProperties":false}`
	}
	arr := func(items, desc string) string {
		return `{"type":"array","items":` + items + `,"description":` + str(desc) + `}`
	}
	pathItem := func(reasonDesc string) string {
		return obj(`"path":`+text("A file path copied exactly from the file list.")+`,"reason":`+text(reasonDesc), `"path","reason"`)
	}
	commentRequired := `"command"`
	if strict {
		commentRequired = `"command","comment"`
	}

	architecture := obj(
		`"summary":`+text(fmt.Sprintf("Two to five sentences, at most %d characters. Wrap code identifiers in backticks.", MaxSummary))+`,`+
			`"nodes":`+arr(obj(
			`"id":`+text(fmt.Sprintf("Short unique id, at most %d characters.", MaxNodeID))+`,`+
				`"label":`+text(fmt.Sprintf("Box label, at most %d characters.", MaxNodeLabel))+`,`+
				`"kind":{"type":"string","enum":["entry","module","store","external"],"description":`+
				str("entry = where execution starts; store = database/cache/queue; external = third-party service.")+`}`,
			`"id","label","kind"`), fmt.Sprintf("%d to %d boxes of the architecture diagram.", MinNodes, MaxNodes))+`,`+
			`"edges":`+arr(obj(`"from":`+text("A node id.")+`,"to":`+text("A node id."), `"from","to"`),
			fmt.Sprintf("At most %d directed edges between node ids above.", MaxEdges)),
		`"summary","nodes","edges"`)

	props := `"architecture":` + architecture + `,` +
		`"critical_paths":` + arr(pathItem(fmt.Sprintf("Why it matters, at most %d characters.", MaxReason)),
		fmt.Sprintf("%d to %d files on the main execution paths.", MinCriticalPaths, MaxCriticalPaths)) + `,` +
		`"run_steps":` + arr(obj(
		`"command":`+text(fmt.Sprintf("One shell command, at most %d characters.", MaxCommand))+`,`+
			`"comment":`+text(fmt.Sprintf("What it does, at most %d characters.", MaxComment)), commentRequired),
		fmt.Sprintf("%d to %d steps to run the project locally; empty when the sources do not say.", MinRunSteps, MaxRunSteps)) + `,` +
		`"reading_path":` + arr(pathItem(fmt.Sprintf("Why read it at this point, at most %d characters.", MaxReason)),
		fmt.Sprintf("%d to %d files in the order a newcomer should read them.", MinReadingPath, MaxReadingPath)) + `,` +
		`"first_tasks":` + arr(obj(
		`"title":`+text(fmt.Sprintf("Task title, at most %d characters.", MaxTaskTitle))+`,`+
			`"path":`+text("A file path from the file list, or a folder that contains listed files.")+`,`+
			`"complexity":{"type":"string","enum":["low","medium","high"]}`, `"title","path","complexity"`),
		fmt.Sprintf("Up to %d starter tasks, picked from the TODO and untested-file signals.", MaxFirstTasks))

	return strings.TrimSuffix(obj(props, `"architecture","critical_paths","run_steps","reading_path","first_tasks"`), `}`) +
		`,"$schema":"http://json-schema.org/draft-07/schema#"}`
}

// outputWire mirrors the schema for decoding the model's answer.
type outputWire struct {
	Architecture struct {
		Summary string `json:"summary"`
		Nodes   []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Kind  string `json:"kind"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	} `json:"architecture"`
	CriticalPaths []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"critical_paths"`
	RunSteps []struct {
		Command string `json:"command"`
		Comment string `json:"comment"`
	} `json:"run_steps"`
	ReadingPath []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"reading_path"`
	FirstTasks []struct {
		Title      string `json:"title"`
		Path       string `json:"path"`
		Complexity string `json:"complexity"`
	} `json:"first_tasks"`
}

// ParseOutput decodes the model's answer (already validated against SchemaJSON by the Phase 2
// client) into an LLMOutput. A missing run-step comment is "".
func ParseOutput(data []byte) (LLMOutput, error) {
	var w outputWire
	if err := json.Unmarshal(data, &w); err != nil {
		return LLMOutput{}, fmt.Errorf("decode onboarding output: %w", err)
	}
	out := LLMOutput{Architecture: LLMArchitecture{Summary: w.Architecture.Summary}}
	for _, n := range w.Architecture.Nodes {
		out.Architecture.Nodes = append(out.Architecture.Nodes, LLMNode{ID: n.ID, Label: n.Label, Kind: n.Kind})
	}
	for _, e := range w.Architecture.Edges {
		out.Architecture.Edges = append(out.Architecture.Edges, Edge{From: e.From, To: e.To})
	}
	for _, p := range w.CriticalPaths {
		out.CriticalPaths = append(out.CriticalPaths, LLMPath{Path: p.Path, Reason: p.Reason})
	}
	for _, s := range w.RunSteps {
		out.RunSteps = append(out.RunSteps, LLMRunStep{Command: s.Command, Comment: s.Comment})
	}
	for _, p := range w.ReadingPath {
		out.ReadingPath = append(out.ReadingPath, LLMPath{Path: p.Path, Reason: p.Reason})
	}
	for _, t := range w.FirstTasks {
		out.FirstTasks = append(out.FirstTasks, LLMTask{Title: t.Title, Path: t.Path, Complexity: t.Complexity})
	}
	return out, nil
}
