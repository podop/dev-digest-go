package evalshttp_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
)

// The request schemas against the Node goldens (internal/parity/testdata/goldens/evals.json,
// sections validation, manual and edit): every case that fits an HTTP request is sent through the
// routes and the issues (code, path, message) or the parsed value must equal Zod's.

type goldenIssue struct {
	Code    string `json:"code"`
	Path    []any  `json:"path"`
	Message string `json:"message"`
}

type goldenParse struct {
	OK     bool           `json:"ok"`
	Data   map[string]any `json:"data"`
	Issues []goldenIssue  `json:"issues"`
}

func (g goldenIssue) String() string {
	p, _ := json.Marshal(g.Path)
	return g.Code + " " + string(p) + " " + g.Message
}

func loadGolden(t *testing.T) (validation []struct {
	Schema string `json:"schema"`
	Cases  []struct {
		Input map[string]any `json:"input"`
		goldenParse
	} `json:"cases"`
}, manual []struct {
	Name  string      `json:"name"`
	Body  any         `json:"body"`
	Parse goldenParse `json:"parse"`
}, edit struct {
	Cases []struct {
		Name  string      `json:"name"`
		Patch any         `json:"patch"`
		Parse goldenParse `json:"parse"`
	} `json:"cases"`
	ManualCases []struct {
		Name  string      `json:"name"`
		Patch any         `json:"patch"`
		Parse goldenParse `json:"parse"`
	} `json:"manualCases"`
}) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "parity", "testdata", "goldens", "evals.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g struct {
		Validation json.RawMessage `json:"validation"`
		Manual     json.RawMessage `json:"manual"`
		Edit       json.RawMessage `json:"edit"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	for _, p := range []struct {
		raw json.RawMessage
		to  any
	}{{g.Validation, &validation}, {g.Manual, &manual}, {g.Edit, &edit}} {
		if err := json.Unmarshal(p.raw, p.to); err != nil {
			t.Fatalf("parse golden section: %v", err)
		}
	}
	return validation, manual, edit
}

// hasBlob reports a digest placeholder of an over-long string, which cannot be sent.
func hasBlob(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["$blob"]; ok {
			return true
		}
		for _, e := range x {
			if hasBlob(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if hasBlob(e) {
				return true
			}
		}
	}
	return false
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// roundTrip normalises a Go value to what JSON decoding gives (numbers as float64).
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	return decode(t, jsonOf(t, v))
}

// prune drops null values and the maps left empty, recursively.
func prune(v map[string]any) map[string]any {
	for k, e := range v {
		switch x := e.(type) {
		case nil:
			delete(v, k)
		case map[string]any:
			if prune(x); len(x) == 0 {
				delete(v, k)
			}
		}
	}
	return v
}

func allStrings(m map[string]any) bool {
	for _, v := range m {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}

func issueStrings(gs []goldenIssue) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.String()
	}
	return out
}

func createMap(in domain.CreateInput) map[string]any {
	e := in.Expectation
	return map[string]any{
		"name": in.Name, "notes": in.Notes,
		"expectation": map[string]any{
			"kind": string(e.Kind), "file": e.File, "start_line": e.StartLine, "end_line": e.EndLine,
			"severity": e.Severity, "category": e.Category, "title": e.Title,
		},
		"input_diff": in.InputDiff,
		"input_meta": map[string]any{"pr_title": in.PRTitle, "pr_description": in.PRDescription},
	}
}

func fieldVal(f domain.Field[string], m map[string]any, key string) {
	if !f.Set {
		return
	}
	if f.Value == nil {
		m[key] = nil
		return
	}
	m[key] = *f.Value
}

func updateMap(in domain.UpdateInput) map[string]any {
	m := map[string]any{}
	if in.Name != nil {
		m["name"] = *in.Name
	}
	fieldVal(in.Notes, m, "notes")
	if in.Kind != nil {
		m["kind"] = string(*in.Kind)
	}
	if in.File != nil {
		m["file"] = *in.File
	}
	if in.StartLine != nil {
		m["start_line"] = *in.StartLine
	}
	if in.EndLine != nil {
		m["end_line"] = *in.EndLine
	}
	fieldVal(in.Severity, m, "severity")
	fieldVal(in.Category, m, "category")
	fieldVal(in.Title, m, "title")
	if in.InputDiff != nil {
		m["input_diff"] = *in.InputDiff
	}
	fieldVal(in.PRTitle, m, "pr_title")
	fieldVal(in.PRDescription, m, "pr_description")
	return m
}

func TestGolden_CreateSchema(t *testing.T) {
	_, manual, _ := loadGolden(t)
	checked := 0
	for _, tc := range manual {
		if hasBlob(tc.Body) {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			f := &fake{caseView: sampleCase()}
			got := do(t, f, postJSON("/agents/"+id1+"/eval-cases", jsonOf(t, tc.Body)))
			if !tc.Parse.OK {
				if diff := cmp.Diff(issueStrings(tc.Parse.Issues), issuesOf(t, got)); diff != "" {
					t.Errorf("issues mismatch (-want +got):\n%s", diff)
				}
				return
			}
			if got.status != http.StatusCreated {
				t.Fatalf("POST = %d %s, want 201", got.status, got.body)
			}
			in, ok := f.args[3].(domain.CreateInput)
			if !ok {
				t.Fatalf("Create args = %v", f.args)
			}
			want := prune(roundTrip(t, tc.Parse.Data).(map[string]any))
			gotMap := prune(roundTrip(t, createMap(in)).(map[string]any))
			if diff := cmp.Diff(want, gotMap); diff != "" {
				t.Errorf("parsed input mismatch (-want +got):\n%s", diff)
			}
		})
		checked++
	}
	if checked < 50 {
		t.Errorf("only %d golden cases were sent, want most of the section", checked)
	}
}

func TestGolden_UpdateSchema(t *testing.T) {
	_, _, edit := loadGolden(t)
	type patchCase struct {
		Name  string
		Patch any
		Parse goldenParse
	}
	var all []patchCase
	for _, c := range edit.Cases {
		all = append(all, patchCase(c))
	}
	for _, c := range edit.ManualCases {
		all = append(all, patchCase(c))
	}
	for _, tc := range all {
		if hasBlob(tc.Patch) {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			f := &fake{caseView: sampleCase()}
			req := request{method: http.MethodPatch, path: "/eval-cases/" + id1, contentType: "application/json", body: jsonOf(t, tc.Patch)}
			got := do(t, f, req)
			if !tc.Parse.OK {
				if diff := cmp.Diff(issueStrings(tc.Parse.Issues), issuesOf(t, got)); diff != "" {
					t.Errorf("issues mismatch (-want +got):\n%s", diff)
				}
				return
			}
			if got.status != http.StatusOK {
				t.Fatalf("PATCH = %d %s, want 200", got.status, got.body)
			}
			in, ok := f.args[2].(domain.UpdateInput)
			if !ok {
				t.Fatalf("Update args = %v", f.args)
			}
			want := roundTrip(t, tc.Parse.Data)
			if tc.Parse.Data == nil {
				want = map[string]any{}
			}
			if diff := cmp.Diff(want, roundTrip(t, updateMap(in))); diff != "" {
				t.Errorf("parsed input mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if len(all) < 20 {
		t.Errorf("golden has %d patch cases, want the section to be read", len(all))
	}
}

func TestGolden_QueryAndBodySchemas(t *testing.T) {
	validation, _, _ := loadGolden(t)
	seen := map[string]int{}
	for _, schema := range validation {
		for i, tc := range schema.Cases {
			if schema.Schema != "EvalSkillRunStartInput" && !allStrings(tc.Input) {
				continue // a query string carries text only
			}
			name := schema.Schema + "/" + jsonOf(t, tc.Input)
			t.Run(name, func(t *testing.T) {
				f := &fake{run: runningRun()}
				var req request
				query := url.Values{}
				for k, v := range tc.Input {
					s, _ := v.(string)
					query.Set(k, s)
				}
				switch schema.Schema {
				case "EvalRunListQuery":
					req = get("/agents/" + id1 + "/eval-runs?" + query.Encode())
				case "EvalCompareQuery":
					req = get("/eval-runs/compare?" + query.Encode())
				case "EvalAgentDashboardQuery":
					req = get("/agents/" + id1 + "/eval/dashboard?" + query.Encode())
				case "EvalSkillRunStartInput":
					req = postJSON("/skills/"+id1+"/eval-runs", jsonOf(t, tc.Input))
				default:
					t.Fatalf("unknown schema %s", schema.Schema)
				}
				got := do(t, f, req)
				seen[schema.Schema]++
				if !tc.OK {
					if diff := cmp.Diff(issueStrings(tc.Issues), issuesOf(t, got)); diff != "" {
						t.Errorf("case %d issues mismatch (-want +got):\n%s", i, diff)
					}
					return
				}
				if got.status/100 != 2 {
					t.Fatalf("%s = %d %s, want success", req.path, got.status, got.body)
				}
				switch schema.Schema {
				case "EvalRunListQuery":
					wantLimit, _ := tc.Data["limit"].(float64)
					wantPeriod, _ := tc.Data["period"].(string)
					if f.args[2] != int(wantLimit) || string(f.args[3].(domain.Period)) != wantPeriod {
						t.Errorf("list args = %v, want limit %v period %q", f.args, wantLimit, wantPeriod)
					}
				case "EvalCompareQuery":
					if f.args[1] != tc.Data["a"] || f.args[2] != tc.Data["b"] {
						t.Errorf("compare args = %v, want %v", f.args, tc.Data)
					}
				case "EvalAgentDashboardQuery":
					wantPeriod, _ := tc.Data["period"].(string)
					if string(f.args[2].(domain.Period)) != wantPeriod {
						t.Errorf("dashboard period = %v, want %q", f.args[2], wantPeriod)
					}
				case "EvalSkillRunStartInput":
					if !strings.EqualFold(f.args[2].(string), tc.Data["agent_id"].(string)) {
						t.Errorf("host agent = %v, want %v", f.args[2], tc.Data["agent_id"])
					}
				}
			})
		}
	}
	for _, schema := range []string{"EvalRunListQuery", "EvalCompareQuery", "EvalAgentDashboardQuery", "EvalSkillRunStartInput"} {
		if seen[schema] < 3 {
			t.Errorf("only %d golden cases of %s ran", seen[schema], schema)
		}
	}
}
