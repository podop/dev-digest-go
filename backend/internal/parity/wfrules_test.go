package parity_test

// Phase 6 parity of the workflow rules (wfrules.Check) with the Node checkWorkflow: the 428 texts of
// testdata/goldens/wfrules.json (spec AC6038, FR6056, NFR6001; plan S32). Where Go's YAML parser
// (yaml.v3) and the Node "yaml" package disagree the case is not chased: it is listed with the reason in
// testdata/handwritten/wfrules_deviations.json and the test checks that list in both directions (every
// disagreement is listed, every listed case still disagrees). The generated workflow of spec 7.6.5 must
// pass the rules for every valid option combination.

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/domain/wfrules"
)

type wfVerdict struct {
	OK   bool   `json:"ok"`
	Rule string `json:"rule,omitempty"`
}

func (v wfVerdict) String() string {
	if v.OK {
		return "ok"
	}
	return "rule " + v.Rule
}

type wfCase struct {
	Name   string    `json:"name"`
	Text   text      `json:"text"`
	Bytes  int       `json:"bytes"`
	Result wfVerdict `json:"result"`
	Stage  string    `json:"stage"`
}

type wfGolden struct {
	MaxWorkflowBytes int      `json:"maxWorkflowBytes"`
	Cases            []wfCase `json:"cases"`
}

// wfDeviation is one hand-written disagreement between Node and Go.
type wfDeviation struct {
	Name     string    `json:"name"`
	Category string    `json:"category"`
	Node     wfVerdict `json:"node"`
	Go       wfVerdict `json:"go"`
	Reason   string    `json:"reason"`
}

type wfDeviations struct {
	HandWritten bool          `json:"handWritten"`
	Deviations  []wfDeviation `json:"deviations"`
}

// wfPad is the corpus rule padToBytes: base, a "\n#" comment line, then padChar repeated to fill the
// target byte size exactly (completed with "x").
func wfPad(base string, target int, padChar string) string {
	s := base + "\n#"
	rem := target - len(s)
	return s + strings.Repeat(padChar, rem/len(padChar)) + strings.Repeat("x", rem%len(padChar))
}

// wfBigText rebuilds the texts the golden carries as a $blob from corpus/wfrules/cases.json (the "size:"
// cases); ok is false for a blob this function does not know.
func wfBigText(name, base string) (string, bool) {
	switch name {
	case "size: exactly 65536 bytes (ascii padding comment)":
		return wfPad(base, 65536, "x"), true
	case "size: 65537 bytes":
		return wfPad(base, 65537, "x"), true
	case "size: 65536 bytes with two-byte characters":
		return wfPad(base, 65536, "é"), true
	case "size: 65537 bytes with two-byte characters":
		return wfPad(base, 65537, "é"), true
	case "size: under 65536 characters but over in bytes":
		return wfPad(base, 80000, "é"), true
	case "size: three-byte characters, 65536 bytes":
		return wfPad(base, 65536, "€"), true
	case "size: huge invalid yaml is rejected by size first":
		return strings.Repeat("{", 70000), true
	}
	return "", false
}

func TestWorkflowRulesParity(t *testing.T) {
	var g wfGolden
	loadGolden(t, "wfrules", &g)
	var dev wfDeviations
	readJSON(t, filepath.Join("testdata", "handwritten", "wfrules_deviations.json"), &dev)
	if !dev.HandWritten {
		t.Fatal("wfrules_deviations.json must carry \"handWritten\": true")
	}
	if g.MaxWorkflowBytes != 65536 || domain.MaxWorkflowBytes != 65536 {
		t.Fatalf("golden maxWorkflowBytes = %d, domain.MaxWorkflowBytes = %d, want both 65536", g.MaxWorkflowBytes, domain.MaxWorkflowBytes)
	}
	listed := map[string]wfDeviation{}
	for _, d := range dev.Deviations {
		if d.Reason == "" || d.Category == "" {
			t.Errorf("deviation %q needs a category and a reason", d.Name)
		}
		if _, dup := listed[d.Name]; dup {
			t.Errorf("deviation %q is listed twice", d.Name)
		}
		listed[d.Name] = d
	}
	if len(g.Cases) == 0 || g.Cases[0].Text.b != nil {
		t.Fatal("the first golden case must be the plain base workflow")
	}
	base := g.Cases[0].Text.s

	seen := map[string]bool{}
	for _, c := range g.Cases {
		if seen[c.Name] {
			t.Errorf("golden case %q appears twice", c.Name)
		}
		seen[c.Name] = true
		in := c.Text.s
		if c.Text.b != nil {
			var ok bool
			if in, ok = wfBigText(c.Name, base); !ok {
				t.Errorf("case %q: no rebuild rule for its $blob text", c.Name)
				continue
			}
			if !c.Text.matches(in) {
				t.Errorf("case %q: the rebuilt text does not match the golden digest", c.Name)
				continue
			}
		}
		if len(in) != c.Bytes {
			t.Errorf("case %q: text has %d bytes, golden says %d", c.Name, len(in), c.Bytes)
		}

		res := wfrules.Check(in)
		got := wfVerdict{OK: res.OK, Rule: string(res.Rule)}
		d, isListed := listed[c.Name]
		if got == c.Result {
			if isListed {
				t.Errorf("case %q agrees with Node (%v) but is listed as a deviation: remove it", c.Name, got)
			}
			continue
		}
		if !isListed {
			t.Errorf("case %q [%s]: Check = %v, Node = %v (not a listed deviation)", c.Name, c.Stage, got, c.Result)
			continue
		}
		if d.Node != c.Result || d.Go != got {
			t.Errorf("case %q: deviation lists node=%v go=%v, actual node=%v go=%v", c.Name, d.Node, d.Go, c.Result, got)
		}
	}
	for _, d := range dev.Deviations {
		if !seen[d.Name] {
			t.Errorf("deviation %q names no golden case", d.Name)
		}
	}
}

// TestWorkflowRulesAcceptGeneratedWorkflow: BuildWorkflow output passes Check for every valid option
// combination (every non-empty trigger list, in the given order and in permuted order, every post_as,
// slugs of several shapes), AC6038.
func TestWorkflowRulesAcceptGeneratedWorkflow(t *testing.T) {
	all := domain.Triggers()
	var lists [][]domain.Trigger
	for mask := 1; mask < 1<<len(all); mask++ {
		var l []domain.Trigger
		for i, tr := range all {
			if mask&(1<<i) != 0 {
				l = append(l, tr)
			}
		}
		lists = append(lists, l)
		// every order of the same subset
		for _, p := range wfPermutations(l) {
			lists = append(lists, p)
		}
	}
	slugs := []string{"a", "9", "0-0", "security-reviewer", strings.Repeat("a", 40), "x-y-z", "agent", "skill", "a1-b2"}
	for _, s := range []string{"Security Reviewer", "日本語", "", "Café Résumé", "UPPER_case-Name"} {
		slugs = append(slugs, domain.AgentSlug(s))
	}
	postAs := []domain.PostAs{domain.PostAsGitHubReview, domain.PostAsPRComment, domain.PostAsNone}
	n := 0
	for _, slug := range slugs {
		for _, l := range lists {
			for _, p := range postAs {
				n++
				text, err := domain.BuildWorkflow(domain.WorkflowOptions{Slug: slug, Triggers: l, PostAs: p})
				if err != nil {
					t.Fatalf("BuildWorkflow(%q, %v, %q): %v", slug, l, p, err)
				}
				if r := wfrules.Check(text); !r.OK {
					t.Fatalf("Check(BuildWorkflow(%q, %v, %q)) = rule %s, want OK", slug, l, p, r.Rule)
				}
			}
		}
	}
	if n < 500 {
		t.Errorf("only %d combinations were checked", n)
	}
}

func wfPermutations(l []domain.Trigger) [][]domain.Trigger {
	if len(l) <= 1 {
		return nil
	}
	var out [][]domain.Trigger
	var rec func(cur []domain.Trigger, rest []domain.Trigger)
	rec = func(cur, rest []domain.Trigger) {
		if len(rest) == 0 {
			out = append(out, slices.Clone(cur))
			return
		}
		for i := range rest {
			next := slices.Concat(rest[:i:i], rest[i+1:])
			rec(append(cur, rest[i]), next)
		}
	}
	rec(nil, l)
	return out
}

// TestWorkflowRulesGoTemplate: the corpus text of spec 7.6.5 (corpus/wfrules/cases.json "go") is what
// BuildWorkflow writes for security-reviewer, all triggers and github_review, and it passes Check.
func TestWorkflowRulesGoTemplate(t *testing.T) {
	var c struct {
		Go string `json:"go"`
	}
	loadCorpus(t, "wfrules", "cases", &c)
	got, err := domain.BuildWorkflow(domain.WorkflowOptions{
		Slug: "security-reviewer", Triggers: domain.Triggers(), PostAs: domain.PostAsGitHubReview,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(c.Go, got); diff != "" {
		t.Errorf("BuildWorkflow(security-reviewer, all, github_review) differs from the corpus text (-want +got):\n%s", diff)
	}
	if r := wfrules.Check(c.Go); !r.OK {
		t.Errorf("Check(corpus go text) = rule %s, want OK", r.Rule)
	}
}
