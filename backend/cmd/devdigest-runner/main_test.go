package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/keynames"
)

const testManifest = `name: "Security Reviewer"
provider: "openrouter"
model: "deepseek/deepseek-v4-flash"
system_prompt: "Review this PR for security issues."
skills: []
strategy: "single-pass"
ci_fail_on: "critical"
`

const testDiff = "diff --git a/src/config.ts b/src/config.ts\n--- a/src/config.ts\n+++ b/src/config.ts\n" +
	"@@ -9,3 +9,4 @@\n host: 'localhost',\n+apiKey: 'sk_live_abcdef123456',\n port: 3000,\n timeout: 30,\n"

// workspace is a temporary checkout with a manifest and an event payload, and the environment
// that points the runner at it.
type workspace struct {
	root string
	env  map[string]string
}

func newWorkspace(t *testing.T, manifest string, env map[string]string) *workspace {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".devdigest", "agents", "security-reviewer.yaml"), manifest)
	mustWrite(t, filepath.Join(root, "event.json"), `{"pull_request":{"number":42,"title":"Add feature X","body":"A cool feature."}}`)
	w := &workspace{root: root, env: map[string]string{
		"GITHUB_REPOSITORY":     "acme/widgets",
		"GITHUB_EVENT_PATH":     filepath.Join(root, "event.json"),
		"DEVDIGEST_DIR":         filepath.Join(root, ".devdigest"),
		"DEVDIGEST_RESULT_PATH": filepath.Join(root, "devdigest-result.json"),
		"GITHUB_TOKEN":          "ghp_test_token_value",
		"OPENROUTER_API_KEY":    "sk-or-test-key-value",
	}}
	for k, v := range env {
		w.env[k] = v
	}
	return w
}

func (w *workspace) lookup(k string) (string, bool) { v, ok := w.env[k]; return v, ok }

func (w *workspace) artifact(t *testing.T) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(w.env["DEVDIGEST_RESULT_PATH"])
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

// openRouterReply answers every chat completion with a review of the given findings and a cost.
func openRouterReply(t *testing.T, findings []map[string]any) *httptest.Server {
	t.Helper()
	if findings == nil {
		findings = []map[string]any{}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review, _ := json.Marshal(map[string]any{"verdict": "approve", "summary": "ok", "score": 90, "findings": findings})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "created": 1, "model": "deepseek/deepseek-v4-flash",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(review)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15, "cost": 0.0025},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// gitHubOK serves the diff and accepts reviews and comments.
func gitHubOK(t *testing.T) *githubStub {
	t.Helper()
	return newGitHubStub(t, func(method, _ string, _ int) stubResp {
		if method == http.MethodGet {
			return stubResp{Status: 200, Text: testDiff}
		}
		return stubResp{Status: 200, Text: "{}"}
	})
}

func TestDefaultEndpoints(t *testing.T) {
	want := endpoints{
		OpenRouter: "https://openrouter.ai/api/v1", OpenAI: "https://api.openai.com/v1",
		Anthropic: "https://api.anthropic.com/v1", GitHub: "https://api.github.com",
	}
	if got := defaultEndpoints(); got != want {
		t.Errorf("defaultEndpoints() = %+v, want %+v", got, want)
	}
}

func TestEnvKeys_OnlyTheTwoNames(t *testing.T) {
	all := func(name string) string { return "value-of-" + name }
	k := envKeys{getenv: all}
	for _, name := range []string{keynames.OpenRouterKey, keynames.GitHubToken} {
		if got, want := k.Get(name), "value-of-"+name; got != want {
			t.Errorf("Get(%q) = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "PATH", ""} {
		if got := k.Get(name); got != "" {
			t.Errorf("Get(%q) = %q, want empty: the runner knows two names only", name, got)
		}
	}
}

// A10, EC29: the client of a built-in provider reads its one key under its own name, nothing else.
func TestSoleKey(t *testing.T) {
	k := soleKey{name: keynames.OpenAIKey, key: "sk-openai"}
	if got := k.Get(keynames.OpenAIKey); got != "sk-openai" {
		t.Errorf("Get(%q) = %q, want the key", keynames.OpenAIKey, got)
	}
	for _, name := range []string{keynames.OpenRouterKey, keynames.AnthropicKey, keynames.GitHubToken, ""} {
		if got := k.Get(name); got != "" {
			t.Errorf("Get(%q) = %q, want empty", name, got)
		}
	}
}

func TestRun_ReviewsAndPosts(t *testing.T) {
	w := newWorkspace(t, testManifest, nil)
	gh := gitHubOK(t)
	or := openRouterReply(t, []map[string]any{{
		"id": "f1", "severity": "CRITICAL", "category": "security", "title": "Hardcoded key", "file": "src/config.ts",
		"start_line": 10, "end_line": 10, "rationale": "A live key is committed.", "confidence": 0.9, "kind": "finding",
	}})

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (the critical finding triggers the gate)\nstderr: %s", code, &stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	wantLines := []string{
		"[agent-runner] trace manifest_version=none model=deepseek/deepseek-v4-flash skills=none commit=unknown runner=2 build=dev",
		"[agent-runner] findings=1 blockers=1 gateTriggered=true posted=github_review",
	}
	if diff := cmp.Diff(wantLines, lines); diff != "" {
		t.Errorf("stdout mismatch (-want +got):\n%s", diff)
	}
	art, ok := w.artifact(t)
	if !ok || !strings.Contains(art, `"cost_usd": 0.0025`) || !strings.Contains(art, `"findings_count": 1`) {
		t.Errorf("artifact = %q, %v; want cost 0.0025 and one finding", art, ok)
	}
	reqs := gh.requests()
	if len(reqs) != 2 || reqs[1].Method != http.MethodPost || !strings.HasSuffix(reqs[1].URL, "/pulls/42/reviews") {
		t.Fatalf("GitHub requests = %+v, want the diff GET and one review POST", reqs)
	}
	if ev := reqs[1].Body.(map[string]any)["event"]; ev != "REQUEST_CHANGES" {
		t.Errorf("review event = %v, want REQUEST_CHANGES", ev)
	}
}

func TestRun_CleanReviewExitsZero(t *testing.T) {
	w := newWorkspace(t, testManifest, nil)
	gh := gitHubOK(t)
	or := openRouterReply(t, nil)
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, &stderr)
	}
	reqs := gh.requests()
	if len(reqs) != 2 || reqs[1].Body.(map[string]any)["event"] != "COMMENT" {
		t.Errorf("GitHub requests = %+v, want the review posted as COMMENT (the Actions token cannot approve)", reqs)
	}
}

func TestRun_FailureBeforeTheArtifact(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		env      map[string]string
		wantLine string
	}{
		{
			name: "invalid manifest", manifest: "name: A\nmodel: m\n",
			wantLine: "failed validation: system_prompt: Required",
		},
		{
			name: "no repository", manifest: testManifest, env: map[string]string{"GITHUB_REPOSITORY": "widgets"},
			wantLine: `GITHUB_REPOSITORY must be set to "owner/name" (got: "widgets")`,
		},
		{
			name: "no token", manifest: testManifest, env: map[string]string{"GITHUB_TOKEN": ""},
			wantLine: "GITHUB_TOKEN is required to post as 'github_review'",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t, tc.manifest, tc.env)
			gh := gitHubOK(t)
			or := openRouterReply(t, nil)
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), "[agent-runner] FAILED: ") || !strings.Contains(stderr.String(), tc.wantLine) {
				t.Errorf("stderr = %q, want a FAILED line containing %q", stderr.String(), tc.wantLine)
			}
			if _, ok := w.artifact(t); ok {
				t.Error("an artifact was written")
			}
			if n := len(gh.requests()); n != 0 {
				t.Errorf("GitHub requests = %d, want 0", n)
			}
		})
	}
}

func TestRun_NoManifestDirectory(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"DEVDIGEST_DIR": filepath.Join(root, "nope"), "GITHUB_TOKEN": "t", "GITHUB_REPOSITORY": "a/b"}
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), func(k string) (string, bool) { v, ok := env[k]; return v, ok }, &stdout, &stderr, defaultEndpoints())
	want := "[agent-runner] FAILED: Agent manifest directory not found: " + filepath.Join(root, "nope", "agents") +
		" (ENOENT: no such file or directory, scandir '" + filepath.Join(root, "nope", "agents") + "')\n"
	if code != 1 || stderr.String() != want {
		t.Errorf("run() = %d, stderr %q; want 1 and %q", code, stderr.String(), want)
	}
}

func TestRun_CancelledContextFails(t *testing.T) {
	w := newWorkspace(t, testManifest, nil)
	gh := gitHubOK(t)
	or := openRouterReply(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer
	code := run(ctx, w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})
	if code != 1 {
		t.Errorf("exit code with a cancelled context = %d, want 1", code)
	}
	if _, ok := w.artifact(t); ok {
		t.Error("an artifact was written after cancellation")
	}
}
