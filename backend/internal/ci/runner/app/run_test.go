package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/app"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

type fakeFiles struct {
	files   map[string]string
	written map[string]string
}

func (f *fakeFiles) ReadFile(p string, max int64) ([]byte, error) {
	s, ok := f.files[p]
	switch {
	case !ok:
		return nil, errors.New("not found: " + p)
	case int64(len(s)) > max:
		return nil, app.ErrFileTooLarge
	}
	return []byte(s), nil
}

func (f *fakeFiles) ReadDir(p string) ([]string, error) {
	var names []string
	for k := range f.files {
		if rest, ok := strings.CutPrefix(k, p+"/"); ok && !strings.Contains(rest, "/") {
			names = append(names, rest)
		}
	}
	return names, nil
}

func (f *fakeFiles) WriteFile(p string, data []byte) error {
	if f.written == nil {
		f.written = map[string]string{}
	}
	f.written[p] = string(data)
	return nil
}

type env map[string]string

func (e env) Lookup(k string) (string, bool) { v, ok := e[k]; return v, ok }
func (env) Cwd() string                      { return "/ws" }

type fakeGitHub struct {
	diff    string
	reviews []app.ReviewPost
	reply   func(n int) error
}

func (g *fakeGitHub) FetchDiff(context.Context, string, app.PullRef) (string, error) {
	return g.diff, nil
}
func (g *fakeGitHub) PostComment(context.Context, string, app.PullRef, string) error { return nil }
func (g *fakeGitHub) PostReview(_ context.Context, _ string, _ app.PullRef, p app.ReviewPost) error {
	g.reviews = append(g.reviews, p)
	if g.reply != nil {
		return g.reply(len(g.reviews))
	}
	return nil
}

type nopLLM struct{}

func (nopLLM) For(string) reviewerapp.LLM { return nil }

type out struct {
	mu             sync.Mutex
	stdout, stderr []string
}

func (o *out) Stdout(l string) { o.mu.Lock(); o.stdout = append(o.stdout, l); o.mu.Unlock() }
func (o *out) Stderr(l string) { o.mu.Lock(); o.stderr = append(o.stderr, l); o.mu.Unlock() }

const diff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n x\n+y\n z\n"

func manifest(extra string) string {
	return "name: Rev\nmodel: m1\nsystem_prompt: be strict\nstrategy: map-reduce\nci_fail_on: warning\n" + extra
}

// run drives app.Run with a scripted engine and returns what the engine saw.
func run(t *testing.T, files *fakeFiles, gh *fakeGitHub, e env, engine app.Engine) (int, *out) {
	t.Helper()
	o := &out{}
	code := app.Run(t.Context(), app.Config{BuildID: "dev"}, app.Deps{
		Env: e, Files: files, GitHub: gh, LLM: nopLLM{}, Out: o, Engine: engine,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	return code, o
}

func baseEnv() env {
	return env{"GITHUB_REPOSITORY": "o/r", "PR_NUMBER": "7", "GITHUB_TOKEN": "tok-0123456789"}
}

func TestRun_EngineInput(t *testing.T) {
	files := &fakeFiles{files: map[string]string{
		"/ws/.devdigest/agents/rev.yaml": manifest("skills: [a, b]\n"),
		"/ws/.devdigest/skills/a.md":     "skill A\n",
		"/ws/.devdigest/skills/b.md":     "",
		"/ws/event.json":                 `{"pull_request":{"title":"T","body":"B"}}`,
	}}
	e := baseEnv()
	e["GITHUB_EVENT_PATH"] = "/ws/event.json"
	var got reviewerapp.Input
	engine := func(_ context.Context, in reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
		got = in
		return reviewer.Outcome{}, nil
	}
	code, _ := run(t, files, &fakeGitHub{diff: diff}, e, engine)
	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	want := reviewerapp.Input{
		SystemPrompt: "be strict", Strategy: reviewer.StrategyMapReduce, Skills: []string{"skill A\n", ""},
		PRDescription: "B", Task: "Review PR #7: T",
	}
	if diff := cmp.Diff(want, got, cmp.FilterPath(func(p cmp.Path) bool { return p.String() == "Diff" }, cmp.Ignore())); diff != "" {
		t.Errorf("engine input mismatch (-want +got):\n%s", diff)
	}
	if got.Diff.Raw != diff || len(got.Diff.Files) != 1 {
		t.Errorf("engine diff = %+v, want the parsed diff", got.Diff)
	}
	if got.Intent != nil || got.RepoMap != "" || got.Callers != "" || got.Specs != nil {
		t.Errorf("engine input carries local-review context: %+v", got)
	}
}

func TestRun_ManifestVersionBeyondSafeIntegerIsNullInTheArtifact(t *testing.T) {
	files := &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": manifest("version: 99999999999999999999\n")}}
	engine := func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
		return reviewer.Outcome{}, nil
	}
	code, o := run(t, files, &fakeGitHub{diff: diff}, baseEnv(), engine)
	if code != 0 {
		t.Fatalf("Run() = %d, want 0 (%v)", code, o.stderr)
	}
	if got := files.written["/ws/devdigest-result.json"]; !strings.Contains(got, `"manifest_version": null`) {
		t.Errorf("artifact = %s, want manifest_version null", got)
	}
	if want := "[agent-runner] trace manifest_version=100000000000000000000 model=m1 skills=none commit=unknown runner=2 build=dev"; len(o.stdout) == 0 || o.stdout[0] != want {
		t.Errorf("trace = %q, want %q", o.stdout, want)
	}
}

func TestRun_ReviewPostRetriesOnce(t *testing.T) {
	files := &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": manifest("")}}
	engine := func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
		return reviewer.Outcome{Review: reviewer.Review{Findings: []reviewer.Finding{{
			Severity: reviewer.SeverityWarning, Category: reviewer.CategoryBug, Title: "T", File: "a.go",
			StartLine: 2, EndLine: 2, Rationale: "R",
		}}}}, nil
	}
	tests := []struct {
		name      string
		replies   []error
		wantPosts int
		wantCode  int
		wantLine  string
	}{
		{name: "422 with comments retries without", replies: []error{&app.APIError{Status: 422, Message: "x"}, nil}, wantPosts: 2, wantCode: 1},
		{name: "422 twice fails", replies: []error{&app.APIError{Status: 422, Message: "first"}, &app.APIError{Status: 422, Message: "second"}}, wantPosts: 2, wantCode: 1, wantLine: "second"},
		{name: "other statuses are not retried", replies: []error{&app.APIError{Status: 500, Message: "boom"}}, wantPosts: 1, wantCode: 1, wantLine: "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeGitHub{diff: diff, reply: func(n int) error { return tc.replies[n-1] }}
			code, o := run(t, files, gh, baseEnv(), engine)
			if code != tc.wantCode || len(gh.reviews) != tc.wantPosts {
				t.Fatalf("Run() = %d with %d posts, want %d with %d", code, len(gh.reviews), tc.wantCode, tc.wantPosts)
			}
			if len(gh.reviews[0].Comments) != 1 {
				t.Errorf("first post has %d comments, want 1", len(gh.reviews[0].Comments))
			}
			if tc.wantPosts == 2 && len(gh.reviews[1].Comments) != 0 {
				t.Errorf("retry has %d comments, want none", len(gh.reviews[1].Comments))
			}
			if tc.wantLine != "" && (len(o.stderr) != 1 || o.stderr[0] != "[agent-runner] FAILED: "+tc.wantLine) {
				t.Errorf("stderr = %q, want the %q failure", o.stderr, tc.wantLine)
			}
			if _, ok := files.written["/ws/devdigest-result.json"]; !ok {
				t.Error("the artifact must be written before posting")
			}
		})
	}
}

func TestRun_OversizedConfigFilesFail(t *testing.T) {
	big := strings.Repeat("x", 1<<20+1)
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"manifest", map[string]string{"/ws/.devdigest/agents/rev.yaml": big}, "Agent manifest at /ws/.devdigest/agents/rev.yaml is larger than 1048576 bytes"},
		{"skill", map[string]string{"/ws/.devdigest/agents/rev.yaml": manifest("skills: [big]\n"), "/ws/.devdigest/skills/big.md": big},
			"Skill file for slug 'big' at /ws/.devdigest/skills/big.md is larger than 1048576 bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeGitHub{diff: diff}
			code, o := run(t, &fakeFiles{files: tc.files}, gh, baseEnv(), nil)
			if code != 1 || len(o.stderr) != 1 || o.stderr[0] != "[agent-runner] FAILED: "+tc.want {
				t.Errorf("Run() = %d, stderr %q; want 1 and %q", code, o.stderr, tc.want)
			}
		})
	}
}
