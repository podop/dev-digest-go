package domain_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/plugins/domain"
)

func TestParseUpload_DefaultsAndUnknownKeys(t *testing.T) {
	text := mutate(t, fullJSON, func(doc map[string]any) {
		doc["color"] = "red"
		doc["manifest"].(map[string]any)["homepage"] = "x"
		doc["agents"].([]any)[1].(map[string]any)["icon"] = "i"
		doc["skills"].([]any)[1].(map[string]any)["tags"] = []any{"a"}
		doc["learnings"].([]any)[0].(map[string]any)["sources"] = 1
	})
	up := parseOK(t, text)

	wantIgnored := []domain.Ignored{
		{"color", domain.ReasonUnknownKey},
		{"manifest.homepage", domain.ReasonUnknownKey},
		{"agents[1].icon", domain.ReasonUnknownKey},
		{"skills[1].tags", domain.ReasonUnknownKey},
		{"learnings[0].sources", domain.ReasonUnknownKey},
	}
	if diff := cmp.Diff(wantIgnored, up.Ignored); diff != "" {
		t.Errorf("ignored mismatch (-want +got):\n%s", diff)
	}

	b := up.Bundle
	wantAgent := domain.BundleAgent{
		Name: "Minimal", Provider: "openai", Model: "gpt", SystemPrompt: "p", Enabled: true, Skills: []string{},
		Strategy: "single-pass", CIFailOn: "critical", RepoIntel: true,
	}
	if diff := cmp.Diff(wantAgent, b.Agents[1]); diff != "" {
		t.Errorf("defaulted agent mismatch (-want +got):\n%s", diff)
	}
	wantSkill := domain.BundleSkill{Name: "sec", Type: "custom", Source: "imported_file", Body: "b2", Enabled: true}
	if diff := cmp.Diff(wantSkill, b.Skills[1]); diff != "" {
		t.Errorf("defaulted skill mismatch (-want +got):\n%s", diff)
	}
	if got := b.Learnings[1]; got.Scope != "global" || got.Kind != "learning" || got.Content != "tabs" {
		t.Errorf("defaulted learning = %+v, want global/learning/tabs", got)
	}
	// An explicit false stays false; an explicit value stays.
	if a := b.Agents[0]; a.Enabled || a.RepoIntel || a.Strategy != "auto" || a.CIFailOn != "any" {
		t.Errorf("explicit agent fields were overridden: %+v", a)
	}
	// JSON null reads as nil, and the manifest counts are the real list lengths.
	if b.EvalCases[0].InputMeta != nil {
		t.Errorf("input_meta = %s, want nil for null", b.EvalCases[0].InputMeta)
	}
	if got, want := b.Manifest.Counts, (domain.ManifestCounts{Agents: 2, Skills: 2, EvalCases: 2, Learnings: 2}); got != want {
		t.Errorf("counts = %+v, want %+v (the manifest's own counts are never trusted)", got, want)
	}
}

func TestParseUpload_NullOrMissingSectionsAreEmpty(t *testing.T) {
	for _, text := range []string{
		`{"manifest":{"name":"a","format":"devdigest-plugin/v1"}}`,
		`{"manifest":{"name":"a","format":"devdigest-plugin/v1"},"agents":null,"skills":null,"eval_cases":null,"conventions":null,"learnings":null}`,
	} {
		up := parseOK(t, text)
		b := up.Bundle
		if b.Agents == nil || b.Skills == nil || b.EvalCases == nil || b.Learnings == nil || b.Conventions == nil {
			t.Errorf("ParseUpload(%s): a list is nil: %+v", text, b)
		}
		if err := domain.Validate(b); err != nil {
			t.Errorf("Validate(%s) = %v", text, err)
		}
	}
}

func TestParseUpload_WrongShapes(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"empty", ``, "not valid JSON"},
		{"truncated", `{"manifest":`, "not valid JSON"},
		{"array", `[]`, "must be a JSON object"},
		{"null", `null`, "must be a JSON object"},
		{"string", `"x"`, "must be a JSON object"},
		{"no manifest", `{"agents":[]}`, "manifest is required"},
		{"null manifest", `{"manifest":null}`, "manifest is required"},
		{"manifest not an object", `{"manifest":[]}`, "manifest must be an object"},
		{"manifest name wrong type", `{"manifest":{"name":5}}`, "manifest.name must be a string (found number)"},
		{"agents not an array", `{"manifest":{},"agents":{}}`, "agents must be an array"},
		{"agent not an object", `{"manifest":{},"agents":[1]}`, "agents[0] must be an object"},
		{"agent null", `{"manifest":{},"agents":[null]}`, "agents[0] must be an object"},
		{"agent enabled wrong type", `{"manifest":{},"agents":[{"enabled":"yes"}]}`, "agents[0].enabled must be a boolean (found string)"},
		{"skill list wrong type", `{"manifest":{},"agents":[{"skills":"a"}]}`, "agents[0].skills must be an array (found string)"},
		{"skill name wrong type", `{"manifest":{},"skills":[{},{"name":true}]}`, "skills[1].name must be a string (found bool)"},
		{"confidence wrong type", `{"manifest":{},"learnings":[{"confidence":"high"}]}`, "learnings[0].confidence must be a number (found string)"},
		{"settings not an object", `{"manifest":{},"settings":[]}`, "settings must be an object"},
		{"conventions not an array", `{"manifest":{},"conventions":{}}`, "conventions must be an array"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ParseUpload("application/json", []byte(tc.text))
			wantInvalid(t, err, tc.want)
		})
	}
}

func TestParseUpload_ListLimitsAtDecode(t *testing.T) {
	items := func(n int, item string) string {
		return "[" + strings.TrimSuffix(strings.Repeat(item+",", n), ",") + "]"
	}
	tests := []struct {
		name    string
		section string
		item    string
		limit   int
	}{
		{"agents", "agents", `{}`, domain.MaxAgents},
		{"skills", "skills", `{}`, domain.MaxSkills},
		{"eval_cases", "eval_cases", `{}`, domain.MaxEvalCases},
		{"learnings", "learnings", `{}`, domain.MaxLearnings},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at := fmt.Sprintf(`{"manifest":{},%q:%s}`, tc.section, items(tc.limit, tc.item))
			if _, err := domain.ParseUpload("application/json", []byte(at)); err != nil {
				t.Errorf("%d %s: error = %v, want nil", tc.limit, tc.section, err)
			}
			over := fmt.Sprintf(`{"manifest":{},%q:%s}`, tc.section, items(tc.limit+1, tc.item))
			_, err := domain.ParseUpload("application/json", []byte(over))
			wantInvalid(t, err, fmt.Sprintf("%s has %d entries; the limit is %d", tc.section, tc.limit+1, tc.limit))
		})
	}
}

func TestParseUpload_BOMAndUTF8(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	if _, err := domain.ParseUpload("application/json", append(bom, minimalJSON...)); err != nil {
		t.Errorf("BOM + JSON: error = %v, want nil", err)
	}
	if _, err := domain.ParseUpload("application/json", append(append(bom, bom...), minimalJSON...)); err == nil {
		t.Error("two BOMs: error = nil, want the second to be refused")
	}
	for name, body := range map[string]string{
		"lone continuation byte": "{\"manifest\":{\"name\":\"\x80\"}}",
		"truncated sequence":     "{\"manifest\":{\"name\":\"\xe2\x82\"}}",
		"overlong NUL":           "{\"manifest\":{\"name\":\"\xc0\x80\"}}",
		"surrogate half":         "{\"manifest\":{\"name\":\"\xed\xa0\x80\"}}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := domain.ParseUpload("application/json", []byte(body))
			wantInvalid(t, err, "not valid UTF-8")
		})
	}
	up := parseOK(t, `{"manifest":{"name":"café 😀","format":"devdigest-plugin/v1"}}`)
	if got := up.Bundle.Manifest.Name; got != "café 😀" {
		t.Errorf("name = %q, want café 😀", got)
	}
}

func TestParseUpload_SlicesBWorkspaceExport(t *testing.T) {
	text := mutate(t, fullJSON, func(doc map[string]any) {
		m := doc["manifest"].(map[string]any)
		m["name"] = "acme · payments"
		m["kind"] = "workspace"
		m["counts"] = map[string]any{"agents": 2, "skills": 2, "eval_cases": 2, "conventions": 1, "learnings": 2, "context_files": 1}
		doc["conventions"] = []any{map[string]any{"rule": "r", "repo": "acme/api"}}
		doc["context_files"] = []any{map[string]any{"repo": "acme/api", "path": "CLAUDE.md", "content": "# rules"}}
		doc["settings"] = map[string]any{"theme": "dark"}
		doc["skills"].([]any)[0].(map[string]any)["versions"] = []any{map[string]any{"version": 1, "body": "b0"}}
	})
	up := parseOK(t, text)
	if err := domain.Validate(up.Bundle); err != nil {
		t.Fatalf("Validate(workspace export) = %v", err)
	}
	if got := up.Bundle.Manifest.Kind; got != domain.KindWorkspace {
		t.Errorf("kind = %q, want workspace", got)
	}
	raw, _ := json.Marshal(up.Bundle.Settings)
	if string(raw) != `{"theme":"dark"}` {
		t.Errorf("settings = %s, want the stored object", raw)
	}
}
