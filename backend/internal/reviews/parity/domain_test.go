package parity_test

// S23 (AC4041, NFR4005): the pure engine rules and the reviews domain against the goldens the
// Node implementation produced: diff parsing and reconstruction, slicing, oversize splitting,
// mode selection, reduce, grounding, scope policy, score, blockers, prompt messages and section
// metadata, the prompt helpers, and the multi-agent read model.

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	reviews "dev-digest-go/backend/internal/reviews/domain"
)

func TestDiffParity_ParseUnifiedDiff(t *testing.T) {
	var g struct {
		Parse []struct {
			Name string `json:"name"`
			Raw  string `json:"raw"`
			Diff struct {
				Raw   string         `json:"raw"`
				Files []diffFileWire `json:"files"`
			} `json:"diff"`
		} `json:"parse"`
	}
	loadGolden(t, "diff", &g)
	if len(g.Parse) == 0 {
		t.Fatal("diff.json has no parse cases")
	}
	for _, tc := range g.Parse {
		t.Run(tc.Name, func(t *testing.T) {
			got := reviewer.ParseUnifiedDiff(tc.Raw)
			if got.Raw != tc.Diff.Raw {
				t.Errorf("ParseUnifiedDiff(%q).Raw = %q, want %q", tc.Raw, got.Raw, tc.Diff.Raw)
			}
			if diff := cmp.Diff(domainFiles(tc.Diff.Files), got.Files, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ParseUnifiedDiff(%q).Files mismatch (-want +got):\n%s", tc.Raw, diff)
			}
		})
	}
}

func TestDiffParity_RawDiffOfStoredFiles(t *testing.T) {
	var g struct {
		Reconstruct []struct {
			Name string `json:"name"`
			Rows []struct {
				Path  string  `json:"path"`
				Patch *string `json:"patch"`
			} `json:"rows"`
			Raw  string `json:"raw"`
			Diff struct {
				Files []diffFileWire `json:"files"`
			} `json:"diff"`
		} `json:"reconstruct"`
	}
	loadGolden(t, "diff", &g)
	if len(g.Reconstruct) == 0 {
		t.Fatal("diff.json has no reconstruct cases")
	}
	for _, tc := range g.Reconstruct {
		t.Run(tc.Name, func(t *testing.T) {
			var rows []reviewer.PatchFile
			for _, r := range tc.Rows {
				p := ""
				if r.Patch != nil {
					p = *r.Patch
				}
				rows = append(rows, reviewer.PatchFile{Path: r.Path, Patch: p})
			}
			raw := reviewer.RawDiff(rows)
			if raw != tc.Raw {
				t.Errorf("RawDiff(%v) = %q, want %q", tc.Rows, raw, tc.Raw)
			}
			if diff := cmp.Diff(domainFiles(tc.Diff.Files), reviewer.ParseUnifiedDiff(raw).Files, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ParseUnifiedDiff(RawDiff(%v)) mismatch (-want +got):\n%s", tc.Rows, diff)
			}
		})
	}
}

func TestChunksParity_SliceDiff(t *testing.T) {
	var g struct {
		Slice []struct {
			Name       string   `json:"name"`
			Raw        string   `json:"raw"`
			FileLabels []string `json:"fileLabels"`
			Slices     []struct {
				Path string `json:"path"`
				Text string `json:"text"`
			} `json:"slices"`
		} `json:"slice"`
	}
	loadGolden(t, "chunks", &g)
	if len(g.Slice) == 0 {
		t.Fatal("chunks.json has no slice cases")
	}
	for _, tc := range g.Slice {
		t.Run(tc.Name, func(t *testing.T) {
			d := reviewer.ParseUnifiedDiff(tc.Raw)
			var labels []string
			for _, c := range reviewer.FileChunks(d) {
				labels = append(labels, c.Label)
			}
			if diff := cmp.Diff(tc.FileLabels, labels, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("FileChunks(%q) labels mismatch (-want +got):\n%s", tc.Raw, diff)
			}
			for _, s := range tc.Slices {
				if got := reviewer.SliceDiff(d, s.Path); got != s.Text {
					t.Errorf("SliceDiff(%q, %q) = %q, want %q", tc.Raw, s.Path, got, s.Text)
				}
			}
		})
	}
}

func TestChunksParity_SplitOversizeDiff(t *testing.T) {
	var g struct {
		Split []struct {
			Name      string     `json:"name"`
			Max       int        `json:"max"`
			Raw       text       `json:"raw"`
			Parts     []text     `json:"parts"`
			Truncated bool       `json:"truncated"`
			Synth     *synthSpec `json:"synth"`
			PadTo     int        `json:"padTo"`
		} `json:"split"`
	}
	loadGolden(t, "chunks", &g)
	if len(g.Split) == 0 {
		t.Fatal("chunks.json has no split cases")
	}
	for _, tc := range g.Split {
		t.Run(tc.Name, func(t *testing.T) {
			raw := rawOf(t, tc.Raw, tc.Synth, tc.PadTo)
			got := reviewer.SplitOversizeDiff(raw, tc.Max)
			if got.Truncated != tc.Truncated {
				t.Errorf("SplitOversizeDiff(%s, %d).Truncated = %v, want %v", describeGot(raw), tc.Max, got.Truncated, tc.Truncated)
			}
			if len(got.Parts) != len(tc.Parts) {
				t.Fatalf("SplitOversizeDiff(%s, %d) made %d parts, want %d", describeGot(raw), tc.Max, len(got.Parts), len(tc.Parts))
			}
			for i, want := range tc.Parts {
				checkText(t, fmt.Sprintf("part %d", i), want, got.Parts[i])
			}
		})
	}
}

// The engine cases named mode_* assert only the mode and the chunk labels (README).
func TestEngineParity_ModeSelection(t *testing.T) {
	var g struct {
		Cases []struct {
			Name  string `json:"name"`
			Input struct {
				Strategy string `json:"strategy"`
			} `json:"input"`
			DiffRaw   text       `json:"diffRaw"`
			DiffSynth *synthSpec `json:"diffSynth"`
			Outcome   *struct {
				Mode   string `json:"mode"`
				Chunks []struct {
					Label string `json:"label"`
				} `json:"chunks"`
			} `json:"outcome"`
		} `json:"cases"`
	}
	loadGolden(t, "engine", &g)
	n := 0
	for _, tc := range g.Cases {
		if len(tc.Name) < 5 || tc.Name[:5] != "mode_" {
			continue
		}
		n++
		t.Run(tc.Name, func(t *testing.T) {
			d := reviewer.ParseUnifiedDiff(rawOf(t, tc.DiffRaw, tc.DiffSynth, 0))
			mode := reviewer.SelectMode(reviewer.Strategy(tc.Input.Strategy), d, reviewer.MapThresholdLines)
			chunks := reviewer.ChunksFor(mode, d)
			if got := reviewer.EffectiveMode(mode, len(chunks)); string(got) != tc.Outcome.Mode {
				t.Errorf("mode = %q, want %q", got, tc.Outcome.Mode)
			}
			var got, want []string
			for _, c := range chunks {
				got = append(got, c.Label)
			}
			for _, c := range tc.Outcome.Chunks {
				want = append(want, c.Label)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("chunk labels mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if n == 0 {
		t.Fatal("engine.json has no mode_* cases")
	}
}

func TestReduceParity_ReduceReviews(t *testing.T) {
	var g struct {
		Reduce []struct {
			Name     string       `json:"name"`
			Partials []wireReview `json:"partials"`
			Merged   wireReview   `json:"merged"`
		} `json:"reduce"`
	}
	loadGolden(t, "reduce", &g)
	if len(g.Reduce) == 0 {
		t.Fatal("reduce.json has no reduce cases")
	}
	for _, tc := range g.Reduce {
		t.Run(tc.Name, func(t *testing.T) {
			var in []reviewer.Review
			for _, p := range tc.Partials {
				in = append(in, p.domain())
			}
			got := reviewer.ReduceReviews(in)
			if diff := cmp.Diff(tc.Merged.domain(), got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ReduceReviews(%d partials) mismatch (-want +got):\n%s", len(in), diff)
			}
		})
	}
}

func TestReduceParity_ScoreAndBlockers(t *testing.T) {
	var g struct {
		Score []struct {
			Name       string   `json:"name"`
			Severities []string `json:"severities"`
			Repeat     *struct {
				Severity string `json:"severity"`
				Count    int    `json:"count"`
			} `json:"repeat"`
			Score int `json:"score"`
		} `json:"score"`
		Blockers []struct {
			Name       string         `json:"name"`
			Severities []string       `json:"severities"`
			Counts     map[string]int `json:"counts"`
		} `json:"blockers"`
	}
	loadGolden(t, "reduce", &g)
	findings := func(sev []string) []reviewer.Finding {
		var out []reviewer.Finding
		for _, s := range sev {
			out = append(out, reviewer.Finding{Severity: reviewer.Severity(s)})
		}
		return out
	}
	for _, tc := range g.Score {
		t.Run("score/"+tc.Name, func(t *testing.T) {
			sev := tc.Severities
			if tc.Repeat != nil {
				for range tc.Repeat.Count {
					sev = append(sev, tc.Repeat.Severity)
				}
			}
			if got := reviewer.ScoreFromFindings(findings(sev)); got != tc.Score {
				t.Errorf("ScoreFromFindings(%v) = %d, want %d", sev, got, tc.Score)
			}
		})
	}
	for _, tc := range g.Blockers {
		t.Run("blockers/"+tc.Name, func(t *testing.T) {
			for gate, want := range tc.Counts {
				if got := reviewer.CountBlockers(findings(tc.Severities), reviewer.FailOn(gate)); got != want {
					t.Errorf("CountBlockers(%v, %q) = %d, want %d", tc.Severities, gate, got, want)
				}
			}
		})
	}
}

func TestGroundingParity(t *testing.T) {
	var g struct {
		Diffs map[string]struct {
			Raw string `json:"raw"`
		} `json:"diffs"`
		Ground []struct {
			Name        string        `json:"name"`
			Diff        string        `json:"diff"`
			Findings    []wireFinding `json:"findings"`
			KeptIndexes []int         `json:"keptIndexes"`
			Dropped     []struct {
				Index  int    `json:"index"`
				Reason string `json:"reason"`
			} `json:"dropped"`
			Summary string `json:"summary"`
		} `json:"ground"`
		Scope []struct {
			Name     string        `json:"name"`
			Intent   any           `json:"intent"`
			Findings []wireFinding `json:"findings"`
			Result   struct {
				Findings                []wireFinding `json:"findings"`
				OutOfScopeCount         int           `json:"outOfScopeCount"`
				OutOfScopeCriticalCount int           `json:"outOfScopeCriticalCount"`
			} `json:"result"`
		} `json:"scope"`
	}
	loadGolden(t, "grounding", &g)
	if len(g.Ground) == 0 {
		t.Fatal("grounding.json has no ground cases")
	}
	for _, tc := range g.Ground {
		t.Run(tc.Name, func(t *testing.T) {
			d, ok := g.Diffs[tc.Diff]
			if !ok {
				t.Fatalf("diff %q is not in the golden", tc.Diff)
			}
			in := domainFindings(tc.Findings)
			got := reviewer.GroundFindings(in, reviewer.ParseUnifiedDiff(d.Raw))

			var wantKept []reviewer.Finding
			for _, i := range tc.KeptIndexes {
				wantKept = append(wantKept, in[i])
			}
			if diff := cmp.Diff(wantKept, got.Kept, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("GroundFindings kept mismatch (-want +got):\n%s", diff)
			}
			var wantDropped []reviewer.Dropped
			for _, d := range tc.Dropped {
				wantDropped = append(wantDropped, reviewer.Dropped{Finding: in[d.Index], Reason: d.Reason})
			}
			if diff := cmp.Diff(wantDropped, got.Dropped, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("GroundFindings dropped mismatch (-want +got):\n%s", diff)
			}
			if s := got.Summary(); s != tc.Summary {
				t.Errorf("GroundFindings(...).Summary() = %q, want %q", s, tc.Summary)
			}
		})
	}
	// Both switch states: a row with an intent is the switch-on case (spec 7.5, D5017), a row without
	// one the switch-off case that Phase 4 pinned.
	if len(g.Scope) < 2 {
		t.Fatalf("grounding.json has %d scope cases, want the no-intent and the intent row", len(g.Scope))
	}
	withIntent := 0
	for _, tc := range g.Scope {
		hasIntent := tc.Intent != nil
		if hasIntent {
			withIntent++
		}
		t.Run("scope/"+tc.Name, func(t *testing.T) {
			got := reviewer.ApplyScopePolicy(domainFindings(tc.Findings), hasIntent)
			if diff := cmp.Diff(domainFindings(tc.Result.Findings), got.Findings, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ApplyScopePolicy(hasIntent=%v) findings mismatch (-want +got):\n%s", hasIntent, diff)
			}
			if got.OutOfScope != tc.Result.OutOfScopeCount || got.OutOfScopeCritical != tc.Result.OutOfScopeCriticalCount {
				t.Errorf("ApplyScopePolicy(hasIntent=%v) counts = %d/%d critical, want %d/%d critical", hasIntent,
					got.OutOfScope, got.OutOfScopeCritical, tc.Result.OutOfScopeCount, tc.Result.OutOfScopeCriticalCount)
			}
		})
	}
	if withIntent == 0 {
		t.Error("grounding.json has no scope row with an intent")
	}
}

type promptParts struct {
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
}

func (p promptParts) domain() reviewer.PromptParts {
	out := reviewer.PromptParts{
		System: p.System, Task: p.Task, PRDescription: p.PRDescription, Skills: p.Skills,
		RepoMap: p.RepoMap, Callers: p.Callers, Diff: p.Diff,
	}
	for _, s := range p.Specs {
		out.Specs = append(out.Specs, reviewer.ProjectDoc{Path: s.Path, Text: s.Text})
	}
	return out
}

func TestPromptParity_AssemblePrompt(t *testing.T) {
	var g struct {
		Assemble []struct {
			Name     string      `json:"name"`
			Parts    promptParts `json:"parts"`
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
			Sections []struct {
				Name      string `json:"name"`
				Source    string `json:"source"`
				Role      string `json:"role"`
				Trust     string `json:"trust"`
				Chars     int    `json:"chars"`
				Tokens    int    `json:"tokens"`
				Items     *int   `json:"items"`
				Truncated *bool  `json:"truncated"`
			} `json:"sections"`
		} `json:"assemble"`
	}
	loadGolden(t, "prompt", &g)
	if len(g.Assemble) == 0 {
		t.Fatal("prompt.json has no assemble cases")
	}
	for _, tc := range g.Assemble {
		t.Run(tc.Name, func(t *testing.T) {
			got := reviewer.AssemblePrompt(tc.Parts.domain())

			var wantMessages []reviewer.Message
			for _, m := range tc.Messages {
				wantMessages = append(wantMessages, reviewer.Message{Role: reviewer.Role(m.Role), Content: m.Content})
			}
			if diff := cmp.Diff(wantMessages, got.Messages); diff != "" {
				t.Errorf("AssemblePrompt messages mismatch (-want +got):\n%s", diff)
			}

			// Node keeps an empty callers/repo map as "", Go reads "" as no section: nil.
			blankNil := func(p *string) *string {
				if p != nil && *p == "" {
					return nil
				}
				return p
			}
			wantAssembly := reviewer.Assembly{
				System: tc.Assembly.System, Skills: tc.Assembly.Skills, Specs: tc.Assembly.Specs,
				Callers: blankNil(tc.Assembly.Callers), RepoMap: blankNil(tc.Assembly.RepoMap),
				PRDescription: tc.Assembly.PRDescription, User: tc.Assembly.User,
			}
			if diff := cmp.Diff(wantAssembly, got.Assembly); diff != "" {
				t.Errorf("AssemblePrompt assembly mismatch (-want +got):\n%s", diff)
			}

			var wantSections []reviewer.SectionMeta
			for _, s := range tc.Sections {
				wantSections = append(wantSections, reviewer.SectionMeta{
					Name: reviewer.SectionName(s.Name), Source: reviewer.SectionSource(s.Source),
					Role: reviewer.Role(s.Role), Trust: reviewer.Trust(s.Trust),
					Chars: s.Chars, Tokens: s.Tokens, Items: s.Items, Truncated: s.Truncated,
				})
			}
			if diff := cmp.Diff(wantSections, got.Sections); diff != "" {
				t.Errorf("AssemblePrompt sections mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// EC4015: a PR description cut inside a surrogate pair drops the pair (hand-written expectation).
func TestPromptParity_SurrogateCut(t *testing.T) {
	var h struct {
		Input struct {
			Prefix struct {
				Repeat []any `json:"$repeat"`
			} `json:"prefix"`
			Astral string `json:"astral"`
			Suffix string `json:"suffix"`
		} `json:"input"`
		InputUnits int `json:"inputUnits"`
		GoExpected struct {
			Description struct {
				Repeat []any `json:"$repeat"`
			} `json:"description"`
			DescriptionUnits int    `json:"descriptionUnits"`
			TruncatedFlag    bool   `json:"truncatedFlag"`
			SectionChars     int    `json:"sectionChars"`
			SectionTokens    int    `json:"sectionTokens"`
			SectionText      string `json:"sectionText"`
		} `json:"goExpected"`
	}
	loadHandwritten(t, "prompt_surrogate", &h)
	repeat := func(r []any) string {
		s, n := r[0].(string), int(r[1].(float64))
		out := make([]byte, 0, len(s)*n)
		for range n {
			out = append(out, s...)
		}
		return string(out)
	}
	body := repeat(h.Input.Prefix.Repeat) + h.Input.Astral + h.Input.Suffix
	if utf16Len(body) != h.InputUnits {
		t.Fatalf("the input is %d units, the file says %d", utf16Len(body), h.InputUnits)
	}
	want := repeat(h.GoExpected.Description.Repeat)

	got := reviewer.AssemblePrompt(reviewer.PromptParts{System: "S", PRDescription: body, Diff: "d"})
	if got.Assembly.PRDescription == nil || *got.Assembly.PRDescription != want {
		t.Errorf("Assembly.PRDescription = %s, want %d 'a' units", describeGot(deref(got.Assembly.PRDescription)), h.GoExpected.DescriptionUnits)
	}
	if utf16Len(want) != h.GoExpected.DescriptionUnits {
		t.Errorf("expected description has %d units, the file says %d", utf16Len(want), h.GoExpected.DescriptionUnits)
	}
	var sec *reviewer.SectionMeta
	for i := range got.Sections {
		if got.Sections[i].Name == reviewer.SectionPRDescription {
			sec = &got.Sections[i]
		}
	}
	if sec == nil {
		t.Fatal("no pr_description section")
	}
	if sec.Chars != h.GoExpected.SectionChars || sec.Tokens != h.GoExpected.SectionTokens {
		t.Errorf("pr_description chars/tokens = %d/%d, want %d/%d", sec.Chars, sec.Tokens, h.GoExpected.SectionChars, h.GoExpected.SectionTokens)
	}
	if sec.Truncated == nil || *sec.Truncated != h.GoExpected.TruncatedFlag {
		t.Errorf("pr_description truncated = %v, want %v", sec.Truncated, h.GoExpected.TruncatedFlag)
	}
	wantText := "## PR description\n<untrusted source=\"pr-description\">\n" + want + "\n</untrusted>"
	if h.GoExpected.SectionText != "## PR description\n<untrusted source=\"pr-description\">\n{description}\n</untrusted>" {
		t.Fatalf("the section template in the file changed: %q", h.GoExpected.SectionText)
	}
	if user := got.Messages[1].Content; !strings.Contains(user, wantText) {
		t.Errorf("the user message does not contain the PR description section built from the cut text")
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestPromptParity_Helpers(t *testing.T) {
	var g struct {
		Wrap []struct {
			Label   string `json:"label"`
			Content string `json:"content"`
			Out     string `json:"out"`
		} `json:"wrap"`
		ContextLabel []struct {
			Path  string `json:"path"`
			Label string `json:"label"`
		} `json:"contextLabel"`
		EstimateTokens []struct {
			Text   string `json:"text"`
			Units  int    `json:"units"`
			Tokens int    `json:"tokens"`
		} `json:"estimateTokens"`
		TaskLine []struct {
			Pull struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
				Author string `json:"author"`
			} `json:"pull"`
			Line string `json:"line"`
		} `json:"taskLine"`
		RankNote []struct {
			Hot     int    `json:"hot"`
			Changed int    `json:"changed"`
			Note    string `json:"note"`
		} `json:"rankNote"`
		HotFiles []struct {
			Rows []struct {
				FilePath   string  `json:"filePath"`
				Percentile float64 `json:"percentile"`
			} `json:"rows"`
			Hot []struct {
				FilePath   string  `json:"filePath"`
				Percentile float64 `json:"percentile"`
			} `json:"hot"`
		} `json:"hotFiles"`
		CallersDigest []struct {
			Rows []struct {
				File      string `json:"file"`
				Symbol    string `json:"symbol"`
				Signature string `json:"signature"`
			} `json:"rows"`
			Digest *string `json:"digest"`
		} `json:"callersDigest"`
		SkillBlock []struct {
			Skill struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Body        string `json:"body"`
			} `json:"skill"`
			Block string `json:"block"`
		} `json:"skillBlock"`
		SkillsLogLine []struct {
			Skills []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Version int    `json:"version"`
			} `json:"skills"`
			Tokens int    `json:"tokens"`
			Repeat bool   `json:"repeat"`
			Line   string `json:"line"`
			Used   []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Version int    `json:"version"`
			} `json:"used"`
		} `json:"skillsLogLine"`
	}
	loadGolden(t, "prompt", &g)

	t.Run("wrapUntrusted", func(t *testing.T) {
		if len(g.Wrap) == 0 {
			t.Fatal("no wrap cases")
		}
		for _, c := range g.Wrap {
			if got := reviewer.WrapUntrusted(c.Label, c.Content); got != c.Out {
				t.Errorf("WrapUntrusted(%q, %q) = %q, want %q", c.Label, c.Content, got, c.Out)
			}
		}
	})
	t.Run("contextLabel", func(t *testing.T) {
		for _, c := range g.ContextLabel {
			if got := reviewer.ContextLabel(c.Path); got != c.Label {
				t.Errorf("ContextLabel(%q) = %q, want %q", c.Path, got, c.Label)
			}
		}
	})
	t.Run("estimateTokens", func(t *testing.T) {
		for _, c := range g.EstimateTokens {
			if got := reviewer.Len16(c.Text); got != c.Units {
				t.Errorf("reviewer.Len16(%q) = %d, want %d", c.Text, got, c.Units)
			}
			if got := reviewer.EstimateTokens(c.Text); got != c.Tokens {
				t.Errorf("reviewer.EstimateTokens(%q) = %d, want %d", c.Text, got, c.Tokens)
			}
			if got := reviews.UTF16Units(c.Text); got != c.Units {
				t.Errorf("reviews.UTF16Units(%q) = %d, want %d", c.Text, got, c.Units)
			}
			if got := reviews.EstimateTokens(c.Text); got != c.Tokens {
				t.Errorf("reviews.EstimateTokens(%q) = %d, want %d", c.Text, got, c.Tokens)
			}
		}
	})
	t.Run("taskLine", func(t *testing.T) {
		if len(g.TaskLine) == 0 {
			t.Fatal("no taskLine cases")
		}
		for _, c := range g.TaskLine {
			if got := reviews.TaskLine(c.Pull.Number, c.Pull.Title, c.Pull.Author); got != c.Line {
				t.Errorf("TaskLine(%d, %q, %q) = %q, want %q", c.Pull.Number, c.Pull.Title, c.Pull.Author, got, c.Line)
			}
		}
	})
	t.Run("rankNote", func(t *testing.T) {
		for _, c := range g.RankNote {
			if got := reviews.RankNote(c.Hot, c.Changed); got != c.Note {
				t.Errorf("RankNote(%d, %d) = %q, want %q", c.Hot, c.Changed, got, c.Note)
			}
		}
	})
	t.Run("hotFiles", func(t *testing.T) {
		for _, c := range g.HotFiles {
			// file_rank percentiles are whole numbers in Go: a row such as 94.9999 has no
			// representation, so the rows are compared at their floor, which keeps the >= 95 rule.
			var rows []reviews.PathRank
			for _, r := range c.Rows {
				rows = append(rows, reviews.PathRank{Path: r.FilePath, Percentile: int(math.Floor(r.Percentile))})
			}
			var want []reviews.PathRank
			for _, r := range c.Hot {
				want = append(want, reviews.PathRank{Path: r.FilePath, Percentile: int(math.Floor(r.Percentile))})
			}
			if diff := cmp.Diff(want, reviews.HotFiles(rows), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("HotFiles(%v) mismatch (-want +got):\n%s", rows, diff)
			}
		}
	})
	t.Run("callersDigest", func(t *testing.T) {
		for _, c := range g.CallersDigest {
			var rows []reviews.CallerSignature
			for _, r := range c.Rows {
				rows = append(rows, reviews.CallerSignature{File: r.File, Symbol: r.Symbol, Signature: r.Signature})
			}
			want := ""
			if c.Digest != nil {
				want = *c.Digest
			}
			if got := reviews.CallersDigest(rows); got != want {
				t.Errorf("CallersDigest(%v) = %q, want %q", rows, got, want)
			}
		}
	})
	t.Run("skillBlock", func(t *testing.T) {
		if len(g.SkillBlock) == 0 {
			t.Fatal("no skillBlock cases")
		}
		for _, c := range g.SkillBlock {
			s := reviews.ReviewSkill{Name: c.Skill.Name, Description: c.Skill.Description, Body: c.Skill.Body}
			if got := reviews.SkillBlock(s); got != c.Block {
				t.Errorf("SkillBlock(%q) = %q, want %q", c.Skill.Name, got, c.Block)
			}
			if got := reviewer.RenderSkillBlock(c.Skill.Name, c.Skill.Description, c.Skill.Body); got != c.Block {
				t.Errorf("RenderSkillBlock(%q) = %q, want %q", c.Skill.Name, got, c.Block)
			}
		}
	})
	t.Run("skillsLogLine", func(t *testing.T) {
		if len(g.SkillsLogLine) == 0 {
			t.Fatal("no skillsLogLine cases")
		}
		for _, c := range g.SkillsLogLine {
			var skills []reviews.ReviewSkill
			var wantUsed []reviews.SkillUsed
			for _, s := range c.Skills {
				skills = append(skills, reviews.ReviewSkill{ID: s.ID, Name: s.Name, Version: s.Version})
			}
			for _, s := range c.Used {
				wantUsed = append(wantUsed, reviews.SkillUsed{ID: s.ID, Name: s.Name, Version: s.Version})
			}
			if got := reviews.SkillsLogLine(skills, c.Tokens, c.Repeat); got != c.Line {
				t.Errorf("SkillsLogLine(%v, %d, %v) = %q, want %q", skills, c.Tokens, c.Repeat, got, c.Line)
			}
			if diff := cmp.Diff(wantUsed, reviews.SkillsUsed(skills), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("SkillsUsed(%v) mismatch (-want +got):\n%s", skills, diff)
			}
		}
	})
}

type multiColumn struct {
	RunID      string       `json:"run_id"`
	AgentID    string       `json:"agent_id"`
	AgentName  string       `json:"agent_name"`
	Provider   *string      `json:"provider"`
	Model      *string      `json:"model"`
	Status     string       `json:"status"`
	Error      *string      `json:"error"`
	Verdict    *string      `json:"verdict"`
	Score      *int         `json:"score"`
	Summary    *string      `json:"summary"`
	DurationMS *int         `json:"duration_ms"`
	CostUSD    *float64     `json:"cost_usd"`
	Findings   []wireRecord `json:"findings"`
}

// wireRecord is a stored finding as the API reads it.
type wireRecord struct {
	ID                 string   `json:"id"`
	Severity           string   `json:"severity"`
	Category           string   `json:"category"`
	Title              string   `json:"title"`
	File               string   `json:"file"`
	StartLine          int      `json:"start_line"`
	EndLine            int      `json:"end_line"`
	Rationale          string   `json:"rationale"`
	Suggestion         *string  `json:"suggestion"`
	Confidence         float64  `json:"confidence"`
	Kind               string   `json:"kind"`
	TrifectaComponents []string `json:"trifecta_components"`
	Skill              *string  `json:"skill"`
	OutOfScope         bool     `json:"out_of_scope"`
	ReviewID           string   `json:"review_id"`
	MemoryID           *string  `json:"memory_id"`
}

func (r wireRecord) domain() reviews.FindingRecord {
	return reviews.FindingRecord{
		ID: r.ID, Severity: reviewer.Severity(r.Severity), Category: reviewer.Category(r.Category), Title: r.Title,
		File: r.File, StartLine: r.StartLine, EndLine: r.EndLine, Rationale: r.Rationale, Suggestion: r.Suggestion,
		Confidence: r.Confidence, Kind: reviewer.Kind(r.Kind), TrifectaComponents: r.TrifectaComponents, Skill: r.Skill,
		OutOfScope: r.OutOfScope, ReviewID: r.ReviewID, MemoryID: r.MemoryID,
	}
}

func (c multiColumn) domain() reviews.AgentColumn {
	out := reviews.AgentColumn{
		RunID: c.RunID, AgentID: c.AgentID, AgentName: c.AgentName, Provider: c.Provider, Model: c.Model,
		Status: reviews.RunStatus(c.Status), Error: c.Error, Verdict: c.Verdict, Score: c.Score, Summary: c.Summary,
		DurationMS: c.DurationMS, CostUSD: c.CostUSD,
	}
	for _, f := range c.Findings {
		out.Findings = append(out.Findings, f.domain())
	}
	return out
}

type multiTake struct {
	AgentID    string   `json:"agent_id"`
	Persona    string   `json:"persona"`
	RunID      string   `json:"run_id"`
	Verdict    string   `json:"verdict"`
	Note       string   `json:"note"`
	FindingIDs []string `json:"finding_ids"`
}

type multiConflict struct {
	File       string      `json:"file"`
	Line       int         `json:"line"`
	EndLine    int         `json:"end_line"`
	Title      string      `json:"title"`
	IsConflict bool        `json:"is_conflict"`
	Takes      []multiTake `json:"takes"`
}

func (c multiConflict) domain() reviews.Conflict {
	out := reviews.Conflict{File: c.File, Line: c.Line, EndLine: c.EndLine, Title: c.Title, IsConflict: c.IsConflict}
	for _, tk := range c.Takes {
		out.Takes = append(out.Takes, reviews.ConflictTake{
			AgentID: tk.AgentID, Persona: tk.Persona, RunID: tk.RunID, Verdict: tk.Verdict, Note: tk.Note, FindingIDs: tk.FindingIDs,
		})
	}
	return out
}

func conflictsOf(in []multiConflict) []reviews.Conflict {
	out := make([]reviews.Conflict, len(in))
	for i, c := range in {
		out[i] = c.domain()
	}
	return out
}

func TestMultiAgentParity_OrderTotalsGroups(t *testing.T) {
	var g struct {
		Columns []struct {
			Name          string        `json:"name"`
			Columns       []multiColumn `json:"columns"`
			OrderedRunIDs []string      `json:"orderedRunIds"`
			Totals        struct {
				DurationMS int      `json:"total_duration_ms"`
				CostUSD    *float64 `json:"total_cost_usd"`
			} `json:"totals"`
			Conflicts []multiConflict `json:"conflicts"`
		} `json:"columns"`
	}
	loadGolden(t, "multiagent", &g)
	if len(g.Columns) == 0 {
		t.Fatal("multiagent.json has no column cases")
	}
	for _, tc := range g.Columns {
		t.Run(tc.Name, func(t *testing.T) {
			var in []reviews.AgentColumn
			for _, c := range tc.Columns {
				in = append(in, c.domain())
			}
			ordered := reviews.OrderColumns(in)
			var ids []string
			for _, c := range ordered {
				ids = append(ids, c.RunID)
			}
			if diff := cmp.Diff(tc.OrderedRunIDs, ids, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("OrderColumns run ids mismatch (-want +got):\n%s", diff)
			}
			dur, cost := reviews.RunTotals(ordered)
			if dur != tc.Totals.DurationMS {
				t.Errorf("RunTotals duration = %d, want %d", dur, tc.Totals.DurationMS)
			}
			if diff := cmp.Diff(tc.Totals.CostUSD, cost); diff != "" {
				t.Errorf("RunTotals cost mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(conflictsOf(tc.Conflicts), reviews.GroupFindings(ordered), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("GroupFindings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// MultiAgentService.get: the read model built from a header and its columns.
func TestMultiAgentParity_GetReadModel(t *testing.T) {
	var g struct {
		Get []struct {
			Name   string `json:"name"`
			Header struct {
				ID       string  `json:"id"`
				PRID     string  `json:"prId"`
				RepoID   string  `json:"repoId"`
				PRNumber *int    `json:"prNumber"`
				PRTitle  *string `json:"prTitle"`
				RanAt    string  `json:"ranAt"`
			} `json:"header"`
			Error    *struct{}     `json:"error"`
			Columns  []multiColumn `json:"columns"`
			Response struct {
				AgentCount      int             `json:"agent_count"`
				TotalDurationMS int             `json:"total_duration_ms"`
				TotalCostUSD    *float64        `json:"total_cost_usd"`
				Columns         []multiColumn   `json:"columns"`
				Conflicts       []multiConflict `json:"conflicts"`
			} `json:"response"`
		} `json:"get"`
	}
	loadGolden(t, "multiagent", &g)
	if len(g.Get) == 0 {
		t.Fatal("multiagent.json has no get cases")
	}
	for _, tc := range g.Get {
		if tc.Error != nil {
			continue // not found: decided by the use case, tested with it
		}
		t.Run(tc.Name, func(t *testing.T) {
			ranAt, err := time.Parse(time.RFC3339Nano, tc.Header.RanAt)
			if err != nil {
				t.Fatalf("ranAt %q: %v", tc.Header.RanAt, err)
			}
			var cols []reviews.AgentColumn
			for _, c := range tc.Columns {
				cols = append(cols, c.domain())
			}
			got := reviews.BuildMultiAgentRun(reviews.MultiAgentHeader{
				ID: tc.Header.ID, PRID: tc.Header.PRID, RepoID: tc.Header.RepoID, PRNumber: tc.Header.PRNumber,
				PRTitle: tc.Header.PRTitle, RanAt: ranAt,
			}, cols)

			if got.AgentCount != tc.Response.AgentCount || got.TotalDurationMS != tc.Response.TotalDurationMS {
				t.Errorf("BuildMultiAgentRun agent_count/total_duration_ms = %d/%d, want %d/%d",
					got.AgentCount, got.TotalDurationMS, tc.Response.AgentCount, tc.Response.TotalDurationMS)
			}
			if diff := cmp.Diff(tc.Response.TotalCostUSD, got.TotalCostUSD); diff != "" {
				t.Errorf("BuildMultiAgentRun total cost mismatch (-want +got):\n%s", diff)
			}
			var wantCols []reviews.AgentColumn
			for _, c := range tc.Response.Columns {
				wantCols = append(wantCols, c.domain())
			}
			if diff := cmp.Diff(wantCols, got.Columns, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("BuildMultiAgentRun columns mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(conflictsOf(tc.Response.Conflicts), got.Conflicts, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("BuildMultiAgentRun conflicts mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMultiAgentParity_ListLimit(t *testing.T) {
	var g struct {
		List []struct {
			Name               string `json:"name"`
			Limit              *int   `json:"limit"`
			LimitPassedToStore *int   `json:"limitPassedToStore"`
			Error              *struct {
				Kind    string `json:"kind"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"list"`
	}
	loadGolden(t, "multiagent", &g)
	for _, tc := range g.List {
		if tc.LimitPassedToStore == nil && (tc.Error == nil || tc.Error.Code != "invalid_limit") {
			continue // an unknown repo: decided by the use case, tested with it
		}
		t.Run(tc.Name, func(t *testing.T) {
			got, err := reviews.ListLimit(tc.Limit)
			if tc.Error != nil {
				if !errors.Is(err, reviews.ErrInvalidLimit) || err.Error() != tc.Error.Message {
					t.Errorf("ListLimit(%v) error = %v, want ErrInvalidLimit %q", tc.Limit, err, tc.Error.Message)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListLimit(%v) error = %v", tc.Limit, err)
			}
			if got != *tc.LimitPassedToStore {
				t.Errorf("ListLimit(%v) = %d, want %d", tc.Limit, got, *tc.LimitPassedToStore)
			}
		})
	}
}

func TestMultiAgentParity_Learn(t *testing.T) {
	var g struct {
		Learn []struct {
			Finding struct {
				Title      string  `json:"title"`
				Rationale  string  `json:"rationale"`
				Suggestion *string `json:"suggestion"`
			} `json:"finding"`
			Content string `json:"content"`
		} `json:"learn"`
		LearnContext []struct {
			FindingID string `json:"findingId"`
			Context   string `json:"context"`
		} `json:"learnContext"`
		LearnUseCase []struct {
			Name   string `json:"name"`
			Target struct {
				Finding     wireRecord `json:"finding"`
				WorkspaceID string     `json:"workspaceId"`
				RepoID      string     `json:"repoId"`
				PRNumber    int        `json:"prNumber"`
			} `json:"target"`
			ExistingMemoryID *string `json:"existingMemoryId"`
			Statements       []struct {
				Kind   string `json:"kind"`
				Key    string `json:"key"`
				Table  string `json:"table"`
				Values *struct {
					WorkspaceID string  `json:"workspaceId"`
					RepoID      string  `json:"repoId"`
					Scope       string  `json:"scope"`
					Kind        string  `json:"kind"`
					Content     string  `json:"content"`
					Confidence  float64 `json:"confidence"`
					Sources     []struct {
						PR      int    `json:"pr"`
						Context string `json:"context"`
					} `json:"sources"`
				} `json:"values"`
			} `json:"statements"`
		} `json:"learnUseCase"`
	}
	loadGolden(t, "multiagent", &g)
	if len(g.Learn) == 0 || len(g.LearnContext) == 0 || len(g.LearnUseCase) == 0 {
		t.Fatal("multiagent.json lacks learn cases")
	}
	for _, c := range g.Learn {
		f := reviews.FindingRecord{Title: c.Finding.Title, Rationale: c.Finding.Rationale, Suggestion: c.Finding.Suggestion}
		if got := reviews.LearnedMemoryContent(f); got != c.Content {
			t.Errorf("LearnedMemoryContent(%+v) = %q, want %q", c.Finding, got, c.Content)
		}
	}
	for _, c := range g.LearnContext {
		if got := reviews.LearnContext(c.FindingID); got != c.Context {
			t.Errorf("LearnContext(%q) = %q, want %q", c.FindingID, got, c.Context)
		}
	}
	for _, tc := range g.LearnUseCase {
		t.Run(tc.Name, func(t *testing.T) {
			target := reviews.LearnTarget{Finding: tc.Target.Finding.domain(), RepoID: tc.Target.RepoID, PRNumber: tc.Target.PRNumber}
			got := reviews.NewLearnMemory(tc.Target.WorkspaceID, target)
			for _, st := range tc.Statements {
				switch st.Kind {
				case "lock":
					if k := reviews.LearnLockKey(tc.Target.Finding.ID); k != st.Key {
						t.Errorf("LearnLockKey(%q) = %q, want %q", tc.Target.Finding.ID, k, st.Key)
					}
				case "insert":
					v := st.Values
					if st.Table != "memory" || v == nil || len(v.Sources) != 1 {
						t.Fatalf("unexpected insert statement %+v", st)
					}
					want := reviews.NewMemory{
						WorkspaceID: v.WorkspaceID, RepoID: v.RepoID, FindingID: tc.Target.Finding.ID,
						PRNumber: v.Sources[0].PR, Content: v.Content, Confidence: v.Confidence,
					}
					if diff := cmp.Diff(want, got); diff != "" {
						t.Errorf("NewLearnMemory mismatch (-want +got):\n%s", diff)
					}
					if v.Scope != "repo" || v.Kind != "learning" {
						t.Errorf("golden memory scope/kind = %q/%q, want repo/learning", v.Scope, v.Kind)
					}
					if c := reviews.LearnContext(tc.Target.Finding.ID); c != v.Sources[0].Context {
						t.Errorf("LearnContext(%q) = %q, want the stored source context %q", tc.Target.Finding.ID, c, v.Sources[0].Context)
					}
				}
			}
		})
	}
}

// agentRunEstimates is SQL in Node, so the expectations are hand-written (handwritten/estimates.json).
func TestMultiAgentParity_Estimates(t *testing.T) {
	var h struct {
		Cases []struct {
			Name string `json:"name"`
			Runs []struct {
				ID         string   `json:"id"`
				Agent      *string  `json:"agent"`
				Status     string   `json:"status"`
				RanAt      string   `json:"ran_at"`
				DurationMS *int     `json:"duration_ms"`
				CostUSD    *float64 `json:"cost_usd"`
			} `json:"runs"`
			Expected []struct {
				AgentID       string   `json:"agent_id"`
				SampleSize    int      `json:"sample_size"`
				AvgDurationMS *float64 `json:"avg_duration_ms"`
				AvgCostUSD    *float64 `json:"avg_cost_usd"`
			} `json:"expected"`
		} `json:"cases"`
	}
	loadHandwritten(t, "estimates", &h)
	if len(h.Cases) == 0 {
		t.Fatal("estimates.json has no cases")
	}
	for _, tc := range h.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var runs []reviews.EstimateRun
			for _, r := range tc.Runs {
				if r.Status != "done" {
					continue // the query reads done runs only
				}
				ranAt, err := time.Parse(time.RFC3339, r.RanAt)
				if err != nil {
					t.Fatalf("ran_at %q: %v", r.RanAt, err)
				}
				run := reviews.EstimateRun{ID: r.ID, RanAt: ranAt, DurationMS: r.DurationMS, CostUSD: r.CostUSD}
				if r.Agent != nil {
					run.AgentID = *r.Agent
				}
				runs = append(runs, run)
			}
			var want []reviews.AgentRunEstimate
			for _, e := range tc.Expected {
				want = append(want, reviews.AgentRunEstimate{AgentID: e.AgentID, SampleSize: e.SampleSize, AvgDurationMS: e.AvgDurationMS, AvgCostUSD: e.AvgCostUSD})
			}
			opt := cmpopts.EquateApprox(0, 1e-12)
			if diff := cmp.Diff(want, reviews.Estimates(runs), cmpopts.EquateEmpty(), opt); diff != "" {
				t.Errorf("Estimates mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
