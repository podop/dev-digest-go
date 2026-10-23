package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
)

func ptr[T any](v T) *T { return &v }

func TestParseResultArtifact_Valid(t *testing.T) {
	full := `{"findings_count":3,"critical":1,"warning":1,"suggestion":1,"cost_usd":0.0123,"duration_ms":4500,
	"agent":"security-reviewer","version":"2","pr_number":17,"commit_sha":"abc","repository":"o/r","repository_id":99,
	"run_id":5,"run_attempt":2,"model":"m","manifest_version":4,"skills":["a","b"],"blockers":1,"unknown":{"x":1}}`
	tests := []struct {
		name string
		in   string
		want domain.CiResultArtifact
	}{
		{"full", full, domain.CiResultArtifact{
			FindingsCount: 3, Critical: ptr(1), Warning: ptr(1), Suggestion: ptr(1), CostUSD: ptr(0.0123),
			DurationMS: ptr(4500), Agent: "security-reviewer", Version: ptr("2"), PRNumber: ptr(17),
			CommitSHA: ptr("abc"), Repository: ptr("o/r"), RepositoryID: ptr(int64(99)), RunID: ptr(int64(5)),
			RunAttempt: ptr(2), Model: ptr("m"), ManifestVersion: ptr(4), Skills: []string{"a", "b"}, Blockers: ptr(1),
		}},
		{"minimal with null cost", `{"findings_count":0,"cost_usd":null,"agent":"a"}`,
			domain.CiResultArtifact{Agent: "a"}},
		{"explicit nulls", `{"findings_count":0,"cost_usd":1,"agent":"a","critical":null,"commit_sha":null,"skills":null,"pr_number":null}`,
			domain.CiResultArtifact{Agent: "a", CostUSD: ptr(1.0)}},
		{"integer-valued floats", `{"findings_count":2.0,"cost_usd":1e-2,"agent":"a","pr_number":1E1,"duration_ms":-0}`,
			domain.CiResultArtifact{FindingsCount: 2, Agent: "a", CostUSD: ptr(0.01), PRNumber: ptr(10), DurationMS: ptr(0)}},
		{"byte order mark dropped", "\xef\xbb\xbf" + `{"findings_count":1,"cost_usd":null,"agent":"a"}`,
			domain.CiResultArtifact{FindingsCount: 1, Agent: "a"}},
		{"leading whitespace", " \n\t" + `{"findings_count":1,"cost_usd":null,"agent":"a"}  `,
			domain.CiResultArtifact{FindingsCount: 1, Agent: "a"}},
		{"duplicate keys last wins", `{"findings_count":1,"findings_count":2,"cost_usd":null,"agent":"a"}`,
			domain.CiResultArtifact{FindingsCount: 2, Agent: "a"}},
		{"escaped separators", `{"findings_count":1,"cost_usd":null,"agent":"a b"}`,
			domain.CiResultArtifact{FindingsCount: 1, Agent: "a b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ParseResultArtifact([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseResultArtifact() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseResultArtifact() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseResultArtifact_Invalid(t *testing.T) {
	const base = `"findings_count":1,"cost_usd":null,"agent":"a"`
	tests := []struct{ name, in string }{
		{"empty", ``},
		{"not json", `{`},
		{"trailing data", `{` + base + `} x`},
		{"array", `[]`},
		{"null", `null`},
		{"string", `"x"`},
		{"invalid utf-8", "{" + base + ",\"model\":\"\xff\"}"},
		{"missing findings_count", `{"cost_usd":null,"agent":"a"}`},
		{"null findings_count", `{"findings_count":null,"cost_usd":null,"agent":"a"}`},
		{"missing cost_usd", `{"findings_count":1,"agent":"a"}`},
		{"missing agent", `{"findings_count":1,"cost_usd":null}`},
		{"null agent", `{"findings_count":1,"cost_usd":null,"agent":null}`},
		{"fractional count", `{"findings_count":1.5,"cost_usd":null,"agent":"a"}`},
		{"string count", `{"findings_count":"1","cost_usd":null,"agent":"a"}`},
		{"string cost", `{"findings_count":1,"cost_usd":"1","agent":"a"}`},
		{"bool pr", `{` + base + `,"pr_number":true}`},
		{"number agent", `{"findings_count":1,"cost_usd":null,"agent":1}`},
		{"unsafe integer", `{"findings_count":9007199254740993,"cost_usd":null,"agent":"a"}`},
		{"overflowing number", `{"findings_count":1e999,"cost_usd":null,"agent":"a"}`},
		{"overflowing cost", `{"findings_count":1,"cost_usd":1e999,"agent":"a"}`},
		{"skills not array", `{` + base + `,"skills":"a"}`},
		{"skills with number", `{` + base + `,"skills":["a",1]}`},
		{"repository_id fractional", `{` + base + `,"repository_id":1.5}`},
		{"model object", `{` + base + `,"model":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.ParseResultArtifact([]byte(tc.in)); !errors.Is(err, domain.ErrArtifactInvalid) {
				t.Errorf("ParseResultArtifact(%q) error = %v, want ErrArtifactInvalid", tc.in, err)
			}
		})
	}
}

func TestParseResultArtifact_SafeIntegerBoundary(t *testing.T) {
	in := `{"findings_count":9007199254740991,"cost_usd":null,"agent":"a"}`
	got, err := domain.ParseResultArtifact([]byte(in))
	if err != nil || got.FindingsCount != 9007199254740991 {
		t.Errorf("ParseResultArtifact(max safe) = %d, %v; want 9007199254740991, nil", got.FindingsCount, err)
	}
}

func TestVerifyArtifact(t *testing.T) {
	const wf = ".github/workflows/devdigest-review-sec.yml"
	run := domain.RunFacts{HeadSHA: "sha1", RepositoryID: 42, Path: wf}
	good := domain.CiResultArtifact{CommitSHA: ptr("sha1"), RepositoryID: ptr(int64(42))}
	tests := []struct {
		name string
		a    domain.CiResultArtifact
		run  domain.RunFacts
		want bool
	}{
		{"match", good, run, true},
		{"path with ref suffix", good, domain.RunFacts{HeadSHA: "sha1", RepositoryID: 42, Path: wf + "@refs/pull/1/merge"}, true},
		{"other commit", domain.CiResultArtifact{CommitSHA: ptr("sha2"), RepositoryID: ptr(int64(42))}, run, false},
		{"other repository", domain.CiResultArtifact{CommitSHA: ptr("sha1"), RepositoryID: ptr(int64(43))}, run, false},
		{"commit_sha absent", domain.CiResultArtifact{RepositoryID: ptr(int64(42))}, run, false},
		{"repository_id absent", domain.CiResultArtifact{CommitSHA: ptr("sha1")}, run, false},
		{"other workflow", good, domain.RunFacts{HeadSHA: "sha1", RepositoryID: 42, Path: ".github/workflows/other.yml"}, false},
		{"other workflow with suffix", good, domain.RunFacts{HeadSHA: "sha1", RepositoryID: 42, Path: ".github/workflows/other.yml@" + wf}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.VerifyArtifact(tc.a, tc.run, wf); got != tc.want {
				t.Errorf("VerifyArtifact() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFactsOf(t *testing.T) {
	got := domain.FactsOf(domain.WorkflowRun{HeadSHA: "s", RepositoryID: 7, Path: "p", ID: 1})
	if want := (domain.RunFacts{HeadSHA: "s", RepositoryID: 7, Path: "p"}); got != want {
		t.Errorf("FactsOf() = %+v, want %+v", got, want)
	}
}

func TestParseResultArtifact_LargeButValid(t *testing.T) {
	skills := `"` + strings.Repeat("s", 100) + `"`
	in := `{"findings_count":1,"cost_usd":null,"agent":"a","skills":[` + strings.Repeat(skills+",", 50) + skills + `]}`
	got, err := domain.ParseResultArtifact([]byte(in))
	if err != nil || len(got.Skills) != 51 {
		t.Errorf("ParseResultArtifact(51 skills) = %d skills, %v; want 51, nil", len(got.Skills), err)
	}
}
