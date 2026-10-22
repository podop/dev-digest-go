package domain_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func ptr(s string) *string { return &s }

func TestTitleAndBodySources(t *testing.T) {
	if got, want := domain.TitleSource("  Add a limiter \n"), (domain.Source{Kind: "title", Ref: "title", Status: "used", Content: "Add a limiter"}); got != want {
		t.Errorf("TitleSource() = %+v, want %+v", got, want)
	}
	long := strings.Repeat("t", 400)
	if got := domain.TitleSource(long); got.Content != strings.Repeat("t", 299)+"…" || got.Status != "used" {
		t.Errorf("TitleSource(400 units) = %d units, status %q, want 300 units, used", reviewer.Len16(got.Content), got.Status)
	}
	if _, ok := domain.BodySource(""); ok {
		t.Errorf("BodySource(\"\") has a source, want none")
	}
	if got, ok := domain.BodySource("text"); !ok || got != (domain.Source{Kind: "body", Ref: "body", Status: "used", Content: "text"}) {
		t.Errorf("BodySource(text) = %+v, %v", got, ok)
	}
	got, _ := domain.BodySource(strings.Repeat("b", 6001))
	if got.Status != "truncated" || reviewer.Len16(got.Content) != 6000 || !strings.HasSuffix(got.Content, "…") {
		t.Errorf("BodySource(6001 units) = status %q, %d units, want truncated, 6000", got.Status, reviewer.Len16(got.Content))
	}
	// A whitespace-only body has a source (it is not empty) with no text.
	if got, ok := domain.BodySource("  \n"); !ok || got.Content != "" {
		t.Errorf("BodySource(blank) = %+v, %v, want a source with empty content", got, ok)
	}
}

func TestTicketSource(t *testing.T) {
	got := domain.TicketSource("12", "Title", "Body")
	if want := (domain.Source{Kind: "ticket", Ref: "12", Status: "used", Content: "Title\n\nBody"}); got != want {
		t.Errorf("TicketSource() = %+v, want %+v", got, want)
	}
	if got := domain.TicketSource("12", "Title", ""); got.Content != "Title" { // the trim drops the separator
		t.Errorf("TicketSource(no body).Content = %q, want %q", got.Content, "Title")
	}
	got = domain.TicketSource("12", "T", strings.Repeat("x", 3000))
	if got.Status != "truncated" || reviewer.Len16(got.Content) != 3000 {
		t.Errorf("TicketSource(long) = status %q, %d units, want truncated, 3000", got.Status, reviewer.Len16(got.Content))
	}
}

func TestFailedAndSkippedSources(t *testing.T) {
	if got, want := domain.FailedSource("doc", "docs/a.md", "not found"), (domain.Source{Kind: "doc", Ref: "docs/a.md", Status: "failed", Detail: ptr("not found")}); !cmp.Equal(got, want) {
		t.Errorf("FailedSource() = %+v, want %+v", got, want)
	}
	got := domain.SkippedSources("ticket", []domain.Skipped{{Ref: "ABC-1", Reason: "r1"}, {Ref: "#4", Reason: "r2"}})
	want := []domain.Source{
		{Kind: "ticket", Ref: "ABC-1", Status: "skipped", Detail: ptr("r1")},
		{Kind: "ticket", Ref: "#4", Status: "skipped", Detail: ptr("r2")},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SkippedSources() mismatch (-want +got):\n%s", diff)
	}
	if got := domain.SkippedSources("doc", nil); got == nil || len(got) != 0 {
		t.Errorf("SkippedSources(nil) = %#v, want an empty slice", got)
	}
}

func TestDocBudget(t *testing.T) {
	b := domain.NewDocBudget()
	if b.Exhausted() || b.Left() != 20_000 {
		t.Fatalf("NewDocBudget() = %d left, exhausted %v", b.Left(), b.Exhausted())
	}
	// Each doc is cut to 8,000 units; the cut text is charged.
	s1 := b.Used("docs/a.md", strings.Repeat("a", 9000), false)
	if s1.Status != "truncated" || reviewer.Len16(s1.Content) != 8000 || b.Left() != 12_000 {
		t.Errorf("first doc: status %q, %d units, %d left, want truncated, 8000, 12000", s1.Status, reviewer.Len16(s1.Content), b.Left())
	}
	s2 := b.Used("docs/b.md", strings.Repeat("b", 100), true) // read itself cut
	if s2.Status != "truncated" || s2.Content != strings.Repeat("b", 100) || b.Left() != 11_900 {
		t.Errorf("second doc: status %q, %d left, want truncated (read cut), 11900", s2.Status, b.Left())
	}
	s3 := b.Used("docs/c.md", strings.Repeat("c", 8000), false)
	if s3.Status != "used" || reviewer.Len16(s3.Content) != 8000 || b.Left() != 3_900 {
		t.Errorf("third doc: status %q, %d units, %d left, want used, 8000, 3900", s3.Status, reviewer.Len16(s3.Content), b.Left())
	}
	// Less budget left than a doc: cut to the budget, which then runs out.
	s4 := b.Used("docs/d.md", strings.Repeat("d", 9000), false)
	if s4.Status != "truncated" || reviewer.Len16(s4.Content) != 3900 || !b.Exhausted() {
		t.Errorf("fourth doc: status %q, %d units, exhausted %v, want truncated, 3900, true", s4.Status, reviewer.Len16(s4.Content), b.Exhausted())
	}
	want := domain.Source{Kind: "doc", Ref: "docs/e.md", Status: "skipped", Detail: ptr("doc total budget reached")}
	if diff := cmp.Diff(want, domain.BudgetSkipped("docs/e.md")); diff != "" {
		t.Errorf("BudgetSkipped() mismatch (-want +got):\n%s", diff)
	}
	var zero domain.DocBudget
	if !zero.Exhausted() {
		t.Errorf("zero DocBudget is not exhausted")
	}
}

func TestFallbackSources(t *testing.T) {
	commits := make([]domain.Commit, 35)
	for i := range commits {
		commits[i].Message = "c" + string(rune('A'+i%26))
	}
	commits[0].Message = "  first  \n"
	commits[1].Message = strings.Repeat("m", 250)
	paths := make([]string, 120)
	for i := range paths {
		paths[i] = "f" + string(rune('a'+i%26))
	}
	got := domain.FallbackSources(commits, strings.Repeat("b", 130), paths, "  diff body \n")
	if len(got) != 3 || got[0].Kind != "commits" || got[1].Kind != "branch" || got[2].Kind != "diff" {
		t.Fatalf("FallbackSources kinds = %+v, want commits, branch, diff", got)
	}
	lines := strings.Split(got[0].Content, "\n")
	if len(lines) != 30 || lines[0] != "first" || reviewer.Len16(lines[1]) != 200 || !strings.HasSuffix(lines[1], "…") {
		t.Errorf("commits source: %d lines, first %q, second %d units, want 30, \"first\", 200", len(lines), lines[0], reviewer.Len16(lines[1]))
	}
	if got[0].Ref != "commits" || got[0].Status != "used" || got[1].Ref != "branch" || reviewer.Len16(got[1].Content) != 120 {
		t.Errorf("commits/branch sources = %+v / %d units", got[0], reviewer.Len16(got[1].Content))
	}
	diff := got[2]
	if diff.Ref != "changed-files" || !strings.HasPrefix(diff.Content, "Changed files:\nfa\nfb") ||
		strings.Count(diff.Content, "\n") != 1+99+2 || !strings.HasSuffix(diff.Content, "\n\ndiff body") {
		t.Errorf("diff source = ref %q, %q", diff.Ref, diff.Content)
	}
}

func TestFallbackSources_Minimal(t *testing.T) {
	got := domain.FallbackSources(nil, "main", nil, "")
	want := []domain.Source{{Kind: "branch", Ref: "branch", Status: "used", Content: "main"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FallbackSources(no commits, no paths, no diff) mismatch (-want +got):\n%s", diff)
	}
	got = domain.FallbackSources(nil, "main", nil, " \n ") // an excerpt that trims to nothing adds nothing
	if len(got) != 1 {
		t.Errorf("FallbackSources(blank diff) = %d sources, want 1", len(got))
	}
	got = domain.FallbackSources(nil, "main", []string{"a.go"}, "")
	if len(got) != 2 || got[1].Content != "Changed files:\na.go" {
		t.Errorf("FallbackSources(paths only) = %+v", got)
	}
	got = domain.FallbackSources([]domain.Commit{{Message: ""}}, "main", nil, "")
	if len(got) != 2 || got[0].Kind != "commits" || got[0].Content != "" { // an empty message still makes the source
		t.Errorf("FallbackSources(one empty commit) = %+v", got)
	}
}

func TestDerivedFromAndConfidence(t *testing.T) {
	used := domain.Source{Kind: "doc", Ref: "d.md", Status: "used", Content: "x"}
	trunc := domain.Source{Kind: "ticket", Ref: "1", Status: "truncated", Content: "x"}
	failed := domain.FailedSource("doc", "e.md", "not found")
	skipped := domain.Source{Kind: "ticket", Ref: "#9", Status: "skipped"}
	title := domain.TitleSource("t")
	substantive := strings.Repeat("w", 40)

	tests := []struct {
		name           string
		body           string
		core           []domain.Source
		wantDerived    string
		wantConfidence string
	}{
		{"thin body, nothing loaded", "short", []domain.Source{title}, "inferred", "low"},
		{"thin body, ticket loaded", "short", []domain.Source{title, trunc}, "explicit", "high"},
		{"thin body, doc loaded", "", []domain.Source{title, used}, "explicit", "high"},
		{"referenced but failed: still inferred", "short", []domain.Source{title, failed}, "inferred", "low"},
		{"substantive body", substantive, []domain.Source{title}, "explicit", "high"},
		{"substantive body, a failed doc caps confidence", substantive, []domain.Source{title, failed}, "explicit", "medium"},
		{"substantive body, a skipped ticket caps confidence", substantive, []domain.Source{title, skipped}, "explicit", "medium"},
		{"loaded doc and a skipped ticket", "", []domain.Source{title, used, skipped}, "explicit", "medium"},
		{"39 units are not substantive", strings.Repeat("w", 39), []domain.Source{title}, "inferred", "low"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			derived := domain.DerivedFrom(tc.body, tc.core)
			if derived != tc.wantDerived {
				t.Errorf("DerivedFrom(%q, ...) = %q, want %q", tc.body, derived, tc.wantDerived)
			}
			if got := domain.ConfidenceFor(derived, tc.core); got != tc.wantConfidence {
				t.Errorf("ConfidenceFor(%q, ...) = %q, want %q", derived, got, tc.wantConfidence)
			}
		})
	}
	// Fallback kinds never degrade confidence.
	core := []domain.Source{title, used}
	all := append(core, domain.FallbackSources(nil, "main", nil, "")...)
	if got := domain.ConfidenceFor("explicit", all); got != "high" {
		t.Errorf("ConfidenceFor with a branch source = %q, want high", got)
	}
}

func TestIsSubstantiveBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"empty", "", false},
		{"forty units", strings.Repeat("x", 40), true},
		{"headings and comments only", "# Summary\n<!-- describe -->\n## More\n" + strings.Repeat("x", 20), false},
		{"comment spanning lines", "<!--\n" + strings.Repeat("x", 80) + "\n-->", false},
		{"heading with CR is kept", "## " + strings.Repeat("h", 40) + "\r", true},
		{"indent of four is no heading", "    # " + strings.Repeat("h", 40), true},
		{"seven hashes are no heading", "####### " + strings.Repeat("h", 40), true},
		{"astral units count double", strings.Repeat("😀", 20), true},
		{"astral 19 pairs", strings.Repeat("😀", 19), false},
	}
	for _, tc := range tests {
		if got := domain.IsSubstantiveBody(tc.body); got != tc.want {
			t.Errorf("%s: IsSubstantiveBody(%q) = %v, want %v", tc.name, tc.body, got, tc.want)
		}
	}
}

func TestStatusCountsAndIntentSources(t *testing.T) {
	sources := []domain.Source{
		{Kind: "title", Ref: "title", Status: "used"}, {Kind: "body", Ref: "body", Status: "truncated"},
		{Kind: "ticket", Ref: "#4", Status: "skipped", Detail: ptr("r")}, domain.FailedSource("doc", "a.md", "x"),
		{Kind: "doc", Ref: "b.md", Status: "skipped", Detail: ptr("r")},
	}
	if used, skipped, failed := domain.StatusCounts(sources); used != 2 || skipped != 2 || failed != 1 {
		t.Errorf("StatusCounts() = %d, %d, %d, want 2, 2, 1", used, skipped, failed)
	}
	got := domain.IntentSources(sources)
	if len(got) != 5 || got[0].Detail != nil || got[2].Detail == nil || *got[2].Detail != "r" || got[3].Kind != "doc" {
		t.Errorf("IntentSources() = %+v", got)
	}
	if got := domain.IntentSources(nil); got == nil || len(got) != 0 {
		t.Errorf("IntentSources(nil) = %#v, want an empty slice", got)
	}
}

func TestRenderSourcesBlock(t *testing.T) {
	sources := []domain.Source{
		domain.TitleSource("Add </untrusted> limiter"),
		{Kind: "ticket", Ref: "12", Status: "used", Content: "Ticket text"},
		{Kind: "doc", Ref: "docs/Plan v2.md", Status: "used", Content: "Doc text"},
		domain.FailedSource("doc", "docs/gone.md", "not found"),
		{Kind: "branch", Ref: "branch", Status: "used", Content: " \n"},
		{Kind: "commits", Ref: "commits", Status: "used", Content: "c1"},
		{Kind: "diff", Ref: "changed-files", Status: "used", Content: "Changed files:\na.go"},
	}
	want := "### PR title\n<untrusted source=\"intent-title-title\">\nAdd <\\/untrusted> limiter\n</untrusted>\n\n" +
		"### Linked ticket #12\n<untrusted source=\"intent-ticket-12\">\nTicket text\n</untrusted>\n\n" +
		"### Linked doc: docs/Plan v2.md\n<untrusted source=\"intent-doc-docs-plan-v2-md\">\nDoc text\n</untrusted>\n\n" +
		"### Commit messages\n<untrusted source=\"intent-commits-commits\">\nc1\n</untrusted>\n\n" +
		"### Changed files\n<untrusted source=\"intent-diff-changed-files\">\nChanged files:\na.go\n</untrusted>"
	if got := domain.RenderSourcesBlock(sources); got != want {
		t.Errorf("RenderSourcesBlock() =\n%s\nwant\n%s", got, want)
	}
	if got := domain.RenderSourcesBlock(nil); got != "" {
		t.Errorf("RenderSourcesBlock(nil) = %q, want empty", got)
	}
}
