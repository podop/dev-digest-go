package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/plugins/domain"
)

func TestUniqueNames(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, []string{}},
		{"all different", []string{"a", "b"}, []string{"a", "b"}},
		{"creation order decides", []string{"Reviewer", "Reviewer", "Reviewer"}, []string{"Reviewer", "Reviewer (2)", "Reviewer (3)"}},
		{"a later real name keeps its own", []string{"A", "A", "A (2)"}, []string{"A", "A (2)", "A (2) (2)"}},
		{"the suffix skips names already taken", []string{"A (2)", "A", "A"}, []string{"A (2)", "A", "A (3)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := append([]string(nil), tc.in...)
			got := domain.UniqueNames(tc.in)
			if diff := cmp.Diff(tc.want, got, cmpEmpty); diff != "" {
				t.Errorf("UniqueNames(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
			if diff := cmp.Diff(in, tc.in, cmpEmpty); diff != "" {
				t.Errorf("UniqueNames changed its input:\n%s", diff)
			}
		})
	}
}

func TestDefaultName(t *testing.T) {
	tests := []struct {
		name, workspace, want string
	}{
		{"plain", "acme", "devdigest-acme"},
		{"punctuation and case", "Acme · Payments!", "devdigest-acme-payments"},
		{"runs of separators", "a   b---c", "devdigest-a-b-c"},
		{"digits", "Team 42", "devdigest-team-42"},
		{"empty", "", "devdigest-workspace"},
		{"only symbols", "···", "devdigest-workspace"},
		{"non ASCII letters are separators", "Über uns", "devdigest-ber-uns"},
		{"default workspace name", "default", "devdigest-default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.DefaultName(tc.workspace); got != tc.want {
				t.Errorf("DefaultName(%q) = %q, want %q", tc.workspace, got, tc.want)
			}
		})
	}
}

func TestDefaultName_NeverExceedsTheLimit(t *testing.T) {
	for _, ws := range []string{
		strings.Repeat("a", 80),
		strings.Repeat("a", 54),
		strings.Repeat("a", 55),
		strings.Repeat("a", 53) + "-" + strings.Repeat("b", 20),
		strings.Repeat("a", 53) + " " + strings.Repeat("b", 20),
	} {
		got := domain.DefaultName(ws)
		if len(got) > domain.ExportNameMax || !domain.ValidExportName(got) {
			t.Errorf("DefaultName(%q) = %q (%d), want a valid name of at most %d", ws, got, len(got), domain.ExportNameMax)
		}
	}
	if got := domain.DefaultName(strings.Repeat("a", 54)); len(got) != 64 {
		t.Errorf("a 54 character slug: len = %d, want exactly 64", len(got))
	}
	if got := domain.DefaultName(strings.Repeat("a", 53) + " " + strings.Repeat("b", 20)); strings.HasSuffix(got, "-") {
		t.Errorf("DefaultName cut = %q, want no trailing dash", got)
	}
}

func TestValidExportName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"a", true}, {"devdigest-acme", true}, {"a1-b2-c3", true}, {strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false}, {"", false}, {"-a", false}, {"a-", false}, {"a--b", false},
		{"A", false}, {"a b", false}, {"a_b", false}, {"a.b", false}, {"é", false}, {"a\n", false},
	}
	for _, tc := range tests {
		if got := domain.ValidExportName(tc.name); got != tc.want {
			t.Errorf("ValidExportName(%q) = %v, want %v", tc.name, got, tc.want)
		}
		if err := domain.CheckExportName(tc.name); (err == nil) != tc.want || (err != nil && !errors.Is(err, domain.ErrInvalidExportName)) {
			t.Errorf("CheckExportName(%q) = %v, want ok=%v", tc.name, err, tc.want)
		}
	}
}

func TestExportFileName(t *testing.T) {
	now := time.Date(2026, 10, 26, 23, 59, 59, 0, time.FixedZone("x", -5*3600)) // 2026-10-27 04:59 UTC
	tests := []struct{ name, want string }{
		{"devdigest-acme", "devdigest-acme-2026-10-27.zip"},
		{"a.b_c-d", "a.b_c-d-2026-10-27.zip"},
		{"a b\"c\r\nd;e/f", "a-b-c--d-e-f-2026-10-27.zip"},
		{"é", "--2026-10-27.zip"},
	}
	for _, tc := range tests {
		if got := domain.ExportFileName(tc.name, now); got != tc.want {
			t.Errorf("ExportFileName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		raw, want string
		wantErr   bool
	}{
		{"acme", "acme", false},
		{"  acme  ", "acme", false},
		{"acme · payments", "acme · payments", false},
		{strings.Repeat("a", 80), strings.Repeat("a", 80), false},
		{strings.Repeat("a", 81), "", true},
		{strings.Repeat("é", 80), strings.Repeat("é", 80), false},
		{"", "", true},
		{" \t ", "", true},
		{"a\tb", "", true},
		{"a\nb", "", true},
		{"a\x00b", "", true},
		{"a\x7fb", "", true},
		{"a\xffb", "", true},
	}
	for _, tc := range tests {
		got, err := domain.NormalizeName(tc.raw)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q, err %v", tc.raw, got, err, tc.want, tc.wantErr)
		}
		if err != nil && !errors.Is(err, domain.ErrInvalidName) {
			t.Errorf("NormalizeName(%q) error = %v, want ErrInvalidName", tc.raw, err)
		}
	}
}

func TestCheckFileName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"acme.zip", false},
		{"my plugin (1).zip", false},
		{"données.json", false},
		{"a", false},
		{strings.Repeat("a", 255), false},
		{strings.Repeat("é", 255), false},
		{strings.Repeat("a", 256), true},
		{"", true},
		{" ", false},
		{"a\x00b", true},
		{"a\nb", true},
		{"a\tb", true},
		{"a\x7fb", true},
		{"a\u0085b", true},
		{"a\xffb", true},
	}
	for _, tc := range tests {
		err := domain.CheckFileName(tc.name)
		if (err != nil) != tc.wantErr {
			t.Errorf("CheckFileName(%q) = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		if err != nil && !errors.Is(err, domain.ErrInvalidFileName) {
			t.Errorf("CheckFileName(%q) error = %v, want ErrInvalidFileName", tc.name, err)
		}
	}
}

func TestConfirmRemoval(t *testing.T) {
	tests := []struct {
		name, confirm string
		wantErr       bool
	}{
		{"acme", "acme", false},
		{"acme", "Acme", true},
		{"acme", "acme ", true},
		{"acme", "", true},
		{"acme · x", "acme · x", false},
	}
	for _, tc := range tests {
		err := domain.ConfirmRemoval(tc.name, tc.confirm)
		if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, domain.ErrConfirmationMismatch)) {
			t.Errorf("ConfirmRemoval(%q, %q) = %v, wantErr %v", tc.name, tc.confirm, err, tc.wantErr)
		}
	}
}

func TestUTF16Len(t *testing.T) {
	for s, want := range map[string]int{"": 0, "abc": 3, "é": 1, "😀": 2, "a😀b": 4, "日本語": 3} {
		if got := domain.UTF16Len(s); got != want {
			t.Errorf("UTF16Len(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestEnumListsAreCopies(t *testing.T) {
	got := domain.SkillTypes()
	got[0] = "changed"
	if domain.SkillTypes()[0] != "rubric" {
		t.Error("SkillTypes() returns shared state")
	}
	for name, list := range map[string][]string{
		"providers": domain.AgentProviders(), "strategies": domain.AgentStrategies(), "ci_fail_on": domain.AgentCIFailOns(),
		"skill sources": domain.SkillSources(), "owner kinds": domain.CaseOwnerKinds(),
		"scopes": domain.LearningScopes(), "learning kinds": domain.LearningKinds(),
	} {
		if len(list) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}
