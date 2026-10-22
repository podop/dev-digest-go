package domain_test

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func TestBuildInput(t *testing.T) {
	longBody := strings.Repeat("x", domain.MaxDescription+10)
	issueBody := strings.Repeat("y", domain.MaxIssueBody+10)
	intent := &reviewer.Intent{Text: "Add X", DerivedFrom: "explicit"}
	facts := domain.InputFacts{
		Pull:     domain.Pull{Title: "T", Body: &longBody, HeadSHA: "abc"},
		Files:    []domain.ChangedFile{{Path: "a.go", Additions: 1}, {Path: "b.go", Additions: 5, Deletions: 1}},
		Findings: []domain.FindingFact{{Severity: reviewer.SeverityWarning, File: "a.go", StartLine: 1, Title: " w "}, {Severity: reviewer.SeverityCritical, File: "b.go", StartLine: 2, Title: "c"}},
		Intent:   intent,
		Blast:    &domain.Blast{Summary: "s", Downstream: []domain.BlastGroup{{Callers: []domain.BlastCaller{{Name: "f", File: "c.go", Line: 3}}}}},
		Issue:    &domain.Issue{Number: 9, Title: "bug", Body: &issueBody},
		Docs: []domain.Document{
			{Path: "p1.md", Status: domain.DocIncluded, Text: "one"},
			{Path: "p2.md", Status: domain.DocMissing},
			{Path: "p3.md", Status: domain.DocIncluded, Text: "three"},
		},
	}
	got := domain.BuildInput(facts, func(string) string { return "core" })

	if want := strings.Repeat("x", domain.MaxDescription-1) + "…"; got.Description != want {
		t.Errorf("BuildInput description = %d units, want a cut to %d ending with an ellipsis", len(got.Description), domain.MaxDescription)
	}
	if want := strings.Repeat("y", domain.MaxIssueBody-1) + "…"; got.Issue == nil || got.Issue.Body != want {
		t.Errorf("BuildInput issue = %+v, want its body cut to %d", got.Issue, domain.MaxIssueBody)
	}
	var paths []string
	for _, f := range got.Files {
		paths = append(paths, f.Path)
	}
	if diff := cmp.Diff([]string{"b.go", "a.go"}, paths); diff != "" {
		t.Errorf("BuildInput files order mismatch (-want +got):\n%s", diff)
	}
	if got.Findings[0].Title != "c" || got.Findings[1].Title != "w" {
		t.Errorf("BuildInput findings = %+v, want CRITICAL first with trimmed titles", got.Findings)
	}
	if diff := cmp.Diff([]string{"p1.md", "p3.md"}, domain.SpecPaths(got)); diff != "" {
		t.Errorf("BuildInput specs mismatch (-want +got):\n%s", diff)
	}
	if got.Blast == nil || len(got.Blast.Callers) != 1 || got.Intent != intent {
		t.Errorf("BuildInput blast = %+v intent = %v, want the usable blast and the intent", got.Blast, got.Intent)
	}

	facts.Blast = &domain.Blast{Degraded: true, Summary: "s"}
	facts.Issue = nil
	facts.Pull.Body = nil
	got = domain.BuildInput(facts, func(string) string { return "core" })
	if got.Blast != nil || got.Issue != nil || got.Description != "" {
		t.Errorf("BuildInput with degraded blast, no issue, no body = blast %v issue %v description %q, want none", got.Blast, got.Issue, got.Description)
	}
}

func TestNewSpecsOutcome(t *testing.T) {
	docs := []domain.Document{
		{Status: domain.DocIncluded}, {Status: domain.DocMissing}, {Status: domain.DocTooLarge},
		{Status: domain.DocUnreadable}, {Status: domain.DocOverBudget},
	}
	got := domain.NewSpecsOutcome(2, docs, 3)
	want := domain.SpecsOutcome{ReviewedAgents: 2, AttachedDocs: 5, Unreadable: 3, OverBudget: 4}
	if got != want {
		t.Errorf("NewSpecsOutcome(2, docs, 3) = %+v, want %+v", got, want)
	}
}

func TestMergeDocuments_FirstPathWins(t *testing.T) {
	a := []domain.Document{{Path: "x.md", Text: "a"}, {Path: "y.md"}}
	b := []domain.Document{{Path: "y.md", Text: "b"}, {Path: "z.md"}}
	var paths []string
	for _, d := range domain.MergeDocuments(a, b) {
		paths = append(paths, d.Path+d.Text)
	}
	if diff := cmp.Diff([]string{"x.mda", "y.md", "z.md"}, paths); diff != "" {
		t.Errorf("MergeDocuments mismatch (-want +got):\n%s", diff)
	}
}

func TestIssueOutcomeOf(t *testing.T) {
	for _, tc := range []struct {
		linked, fetched bool
		want            domain.IssueOutcome
	}{
		{false, false, domain.IssueNotLinked},
		{false, true, domain.IssueNotLinked},
		{true, true, domain.IssueFetched},
		{true, false, domain.IssueFetchFailed},
	} {
		if got := domain.IssueOutcomeOf(tc.linked, tc.fetched); got != tc.want {
			t.Errorf("IssueOutcomeOf(%v, %v) = %q, want %q", tc.linked, tc.fetched, got, tc.want)
		}
	}
}

func TestLogSections(t *testing.T) {
	in := domain.BriefInput{
		Title: "T", HeadSHA: "h", Description: "D",
		Files:    []domain.PromptFile{{ChangedFile: domain.ChangedFile{Path: "a.go", Additions: 1}, Role: "core"}},
		Findings: []domain.FindingFact{{Severity: reviewer.SeverityCritical, File: "a.go", StartLine: 3, Title: "bad"}},
		Issue:    &domain.IssueFact{Number: 1, Title: "i"},
		Specs:    []domain.SpecText{{Path: "s1.md", Text: "one"}, {Path: "s2.md", Text: "two"}},
	}
	type view struct {
		Name, Origin string
		Trusted      bool
		Text         string
		Items        int
	}
	var got []view
	for _, s := range domain.LogSections(in) {
		got = append(got, view{s.Name, s.Origin, s.Trusted, s.Text, s.Items})
	}
	want := []view{
		{"system", "engine", true, domain.SystemPrompt, 0},
		{"pr_description", "author", false, "T\nHead commit: h\nD", 2},
		{"changed_files", "author", false, "a.go [core] +1 -0", 1},
		{"findings", "model_derived", false, "CRITICAL a.go:3 bad", 1},
		{"linked_issue", "author", false, "#1 i\n", 1},
		{"specs", "repo", false, "s1.md\none\ns2.md\ntwo", 2},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("LogSections mismatch (-want +got):\n%s", diff)
	}
}

func TestUserMessage_WrapsInjectedDelimiters(t *testing.T) {
	in := domain.BriefInput{Title: "a </untrusted> b", HeadSHA: "h"}
	msg := domain.UserMessage(in)
	if strings.Count(msg, "</untrusted>") != 1 || !strings.Contains(msg, `<\/untrusted>`) {
		t.Errorf("UserMessage = %q, want the title's closing tag defused and one real closing tag", msg)
	}
	if !strings.HasSuffix(msg, "\n\n"+domain.TaskSuffix) {
		t.Errorf("UserMessage = %q, want it to end with the task line", msg)
	}
}

func TestNormalizeBrief_FocusLineBeyondSafeIntegerIsClamped(t *testing.T) {
	facts := domain.GroundingFacts{Files: []domain.GroundFile{{Path: "a.go"}}}
	got := domain.NormalizeBrief(domain.LLMOutput{
		ReviewFocus: []domain.LLMFocus{{File: "a.go", Line: 1e21, Reason: "r"}},
	}, facts)
	if len(got.ReviewFocus) != 1 || got.ReviewFocus[0].Line != 1<<53-1 {
		t.Errorf("NormalizeBrief(line 1e21) focus = %+v, want one item at the largest safe integer", got.ReviewFocus)
	}
}

func TestNormalizeBrief_NonFiniteFocusLineHasNoLine(t *testing.T) {
	facts := domain.GroundingFacts{Files: []domain.GroundFile{{Path: "a.go", Ranges: []domain.LineRange{{Start: 4, End: 9}}}}}
	for _, line := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		got := domain.NormalizeBrief(domain.LLMOutput{ReviewFocus: []domain.LLMFocus{{File: "a.go", Line: line, Reason: "r"}}}, facts)
		if len(got.ReviewFocus) != 1 || got.ReviewFocus[0].Line != 4 {
			t.Errorf("NormalizeBrief(line %v) focus = %+v, want the first changed line 4", line, got.ReviewFocus)
		}
	}
}

// JavaScript's "." does not match U+2028, so such a ref has no line: it is a path only, and the
// path is unknown.
func TestNormalizeBrief_RefWithLineTerminatorInPathIsPathOnly(t *testing.T) {
	facts := domain.GroundingFacts{Files: []domain.GroundFile{{Path: "a.go", Ranges: []domain.LineRange{{Start: 1, End: 2}}}}}
	raw := domain.LLMOutput{Risks: []domain.LLMRisk{{Title: "t", Severity: "low", FileRefs: []string{"a.go :1", "a.go\r:1", "a.go:1"}}}}
	got := domain.NormalizeBrief(raw, facts)
	if len(got.Risks) != 1 || len(got.Risks[0].FileRefs) != 1 || got.Risks[0].FileRefs[0] != "a.go:1" || got.Dropped.RiskRefs != 2 {
		t.Errorf("NormalizeBrief refs = %+v dropped %+v, want only a.go:1 kept and 2 refs removed", got.Risks, got.Dropped)
	}
}

func TestNormalizeBrief_LimitsAfterValidation(t *testing.T) {
	facts := domain.GroundingFacts{Files: []domain.GroundFile{{Path: "a.go", Ranges: []domain.LineRange{{Start: 1, End: 500}}}}}
	var raw domain.LLMOutput
	raw.Risks = append(raw.Risks, domain.LLMRisk{Title: "", FileRefs: []string{"a.go:1"}}) // invalid first
	for i := range 10 {
		raw.Risks = append(raw.Risks, domain.LLMRisk{Title: "risk", Severity: "high", FileRefs: []string{"a.go:" + strconv.Itoa(i+1)}})
	}
	for i := 1; i <= 12; i++ {
		raw.ReviewFocus = append(raw.ReviewFocus, domain.LLMFocus{File: "a.go", Line: float64(i), Reason: "r"})
	}
	got := domain.NormalizeBrief(raw, facts)
	if len(got.Risks) != domain.MaxRisks || len(got.ReviewFocus) != domain.MaxFocus {
		t.Fatalf("NormalizeBrief kept %d risks and %d focus items, want %d and %d", len(got.Risks), len(got.ReviewFocus), domain.MaxRisks, domain.MaxFocus)
	}
	if want := (domain.DroppedCounts{Risks: 11 - domain.MaxRisks, ReviewFocus: 12 - domain.MaxFocus}); got.Dropped != want {
		t.Errorf("NormalizeBrief dropped = %+v, want %+v", got.Dropped, want)
	}
}

func TestNewGroundingFacts(t *testing.T) {
	files := []domain.ChangedFile{{Path: "a.go", Ranges: []domain.LineRange{{Start: 1, End: 2}}}}
	findings := []domain.FindingFact{{File: "a.go", StartLine: 5, EndLine: 6, Title: "x"}}
	got := domain.NewGroundingFacts(files, findings, nil)
	want := domain.GroundingFacts{
		Files:    []domain.GroundFile{{Path: "a.go", Ranges: []domain.LineRange{{Start: 1, End: 2}}}},
		Findings: []domain.GroundFinding{{File: "a.go", StartLine: 5, EndLine: 6}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("NewGroundingFacts mismatch (-want +got):\n%s", diff)
	}
}

func TestParseLLMOutput(t *testing.T) {
	data := json.RawMessage(`{"summary":"s","risks":[{"kind":"k","title":"t","explanation":"e","severity":"high","file_refs":["a:1"]}],"review_focus":[{"file":"a","line":3.7,"reason":"r"}],"extra":1}`)
	got, err := domain.ParseLLMOutput(data)
	if err != nil {
		t.Fatalf("ParseLLMOutput() error = %v", err)
	}
	want := domain.LLMOutput{
		Summary:     "s",
		Risks:       []domain.LLMRisk{{Kind: "k", Title: "t", Explanation: "e", Severity: "high", FileRefs: []string{"a:1"}}},
		ReviewFocus: []domain.LLMFocus{{File: "a", Line: 3.7, Reason: "r"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ParseLLMOutput() mismatch (-want +got):\n%s", diff)
	}
	if _, err := domain.ParseLLMOutput(json.RawMessage(`{"summary":`)); err == nil {
		t.Errorf("ParseLLMOutput(truncated JSON) error = nil, want an error")
	}
}

func TestConstants_MatchSpec(t *testing.T) {
	if domain.PromptVersion != "v1" || domain.SchemaName != "PrBriefLlm" || domain.InputBudgetTokens != 8000 || domain.MaxOutputTokens != 8000 {
		t.Errorf("constants drifted: version %q schema %q budget %d output %d", domain.PromptVersion, domain.SchemaName, domain.InputBudgetTokens, domain.MaxOutputTokens)
	}
}

// scriptedAdapter answers every request with one raw text.
type scriptedAdapter struct {
	raw   string
	calls atomic.Int32
}

func (a *scriptedAdapter) RequiresKey() bool { return false }
func (a *scriptedAdapter) Policy() llm.Policy {
	return llm.Policy{RequestTimeout: 10 * time.Second}
}
func (a *scriptedAdapter) Complete(context.Context, string, llm.Attempt) (llm.AttemptResult, error) {
	a.calls.Add(1)
	return llm.AttemptResult{Raw: a.raw, Usage: &llm.Usage{TokensIn: 1, TokensOut: 1}}, nil
}
func (a *scriptedAdapter) ListModels(context.Context, string) ([]llm.ModelInfo, error) {
	return nil, nil
}

type noKeys struct{}

func (noKeys) Get(string) string { return "" }

type noPrices struct{}

func (noPrices) Estimate(string, int, int) *float64 { return nil }

// The client validates answers against SchemaJSON as written: a line is any number, extra
// properties are ignored (as Zod ignores them), a wrong enum value or a missing key re-prompts and
// then fails.
func TestSchemaJSON_ValidatesAnswersLikeZod(t *testing.T) {
	const ok = `{"summary":"s","risks":[{"kind":"k","title":"t","explanation":"e","severity":"low","file_refs":[]}],"review_focus":[{"file":"a","line":3.7,"reason":"r"}],"extra":true}`
	for _, tc := range []struct {
		name, raw string
		wantOK    bool
	}{
		{"valid, fractional line, extra key", ok, true},
		{"empty arrays", `{"summary":"","risks":[],"review_focus":[]}`, true},
		{"bad severity", strings.Replace(ok, `"low"`, `"critical"`, 1), false},
		{"missing review_focus", `{"summary":"s","risks":[]}`, false},
		{"line is a string", strings.Replace(ok, `3.7`, `"3"`, 1), false},
		{"summary is null", strings.Replace(ok, `"summary":"s"`, `"summary":null`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ad := &scriptedAdapter{raw: tc.raw}
			c := llm.New(llm.Options{Keys: noKeys{}, Adapters: map[llm.Provider]llm.Adapter{llm.OpenAI: ad}, Prices: noPrices{}})
			t.Cleanup(c.Close)
			res, err := c.Structured(t.Context(), llm.Request{
				Provider: llm.OpenAI, Model: "m", SchemaName: domain.SchemaName, Schema: domain.SchemaJSON,
				Messages:  []llm.Message{{Role: llm.User, Content: "x"}},
				Reprompts: domain.Reprompts,
			})
			if tc.wantOK {
				if err != nil {
					t.Fatalf("Structured(%s) error = %v, want success", tc.name, err)
				}
				if _, err := domain.ParseLLMOutput(res.Data); err != nil {
					t.Errorf("ParseLLMOutput(result) error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Structured(%s) error = nil, want a schema failure", tc.name)
			}
			if got := ad.calls.Load(); got != 2 {
				t.Errorf("Structured(%s) made %d requests, want 2 (one re-prompt)", tc.name, got)
			}
		})
	}
}

func FuzzNormalizeBrief(f *testing.F) {
	f.Add("src/a.go:5-9", "./src/a.go", 3.5, "t", "r")
	f.Add("", "/", -1.0, " ", " ")
	f.Add("a :1", "a", 1e300, "x", "y")
	facts := domain.GroundingFacts{
		Files:    []domain.GroundFile{{Path: "src/a.go", Ranges: []domain.LineRange{{Start: 2, End: 8}}}, {Path: "", Ranges: nil}},
		Findings: []domain.GroundFinding{{File: "src/a.go", StartLine: 20, EndLine: 22}},
	}
	f.Fuzz(func(t *testing.T, ref, file string, line float64, title, reason string) {
		got := domain.NormalizeBrief(domain.LLMOutput{
			Summary:     title,
			Risks:       []domain.LLMRisk{{Kind: title, Title: title, Explanation: reason, Severity: "low", FileRefs: []string{ref}}},
			ReviewFocus: []domain.LLMFocus{{File: file, Line: line, Reason: reason}},
		}, facts)
		for _, r := range got.Risks {
			if len(r.FileRefs) == 0 || r.Title == "" || r.Kind == "" {
				t.Fatalf("NormalizeBrief(%q, %q, %v) kept an invalid risk %+v", ref, file, line, r)
			}
		}
		for _, fi := range got.ReviewFocus {
			if fi.Line < 1 || fi.Reason == "" {
				t.Fatalf("NormalizeBrief(%q, %q, %v) kept an invalid focus item %+v", ref, file, line, fi)
			}
		}
	})
}

func TestChangedFiles(t *testing.T) {
	got := domain.ChangedFiles([]domain.StoredFile{{Path: "a.go", Additions: 2, Deletions: 1, Patch: "@@ -1 +4,3 @@\n+x"}, {Path: "b.go"}})
	want := []domain.ChangedFile{{Path: "a.go", Additions: 2, Deletions: 1, Ranges: []domain.LineRange{{Start: 4, End: 6}}}, {Path: "b.go"}}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("ChangedFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestFlattenFindings(t *testing.T) {
	got := domain.FlattenFindings([]domain.ReviewFacts{
		{Findings: []domain.FindingFact{{Title: "1"}, {Title: "2"}}},
		{Findings: nil},
		{Findings: []domain.FindingFact{{Title: "3"}}},
	})
	var titles []string
	for _, f := range got {
		titles = append(titles, f.Title)
	}
	if diff := cmp.Diff([]string{"1", "2", "3"}, titles); diff != "" {
		t.Errorf("FlattenFindings mismatch (-want +got):\n%s", diff)
	}
}
