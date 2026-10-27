package domain_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func candidate(id string, sev reviewer.Severity, file string, start, end int) domain.InlineCandidate {
	return domain.InlineCandidate{
		Finding: reviewer.Finding{ID: id, Severity: sev, Category: reviewer.CategoryBug, Title: "T" + id, File: file, StartLine: start, EndLine: end},
		Comment: domain.InlineComment{Path: file, Line: start, Body: id},
	}
}

func ids(cs []domain.InlineCandidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Finding.ID
	}
	return out
}

func TestSelectInline_Order(t *testing.T) {
	cs := []domain.InlineCandidate{
		candidate("1", reviewer.SeveritySuggestion, "a.go", 1, 1),
		candidate("2", reviewer.Severity("INFO"), "a.go", 1, 1),
		candidate("3", reviewer.SeverityWarning, "b.go", 9, 9),
		candidate("4", reviewer.SeverityCritical, "z.go", 1, 1),
		candidate("5", reviewer.SeverityCritical, "B.go", 1, 1),
		candidate("6", reviewer.SeverityCritical, "z.go", 1, 1), // engine order breaks the tie with 4
		candidate("7", reviewer.SeverityCritical, "z.go", 0, 0),
		candidate("8", reviewer.SeverityWarning, "b.go", 3, 4),
		candidate("9", reviewer.SeverityCritical, "a.go", 2, 2),
	}
	selected, rest := domain.SelectInline(cs, 100)
	want := []string{"5", "9", "7", "4", "6", "8", "3", "1", "2"}
	if diff := cmp.Diff(want, ids(selected)); diff != "" {
		t.Errorf("SelectInline(cs, 100) selected mismatch (-want +got):\n%s", diff)
	}
	if len(rest) != 0 {
		t.Errorf("SelectInline(cs, 100) rest = %v, want none", ids(rest))
	}
	if got := ids(cs); got[0] != "1" || got[8] != "9" {
		t.Errorf("SelectInline modified its input: %v", got)
	}
}

func TestSelectInline_Boundaries(t *testing.T) {
	mk := func(n int) []domain.InlineCandidate {
		cs := make([]domain.InlineCandidate, n)
		for i := range cs {
			cs[i] = candidate(fmt.Sprint(i), reviewer.SeverityWarning, "f.go", i+1, i+1)
		}
		return cs
	}
	tests := []struct {
		name         string
		n, limit     int
		wantSelected int
		wantRest     int
	}{
		{"none", 0, domain.MaxInlineThreads, 0, 0},
		{"below the cap", 45, domain.MaxInlineThreads, 45, 0},
		{"exactly the cap", 50, domain.MaxInlineThreads, 50, 0},
		{"one over", 51, domain.MaxInlineThreads, 50, 1},
		{"73", 73, domain.MaxInlineThreads, 50, 23},
		{"zero limit", 3, 0, 0, 3},
		{"negative limit", 3, -1, 0, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selected, rest := domain.SelectInline(mk(tc.n), tc.limit)
			if len(selected) != tc.wantSelected || len(rest) != tc.wantRest {
				t.Errorf("SelectInline(%d candidates, %d) = %d selected, %d rest, want %d, %d",
					tc.n, tc.limit, len(selected), len(rest), tc.wantSelected, tc.wantRest)
			}
			// Appending to selected must not write into rest.
			_ = append(selected, candidate("x", reviewer.SeverityCritical, "x", 1, 1))
			if len(rest) > 0 && rest[0].Finding.ID == "x" {
				t.Errorf("selected shares its backing array with rest")
			}
		})
	}
}

// TestSelectInline_AC46 is the AC46 shape: 73 anchored findings (20 critical, 25 warning, 28
// suggestion in mixed engine order) -> the 20 critical, the 25 warning, the first 5 suggestions by
// file then start line.
func TestSelectInline_AC46(t *testing.T) {
	var cs []domain.InlineCandidate
	crit, warn, sugg := 0, 0, 0
	for i := range 73 {
		file := fmt.Sprintf("f%02d.go", (i*7)%13)
		switch {
		case i%3 == 0 && crit < 20, warn >= 25 && sugg >= 28 && crit < 20:
			cs = append(cs, candidate(fmt.Sprintf("c%d", i), reviewer.SeverityCritical, file, 100-i, 100-i))
			crit++
		case i%3 == 1 && warn < 25, sugg >= 28 && warn < 25:
			cs = append(cs, candidate(fmt.Sprintf("w%d", i), reviewer.SeverityWarning, file, 100-i, 100-i))
			warn++
		default:
			cs = append(cs, candidate(fmt.Sprintf("s%d", i), reviewer.SeveritySuggestion, file, 100-i, 100-i))
			sugg++
		}
	}
	if crit != 20 || warn != 25 || sugg != 28 {
		t.Fatalf("fixture has %d/%d/%d critical/warning/suggestion, want 20/25/28", crit, warn, sugg)
	}
	selected, rest := domain.SelectInline(cs, 50)
	if len(selected) != 50 || len(rest) != 23 {
		t.Fatalf("SelectInline = %d selected, %d rest, want 50, 23", len(selected), len(rest))
	}
	for i, c := range selected {
		want := reviewer.SeveritySuggestion
		if i < 20 {
			want = reviewer.SeverityCritical
		} else if i < 45 {
			want = reviewer.SeverityWarning
		}
		if c.Finding.Severity != want {
			t.Fatalf("selected[%d] severity = %s, want %s", i, c.Finding.Severity, want)
		}
	}
	sorted := func(group []domain.InlineCandidate) bool {
		return slices.IsSortedFunc(group, func(a, b domain.InlineCandidate) int {
			if c := strings.Compare(a.Finding.File, b.Finding.File); c != 0 {
				return c
			}
			return a.Finding.StartLine - b.Finding.StartLine
		})
	}
	if !sorted(selected[:20]) || !sorted(selected[20:45]) || !sorted(selected[45:]) || !sorted(rest[:23]) {
		t.Errorf("a severity group is not ordered by file then start line")
	}
}

func TestCapSection(t *testing.T) {
	rest := []domain.InlineCandidate{
		candidate("1", reviewer.SeverityWarning, "src/a.go", 3, 9),
		candidate("2", reviewer.SeveritySuggestion, "b.go", 7, 7),
		candidate("3", reviewer.Severity("INFO"), "c.go", 1, 1),
	}
	want := "_3 more findings not posted inline (limit: 50 inline threads per run):_\n" +
		"- 🟡 **T1** (warning) — `src/a.go:3-9`\n" +
		"- 🔵 **T2** (suggestion) — `b.go:7`\n" +
		"- • **T3** (info) — `c.go:1`"
	if got := domain.CapSection(rest); got != want {
		t.Errorf("CapSection(3) =\n%s\nwant\n%s", got, want)
	}

	one := domain.CapSection(rest[:1])
	if first, _, _ := strings.Cut(one, "\n"); first != "_1 more finding not posted inline (limit: 50 inline threads per run):_" {
		t.Errorf("CapSection(1) first line = %q", first)
	}
	if got := domain.CapSection(nil); got != "" {
		t.Errorf("CapSection(nil) = %q, want empty", got)
	}
}

func TestInsertCapSection(t *testing.T) {
	section := "_1 more finding not posted inline (limit: 50 inline threads per run):_\n- x"
	tests := []struct {
		name, body, section, want string
	}{
		{
			name: "before the posted line", section: section,
			body: "## T\n\n**2 findings**\n\n- a\n- b\n\n_Posted via DevDigest._",
			want: "## T\n\n**2 findings**\n\n- a\n- b\n\n" + section + "\n\n_Posted via DevDigest._",
		},
		{name: "no posted line appends", section: section, body: "## T", want: "## T\n\n" + section},
		{name: "empty section", section: "", body: "## T\n\n_Posted via DevDigest._", want: "## T\n\n_Posted via DevDigest._"},
	}
	for _, tc := range tests {
		if got := domain.InsertCapSection(tc.body, tc.section); got != tc.want {
			t.Errorf("%s: InsertCapSection(%q, ...) = %q, want %q", tc.name, tc.body, got, tc.want)
		}
	}
}

// TestCapSection_WithPayloadBody checks the insertion point against the real Phase 6 body.
func TestCapSection_WithPayloadBody(t *testing.T) {
	f := finding(reviewer.SeverityCritical, "Leaked key", "src/config.ts", 10, 10)
	payload := domain.ToReviewPayload([]reviewer.Finding{f}, reviewer.FailOnCritical, configDiff(), "Agent")
	section := domain.CapSection([]domain.InlineCandidate{candidate("1", reviewer.SeverityWarning, "x.go", 1, 1)})
	got := domain.InsertCapSection(payload.Body, section)
	if !strings.HasSuffix(got, "\n\n"+section+"\n\n_Posted via DevDigest._") || strings.Count(got, "_Posted via DevDigest._") != 1 {
		t.Errorf("InsertCapSection on the payload body = %q", got)
	}
}

func TestCapLogLine(t *testing.T) {
	if got, want := domain.CapLogLine(50, 23), "[agent-runner] inline comments capped: posted=50 not_posted=23"; got != want {
		t.Errorf("CapLogLine(50, 23) = %q, want %q", got, want)
	}
}

func TestStripGitLabIgnored(t *testing.T) {
	section := func(path string) string {
		return "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-a\n+b\n"
	}
	keep := section("src/main.go")
	// Like StripIgnoredFiles, a stripped last section takes the final empty line with it, so the
	// kept text then ends without the closing line break (ParseDiff trims it anyway).
	keepNoEOL := strings.TrimSuffix(keep, "\n")
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"runner dir", section(".devdigest/runner/x.b64") + keep, keep},
		{"job dir", keep + section(".gitlab/devdigest/review-a.yml"), keepNoEOL},
		{"both around a kept one", section(".devdigest/a") + keep + section(".gitlab/devdigest/b.yml"), keepNoEOL},
		{"github workflows are kept in GitLab mode", section(".github/workflows/ci.yml"), section(".github/workflows/ci.yml")},
		{"other .gitlab files are kept", section(".gitlab/ci/other.yml"), section(".gitlab/ci/other.yml")},
		{"root include is kept", section(".gitlab-ci.yml"), section(".gitlab-ci.yml")},
		{"prefix is exact", section(".devdigest-x/a") + section("x.devdigest/a"), section(".devdigest-x/a") + section("x.devdigest/a")},
		{"rename into the ignored dir is stripped", "diff --git a/old.txt b/.devdigest/new.txt\nrename from old.txt\nrename to .devdigest/new.txt\n" + keep, keep},
		{"rename out of the ignored dir is kept", "diff --git a/.devdigest/old b/new.txt\nrename from .devdigest/old\nrename to new.txt\n" + keep, "diff --git a/.devdigest/old b/new.txt\nrename from .devdigest/old\nrename to new.txt\n" + keep},
		{"quoted path in the ignored dir", "diff --git \"a/.devdigest/sp\\\\tx\" \"b/.devdigest/sp\\\\tx\"\n+x\n" + keep, keep},
		{"nothing to strip", keep, keep},
		{"empty", "", ""},
		{"no trailing newline", keep + strings.TrimSuffix(section(".devdigest/a"), "\n"), keepNoEOL},
	}
	for _, tc := range tests {
		if got := domain.StripGitLabIgnored(tc.raw); got != tc.want {
			t.Errorf("%s: StripGitLabIgnored mismatch:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
