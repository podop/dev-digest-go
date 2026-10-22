package domain_test

// Cases in testdata/node_cases.json were produced by running the Node implementation of
// dev-digest (commit d1c9093: server diff-parser.ts and reviewer-core sliceDiff, splitOversizeDiff,
// reduceReviews, groundFindings, scoreFromFindings, countBlockers, applyScopePolicy and
// assemblePrompt) on seeded random inputs full of awkward text: surrogate pairs, NBSP, BOM,
// U+2028, CR, quotes, odd diff headers. A sample of 184 cases is kept; the full run of 11,000+
// cases matched. The Go port must give identical results.

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/reviewer/domain"
)

type nodeHunk struct {
	File           string `json:"file"`
	OldStart       int    `json:"oldStart"`
	OldLines       int    `json:"oldLines"`
	NewStart       int    `json:"newStart"`
	NewLines       int    `json:"newLines"`
	NewLineNumbers []int  `json:"newLineNumbers"`
}

type nodeFile struct {
	Path      string     `json:"path"`
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	Hunks     []nodeHunk `json:"hunks"`
}

type nodeFinding struct {
	ID         string  `json:"id"`
	Severity   string  `json:"severity"`
	Title      string  `json:"title"`
	File       string  `json:"file"`
	StartLine  float64 `json:"start_line"`
	EndLine    float64 `json:"end_line"`
	Kind       *string `json:"kind"`
	OutOfScope *bool   `json:"out_of_scope"`
}

type nodeReview struct {
	Verdict  string        `json:"verdict"`
	Summary  string        `json:"summary"`
	Score    int           `json:"score"`
	Findings []nodeFinding `json:"findings"`
}

type nodeSection struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Role      string `json:"role"`
	Trust     string `json:"trust"`
	Chars     int    `json:"chars"`
	Tokens    int    `json:"tokens"`
	Items     *int   `json:"items"`
	Truncated *bool  `json:"truncated"`
}

type nodeCases struct {
	Parse []struct {
		Raw   string     `json:"raw"`
		Files []nodeFile `json:"files"`
	} `json:"parse"`
	Slice []struct {
		Raw   string     `json:"raw"`
		Files []nodeFile `json:"files"`
		Path  string     `json:"path"`
		Want  string     `json:"want"`
	} `json:"slice"`
	Split []struct {
		Raw       string   `json:"raw"`
		Max       int      `json:"max"`
		Parts     []string `json:"parts"`
		Truncated bool     `json:"truncated"`
	} `json:"split"`
	Reduce []struct {
		Partials []nodeReview `json:"partials"`
		Want     nodeReview   `json:"want"`
	} `json:"reduce"`
	Ground []struct {
		Raw      string         `json:"raw"`
		Findings []nodeFinding  `json:"findings"`
		Kept     []string       `json:"kept"`
		Dropped  [][]string     `json:"dropped"`
		Summary  string         `json:"summary"`
		Score    int            `json:"score"`
		Blockers map[string]int `json:"blockers"`
		Scope    []*bool        `json:"scope"`
	} `json:"ground"`
	Prompt []struct {
		Parts struct {
			System        string   `json:"system"`
			Task          string   `json:"task"`
			PRDescription string   `json:"prDescription"`
			Skills        []string `json:"skills"`
			RepoMap       string   `json:"repoMap"`
			Callers       string   `json:"callers"`
			Specs         []struct {
				Path string `json:"path"`
				Text string `json:"text"`
			} `json:"specs"`
			Diff string `json:"diff"`
		} `json:"parts"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Assembly struct {
			System        string  `json:"system"`
			Skills        *string `json:"skills"`
			Specs         *string `json:"specs"`
			Callers       *string `json:"callers"`
			RepoMap       *string `json:"repo_map"`
			PRDescription *string `json:"pr_description"`
			User          string  `json:"user"`
		} `json:"assembly"`
		Sections []nodeSection `json:"sections"`
	} `json:"prompt"`
}

func loadNodeCases(t *testing.T) nodeCases {
	t.Helper()
	b, err := os.ReadFile("testdata/node_cases.json")
	if err != nil {
		t.Fatalf("read node cases: %v", err)
	}
	var c nodeCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("decode node cases: %v", err)
	}
	return c
}

func diffFiles(in []nodeFile) []domain.DiffFile {
	var out []domain.DiffFile
	for _, f := range in {
		df := domain.DiffFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions}
		for _, h := range f.Hunks {
			df.Hunks = append(df.Hunks, domain.Hunk{
				File: h.File, OldStart: h.OldStart, OldLines: h.OldLines,
				NewStart: h.NewStart, NewLines: h.NewLines, NewLineNumbers: h.NewLineNumbers,
			})
		}
		out = append(out, df)
	}
	return out
}

func domainFinding(f nodeFinding) domain.Finding {
	var kind domain.Kind
	if f.Kind != nil {
		kind = domain.Kind(*f.Kind)
	}
	return domain.Finding{
		ID: f.ID, Severity: domain.Severity(f.Severity), Title: f.Title, File: f.File,
		StartLine: int(f.StartLine), EndLine: int(f.EndLine), Kind: kind, OutOfScope: f.OutOfScope,
	}
}

func TestNodeParity_ParseUnifiedDiff(t *testing.T) {
	for i, tc := range loadNodeCases(t).Parse {
		got := domain.ParseUnifiedDiff(tc.Raw)
		if diff := cmp.Diff(diffFiles(tc.Files), got.Files, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d ParseUnifiedDiff(%q) mismatch (-want +got):\n%s", i, tc.Raw, diff)
		}
		if got.Raw != tc.Raw {
			t.Errorf("case %d ParseUnifiedDiff(%q).Raw = %q, want the input", i, tc.Raw, got.Raw)
		}
	}
}

func TestNodeParity_SliceDiff(t *testing.T) {
	for i, tc := range loadNodeCases(t).Slice {
		d := domain.Diff{Raw: tc.Raw, Files: diffFiles(tc.Files)}
		if got := domain.SliceDiff(d, tc.Path); got != tc.Want {
			t.Errorf("case %d SliceDiff(%q, %q) = %q, want %q", i, tc.Raw, tc.Path, got, tc.Want)
		}
	}
}

func TestNodeParity_SplitOversizeDiff(t *testing.T) {
	for i, tc := range loadNodeCases(t).Split {
		got := domain.SplitOversizeDiff(tc.Raw, tc.Max)
		if diff := cmp.Diff(tc.Parts, got.Parts); diff != "" || got.Truncated != tc.Truncated {
			t.Errorf("case %d SplitOversizeDiff(%q, %d) = %q truncated=%v, want %q truncated=%v\n%s",
				i, tc.Raw, tc.Max, got.Parts, got.Truncated, tc.Parts, tc.Truncated, diff)
		}
	}
}

func TestNodeParity_ReduceReviews(t *testing.T) {
	for i, tc := range loadNodeCases(t).Reduce {
		var in []domain.Review
		for _, p := range tc.Partials {
			r := domain.Review{Verdict: domain.Verdict(p.Verdict), Summary: p.Summary, Score: p.Score}
			for _, f := range p.Findings {
				r.Findings = append(r.Findings, domainFinding(f))
			}
			in = append(in, r)
		}
		got := domain.ReduceReviews(in)
		if string(got.Verdict) != tc.Want.Verdict || got.Summary != tc.Want.Summary ||
			got.Score != tc.Want.Score || len(got.Findings) != len(tc.Want.Findings) {
			t.Errorf("case %d ReduceReviews = {%s %q %d, %d findings}, want {%s %q %d, %d findings}", i,
				got.Verdict, got.Summary, got.Score, len(got.Findings),
				tc.Want.Verdict, tc.Want.Summary, tc.Want.Score, len(tc.Want.Findings))
		}
	}
}

func TestNodeParity_GroundScoreBlockers(t *testing.T) {
	for i, tc := range loadNodeCases(t).Ground {
		d := domain.ParseUnifiedDiff(tc.Raw)
		var in []domain.Finding
		for _, f := range tc.Findings {
			in = append(in, domainFinding(f))
		}
		g := domain.GroundFindings(in, d)

		var kept []string
		for _, f := range g.Kept {
			kept = append(kept, f.ID+"|"+f.Title)
		}
		var dropped [][]string
		for _, x := range g.Dropped {
			dropped = append(dropped, []string{x.Finding.ID + "|" + x.Finding.Title, x.Reason})
		}
		if diff := cmp.Diff(tc.Kept, kept, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d kept mismatch (-want +got):\n%s", i, diff)
		}
		if diff := cmp.Diff(tc.Dropped, dropped, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("case %d dropped mismatch (-want +got):\n%s", i, diff)
		}
		if got := g.Summary(); got != tc.Summary {
			t.Errorf("case %d Summary() = %q, want %q", i, got, tc.Summary)
		}
		if got := domain.ScoreFromFindings(g.Kept); got != tc.Score {
			t.Errorf("case %d ScoreFromFindings = %d, want %d", i, got, tc.Score)
		}
		for failOn, want := range tc.Blockers {
			if got := domain.CountBlockers(g.Kept, domain.FailOn(failOn)); got != want {
				t.Errorf("case %d CountBlockers(%s) = %d, want %d", i, failOn, got, want)
			}
		}
		var scope []*bool
		for _, f := range domain.ApplyScopePolicy(g.Kept, false).Findings {
			scope = append(scope, f.OutOfScope)
		}
		if len(scope) != len(g.Kept) {
			t.Errorf("case %d ApplyScopePolicy kept %d findings, want %d", i, len(scope), len(g.Kept))
		}
		for _, s := range scope {
			if s != nil {
				t.Errorf("case %d ApplyScopePolicy without intent left out_of_scope = %v, want nil", i, *s)
			}
		}
	}
}

func TestNodeParity_AssemblePrompt(t *testing.T) {
	for i, tc := range loadNodeCases(t).Prompt {
		p := domain.PromptParts{
			System: tc.Parts.System, Task: tc.Parts.Task, PRDescription: tc.Parts.PRDescription,
			Skills: tc.Parts.Skills, RepoMap: tc.Parts.RepoMap, Callers: tc.Parts.Callers, Diff: tc.Parts.Diff,
		}
		for _, s := range tc.Parts.Specs {
			p.Specs = append(p.Specs, domain.ProjectDoc{Path: s.Path, Text: s.Text})
		}
		got := domain.AssemblePrompt(p)

		// Node keeps an empty callers/repoMap string as "", Go reads "" as absent.
		wantCallers, wantRepoMap := tc.Assembly.Callers, tc.Assembly.RepoMap
		if wantCallers != nil && *wantCallers == "" {
			wantCallers = nil
		}
		if wantRepoMap != nil && *wantRepoMap == "" {
			wantRepoMap = nil
		}
		wantMessages := []domain.Message{
			{Role: domain.RoleSystem, Content: tc.Messages[0].Content},
			{Role: domain.RoleUser, Content: tc.Messages[1].Content},
		}
		if diff := cmp.Diff(wantMessages, got.Messages); diff != "" {
			t.Errorf("case %d messages mismatch (-want +got):\n%s", i, diff)
		}
		wantAssembly := domain.Assembly{
			System: tc.Assembly.System, Skills: tc.Assembly.Skills, Specs: tc.Assembly.Specs,
			Callers: wantCallers, RepoMap: wantRepoMap, PRDescription: tc.Assembly.PRDescription,
			User: tc.Assembly.User,
		}
		if diff := cmp.Diff(wantAssembly, got.Assembly); diff != "" {
			t.Errorf("case %d assembly mismatch (-want +got):\n%s", i, diff)
		}
		var wantSections []domain.SectionMeta
		for _, s := range tc.Sections {
			wantSections = append(wantSections, domain.SectionMeta{
				Name: domain.SectionName(s.Name), Source: domain.SectionSource(s.Source),
				Role: domain.Role(s.Role), Trust: domain.Trust(s.Trust),
				Chars: s.Chars, Tokens: s.Tokens, Items: s.Items, Truncated: s.Truncated,
			})
		}
		if diff := cmp.Diff(wantSections, got.Sections); diff != "" {
			t.Errorf("case %d sections mismatch (-want +got):\n%s", i, diff)
		}
	}
}
