package conventionshttp

import (
	"strings"
	"testing"

	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/domain"
)

func TestParseUpdate_ReturnsTheValidatedPatch(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		status   string
		rule     string
		category string
	}{
		{"status", `{"status":"accepted"}`, "accepted", "", ""},
		{"trimmed rule", "{\"rule\":\"\\t spaced \\u00a0\"}", "", "spaced", ""},
		{"rule of 500 emoji units", `{"rule":"` + strings.Repeat("a", 500) + `"}`, "", strings.Repeat("a", 500), ""},
		{"everything", `{"status":"rejected","rule":"r","category":"api"}`, "rejected", "r", "api"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, issues := parseUpdate([]byte(tc.body))
			if issues != nil {
				t.Fatalf("parseUpdate(%s) issues = %+v, want none", tc.body, issues)
			}
			if got := patchStrings(p); got != [3]string{tc.status, tc.rule, tc.category} {
				t.Errorf("parseUpdate(%s) = %v, want %v", tc.body, got, [3]string{tc.status, tc.rule, tc.category})
			}
		})
	}
}

func patchStrings(p app.Patch) (out [3]string) {
	if p.Status != nil {
		out[0] = string(*p.Status)
	}
	if p.Rule != nil {
		out[1] = *p.Rule
	}
	if p.Category != nil {
		out[2] = *p.Category
	}
	return out
}

func TestParseUpdate_RuleLengthCountsUTF16Units(t *testing.T) {
	// 251 emoji are 502 UTF-16 code units: too long for Zod's max(500), though only 251 runes.
	_, issues := parseUpdate([]byte(`{"rule":"` + strings.Repeat("😀", 251) + `"}`))
	if len(issues) != 1 || issues[0].Code != "too_big" {
		t.Errorf("parseUpdate(251 emoji) issues = %+v, want one too_big", issues)
	}
	if _, issues := parseUpdate([]byte(`{"rule":"` + strings.Repeat("😀", 250) + `"}`)); issues != nil {
		t.Errorf("parseUpdate(250 emoji) issues = %+v, want none", issues)
	}
}

func TestStatusNamesFollowTheDomain(t *testing.T) {
	if len(statusNames) != len(domain.Statuses()) {
		t.Errorf("statusNames = %v, want %v", statusNames, domain.Statuses())
	}
}
