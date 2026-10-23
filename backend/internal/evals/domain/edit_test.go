package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
)

var editNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func storedCase() domain.Case {
	return domain.Case{
		ID: "c1", WorkspaceID: "ws", OwnerKind: domain.OwnerAgent, OwnerID: "ag", Name: "Name",
		Notes: sp("notes"),
		Expectation: domain.Expectation{
			Kind: domain.MustFind, File: "a.go", StartLine: 5, EndLine: 8,
			Severity: sp("high"), Category: sp("bug"), Title: sp("T"),
		},
		InputDiff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -5 +5 @@\n+x",
		InputMeta: domain.InputMeta{PRTitle: "PR", PRDescription: sp("desc")},
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func set(s string) domain.Field[string] { return domain.Field[string]{Set: true, Value: &s} }
func null() domain.Field[string]        { return domain.Field[string]{Set: true} }

func TestMergeEdit(t *testing.T) {
	kind := domain.MustNotFlag
	tests := []struct {
		name        string
		patch       domain.UpdateInput
		mutateWant  func(*domain.Case)
		wantRecheck bool
		wantChanged bool
	}{
		{"empty patch changes nothing", domain.UpdateInput{}, func(*domain.Case) {}, false, false},
		{
			"name only is not content", domain.UpdateInput{Name: sp("New")},
			func(c *domain.Case) { c.Name = "New" }, false, false,
		},
		{
			"notes only is not content", domain.UpdateInput{Notes: set("n2")},
			func(c *domain.Case) { c.Notes = sp("n2") }, false, false,
		},
		{
			"null notes clears", domain.UpdateInput{Notes: null()},
			func(c *domain.Case) { c.Notes = nil }, false, false,
		},
		{
			"same severity is no change", domain.UpdateInput{Severity: set("high")},
			func(*domain.Case) {}, false, false,
		},
		{
			"severity changes content", domain.UpdateInput{Severity: set("low")},
			func(c *domain.Case) { c.Expectation.Severity = sp("low"); c.ContentEditedAt = &editNow }, false, true,
		},
		{
			"null category clears", domain.UpdateInput{Category: null()},
			func(c *domain.Case) { c.Expectation.Category = nil; c.ContentEditedAt = &editNow }, false, true,
		},
		{
			"null title clears", domain.UpdateInput{Title: null()},
			func(c *domain.Case) { c.Expectation.Title = nil; c.ContentEditedAt = &editNow }, false, true,
		},
		{
			"kind", domain.UpdateInput{Kind: &kind},
			func(c *domain.Case) { c.Expectation.Kind = kind; c.ContentEditedAt = &editNow }, false, true,
		},
		{
			"only the end line", domain.UpdateInput{EndLine: ip(20)},
			func(c *domain.Case) { c.Expectation.EndLine = 20; c.ContentEditedAt = &editNow }, true, true,
		},
		{
			"only the start line", domain.UpdateInput{StartLine: ip(6)},
			func(c *domain.Case) { c.Expectation.StartLine = 6; c.ContentEditedAt = &editNow }, true, true,
		},
		{
			"lines equal to stored still recheck", domain.UpdateInput{StartLine: ip(5), EndLine: ip(8)},
			func(*domain.Case) {}, true, false,
		},
		{
			"file", domain.UpdateInput{File: sp("b.go")},
			func(c *domain.Case) { c.Expectation.File = "b.go"; c.ContentEditedAt = &editNow }, true, true,
		},
		{
			"new diff is normalised against the merged file",
			domain.UpdateInput{File: sp("b.go"), InputDiff: sp("@@ -1 +1 @@\r\n+z")},
			func(c *domain.Case) {
				c.Expectation.File = "b.go"
				c.InputDiff = "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n+z"
				c.ContentEditedAt = &editNow
			}, true, true,
		},
		{
			"same diff is no change", domain.UpdateInput{InputDiff: sp(storedCase().InputDiff)},
			func(*domain.Case) {}, true, false,
		},
		{
			"pr title", domain.UpdateInput{PRTitle: set("Other")},
			func(c *domain.Case) { c.InputMeta.PRTitle = "Other"; c.ContentEditedAt = &editNow }, false, true,
		},
		{
			"null pr title becomes the stored name", domain.UpdateInput{PRTitle: null()},
			func(c *domain.Case) { c.InputMeta.PRTitle = "Name"; c.ContentEditedAt = &editNow }, false, true,
		},
		{
			"blank pr title becomes the merged name", domain.UpdateInput{Name: sp("Fresh"), PRTitle: set(" \n")},
			func(c *domain.Case) { c.Name = "Fresh"; c.InputMeta.PRTitle = "Fresh"; c.ContentEditedAt = &editNow }, false, true,
		},
		{
			"renaming does not move the pr title", domain.UpdateInput{Name: sp("Fresh")},
			func(c *domain.Case) { c.Name = "Fresh" }, false, false,
		},
		{
			"null description clears", domain.UpdateInput{PRDescription: null()},
			func(c *domain.Case) { c.InputMeta.PRDescription = nil; c.ContentEditedAt = &editNow }, false, true,
		},
		{
			"blank description clears", domain.UpdateInput{PRDescription: set("  ")},
			func(c *domain.Case) { c.InputMeta.PRDescription = nil; c.ContentEditedAt = &editNow }, false, true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stored := storedCase()
			got, err := domain.MergeEdit(stored, tc.patch, editNow)
			if err != nil {
				t.Fatalf("MergeEdit: %v", err)
			}
			want := storedCase()
			tc.mutateWant(&want)
			if diff := cmp.Diff(want, got.Case); diff != "" {
				t.Errorf("MergeEdit case mismatch (-want +got):\n%s", diff)
			}
			if got.Recheck != tc.wantRecheck || got.ContentChanged != tc.wantChanged {
				t.Errorf("MergeEdit recheck, changed = %v, %v, want %v, %v", got.Recheck, got.ContentChanged, tc.wantRecheck, tc.wantChanged)
			}
			if diff := cmp.Diff(storedCase(), stored); diff != "" {
				t.Errorf("MergeEdit modified the stored case (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMergeEdit_ContentEditedAtIsStampedOnce(t *testing.T) {
	earlier := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	stored := storedCase()
	stored.ContentEditedAt = &earlier

	got, err := domain.MergeEdit(stored, domain.UpdateInput{Severity: set("low")}, editNow)
	if err != nil {
		t.Fatalf("MergeEdit: %v", err)
	}
	if got.Case.ContentEditedAt == nil || !got.Case.ContentEditedAt.Equal(earlier) {
		t.Errorf("ContentEditedAt = %v, want the earlier stamp %v", got.Case.ContentEditedAt, earlier)
	}
	if !got.ContentChanged {
		t.Errorf("ContentChanged = false, want true")
	}
}

func TestMergeEdit_EndBeforeStart(t *testing.T) {
	tests := []struct {
		name  string
		patch domain.UpdateInput
	}{
		{"only end below stored start", domain.UpdateInput{EndLine: ip(4)}},
		{"only start above stored end", domain.UpdateInput{StartLine: ip(9)}},
		{"both inverted", domain.UpdateInput{StartLine: ip(5), EndLine: ip(3)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.MergeEdit(storedCase(), tc.patch, editNow)
			re := ruleErr(t, err)
			if re.Kind != domain.RuleBadRequest || re.Code != "validation_error" || re.Message != "end_line must be >= start_line" {
				t.Errorf("MergeEdit error = %+v, want 400 validation_error \"end_line must be >= start_line\"", re)
			}
		})
	}
}

func TestMergeEdit_StoredInvertedLinesAreNotCheckedWithoutAnEditOfLines(t *testing.T) {
	stored := storedCase()
	stored.Expectation.StartLine, stored.Expectation.EndLine = 9, 3
	if _, err := domain.MergeEdit(stored, domain.UpdateInput{Name: sp("x")}, editNow); err != nil {
		t.Errorf("MergeEdit(name only on inverted stored lines) = %v, want nil", err)
	}
}
