package domain_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func TestClampIntentText(t *testing.T) {
	if got := domain.ClampIntentText("  Add a limiter.\n"); got != "Add a limiter." {
		t.Errorf("ClampIntentText(padded) = %q", got)
	}
	got := domain.ClampIntentText(strings.Repeat("x", 501))
	if reviewer.Len16(got) != 500 || got != strings.Repeat("x", 499)+"…" {
		t.Errorf("ClampIntentText(501 units) = %d units, want 499 x and an ellipsis", reviewer.Len16(got))
	}
	if got := domain.ClampIntentText(strings.Repeat("x", 500)); got != strings.Repeat("x", 500) {
		t.Errorf("ClampIntentText(500 units) was shortened")
	}
}

func TestClampScopeList(t *testing.T) {
	items := make([]string, 0, 14)
	for i := 0; i < 14; i++ {
		items = append(items, strings.Repeat(string(rune('a'+i)), 3))
	}
	if diff := cmp.Diff(items[:10], domain.ClampScopeList(items)); diff != "" {
		t.Errorf("ClampScopeList(14 items) mismatch (-want +got):\n%s", diff)
	}
	// Empty items are dropped after the first ten were taken; each item is trimmed and cut.
	in := []string{"  keep  ", "", " \n", strings.Repeat("y", 300)}
	got := domain.ClampScopeList(in)
	if len(got) != 2 || got[0] != "keep" || got[1] != strings.Repeat("y", 239)+"…" {
		t.Errorf("ClampScopeList(%q) = %q", in, got)
	}
	tail := append(make([]string, 10), "late")
	if got := domain.ClampScopeList(tail); got == nil || len(got) != 0 {
		t.Errorf("ClampScopeList(ten empty, then one) = %#v, want an empty non-nil slice (the 11th is dropped)", got)
	}
	if got := domain.ClampScopeList(nil); got == nil || len(got) != 0 {
		t.Errorf("ClampScopeList(nil) = %#v, want an empty non-nil slice", got)
	}
}

func TestBuildIntent(t *testing.T) {
	sources := []domain.Source{
		domain.TitleSource("t"),
		domain.FailedSource("doc", "a.md", "not found"),
	}
	got := domain.BuildIntent(domain.Classification{
		Intent: " Do it. ", InScope: []string{" a ", ""}, OutOfScope: nil, ChangeType: "bugfix",
	}, "explicit", "medium", sources)
	change, conf, detail := "bugfix", "medium", "not found"
	want := reviewer.Intent{
		Text: "Do it.", InScope: []string{"a"}, OutOfScope: []string{}, ChangeType: &change, Confidence: &conf, DerivedFrom: "explicit",
		Sources: []reviewer.IntentSource{
			{Kind: "title", Ref: "title", Status: "used"},
			{Kind: "doc", Ref: "a.md", Status: "failed", Detail: &detail},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildIntent() mismatch (-want +got):\n%s", diff)
	}
}
