package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func ptr[T any](v T) *T { return &v }

func finding(sev reviewer.Severity, title, file string, start, end int) reviewer.Finding {
	return reviewer.Finding{
		Severity: sev, Category: reviewer.CategoryBug, Title: title, File: file,
		StartLine: start, EndLine: end, Rationale: "Because " + title + ".",
	}
}

// configDiff covers src/config.ts lines 9-12.
func configDiff() reviewer.Diff { return domain.ParseDiff(fixtureDiff) }

func TestToReviewPayload_Body(t *testing.T) {
	crit := finding(reviewer.SeverityCritical, "Leaked key", "src/config.ts", 10, 10)
	crit.Suggestion = ptr("Use an env var")
	warn := finding(reviewer.SeverityWarning, "Port", "src/config.ts", 11, 12)
	sugg := finding(reviewer.SeveritySuggestion, "Name", "src/config.ts", 9, 9)
	sugg.Suggestion = ptr("")
	odd := finding(reviewer.Severity("INFO"), "Odd", "src/config.ts", 9, 9)

	tests := []struct {
		name     string
		findings []reviewer.Finding
		failOn   reviewer.FailOn
		event    string
		body     string
	}{
		{
			name: "no findings approves", failOn: reviewer.FailOnCritical, event: domain.EventApprove,
			body: "## Reviewer — Approved ✅\n\n_No findings. Looks good._",
		},
		{
			name: "gate triggered requests changes", findings: []reviewer.Finding{crit}, failOn: reviewer.FailOnCritical,
			event: domain.EventRequestChanges,
			body: "## Reviewer — Changes requested\n\n**1 finding** · 1 critical · 0 warning · 0 suggestion\n\n" +
				"- 🔴 **Leaked key** (critical, bug) — `src/config.ts:10`\n  - Because Leaked key.\n  - _Suggestion:_ Use an env var\n\n" +
				"_Posted via DevDigest._",
		},
		{
			name: "findings below the gate only comment", findings: []reviewer.Finding{warn, sugg}, failOn: reviewer.FailOnCritical,
			event: domain.EventComment,
			body: "## Reviewer\n\n**2 findings** · 0 critical · 1 warning · 1 suggestion\n\n" +
				"- 🟡 **Port** (warning, bug) — `src/config.ts:11-12`\n  - Because Port.\n" +
				"- 🔵 **Name** (suggestion, bug) — `src/config.ts:9`\n  - Because Name.\n\n" +
				"_Posted via DevDigest._",
		},
		{
			name: "never gate comments even on critical", findings: []reviewer.Finding{crit}, failOn: reviewer.FailOnNever,
			event: domain.EventComment,
			body: "## Reviewer\n\n**1 finding** · 1 critical · 0 warning · 0 suggestion\n\n" +
				"- 🔴 **Leaked key** (critical, bug) — `src/config.ts:10`\n  - Because Leaked key.\n  - _Suggestion:_ Use an env var\n\n" +
				"_Posted via DevDigest._",
		},
		{
			name: "unknown severity gets a bullet and no count", findings: []reviewer.Finding{odd}, failOn: reviewer.FailOnAny,
			event: domain.EventComment,
			body: "## Reviewer\n\n**1 finding** · 0 critical · 0 warning · 0 suggestion\n\n" +
				"- • **Odd** (info, bug) — `src/config.ts:9`\n  - Because Odd.\n\n_Posted via DevDigest._",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ToReviewPayload(tc.findings, tc.failOn, configDiff(), "Reviewer")
			if got.Event != tc.event {
				t.Errorf("ToReviewPayload() event = %q, want %q", got.Event, tc.event)
			}
			if got.Body != tc.body {
				t.Errorf("ToReviewPayload() body =\n%q\nwant\n%q", got.Body, tc.body)
			}
		})
	}
}

func TestToReviewPayload_InlineComments(t *testing.T) {
	crit := finding(reviewer.SeverityCritical, "Leaked key", "src/config.ts", 10, 10)
	crit.Suggestion = ptr("Use an env var")
	warn := finding(reviewer.SeverityWarning, "Port", "src/config.ts", 11, 11)

	got := domain.ToReviewPayload([]reviewer.Finding{crit, warn}, reviewer.FailOnCritical, configDiff(), "R")
	want := []domain.InlineComment{
		{Path: "src/config.ts", Line: 10, Body: "**Leaked key** (critical)\n\nBecause Leaked key.\n\n_Suggestion:_ Use an env var"},
		{Path: "src/config.ts", Line: 11, Body: "**Port** (warning)\n\nBecause Port."},
	}
	if diff := cmp.Diff(want, got.Comments); diff != "" {
		t.Errorf("ToReviewPayload() comments mismatch (-want +got):\n%s", diff)
	}
}

func TestToReviewPayload_CommentLine(t *testing.T) {
	// Lines 9-12 are in the diff (the fixture), and file b.go has a hunk of deletions only: its
	// declared new range is 5-6.
	deleteOnly := reviewer.Diff{Files: []reviewer.DiffFile{
		{Path: "src/config.ts", Hunks: configDiff().Files[0].Hunks},
		{Path: "b.go", Hunks: []reviewer.Hunk{{File: "b.go", NewStart: 5, NewLines: 2}}},
		{Path: "huge.go", Hunks: []reviewer.Hunk{{File: "huge.go", NewStart: 5, NewLines: 1 << 50}}},
		{Path: "two.go", Hunks: []reviewer.Hunk{
			{File: "two.go", NewStart: 1, NewLines: 2, NewLineNumbers: []int{1, 2}},
			{File: "two.go", NewStart: 40, NewLines: 2, NewLineNumbers: []int{40, 41}},
		}},
	}}
	tests := []struct {
		name       string
		file       string
		start, end int
		line       int // 0 = no inline comment
	}{
		{"end line is in the diff", "src/config.ts", 1, 10, 10},
		{"end line wins over a start below the diff", "src/config.ts", 20, 11, 11},
		{"end past the hunk takes the last hunk line", "src/config.ts", 8, 20, 12},
		{"reversed range anchors to the nearest line of the lower end", "src/config.ts", 30, 5, 9},
		{"range wholly after the hunk", "src/config.ts", 100, 120, 0},
		{"range wholly before the hunk", "src/config.ts", 1, 8, 0},
		{"file that is not in the diff", "other.go", 1, 5, 0},
		{"deletion-only hunk covers its declared range", "b.go", 6, 6, 6},
		{"deletion-only hunk, end beyond", "b.go", 4, 9, 6},
		{"deletion-only hunk, range before it", "b.go", 1, 3, 0},
		{"a declared range of 2^50 lines is not expanded", "huge.go", 1, 1 << 40, 1 << 40},
		{"nearest line across two hunks", "two.go", 1, 39, 2},
		{"between two hunks", "two.go", 3, 39, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := finding(reviewer.SeverityWarning, "T", tc.file, tc.start, tc.end)
			got := domain.ToReviewPayload([]reviewer.Finding{f}, reviewer.FailOnCritical, deleteOnly, "R")
			switch {
			case tc.line == 0 && got.Comments != nil:
				t.Errorf("comments = %+v, want none", got.Comments)
			case tc.line != 0 && (len(got.Comments) != 1 || got.Comments[0].Line != tc.line):
				t.Errorf("comments = %+v, want one on line %d", got.Comments, tc.line)
			}
		})
	}
}

// TestToReviewPayload_MatchesNode pins the output of reviewer-core's toReviewPayload (run with
// node 22 on the same findings and diff, dev-digest d1c9093).
func TestToReviewPayload_MatchesNode(t *testing.T) {
	cfg := configDiff().Files[0]
	diff := reviewer.Diff{Files: []reviewer.DiffFile{cfg, {Path: "b.go", Hunks: []reviewer.Hunk{{File: "b.go", NewStart: 5, NewLines: 2}}}}}
	f := func(sev reviewer.Severity, title, file string, start, end int, sug *string) reviewer.Finding {
		x := finding(sev, title, file, start, end)
		x.Suggestion = sug
		return x
	}
	findings := []reviewer.Finding{
		f(reviewer.SeverityCritical, "Leaked key", "src/config.ts", 10, 10, ptr("Use an env var")),
		f(reviewer.SeverityWarning, "Port", "src/config.ts", 11, 12, nil),
		f(reviewer.SeveritySuggestion, "Name", "src/config.ts", 9, 9, ptr("")),
		f(reviewer.Severity("INFO"), "Odd", "src/config.ts", 9, 9, nil),
		f(reviewer.SeverityWarning, "D", "b.go", 4, 9, nil),
		f(reviewer.SeverityWarning, "N", "src/config.ts", 30, 5, nil),
		f(reviewer.SeverityWarning, "Out", "src/config.ts", 100, 120, nil),
	}
	wantBody := "## Reviewer — Changes requested\n\n**7 findings** · 1 critical · 4 warning · 1 suggestion\n\n" +
		"- 🔴 **Leaked key** (critical, bug) — `src/config.ts:10`\n  - Because Leaked key.\n  - _Suggestion:_ Use an env var\n" +
		"- 🟡 **Port** (warning, bug) — `src/config.ts:11-12`\n  - Because Port.\n" +
		"- 🔵 **Name** (suggestion, bug) — `src/config.ts:9`\n  - Because Name.\n" +
		"- • **Odd** (info, bug) — `src/config.ts:9`\n  - Because Odd.\n" +
		"- 🟡 **D** (warning, bug) — `b.go:4-9`\n  - Because D.\n" +
		"- 🟡 **N** (warning, bug) — `src/config.ts:30-5`\n  - Because N.\n" +
		"- 🟡 **Out** (warning, bug) — `src/config.ts:100-120`\n  - Because Out.\n\n_Posted via DevDigest._"
	wantComments := []domain.InlineComment{
		{Path: "src/config.ts", Line: 10, Body: "**Leaked key** (critical)\n\nBecause Leaked key.\n\n_Suggestion:_ Use an env var"},
		{Path: "src/config.ts", Line: 12, Body: "**Port** (warning)\n\nBecause Port."},
		{Path: "src/config.ts", Line: 9, Body: "**Name** (suggestion)\n\nBecause Name."},
		{Path: "src/config.ts", Line: 9, Body: "**Odd** (info)\n\nBecause Odd."},
		{Path: "b.go", Line: 6, Body: "**D** (warning)\n\nBecause D."},
		{Path: "src/config.ts", Line: 9, Body: "**N** (warning)\n\nBecause N."},
	}
	got := domain.ToReviewPayload(findings, reviewer.FailOnCritical, diff, "Reviewer")
	if got.Event != domain.EventRequestChanges {
		t.Errorf("event = %q, want %q", got.Event, domain.EventRequestChanges)
	}
	if got.Body != wantBody {
		t.Errorf("body =\n%q\nwant\n%q", got.Body, wantBody)
	}
	if diff := cmp.Diff(wantComments, got.Comments); diff != "" {
		t.Errorf("comments mismatch (-want +got):\n%s", diff)
	}
}
