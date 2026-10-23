package app_test

// The job log of the runner is public on a public repository (spec 7.8.9, NFR6005): a failure
// line carries no diff, prompt or model text, is cut, and no value printed on the log can be read
// as a GitHub Actions workflow command (security r1 F1, F2).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"dev-digest-go/backend/internal/ci/runner/app"
	"dev-digest-go/backend/internal/platform/apperr"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

const quotedDiffLine = "+apiKey: 'sk_live_abcdef123456'"

func failingEngine(err error) app.Engine {
	return func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
		return reviewer.Outcome{}, err
	}
}

func validFiles() *fakeFiles {
	return &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": manifest("")}}
}

// noCommand fails when text holds a sequence the Actions runner would read as a command.
func noCommand(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, "##[") {
		t.Errorf("%s holds a legacy workflow command: %q", where, text)
	}
	if strings.Contains(text, "::") {
		t.Errorf("%s holds a :: workflow command: %q", where, text)
	}
}

func TestRun_ModelFailureLineHoldsNoModelOutput(t *testing.T) {
	// what llm.Structured returns after three bad answers: the issues and a cut of the raw output
	raw := quotedDiffLine + " ##[add-mask]sk_live_abcdef123456 ##[stop-commands]zz"
	schema := apperr.ExternalService(fmt.Sprintf("OpenRouter structured output failed schema validation for Review after 3 attempt(s). Last issues:\n- verdict: Required\nLast raw output (%d chars): %s", len(raw), raw))

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"schema failure", schema, "the model call failed (schema_failure)"},
		{"provider status", apperr.ExternalService("OpenRouter chat completions returned 502"), "the model call failed (http 502)"},
		{"provider message after the colon", apperr.ExternalService("OpenRouter returned no choices for Review: returned 418 " + quotedDiffLine), "the model call failed (provider_error)"},
		{"budget", apperr.ExternalService("Review exceeded its total budget of 360000ms"), "the model call failed (timeout)"},
		{"cancelled", fmt.Errorf("Review was cancelled: %w", context.Canceled), "the model call failed (cancelled)"},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), "the model call failed (timeout)"},
		{"not configured", apperr.Config("OPENROUTER_API_KEY is not configured"), "the model call failed (not_configured)"},
		{"anything else", errors.New("boom " + quotedDiffLine), "the model call failed (error)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, o := run(t, validFiles(), &fakeGitHub{diff: diff}, baseEnv(), failingEngine(tc.err))
			if code != 1 || len(o.stderr) != 1 {
				t.Fatalf("Run() = %d, stderr %q; want 1 and one line", code, o.stderr)
			}
			if want := "[agent-runner] FAILED: " + tc.want; o.stderr[0] != want {
				t.Errorf("failure line = %q, want %q", o.stderr[0], want)
			}
		})
	}
}

func TestRun_FailureLineIsCutAtAUTF8Boundary(t *testing.T) {
	// a 3-byte rune at every offset of the cut
	for pad := 0; pad < 3; pad++ {
		t.Run(fmt.Sprintf("pad %d", pad), func(t *testing.T) {
			bad := strings.Repeat("x", pad) + strings.Repeat("€", 2000)
			files := &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": manifest("skills: [" + bad + "]\n")}}
			code, o := run(t, files, &fakeGitHub{diff: diff}, baseEnv(), nil)
			if code != 1 || len(o.stderr) != 1 {
				t.Fatalf("Run() = %d, stderr %d lines; want 1 and one line", code, len(o.stderr))
			}
			line := o.stderr[0]
			if len(line) > app.MaxFailureBytes {
				t.Errorf("failure line is %d bytes, want at most %d", len(line), app.MaxFailureBytes)
			}
			if !utf8.ValidString(line) || !strings.HasSuffix(line, "…") {
				t.Errorf("failure line = %q, want valid UTF-8 ending in the cut mark", line[len(line)-20:])
			}
		})
	}
}

func TestRun_EnumValueIsNotEchoed(t *testing.T) {
	for _, field := range []string{"strategy", "ci_fail_on", "provider"} {
		t.Run(field, func(t *testing.T) {
			huge := strings.Repeat("A", 1<<20-200)
			files := &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": "name: Rev\nmodel: m1\nsystem_prompt: p\n" + field + ": " + huge + "\n"}}
			code, o := run(t, files, &fakeGitHub{diff: diff}, baseEnv(), nil)
			if code != 1 || len(o.stderr) != 1 {
				t.Fatalf("Run() = %d, stderr %d lines; want 1 and one line", code, len(o.stderr))
			}
			if got := o.stderr[0]; len(got) > app.MaxFailureBytes || strings.Contains(got, "AAAA") ||
				!strings.Contains(got, field+": Invalid enum value. Expected '") || strings.Contains(got, "received") {
				t.Errorf("failure line (%d bytes) = %.300q, want the enum issue without the value", len(got), got)
			}
		})
	}
}

func TestRun_FailureLineRedactsBothSecretsAndBreaksCommands(t *testing.T) {
	e := baseEnv()
	e["OPENROUTER_API_KEY"] = "sk-or-secret-0123456789"
	bad := "../##[add-mask]x::error::" + e["OPENROUTER_API_KEY"] + e["GITHUB_TOKEN"]
	files := &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": manifest("skills: ['" + bad + "']\n")}}
	code, o := run(t, files, &fakeGitHub{diff: diff}, e, nil)
	if code != 1 || len(o.stderr) != 1 {
		t.Fatalf("Run() = %d, stderr %q; want 1 and one line", code, o.stderr)
	}
	line := o.stderr[0]
	for _, secret := range []string{e["OPENROUTER_API_KEY"], e["GITHUB_TOKEN"]} {
		if strings.Contains(line, secret) {
			t.Errorf("failure line holds a secret: %q", line)
		}
	}
	if !strings.Contains(line, "[REDACTED]") {
		t.Errorf("failure line = %q, want the secrets redacted (not dropped silently)", line)
	}
	noCommand(t, "failure line", strings.TrimPrefix(line, "[agent-runner] FAILED: "))
}

func TestRun_TraceLineBreaksCommandsInTheModelName(t *testing.T) {
	files := &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": "name: Rev\nmodel: \"m ##[error]spoof ##[add-mask]x\\n::stop-commands::t\"\nsystem_prompt: p\n"}}
	ok := func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
		return reviewer.Outcome{}, nil
	}
	code, o := run(t, files, &fakeGitHub{diff: diff}, baseEnv(), ok)
	if code != 0 || len(o.stdout) == 0 {
		t.Fatalf("Run() = %d, stdout %q; want 0 and a trace", code, o.stdout)
	}
	trace := o.stdout[0]
	if !strings.Contains(trace, "model=m #") || strings.Contains(trace, "\n") {
		t.Errorf("trace = %q, want the model name on one line", trace)
	}
	noCommand(t, "trace line", trace)
}

func TestBreakCommands(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "model a/b: c", "model a/b: c"},
		{"legacy command", "x ##[error]y", "x #\u200b#[error]y"},
		{"three hashes", "###[x", "##\u200b#[x"},
		{"colons", "::add-mask::v", ":\u200b:add-mask:\u200b:v"},
		{"run of colons", ":::", ":\u200b:\u200b:"},
		{"single colon and url", "https://a:b", "https://a:b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := app.BreakCommands(tc.in)
			if got != tc.want {
				t.Errorf("BreakCommands(%q) = %q, want %q", tc.in, got, tc.want)
			}
			noCommand(t, "BreakCommands", got)
		})
	}
}
