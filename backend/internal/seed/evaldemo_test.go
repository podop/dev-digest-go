package seed

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestEvalDemoPatches_MatchNodeGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/pr483_patches.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(want) != 4 {
		t.Fatalf("golden has %d patches, want 4", len(want))
	}
	if diff := cmp.Diff(want, evalDemoPatches()); diff != "" {
		t.Errorf("evalDemoPatches() mismatch against Node SEED_EVAL_DEMO_PATCHES (-want +got):\n%s", diff)
	}
}

// The lines, fates and severities below were printed by Node's lineOf/DEMO_FINDINGS.
func TestEvalDemoReview_MatchesNode(t *testing.T) {
	agent := AgentRef{ID: "a1", Provider: "prov", Model: "mod"}
	d, err := evalDemoReview("ws", "pr483", agent)
	if err != nil {
		t.Fatalf("evalDemoReview() error = %v", err)
	}
	type row struct {
		File     string
		Line     int
		Severity string
		Fate     string
	}
	want := []row{
		{evalDemoExportPath, 9, "CRITICAL", fateAccepted},
		{evalDemoExportPath, 11, "CRITICAL", fateAccepted},
		{evalDemoExportPath, 20, "CRITICAL", fateAccepted},
		{evalDemoExportPath, 7, "CRITICAL", fateAccepted},
		{evalDemoExportPath, 14, "WARNING", fateAccepted},
		{evalDemoStripePath, 10, "WARNING", fateAccepted},
		{evalDemoSessionPath, 5, "CRITICAL", fateAccepted},
		{evalDemoSessionPath, 9, "CRITICAL", fateAccepted},
		{evalDemoStripePath, 11, "WARNING", fatePending},
		{evalDemoExportPath, 12, "SUGGESTION", fateDismissed},
		{evalDemoExportPath, 10, "SUGGESTION", fateDismissed},
		{evalDemoSessionPath, 5, "SUGGESTION", fateDismissed},
		{evalDemoInvoicePath, 7, "WARNING", fateDismissed},
		{evalDemoInvoicePath, 6, "SUGGESTION", fateDismissed},
	}
	var got []row
	for _, f := range d.Findings {
		fate := fatePending
		switch {
		case f.Accepted && f.Dismissed:
			t.Fatalf("finding %q is both accepted and dismissed", f.Title)
		case f.Accepted:
			fate = fateAccepted
		case f.Dismissed:
			fate = fateDismissed
		}
		if f.StartLine != f.EndLine {
			t.Errorf("finding %q range = %d-%d, want one line", f.Title, f.StartLine, f.EndLine)
		}
		got = append(got, row{f.File, f.StartLine, f.Severity, fate})
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("evalDemoReview() findings mismatch (-want +got):\n%s", diff)
	}

	wantRun := DemoRun{
		Provider: "prov", Model: "mod", DurationMs: 21350, TokensIn: 5870, TokensOut: 1460, CostUSD: 0.002412,
		Status: "done", Source: "local", FindingsCount: 14, Grounding: "14/14 passed", Score: 34, Blockers: 6,
	}
	if diff := cmp.Diff(wantRun, d.Run); diff != "" {
		t.Errorf("evalDemoReview() run mismatch (-want +got):\n%s", diff)
	}
	if d.AgentID != "a1" || d.Model != "seed-eval-demo" || d.Kind != "review" || d.Verdict != "request_changes" || d.Score != 34 {
		t.Errorf("evalDemoReview() = agent %q model %q kind %q verdict %q score %d, want a1/seed-eval-demo/review/request_changes/34",
			d.AgentID, d.Model, d.Kind, d.Verdict, d.Score)
	}
}

// Every finding must point at an added line of the stored hunk that holds its anchor.
func TestEvalDemoFindings_AnchorsLandInTheHunk(t *testing.T) {
	patches := evalDemoPatches()
	for _, f := range evalDemoFindings {
		line, err := lineOf(f.file, f.at)
		if err != nil {
			t.Errorf("lineOf(%q, %q) error = %v", f.file, f.at, err)
			continue
		}
		hunk := strings.Split(patches[f.file], "\n")[1:] // drop the @@ header
		if got := hunk[line-1]; !strings.HasPrefix(got, "+") || !strings.Contains(got, f.at) {
			t.Errorf("lineOf(%q, %q) = %d, hunk line %q does not hold the anchor", f.file, f.at, line, got)
		}
	}
	if _, err := lineOf(evalDemoExportPath, "no such text"); err == nil {
		t.Error("lineOf() with a stale anchor error = nil, want an error")
	}
}

func TestEvalDemoPullRequest(t *testing.T) {
	if got := evalDemoAdditions(); got != 55 {
		t.Errorf("evalDemoAdditions() = %d, want 55 (Node countAdditions)", got)
	}
}

func TestSeederRun_EvalDemoGuard(t *testing.T) {
	tests := []struct {
		name         string
		noSecurity   bool
		prExists     bool
		wantWritten  bool
		wantLookedUp bool
	}{
		{name: "PR 483 missing", wantWritten: true, wantLookedUp: true},
		{name: "PR 483 exists", prExists: true, wantLookedUp: true},
		{name: "Security Reviewer missing", noSecurity: true, wantLookedUp: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{patches: map[string]string{}, workspaceExists: true, occupied: true, noSecurity: tc.noSecurity, evalPRExists: tc.prExists}
			res, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context())
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := tx.evalRunCount == 1; got != tc.wantWritten {
				t.Fatalf("eval demo written = %v, want %v", got, tc.wantWritten)
			}
			wantReviews, wantFindings, wantPRs := 1, 2, 1 // the PR #482 demo alone
			if tc.wantWritten {
				wantReviews, wantFindings, wantPRs = 2, 16, 2
			}
			if res.Reviews != wantReviews || res.Findings != wantFindings || res.PullRequests != wantPRs {
				t.Errorf("Run() reviews/findings/prs = %d/%d/%d, want %d/%d/%d",
					res.Reviews, res.Findings, res.PullRequests, wantReviews, wantFindings, wantPRs)
			}
			if tc.wantWritten && tx.evalDemo.AgentID != "agent:Security Reviewer" {
				t.Errorf("eval demo agent id = %q, want agent:Security Reviewer", tx.evalDemo.AgentID)
			}
			if tc.prExists || tc.noSecurity {
				if res.Files != 4 || res.Commits != 1 {
					t.Errorf("Run() files/commits = %d/%d, want 4/1 (no eval demo rows)", res.Files, res.Commits)
				}
			}
		})
	}
}

func TestSeederRun_EvalDemoErrorNamesTheStep(t *testing.T) {
	tests := []struct{ failOn, want string }{
		{"find-agent:Security Reviewer@ws", "seed: find Security Reviewer: boom"},
		{"pr483:repo", "seed: pull request 483: boom"},
		{"file:pr483/src/auth/session.ts", "seed: pr file src/auth/session.ts: boom"},
		{"commit:pr483/c3d4e5f6a7b8", "seed: pr commit: boom"},
		{"demo-review:pr483/agent:Security Reviewer", "seed: eval demo review: boom"},
	}
	for _, tc := range tests {
		t.Run(tc.failOn, func(t *testing.T) {
			tx := &fakeTx{patches: map[string]string{}, failOn: tc.failOn}
			_, err := New(fakeStore{tx}, slog.New(slog.DiscardHandler)).Run(t.Context())
			if err == nil || err.Error() != tc.want {
				t.Errorf("Run() error = %v, want %q", err, tc.want)
			}
		})
	}
}
