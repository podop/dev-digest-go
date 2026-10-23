package main

// The job log is public on a public repository (spec 7.8.9, NFR6005; security r1 F1, F2): after a
// failure it holds no diff, prompt or model text and is cut, and nothing printed can be read by
// GitHub Actions as a workflow command.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
)

// hostileText is what a model steered by the diff can answer: a diff line, a secret-looking
// value and workflow commands, in every form the runner reads.
const hostileText = "I will not answer in JSON. " + "+apiKey: 'sk_live_abcdef123456' ##[add-mask]sk_live_abcdef123456 ##[stop-commands]zz ::error::spoof"

// textOpenRouter answers every chat completion with plain text instead of JSON, so the schema
// check fails on all attempts and the model layer builds its error from the raw output.
func textOpenRouter(t *testing.T, text string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "created": 1, "model": "m",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15, "cost": 0.0025},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestJobLog_SchemaFailureHoldsNoModelOutput(t *testing.T) {
	w := newWorkspace(t, testManifest, map[string]string{"OPENROUTER_API_KEY": keyMarker, "GITHUB_TOKEN": tokenMarker})
	gh := gitHubOK(t)
	or := textOpenRouter(t, hostileText+strings.Repeat(" filler", 700))

	var stdout, stderr bytes.Buffer
	code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, &stderr)
	}
	log := stdout.String() + stderr.String()
	for _, leak := range []string{"sk_live", "apiKey", "I will not answer", "add-mask", "Last raw output"} {
		if strings.Contains(log, leak) {
			t.Errorf("job log holds %q:\n%s", leak, log)
		}
	}
	if want := "[agent-runner] FAILED: the model call failed (schema_failure)\n"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to hold %q", stderr.String(), want)
	}
	assertNoMarkers(t, "job log", log)
}

func TestJobLog_HugeEnumValueIsNotEchoed(t *testing.T) {
	manifest := "name: A\nmodel: m\nsystem_prompt: p\nstrategy: " + strings.Repeat("Z", 1<<20-100) + "\n"
	w := newWorkspace(t, manifest, nil)

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), w.lookup, &stdout, &stderr, defaultEndpoints()); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if n := stderr.Len(); n > runnerapp.MaxFailureBytes+1 {
		t.Errorf("stderr is %d bytes, want at most %d", n, runnerapp.MaxFailureBytes+1)
	}
	if strings.Contains(stderr.String(), "ZZZZ") {
		t.Errorf("stderr echoes the value: %.200q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "strategy: Invalid enum value. Expected 'auto' | 'single-pass' | 'map-reduce'") {
		t.Errorf("stderr = %.300q, want the enum issue", stderr.String())
	}
}

func TestJobLog_WorkflowCommandsInManifestValuesAreBroken(t *testing.T) {
	manifest := "name: A\nmodel: \"m ##[error]spoof ##[add-mask]x ::warning::y\"\nsystem_prompt: p\nstrategy: single-pass\n"
	w := newWorkspace(t, manifest, map[string]string{"OPENROUTER_API_KEY": keyMarker, "GITHUB_TOKEN": tokenMarker})
	gh := gitHubOK(t)
	or := openRouterReply(t, nil)

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: or.URL + "/v1", GitHub: gh.srv.URL}); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, &stderr)
	}
	if !strings.Contains(stdout.String(), "[agent-runner] trace ") {
		t.Fatalf("stdout = %q, want the trace line", stdout.String())
	}
	for name, text := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if strings.Contains(text, "##[") || strings.Contains(text, "::") {
			t.Errorf("%s holds a workflow command: %q", name, text)
		}
	}
}

func TestJobLog_ModelLayerWarningsAreBroken(t *testing.T) {
	// an answer without usage makes the model layer warn with the model name
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		review, _ := json.Marshal(map[string]any{"verdict": "approve", "summary": "ok", "score": 90, "findings": []any{}})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "created": 1, "model": "m",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(review)}, "finish_reason": "stop"}},
		})
	}))
	t.Cleanup(srv.Close)
	manifest := "name: A\nmodel: \"m ##[error]spoof\"\nsystem_prompt: p\nstrategy: single-pass\n"
	w := newWorkspace(t, manifest, nil)
	gh := gitHubOK(t)

	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: srv.URL + "/v1", GitHub: gh.srv.URL}); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, &stderr)
	}
	if !strings.Contains(stderr.String(), "returned no usage") {
		t.Fatalf("stderr = %q, want the usage warning (the path under test)", stderr.String())
	}
	if strings.Contains(stderr.String(), "##[") {
		t.Errorf("stderr holds a legacy workflow command: %q", stderr.String())
	}
}

func TestWithCommandsStopped(t *testing.T) {
	stopLine := regexp.MustCompile(`^::stop-commands::([A-Z2-7]{26})$`)
	runOnce := func(body func() int) (int, []string) {
		var out bytes.Buffer
		code := withCommandsStopped(&out, body)
		return code, strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	}

	var tokens []string
	for range 2 {
		code, lines := runOnce(func() int { return 7 })
		if code != 7 || len(lines) != 2 {
			t.Fatalf("withCommandsStopped() = %d, %q; want 7 and a stop and a resume line", code, lines)
		}
		m := stopLine.FindStringSubmatch(lines[0])
		if m == nil {
			t.Fatalf("first line = %q, want ::stop-commands::<token>", lines[0])
		}
		if lines[1] != "::"+m[1]+"::" {
			t.Errorf("last line = %q, want the resume line for the same token", lines[1])
		}
		tokens = append(tokens, m[1])
	}
	if tokens[0] == tokens[1] {
		t.Errorf("two runs used the token %q: it must be random", tokens[0])
	}

	// the resume line is printed even when the body panics
	var out bytes.Buffer
	func() {
		defer func() { _ = recover() }()
		withCommandsStopped(&out, func() int { panic("boom") })
	}()
	if !strings.HasSuffix(out.String(), "::\n") || strings.Count(out.String(), "\n") != 2 {
		t.Errorf("output after a panic = %q, want the stop and the resume line", out.String())
	}
}

func TestCommandSafeWriter(t *testing.T) {
	var out bytes.Buffer
	in := "time=1 level=WARN msg=\"m ##[error]x ::stop-commands::t\"\n"
	n, err := commandSafeWriter{w: &out}.Write([]byte(in))
	if err != nil || n != len(in) {
		t.Fatalf("Write() = %d, %v; want %d, nil", n, err, len(in))
	}
	if strings.Contains(out.String(), "##[") || strings.Contains(out.String(), "::") || !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("written = %q, want the commands broken and the line end kept", out.String())
	}
}
