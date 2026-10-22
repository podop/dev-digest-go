package domain_test

import (
	"testing"
	"time"

	"dev-digest-go/backend/internal/skills/domain"
)

func ptr[T any](v T) *T { return &v }

func TestPatch_EditMessage(t *testing.T) {
	cur := domain.Skill{Body: "b", Description: "d"}
	tests := []struct {
		name  string
		patch domain.Patch
		want  string
	}{
		{"both", domain.Patch{Body: ptr("b2"), Description: ptr("d2")}, "Edited body and description"},
		{"body", domain.Patch{Body: ptr("b2")}, "Edited body"},
		{"description", domain.Patch{Description: ptr("d2")}, "Edited description"},
		{"body changed, description resent", domain.Patch{Body: ptr("b2"), Description: ptr("d")}, "Edited body"},
		{"same texts", domain.Patch{Body: ptr("b"), Description: ptr("d")}, ""},
		{"other fields only", domain.Patch{Name: ptr("n"), Type: ptr(domain.TypeRubric), Enabled: ptr(false)}, ""},
		{"empty", domain.Patch{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.patch.EditMessage(cur); got != tc.want {
				t.Errorf("EditMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessages(t *testing.T) {
	ref := "https://h/x.md"
	empty := ""
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"manual", domain.CreatedMessage(domain.SourceManual, &ref), "Created"},
		{"imported with ref", domain.CreatedMessage(domain.SourceImportedURL, &ref), "Imported from https://h/x.md"},
		{"imported without ref", domain.CreatedMessage(domain.SourceImportedFile, nil), "Created"},
		{"imported with empty ref", domain.CreatedMessage(domain.SourceCommunity, &empty), "Created"},
		{"extracted", domain.ExtractedMessage("o/r"), "Extracted from conventions of o/r"},
		{"extracted ref", domain.ExtractedSourceRef("o/r"), "conventions:o/r"},
		{"restored", domain.RestoredMessage(3), "Restored from v3"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestInitialEnabled(t *testing.T) {
	tests := []struct {
		name      string
		source    domain.Source
		requested *bool
		want      bool
	}{
		{"manual default", domain.SourceManual, nil, true},
		{"manual true", domain.SourceManual, ptr(true), true},
		{"manual false", domain.SourceManual, ptr(false), false},
		{"imported file true", domain.SourceImportedFile, ptr(true), false},
		{"imported url default", domain.SourceImportedURL, nil, false},
		{"community true", domain.SourceCommunity, ptr(true), false},
		{"extracted true", domain.SourceExtracted, ptr(true), false},
	}
	for _, tc := range tests {
		if got := domain.InitialEnabled(tc.source, tc.requested); got != tc.want {
			t.Errorf("%s: InitialEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseCreatableSource(t *testing.T) {
	for _, s := range []string{"manual", "imported_file", "imported_url", "community"} {
		if got, ok := domain.ParseCreatableSource(s); !ok || string(got) != s {
			t.Errorf("ParseCreatableSource(%q) = %q, %v, want it accepted", s, got, ok)
		}
	}
	for _, s := range []string{"extracted", "Manual", "", "other"} {
		if _, ok := domain.ParseCreatableSource(s); ok {
			t.Errorf("ParseCreatableSource(%q) accepted, want refused", s)
		}
	}
}

func TestRate(t *testing.T) {
	if got := domain.Rate(1, 0); got != nil {
		t.Errorf("Rate(1, 0) = %v, want nil", *got)
	}
	if got := domain.Rate(0, 0); got != nil {
		t.Errorf("Rate(0, 0) = %v, want nil", *got)
	}
	if got := domain.Rate(1, 3); got == nil || *got != 1.0/3.0 {
		t.Errorf("Rate(1, 3) = %v, want 1/3 unrounded", got)
	}
	if got := domain.Rate(0, 4); got == nil || *got != 0 {
		t.Errorf("Rate(0, 4) = %v, want 0", got)
	}
}

func TestCounters_Rates(t *testing.T) {
	c := domain.Counters{RunsAttached: 4, RunsCited: 1, Accepted: 3, Dismissed: 1}
	if got := c.PullRate(); got == nil || *got != 0.25 {
		t.Errorf("PullRate() = %v, want 0.25", got)
	}
	if got := c.AcceptRate(); got == nil || *got != 0.75 {
		t.Errorf("AcceptRate() = %v, want 0.75", got)
	}
	if got := (domain.Counters{Accepted: 0, Dismissed: 0}).AcceptRate(); got != nil {
		t.Errorf("AcceptRate() with nothing acted on = %v, want nil", *got)
	}
}

func TestWindowStart(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if got, want := domain.WindowStart(now, 30), now.Add(-30*24*time.Hour); !got.Equal(want) {
		t.Errorf("WindowStart(now, 30) = %v, want %v", got, want)
	}
}

func TestUTF16Len(t *testing.T) {
	if got := domain.UTF16Len("a😀é"); got != 4 {
		t.Errorf("UTF16Len(%q) = %d, want 4", "a😀é", got)
	}
}
