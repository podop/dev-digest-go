package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/adapters/runnerbin"
	cidomain "dev-digest-go/backend/internal/ci/domain"
	runnerenv "dev-digest-go/backend/internal/ci/runner/adapters/env"
	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
	evalsdomain "dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/llm"
	reviewerllm "dev-digest-go/backend/internal/reviewer/adapters/llmcall"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Phase 6 component tests, lane X3: the CI runner as the export ships it (AC6049, AC6052, AC6055),
// secrets (AC6059), the agent delete cascade (AC6064) and the LLM call counts (AC6067). Every
// helper of this file starts with ops.

// ---- a recording LLM double ----

// opsLLM is an llm.Adapter that records every attempt and the key it was given, and answers with
// the function the test sets.
type opsLLM struct {
	needsKey bool
	answer   func(key string, at llm.Attempt) (llm.AttemptResult, error)

	mu       sync.Mutex
	attempts []llm.Attempt
	keys     []string
}

var _ llm.Adapter = (*opsLLM)(nil)

func (a *opsLLM) RequiresKey() bool  { return a.needsKey }
func (a *opsLLM) Policy() llm.Policy { return llm.Policy{RequestTimeout: 120 * time.Second} }
func (a *opsLLM) ListModels(context.Context, string) ([]llm.ModelInfo, error) {
	return nil, nil
}

func (a *opsLLM) Complete(_ context.Context, key string, at llm.Attempt) (llm.AttemptResult, error) {
	a.mu.Lock()
	a.attempts = append(a.attempts, at)
	a.keys = append(a.keys, key)
	a.mu.Unlock()
	return a.answer(key, at)
}

func (a *opsLLM) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.attempts)
}

func (a *opsLLM) attempt(i int) llm.Attempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.attempts[i]
}

func (a *opsLLM) sawKey(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range a.keys {
		if k == key {
			return true
		}
	}
	return false
}

func (a *opsLLM) forAll() map[llm.Provider]llm.Adapter {
	return map[llm.Provider]llm.Adapter{llm.OpenAI: a, llm.Anthropic: a, llm.OpenRouter: a}
}

// opsFinding is a finding of the Review answer.
type opsFinding struct {
	ID         string  `json:"id"`
	Severity   string  `json:"severity"`
	Category   string  `json:"category"`
	Title      string  `json:"title"`
	File       string  `json:"file"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	Rationale  string  `json:"rationale"`
	Suggestion string  `json:"suggestion"`
	Confidence float64 `json:"confidence"`
	Kind       string  `json:"kind"`
}

func opsFind(id, file string, line int) opsFinding {
	return opsFinding{
		ID: id, Severity: "WARNING", Category: "bug", Title: "Finding " + id, File: file,
		StartLine: line, EndLine: line, Rationale: "Because " + id + ".", Suggestion: "Fix " + id + ".",
		Confidence: 0.9, Kind: "finding",
	}
}

// opsAnswer answers every call with a Review holding findings.
func opsAnswer(findings ...opsFinding) func(string, llm.Attempt) (llm.AttemptResult, error) {
	if findings == nil {
		findings = []opsFinding{}
	}
	raw, err := json.Marshal(map[string]any{"verdict": "comment", "summary": "ops summary", "score": 80, "findings": findings})
	if err != nil {
		panic(err)
	}
	cost := 0.001
	return func(string, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{Raw: string(raw), Usage: &llm.Usage{TokensIn: 100, TokensOut: 20}, CostUSD: &cost}, nil
	}
}

// The case diffs: line 2 of a.go and of b.go is an added line; line 90 is outside every hunk.
const (
	opsDiffA = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n package a\n+var X = 1\n func F() {}\n"
	opsDiffB = "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,2 +1,3 @@\n package b\n+var Y = 2\n func G() {}\n"
)

// ---- evals through the API ----

// opsCase creates a manual case of an agent and returns its id.
func opsCase(t testing.TB, s *stack, agent, name, diff string) string {
	t.Helper()
	resp := s.post("/agents/"+agent+"/eval-cases", map[string]any{
		"name":        name,
		"expectation": map[string]any{"kind": "must_find", "file": "a.go", "start_line": 2, "end_line": 2},
		"input_diff":  diff,
		"input_meta":  map[string]any{"pr_title": "T", "pr_description": "desc"},
	})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /agents/%s/eval-cases %q = %d %s, want 201", agent, name, resp.Status, resp.Body)
	}
	return idOf(t, resp)
}

// opsStartRun starts an eval run of the agent and returns the suite run id.
func opsStartRun(t testing.TB, s *stack, agent string) string {
	t.Helper()
	resp := s.post("/agents/"+agent+"/eval-runs", nil)
	if resp.Status != http.StatusAccepted {
		t.Fatalf("POST /agents/%s/eval-runs = %d %s, want 202", agent, resp.Status, resp.Body)
	}
	return idOf(t, resp)
}

// opsWaitRun waits until the suite run is no longer running and returns its status.
func opsWaitRun(t testing.TB, s *stack, run string) string {
	t.Helper()
	var status string
	eventually(t, 60*time.Second, "eval run "+run+" to finish", func() bool {
		status = scalar[string](t, s.db.DB, `SELECT status FROM eval_suite_runs WHERE id = ?`, run)
		return status == "completed" || status == "failed"
	})
	return status
}

// opsGitHub plays the Git Data, pull request and workflow-run endpoints of acme/payments-api for
// the CI export and refresh. With fail set every request of the repository answers 500 with a body
// that echoes the request's credentials.
type opsGitHub struct {
	fail atomic.Bool

	mu     sync.Mutex
	prBody string
}

func (g *opsGitHub) body() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.prBody
}

func (g *opsGitHub) handle(w http.ResponseWriter, r *http.Request) bool {
	const base = "/repos/acme/payments-api"
	rest, ok := strings.CutPrefix(r.URL.Path, base)
	if !ok {
		return false
	}
	reply := func(code int, body string) bool {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
		return true
	}
	if g.fail.Load() {
		return reply(http.StatusInternalServerError,
			fmt.Sprintf(`{"message":"server error for %s (Authorization: %s)"}`, r.URL.Path, r.Header.Get("Authorization")))
	}
	switch {
	case r.Method == http.MethodGet && rest == "/git/ref/heads/devdigest/ci":
		return reply(http.StatusNotFound, `{"message":"Not Found"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "/git/ref/heads/"):
		return reply(http.StatusOK, `{"object":{"sha":"base0"}}`)
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "/git/commits/"):
		return reply(http.StatusOK, `{"sha":"base0","tree":{"sha":"tree0"}}`)
	case r.Method == http.MethodPost && rest == "/git/blobs":
		return reply(http.StatusCreated, `{"sha":"blob1"}`)
	case r.Method == http.MethodPost && rest == "/git/trees":
		return reply(http.StatusCreated, `{"sha":"tree1"}`)
	case r.Method == http.MethodPost && rest == "/git/commits":
		return reply(http.StatusCreated, `{"sha":"commit1"}`)
	case r.Method == http.MethodPost && rest == "/git/refs":
		return reply(http.StatusCreated, `{}`)
	case r.Method == http.MethodGet && rest == "/pulls":
		return reply(http.StatusOK, `[]`)
	case r.Method == http.MethodPost && rest == "/pulls":
		var in struct {
			Body string `json:"body"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		g.mu.Lock()
		g.prBody = in.Body
		g.mu.Unlock()
		return reply(http.StatusCreated, `{"number":9,"html_url":"https://github.com/acme/payments-api/pull/9"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "/actions/workflows/"):
		return reply(http.StatusOK, `{"workflow_runs":[]}`)
	}
	return false
}

// opsExport posts an export of the agent to the seeded repository.
func opsExport(s *stack, agent, action string) response {
	s.t.Helper()
	return s.post("/agents/"+agent+"/export-ci", map[string]any{"repo": "acme/payments-api", "action": action, "post_as": "none"})
}

// opsFile is one file of an export answer.
type opsFile struct{ Path, Contents string }

func opsFiles(t testing.TB, resp response) []opsFile {
	t.Helper()
	var out struct {
		Files []struct {
			Path     string `json:"path"`
			Contents string `json:"contents"`
		} `json:"files"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("export answer %q: %v", resp.Body, err)
	}
	files := make([]opsFile, len(out.Files))
	for i, f := range out.Files {
		files[i] = opsFile{Path: f.Path, Contents: f.Contents}
	}
	return files
}

func opsFileNamed(t testing.TB, files []opsFile, path string) *opsFile {
	t.Helper()
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}

// opsWriteFiles writes the files below dir.
func opsWriteFiles(t testing.TB, dir string, files []opsFile) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.Contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// opsRunBlock is the lines of the `run: |` block of the generated workflow, unindented.
func opsRunBlock(t testing.TB, workflow string) []string {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "run: |" {
			continue
		}
		indent := strings.Repeat(" ", len(l)-len(strings.TrimLeft(l, " "))+2)
		var block []string
		for _, b := range lines[i+1:] {
			rest, ok := strings.CutPrefix(b, indent)
			if !ok {
				break
			}
			block = append(block, rest)
		}
		return block
	}
	t.Fatalf("the workflow has no run block:\n%s", workflow)
	return nil
}

// opsBash runs a script with bash -e -o pipefail in dir, like the Actions runner runs a step.
func opsBash(t testing.TB, dir, script string, env ...string) (out string, code int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", "-e", "-o", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	raw, err := cmd.CombinedOutput()
	switch e := err.(type) {
	case nil:
		return string(raw), 0
	case *exec.ExitError:
		return string(raw), e.ExitCode()
	default:
		t.Fatalf("bash: %v", err)
		return "", -1
	}
}

func opsNeedTools(t testing.TB, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// opsBuildRunner builds cmd/devdigest-runner for linux/amd64 without cgo, stamped with buildID.
func opsBuildRunner(t testing.TB, buildID string) []byte {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	out := filepath.Join(t.TempDir(), "devdigest-runner")
	cmd := exec.CommandContext(t.Context(), goBin, "build", "-trimpath", "-buildvcs=false",
		"-ldflags", "-s -w -X main.buildID="+buildID, "-o", out, "dev-digest-go/backend/cmd/devdigest-runner")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/devdigest-runner = %v\n%s", err, raw)
	}
	exe, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// ---- AC6049 ----

// AC6049 (FR6047, FR6035): the runner file of an export, decoded with `base64 -d | gzip -dc` and
// checked with the checksum file by `sha256sum --check --strict`, passes and holds exactly the
// bytes of the injected runner; the PR body carries the SHA-256 and the build id, and the decoded
// binary itself prints the same build id in its trace line.
func TestOpsP6_RunnerFileDecodesChecksAndCarriesItsIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the runner for linux/amd64")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("runs the linux/amd64 runner")
	}
	opsNeedTools(t, "bash", "base64", "gzip", "sha256sum")
	const buildID = "0123456789ab"
	exe := opsBuildRunner(t, buildID)
	sum := sha256.Sum256(exe)
	wantSHA := hex.EncodeToString(sum[:])

	s := startStack(t, spec{runner: runnerbin.New(exe, buildID), env: map[string]string{"GITHUB_TOKEN": "ghp_ops_runner_token_0123456789"}})
	s.client.Timeout = 5 * time.Minute // encoding a 10 MiB executable at best compression is slow under -race
	pub := &opsGitHub{}
	s.gh.setOverride(pub.handle)
	agent := newAgent(t, s, map[string]any{"name": "Runner Check"})
	linkSkill(t, s, agent, newSkill(t, s, "ops-skill", "Check the thing."))

	resp := opsExport(s, agent, "files")
	if resp.Status != http.StatusOK {
		t.Fatalf("export files = %d %s, want 200", resp.Status, resp.Body)
	}
	files := opsFiles(t, resp)
	ws, tmp := t.TempDir(), t.TempDir()
	opsWriteFiles(t, ws, files)

	sumFile := opsFileNamed(t, files, cidomain.RunnerSumPath)
	if sumFile == nil || opsFileNamed(t, files, cidomain.RunnerPath) == nil {
		t.Fatalf("export lacks the runner file or its checksum file: %v", files)
	}
	if want := wantSHA + "  devdigest-runner\n"; sumFile.Contents != want {
		t.Errorf("checksum file = %q, want %q", sumFile.Contents, want)
	}

	var workflow string
	for _, f := range files {
		if strings.HasPrefix(f.Path, ".github/workflows/") {
			workflow = f.Contents
		}
	}
	block := opsRunBlock(t, workflow)
	if len(block) < 2 {
		t.Fatalf("run block = %q, want the decode and the checksum lines first", block)
	}
	// The decode and the checksum lines of the workflow's own run step.
	out, code := opsBash(t, ws, strings.Join(block[:2], "\n"), "RUNNER_TEMP="+tmp, "GITHUB_WORKSPACE="+ws)
	if code != 0 {
		t.Fatalf("decode and sha256sum --check --strict exit %d, want 0:\n%s", code, out)
	}
	decoded, err := os.ReadFile(filepath.Join(tmp, "devdigest-runner"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, exe) {
		t.Errorf("decoded runner = %d bytes with SHA-256 %x, want the injected %d bytes", len(decoded), sha256.Sum256(decoded), len(exe))
	}

	// The PR body of open_pr names the same SHA-256 and build id.
	resp = opsExport(s, agent, "open_pr")
	if resp.Status != http.StatusCreated {
		t.Fatalf("export open_pr = %d %s, want 201", resp.Status, resp.Body)
	}
	body := pub.body()
	if !strings.Contains(body, "`"+wantSHA+"`") || !strings.Contains(body, "`"+buildID+"`") {
		t.Errorf("PR body does not carry SHA-256 %s and build id %s:\n%s", wantSHA, buildID, body)
	}

	// The decoded binary prints the build id in its trace line. It stops at the diff fetch: the
	// proxy of its environment refuses, so the test never reaches the real GitHub.
	slug := ""
	for _, f := range files {
		if rest, ok := strings.CutPrefix(f.Path, ".devdigest/agents/"); ok {
			slug = strings.TrimSuffix(rest, ".yaml")
		}
	}
	if slug == "" {
		t.Fatalf("export has no agent manifest: %v", files)
	}
	if err := os.Chmod(filepath.Join(tmp, "devdigest-runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), filepath.Join(tmp, "devdigest-runner"))
	cmd.Dir = ws
	cmd.Env = []string{
		"GITHUB_REPOSITORY=acme/payments-api", "PR_NUMBER=7", "DEVDIGEST_AGENT=" + slug, "DEVDIGEST_POST_AS=none",
		"DEVDIGEST_DIR=" + filepath.Join(ws, ".devdigest"), "DEVDIGEST_RESULT_PATH=" + filepath.Join(ws, "result.json"),
		"HTTPS_PROXY=http://127.0.0.1:1", "HTTP_PROXY=http://127.0.0.1:1",
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !asExit(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("runner exit = %v, want exit status 1 (the diff fetch fails)\nstdout:\n%s\nstderr:\n%s", err, &stdout, &stderr)
	}
	if want := "runner=2 build=" + buildID; !strings.Contains(stdout.String(), want) {
		t.Errorf("runner trace line lacks %q:\n%s", want, &stdout)
	}
	if _, err := os.Stat(filepath.Join(ws, "result.json")); err == nil {
		t.Error("a runner that failed before the review left an artifact")
	}
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// ---- AC6055 ----

// AC6055 (FR6036, EC6050): in the generated run step, a decoded runner of which one byte differs
// fails `sha256sum --check`, is never executed and makes the step exit non-zero (the step runs
// with bash -e -o pipefail). The unchanged file runs: that is the control.
func TestOpsP6_ARunnerWithOneChangedByteIsNeverExecuted(t *testing.T) {
	opsNeedTools(t, "bash", "base64", "gzip", "sha256sum", "chmod")
	exe := []byte("#!/bin/sh\necho ran > \"$RUNNER_TEMP/executed\"\n# padding for the changed byte: aaaaaaaaaaaaaaaa\n")
	s := startStack(t, spec{runner: runnerbin.New(exe, "dev")})
	agent := newAgent(t, s, map[string]any{"name": "Tamper Check"})

	resp := opsExport(s, agent, "files")
	if resp.Status != http.StatusOK {
		t.Fatalf("export files = %d %s, want 200", resp.Status, resp.Body)
	}
	files := opsFiles(t, resp)
	var workflow string
	for _, f := range files {
		if strings.HasPrefix(f.Path, ".github/workflows/") {
			workflow = f.Contents
		}
	}
	script := strings.Join(opsRunBlock(t, workflow), "\n")
	ws := t.TempDir()
	opsWriteFiles(t, ws, files)

	run := func() (out string, code int, executed bool) {
		tmp := t.TempDir()
		out, code = opsBash(t, ws, script, "RUNNER_TEMP="+tmp, "GITHUB_WORKSPACE="+ws)
		_, err := os.Stat(filepath.Join(tmp, "executed"))
		return out, code, err == nil
	}

	if out, code, executed := run(); code != 0 || !executed {
		t.Fatalf("control: step exit = %d, executed = %t, want 0 and true:\n%s", code, executed, out)
	}

	runner := opsFileNamed(t, files, cidomain.RunnerPath)
	decoded, err := cidomain.DecodeRunner(runner.Contents, cidomain.MaxRunnerBytes)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(decoded)
	changed[len(changed)-5] ^= 1 // one byte of the trailing padding comment
	if bytes.Equal(changed, decoded) {
		t.Fatal("the byte was not changed")
	}
	encoded, err := cidomain.EncodeRunner(changed)
	if err != nil {
		t.Fatal(err)
	}
	opsWriteFiles(t, ws, []opsFile{{Path: cidomain.RunnerPath, Contents: encoded}})

	out, code, executed := run()
	if code == 0 {
		t.Errorf("step with a changed byte exits 0, want non-zero:\n%s", out)
	}
	if executed {
		t.Error("the changed runner was executed")
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("step output = %q, want sha256sum's FAILED", out)
	}
}

// ---- AC6052 ----

type opsGitHubDiff struct{ diff string }

func (g opsGitHubDiff) FetchDiff(context.Context, string, runnerapp.PullRef) (string, error) {
	return g.diff, nil
}
func (opsGitHubDiff) PostReview(context.Context, string, runnerapp.PullRef, runnerapp.ReviewPost) error {
	return nil
}
func (opsGitHubDiff) PostComment(context.Context, string, runnerapp.PullRef, string) error {
	return nil
}

type opsNoKeys struct{}

func (opsNoKeys) Get(string) string { return "" }

type opsNoPrices struct{}

func (opsNoPrices) Estimate(string, int, int) *float64 { return nil }

// opsRunnerLLM is the runner's LLM wiring (cmd/devdigest-runner reviewLLM) over a recording adapter.
type opsRunnerLLM struct{ client *llm.Client }

func (r opsRunnerLLM) For(model string) reviewerapp.LLM {
	return reviewerllm.New(reviewerllm.Config{Client: r.client, Provider: llm.OpenRouter, Model: model, RunID: "ci-runner"})
}

// opsKept is a kept finding as scoring and display read it.
type opsKept struct {
	File               string
	StartLine, EndLine int
	Severity, Category string
	Title              string
}

// AC6052 (FR6051, NFR6014): the runner and an eval run of the server, given the same diff, system
// prompt, skill strings, PR description, task, model and strategy, send byte-identical requests
// (messages, schema, temperature) and keep and drop the same findings. The runner's own input is
// checked too: its task line and none of memory, project context, intent, callers or repo map.
func TestOpsP6_RunnerAndEvalRunSendTheSameMessages(t *testing.T) {
	answer := opsAnswer(opsFind("kept", "a.go", 2), opsFind("outside", "a.go", 90))
	server := &opsLLM{answer: answer}
	s := startStack(t, spec{llmAdapters: server.forAll()})

	const sysPrompt = "You are the parity reviewer.\nSecond line, with \"quotes\" and a tab\t."
	agent := newAgent(t, s, map[string]any{"name": "Parity", "strategy": "single-pass", "system_prompt": sysPrompt})
	linkSkill(t, s, agent, newSkill(t, s, "ops-skill", "Always check X.\n\nThen Y."))
	opsCase(t, s, agent, "case", opsDiffA)
	run := opsStartRun(t, s, agent)
	if got := opsWaitRun(t, s, run); got != "completed" {
		t.Fatalf("eval run = %s, want completed", got)
	}
	if n := server.calls(); n != 1 {
		t.Fatalf("eval run made %d LLM calls, want 1", n)
	}

	// What the run froze: the skills as the run renders them, the model of the agent.
	var cfg struct {
		Model  string `json:"model"`
		Skills []struct {
			Name, Description, Body string
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(scalar[string](t, s.db.DB, `SELECT config::text FROM eval_suite_runs WHERE id = ?`, run)), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Skills) != 1 {
		t.Fatalf("run config skills = %+v, want one", cfg.Skills)
	}
	skillText := reviewer.SkillBlock(cfg.Skills[0].Name, cfg.Skills[0].Description, cfg.Skills[0].Body)

	// The runner: manifest and skill files as the export writes them, an event with the PR text.
	dir := t.TempDir()
	devdir := filepath.Join(dir, ".devdigest")
	manifest, err := json.Marshal(map[string]any{
		"name": "Parity", "provider": "openrouter", "model": cfg.Model, "system_prompt": sysPrompt,
		"skills": []string{"ops-skill"}, "strategy": "single-pass", "ci_fail_on": "never", "version": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	opsWriteFiles(t, dir, []opsFile{
		{Path: ".devdigest/agents/parity.yaml", Contents: string(manifest)},
		{Path: ".devdigest/skills/ops-skill.md", Contents: skillText},
		{Path: "event.json", Contents: `{"pull_request":{"number":7,"title":"T","body":"desc"}}`},
	})
	envm := map[string]string{
		"GITHUB_REPOSITORY": "acme/payments-api", "PR_NUMBER": "7", "GITHUB_EVENT_PATH": filepath.Join(dir, "event.json"),
		"DEVDIGEST_DIR": devdir, "DEVDIGEST_RESULT_PATH": filepath.Join(dir, "result.json"),
		"DEVDIGEST_POST_AS": "none", "DEVDIGEST_AGENT": "parity",
	}
	rec := &opsLLM{answer: answer}
	client := llm.New(llm.Options{
		Keys:     opsNoKeys{},
		Adapters: map[llm.Provider]llm.Adapter{llm.OpenRouter: rec},
		Logger:   slog.New(slog.DiscardHandler),
		Prices:   opsNoPrices{},
	})
	defer client.Close()

	evalTask := evalsdomain.TaskLine(nil, "T") // a manual eval case titled "T" has no PR number
	var runnerIn reviewerapp.Input
	var runnerOut reviewer.Outcome
	var stdout, stderr bytes.Buffer
	code := runnerapp.Run(t.Context(), runnerapp.Config{BuildID: "dev"}, runnerapp.Deps{
		Env:    runnerenv.Process{LookupEnv: func(k string) (string, bool) { v, ok := envm[k]; return v, ok }, Dir: dir},
		Files:  runnerenv.Files{},
		GitHub: opsGitHubDiff{diff: opsDiffA},
		LLM:    opsRunnerLLM{client: client},
		Out:    runnerenv.NewOut(&stdout, &stderr),
		Engine: func(ctx context.Context, in reviewerapp.Input, l reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error) {
			runnerIn = in
			in.Task = evalTask // the one input that differs by design: the same task for both sides
			out, err := reviewerapp.Run(ctx, in, l, ev, opts)
			runnerOut = out
			return out, err
		},
	})
	if code != runnerapp.ExitOK {
		t.Fatalf("runner exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, &stdout, &stderr)
	}

	// The runner's own input (spec 7.8.6).
	if runnerIn.Task != "Review PR #7: T" || runnerIn.PRDescription != "desc" || runnerIn.SystemPrompt != sysPrompt ||
		runnerIn.Strategy != reviewer.StrategySinglePass {
		t.Errorf("runner input = task %q, description %q, strategy %q, want the task line, desc and single-pass",
			runnerIn.Task, runnerIn.PRDescription, runnerIn.Strategy)
	}
	if diff := cmp.Diff([]string{skillText}, runnerIn.Skills); diff != "" {
		t.Errorf("runner skills mismatch (-want +got):\n%s", diff)
	}
	if len(runnerIn.Specs) != 0 || runnerIn.Callers != "" || runnerIn.RepoMap != "" || runnerIn.Intent != nil {
		t.Errorf("runner input carries project context, callers, repo map or intent: %+v", runnerIn)
	}

	if rec.calls() != 1 {
		t.Fatalf("runner made %d LLM calls, want 1", rec.calls())
	}
	if diff := cmp.Diff(server.attempt(0), rec.attempt(0)); diff != "" {
		t.Errorf("request of the runner differs from the eval run's (-eval +runner):\n%s", diff)
	}

	// The grounding results.
	var want []opsKept
	for _, f := range runnerOut.Review.Findings {
		want = append(want, opsKept{File: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
			Severity: string(f.Severity), Category: string(f.Category), Title: f.Title})
	}
	var stored []struct {
		File      string `json:"file"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Severity  string `json:"severity"`
		Category  string `json:"category"`
		Title     string `json:"title"`
	}
	raw := scalar[string](t, s.db.DB, `SELECT actual_output::text FROM eval_runs WHERE run_id = ?`, run)
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("actual_output %q: %v", raw, err)
	}
	var got []opsKept
	for _, f := range stored {
		got = append(got, opsKept{File: f.File, StartLine: f.StartLine, EndLine: f.EndLine, Severity: f.Severity, Category: f.Category, Title: f.Title})
	}
	if len(want) != 1 {
		t.Fatalf("runner kept %d findings, want 1 (the one inside the diff)", len(want))
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("kept findings of the eval run differ from the runner's (-runner +eval):\n%s", diff)
	}
	if n, dropped := len(runnerOut.Dropped), scalar[int](t, s.db.DB, `SELECT dropped_count FROM eval_runs WHERE run_id = ?`, run); n != dropped || n != 1 {
		t.Errorf("dropped findings: runner %d, eval run %d, want 1 and 1", n, dropped)
	}
}

// ---- AC6064 ----

// AC6064 (FR6005, FR6044, EC6043): deleting an agent removes its cases, suite runs, case results
// and CI installations; its CI runs stay, detached (agent_id null). Another agent's cases stay.
func TestOpsP6_DeletingAnAgentRemovesItsEvalsAndInstallationsButKeepsCIRuns(t *testing.T) {
	ad := &opsLLM{answer: opsAnswer(opsFind("kept", "a.go", 2))}
	s := startStack(t, spec{llmAdapters: ad.forAll(), runner: runnerbin.New([]byte("exe"), "dev"), env: map[string]string{"GITHUB_TOKEN": "ghp_ops_delete_token_0123456789"}})
	pub := &opsGitHub{}
	s.gh.setOverride(pub.handle)

	agent := newAgent(t, s, map[string]any{"name": "Doomed"})
	other := newAgent(t, s, map[string]any{"name": "Survivor"})
	opsCase(t, s, agent, "c1", opsDiffA)
	opsCase(t, s, agent, "c2", opsDiffA)
	keep := opsCase(t, s, other, "keep", opsDiffA)
	run := opsStartRun(t, s, agent)
	if got := opsWaitRun(t, s, run); got != "completed" {
		t.Fatalf("eval run = %s, want completed", got)
	}
	if resp := opsExport(s, agent, "open_pr"); resp.Status != http.StatusCreated {
		t.Fatalf("export open_pr = %d %s, want 201", resp.Status, resp.Body)
	}
	inst := scalar[string](t, s.db.DB, `SELECT id::text FROM ci_installations WHERE agent_id = ?`, agent)
	ws := scalar[string](t, s.db.DB, `SELECT workspace_id::text FROM agents WHERE id = ?`, agent)
	for i := 1; i <= 2; i++ {
		err := s.db.DB.Exec(`WITH ar AS (
				INSERT INTO agent_runs (workspace_id, agent_id, ran_at, provider, model, status, source, findings_count, cost_usd)
				VALUES (?, ?, now(), 'openrouter', 'deepseek/deepseek-v4-flash', 'done', 'ci', 0, 0.01) RETURNING id)
			INSERT INTO ci_runs (ci_installation_id, agent_run_id, pr_number, ran_at, status, findings_count, cost_usd, github_url, source, github_run_id, run_attempt, head_sha, manifest_version)
			SELECT ?::uuid, ar.id, ?, now(), 'succeeded', 0, 0.01, ?, 'gha', ?, 1, 'abc123', 1 FROM ar`,
			ws, agent, inst, i, fmt.Sprintf("https://github.com/acme/payments-api/actions/runs/%d", 500+i), 500+i).Error
		if err != nil {
			t.Fatal(err)
		}
	}
	counts := func() (cases, suites, results, installs, ciRuns, detached int) {
		db := s.db.DB
		return count(t, db, `SELECT count(*) FROM eval_cases WHERE owner_id = ?`, agent),
			count(t, db, `SELECT count(*) FROM eval_suite_runs WHERE agent_id = ?`, agent),
			count(t, db, `SELECT count(*) FROM eval_runs WHERE run_id = ?`, run),
			count(t, db, `SELECT count(*) FROM ci_installations WHERE agent_id = ?`, agent),
			count(t, db, `SELECT count(*) FROM ci_runs`),
			count(t, db, `SELECT count(*) FROM ci_runs WHERE ci_installation_id IS NULL`)
	}
	if c, su, r, in, ci, d := counts(); c != 2 || su != 1 || r != 2 || in != 1 || ci != 2 || d != 0 {
		t.Fatalf("before the delete: cases %d suites %d results %d installations %d ci_runs %d detached %d, want 2 1 2 1 2 0", c, su, r, in, ci, d)
	}

	if resp := s.do(http.MethodDelete, "/agents/"+agent, nil); resp.Status/100 != 2 {
		t.Fatalf("DELETE /agents/%s = %d %s, want 2xx", agent, resp.Status, resp.Body)
	}
	if c, su, r, in, ci, d := counts(); c != 0 || su != 0 || r != 0 || in != 0 || ci != 2 || d != 2 {
		t.Errorf("after the delete: cases %d suites %d results %d installations %d ci_runs %d detached %d, want 0 0 0 0 2 2", c, su, r, in, ci, d)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM eval_cases WHERE id = ?`, keep); n != 1 {
		t.Errorf("the other agent's case count = %d, want 1", n)
	}

	resp := s.get("/ci/runs")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /ci/runs = %d %s, want 200", resp.Status, resp.Body)
	}
	var runs struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(resp.Body, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Runs) != 2 {
		t.Fatalf("GET /ci/runs lists %d runs, want the 2 kept ones: %s", len(runs.Runs), resp.Body)
	}
	for _, r := range runs.Runs {
		if r["agent_id"] != nil {
			t.Errorf("run %v has agent_id %v, want null", r["id"], r["agent_id"])
		}
		if r["repo"] != "acme/payments-api" {
			t.Errorf("run %v has repo %v, want acme/payments-api (from its URL)", r["id"], r["repo"])
		}
	}
}

// ---- AC6067 ----

// AC6067 (NFR6012): an export (files and open_pr) and a refresh make no LLM call; a run of three
// single-pass cases makes exactly three; a map-reduce case of two files makes exactly two.
func TestOpsP6_OnlyEvalCasesCallTheLLM(t *testing.T) {
	ad := &opsLLM{answer: opsAnswer(opsFind("kept", "a.go", 2))}
	s := startStack(t, spec{llmAdapters: ad.forAll(), runner: runnerbin.New([]byte("exe"), "dev"), env: map[string]string{"GITHUB_TOKEN": "ghp_ops_calls_token_0123456789"}})
	pub := &opsGitHub{}
	s.gh.setOverride(pub.handle)

	exported := newAgent(t, s, map[string]any{"name": "Exported"})
	if resp := opsExport(s, exported, "files"); resp.Status != http.StatusOK {
		t.Fatalf("export files = %d %s, want 200", resp.Status, resp.Body)
	}
	if resp := opsExport(s, exported, "open_pr"); resp.Status != http.StatusCreated {
		t.Fatalf("export open_pr = %d %s, want 201", resp.Status, resp.Body)
	}
	if resp := s.post("/ci/refresh", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	if n := ad.calls(); n != 0 {
		t.Fatalf("export and refresh made %d LLM calls, want 0", n)
	}

	single := newAgent(t, s, map[string]any{"name": "Single", "strategy": "single-pass"})
	for _, name := range []string{"one", "two", "three"} {
		opsCase(t, s, single, name, opsDiffA)
	}
	if got := opsWaitRun(t, s, opsStartRun(t, s, single)); got != "completed" {
		t.Fatalf("single-pass run = %s, want completed", got)
	}
	if n := ad.calls(); n != 3 {
		t.Errorf("a 3-case single-pass run made %d LLM calls, want 3", n)
	}

	mapped := newAgent(t, s, map[string]any{"name": "Mapped", "strategy": "map-reduce"})
	opsCase(t, s, mapped, "two files", opsDiffA+opsDiffB)
	if got := opsWaitRun(t, s, opsStartRun(t, s, mapped)); got != "completed" {
		t.Fatalf("map-reduce run = %s, want completed", got)
	}
	if n := ad.calls(); n != 5 {
		t.Errorf("after a map-reduce case of 2 files the double has %d calls, want 3 + 2 = 5", n)
	}
}

// ---- AC6059 ----

// AC6059 (NFR6002, NFR6005): provider keys and the GitHub token set to marker values never reach
// a log line, a response body, a stored row, a generated file or the PR body, while evals run,
// fail with a provider error that echoes the key, and the CI export and refresh succeed and fail
// against a GitHub that echoes the credentials it was sent.
func TestOpsP6_KeysAndTokenNeverReachOutputsOrRows(t *testing.T) {
	const (
		orKey    = "sk-or-MARKER-openrouter-1f9c2a7e"
		openAI   = "sk-MARKER-openai-77b0d4c1"
		anthropc = "sk-ant-MARKER-anthropic-5e3a9b20"
		ghToken  = "ghp_MARKER_github_token_c4d81f06"
	)
	markers := []string{orKey, openAI, anthropc, ghToken}

	var echo atomic.Bool
	ad := &opsLLM{needsKey: true, answer: func(key string, at llm.Attempt) (llm.AttemptResult, error) {
		if echo.Load() {
			return llm.AttemptResult{}, fmt.Errorf("401 Unauthorized: invalid api key %s for model %s", key, at.Model)
		}
		return opsAnswer(opsFind("kept", "a.go", 2))(key, at)
	}}
	s := startStack(t, spec{
		llmAdapters: ad.forAll(), llm: fastLLM(), runner: runnerbin.New([]byte("exe"), "dev"),
		env: map[string]string{"OPENROUTER_API_KEY": orKey, "OPENAI_API_KEY": openAI, "ANTHROPIC_API_KEY": anthropc, "GITHUB_TOKEN": ghToken},
	})
	pub := &opsGitHub{}
	s.gh.setOverride(pub.handle)

	var outputs []string
	keep := func(r response) response {
		outputs = append(outputs, string(r.Body))
		return r
	}

	agent := newAgent(t, s, map[string]any{"name": "Secret Check", "strategy": "single-pass"})
	opsCase(t, s, agent, "c1", opsDiffA)
	opsCase(t, s, agent, "c2", opsDiffA)

	readEvals := func(run string) {
		for _, p := range []string{"/eval-runs/" + run, "/agents/" + agent + "/eval-runs", "/agents/" + agent + "/eval-cases",
			"/eval/dashboard", "/agents/" + agent + "/eval/dashboard"} {
			keep(s.get(p))
		}
	}
	// Evals: a run that works, then one whose every call fails with an error echoing the key.
	ok := opsStartRun(t, s, agent)
	if got := opsWaitRun(t, s, ok); got != "completed" {
		t.Fatalf("working run = %s, want completed", got)
	}
	readEvals(ok)
	echo.Store(true)
	bad := opsStartRun(t, s, agent)
	opsWaitRun(t, s, bad)
	readEvals(bad)
	if !ad.sawKey(orKey) {
		t.Fatal("the OpenRouter marker never reached the provider adapter: the test would prove nothing")
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM eval_runs WHERE run_id = ? AND error IS NOT NULL`, bad); n == 0 {
		t.Fatal("the failing run stored no case error: the echoed key was never exercised")
	}

	// CI: files, a published PR, the installation views, a refresh; then the same against a GitHub
	// that fails and echoes the Authorization header.
	var generated []string
	files := keep(opsExport(s, agent, "files"))
	for _, f := range opsFiles(t, files) {
		generated = append(generated, f.Contents)
	}
	if resp := keep(opsExport(s, agent, "open_pr")); resp.Status != http.StatusCreated {
		t.Fatalf("export open_pr = %d %s, want 201", resp.Status, resp.Body)
	}
	keep(s.get("/agents/" + agent + "/ci-installations"))
	keep(s.get("/ci/runs"))
	keep(s.post("/ci/refresh", nil))
	pub.fail.Store(true)
	if resp := keep(opsExport(s, agent, "open_pr")); resp.Status == http.StatusCreated {
		t.Fatalf("export open_pr against a failing GitHub = %d, want an error", resp.Status)
	}
	keep(s.post("/ci/refresh", nil))
	keep(s.get("/ci/runs"))
	pub.fail.Store(false)
	var sawToken bool
	for _, r := range s.gh.requests() {
		sawToken = sawToken || strings.Contains(r.Auth, ghToken)
	}
	if !sawToken {
		t.Fatal("the GitHub token marker never reached GitHub: the test would prove nothing")
	}

	// Every table of the database, row by row.
	var tables []string
	if err := s.db.DB.Raw(`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, tbl := range tables {
		rows = append(rows, scalar[string](t, s.db.DB, fmt.Sprintf(`SELECT coalesce(string_agg(x::text, E'\n'), '') FROM %q x`, tbl)))
	}

	places := map[string]string{
		"the log":               s.logs.String(),
		"the response bodies":   strings.Join(outputs, "\n"),
		"the stored rows":       strings.Join(rows, "\n"),
		"the generated files":   strings.Join(generated, "\n"),
		"the pull request body": pub.body(),
	}
	if places["the pull request body"] == "" {
		t.Error("no pull request body was captured")
	}
	for where, text := range places {
		assertNoSecret(t, where, text, markers...)
	}
}
