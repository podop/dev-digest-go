package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func TestRecord_SnapshotDropsTheUsage(t *testing.T) {
	change, conf := "feature", "high"
	detail := "not found"
	rec := domain.Record{
		PRID: "pr", Text: "Add a limiter.", InScope: []string{"a"}, OutOfScope: []string{"b"},
		ChangeType: &change, Confidence: &conf, DerivedFrom: domain.DerivedExplicit,
		Sources:   []reviewer.IntentSource{{Kind: domain.KindDoc, Ref: "docs/a.md", Status: domain.StatusFailed, Detail: &detail}},
		InputHash: "h", Provider: "openrouter", Model: "m", TokensIn: 3, TokensOut: 4,
	}
	want := reviewer.Intent{
		Text: "Add a limiter.", InScope: []string{"a"}, OutOfScope: []string{"b"},
		ChangeType: &change, Confidence: &conf, DerivedFrom: "explicit", Sources: rec.Sources,
	}
	if diff := cmp.Diff(want, rec.Snapshot()); diff != "" {
		t.Errorf("Record.Snapshot() mismatch (-want +got):\n%s", diff)
	}
}

func TestSource_IntentSourceDropsTheContent(t *testing.T) {
	detail := "doc total budget reached"
	s := domain.Source{Kind: domain.KindDoc, Ref: "docs/a.md", Status: domain.StatusSkipped, Detail: &detail, Content: "text"}
	want := reviewer.IntentSource{Kind: "doc", Ref: "docs/a.md", Status: "skipped", Detail: &detail}
	if diff := cmp.Diff(want, s.IntentSource()); diff != "" {
		t.Errorf("Source.IntentSource() mismatch (-want +got):\n%s", diff)
	}
}

// The constants mirror the shared contract enums (brief.ts); a typo here is a wire change.
func TestDomainConstantsMatchTheContract(t *testing.T) {
	if domain.DerivedInferred != "inferred" || domain.DerivedExplicit != "explicit" {
		t.Errorf("derived_from constants = %q, %q", domain.DerivedExplicit, domain.DerivedInferred)
	}
	if domain.PromptVersion != 2 || domain.Budget.Milliseconds() != 240_000 {
		t.Errorf("PromptVersion, Budget = %d, %v, want 2, 4m0s", domain.PromptVersion, domain.Budget)
	}
	if domain.EventInfo != string(reviewer.EventInfo) || domain.EventResult != string(reviewer.EventResult) {
		t.Errorf("event constants = %q, %q, want the reviewer event kinds", domain.EventInfo, domain.EventResult)
	}
}
