package app_test

// GitLab mode of the runner (spec FR16-FR30, FR38, FR48, NFR3, AC27 at use-case level): the
// environment checks come before the diff, every body sent to GitLab went through the note body
// rules once, the inline cap, the draft protocol and the job log.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/app"
	"dev-digest-go/backend/internal/platform/gitlab/notebody"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

const (
	glBase  = "1111111111111111111111111111111111111111"
	glHead  = "2222222222222222222222222222222222222222"
	glMerge = "3333333333333333333333333333333333333333"
	glToken = "glpat-secret-token-0123456789"
)

// glDiff has a renamed file (new.go: lines 1-2 and 4 are context, 3 is added) and a DevDigest file
// that must not be reviewed.
const glDiff = "diff --git a/old.go b/new.go\nsimilarity index 90%\nrename from old.go\nrename to new.go\n" +
	"--- a/old.go\n+++ b/new.go\n@@ -1,3 +1,4 @@\n a\n b\n+c\n d\n" +
	"diff --git a/.devdigest/agents/rev.yaml b/.devdigest/agents/rev.yaml\n--- a/.devdigest/agents/rev.yaml\n+++ b/.devdigest/agents/rev.yaml\n@@ -1,1 +1,2 @@\n x\n+secret-agent-line\n"

// glBigDiff adds 60 lines to each of the given files.
func glBigDiff(files ...string) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("diff --git a/" + f + " b/" + f + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + f + "\n@@ -0,0 +1,60 @@\n")
		for i := 1; i <= 60; i++ {
			b.WriteString("+line " + strconv.Itoa(i) + "\n")
		}
	}
	return b.String()
}

func glVars() env {
	return env{
		"DEVDIGEST_PLATFORM": "gitlab_ci", "GITLAB_CI": "true", "CI_PIPELINE_SOURCE": "merge_request_event",
		"CI_MERGE_REQUEST_IID": "7", "CI_PROJECT_ID": "42", "CI_MERGE_REQUEST_PROJECT_ID": "42",
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "42", "CI_PROJECT_PATH": "grp/proj", "CI_JOB_ID": "900",
		"CI_MERGE_REQUEST_DIFF_BASE_SHA": glBase, "CI_COMMIT_SHA": glHead,
		"CI_MERGE_REQUEST_TITLE": "My MR", "CI_MERGE_REQUEST_DESCRIPTION": "why",
		"DEVDIGEST_POST_AS": "github_review", "DEVDIGEST_GITLAB_TOKEN": glToken,
		"CI_API_V4_URL": "https://gl.example/api/v4",
	}
}

func glFiles() *fakeFiles {
	return &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": manifest("")}}
}

// recEnv records every variable the runner looks up.
type recEnv struct {
	env
	looked map[string]bool
}

func (r *recEnv) Lookup(k string) (string, bool) { r.looked[k] = true; return r.env.Lookup(k) }

type fakeDiffer struct {
	raw        string
	err        error
	calls      int
	base, head string
}

func (d *fakeDiffer) Diff(_ context.Context, base, head string) (string, error) {
	d.calls++
	d.base, d.head = base, head
	return d.raw, d.err
}

// fakeGitLab records every call as one line and plays a scripted answer per call line.
type fakeGitLab struct {
	calls   []string
	mr      app.MergeRequest
	mrErr   error
	token   string      // the token the runner must send; glToken when empty
	drafts  []app.Draft // already pending (foreign)
	fail    map[string]error
	next    int
	bodies  []string // every body sent: drafts and notes, in order
	created []app.NewDraft
}

func (g *fakeGitLab) rec(call string) error {
	g.calls = append(g.calls, call)
	return g.fail[call]
}

func (g *fakeGitLab) MR(_ context.Context, token string, ref app.MRRef) (app.MergeRequest, error) {
	want := glToken
	if g.token != "" {
		want = g.token
	}
	if token != want || ref != (app.MRRef{ProjectID: "42", IID: 7}) {
		return app.MergeRequest{}, fmt.Errorf("unexpected token or ref: %q %+v", token, ref)
	}
	g.calls = append(g.calls, "mr")
	return g.mr, g.mrErr
}

func (g *fakeGitLab) ListDrafts(context.Context, string, app.MRRef) ([]app.Draft, error) {
	return g.drafts, g.rec("list")
}

func (g *fakeGitLab) CreateDraft(_ context.Context, _ string, _ app.MRRef, d app.NewDraft) (app.Draft, error) {
	g.next++
	call := "create:" + strconv.Itoa(len(g.created)+1)
	g.bodies = append(g.bodies, d.Note)
	g.created = append(g.created, d)
	if err := g.rec(call); err != nil {
		return app.Draft{}, err
	}
	return app.Draft{ID: 100 + g.next}, nil
}

func (g *fakeGitLab) DeleteDraft(_ context.Context, _ string, _ app.MRRef, id int) error {
	return g.rec("delete:" + strconv.Itoa(id))
}

func (g *fakeGitLab) PublishDraft(_ context.Context, _ string, _ app.MRRef, id int) error {
	return g.rec("publish:" + strconv.Itoa(id))
}

func (g *fakeGitLab) BulkPublish(context.Context, string, app.MRRef) error { return g.rec("bulk") }

func (g *fakeGitLab) CreateNote(_ context.Context, _ string, _ app.MRRef, body string) error {
	g.bodies = append(g.bodies, body)
	return g.rec("note")
}

type glResult struct {
	code  int
	out   *out
	gl    *fakeGitLab
	diff  *fakeDiffer
	files *fakeFiles
	input reviewerapp.Input
}

func glEngine(findings ...reviewer.Finding) app.Engine {
	return func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
		return reviewer.Outcome{Review: reviewer.Review{Findings: findings}}, nil
	}
}

func headRefs() *app.DiffRefs {
	return &app.DiffRefs{BaseSHA: glBase, StartSHA: glMerge, HeadSHA: glHead}
}

func glRunApp(t *testing.T, e app.Env, files *fakeFiles, gl *fakeGitLab, d *fakeDiffer, engine app.Engine) glResult {
	t.Helper()
	o := &out{}
	r := glResult{out: o, gl: gl, diff: d, files: files}
	wrapped := func(ctx context.Context, in reviewerapp.Input, l reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error) {
		r.input = in
		return engine(ctx, in, l, ev, opts)
	}
	r.code = app.Run(t.Context(), app.Config{BuildID: "dev"}, app.Deps{
		Env: e, Files: files, GitLab: gl, GitDiff: d, LLM: nopLLM{}, Out: o, Engine: wrapped,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	return r
}

func glStd() (*fakeGitLab, *fakeDiffer) {
	return &fakeGitLab{mr: app.MergeRequest{IID: 7, DiffRefs: headRefs()}}, &fakeDiffer{raw: glDiff}
}

func finding(sev reviewer.Severity, file string, line int, title string) reviewer.Finding {
	return reviewer.Finding{
		Severity: sev, Category: reviewer.CategoryBug, Title: title, File: file,
		StartLine: line, EndLine: line, Rationale: "because " + title,
	}
}

func TestGitLab_EnvironmentFailuresComeBeforeTheDiff(t *testing.T) {
	tests := []struct {
		name string
		edit func(env)
		want string
	}{
		{"not a merge request pipeline", func(e env) { e["CI_PIPELINE_SOURCE"] = "push" }, "This job must run in a GitLab merge request pipeline"},
		{"outside GitLab", func(e env) { delete(e, "GITLAB_CI") }, "This job must run in a GitLab merge request pipeline"},
		{"fork", func(e env) { e["CI_MERGE_REQUEST_SOURCE_PROJECT_ID"] = "43" }, "Merge requests from forks are not reviewed"},
		{"token missing", func(e env) { delete(e, "DEVDIGEST_GITLAB_TOKEN") }, "DEVDIGEST_GITLAB_TOKEN is not set: add it as a masked, not protected CI/CD variable (scope api), or set post_as to none"},
		{"api url not https", func(e env) { e["CI_API_V4_URL"] = "http://gl.example/api/v4" }, "CI_API_V4_URL must be an https URL without credentials, query or fragment"},
		{"invalid sha", func(e env) { e["CI_COMMIT_SHA"] = "xyz" }, "CI_COMMIT_SHA is missing or invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := glVars()
			tc.edit(e)
			gl, d := glStd()
			files := glFiles()
			r := glRunApp(t, e, files, gl, d, glEngine())
			if r.code != 1 {
				t.Fatalf("Run() = %d, want 1", r.code)
			}
			if want := "[agent-runner] FAILED: " + tc.want; len(r.out.stderr) != 1 || r.out.stderr[0] != want {
				t.Errorf("stderr = %q, want %q", r.out.stderr, want)
			}
			if d.calls != 0 || len(gl.calls) != 0 || len(files.written) != 0 || r.input.Task != "" {
				t.Errorf("diff calls %d, GitLab calls %v, artifacts %v, engine task %q; want nothing at all", d.calls, gl.calls, files.written, r.input.Task)
			}
		})
	}
}

func TestGitLab_ModeIsOnlyDevdigestPlatform(t *testing.T) {
	// without DEVDIGEST_PLATFORM the GitHub behaviour applies: no GitLab port, no git diff
	e := baseEnv()
	for k, v := range glVars() {
		if k != "DEVDIGEST_PLATFORM" {
			e[k] = v
		}
	}
	gh := &fakeGitHub{diff: diff}
	gl, d := glStd()
	o := &out{}
	code := app.Run(t.Context(), app.Config{BuildID: "dev"}, app.Deps{
		Env: e, Files: glFiles(), GitHub: gh, GitLab: gl, GitDiff: d, LLM: nopLLM{}, Out: o, Engine: glEngine(),
	})
	if code != 0 || len(gh.reviews) != 1 || d.calls != 0 || len(gl.calls) != 0 {
		t.Errorf("Run() = %d, GitHub reviews %d, diff calls %d, GitLab calls %v; want 0, 1, 0, none", code, len(gh.reviews), d.calls, gl.calls)
	}
}

func TestGitLab_PostAsNoneReadsNoTokenAndCallsNoGitLab(t *testing.T) {
	e := glVars()
	e["DEVDIGEST_POST_AS"] = "none"
	delete(e, "DEVDIGEST_GITLAB_TOKEN")
	delete(e, "CI_API_V4_URL")
	re := &recEnv{env: e, looked: map[string]bool{}}
	d := &fakeDiffer{raw: glDiff}
	files := glFiles()
	o := &out{}
	code := app.Run(t.Context(), app.Config{BuildID: "dev"}, app.Deps{
		Env: re, Files: files, GitDiff: d, LLM: nopLLM{}, Out: o,
		Engine: glEngine(finding(reviewer.SeverityWarning, "new.go", 3, "T")),
	})
	if code != 1 { // ci_fail_on warning, one warning: the gate triggers
		t.Fatalf("Run() = %d, want 1 (gate), stderr %q", code, o.stderr)
	}
	if re.looked["DEVDIGEST_GITLAB_TOKEN"] || re.looked["CI_API_V4_URL"] {
		t.Errorf("post_as none read the token or the API URL: %v", re.looked)
	}
	if _, ok := files.written["/ws/devdigest-result.json"]; !ok {
		t.Error("the artifact must be written")
	}
	if last := o.stdout[len(o.stdout)-1]; last != "[agent-runner] findings=1 blockers=1 gateTriggered=true posted=none" {
		t.Errorf("result line = %q", last)
	}
}

func TestGitLab_DiffAndEngineInput(t *testing.T) {
	tests := []struct {
		name     string
		sourceSH string
		wantHead string
	}{
		{"head is the commit of the job", "", glHead},
		{"head is the source branch sha of a merged-results pipeline", glMerge, glMerge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := glVars()
			e["DEVDIGEST_POST_AS"] = "none"
			if tc.sourceSH != "" {
				e["CI_MERGE_REQUEST_SOURCE_BRANCH_SHA"] = tc.sourceSH
			}
			gl, d := glStd()
			r := glRunApp(t, e, glFiles(), gl, d, glEngine())
			if r.code != 0 {
				t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
			}
			if d.base != glBase || d.head != tc.wantHead {
				t.Errorf("Diff(%q, %q), want (%q, %q)", d.base, d.head, glBase, tc.wantHead)
			}
			if r.input.Task != "Review PR #7: My MR" || r.input.PRDescription != "why" {
				t.Errorf("engine task %q description %q", r.input.Task, r.input.PRDescription)
			}
			if strings.Contains(r.input.Diff.Raw, "secret-agent-line") || len(r.input.Diff.Files) != 1 || r.input.Diff.Files[0].Path != "new.go" {
				t.Errorf("engine diff = %+v, want only new.go (DevDigest's own files stripped)", r.input.Diff.Files)
			}
		})
	}
}

func TestGitLab_BlankDescriptionAndMissingTitle(t *testing.T) {
	e := glVars()
	e["DEVDIGEST_POST_AS"] = "none"
	e["CI_MERGE_REQUEST_DESCRIPTION"] = " \n\t"
	delete(e, "CI_MERGE_REQUEST_TITLE")
	gl, d := glStd()
	r := glRunApp(t, e, glFiles(), gl, d, glEngine())
	if r.input.Task != "Review PR #7: " || r.input.PRDescription != "" {
		t.Errorf("engine task %q description %q, want %q and empty", r.input.Task, r.input.PRDescription, "Review PR #7: ")
	}
}

type missingCommit struct{ text, sha string }

func (m *missingCommit) Error() string         { return m.text }
func (m *missingCommit) CommitMissing() string { return m.sha }

func TestGitLab_DiffFailures(t *testing.T) {
	const fr20 = "Base commit " + glBase + " is not in the job's clone; keep GIT_DEPTH: \"0\" in the job"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"missing commit, text as it is", fmt.Errorf("wrapped: %w", &missingCommit{fr20, glBase}), "wrapped: " + fr20},
		{"other failure", errors.New("git program not found"), "Could not compute the merge request diff: git program not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := glVars()
			gl, d := glStd()
			d.err = tc.err
			files := glFiles()
			r := glRunApp(t, e, files, gl, d, glEngine())
			if want := "[agent-runner] FAILED: " + tc.want; r.code != 1 || len(r.out.stderr) != 1 || r.out.stderr[0] != want {
				t.Errorf("Run() = %d, stderr %q, want 1 and %q", r.code, r.out.stderr, want)
			}
			if len(files.written) != 0 || len(gl.calls) != 0 {
				t.Errorf("artifacts %v, GitLab calls %v; want none", files.written, gl.calls)
			}
		})
	}
}

func TestGitLab_ArtifactFields(t *testing.T) {
	e := glVars()
	e["DEVDIGEST_POST_AS"] = "none"
	e["CI_MERGE_REQUEST_SOURCE_BRANCH_SHA"] = glMerge
	gl, d := glStd()
	files := glFiles()
	r := glRunApp(t, e, files, gl, d, glEngine())
	if r.code != 0 {
		t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	got := files.written["/ws/devdigest-result.json"]
	for _, want := range []string{
		`"pr_number": 7`, `"commit_sha": "` + glHead + `"`, `"repository": "grp/proj"`,
		`"repository_id": 42`, `"run_id": 900`, `"run_attempt": 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("artifact lacks %s:\n%s", want, got)
		}
	}
}

// FR30: the artifact carries no finding text, and the strings it does carry are redacted.
func TestGitLab_ArtifactHoldsNoSecret(t *testing.T) {
	const key = "sk-or-ABCDEFGH12345678"
	leaky := finding(reviewer.SeverityWarning, "new.go", 3, "key "+key)
	leaky.Rationale = "token " + glToken
	e := glVars()
	e["DEVDIGEST_POST_AS"] = "none"
	e["OPENROUTER_API_KEY"] = key
	e["CI_PROJECT_PATH"] = "grp/" + key
	files := &fakeFiles{files: map[string]string{
		"/ws/.devdigest/agents/rev.yaml": "name: Rev " + key + "\nmodel: m-" + key + "\nsystem_prompt: be strict\nstrategy: map-reduce\nci_fail_on: warning\n",
	}}
	gl, d := glStd()
	r := glRunApp(t, e, files, gl, d, glEngine(leaky))
	if r.code != 0 && r.code != 1 {
		t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	got := files.written["/ws/devdigest-result.json"]
	if got == "" {
		t.Fatalf("no artifact written, stderr %q", r.out.stderr)
	}
	for _, secret := range []string{key, glToken} {
		if strings.Contains(got, secret) {
			t.Errorf("artifact holds %q:\n%s", secret, got)
		}
	}
	for _, want := range []string{`"model": "m-[REDACTED]"`, `"repository": "grp/[REDACTED]"`, `"agent": "Rev [REDACTED]"`} {
		if !strings.Contains(got, want) {
			t.Errorf("artifact lacks %s:\n%s", want, got)
		}
	}
}

func TestGitLab_TraceLine(t *testing.T) {
	e := glVars()
	e["DEVDIGEST_POST_AS"] = "none"
	files := &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": manifest("")}}
	gl, d := glStd()
	r := glRunApp(t, e, files, gl, d, glEngine())
	want := "[agent-runner] trace manifest_version=none model=m1 skills=none commit=" + glHead + " runner=2 build=dev"
	if len(r.out.stdout) == 0 || r.out.stdout[0] != want {
		t.Errorf("trace = %q, want %q", r.out.stdout, want)
	}
}

func TestGitLab_ReviewDraftsAndBulkPublish(t *testing.T) {
	gl, d := glStd()
	files := glFiles()
	f := []reviewer.Finding{
		finding(reviewer.SeverityWarning, "new.go", 2, "ctx line"),
		finding(reviewer.SeverityCritical, "new.go", 3, "added line"),
		finding(reviewer.SeveritySuggestion, "nowhere.go", 1, "no anchor"),
	}
	r := glRunApp(t, glVars(), files, gl, d, glEngine(f...))
	if r.code != 1 {
		t.Fatalf("Run() = %d, want 1 (gate), stderr %q", r.code, r.out.stderr)
	}
	wantCalls := []string{"mr", "list", "create:1", "create:2", "create:3", "bulk"}
	if diff := cmp.Diff(wantCalls, gl.calls); diff != "" {
		t.Fatalf("GitLab calls mismatch (-want +got):\n%s", diff)
	}
	if gl.created[0].Position != nil || !strings.Contains(gl.created[0].Note, "**3 findings**") {
		t.Errorf("first draft = %+v, want the summary without position", gl.created[0])
	}
	// FR48 order: CRITICAL before WARNING; the finding without a line is in the summary only
	refs := *headRefs()
	wantPos := []*app.DraftPosition{
		{Refs: refs, NewPath: "new.go", OldPath: "old.go", NewLine: 3, OldLine: 0},
		{Refs: refs, NewPath: "new.go", OldPath: "old.go", NewLine: 2, OldLine: 2},
	}
	for i, want := range wantPos {
		if diff := cmp.Diff(want, gl.created[i+1].Position); diff != "" {
			t.Errorf("position of draft %d mismatch (-want +got):\n%s", i+1, diff)
		}
	}
	if !strings.Contains(gl.created[1].Note, "**added line** (critical)") || !strings.Contains(gl.created[2].Note, "**ctx line** (warning)") {
		t.Errorf("inline bodies = %q, %q", gl.created[1].Note, gl.created[2].Note)
	}
	if strings.Contains(gl.created[0].Note, "more finding") {
		t.Errorf("the summary has a cap section without a cap: %q", gl.created[0].Note)
	}
	if _, ok := files.written["/ws/devdigest-result.json"]; !ok {
		t.Error("the artifact must be written")
	}
}

func TestGitLab_ForeignDraftsArePublishedOneByOne(t *testing.T) {
	gl, d := glStd()
	gl.drafts = []app.Draft{{ID: 7}, {ID: 8}}
	f := []reviewer.Finding{finding(reviewer.SeverityWarning, "new.go", 2, "a"), finding(reviewer.SeveritySuggestion, "new.go", 3, "b")}
	r := glRunApp(t, glVars(), glFiles(), gl, d, glEngine(f...))
	if r.code != 1 {
		t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	want := []string{"mr", "list", "create:1", "create:2", "create:3", "publish:101", "publish:102", "publish:103"}
	if diff := cmp.Diff(want, gl.calls); diff != "" {
		t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLab_MergeRequestMovedPostsTheSummaryOnly(t *testing.T) {
	const skip = "[agent-runner] inline comments skipped: the merge request changed since this pipeline started"
	tests := []struct {
		name string
		refs *app.DiffRefs
	}{
		{"no diff_refs", nil},
		{"another head", &app.DiffRefs{BaseSHA: glBase, StartSHA: glMerge, HeadSHA: glMerge}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gl, d := glStd()
			gl.mr.DiffRefs = tc.refs
			d.raw = glBigDiff("f.go")
			var fs []reviewer.Finding
			for i := 1; i <= 52; i++ {
				fs = append(fs, finding(reviewer.SeverityWarning, "f.go", i, "t"+strconv.Itoa(i)))
			}
			r := glRunApp(t, glVars(), glFiles(), gl, d, glEngine(fs...))
			if r.code != 1 {
				t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
			}
			if diff := cmp.Diff([]string{"mr", "list", "create:1", "bulk"}, gl.calls); diff != "" {
				t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
			}
			if strings.Contains(gl.bodies[0], "not posted inline") {
				t.Errorf("a summary-only post has a cap section: %q", gl.bodies[0])
			}
			if !strings.Contains(strings.Join(r.out.stdout, "\n"), skip) || strings.Contains(strings.Join(r.out.stdout, "\n"), "capped") {
				t.Errorf("stdout = %q, want the skip line and no cap line", r.out.stdout)
			}
		})
	}
}

func TestGitLab_InlineCap(t *testing.T) {
	gl, d := glStd()
	d.raw = glBigDiff("a.go", "b.go")
	var fs []reviewer.Finding
	for i := 1; i <= 40; i++ { // 40 warnings in a.go, 1..40
		fs = append(fs, finding(reviewer.SeverityWarning, "a.go", i, "a"+strconv.Itoa(i)))
	}
	for i := 1; i <= 13; i++ { // 13 critical in b.go, 1..13: they come first
		fs = append(fs, finding(reviewer.SeverityCritical, "b.go", i, "b"+strconv.Itoa(i)))
	}
	fs = append(fs, reviewer.Finding{ // no anchor line: summary only, not counted
		Severity: reviewer.SeverityCritical, Category: reviewer.CategoryBug, Title: "global", File: "a.go",
		StartLine: 500, EndLine: 500, Rationale: "r",
	})
	r := glRunApp(t, glVars(), glFiles(), gl, d, glEngine(fs...))
	if r.code != 1 {
		t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	if len(gl.created) != 51 {
		t.Fatalf("created %d drafts, want 51 (summary + 50)", len(gl.created))
	}
	// 53 anchored findings: 13 critical b.go then 37 warnings a.go (a1..a37) posted; a38..a40 not
	for i := 1; i <= 13; i++ {
		if got := gl.created[i].Position; got.NewPath != "b.go" || got.NewLine != i {
			t.Fatalf("draft %d at %s:%d, want b.go:%d", i, got.NewPath, got.NewLine, i)
		}
	}
	if got := gl.created[50].Position; got.NewPath != "a.go" || got.NewLine != 37 {
		t.Errorf("last draft at %s:%d, want a.go:37", got.NewPath, got.NewLine)
	}
	summary := gl.created[0].Note
	wantSection := "_3 more findings not posted inline (limit: 50 inline threads per run):_\n" +
		"- 🟡 **a38** (warning) — `a.go:38`\n- 🟡 **a39** (warning) — `a.go:39`\n- 🟡 **a40** (warning) — `a.go:40`\n\n_Posted via DevDigest._"
	if !strings.HasSuffix(summary, "\n\n"+wantSection) {
		t.Errorf("summary does not end with the cap section:\n%s", summary)
	}
	if !strings.Contains(summary, "**global**") || strings.Contains(summary, "a.go:500\n- 🟡") {
		t.Errorf("the summary must list every finding once in its bullets:\n%s", summary)
	}
	if got := r.out.stdout[len(r.out.stdout)-2]; got != "[agent-runner] inline comments capped: posted=50 not_posted=3" {
		t.Errorf("cap line = %q (stdout %q)", got, r.out.stdout)
	}
	if diff := cmp.Diff("bulk", gl.calls[len(gl.calls)-1]); diff != "" {
		t.Errorf("last call mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLab_RefusedPositionRetriesWithTheSummaryAlone(t *testing.T) {
	gl, d := glStd()
	d.raw = glBigDiff("f.go")
	var fs []reviewer.Finding
	for i := 1; i <= 52; i++ {
		fs = append(fs, finding(reviewer.SeverityWarning, "f.go", i, "t"+strconv.Itoa(i)))
	}
	gl.fail = map[string]error{"create:3": &app.GitLabAPIError{Status: 400, Message: "GitLab API error posting MR review (/x): 400 line_code"}}
	files := glFiles()
	r := glRunApp(t, glVars(), files, gl, d, glEngine(fs...))
	if r.code != 1 { // the gate triggers; the retry itself succeeded
		t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	want := []string{"mr", "list", "create:1", "create:2", "create:3", "delete:101", "delete:102", "create:4", "bulk"}
	if diff := cmp.Diff(want, gl.calls); diff != "" {
		t.Fatalf("GitLab calls mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(gl.bodies[3], "not posted inline") || gl.created[3].Position != nil {
		t.Errorf("the re-created draft = %+v, want the summary without cap section and position", gl.created[3])
	}
	if strings.Contains(strings.Join(r.out.stdout, "\n"), "capped") {
		t.Errorf("stdout = %q, a retried post has no cap line", r.out.stdout)
	}
}

func TestGitLab_RefusedPositionWithForeignDraftsPublishesOnlyTheSummary(t *testing.T) {
	gl, d := glStd()
	gl.drafts = []app.Draft{{ID: 9}}
	gl.fail = map[string]error{"create:2": &app.GitLabAPIError{Status: 400, Message: "x"}}
	r := glRunApp(t, glVars(), glFiles(), gl, d, glEngine(finding(reviewer.SeverityWarning, "new.go", 2, "a")))
	if r.code != 1 {
		t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	want := []string{"mr", "list", "create:1", "create:2", "delete:101", "create:3", "publish:103"}
	if diff := cmp.Diff(want, gl.calls); diff != "" {
		t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLab_UndeletableDraftNeverGoesOutInABulk(t *testing.T) {
	gl, d := glStd()
	gl.fail = map[string]error{
		"create:2":   &app.GitLabAPIError{Status: 400, Message: "x"},
		"delete:101": errors.New("delete failed"),
	}
	r := glRunApp(t, glVars(), glFiles(), gl, d, glEngine(finding(reviewer.SeverityWarning, "new.go", 2, "a")))
	if r.code != 1 {
		t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	for _, c := range gl.calls {
		if c == "bulk" {
			t.Fatalf("GitLab calls %v: a bulk publish would publish the draft that could not be deleted", gl.calls)
		}
	}
	if gl.calls[len(gl.calls)-1] != "publish:103" {
		t.Errorf("GitLab calls %v, want the re-created summary published alone", gl.calls)
	}
}

func TestGitLab_PostingFailuresCleanUpAndKeepTheArtifact(t *testing.T) {
	two := []reviewer.Finding{finding(reviewer.SeverityWarning, "new.go", 2, "a"), finding(reviewer.SeveritySuggestion, "new.go", 3, "b")}
	api := func(status int) error {
		return &app.GitLabAPIError{Status: status, Message: "GitLab API error posting MR review (/p): " + strconv.Itoa(status) + " boom"}
	}
	tests := []struct {
		name      string
		foreign   []app.Draft
		fail      map[string]error
		mrErr     error
		wantCalls []string
		wantLine  string
	}{
		{"MR read fails", nil, nil, api(500), []string{"mr"}, "boom"},
		{"drafts list fails", nil, map[string]error{"list": api(403)}, nil, []string{"mr", "list"}, "403 boom"},
		{"summary draft refused", nil, map[string]error{"create:1": api(400)}, nil, []string{"mr", "list", "create:1"}, "400 boom"},
		{"429 on a create is a failure, not retried", nil, map[string]error{"create:2": api(429)}, nil,
			[]string{"mr", "list", "create:1", "create:2", "delete:101"}, "429 boom"},
		{"bulk publish fails", nil, map[string]error{"bulk": api(500)}, nil,
			[]string{"mr", "list", "create:1", "create:2", "create:3", "bulk", "delete:101", "delete:102", "delete:103"}, "500 boom"},
		{"single publish fails midway", []app.Draft{{ID: 1}}, map[string]error{"publish:102": api(500)}, nil,
			[]string{"mr", "list", "create:1", "create:2", "create:3", "publish:101", "publish:102", "delete:102", "delete:103"}, "500 boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gl, d := glStd()
			gl.drafts, gl.fail, gl.mrErr = tc.foreign, tc.fail, tc.mrErr
			files := glFiles()
			r := glRunApp(t, glVars(), files, gl, d, glEngine(two...))
			if r.code != 1 {
				t.Fatalf("Run() = %d, want 1", r.code)
			}
			if diff := cmp.Diff(tc.wantCalls, gl.calls); diff != "" {
				t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
			}
			if len(r.out.stderr) != 1 || !strings.HasPrefix(r.out.stderr[0], "[agent-runner] FAILED: GitLab API error posting MR review") || !strings.HasSuffix(r.out.stderr[0], tc.wantLine) {
				t.Errorf("stderr = %q, want the GitLab failure ending %q", r.out.stderr, tc.wantLine)
			}
			if _, ok := files.written["/ws/devdigest-result.json"]; !ok {
				t.Error("a posting failure keeps the artifact")
			}
		})
	}
}

func TestGitLab_PRCommentIsOneNote(t *testing.T) {
	e := glVars()
	e["DEVDIGEST_POST_AS"] = "pr_comment"
	gl, d := glStd()
	d.raw = glBigDiff("f.go")
	var fs []reviewer.Finding
	for i := 1; i <= 52; i++ {
		fs = append(fs, finding(reviewer.SeverityWarning, "f.go", i, "t"+strconv.Itoa(i)))
	}
	r := glRunApp(t, e, glFiles(), gl, d, glEngine(fs...))
	if r.code != 1 {
		t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	if diff := cmp.Diff([]string{"note"}, gl.calls); diff != "" {
		t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(gl.bodies[0], "not posted inline") || !strings.Contains(gl.bodies[0], "t52") {
		t.Errorf("the note must list every finding and no cap section: %q", gl.bodies[0])
	}
	if last := r.out.stdout[len(r.out.stdout)-1]; !strings.HasSuffix(last, "posted=pr_comment") {
		t.Errorf("result line = %q", last)
	}
}

func TestGitLab_NoteFailureIsAPostingFailure(t *testing.T) {
	e := glVars()
	e["DEVDIGEST_POST_AS"] = "pr_comment"
	gl, d := glStd()
	gl.fail = map[string]error{"note": &app.GitLabAPIError{Status: 500, Message: "GitLab API error posting MR comment (/n): 500 x"}}
	files := glFiles()
	r := glRunApp(t, e, files, gl, d, glEngine())
	if r.code != 1 || len(r.out.stderr) != 1 || r.out.stderr[0] != "[agent-runner] FAILED: GitLab API error posting MR comment (/n): 500 x" {
		t.Errorf("Run() = %d, stderr %q", r.code, r.out.stderr)
	}
	if _, ok := files.written["/ws/devdigest-result.json"]; !ok {
		t.Error("a posting failure keeps the artifact")
	}
}

// Every body that reaches GitLab - summary draft, inline drafts, the summary re-created after a
// refused position, and the pr_comment note - has passed the note body rules once (FR38, NFR3).
func TestGitLab_EveryBodyPassedTheNoteBodyRules(t *testing.T) {
	evil := reviewer.Finding{
		Severity: reviewer.SeverityCritical, Category: reviewer.CategoryBug, File: "new.go", StartLine: 3, EndLine: 3,
		Title:     "ping @alice <b>x</b> ![i](http://e/x.png)",
		Rationale: "/approve\n> /merge\n- /label ~x\n<img src=x onerror=1>",
	}
	sug := "/close\r\n@bob"
	evil.Suggestion = &sug
	runs := []struct {
		name   string
		postAs string
		fail   map[string]error
	}{
		{"review", "github_review", nil},
		{"review, retry after a refused position", "github_review", map[string]error{"create:2": &app.GitLabAPIError{Status: 400, Message: "x"}}},
		{"comment", "pr_comment", nil},
	}
	for _, tc := range runs {
		t.Run(tc.name, func(t *testing.T) {
			e := glVars()
			e["DEVDIGEST_POST_AS"] = tc.postAs
			gl, d := glStd()
			gl.fail = tc.fail
			r := glRunApp(t, e, glFiles(), gl, d, glEngine(evil))
			if r.code != 1 {
				t.Fatalf("Run() = %d, stderr %q", r.code, r.out.stderr)
			}
			if len(gl.bodies) == 0 {
				t.Fatal("no body was sent")
			}
			for i, b := range gl.bodies {
				if got := notebody.Apply(b); got != b {
					t.Errorf("body %d is not a fixed point of the note body rules:\nsent: %q\nrule: %q", i, b, got)
				}
				for _, bad := range []string{"@alice", "@bob", "<b>", "<img", "![i]", "\n/approve", "\n> /merge", "\n- /label"} {
					if strings.Contains(b, bad) {
						t.Errorf("body %d still holds %q: %q", i, bad, b)
					}
				}
			}
		})
	}
}

func TestGitLab_SecretsNeverReachABodyOrTheLog(t *testing.T) {
	leaky := finding(reviewer.SeverityWarning, "new.go", 3, "key "+glToken)
	leaky.Rationale = "token " + glToken + " and sk-or-ABCDEFGH12345678"
	e := glVars()
	e["OPENROUTER_API_KEY"] = "sk-or-ABCDEFGH12345678"
	gl, d := glStd()
	gl.fail = map[string]error{"bulk": &app.GitLabAPIError{Status: 500, Message: "GitLab API error posting MR review (/p): 500 echo " + glToken}}
	r := glRunApp(t, e, glFiles(), gl, d, glEngine(leaky))
	for i, b := range gl.bodies {
		if strings.Contains(b, glToken) || strings.Contains(b, "sk-or-ABCDEFGH12345678") {
			t.Errorf("body %d holds a secret: %q", i, b)
		}
	}
	for _, l := range append(r.out.stdout, r.out.stderr...) {
		if strings.Contains(l, glToken) {
			t.Errorf("log line holds the token: %q", l)
		}
	}
	if len(r.out.stderr) != 1 || !strings.HasSuffix(r.out.stderr[0], "500 echo [REDACTED]") {
		t.Errorf("stderr = %q, want the redacted failure", r.out.stderr)
	}
}

func TestGitLab_ShortTokenIsRedactedToo(t *testing.T) {
	e := glVars()
	e["DEVDIGEST_GITLAB_TOKEN"] = "tok"
	gl, d := glStd()
	gl.token = "tok"
	gl.mrErr = &app.GitLabAPIError{Status: 500, Message: "denied for tok"}
	r := glRunApp(t, e, glFiles(), gl, d, glEngine())
	if want := "[agent-runner] FAILED: denied for [REDACTED]"; r.code != 1 || len(r.out.stderr) != 1 || r.out.stderr[0] != want {
		t.Errorf("Run() = %d, stderr %q, want %q", r.code, r.out.stderr, want)
	}
}

func TestGitLab_FailureLineIsSafeForTheGitLabLog(t *testing.T) {
	bad := "boom\x1b[31m red\x1b[0m\r\nsection_start:1700000000:x\u0085\x00 " + strings.Repeat("é", 800)
	gl, d := glStd()
	gl.mrErr = &app.GitLabAPIError{Status: 500, Message: bad}
	r := glRunApp(t, glVars(), glFiles(), gl, d, glEngine())
	if len(r.out.stderr) != 1 {
		t.Fatalf("stderr = %q", r.out.stderr)
	}
	line := r.out.stderr[0]
	if len(line) > app.MaxFailureBytes || !strings.HasPrefix(line, "[agent-runner] FAILED: boom red") || !strings.HasSuffix(line, "…") {
		t.Errorf("failure line = %q (%d bytes), want it safe and cut at %d bytes", line, len(line), app.MaxFailureBytes)
	}
	for _, bad := range []string{"\x1b", "\n", "\r", "section_start:", "\u0085", "\x00"} {
		if strings.Contains(line, bad) {
			t.Errorf("failure line holds %q: %q", bad, line)
		}
	}
	if !strings.Contains(line, "↵") {
		t.Errorf("failure line = %q, want line breaks shown as ↵", line)
	}
	if strings.Contains(line, "::") && strings.Contains(line, "\u200b") {
		t.Errorf("GitLab mode must not break '::' sequences: %q", line)
	}
}

func TestGitLab_NeedsItsPorts(t *testing.T) {
	e := glVars()
	o := &out{}
	code := app.Run(t.Context(), app.Config{BuildID: "dev"}, app.Deps{
		Env: e, Files: glFiles(), GitDiff: &fakeDiffer{raw: glDiff}, LLM: nopLLM{}, Out: o, Engine: glEngine(),
	})
	if code != 1 || len(o.stderr) != 1 || o.stderr[0] != "[agent-runner] FAILED: This runner cannot call GitLab" {
		t.Errorf("Run() = %d, stderr %q", code, o.stderr)
	}
}

// brokenGitLab is the port the composition root hands when the GitLab client could not be built.
type brokenGitLab struct {
	*fakeGitLab
	err error
}

func (b brokenGitLab) Unusable() error { return b.err }

func TestGitLab_UnusableClientFailsBeforeTheDiffAndTheModel(t *testing.T) {
	const reason = "CI_SERVER_TLS_CA_FILE: no such file"
	tests := []struct {
		name   string
		postAs string
		want   string // "" = the run goes on
	}{
		{"review", "github_review", "[agent-runner] FAILED: " + reason},
		{"comment", "pr_comment", "[agent-runner] FAILED: " + reason},
		{"none never needs the client", "none", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := glVars()
			e["DEVDIGEST_POST_AS"] = tc.postAs
			gl, d := glStd()
			files := glFiles()
			o := &out{}
			var engineCalls int
			engine := func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
				engineCalls++
				return reviewer.Outcome{}, nil
			}
			code := app.Run(t.Context(), app.Config{BuildID: "dev"}, app.Deps{
				Env: e, Files: files, GitLab: brokenGitLab{fakeGitLab: gl, err: errors.New(reason)}, GitDiff: d,
				LLM: nopLLM{}, Out: o, Engine: engine,
			})
			if tc.want == "" {
				if code != 0 || engineCalls != 1 || d.calls != 1 {
					t.Errorf("Run() = %d, engine calls %d, diff calls %d; want 0, 1, 1", code, engineCalls, d.calls)
				}
				return
			}
			if code != 1 {
				t.Errorf("Run() = %d, want 1", code)
			}
			if len(o.stderr) != 1 || o.stderr[0] != tc.want {
				t.Errorf("stderr = %q, want %q", o.stderr, tc.want)
			}
			if len(o.stdout) != 0 || d.calls != 0 || engineCalls != 0 || len(gl.calls) != 0 || len(files.written) != 0 {
				t.Errorf("stdout %q, diff calls %d, model calls %d, GitLab calls %v, artifacts %v; want nothing at all",
					o.stdout, d.calls, engineCalls, gl.calls, files.written)
			}
		})
	}
}
