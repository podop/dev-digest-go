package domain_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/plugins/domain"
)

var exportInstant = time.Date(2026, 10, 26, 8, 30, 15, 123_000_000, time.UTC)

func exportFixture() domain.ExportData {
	return domain.ExportData{
		Agents: []domain.AgentRecord{
			{ID: "a1", Name: "Reviewer", Description: "first", Provider: "openai", Model: "gpt", SystemPrompt: "strict", OutputSchema: json.RawMessage(`{"type":"object"}`),
				Enabled: true, Strategy: "single-pass", CIFailOn: "critical", RepoIntel: true, SkillNames: []string{"s2", "s1", "s2", "outside"}},
			{ID: "a2", Name: "Reviewer", Provider: "anthropic", Model: "claude", SystemPrompt: "kind", Strategy: "map-reduce", CIFailOn: "never"},
			{ID: "a3", Name: "Reviewer", Provider: "openrouter", Model: "m", SystemPrompt: "x", Strategy: "auto", CIFailOn: "any", OutputSchema: json.RawMessage(`null`)},
		},
		Skills: []domain.SkillRecord{
			{ID: "k1", Name: "s1", Description: "d1", Type: "rubric", Source: "manual", Body: "b1", Enabled: true, EvidenceFiles: []string{"a.go"}},
			{ID: "k2", Name: "s2", Type: "custom", Source: "imported_file", Body: "b2", Enabled: false},
		},
		EvalCases: []domain.EvalCaseRecord{
			{Name: "on a1", OwnerKind: "agent", OwnerID: "a1", InputDiff: ptr("d"), ExpectedOutput: json.RawMessage(`{"findings":[]}`)},
			{Name: "on a2", OwnerKind: "agent", OwnerID: "a2"},
			{Name: "on a3", OwnerKind: "agent", OwnerID: "a3", InputMeta: json.RawMessage(`null`)},
			{Name: "on s2", OwnerKind: "skill", OwnerID: "k2", Notes: ptr("n")},
			{Name: "orphan agent", OwnerKind: "agent", OwnerID: "gone"},
			{Name: "skill id used as agent", OwnerKind: "agent", OwnerID: "k1"},
		},
		Learnings: []domain.LearningRecord{
			{Scope: "repo", Kind: "decision", Content: "use pgx", Confidence: ptr(0.5), Repo: ptr("acme/api")},
			{Scope: "global", Kind: "fact", Content: "tabs"},
		},
	}
}

func TestBuildBundle(t *testing.T) {
	b := domain.BuildBundle(exportFixture(), domain.Meta{Name: "devdigest-acme", Description: ptr("desc"), Now: exportInstant})

	m := b.Manifest
	if m.Name != "devdigest-acme" || m.Version != "dev" || m.Format != "devdigest-plugin/v1" || m.Kind != "plugin" ||
		m.ExportedAt != "2026-10-26T08:30:15.123Z" || m.Description == nil || *m.Description != "desc" {
		t.Errorf("manifest = %+v", m)
	}
	if want := (domain.ManifestCounts{Agents: 3, Skills: 2, EvalCases: 4, Learnings: 2}); m.Counts != want {
		t.Errorf("counts = %+v, want %+v", m.Counts, want)
	}

	names := []string{b.Agents[0].Name, b.Agents[1].Name, b.Agents[2].Name}
	if diff := cmp.Diff([]string{"Reviewer", "Reviewer (2)", "Reviewer (3)"}, names); diff != "" {
		t.Errorf("agent names mismatch (-want +got):\n%s", diff)
	}
	// Skills follow link order, each once, and only skills that are in the bundle.
	if diff := cmp.Diff([]string{"s2", "s1"}, b.Agents[0].Skills); diff != "" {
		t.Errorf("agent 0 skills mismatch (-want +got):\n%s", diff)
	}
	if b.Agents[1].Skills == nil || len(b.Agents[1].Skills) != 0 {
		t.Errorf("agent 1 skills = %#v, want []", b.Agents[1].Skills)
	}
	if b.Agents[2].OutputSchema != nil {
		t.Errorf("a stored JSON null output schema = %s, want nil", b.Agents[2].OutputSchema)
	}

	type ref struct{ Name, Kind, Owner string }
	var got []ref
	for _, c := range b.EvalCases {
		got = append(got, ref{c.Name, c.OwnerKind, c.OwnerRef})
	}
	want := []ref{{"on a1", "agent", "Reviewer"}, {"on a2", "agent", "Reviewer (2)"}, {"on a3", "agent", "Reviewer (3)"}, {"on s2", "skill", "s2"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("eval case owner refs mismatch (-want +got):\n%s", diff)
	}
	if b.EvalCases[2].InputMeta != nil {
		t.Errorf("eval case input_meta = %s, want nil for a stored null", b.EvalCases[2].InputMeta)
	}
	if !b.Skills[0].Enabled || b.Skills[1].Enabled {
		t.Error("skills lost their enabled flags")
	}
	if b.Conventions == nil || len(b.Conventions) != 0 {
		t.Errorf("conventions = %#v, want []", b.Conventions)
	}
	if err := domain.Validate(b); err != nil {
		t.Errorf("an exported bundle does not validate: %v", err)
	}
}

func TestBuildBundle_JSONShape(t *testing.T) {
	b := domain.BuildBundle(exportFixture(), domain.Meta{Name: "p", Version: "2.0.0", Now: exportInstant})
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"context_files", "settings"} {
		if _, ok := doc[key]; ok {
			t.Errorf("exported bundle has %q, want it left out", key)
		}
	}
	if string(doc["conventions"]) != "[]" {
		t.Errorf("conventions = %s, want []", doc["conventions"])
	}
	var skills []map[string]json.RawMessage
	_ = json.Unmarshal(doc["skills"], &skills)
	if _, ok := skills[0]["versions"]; ok {
		t.Error("an exported skill has versions, want them left out")
	}
	var manifest map[string]json.RawMessage
	_ = json.Unmarshal(doc["manifest"], &manifest)
	if string(manifest["kind"]) != `"plugin"` || string(manifest["version"]) != `"2.0.0"` {
		t.Errorf("manifest kind/version = %s / %s", manifest["kind"], manifest["version"])
	}
	if strings.Contains(string(manifest["counts"]), "context_files") {
		t.Errorf("manifest counts = %s, want no context_files", manifest["counts"])
	}
	// Without learnings the list is still written, as [].
	empty := domain.BuildBundle(domain.ExportData{Skills: exportFixture().Skills}, domain.Meta{Name: "p", Now: exportInstant})
	raw, _ = json.Marshal(empty)
	if !strings.Contains(string(raw), `"learnings":[]`) || !strings.Contains(string(raw), `"agents":[]`) || !strings.Contains(string(raw), `"eval_cases":[]`) {
		t.Errorf("empty lists are not written as []: %s", raw)
	}
}

func TestEncodeArchive_RoundTrip(t *testing.T) {
	b := domain.BuildBundle(exportFixture(), domain.Meta{Name: "p", Now: exportInstant})
	data, err := domain.EncodeArchive(b)
	if err != nil {
		t.Fatalf("EncodeArchive() error = %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the output is not a zip: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != domain.BundleFile || zr.File[0].Method != zip.Deflate {
		t.Fatalf("archive entries = %v, want one deflated %s", zr.File, domain.BundleFile)
	}
	// The zip directory keeps 2-second DOS time.
	if d := exportInstant.Sub(zr.File[0].Modified); d < 0 || d >= 2*time.Second {
		t.Errorf("entry time = %v, want the export instant %v", zr.File[0].Modified, exportInstant)
	}

	up, err := domain.ParseUpload("application/zip", data)
	if err != nil {
		t.Fatalf("ParseUpload(EncodeArchive()) error = %v", err)
	}
	if len(up.Ignored) != 0 {
		t.Errorf("ignored = %v, want none", up.Ignored)
	}
	if err := domain.Validate(up.Bundle); err != nil {
		t.Fatalf("Validate(round trip) = %v", err)
	}
	if diff := cmp.Diff(b, up.Bundle); diff != "" {
		t.Errorf("round trip mismatch (-export +import):\n%s", diff)
	}

	again, err := domain.EncodeArchive(b)
	if err != nil || !bytes.Equal(data, again) {
		t.Errorf("EncodeArchive is not deterministic (err %v)", err)
	}
}

func TestEncodeArchive_TooLarge(t *testing.T) {
	big := func(n int) domain.Bundle {
		return domain.Bundle{
			Manifest: domain.Manifest{Name: "p", Format: domain.BundleFormat, Kind: domain.KindPlugin},
			Skills:   []domain.BundleSkill{{Name: "s", Body: strings.Repeat("x", n)}},
		}
	}
	// The JSON around the body is a fixed overhead; find the body size that lands exactly on the cap.
	probe, _ := json.Marshal(big(0))
	overhead := len(probe)
	if _, err := domain.EncodeArchive(big(domain.MaxInflatedBytes - overhead)); err != nil {
		t.Errorf("a bundle of exactly %d bytes: error = %v, want nil", domain.MaxInflatedBytes, err)
	}
	if _, err := domain.EncodeArchive(big(domain.MaxInflatedBytes - overhead + 1)); !errors.Is(err, domain.ErrExportTooLarge) {
		t.Errorf("a bundle of %d bytes: error = %v, want ErrExportTooLarge", domain.MaxInflatedBytes+1, err)
	}
}
