package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/workspace/domain"
)

func ptr[T any](v T) *T { return &v }

var exportInstant = time.Date(2026, 10, 23, 8, 30, 15, 123_000_000, time.UTC)

func fixture() domain.ExportData {
	return domain.ExportData{
		Agents: []domain.AgentRecord{
			{
				ID: "agent-uuid-1", Name: "Reviewer", Description: "first", Provider: "openai", Model: "gpt-4o",
				SystemPrompt: "Be strict.", OutputSchema: json.RawMessage(`{"type":"object"}`), Enabled: true,
				Strategy: "single-pass", CIFailOn: "critical", RepoIntel: true, SkillNames: []string{"Style", "Security"},
			},
			{
				ID: "agent-uuid-2", Name: "Reviewer", Description: "second", Provider: "anthropic", Model: "claude",
				SystemPrompt: "Be kind.", Enabled: false, Strategy: "map-reduce", CIFailOn: "never",
			},
		},
		Skills: []domain.SkillRecord{
			{
				ID: "skill-uuid-1", Name: "Security", Description: "d", Type: "security", Source: "imported_file", Body: "v3 body",
				Enabled: true, EvidenceFiles: json.RawMessage(`["a.go"]`),
				Versions: []domain.SkillVersionRecord{
					{Version: 1, Body: "v1", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.FixedZone("x", 3600))},
					{Version: 2, Body: "v2", Description: ptr("desc"), Message: ptr("msg"), CreatedAt: exportInstant},
					{Version: 3, Body: "v3 body", CreatedAt: exportInstant},
				},
			},
			{ID: "skill-uuid-2", Name: "Style", Description: "s", Type: "custom", Source: "manual", Body: "b"},
		},
		EvalCases: []domain.EvalCaseRecord{
			{Name: "case on first agent", OwnerKind: domain.OwnerAgent, OwnerID: "agent-uuid-1", InputDiff: ptr("diff")},
			{Name: "case on second agent", OwnerKind: domain.OwnerAgent, OwnerID: "agent-uuid-2", ExpectedOutput: json.RawMessage(`{"findings":[]}`)},
			{Name: "case on skill", OwnerKind: domain.OwnerSkill, OwnerID: "skill-uuid-1", Notes: ptr("n")},
			{Name: "orphan agent case", OwnerKind: domain.OwnerAgent, OwnerID: "gone"},
			{Name: "orphan skill case", OwnerKind: domain.OwnerSkill, OwnerID: "agent-uuid-1"},
		},
		Conventions: []domain.ConventionRecord{
			{Repo: "acme/api", Rule: "Wrap errors", Category: "error-handling", Status: "accepted", Confidence: ptr(0.9), EvidencePath: ptr("a/b.go"), EvidenceSnippet: ptr("snip")},
			{Repo: "acme/api", Rule: "No globals", Category: "style", Status: "pending"},
		},
		Learnings: []domain.LearningRecord{
			{Scope: "repo", Kind: "decision", Content: "use pgx", Confidence: ptr(0.5), Repo: ptr("acme/api")},
			{Scope: "global", Kind: "fact", Content: "tabs"},
		},
		ContextFiles: []domain.ContextFileRecord{{Repo: "acme/api", Path: "CLAUDE.md", Content: "# rules"}},
		Settings: map[string]json.RawMessage{
			"theme":             json.RawMessage(`"dark"`),
			"workspace_name":    json.RawMessage(`"acme"`),
			"automatic_reviews": json.RawMessage(`{"agent_ids":["agent-uuid-1"],"enabled_at":"2026-10-23T00:00:00.000Z","enabled":true}`),
		},
	}
}

func decode(t *testing.T, b domain.Bundle) any {
	t.Helper()
	enc, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal(bundle) error = %v", err)
	}
	var v any
	if err := json.Unmarshal(enc, &v); err != nil {
		t.Fatalf("Unmarshal(bundle) error = %v", err)
	}
	return v
}

func TestBuildBundle(t *testing.T) {
	got := domain.BuildBundle(fixture(), domain.Meta{Name: "acme · payments", Version: "1.2.3", Now: exportInstant})

	want := `{
	  "manifest": {"name":"acme · payments","version":"1.2.3","format":"devdigest-plugin/v1","kind":"workspace",
	    "exported_at":"2026-10-23T08:30:15.123Z","description":null,
	    "counts":{"agents":2,"skills":2,"eval_cases":3,"conventions":2,"learnings":2,"context_files":1}},
	  "agents": [
	    {"name":"Reviewer","description":"first","provider":"openai","model":"gpt-4o","system_prompt":"Be strict.",
	     "output_schema":{"type":"object"},"enabled":true,"skills":["Style","Security"],
	     "strategy":"single-pass","ci_fail_on":"critical","repo_intel":true},
	    {"name":"Reviewer (2)","description":"second","provider":"anthropic","model":"claude","system_prompt":"Be kind.",
	     "output_schema":null,"enabled":false,"skills":[],
	     "strategy":"map-reduce","ci_fail_on":"never","repo_intel":false}
	  ],
	  "skills": [
	    {"name":"Security","description":"d","type":"security","source":"imported_file","body":"v3 body","enabled":true,
	     "evidence_files":["a.go"],"versions":[
	       {"version":1,"body":"v1","description":null,"message":null,"created_at":"2026-01-02T02:04:05.006Z"},
	       {"version":2,"body":"v2","description":"desc","message":"msg","created_at":"2026-10-23T08:30:15.123Z"},
	       {"version":3,"body":"v3 body","description":null,"message":null,"created_at":"2026-10-23T08:30:15.123Z"}]},
	    {"name":"Style","description":"s","type":"custom","source":"manual","body":"b","enabled":false,
	     "evidence_files":null,"versions":[]}
	  ],
	  "eval_cases": [
	    {"name":"case on first agent","owner_kind":"agent","owner_ref":"Reviewer","input_diff":"diff","input_files":null,"input_meta":null,"expected_output":null,"notes":null},
	    {"name":"case on second agent","owner_kind":"agent","owner_ref":"Reviewer (2)","input_diff":null,"input_files":null,"input_meta":null,"expected_output":{"findings":[]},"notes":null},
	    {"name":"case on skill","owner_kind":"skill","owner_ref":"Security","input_diff":null,"input_files":null,"input_meta":null,"expected_output":null,"notes":"n"}
	  ],
	  "conventions": [
	    {"rule":"Wrap errors","evidence_path":"a/b.go","evidence_snippet":"snip","confidence":0.9,"accepted":true,"repo":"acme/api","category":"error-handling","status":"accepted"},
	    {"rule":"No globals","evidence_path":null,"evidence_snippet":null,"confidence":null,"accepted":false,"repo":"acme/api","category":"style","status":"pending"}
	  ],
	  "learnings": [
	    {"scope":"repo","kind":"decision","content":"use pgx","confidence":0.5,"repo":"acme/api"},
	    {"scope":"global","kind":"fact","content":"tabs","confidence":null,"repo":null}
	  ],
	  "context_files": [{"repo":"acme/api","path":"CLAUDE.md","content":"# rules"}],
	  "settings": {"theme":"dark","automatic_reviews":{"enabled":true}}
	}`
	var wantV any
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("bad expectation: %v", err)
	}
	if diff := cmp.Diff(wantV, decode(t, got)); diff != "" {
		t.Errorf("BuildBundle() mismatch (-want +got):\n%s", diff)
	}
}

// EC12: an empty workspace is a valid bundle with empty arrays, settings {} and zero counts.
func TestBuildBundleEmpty(t *testing.T) {
	got := domain.BuildBundle(domain.ExportData{}, domain.Meta{Name: "default", Now: exportInstant})
	enc, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	want := `{"manifest":{"name":"default","version":"dev","format":"devdigest-plugin/v1","kind":"workspace",` +
		`"exported_at":"2026-10-23T08:30:15.123Z","description":null,` +
		`"counts":{"agents":0,"skills":0,"eval_cases":0,"conventions":0,"learnings":0,"context_files":0}},` +
		`"agents":[],"skills":[],"eval_cases":[],"conventions":[],"learnings":[],"context_files":[],"settings":{}}`
	if string(enc) != want {
		t.Errorf("BuildBundle(empty) =\n%s\nwant\n%s", enc, want)
	}
}

func TestBuildBundleExportedAtIsUTC(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 7_000_000, time.FixedZone("UTC+2", 2*3600))
	got := domain.BuildBundle(domain.ExportData{}, domain.Meta{Name: "n", Now: now})
	if want := "2026-10-23T08:00:00.007Z"; got.Manifest.ExportedAt != want {
		t.Errorf("ExportedAt = %q, want %q", got.Manifest.ExportedAt, want)
	}
}

// FR14 / NFR1: no row id, no secret-like or non-allow-listed setting reaches the bytes.
func TestBuildBundleLeaksNothing(t *testing.T) {
	data := fixture()
	data.Settings["api_key"] = json.RawMessage(`"sk-live-supersecret"`)
	data.Settings["token_like"] = json.RawMessage(`"ghp_abcdefghijklmnopqrstuvwxyz0123456789"`)
	enc, err := json.Marshal(domain.BuildBundle(data, domain.Meta{Name: "acme", Now: exportInstant}))
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	for _, needle := range []string{
		"agent-uuid-1", "agent-uuid-2", "skill-uuid-1", "skill-uuid-2", "agent_ids", "enabled_at",
		"sk-live-supersecret", "ghp_", "api_key", "token_like", "workspace_name", "orphan",
	} {
		if strings.Contains(string(enc), needle) {
			t.Errorf("bundle contains %q, want it absent", needle)
		}
	}
}

func TestBuildBundleDoesNotChangeInput(t *testing.T) {
	data := fixture()
	domain.BuildBundle(data, domain.Meta{Name: "acme", Now: exportInstant})
	if got := data.Agents[1].Name; got != "Reviewer" {
		t.Errorf("input agent name = %q after BuildBundle, want unchanged", got)
	}
	if got := string(data.Settings["automatic_reviews"]); !strings.Contains(got, "agent_ids") {
		t.Errorf("input automatic_reviews = %s after BuildBundle, want unchanged", got)
	}
}
