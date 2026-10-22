package domain

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestBuildRunDocList(t *testing.T) {
	skillA := SkillPaths{SkillID: "s1", SkillName: "Alpha", Paths: []string{"docs/b.md", "docs/c.md"}}
	skillB := SkillPaths{SkillID: "s2", SkillName: "Beta", Paths: []string{"docs/c.md", "docs/d.md"}}
	tests := []struct {
		name   string
		agent  []string
		skills []SkillPaths
		want   []RunDocRef
	}{
		{name: "nothing", want: nil},
		{
			name:  "agent then skills in order, first origin wins",
			agent: []string{"docs/a.md", "docs/b.md"}, skills: []SkillPaths{skillA, skillB},
			want: []RunDocRef{
				{Path: "docs/a.md", Origin: RunOrigin{Kind: OriginAgent}},
				{Path: "docs/b.md", Origin: RunOrigin{Kind: OriginAgent}},
				{Path: "docs/c.md", Origin: RunOrigin{Kind: OriginSkill, SkillID: "s1", SkillName: "Alpha"}},
				{Path: "docs/d.md", Origin: RunOrigin{Kind: OriginSkill, SkillID: "s2", SkillName: "Beta"}},
			},
		},
		{
			name:  "duplicate inside the agent list",
			agent: []string{"docs/a.md", "docs/a.md"},
			want:  []RunDocRef{{Path: "docs/a.md", Origin: RunOrigin{Kind: OriginAgent}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildRunDocList(tc.agent, tc.skills)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BuildRunDocList(%v, %v) mismatch (-want +got):\n%s", tc.agent, tc.skills, diff)
			}
		})
	}
}

func TestRunTextTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "empty", text: "", want: 0},
		{name: "ascii rounds up", text: "abcde", want: 2},
		{name: "astral counts two units", text: "😀😀", want: 1}, // 4 UTF-16 units
		{name: "bmp counts one unit", text: "ééééé", want: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RunTextTokens(tc.text); got != tc.want {
				t.Errorf("RunTextTokens(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func included(path string, tokens int) RunDoc {
	text := strings.Repeat("x", tokens*4)
	return RunDoc{Path: path, Status: RunIncluded, Tokens: tokens, Text: &text, Source: RunSourceRepo}
}

func statuses(docs []RunDoc) []RunStatus {
	out := make([]RunStatus, len(docs))
	for i, d := range docs {
		out[i] = d.Status
	}
	return out
}

func TestApplyBudget(t *testing.T) {
	skipped := RunDoc{Path: "gone.md", Status: RunMissing}
	tests := []struct {
		name       string
		docs       []RunDoc
		budget     int
		wantStatus []RunStatus
		wantTotal  int
	}{
		{
			name: "three fit exactly under the budget", budget: 16_000, wantTotal: 15_000,
			docs:       []RunDoc{included("a", 7000), included("b", 7000), included("c", 1000)},
			wantStatus: []RunStatus{RunIncluded, RunIncluded, RunIncluded},
		},
		{
			name: "second does not fit: it and every later one are over budget", budget: 16_000, wantTotal: 7000,
			docs:       []RunDoc{included("a", 7000), included("b", 10_000), included("c", 1000)},
			wantStatus: []RunStatus{RunIncluded, RunOverBudget, RunOverBudget},
		},
		{
			name: "total equal to the budget fits", budget: 100, wantTotal: 100,
			docs:       []RunDoc{included("a", 60), included("b", 40)},
			wantStatus: []RunStatus{RunIncluded, RunIncluded},
		},
		{
			name: "skipped documents before the cut keep their status, after it they are cut too", budget: 10, wantTotal: 8,
			docs:       []RunDoc{skipped, included("a", 8), included("b", 3), skipped},
			wantStatus: []RunStatus{RunMissing, RunIncluded, RunOverBudget, RunOverBudget},
		},
		{name: "empty", budget: 10, wantStatus: []RunStatus{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, total := ApplyBudget(tc.docs, tc.budget)
			if diff := cmp.Diff(tc.wantStatus, statuses(got)); diff != "" {
				t.Errorf("ApplyBudget(%d docs, %d) statuses mismatch (-want +got):\n%s", len(tc.docs), tc.budget, diff)
			}
			if total != tc.wantTotal {
				t.Errorf("ApplyBudget(%d docs, %d) total = %d, want %d", len(tc.docs), tc.budget, total, tc.wantTotal)
			}
			for _, d := range got {
				if d.Status != RunIncluded && d.Text != nil {
					t.Errorf("ApplyBudget: %s is %s but keeps its text", d.Path, d.Status)
				}
			}
		})
	}
}

func TestApplyBudget_KeepsInput(t *testing.T) {
	docs := []RunDoc{included("a", 9), included("b", 9)}
	ApplyBudget(docs, 10)
	if docs[1].Status != RunIncluded || docs[1].Text == nil {
		t.Errorf("ApplyBudget changed its input: %+v", docs[1])
	}
}

func TestRunLogLine(t *testing.T) {
	docs := []RunDoc{{Status: RunIncluded}, {Status: RunTooLarge}, {Status: RunMissing}}
	want := "project context: 1 included, 2 skipped · ~42 tokens"
	if got := RunLogLine(docs, 42); got != want {
		t.Errorf("RunLogLine(%d docs, 42) = %q, want %q", len(docs), got, want)
	}
}

func TestNewRunDoc(t *testing.T) {
	ref := RunDocRef{Path: "docs/specs/a.md", Origin: RunOrigin{Kind: OriginAgent}}
	got := NewRunDoc(ref, RunSourceStore, "12345678")
	if got.Status != RunIncluded || got.Tokens != 2 || got.DocType != DocTypeSpecs || got.Source != RunSourceStore ||
		got.Text == nil || *got.Text != "12345678" {
		t.Errorf("NewRunDoc = %+v, want included specs store doc of 2 tokens with text", got)
	}
	skip := SkippedRunDoc(ref, RunSourceRepo, RunMissing)
	if skip.Text != nil || skip.Tokens != 0 || skip.Status != RunMissing {
		t.Errorf("SkippedRunDoc = %+v, want no text, 0 tokens, missing", skip)
	}
}
