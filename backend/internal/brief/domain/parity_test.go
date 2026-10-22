package domain_test

// Differential tests against the Node implementation (spec AC5044): testdata/node_cases.json holds
// inputs and the outputs of dev-digest d1c9093 brief/domain/*.ts for them, produced by
// testdata/gen.mts. The role of a path (smart diff) is part of the data, so this package does not
// depend on the smart-diff context.

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

type jBlastSymbol struct {
	Name string `json:"name"`
	File string `json:"file"`
	Kind string `json:"kind"`
}

type jBlastCaller struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type jDownstream struct {
	Symbol  string         `json:"symbol"`
	Callers []jBlastCaller `json:"callers"`
}

type jBlast struct {
	Summary        string         `json:"summary"`
	ChangedSymbols []jBlastSymbol `json:"changed_symbols"`
	Downstream     []jDownstream  `json:"downstream"`
	Degraded       bool           `json:"degraded"`
	Reason         string         `json:"reason"`
}

func (b jBlast) domain() domain.Blast {
	out := domain.Blast{Summary: b.Summary, Degraded: b.Degraded, Reason: b.Reason}
	for _, s := range b.ChangedSymbols {
		out.ChangedSymbols = append(out.ChangedSymbols, domain.BlastSymbol(s))
	}
	for _, g := range b.Downstream {
		dg := domain.BlastGroup{Symbol: g.Symbol}
		for _, c := range g.Callers {
			dg.Callers = append(dg.Callers, domain.BlastCaller(c))
		}
		out.Downstream = append(out.Downstream, dg)
	}
	return out
}

type jFinding struct {
	Severity  string `json:"severity"`
	File      string `json:"file"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Title     string `json:"title"`
}

func (f jFinding) domain() domain.FindingFact {
	return domain.FindingFact{Severity: reviewer.Severity(f.Severity), File: f.File, StartLine: f.StartLine, EndLine: f.EndLine, Title: f.Title}
}

type jFile struct {
	Path      string   `json:"path"`
	Role      string   `json:"role"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
	Ranges    [][2]int `json:"ranges"`
}

func (f jFile) changed() domain.ChangedFile {
	return domain.ChangedFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Ranges: toRanges(f.Ranges)}
}

func toRanges(in [][2]int) []domain.LineRange {
	out := make([]domain.LineRange, len(in))
	for i, r := range in {
		out[i] = domain.LineRange{Start: r[0], End: r[1]}
	}
	return out
}

func fromRanges(in []domain.LineRange) [][2]int {
	out := make([][2]int, len(in))
	for i, r := range in {
		out[i] = [2]int{r.Start, r.End}
	}
	return out
}

type jIntent struct {
	Intent      string   `json:"intent"`
	InScope     []string `json:"in_scope"`
	OutOfScope  []string `json:"out_of_scope"`
	ChangeType  *string  `json:"change_type"`
	Confidence  *string  `json:"confidence"`
	DerivedFrom string   `json:"derived_from"`
}

type jInput struct {
	Title       string   `json:"title"`
	Description *string  `json:"description"`
	HeadSHA     string   `json:"headSha"`
	Files       []jFile  `json:"files"`
	MoreFiles   int      `json:"moreFiles"`
	Intent      *jIntent `json:"intent"`
	Blast       *struct {
		Summary        string         `json:"summary"`
		ChangedSymbols []jBlastSymbol `json:"changedSymbols"`
		Callers        []jBlastCaller `json:"callers"`
	} `json:"blast"`
	Findings []jFinding `json:"findings"`
	Issue    *struct {
		Number int     `json:"number"`
		Title  string  `json:"title"`
		Body   *string `json:"body"`
	} `json:"issue"`
	Specs []struct {
		Path   string  `json:"path"`
		Text   *string `json:"text"`
		Repeat *struct {
			Unit string `json:"unit"`
			N    int    `json:"n"`
		} `json:"repeat"`
	} `json:"specs"`
}

func (j jInput) domain() domain.BriefInput {
	in := domain.BriefInput{Title: j.Title, HeadSHA: j.HeadSHA, MoreFiles: j.MoreFiles}
	if j.Description != nil {
		in.Description = *j.Description
	}
	for _, f := range j.Files {
		in.Files = append(in.Files, domain.PromptFile{ChangedFile: f.changed(), Role: f.Role})
	}
	if j.Intent != nil {
		in.Intent = &reviewer.Intent{
			Text: j.Intent.Intent, InScope: j.Intent.InScope, OutOfScope: j.Intent.OutOfScope,
			ChangeType: j.Intent.ChangeType, Confidence: j.Intent.Confidence, DerivedFrom: j.Intent.DerivedFrom,
		}
	}
	if j.Blast != nil {
		pb := domain.PromptBlast{Summary: j.Blast.Summary}
		for _, s := range j.Blast.ChangedSymbols {
			pb.ChangedSymbols = append(pb.ChangedSymbols, domain.BlastSymbol(s))
		}
		for _, c := range j.Blast.Callers {
			pb.Callers = append(pb.Callers, domain.BlastCaller(c))
		}
		in.Blast = &pb
	}
	for _, f := range j.Findings {
		in.Findings = append(in.Findings, f.domain())
	}
	if j.Issue != nil {
		is := domain.IssueFact{Number: j.Issue.Number, Title: j.Issue.Title}
		if j.Issue.Body != nil {
			is.Body = *j.Issue.Body
		}
		in.Issue = &is
	}
	for _, s := range j.Specs {
		text := ""
		switch {
		case s.Repeat != nil:
			text = strings.Repeat(s.Repeat.Unit, s.Repeat.N)
		case s.Text != nil:
			text = *s.Text
		}
		in.Specs = append(in.Specs, domain.SpecText{Path: s.Path, Text: text})
	}
	return in
}

type nodeCases struct {
	System []struct {
		System string `json:"system"`
	} `json:"system"`
	Roles  []struct{ Path, Role string } `json:"roles"`
	Ranges []struct {
		Patch string   `json:"patch"`
		Want  [][2]int `json:"want"`
	} `json:"ranges"`
	SelectFiles []struct {
		Files []jFile `json:"files"`
		Want  struct {
			Files []jFile `json:"files"`
			More  int     `json:"more"`
		} `json:"want"`
	} `json:"select_files"`
	Findings []struct {
		Findings []jFinding `json:"findings"`
		Want     []jFinding `json:"want"`
	} `json:"findings"`
	Blast []struct {
		Blast  jBlast `json:"blast"`
		Usable bool   `json:"usable"`
		Want   struct {
			Summary        string         `json:"summary"`
			ChangedSymbols []jBlastSymbol `json:"changedSymbols"`
			Callers        []jBlastCaller `json:"callers"`
		} `json:"want"`
	} `json:"blast"`
	Issue []struct {
		Body *string `json:"body"`
		Want *int    `json:"want"`
	} `json:"issue"`
	Missing []struct {
		Description *string `json:"description"`
		HasIntent   bool    `json:"hasIntent"`
		Blast       *struct {
			Degraded bool   `json:"degraded"`
			Reason   string `json:"reason"`
		} `json:"blast"`
		Specs struct {
			ReviewedAgents int `json:"reviewedAgents"`
			AttachedDocs   int `json:"attachedDocs"`
			Unreadable     int `json:"unreadable"`
			OverBudget     int `json:"overBudget"`
		} `json:"specs"`
		Issue string        `json:"issue"`
		Want  []jMissingOut `json:"want"`
	} `json:"missing"`
	Prompt []struct {
		Input jInput `json:"input"`
		Want  struct {
			Sources  []struct{ Heading, Label, Content string } `json:"sources"`
			User     string                                     `json:"user"`
			Estimate int                                        `json:"estimate"`
		} `json:"want"`
	} `json:"prompt"`
	Trim []struct {
		Name      string `json:"name"`
		Input     jInput `json:"input"`
		MaxTokens int    `json:"maxTokens"`
		Want      struct {
			DroppedSpecs int      `json:"droppedSpecs"`
			DroppedFiles int      `json:"droppedFiles"`
			EstTokens    int      `json:"estTokens"`
			SpecPaths    []string `json:"specPaths"`
			FilePaths    []string `json:"filePaths"`
			MoreFiles    int      `json:"moreFiles"`
			User         *string  `json:"user"`
		} `json:"want"`
	} `json:"trim"`
	Normalize []struct {
		Files []struct {
			Path   string   `json:"path"`
			Ranges [][2]int `json:"ranges"`
		} `json:"files"`
		Findings []struct {
			File      string `json:"file"`
			StartLine int    `json:"startLine"`
			EndLine   int    `json:"endLine"`
		} `json:"findings"`
		Blast *struct {
			ChangedSymbols []jBlastSymbol `json:"changedSymbols"`
			Downstream     []jDownstream  `json:"downstream"`
		} `json:"blast"`
		Raw  jRaw `json:"raw"`
		Want struct {
			Summary     string   `json:"summary"`
			Risks       []jRisk  `json:"risks"`
			ReviewFocus []jFocus `json:"review_focus"`
			Dropped     struct {
				Risks       int `json:"risks"`
				ReviewFocus int `json:"review_focus"`
				RiskRefs    int `json:"risk_refs"`
			} `json:"dropped"`
		} `json:"want"`
	} `json:"normalize"`
	Stale []struct {
		StoredHead    string `json:"storedHead"`
		StoredVersion string `json:"storedVersion"`
		CurrentHead   string `json:"currentHead"`
		Want          bool   `json:"want"`
	} `json:"stale"`
}

type jMissingOut struct {
	Input  string `json:"input"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

type jRisk struct {
	Kind        string   `json:"kind"`
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Severity    string   `json:"severity"`
	FileRefs    []string `json:"file_refs"`
}

type jFocus struct {
	File   string  `json:"file"`
	Line   float64 `json:"line"`
	Reason string  `json:"reason"`
}

type jRaw struct {
	Summary     string   `json:"summary"`
	Risks       []jRisk  `json:"risks"`
	ReviewFocus []jFocus `json:"review_focus"`
}

func (r jRaw) domain() domain.LLMOutput {
	out := domain.LLMOutput{Summary: r.Summary}
	for _, k := range r.Risks {
		out.Risks = append(out.Risks, domain.LLMRisk{Kind: k.Kind, Title: k.Title, Explanation: k.Explanation, Severity: k.Severity, FileRefs: k.FileRefs})
	}
	for _, f := range r.ReviewFocus {
		out.ReviewFocus = append(out.ReviewFocus, domain.LLMFocus(f))
	}
	return out
}

var loadCases = sync.OnceValues(func() (*nodeCases, error) {
	raw, err := os.ReadFile("testdata/node_cases.json")
	if err != nil {
		return nil, err
	}
	var c nodeCases
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return &c, nil
})

func cases(t *testing.T) *nodeCases {
	t.Helper()
	c, err := loadCases()
	if err != nil {
		t.Fatalf("load testdata/node_cases.json: %v", err)
	}
	return c
}

func roleFunc(t *testing.T) domain.RoleFunc {
	t.Helper()
	roles := map[string]string{}
	for _, r := range cases(t).Roles {
		roles[r.Path] = r.Role
	}
	return func(path string) string {
		role, ok := roles[path]
		if !ok {
			t.Errorf("role of %q is not in the Node data", path)
		}
		return role
	}
}

func TestSystemPrompt_EqualsNode(t *testing.T) {
	c := cases(t)
	if len(c.System) != 1 {
		t.Fatalf("system prompts in data = %d, want 1", len(c.System))
	}
	if diff := cmp.Diff(c.System[0].System, domain.SystemPrompt); diff != "" {
		t.Errorf("SystemPrompt mismatch (-node +go):\n%s", diff)
	}
}

func TestChangedRanges_EqualsNode(t *testing.T) {
	for i, tc := range cases(t).Ranges {
		got := fromRanges(domain.ChangedRanges(tc.Patch))
		if diff := cmp.Diff(tc.Want, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d ChangedRanges(%q) mismatch (-want +got):\n%s", i, tc.Patch, diff)
		}
	}
}

func TestSelectFiles_EqualsNode(t *testing.T) {
	role := roleFunc(t)
	for i, tc := range cases(t).SelectFiles {
		files := make([]domain.ChangedFile, len(tc.Files))
		for k, f := range tc.Files {
			files[k] = f.changed()
		}
		got := domain.SelectFiles(files, role)
		var gotFiles []jFile
		for _, f := range got.Files {
			gotFiles = append(gotFiles, jFile{Path: f.Path, Role: f.Role, Additions: f.Additions, Deletions: f.Deletions, Ranges: fromRanges(f.Ranges)})
		}
		if diff := cmp.Diff(tc.Want.Files, gotFiles, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d (%d files) SelectFiles files mismatch (-want +got):\n%s", i, len(tc.Files), diff)
		}
		if got.More != tc.Want.More {
			t.Errorf("case %d (%d files) SelectFiles more = %d, want %d", i, len(tc.Files), got.More, tc.Want.More)
		}
	}
}

func TestSelectFindings_EqualsNode(t *testing.T) {
	for i, tc := range cases(t).Findings {
		in := make([]domain.FindingFact, len(tc.Findings))
		for k, f := range tc.Findings {
			in[k] = f.domain()
		}
		var got []jFinding
		for _, f := range domain.SelectFindings(in) {
			got = append(got, jFinding{Severity: string(f.Severity), File: f.File, StartLine: f.StartLine, EndLine: f.EndLine, Title: f.Title})
		}
		if diff := cmp.Diff(tc.Want, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d SelectFindings(%d findings) mismatch (-want +got):\n%s", i, len(tc.Findings), diff)
		}
	}
}

func TestPromptBlast_EqualsNode(t *testing.T) {
	for i, tc := range cases(t).Blast {
		b := tc.Blast.domain()
		got := domain.NewPromptBlast(b)
		if got.Summary != tc.Want.Summary {
			t.Errorf("case %d NewPromptBlast summary = %q, want %q", i, got.Summary, tc.Want.Summary)
		}
		var syms []jBlastSymbol
		for _, s := range got.ChangedSymbols {
			syms = append(syms, jBlastSymbol(s))
		}
		var callers []jBlastCaller
		for _, c := range got.Callers {
			callers = append(callers, jBlastCaller(c))
		}
		if diff := cmp.Diff(tc.Want.ChangedSymbols, syms, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d NewPromptBlast symbols mismatch (-want +got):\n%s", i, diff)
		}
		if diff := cmp.Diff(tc.Want.Callers, callers, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d NewPromptBlast callers mismatch (-want +got):\n%s", i, diff)
		}
		if usable := domain.UsableBlast(&b) != nil; usable != tc.Usable {
			t.Errorf("case %d UsableBlast(degraded=%v) usable = %v, want %v", i, b.Degraded, usable, tc.Usable)
		}
	}
}

func TestLinkedIssueNumber_EqualsNode(t *testing.T) {
	for _, tc := range cases(t).Issue {
		n, ok := domain.LinkedIssueNumber(tc.Body)
		switch {
		case tc.Want == nil && ok:
			t.Errorf("LinkedIssueNumber(%v) = %d, want none", tc.Body, n)
		case tc.Want != nil && (!ok || n != *tc.Want):
			t.Errorf("LinkedIssueNumber(%v) = %d (ok=%v), want %d", tc.Body, n, ok, *tc.Want)
		}
	}
}

func TestBuildMissingInputs_EqualsNode(t *testing.T) {
	for i, tc := range cases(t).Missing {
		f := domain.MissingInputFacts{
			Description: tc.Description,
			HasIntent:   tc.HasIntent,
			Specs:       domain.SpecsOutcome{ReviewedAgents: tc.Specs.ReviewedAgents, AttachedDocs: tc.Specs.AttachedDocs, Unreadable: tc.Specs.Unreadable, OverBudget: tc.Specs.OverBudget},
			Issue:       domain.IssueOutcome(tc.Issue),
		}
		if tc.Blast != nil {
			f.Blast = &domain.Blast{Degraded: tc.Blast.Degraded, Reason: tc.Blast.Reason}
		}
		var got []jMissingOut
		for _, m := range domain.BuildMissingInputs(f) {
			got = append(got, jMissingOut{Input: string(m.Input), Reason: string(m.Reason), Detail: m.Detail})
		}
		if diff := cmp.Diff(tc.Want, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d BuildMissingInputs(%+v) mismatch (-want +got):\n%s", i, f, diff)
		}
	}
}

func TestPrompt_EqualsNode(t *testing.T) {
	for i, tc := range cases(t).Prompt {
		in := tc.Input.domain()
		var gotSources []struct{ Heading, Label, Content string }
		for _, s := range domain.Sources(in) {
			gotSources = append(gotSources, struct{ Heading, Label, Content string }{s.Heading, string(s.Label), s.Content})
		}
		if diff := cmp.Diff(tc.Want.Sources, gotSources, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d Sources mismatch (-want +got):\n%s", i, diff)
		}
		if diff := cmp.Diff(tc.Want.User, domain.UserMessage(in)); diff != "" {
			t.Errorf("case %d UserMessage mismatch (-want +got):\n%s", i, diff)
		}
		if got := domain.EstimateInputTokens(in); got != tc.Want.Estimate {
			t.Errorf("case %d EstimateInputTokens = %d, want %d", i, got, tc.Want.Estimate)
		}
	}
}

func TestTrimToBudget_EqualsNode(t *testing.T) {
	for _, tc := range cases(t).Trim {
		t.Run(tc.Name, func(t *testing.T) {
			in := tc.Input.domain()
			files, specs := len(in.Files), len(in.Specs)
			got := domain.TrimToBudget(in, tc.MaxTokens)
			if got.DroppedSpecs != tc.Want.DroppedSpecs || got.DroppedFiles != tc.Want.DroppedFiles || got.EstTokens != tc.Want.EstTokens || got.Input.MoreFiles != tc.Want.MoreFiles {
				t.Errorf("TrimToBudget(max %d) = specs -%d files -%d est %d more %d, want specs -%d files -%d est %d more %d",
					tc.MaxTokens, got.DroppedSpecs, got.DroppedFiles, got.EstTokens, got.Input.MoreFiles,
					tc.Want.DroppedSpecs, tc.Want.DroppedFiles, tc.Want.EstTokens, tc.Want.MoreFiles)
			}
			var filePaths []string
			for _, f := range got.Input.Files {
				filePaths = append(filePaths, f.Path)
			}
			if diff := cmp.Diff(tc.Want.FilePaths, filePaths, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("TrimToBudget files mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.Want.SpecPaths, domain.SpecPaths(got.Input), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("TrimToBudget specs mismatch (-want +got):\n%s", diff)
			}
			if tc.Want.User != nil {
				if diff := cmp.Diff(*tc.Want.User, domain.UserMessage(got.Input)); diff != "" {
					t.Errorf("trimmed UserMessage mismatch (-want +got):\n%s", diff)
				}
			}
			if len(in.Files) != files || len(in.Specs) != specs {
				t.Errorf("TrimToBudget changed its argument: files %d -> %d, specs %d -> %d", files, len(in.Files), specs, len(in.Specs))
			}
		})
	}
}

func TestNormalizeBrief_EqualsNode(t *testing.T) {
	for i, tc := range cases(t).Normalize {
		var facts domain.GroundingFacts
		for _, f := range tc.Files {
			facts.Files = append(facts.Files, domain.GroundFile{Path: f.Path, Ranges: toRanges(f.Ranges)})
		}
		for _, f := range tc.Findings {
			facts.Findings = append(facts.Findings, domain.GroundFinding(f))
		}
		if tc.Blast != nil {
			b := jBlast{ChangedSymbols: tc.Blast.ChangedSymbols, Downstream: tc.Blast.Downstream}.domain()
			facts.Blast = &b
		}
		got := domain.NormalizeBrief(tc.Raw.domain(), facts)

		var gotRisks []jRisk
		for _, r := range got.Risks {
			gotRisks = append(gotRisks, jRisk{Kind: r.Kind, Title: r.Title, Explanation: r.Explanation, Severity: string(r.Severity), FileRefs: r.FileRefs})
		}
		var gotFocus []jFocus
		for _, f := range got.ReviewFocus {
			gotFocus = append(gotFocus, jFocus{File: f.File, Line: float64(f.Line), Reason: f.Reason})
		}
		if got.Summary != tc.Want.Summary {
			t.Errorf("case %d NormalizeBrief summary = %q, want %q", i, got.Summary, tc.Want.Summary)
		}
		if diff := cmp.Diff(tc.Want.Risks, gotRisks, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d NormalizeBrief risks mismatch (-want +got):\n%s", i, diff)
		}
		if diff := cmp.Diff(tc.Want.ReviewFocus, gotFocus, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d NormalizeBrief review_focus mismatch (-want +got):\n%s", i, diff)
		}
		want := domain.DroppedCounts{Risks: tc.Want.Dropped.Risks, ReviewFocus: tc.Want.Dropped.ReviewFocus, RiskRefs: tc.Want.Dropped.RiskRefs}
		if got.Dropped != want {
			t.Errorf("case %d NormalizeBrief dropped = %+v, want %+v", i, got.Dropped, want)
		}
	}
}

func TestIsStale_EqualsNode(t *testing.T) {
	for _, tc := range cases(t).Stale {
		if got := domain.IsStale(tc.StoredHead, tc.StoredVersion, tc.CurrentHead); got != tc.Want {
			t.Errorf("IsStale(%q, %q, %q) = %v, want %v", tc.StoredHead, tc.StoredVersion, tc.CurrentHead, got, tc.Want)
		}
	}
}

// The provider schema is the output of Node's toJsonSchema(BriefLlmOutput, 'PrBriefLlm').
func TestSchemaJSON_EqualsNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/prbrief_schema.node.json")
	if err != nil {
		t.Fatalf("read Node golden: %v", err)
	}
	want := bytes.TrimSpace(raw)
	if !bytes.Equal(domain.SchemaJSON, want) {
		t.Errorf("SchemaJSON differs from the Node golden\n got %s\nwant %s", domain.SchemaJSON, want)
	}
	strict, err := llm.StrictSchema(domain.SchemaJSON)
	if err != nil {
		t.Fatalf("StrictSchema(SchemaJSON): %v", err)
	}
	if !bytes.Equal(strict, want) {
		t.Errorf("StrictSchema(SchemaJSON) differs from the Node golden\n got %s\nwant %s", strict, want)
	}
}
