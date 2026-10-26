package domain_test

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/plugins/domain"
)

func TestPreview_ListsEverythingAndTheConflicts(t *testing.T) {
	up := parseOK(t, fullJSON)
	doc := domain.Preview(up, domain.Conflicts{Plugin: true, Skills: []string{"sec", "unrelated", "style"}})

	if doc.Kind != "plugin" || doc.Manifest.Kind != "plugin" {
		t.Errorf("kind = %q / %q, want plugin", doc.Kind, doc.Manifest.Kind)
	}
	if doc.Manifest.Name != "acme" {
		t.Errorf("manifest.name = %q, want the trimmed acme", doc.Manifest.Name)
	}
	if want := (domain.ManifestCounts{Agents: 2, Skills: 2, EvalCases: 2, Learnings: 2}); doc.Counts != want {
		t.Errorf("counts = %+v, want %+v", doc.Counts, want)
	}
	wantAgents := []domain.PreviewAgent{
		{Name: "Reviewer", Provider: "openrouter", Model: "m", Enabled: false, Skills: []string{"style", "sec"}},
		{Name: "Minimal", Provider: "openai", Model: "gpt", Enabled: true, Skills: []string{}},
	}
	if diff := cmp.Diff(wantAgents, doc.Agents); diff != "" {
		t.Errorf("agents mismatch (-want +got):\n%s", diff)
	}
	wantSkills := []domain.PreviewSkill{
		{Name: "style", Type: "rubric", Description: "s", Enabled: true},
		{Name: "sec", Type: "custom", Enabled: true},
	}
	if diff := cmp.Diff(wantSkills, doc.Skills); diff != "" {
		t.Errorf("skills mismatch (-want +got):\n%s", diff)
	}
	// Conflicts keep the bundle's order and drop names the bundle does not have.
	if want := (domain.Conflicts{Plugin: true, Skills: []string{"style", "sec"}}); !cmp.Equal(doc.Conflicts, want) {
		t.Errorf("conflicts = %+v, want %+v", doc.Conflicts, want)
	}
	if !doc.Conflicts.Any() {
		t.Error("Conflicts.Any() = false")
	}
	wantWarnings := []string{"1 learning(s) name a repository; they are installed without one"}
	if diff := cmp.Diff(wantWarnings, doc.Warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
	if len(doc.Ignored) != 0 {
		t.Errorf("ignored = %v, want none for a clean plugin", doc.Ignored)
	}
}

func TestPreview_NoConflictHasEmptyListsNotNil(t *testing.T) {
	doc := domain.Preview(parseOK(t, minimalJSON), domain.Conflicts{})
	if doc.Conflicts.Skills == nil || doc.Ignored == nil || doc.Warnings == nil || doc.Agents == nil || doc.Skills == nil {
		t.Errorf("a list is nil (JSON would be null): %+v", doc)
	}
	if doc.Conflicts.Any() {
		t.Error("Conflicts.Any() = true for no conflict")
	}
}

func TestPreview_WorkspaceExportListsWhatIsNeverInstalled(t *testing.T) {
	text := mutate(t, fullJSON, func(doc map[string]any) {
		doc["manifest"].(map[string]any)["kind"] = "workspace"
		doc["manifest"].(map[string]any)["name"] = "acme · payments"
		doc["conventions"] = []any{map[string]any{"rule": "a"}, map[string]any{"rule": "b"}}
		doc["context_files"] = []any{map[string]any{"repo": "r", "path": "p", "content": "c"}}
		doc["settings"] = map[string]any{"theme": "dark", "density": "compact"}
		doc["skills"].([]any)[0].(map[string]any)["versions"] = []any{map[string]any{"version": 1}, map[string]any{"version": 2}}
		doc["skills"].([]any)[1].(map[string]any)["versions"] = []any{map[string]any{"version": 1}}
		doc["extra"] = true
	})
	doc := domain.Preview(parseOK(t, text), domain.Conflicts{})
	if doc.Kind != "workspace" {
		t.Errorf("kind = %q, want workspace", doc.Kind)
	}
	if doc.Manifest.Name != "acme · payments" {
		t.Errorf("name = %q", doc.Manifest.Name)
	}
	wantIgnored := []domain.Ignored{
		{"extra", domain.ReasonUnknownKey},
		{"conventions", domain.ReasonNeverInstalled},
		{"skills[].versions", domain.ReasonNeverInstalled},
		{"context_files", domain.ReasonNeverInstalled},
		{"settings", domain.ReasonNeverInstalled},
	}
	if diff := cmp.Diff(wantIgnored, doc.Ignored); diff != "" {
		t.Errorf("ignored mismatch (-want +got):\n%s", diff)
	}
	wantWarnings := []string{
		"1 unknown key(s) were ignored",
		"2 convention(s) are ignored: they are never installed",
		"3 skill version(s) are ignored: they are never installed",
		"1 context file(s) are ignored: they are never installed",
		"2 setting(s) are ignored: they are never installed",
		"1 learning(s) name a repository; they are installed without one",
	}
	if diff := cmp.Diff(wantWarnings, doc.Warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}
	if want := (domain.ManifestCounts{Agents: 2, Skills: 2, EvalCases: 2, Conventions: 2, Learnings: 2, ContextFiles: 1}); doc.Counts != want {
		t.Errorf("counts = %+v, want %+v", doc.Counts, want)
	}
}

func TestPlanInstall(t *testing.T) {
	up := parseOK(t, fullJSON)
	if err := domain.Validate(up.Bundle); err != nil {
		t.Fatal(err)
	}
	plan := domain.PlanInstall(up.Bundle, "acme.zip")

	if plan.Name != "acme" || plan.Version != "1.2.3" || plan.Source != "acme.zip" || plan.Description == nil || *plan.Description != "d" {
		t.Errorf("plan header = %q %q %q %v, want acme 1.2.3 acme.zip d", plan.Name, plan.Version, plan.Source, plan.Description)
	}
	if got, want := plan.Created(), (domain.Created{Agents: 2, Skills: 2, EvalCases: 2, Conventions: 0, Learnings: 2}); got != want {
		t.Errorf("Created() = %+v, want %+v", got, want)
	}
	if diff := cmp.Diff([]string{"style", "sec"}, plan.SkillNames()); diff != "" {
		t.Errorf("SkillNames() mismatch (-want +got):\n%s", diff)
	}

	a := plan.Agents[0]
	if a.Name != "Reviewer" || a.Enabled || a.RepoIntel || a.Strategy != "auto" || a.CIFailOn != "any" || string(a.OutputSchema) != `{"type":"object"}` ||
		!cmp.Equal(a.Skills, []string{"style", "sec"}) {
		t.Errorf("agent 0 = %+v, want the bundle's values (enabled flag kept)", a)
	}
	for i, s := range plan.Skills {
		if s.Source != "imported_file" || s.SourceRef != "plugin:acme" || s.VersionNote != "Installed from plugin acme" {
			t.Errorf("skill %d provenance = %q %q %q, want imported_file / plugin:acme / Installed from plugin acme", i, s.Source, s.SourceRef, s.VersionNote)
		}
	}
	if plan.Skills[0].Name != "style" || !plan.Skills[0].Enabled || !cmp.Equal(plan.Skills[0].EvidenceFiles, []string{"a.go"}) {
		t.Errorf("skill 0 = %+v", plan.Skills[0])
	}
	c := plan.EvalCases[0]
	if c.OwnerKind != "agent" || c.OwnerRef != "Reviewer" || c.InputDiff == nil || *c.InputDiff != "diff" || c.InputMeta != nil || string(c.ExpectedOutput) != `{"findings":[]}` {
		t.Errorf("eval case 0 = %+v", c)
	}
	l := plan.Learnings[0]
	if l.Scope != "repo" || l.Kind != "decision" || l.Content != "use pgx" || l.Confidence == nil || *l.Confidence != 0.5 {
		t.Errorf("learning 0 = %+v (the repository is dropped, D13)", l)
	}
}

func TestPlanInstall_DoesNotAliasTheBundle(t *testing.T) {
	up := parseOK(t, fullJSON)
	plan := domain.PlanInstall(up.Bundle, "")
	plan.Agents[0].Skills[0] = "changed"
	plan.Skills[0].EvidenceFiles[0] = "changed"
	if up.Bundle.Agents[0].Skills[0] != "style" || up.Bundle.Skills[0].EvidenceFiles[0] != "a.go" {
		t.Error("changing the plan changed the bundle")
	}
}

func TestConflictError(t *testing.T) {
	err := error(&domain.ConflictError{Conflicts: domain.Conflicts{Plugin: true, Skills: []string{"a", "b"}}})
	if !errors.Is(err, domain.ErrConflict) {
		t.Error("errors.Is(err, ErrConflict) = false")
	}
	if got, want := err.Error(), "a plugin of this name is already installed; 2 skill name(s) already exist"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Error("a conflict is not an invalid upload")
	}
}

func TestLastExport(t *testing.T) {
	b := domain.Bundle{Agents: make([]domain.BundleAgent, 2), Skills: make([]domain.BundleSkill, 3), EvalCases: make([]domain.BundleCase, 4), Learnings: make([]domain.BundleLearning, 5)}
	b.Manifest.ExportedAt = "2026-10-26T09:00:00.000Z"
	last := domain.NewLastExport("x-2026-10-26.zip", b)
	raw, err := domain.EncodeLastExport(last)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"file_name":"x-2026-10-26.zip","exported_at":"2026-10-26T09:00:00.000Z","counts":{"agents":2,"skills":3,"eval_cases":4,"learnings":5}}`
	if string(raw) != want {
		t.Errorf("EncodeLastExport() = %s, want %s", raw, want)
	}
	got, ok := domain.DecodeLastExport(raw)
	if !ok || got != last {
		t.Errorf("DecodeLastExport() = %+v, %v, want %+v, true", got, ok, last)
	}
	for _, bad := range []string{``, `null`, `[]`, `"x"`, `{}`, `{"file_name":5}`, `{`} {
		if _, ok := domain.DecodeLastExport(json.RawMessage(bad)); ok {
			t.Errorf("DecodeLastExport(%q) ok = true, want none", bad)
		}
	}
}

// customProviderJSON has three agents: custom:ghost twice and custom:synapse (EC16).
const customProviderJSON = `{"manifest":{"name":"acme","version":"1.0.0","format":"devdigest-plugin/v1"},` +
	`"agents":[` +
	`{"name":"A","provider":"custom:ghost","model":"m","system_prompt":"p"},` +
	`{"name":"B","provider":"custom:synapse","model":"m","system_prompt":"p"},` +
	`{"name":"C","provider":"custom:ghost","model":"m","system_prompt":"p"},` +
	`{"name":"D","provider":"openai","model":"m","system_prompt":"p"}],` +
	`"skills":[],"eval_cases":[],"conventions":[]}`

func TestPrepared_CustomProviders(t *testing.T) {
	p, err := domain.Prepare(parseOK(t, customProviderJSON), "")
	if err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	if diff := cmp.Diff([]string{"custom:ghost", "custom:synapse"}, p.CustomProviders()); diff != "" {
		t.Errorf("CustomProviders() mismatch (-want +got):\n%s", diff)
	}
	plan := p.Plan.Agents
	if plan[0].Provider != "custom:ghost" || plan[1].Provider != "custom:synapse" || plan[3].Provider != "openai" {
		t.Errorf("Plan.Agents providers = %q %q %q, want the bundle strings kept", plan[0].Provider, plan[1].Provider, plan[3].Provider)
	}

	none, err := domain.Prepare(parseOK(t, minimalJSON), "")
	if err != nil {
		t.Fatalf("Prepare(minimal) = %v, want nil", err)
	}
	if got := none.CustomProviders(); len(got) != 0 {
		t.Errorf("CustomProviders() of a bundle without agents = %v, want none", got)
	}
}

func TestPreviewDoc_WithUndefinedProviders(t *testing.T) {
	p, err := domain.Prepare(parseOK(t, customProviderJSON), "")
	if err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	base := p.Preview(domain.Conflicts{})
	doc := base.WithUndefinedProviders([]string{"custom:ghost"})
	if diff := cmp.Diff([]string{"Provider custom:ghost is not configured here"}, doc.Warnings); diff != "" {
		t.Errorf("WithUndefinedProviders() warnings mismatch (-want +got):\n%s", diff)
	}
	if len(base.Warnings) != 0 {
		t.Errorf("the receiver's warnings = %v, want them untouched", base.Warnings)
	}
	if got := base.WithUndefinedProviders(nil).Warnings; len(got) != 0 || got == nil {
		t.Errorf("WithUndefinedProviders(nil).Warnings = %#v, want an empty non-nil list", got)
	}
}

// TestIsCustomProvider_MatchesTheCheckPattern pins the domain's copy of the id rule to the
// pattern of the agents_provider_chk constraint.
func TestIsCustomProvider_MatchesTheCheckPattern(t *testing.T) {
	pattern := regexp.MustCompile(`^custom:[a-z0-9-]{1,32}$`)
	for _, s := range []string{
		"custom:a", "custom:synapse", "custom:-", "custom:0", "custom:" + strings.Repeat("a", 32), "custom:" + strings.Repeat("a", 33),
		"custom:", "custom:A", "custom:a_b", "custom:a b", "custom:é", "Custom:a", "custom", "openai", "", " custom:a", "custom:a\n",
	} {
		// Go's `$` without (?m) matches only at the end of the text, like Postgres' ~ on the
		// whole string, so the two agree on a trailing newline too.
		if got, want := domain.IsCustomProvider(s), pattern.MatchString(s); got != want {
			t.Errorf("IsCustomProvider(%q) = %v, the CHECK pattern says %v", s, got, want)
		}
	}
}
