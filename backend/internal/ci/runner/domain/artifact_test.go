package domain_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func TestBuildArtifact_JSON(t *testing.T) {
	findings := findingsOf(reviewer.SeverityCritical, reviewer.SeverityWarning, reviewer.SeverityWarning,
		reviewer.SeveritySuggestion, reviewer.Severity("INFO"))
	tests := []struct {
		name string
		in   domain.ArtifactInput
		want string
	}{
		{
			name: "full",
			in: domain.ArtifactInput{
				Findings: findings, CostUSD: ptr(0.0123), DurationMS: 4821, Agent: "Code Review", PRNumber: 12,
				CommitSHA: ptr("abc123"), Repository: "acme/widgets",
				RepositoryID: ptr(int64(99)), RunID: ptr(int64(1234567890)), RunAttempt: ptr(int64(2)),
				Model: "openai/gpt-5", ManifestVersion: ptr(int64(3)), Skills: []string{"a-skill", "b-skill"}, Blockers: 1,
			},
			want: `{
  "findings_count": 5,
  "critical": 1,
  "warning": 2,
  "suggestion": 2,
  "cost_usd": 0.0123,
  "duration_ms": 4821,
  "agent": "Code Review",
  "version": "2",
  "pr_number": 12,
  "commit_sha": "abc123",
  "repository": "acme/widgets",
  "repository_id": 99,
  "run_id": 1234567890,
  "run_attempt": 2,
  "model": "openai/gpt-5",
  "manifest_version": 3,
  "skills": [
    "a-skill",
    "b-skill"
  ],
  "blockers": 1
}
`,
		},
		{
			name: "nulls and no skills",
			in:   domain.ArtifactInput{Agent: "a", PRNumber: 1, Repository: "o/r", Model: "m"},
			want: `{
  "findings_count": 0,
  "critical": 0,
  "warning": 0,
  "suggestion": 0,
  "cost_usd": null,
  "duration_ms": 0,
  "agent": "a",
  "version": "2",
  "pr_number": 1,
  "commit_sha": null,
  "repository": "o/r",
  "repository_id": null,
  "run_id": null,
  "run_attempt": null,
  "model": "m",
  "manifest_version": null,
  "skills": [],
  "blockers": 0
}
`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := domain.BuildArtifact(tc.in)
			if err != nil {
				t.Fatalf("BuildArtifact() error = %v", err)
			}
			if got := a.JSON(); got != tc.want {
				t.Errorf("JSON() =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// TestArtifactJSON_JavaScriptText pins the places where encoding/json and JSON.stringify differ
// (values checked against node 22).
func TestArtifactJSON_JavaScriptText(t *testing.T) {
	tenth, fifth := 0.1, 0.2 // runtime values: constant arithmetic would be exact, not 0.30000000000000004
	tests := []struct {
		name string
		cost float64
		text string // agent name
		want string // `"cost_usd": …` and `"agent": …` lines
	}{
		{"html is not escaped", 1, "a<b>&c", `"cost_usd": 1,` + "\n" + `  "duration_ms": 0,` + "\n" + `  "agent": "a<b>&c",`},
		{"line separators are not escaped", 1, "x y z", `"agent": "x` + " y " + `z",`},
		{"control characters", 1, "\x01\b\f\n\r\t\x1f\x7f", `"agent": "\u0001\b\f\n\r\t\u001f` + "\x7f" + `",`},
		{"quote and backslash", 1, `"\`, `"agent": "\"\\",`},
		{"invalid UTF-8", 1, "a\xffb", `"agent": "a` + "�" + `b",`},
		{"small cost uses an exponent", 1e-7, "a", `"cost_usd": 1e-7,`},
		{"cost at 1e-6 is plain", 0.000001, "a", `"cost_usd": 0.000001,`},
		{"large cost uses an exponent", 1.5e21, "a", `"cost_usd": 1.5e+21,`},
		{"float noise is shortest round trip", tenth + fifth, "a", `"cost_usd": 0.30000000000000004,`},
		{"negative zero", math.Copysign(0, -1), "a", `"cost_usd": 0,`},
		{"infinity is null like JSON.stringify", math.Inf(1), "a", `"cost_usd": null,`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := domain.BuildArtifact(domain.ArtifactInput{CostUSD: ptr(tc.cost), Agent: tc.text})
			if err != nil {
				t.Fatalf("BuildArtifact() error = %v", err)
			}
			if got := a.JSON(); !strings.Contains(got, tc.want) {
				t.Errorf("JSON() = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestArtifactJSON_IsValidAndRoundTrips checks the text with a real JSON decoder, the way the
// ingest reads it: valid, keys in the CiResultArtifact order, unchanged strings.
func TestArtifactJSON_IsValidAndRoundTrips(t *testing.T) {
	a, err := domain.BuildArtifact(domain.ArtifactInput{
		CostUSD: ptr(2.5), Agent: "n \"\\\x00é😀", Repository: "o/r", Model: "m", CommitSHA: ptr("\n"),
		Skills: []string{"s\t"},
	})
	if err != nil {
		t.Fatalf("BuildArtifact() error = %v", err)
	}
	text := a.JSON()
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("JSON() is not valid JSON: %v\n%s", err, text)
	}
	if got["agent"] != "n \"\\\x00é😀" || got["commit_sha"] != "\n" || got["cost_usd"] != 2.5 {
		t.Errorf("decoded artifact = %v, want the original values back", got)
	}
	if diff := cmp.Diff([]any{"s\t"}, got["skills"]); diff != "" {
		t.Errorf("decoded skills mismatch (-want +got):\n%s", diff)
	}
	dec := json.NewDecoder(strings.NewReader(text))
	if _, err := dec.Token(); err != nil {
		t.Fatalf("Token() = %v", err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatalf("Token() = %v", err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("Decode() = %v", err)
		}
	}
	wantKeys := []string{"findings_count", "critical", "warning", "suggestion", "cost_usd", "duration_ms", "agent", "version",
		"pr_number", "commit_sha", "repository", "repository_id", "run_id", "run_attempt", "model", "manifest_version", "skills", "blockers"}
	if diff := cmp.Diff(wantKeys, keys); diff != "" {
		t.Errorf("JSON() key order mismatch (-want +got):\n%s", diff)
	}
	if !strings.HasSuffix(text, "}\n") {
		t.Errorf("JSON() = %q, want it to end with a line break", text[max(0, len(text)-5):])
	}
}

func TestBuildArtifact_NaNCost(t *testing.T) {
	_, err := domain.BuildArtifact(domain.ArtifactInput{CostUSD: ptr(math.NaN())})
	if err == nil || !strings.HasPrefix(err.Error(), "Internal error: built result artifact failed CiResultArtifact validation") {
		t.Errorf("BuildArtifact(NaN cost) error = %v, want the internal validation error", err)
	}
}

func TestIntOrNull(t *testing.T) {
	tests := []struct {
		in   string
		want *int64
	}{
		{"", nil},
		{"0", ptr(int64(0))},
		{"42", ptr(int64(42))},
		{"0042", ptr(int64(42))},
		{"9007199254740991", ptr(int64(9007199254740991))},
		{"9007199254740992", nil},
		{"99999999999999999999999", nil},
		{"-1", nil},
		{"+1", nil},
		{"1.0", nil},
		{"1e3", nil},
		{" 1", nil},
		{"1 ", nil},
		{"1\n", nil},
		{"0x10", nil},
		{"１２", nil}, // full-width digits: JavaScript \d is ASCII only
	}
	for _, tc := range tests {
		got := domain.IntOrNull(tc.in)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("IntOrNull(%q) mismatch (-want +got):\n%s", tc.in, diff)
		}
	}
}
