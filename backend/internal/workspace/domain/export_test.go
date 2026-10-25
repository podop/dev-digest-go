package domain_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/workspace/domain"
)

func TestExportFileName(t *testing.T) {
	now := time.Date(2026, 10, 23, 23, 59, 59, 0, time.UTC)
	tests := []struct {
		name string
		in   string
		now  time.Time
		want string
	}{
		{name: "AC15", in: "acme · payments", now: now, want: "devdigest-workspace-acme-payments-2026-10-23.json"},
		{name: "runs of other characters collapse", in: "  --A!!B__C  ", now: now, want: "devdigest-workspace-a-b-c-2026-10-23.json"},
		{name: "digits kept", in: "Team 42", now: now, want: "devdigest-workspace-team-42-2026-10-23.json"},
		{name: "nothing left", in: "···", now: now, want: "devdigest-workspace-workspace-2026-10-23.json"},
		{name: "non-ASCII letters are separators", in: "Ünï", now: now, want: "devdigest-workspace-n-2026-10-23.json"},
		{name: "cut to 40", in: strings.Repeat("a", 60), now: now, want: "devdigest-workspace-" + strings.Repeat("a", 40) + "-2026-10-23.json"},
		{name: "cut never leaves a trailing dash", in: strings.Repeat("a", 39) + " b", now: now, want: "devdigest-workspace-" + strings.Repeat("a", 39) + "-2026-10-23.json"},
		{name: "date is UTC", in: "x", now: time.Date(2026, 10, 23, 23, 30, 0, 0, time.FixedZone("UTC-5", -5*3600)), want: "devdigest-workspace-x-2026-10-24.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ExportFileName(tc.in, tc.now); got != tc.want {
				t.Errorf("ExportFileName(%q, %v) = %q, want %q", tc.in, tc.now, got, tc.want)
			}
		})
	}
}

func TestUniqueNames(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "empty", in: nil, want: []string{}},
		{name: "distinct", in: []string{"A", "B"}, want: []string{"A", "B"}},
		{name: "EC13", in: []string{"Reviewer", "Reviewer"}, want: []string{"Reviewer", "Reviewer (2)"}},
		{name: "three in creation order", in: []string{"R", "R", "R"}, want: []string{"R", "R (2)", "R (3)"}},
		{name: "suffix collides with a later name", in: []string{"R", "R", "R (2)"}, want: []string{"R", "R (2)", "R (2) (2)"}},
		{name: "suffix collides with an earlier name", in: []string{"R (2)", "R", "R"}, want: []string{"R (2)", "R", "R (3)"}},
		{name: "case is significant", in: []string{"R", "r"}, want: []string{"R", "r"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := slices.Clone(tc.in)
			got := domain.UniqueNames(tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("UniqueNames(%q) mismatch (-want +got):\n%s", in, diff)
			}
			if diff := cmp.Diff(in, tc.in); diff != "" {
				t.Errorf("UniqueNames changed its input (-before +after):\n%s", diff)
			}
		})
	}
}

func TestExportableKeys(t *testing.T) {
	want := []string{
		"automatic_reviews", "default_model", "density", "feature_models", "monthly_spend_cap_usd",
		"polling_interval_min", "retention_days", "sync_to_folder", "theme",
	}
	got := domain.ExportableKeys()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ExportableKeys() mismatch (-want +got):\n%s", diff)
	}
	got[0] = "tampered"
	if domain.ExportableKeys()[0] != "automatic_reviews" {
		t.Error("ExportableKeys() returned a shared slice")
	}
}

func raw(m map[string]string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = json.RawMessage(v)
	}
	return out
}

func TestExportSettings(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{name: "nil stays an empty map", in: nil, want: map[string]string{}},
		{
			name: "allow-list only, values as stored",
			in: map[string]string{
				"theme": `"dark"`, "density": `"compact"`, "polling_interval_min": `15`, "sync_to_folder": `true`,
				"feature_models": `{"brief":"gpt-4o"}`, "retention_days": `null`, "default_model": `"gpt-4o"`,
				"monthly_spend_cap_usd": `12.5`,
			},
			want: map[string]string{
				"theme": `"dark"`, "density": `"compact"`, "polling_interval_min": `15`, "sync_to_folder": `true`,
				"feature_models": `{"brief":"gpt-4o"}`, "retention_days": `null`, "default_model": `"gpt-4o"`,
				"monthly_spend_cap_usd": `12.5`,
			},
		},
		{
			name: "unknown and internal keys are dropped (FR14)",
			in: map[string]string{
				"theme": `"light"`, "workspace_name": `"acme"`, "api_token": `"ghp_secretsecretsecret"`, "x": `1`,
			},
			want: map[string]string{"theme": `"light"`},
		},
		{
			name: "automatic_reviews loses agent_ids and enabled_at (D26)",
			in: map[string]string{
				"automatic_reviews": `{"enabled":true,"agent_ids":["6f1c0d2e-0000-4000-8000-000000000001"],"enabled_at":"2026-10-25T10:00:00.000Z","mode":"all"}`,
			},
			want: map[string]string{"automatic_reviews": `{"enabled":true,"mode":"all"}`},
		},
		{
			name: "automatic_reviews with only dropped keys becomes an empty object",
			in:   map[string]string{"automatic_reviews": `{"agent_ids":[],"enabled_at":null}`},
			want: map[string]string{"automatic_reviews": `{}`},
		},
		{name: "automatic_reviews null passes", in: map[string]string{"automatic_reviews": `null`}, want: map[string]string{"automatic_reviews": `null`}},
		{name: "automatic_reviews string is dropped", in: map[string]string{"automatic_reviews": `"6f1c0d2e"`}, want: map[string]string{}},
		{name: "automatic_reviews array is dropped", in: map[string]string{"automatic_reviews": `["a"]`}, want: map[string]string{}},
		{name: "automatic_reviews garbage is dropped", in: map[string]string{"automatic_reviews": `{`}, want: map[string]string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ExportSettings(raw(tc.in))
			if got == nil {
				t.Fatal("ExportSettings() = nil map, want non-nil")
			}
			gotStrings := make(map[string]string, len(got))
			for k, v := range got {
				gotStrings[k] = string(v)
			}
			if diff := cmp.Diff(tc.want, gotStrings); diff != "" {
				t.Errorf("ExportSettings(%v) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestExportSettingsDoesNotChangeInput(t *testing.T) {
	in := raw(map[string]string{"automatic_reviews": `{"agent_ids":["a"],"enabled":true}`, "secret": `"x"`})
	domain.ExportSettings(in)
	if got := string(in["automatic_reviews"]); got != `{"agent_ids":["a"],"enabled":true}` {
		t.Errorf("input automatic_reviews = %s after ExportSettings, want unchanged", got)
	}
	if _, ok := in["secret"]; !ok {
		t.Error("ExportSettings removed a key from its input")
	}
}
