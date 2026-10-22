package parity_test

// S31 (AC5044): the pure functions of the brief context against the Node golden brief.json:
// changed ranges from hunk headers, file and finding selection, the usable and the prompt blast,
// the linked issue number, the missing inputs, the prompt (sources, user message, estimate, log
// sections, trimming), the normalised brief and staleness. The service golden sections (flows,
// scenarios, stored documents, the issue source) are in brief_services_test.go.
//
// The domain package has its own Node differential (brief/domain/parity_test.go, testdata
// node_cases.json); this one runs the larger S21 corpus through the same functions.
//
// Intentional deviation, asserted below: a focus line the model asked for above 2^53-1 that no fact
// grounds is clamped to 2^53-1 (spec 7.9.5; Node kept the float, which no integer column holds).

import (
	"fmt"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/brief/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	smartdiff "dev-digest-go/backend/internal/smartdiff/domain"
)

// ---------------------------------------------------------------------------
// Golden shapes shared by the brief tests
// ---------------------------------------------------------------------------

// pbRange is a golden range; JavaScript numbers, so a start above the int range stays exact enough
// to be recognised (see TestBriefParity_ChangedRanges).
type pbRange struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

func pbRanges(rs []pbRange) []domain.LineRange {
	var out []domain.LineRange
	for _, r := range rs {
		out = append(out, domain.LineRange{Start: int(r.Start), End: int(r.End)})
	}
	return out
}

func pbRangesShape(rs []domain.LineRange) []pbRange {
	out := []pbRange{}
	for _, r := range rs {
		out = append(out, pbRange{float64(r.Start), float64(r.End)})
	}
	return out
}

type pbBriefFile struct {
	Path      string    `json:"path"`
	Additions int       `json:"additions"`
	Deletions int       `json:"deletions"`
	Ranges    []pbRange `json:"ranges"`
	Role      string    `json:"role"`
}

func (f pbBriefFile) changed() domain.ChangedFile {
	return domain.ChangedFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Ranges: pbRanges(f.Ranges)}
}

type pbBriefFinding struct {
	Severity  string `json:"severity"`
	File      string `json:"file"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Title     string `json:"title"`
}

func (f pbBriefFinding) fact() domain.FindingFact {
	return domain.FindingFact{Severity: reviewer.Severity(f.Severity), File: f.File, StartLine: f.StartLine, EndLine: f.EndLine, Title: f.Title}
}

type pbBlastGolden struct {
	ChangedSymbols []struct {
		Name string `json:"name"`
		File string `json:"file"`
		Kind string `json:"kind"`
	} `json:"changed_symbols"`
	Downstream []struct {
		Symbol  string `json:"symbol"`
		Callers []struct {
			Name string `json:"name"`
			File string `json:"file"`
			Line int    `json:"line"`
		} `json:"callers"`
		Endpoints []string `json:"endpoints_affected"`
		Crons     []string `json:"crons_affected"`
	} `json:"downstream"`
	Summary  string `json:"summary"`
	Degraded bool   `json:"degraded"`
	Reason   string `json:"reason"`
}

func (b *pbBlastGolden) domain() *domain.Blast {
	if b == nil {
		return nil
	}
	out := domain.Blast{Summary: b.Summary, Degraded: b.Degraded, Reason: b.Reason}
	for _, s := range b.ChangedSymbols {
		out.ChangedSymbols = append(out.ChangedSymbols, domain.BlastSymbol{Name: s.Name, File: s.File, Kind: s.Kind})
	}
	for _, g := range b.Downstream {
		bg := domain.BlastGroup{Symbol: g.Symbol, EndpointsAffected: g.Endpoints, CronsAffected: g.Crons}
		for _, c := range g.Callers {
			bg.Callers = append(bg.Callers, domain.BlastCaller{Name: c.Name, File: c.File, Line: c.Line})
		}
		out.Downstream = append(out.Downstream, bg)
	}
	return &out
}

type pbIntentGolden struct {
	Intent      string   `json:"intent"`
	InScope     []string `json:"in_scope"`
	OutScope    []string `json:"out_of_scope"`
	ChangeType  *string  `json:"change_type"`
	Confidence  *string  `json:"confidence"`
	DerivedFrom string   `json:"derived_from"`
	Sources     []struct {
		Kind   string  `json:"kind"`
		Ref    string  `json:"ref"`
		Status string  `json:"status"`
		Detail *string `json:"detail"`
	} `json:"sources"`
}

func (i *pbIntentGolden) domain() *reviewer.Intent {
	if i == nil {
		return nil
	}
	out := reviewer.Intent{
		Text: i.Intent, InScope: i.InScope, OutOfScope: i.OutScope, ChangeType: i.ChangeType,
		Confidence: i.Confidence, DerivedFrom: i.DerivedFrom,
	}
	for _, s := range i.Sources {
		out.Sources = append(out.Sources, reviewer.IntentSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail})
	}
	return &out
}

// pbBriefRole is the smart-diff role of a path, as the brief's peers adapter binds it.
func pbBriefRole(path string) string { return string(smartdiff.Classify(path)) }

// pbPromptInput is the golden's BriefInput.
type pbPromptInput struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	HeadSHA     string          `json:"headSha"`
	Files       []pbBriefFile   `json:"files"`
	MoreFiles   int             `json:"moreFiles"`
	Intent      *pbIntentGolden `json:"intent"`
	Blast       *struct {
		Summary        string `json:"summary"`
		ChangedSymbols []struct {
			Name string `json:"name"`
			File string `json:"file"`
			Kind string `json:"kind"`
		} `json:"changedSymbols"`
		Callers []struct {
			Name string `json:"name"`
			File string `json:"file"`
			Line int    `json:"line"`
		} `json:"callers"`
	} `json:"blast"`
	Findings []pbBriefFinding `json:"findings"`
	Issue    *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	} `json:"issue"`
	Specs []struct {
		Path string `json:"path"`
		Text pbText `json:"text"`
	} `json:"specs"`
}

func (p pbPromptInput) domain(t testing.TB) domain.BriefInput {
	in := domain.BriefInput{
		Title: p.Title, Description: p.Description, HeadSHA: p.HeadSHA, MoreFiles: p.MoreFiles, Intent: p.Intent.domain(),
	}
	for _, f := range p.Files {
		in.Files = append(in.Files, domain.PromptFile{ChangedFile: f.changed(), Role: f.Role})
	}
	if p.Blast != nil {
		pb := domain.PromptBlast{Summary: p.Blast.Summary}
		for _, s := range p.Blast.ChangedSymbols {
			pb.ChangedSymbols = append(pb.ChangedSymbols, domain.BlastSymbol{Name: s.Name, File: s.File, Kind: s.Kind})
		}
		for _, c := range p.Blast.Callers {
			pb.Callers = append(pb.Callers, domain.BlastCaller{Name: c.Name, File: c.File, Line: c.Line})
		}
		in.Blast = &pb
	}
	for _, f := range p.Findings {
		in.Findings = append(in.Findings, f.fact())
	}
	if p.Issue != nil {
		in.Issue = &domain.IssueFact{Number: p.Issue.Number, Title: p.Issue.Title, Body: p.Issue.Body}
	}
	for _, s := range p.Specs {
		in.Specs = append(in.Specs, domain.SpecText{Path: s.Path, Text: s.Text.plain(t)})
	}
	return in
}

type pbBriefGolden struct {
	Constants struct {
		SystemPrompt  string           `json:"systemPrompt"`
		SchemaName    string           `json:"schemaName"`
		PromptVersion string           `json:"promptVersion"`
		Language      string           `json:"language"`
		Limits        map[string]int64 `json:"limits"`
	} `json:"constants"`
	Input struct {
		Ranges []struct {
			Name   string    `json:"name"`
			Patch  *string   `json:"patch"`
			Ranges []pbRange `json:"ranges"`
		} `json:"ranges"`
		SelectFiles []struct {
			Name     string        `json:"name"`
			Files    []pbBriefFile `json:"files"`
			Selected []pbBriefFile `json:"selected"`
			More     int           `json:"moreFiles"`
		} `json:"selectFiles"`
		SelectFindings []struct {
			Name     string           `json:"name"`
			Findings []pbBriefFinding `json:"findings"`
			Selected []pbBriefFinding `json:"selected"`
		} `json:"selectFindings"`
		Blast []struct {
			Name   string         `json:"name"`
			Blast  *pbBlastGolden `json:"blast"`
			Usable bool           `json:"usable"`
			Prompt *struct {
				Summary        string `json:"summary"`
				ChangedSymbols []struct {
					Name string `json:"name"`
					File string `json:"file"`
					Kind string `json:"kind"`
				} `json:"changedSymbols"`
				Callers []struct {
					Name string `json:"name"`
					File string `json:"file"`
					Line int    `json:"line"`
				} `json:"callers"`
			} `json:"prompt"`
		} `json:"blast"`
		LinkedIssue []struct {
			Body   *string `json:"body"`
			Number *int64  `json:"number"`
		} `json:"linkedIssue"`
		Missing []struct {
			Name  string `json:"name"`
			Facts struct {
				Description *string        `json:"description"`
				HasIntent   bool           `json:"hasIntent"`
				Blast       *pbBlastGolden `json:"blast"`
				Specs       struct {
					ReviewedAgents int `json:"reviewedAgents"`
					AttachedDocs   int `json:"attachedDocs"`
					Unreadable     int `json:"unreadable"`
					OverBudget     int `json:"overBudget"`
				} `json:"specs"`
				Issue string `json:"issue"`
			} `json:"facts"`
			Result []struct {
				Input  string `json:"input"`
				Reason string `json:"reason"`
				Detail string `json:"detail"`
			} `json:"result"`
		} `json:"missing"`
		Truncate []struct {
			Text   string `json:"text"`
			Max    int    `json:"max"`
			Result string `json:"result"`
		} `json:"truncate"`
	} `json:"input"`
	Prompt []struct {
		Name      string        `json:"name"`
		MaxTokens int           `json:"maxTokens"`
		Input     pbPromptInput `json:"input"`
		Sources   []struct {
			Heading string `json:"heading"`
			Label   string `json:"label"`
			Content pbText `json:"content"`
		} `json:"sources"`
		UserMessage pbText `json:"userMessage"`
		Estimate    int    `json:"estimate"`
		Trim        struct {
			DroppedSpecs int      `json:"droppedSpecs"`
			DroppedFiles int      `json:"droppedFiles"`
			EstTokens    int      `json:"estTokens"`
			MoreFiles    int      `json:"moreFiles"`
			Files        []string `json:"files"`
			Specs        []string `json:"specs"`
			UserMessage  pbText   `json:"userMessage"`
			Sections     []struct {
				Name   string `json:"name"`
				Source string `json:"source"`
				Trust  string `json:"trust"`
				Chars  int    `json:"chars"`
				Tokens int    `json:"tokens"`
				Items  int    `json:"items"`
			} `json:"sections"`
		} `json:"trim"`
		SystemIsTheNodeConstant bool `json:"systemIsTheNodeConstant"`
	} `json:"prompt"`
	Normalize []struct {
		Name  string `json:"name"`
		Facts struct {
			Files []struct {
				Path   string    `json:"path"`
				Ranges []pbRange `json:"ranges"`
			} `json:"files"`
			Findings []struct {
				File      string `json:"file"`
				StartLine int    `json:"startLine"`
				EndLine   int    `json:"endLine"`
			} `json:"findings"`
			Blast *pbBlastGolden `json:"blast"`
		} `json:"facts"`
		Raw struct {
			Summary string `json:"summary"`
			Risks   []struct {
				Kind        string   `json:"kind"`
				Title       string   `json:"title"`
				Explanation string   `json:"explanation"`
				Severity    string   `json:"severity"`
				FileRefs    []string `json:"file_refs"`
			} `json:"risks"`
			Focus []struct {
				File   string  `json:"file"`
				Line   float64 `json:"line"`
				Reason string  `json:"reason"`
			} `json:"review_focus"`
		} `json:"raw"`
		Result map[string]any `json:"result"`
	} `json:"normalize"`
	Stale []struct {
		Stored struct {
			HeadSHA       string `json:"head_sha"`
			PromptVersion string `json:"prompt_version"`
		} `json:"stored"`
		Current       string  `json:"current"`
		CurrentPrompt *string `json:"currentPromptVersion"`
		Result        bool    `json:"result"`
	} `json:"stale"`
}

func pbBrief(t *testing.T) *pbBriefGolden {
	t.Helper()
	var g pbBriefGolden
	pbLoad(t, "brief", &g)
	return &g
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestBriefParity_Constants(t *testing.T) {
	g := pbBrief(t)
	if domain.SystemPrompt != g.Constants.SystemPrompt {
		t.Errorf("SystemPrompt differs from Node (-node +go):\n%s", cmp.Diff(g.Constants.SystemPrompt, domain.SystemPrompt))
	}
	if domain.SchemaName != g.Constants.SchemaName || domain.PromptVersion != g.Constants.PromptVersion || domain.BriefLanguage != g.Constants.Language {
		t.Errorf("schema/version/language = %q/%q/%q, want %q/%q/%q", domain.SchemaName, domain.PromptVersion, domain.BriefLanguage,
			g.Constants.SchemaName, g.Constants.PromptVersion, g.Constants.Language)
	}
	l := g.Constants.Limits
	for name, c := range map[string][2]int64{
		"inputMaxTokens":       {domain.InputBudgetTokens, l["inputMaxTokens"]},
		"llmMaxOutputTokens":   {domain.MaxOutputTokens, l["llmMaxOutputTokens"]},
		"llmMaxRetries":        {domain.Reprompts, l["llmMaxRetries"]},
		"generationTimeoutMs":  {domain.GenerationBudget.Milliseconds(), l["generationTimeoutMs"]},
		"issueFetchTimeoutMs":  {domain.IssueTimeout.Milliseconds(), l["issueFetchTimeoutMs"]},
		"descriptionMaxChars":  {domain.MaxDescription, l["descriptionMaxChars"]},
		"issueBodyMaxChars":    {domain.MaxIssueBody, l["issueBodyMaxChars"]},
		"maxFiles":             {domain.MaxFiles, l["maxFiles"]},
		"maxRangesPerFile":     {domain.MaxRanges, l["maxRangesPerFile"]},
		"maxFindings":          {domain.MaxFindings, l["maxFindings"]},
		"findingTitleMaxChars": {domain.MaxFindingTitle, l["findingTitleMaxChars"]},
		"maxCallers":           {domain.MaxCallers, l["maxCallers"]},
		"summaryMax":           {domain.MaxSummary, l["summaryMax"]},
		"risksMax":             {domain.MaxRisks, l["risksMax"]},
		"riskTitleMax":         {domain.MaxRiskTitle, l["riskTitleMax"]},
		"riskExplanationMax":   {domain.MaxRiskExplanation, l["riskExplanationMax"]},
		"riskKindMax":          {domain.MaxRiskKind, l["riskKindMax"]},
		"focusMax":             {domain.MaxFocus, l["focusMax"]},
		"focusReasonMax":       {domain.MaxFocusReason, l["focusReasonMax"]},
	} {
		if c[0] != c[1] {
			t.Errorf("limit %s = %d, want %d (Node)", name, c[0], c[1])
		}
	}
}

// TestBriefParity_ChangedRanges: the one intentional deviation is a hunk number above the int
// range (12345678901234567890): Node keeps a float of 1.2345678901234567e19, Go's domain.LineRange
// holds an int and saturates at math.MaxInt (line numbers that large do not exist; domain
// atoiSat). The case is asserted as that saturation, not skipped.
func TestBriefParity_ChangedRanges(t *testing.T) {
	for _, c := range pbBrief(t).Input.Ranges {
		t.Run(c.Name, func(t *testing.T) {
			patch := ""
			if c.Patch != nil {
				patch = *c.Patch
			}
			got := pbRangesShape(domain.ChangedRanges(patch))
			want := make([]pbRange, len(c.Ranges))
			copy(want, c.Ranges)
			for i := range want {
				for _, f := range []*float64{&want[i].Start, &want[i].End} {
					if *f >= math.MaxInt64 { // beyond the int range: Go saturates
						*f = float64(math.MaxInt)
					}
				}
			}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ChangedRanges(%q) mismatch (-node +go):\n%s", pbClip(patch, 200), diff)
			}
		})
	}
}

func TestBriefParity_SelectFiles(t *testing.T) {
	for _, c := range pbBrief(t).Input.SelectFiles {
		t.Run(c.Name, func(t *testing.T) {
			var in []domain.ChangedFile
			for _, f := range c.Files {
				in = append(in, f.changed())
			}
			sel := domain.SelectFiles(in, pbBriefRole)
			got := []pbBriefFile{}
			for _, f := range sel.Files {
				got = append(got, pbBriefFile{f.Path, f.Additions, f.Deletions, pbRangesShape(f.Ranges), f.Role})
			}
			want := c.Selected
			for i := range want {
				if want[i].Ranges == nil {
					want[i].Ranges = []pbRange{}
				}
			}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("SelectFiles mismatch (-node +go):\n%s", pbClip(diff, 3000))
			}
			if sel.More != c.More {
				t.Errorf("SelectFiles more = %d, want %d", sel.More, c.More)
			}
		})
	}
}

func TestBriefParity_SelectFindings(t *testing.T) {
	for _, c := range pbBrief(t).Input.SelectFindings {
		t.Run(c.Name, func(t *testing.T) {
			var in []domain.FindingFact
			for _, f := range c.Findings {
				in = append(in, f.fact())
			}
			got := []pbBriefFinding{}
			for _, f := range domain.SelectFindings(in) {
				got = append(got, pbBriefFinding{string(f.Severity), f.File, f.StartLine, f.EndLine, f.Title})
			}
			if diff := cmp.Diff(c.Selected, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("SelectFindings mismatch (-node +go):\n%s", pbClip(diff, 3000))
			}
		})
	}
}

func TestBriefParity_Blast(t *testing.T) {
	for _, c := range pbBrief(t).Input.Blast {
		t.Run(c.Name, func(t *testing.T) {
			usable := domain.UsableBlast(c.Blast.domain())
			if (usable != nil) != c.Usable {
				t.Fatalf("UsableBlast usable = %v, want %v", usable != nil, c.Usable)
			}
			if usable == nil {
				return
			}
			pb := domain.NewPromptBlast(*usable)
			got := map[string]any{"summary": pb.Summary, "changedSymbols": pb.ChangedSymbols, "callers": pb.Callers}
			wantSyms, wantCallers := []domain.BlastSymbol{}, []domain.BlastCaller{}
			for _, s := range c.Prompt.ChangedSymbols {
				wantSyms = append(wantSyms, domain.BlastSymbol(s))
			}
			for _, k := range c.Prompt.Callers {
				wantCallers = append(wantCallers, domain.BlastCaller(k))
			}
			want := map[string]any{"summary": c.Prompt.Summary, "changedSymbols": wantSyms, "callers": wantCallers}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("NewPromptBlast mismatch (-node +go):\n%s", diff)
			}
		})
	}
}

func TestBriefParity_LinkedIssueNumber(t *testing.T) {
	for i, c := range pbBrief(t).Input.LinkedIssue {
		got, ok := domain.LinkedIssueNumber(c.Body)
		switch {
		case c.Number == nil && ok:
			t.Errorf("case %d: LinkedIssueNumber(%v) = %d, want none", i, c.Body, got)
		case c.Number != nil && (!ok || int64(got) != *c.Number):
			t.Errorf("case %d: LinkedIssueNumber(%v) = %d/%v, want %d", i, c.Body, got, ok, *c.Number)
		}
	}
}

func TestBriefParity_MissingInputs(t *testing.T) {
	for _, c := range pbBrief(t).Input.Missing {
		t.Run(c.Name, func(t *testing.T) {
			f := c.Facts
			got := domain.BuildMissingInputs(domain.MissingInputFacts{
				Description: f.Description,
				HasIntent:   f.HasIntent,
				Blast:       f.Blast.domain(),
				Specs: domain.SpecsOutcome{ReviewedAgents: f.Specs.ReviewedAgents, AttachedDocs: f.Specs.AttachedDocs,
					Unreadable: f.Specs.Unreadable, OverBudget: f.Specs.OverBudget},
				Issue: domain.IssueOutcome(f.Issue),
			})
			type item struct{ Input, Reason, Detail string }
			var gotItems, wantItems []item
			for _, m := range got {
				gotItems = append(gotItems, item{string(m.Input), string(m.Reason), m.Detail})
			}
			for _, m := range c.Result {
				wantItems = append(wantItems, item(m))
			}
			if diff := cmp.Diff(wantItems, gotItems); diff != "" {
				t.Errorf("BuildMissingInputs mismatch (-node +go):\n%s", diff)
			}
		})
	}
}

// TestBriefParity_Truncate covers Node's truncateText (trim, then cut with an ellipsis, counted
// in UTF-16 units). A cut inside a surrogate pair keeps a lone high surrogate in Node, which a Go
// string cannot hold: the golden's decoded form and Go's output both carry U+FFFD there.
//
// Intentional difference, asserted by the check of the limits: reviewer.Cut follows the intent
// module's truncateText, whose slice(0, max-1) counts from the end for max < 1 ("ab..." for
// Cut("abc", 0)); the brief's own truncateText clamps at 0 ("..."). Every brief limit is at least 1,
// so the two cannot differ in the brief.
func TestBriefParity_Truncate(t *testing.T) {
	for _, l := range []int{domain.MaxDescription, domain.MaxIssueBody, domain.MaxFindingTitle, domain.MaxSummary,
		domain.MaxRiskKind, domain.MaxRiskTitle, domain.MaxRiskExplanation, domain.MaxFocusReason} {
		if l < 1 {
			t.Errorf("a brief limit is %d: reviewer.Cut and Node's brief truncateText differ below 1", l)
		}
	}
	for i, c := range pbBrief(t).Input.Truncate {
		if c.Max < 1 {
			continue
		}
		if got := reviewer.Cut(c.Text, c.Max); got != c.Result {
			t.Errorf("case %d: Cut(%q, %d) = %q, want %q", i, c.Text, c.Max, got, c.Result)
		}
	}
}

func TestBriefParity_Prompt(t *testing.T) {
	g := pbBrief(t)
	for _, c := range g.Prompt {
		t.Run(c.Name, func(t *testing.T) {
			if !c.SystemIsTheNodeConstant {
				t.Fatal("golden does not vouch for the system prompt")
			}
			in := c.Input.domain(t)
			if len(c.Sources) != len(domain.Sources(in)) {
				t.Fatalf("Sources has %d blocks, Node %d", len(domain.Sources(in)), len(c.Sources))
			}
			for i, s := range domain.Sources(in) {
				w := c.Sources[i]
				if w.Heading != s.Heading || w.Label != string(s.Label) {
					t.Errorf("source %d = %q/%q, want %q/%q", i, s.Heading, s.Label, w.Heading, w.Label)
				}
				pbCheckText(t, fmt.Sprintf("source %d (%s) content", i, s.Label), w.Content, s.Content)
			}
			pbCheckText(t, "UserMessage", c.UserMessage, domain.UserMessage(in))
			if got := domain.EstimateInputTokens(in); got != c.Estimate {
				t.Errorf("EstimateInputTokens = %d, want %d", got, c.Estimate)
			}

			tr := domain.TrimToBudget(in, c.MaxTokens)
			want := c.Trim
			if tr.DroppedSpecs != want.DroppedSpecs || tr.DroppedFiles != want.DroppedFiles || tr.EstTokens != want.EstTokens || tr.Input.MoreFiles != want.MoreFiles {
				t.Errorf("TrimToBudget(max %d) = specs -%d files -%d est %d more %d, want specs -%d files -%d est %d more %d",
					c.MaxTokens, tr.DroppedSpecs, tr.DroppedFiles, tr.EstTokens, tr.Input.MoreFiles,
					want.DroppedSpecs, want.DroppedFiles, want.EstTokens, want.MoreFiles)
			}
			var files []string
			for _, f := range tr.Input.Files {
				files = append(files, f.Path)
			}
			if diff := cmp.Diff(want.Files, files, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("trimmed files mismatch (-node +go):\n%s", diff)
			}
			if diff := cmp.Diff(want.Specs, domain.SpecPaths(tr.Input), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("trimmed specs mismatch (-node +go):\n%s", diff)
			}
			wantUser := want.UserMessage
			if wantUser.b == nil && wantUser.s == "$same" {
				if tr.DroppedSpecs+tr.DroppedFiles != 0 {
					t.Fatalf("golden says $same but the trim dropped %d specs and %d files", tr.DroppedSpecs, tr.DroppedFiles)
				}
				wantUser = c.UserMessage
			}
			pbCheckText(t, "trimmed UserMessage", wantUser, domain.UserMessage(tr.Input))

			// The content-free prompt log sections of the trimmed input.
			type section struct {
				Name, Origin, Trust  string
				Chars, Tokens, Items int
			}
			var wantSecs, gotSecs []section
			for _, s := range want.Sections {
				wantSecs = append(wantSecs, section{s.Name, s.Source, s.Trust, s.Chars, s.Tokens, s.Items})
			}
			for _, s := range domain.LogSections(tr.Input) {
				trust := "untrusted"
				if s.Trusted {
					trust = "trusted"
				}
				gotSecs = append(gotSecs, section{s.Name, s.Origin, trust, pbUnits(s.Text), reviewer.EstimateTokens(s.Text), s.Items})
			}
			if diff := cmp.Diff(wantSecs, gotSecs); diff != "" {
				t.Errorf("LogSections mismatch (-node +go):\n%s", diff)
			}
		})
	}
}

func TestBriefParity_NormalizeBrief(t *testing.T) {
	g := pbBrief(t)
	for _, c := range g.Normalize {
		t.Run(c.Name, func(t *testing.T) {
			var files []domain.ChangedFile
			for _, f := range c.Facts.Files {
				files = append(files, domain.ChangedFile{Path: f.Path, Ranges: pbRanges(f.Ranges)})
			}
			var findings []domain.FindingFact
			for _, f := range c.Facts.Findings {
				findings = append(findings, domain.FindingFact{File: f.File, StartLine: f.StartLine, EndLine: f.EndLine})
			}
			var raw domain.LLMOutput
			raw.Summary = c.Raw.Summary
			for _, r := range c.Raw.Risks {
				raw.Risks = append(raw.Risks, domain.LLMRisk{Kind: r.Kind, Title: r.Title, Explanation: r.Explanation, Severity: r.Severity, FileRefs: r.FileRefs})
			}
			for _, f := range c.Raw.Focus {
				raw.ReviewFocus = append(raw.ReviewFocus, domain.LLMFocus{File: f.File, Line: f.Line, Reason: f.Reason})
			}
			n := domain.NormalizeBrief(raw, domain.NewGroundingFacts(files, findings, c.Facts.Blast.domain()))

			risks := []map[string]any{}
			for _, r := range n.Risks {
				risks = append(risks, map[string]any{"kind": r.Kind, "title": r.Title, "explanation": r.Explanation, "severity": string(r.Severity), "file_refs": r.FileRefs})
			}
			focus := []map[string]any{}
			for _, f := range n.ReviewFocus {
				focus = append(focus, map[string]any{"file": f.File, "line": f.Line, "reason": f.Reason})
			}
			got := pbGeneric(t, map[string]any{
				"summary": n.Summary, "risks": risks, "review_focus": focus,
				"dropped": map[string]any{"risks": n.Dropped.Risks, "review_focus": n.Dropped.ReviewFocus, "risk_refs": n.Dropped.RiskRefs},
			}).(map[string]any)
			want := c.Result

			// Intentional deviation: a line above 2^53-1 is clamped (the stored line is an integer).
			wantFocus, _ := want["review_focus"].([]any)
			gotFocus, _ := got["review_focus"].([]any)
			for i := range wantFocus {
				wf := wantFocus[i].(map[string]any)
				if line, _ := wf["line"].(float64); line > math.MaxInt64>>10-1 && i < len(gotFocus) {
					gf := gotFocus[i].(map[string]any)
					if gf["line"] != float64(1<<53-1) {
						t.Errorf("review_focus[%d].line = %v, want the clamp 2^53-1 for Node's %v", i, gf["line"], line)
					}
					wf["line"], gf["line"] = nil, nil
				}
			}
			pbDiff(t, "NormalizeBrief("+c.Name+")", want, got)
		})
	}
}

// TestBriefParity_IsStale: Go's IsStale has the one current prompt version (domain.PromptVersion),
// so the golden case that passes another current version ("v2") cannot be run; it is checked to
// be a case Go cannot reach (its version differs from the constant) and skipped otherwise.
func TestBriefParity_IsStale(t *testing.T) {
	for i, c := range pbBrief(t).Stale {
		if c.CurrentPrompt != nil {
			if *c.CurrentPrompt == domain.PromptVersion {
				t.Errorf("case %d: golden passes the current version %q, which Go has built in, but it is not a plain case", i, *c.CurrentPrompt)
			}
			continue
		}
		if got := domain.IsStale(c.Stored.HeadSHA, c.Stored.PromptVersion, c.Current); got != c.Result {
			t.Errorf("case %d: IsStale(%q, %q, %q) = %v, want %v", i, c.Stored.HeadSHA, c.Stored.PromptVersion, c.Current, got, c.Result)
		}
	}
}

// TestBriefParity_FocusLineClamp asserts the intentional deviation: a focus line above 2^53-1 that
// no fact grounds is kept by Node as a float ("no ranges keeps the requested line" in the golden,
// there with 3) and clamped by Go, whose stored line is an integer (spec 7.9.5).
func TestBriefParity_FocusLineClamp(t *testing.T) {
	facts := domain.NewGroundingFacts([]domain.ChangedFile{{Path: "src/nodiff.ts"}}, nil, nil)
	for _, line := range []float64{1e300, 9007199254740993, 1 << 53} {
		n := domain.NormalizeBrief(domain.LLMOutput{ReviewFocus: []domain.LLMFocus{{File: "src/nodiff.ts", Line: line, Reason: "r"}}}, facts)
		if len(n.ReviewFocus) != 1 || n.ReviewFocus[0].Line != 1<<53-1 {
			t.Errorf("NormalizeBrief focus line %v = %+v, want one item clamped to %d", line, n.ReviewFocus, int64(1<<53-1))
		}
	}
}
