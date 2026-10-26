package app_test

// The runner for a manifest whose provider is not openrouter (spec FR30, FR36, NFR7; AC35, AC46):
// the manifest block, the environment check before any GitHub call, the single key the provider
// reads, the scrubbing of every known secret, the manifest price and the strict-mode hint.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	cidomain "dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/runner/app"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

const (
	gatewayKey   = "sk-RUNNER-SECRET-7"
	openRouter   = "sk-OR-MARKER-5"
	openAIKey    = "sk-OPENAI-RUNNER-3"
	headerValue1 = "tenant-value-0001"
	headerValue2 = "tenant-value-0002"
)

const customBlock = `custom_provider:
  protocol: "openai"
  auth_mode: "bearer"
  auth_header: null
  extra_headers:
    - "X-Tenant"
    - "X-Team"
  structured_output: "strict"
  pricing:
    promptPerM: 2
    completionPerM: 8
`

func customManifest(block string) string {
	return "name: Rev\nprovider: custom:my-gw\nmodel: m1\nsystem_prompt: be strict\nstrategy: auto\nci_fail_on: warning\nversion: 3\n" + block
}

func customEnv() env {
	e := baseEnv()
	e["DEVDIGEST_LLM_BASE_URL"] = "https://gw.example.com/v1/"
	e["DEVDIGEST_LLM_API_KEY"] = gatewayKey
	e["DEVDIGEST_LLM_HEADER_1"] = headerValue1
	e["DEVDIGEST_LLM_HEADER_2"] = headerValue2
	e["OPENROUTER_API_KEY"] = openRouter
	return e
}

// countingGitHub counts every call, so a test can prove nothing reached GitHub.
type countingGitHub struct {
	calls atomic.Int32
	diff  string
	reply error
}

func (g *countingGitHub) FetchDiff(context.Context, string, app.PullRef) (string, error) {
	g.calls.Add(1)
	return g.diff, nil
}
func (g *countingGitHub) PostComment(context.Context, string, app.PullRef, string) error {
	g.calls.Add(1)
	return g.reply
}
func (g *countingGitHub) PostReview(context.Context, string, app.PullRef, app.ReviewPost) error {
	g.calls.Add(1)
	return g.reply
}

// fakeProviders records the selections it is asked for.
type fakeProviders struct {
	got []app.ProviderSelection
	err error
}

func (p *fakeProviders) For(sel app.ProviderSelection) (reviewerapp.LLM, error) {
	p.got = append(p.got, sel)
	return nil, p.err
}

// countingLLM is the OpenRouter port; a non-openrouter manifest must never reach it.
type countingLLM struct{ calls int }

func (l *countingLLM) For(string) reviewerapp.LLM { l.calls++; return nil }

func okEngine(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
	return reviewer.Outcome{}, nil
}

func failEngine(err error) app.Engine {
	return func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
		return reviewer.Outcome{}, err
	}
}

type harness struct {
	files     *fakeFiles
	gh        *countingGitHub
	providers *fakeProviders
	llm       *countingLLM
	out       *out
}

func runCustom(t *testing.T, manifestText string, e env, engine app.Engine) (int, *harness) {
	t.Helper()
	h := &harness{
		files:     &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": manifestText}},
		gh:        &countingGitHub{diff: diff},
		providers: &fakeProviders{},
		llm:       &countingLLM{},
		out:       &out{},
	}
	if engine == nil {
		engine = okEngine
	}
	code := app.Run(t.Context(), app.Config{BuildID: "dev"}, app.Deps{
		Env: e, Files: h.files, GitHub: h.gh, LLM: h.llm, Providers: h.providers, Out: h.out, Engine: engine,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	return code, h
}

func (h *harness) noArtifactNoGitHub(t *testing.T) {
	t.Helper()
	if len(h.files.written) != 0 {
		t.Errorf("artifact written: %v, want none", h.files.written)
	}
	if n := h.gh.calls.Load(); n != 0 {
		t.Errorf("GitHub calls = %d, want 0", n)
	}
}

func TestLoadManifest_CustomProvider(t *testing.T) {
	files := &fakeFiles{files: map[string]string{"/d/agents/rev.yaml": customManifest(customBlock)}}
	got, err := app.LoadManifest(files, "/d", "")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	want := &app.CustomProvider{
		Protocol: custom.ProtocolOpenAI, AuthMode: custom.AuthBearer,
		ExtraHeaders: []string{"X-Tenant", "X-Team"}, StructuredOutput: custom.ModeStrict,
	}
	if diff := cmp.Diff(want, got.CustomProvider); diff != "" {
		t.Errorf("CustomProvider mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(&cidomain.Pricing{PromptPerM: 2, CompletionPerM: 8}, got.Pricing); diff != "" {
		t.Errorf("Pricing mismatch (-want +got):\n%s", diff)
	}
	if got.Provider != "custom:my-gw" {
		t.Errorf("Provider = %q, want custom:my-gw", got.Provider)
	}
}

func TestLoadManifest_HeaderAuthAndDefaults(t *testing.T) {
	block := "custom_provider:\n  protocol: anthropic\n  auth_mode: header\n  auth_header: x-api-key\n"
	files := &fakeFiles{files: map[string]string{"/d/agents/rev.yaml": customManifest(block)}}
	got, err := app.LoadManifest(files, "/d", "")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	want := &app.CustomProvider{
		Protocol: custom.ProtocolAnthropic, AuthMode: custom.AuthHeader, AuthHeader: "x-api-key",
		ExtraHeaders: []string{}, StructuredOutput: custom.ModeStrict,
	}
	if diff := cmp.Diff(want, got.CustomProvider); diff != "" {
		t.Errorf("CustomProvider mismatch (-want +got):\n%s", diff)
	}
	if got.Pricing != nil {
		t.Errorf("Pricing = %+v, want nil (pricing: absent)", got.Pricing)
	}
}

func TestLoadManifest_CustomProviderInvalid(t *testing.T) {
	tests := []struct {
		name  string
		block string
		want  string
	}{
		{"block missing", "", "custom_provider: Required"},
		{"block null", "custom_provider: null\n", "custom_provider: Required"},
		{"block not an object", "custom_provider: true\n", "custom_provider: Expected object, received boolean"},
		{"protocol missing", "custom_provider:\n  auth_mode: bearer\n", "custom_provider.protocol: Required"},
		{"protocol unknown", "custom_provider:\n  protocol: grpc\n  auth_mode: bearer\n", "custom_provider.protocol: Invalid enum value. Expected 'openai' | 'anthropic'"},
		{"auth mode unknown", "custom_provider:\n  protocol: openai\n  auth_mode: basic\n", "custom_provider.auth_mode: Invalid enum value. Expected 'bearer' | 'header'"},
		{"header mode without header", "custom_provider:\n  protocol: openai\n  auth_mode: header\n", "custom_provider.auth_header: Required"},
		{"auth header reserved", "custom_provider:\n  protocol: openai\n  auth_mode: header\n  auth_header: Authorization\n", "custom_provider.auth_header: Header name is reserved"},
		{"six headers", "custom_provider:\n  protocol: openai\n  auth_mode: bearer\n  extra_headers: [a, b, c, d, e, f]\n", "custom_provider.extra_headers: Array must contain at most 5 element(s)"},
		{"header duplicates auth header", "custom_provider:\n  protocol: openai\n  auth_mode: header\n  auth_header: X-Key\n  extra_headers: [x-key]\n", "custom_provider.extra_headers.0: Header name duplicates the auth header"},
		{"header duplicated", "custom_provider:\n  protocol: openai\n  auth_mode: bearer\n  extra_headers: [X-A, x-a]\n", "custom_provider.extra_headers.1: Header name is duplicated"},
		{"header not a token", "custom_provider:\n  protocol: openai\n  auth_mode: bearer\n  extra_headers: [\"a b\"]\n", "custom_provider.extra_headers.0: Header name must be a valid HTTP token"},
		{"json_object on anthropic", "custom_provider:\n  protocol: anthropic\n  auth_mode: header\n  auth_header: k\n  structured_output: json_object\n", "custom_provider.structured_output: json_object needs the openai protocol"},
		{"mode unknown", "custom_provider:\n  protocol: openai\n  auth_mode: bearer\n  structured_output: loose\n", "custom_provider.structured_output: Invalid enum value. Expected 'strict' | 'json_object' | 'prompt_only'"},
		{"negative price", "custom_provider:\n  protocol: openai\n  auth_mode: bearer\n  pricing:\n    promptPerM: -1\n    completionPerM: 2\n", "custom_provider.pricing.promptPerM: Number must be finite and greater than or equal to 0"},
		{"infinite price", "custom_provider:\n  protocol: openai\n  auth_mode: bearer\n  pricing:\n    promptPerM: .inf\n    completionPerM: 2\n", "custom_provider.pricing.promptPerM: Number must be finite and greater than or equal to 0"},
		{"half a price", "custom_provider:\n  protocol: openai\n  auth_mode: bearer\n  pricing:\n    promptPerM: 1\n", "custom_provider.pricing.completionPerM: Required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := &fakeFiles{files: map[string]string{"/d/agents/rev.yaml": customManifest(tc.block)}}
			_, err := app.LoadManifest(files, "/d", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("LoadManifest() error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadManifest_ProviderValues(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		extra    string
		wantErr  string
		wantPr   *cidomain.Pricing
	}{
		{name: "openai prices from the top-level key", provider: "openai", extra: "pricing:\n  promptPerM: 2\n  completionPerM: 8\n", wantPr: &cidomain.Pricing{PromptPerM: 2, CompletionPerM: 8}},
		{name: "anthropic without a price", provider: "anthropic"},
		{name: "openrouter never reads pricing", provider: "openrouter", extra: "pricing:\n  promptPerM: -5\n"},
		{name: "custom ignores the top-level price", provider: "custom:gw", extra: "custom_provider:\n  protocol: openai\n  auth_mode: bearer\npricing:\n  promptPerM: 9\n  completionPerM: 9\n"},
		{name: "openai bad price", provider: "openai", extra: "pricing:\n  promptPerM: -1\n  completionPerM: 1\n", wantErr: "pricing.promptPerM: Number must be finite"},
		{name: "uppercase custom id", provider: "custom:Gw", wantErr: "provider: Invalid enum value. Expected 'openai' | 'anthropic' | 'openrouter'"},
		{name: "custom id without slug", provider: `"custom:"`, wantErr: "provider: Invalid enum value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := "name: n\nprovider: " + tc.provider + "\nmodel: m\nsystem_prompt: p\n" + tc.extra
			files := &fakeFiles{files: map[string]string{"/d/agents/rev.yaml": text}}
			got, err := app.LoadManifest(files, "/d", "")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("LoadManifest() error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadManifest() error = %v", err)
			}
			if diff := cmp.Diff(tc.wantPr, got.Pricing); diff != "" {
				t.Errorf("Pricing mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// AC35 / EC18: an unusable environment fails before the first GitHub call and writes no artifact.
func TestRun_CustomEnvironmentChecked(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		mutate   func(env)
		want     []string
	}{
		{"base URL empty", customBlock, func(e env) { e["DEVDIGEST_LLM_BASE_URL"] = "" },
			[]string{"DEVDIGEST_LLM_BASE_URL", "DEVDIGEST_MY_GW_BASE_URL", "is empty"}},
		{"base URL unset", customBlock, func(e env) { delete(e, "DEVDIGEST_LLM_BASE_URL") },
			[]string{"DEVDIGEST_LLM_BASE_URL", "DEVDIGEST_MY_GW_BASE_URL", "is empty"}},
		{"base URL ftp", customBlock, func(e env) { e["DEVDIGEST_LLM_BASE_URL"] = "ftp://x" },
			[]string{"DEVDIGEST_LLM_BASE_URL", "DEVDIGEST_MY_GW_BASE_URL", "must start with http:// or https://"}},
		{"base URL link-local", customBlock, func(e env) { e["DEVDIGEST_LLM_BASE_URL"] = "http://169.254.169.254/v1" },
			[]string{"DEVDIGEST_LLM_BASE_URL", "DEVDIGEST_MY_GW_BASE_URL", "link-local"}},
		{"header 2 empty", customBlock, func(e env) { e["DEVDIGEST_LLM_HEADER_2"] = "" },
			[]string{"DEVDIGEST_LLM_HEADER_2", "DEVDIGEST_MY_GW_HEADER_2", "is empty"}},
		{"header value with a line break", customBlock, func(e env) { e["DEVDIGEST_LLM_HEADER_1"] = "a\r\nX-Evil: 1" },
			[]string{"DEVDIGEST_LLM_HEADER_1", "CR, LF or NUL"}},
		{"key with a line break", customBlock, func(e env) { e["DEVDIGEST_LLM_API_KEY"] = "k\nX-Evil: 1" },
			[]string{"DEVDIGEST_LLM_API_KEY", "CR, LF or NUL"}},
		{"block missing", "", func(env) {}, []string{"custom_provider: Required"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := customEnv()
			tc.mutate(e)
			code, h := runCustom(t, customManifest(tc.manifest), e, nil)
			if code != 1 {
				t.Fatalf("Run() = %d, want 1", code)
			}
			h.noArtifactNoGitHub(t)
			if len(h.out.stderr) != 1 {
				t.Fatalf("stderr = %q, want one failure line", h.out.stderr)
			}
			for _, w := range tc.want {
				if !strings.Contains(h.out.stderr[0], w) {
					t.Errorf("failure line %q lacks %q", h.out.stderr[0], w)
				}
			}
			if len(h.providers.got) != 0 || h.llm.calls != 0 {
				t.Errorf("an adapter was built for an unusable environment (providers %d, openrouter %d)", len(h.providers.got), h.llm.calls)
			}
			for _, secret := range []string{gatewayKey, headerValue1, headerValue2, openRouter} {
				if strings.Contains(h.out.stderr[0], secret) {
					t.Errorf("failure line holds a secret value: %q", h.out.stderr[0])
				}
			}
		})
	}
}

func TestRun_CustomPlainHTTPWarning(t *testing.T) {
	tests := []struct {
		url      string
		wantWarn int
	}{
		{"http://gw.corp/v1", 1},
		{"http://10.0.0.5:8080/v1", 1},
		{"http://localhost:8080/v1", 0},
		{"http://127.0.0.1:8080/v1", 0},
		{"http://[::1]/v1", 0},
		{"https://gw.corp/v1", 0},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			e := customEnv()
			e["DEVDIGEST_LLM_BASE_URL"] = tc.url
			code, h := runCustom(t, customManifest(customBlock), e, nil)
			if code != 0 {
				t.Fatalf("Run() = %d, want 0 (%v)", code, h.out.stderr)
			}
			if len(h.out.stderr) != tc.wantWarn {
				t.Fatalf("stderr = %q, want %d warning line(s)", h.out.stderr, tc.wantWarn)
			}
			if tc.wantWarn == 1 {
				line := h.out.stderr[0]
				if !strings.Contains(line, "warning") || !strings.Contains(line, "plain http") || strings.Contains(line, gatewayKey) {
					t.Errorf("warning line = %q, want a plain-http warning without secrets", line)
				}
			}
		})
	}
}

// FR30, A10: the selection holds the gateway's key and headers and nothing of OpenRouter.
func TestRun_CustomSelection(t *testing.T) {
	code, h := runCustom(t, customManifest(customBlock), customEnv(), nil)
	if code != 0 {
		t.Fatalf("Run() = %d, want 0 (%v)", code, h.out.stderr)
	}
	if h.llm.calls != 0 {
		t.Errorf("the OpenRouter port was used %d time(s), want 0", h.llm.calls)
	}
	want := []app.ProviderSelection{{
		Provider: "custom:my-gw", Model: "m1", Key: gatewayKey,
		Price: &cidomain.Pricing{PromptPerM: 2, CompletionPerM: 8},
		Custom: &app.CustomEndpoint{
			BaseURL: "https://gw.example.com/v1", Protocol: custom.ProtocolOpenAI, AuthMode: custom.AuthBearer,
			Headers: []app.Header{{Name: "X-Tenant", Value: headerValue1}, {Name: "X-Team", Value: headerValue2}},
			Mode:    custom.ModeStrict,
		},
	}}
	if diff := cmp.Diff(want, h.providers.got); diff != "" {
		t.Errorf("selections mismatch (-want +got):\n%s", diff)
	}
	if got := fmt.Sprintf("%+v", h.providers.got); strings.Contains(got, openRouter) {
		t.Errorf("the selection carries the OpenRouter key: %s", got)
	}
	if diff := cmp.Diff([]string{gatewayKey, headerValue1, headerValue2}, h.providers.got[0].Secrets()); diff != "" {
		t.Errorf("Secrets() mismatch (-want +got):\n%s", diff)
	}
}

// EC19: an empty gateway key is allowed; the adapter then sends no auth header.
func TestRun_CustomEmptyKeyAllowed(t *testing.T) {
	for _, set := range []bool{true, false} {
		e := customEnv()
		if set {
			e["DEVDIGEST_LLM_API_KEY"] = ""
		} else {
			delete(e, "DEVDIGEST_LLM_API_KEY")
		}
		code, h := runCustom(t, customManifest(customBlock), e, nil)
		if code != 0 || len(h.providers.got) != 1 || h.providers.got[0].Key != "" {
			t.Errorf("Run() = %d with selections %+v (key set=%v), want 0 and an empty key", code, h.providers.got, set)
		}
	}
}

func TestRun_CustomHeaderAuthSelection(t *testing.T) {
	block := "custom_provider:\n  protocol: anthropic\n  auth_mode: header\n  auth_header: X-Gw-Key\n  structured_output: prompt_only\n"
	e := customEnv()
	e["DEVDIGEST_LLM_BASE_URL"] = "HTTPS://Gw.Example.com:8443/anthropic/"
	_, h := runCustom(t, customManifest(block), e, nil)
	if len(h.providers.got) != 1 {
		t.Fatalf("selections = %+v, want one", h.providers.got)
	}
	got := h.providers.got[0].Custom
	want := &app.CustomEndpoint{
		BaseURL: "https://gw.example.com:8443/anthropic", Protocol: custom.ProtocolAnthropic,
		AuthMode: custom.AuthHeader, AuthHeader: "X-Gw-Key", Headers: []app.Header{}, Mode: custom.ModePromptOnly,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("endpoint mismatch (-want +got):\n%s", diff)
	}
	if h.providers.got[0].Price != nil {
		t.Errorf("Price = %+v, want nil (no manifest price)", h.providers.got[0].Price)
	}
}

// FR36, EC29: a built-in provider reads its own key only; the OpenRouter key never reaches it.
func TestRun_BuiltInProviderReadsOnlyItsKey(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		set      map[string]string
		wantKey  string
	}{
		{"openai", "openai", map[string]string{"OPENAI_API_KEY": openAIKey, "ANTHROPIC_API_KEY": "sk-ANT-OTHER-9"}, openAIKey},
		{"anthropic", "anthropic", map[string]string{"ANTHROPIC_API_KEY": "sk-ANT-RUNNER-4", "OPENAI_API_KEY": openAIKey}, "sk-ANT-RUNNER-4"},
		{"anthropic key empty", "anthropic", map[string]string{"ANTHROPIC_API_KEY": ""}, ""},
		{"openai key unset", "openai", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := baseEnv()
			e["OPENROUTER_API_KEY"] = openRouter
			e["DEVDIGEST_LLM_API_KEY"] = gatewayKey
			for k, v := range tc.set {
				e[k] = v
			}
			text := "name: Rev\nprovider: " + tc.provider + "\nmodel: gpt-x\nsystem_prompt: p\npricing:\n  promptPerM: 2\n  completionPerM: 8\n"
			code, h := runCustom(t, text, e, nil)
			if code != 0 {
				t.Fatalf("Run() = %d, want 0 (%v)", code, h.out.stderr)
			}
			want := []app.ProviderSelection{{Provider: tc.provider, Model: "gpt-x", Key: tc.wantKey, Price: &cidomain.Pricing{PromptPerM: 2, CompletionPerM: 8}}}
			if diff := cmp.Diff(want, h.providers.got); diff != "" {
				t.Errorf("selections mismatch (-want +got):\n%s", diff)
			}
			if h.llm.calls != 0 {
				t.Errorf("the OpenRouter port was used %d time(s), want 0", h.llm.calls)
			}
		})
	}
}

// EC29: an empty key fails at the first model call: exit 1, no artifact, nothing posted.
func TestRun_BuiltInEmptyKeyFailsAtTheModelCall(t *testing.T) {
	e := baseEnv()
	e["OPENROUTER_API_KEY"] = openRouter
	e["ANTHROPIC_API_KEY"] = ""
	text := "name: Rev\nprovider: anthropic\nmodel: m\nsystem_prompt: p\n"
	code, h := runCustom(t, text, e, failEngine(apperr.Config("ANTHROPIC_API_KEY is not configured")))
	if code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if len(h.files.written) != 0 {
		t.Errorf("artifact written: %v", h.files.written)
	}
	if want := "[agent-runner] FAILED: the model call failed (not_configured)"; len(h.out.stderr) != 1 || h.out.stderr[0] != want {
		t.Errorf("stderr = %q, want %q", h.out.stderr, want)
	}
}

func TestRun_OpenRouterManifestUsesTheOpenRouterPort(t *testing.T) {
	code, h := runCustom(t, manifest(""), baseEnv(), nil)
	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if h.llm.calls != 1 || len(h.providers.got) != 0 {
		t.Errorf("OpenRouter port calls = %d, provider selections = %d; want 1 and 0", h.llm.calls, len(h.providers.got))
	}
}

func TestRun_NoProvidersPortFailsBeforeGitHub(t *testing.T) {
	h := &harness{
		files: &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": customManifest(customBlock)}},
		gh:    &countingGitHub{diff: diff}, out: &out{},
	}
	code := app.Run(t.Context(), app.Config{}, app.Deps{Env: customEnv(), Files: h.files, GitHub: h.gh, LLM: &countingLLM{}, Out: h.out, Engine: okEngine})
	if code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	h.noArtifactNoGitHub(t)
}

func TestRun_ProviderBuildFailureIsContentFree(t *testing.T) {
	h := &harness{
		files:     &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": customManifest(customBlock)}},
		gh:        &countingGitHub{diff: diff},
		providers: &fakeProviders{err: errors.New("dial " + gatewayKey)},
		out:       &out{},
	}
	code := app.Run(t.Context(), app.Config{}, app.Deps{Env: customEnv(), Files: h.files, GitHub: h.gh, Providers: h.providers, Out: h.out, Engine: okEngine})
	if code != 1 || len(h.out.stderr) != 1 || strings.Contains(h.out.stderr[0], gatewayKey) {
		t.Errorf("Run() = %d, stderr %q; want 1 and a line without the key", code, h.out.stderr)
	}
	h.noArtifactNoGitHub(t)
}

// NFR7, A10: every known secret value is scrubbed from the failure line, whichever provider the
// manifest names.
func TestRun_FailureLineScrubsEveryKnownSecret(t *testing.T) {
	secrets := map[string]string{
		"OPENROUTER_API_KEY":     "sk-or-value-0001",
		"OPENAI_API_KEY":         "sk-openai-0002",
		"ANTHROPIC_API_KEY":      "sk-anthropic-0003",
		"GITHUB_TOKEN":           "ghs_token_value_0004",
		"DEVDIGEST_LLM_API_KEY":  "gateway-key-0005",
		"DEVDIGEST_LLM_HEADER_1": "header-value-0006",
		"DEVDIGEST_LLM_HEADER_2": "header-value-0007",
		"DEVDIGEST_LLM_HEADER_3": "header-value-0008",
		"DEVDIGEST_LLM_HEADER_4": "header-value-0009",
		"DEVDIGEST_LLM_HEADER_5": "header-value-0010",
	}
	for _, tc := range []struct{ name, manifest string }{
		{"openrouter", manifest("")},
		{"custom", customManifest(customBlock)},
		{"openai", "name: n\nprovider: openai\nmodel: m\nsystem_prompt: p\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := customEnv()
			var all []string
			for k, v := range secrets {
				e[k] = v
				all = append(all, v)
			}
			e["DEVDIGEST_LLM_API_KEY"] = secrets["DEVDIGEST_LLM_API_KEY"]
			e["DEVDIGEST_LLM_HEADER_1"] = secrets["DEVDIGEST_LLM_HEADER_1"]
			e["DEVDIGEST_LLM_HEADER_2"] = secrets["DEVDIGEST_LLM_HEADER_2"]
			h := &harness{
				files: &fakeFiles{files: map[string]string{"/ws/.devdigest/agents/rev.yaml": tc.manifest}},
				gh:    &countingGitHub{diff: diff, reply: &app.APIError{Status: 500, Message: "echoed: " + strings.Join(all, " | ")}},
				out:   &out{}, providers: &fakeProviders{}, llm: &countingLLM{},
			}
			// the review is empty, so the post is a plain review; the 500 carries every secret
			code := app.Run(t.Context(), app.Config{}, app.Deps{Env: e, Files: h.files, GitHub: h.gh, LLM: h.llm, Providers: h.providers, Out: h.out, Engine: okEngine})
			if code != 1 || len(h.out.stderr) != 1 {
				t.Fatalf("Run() = %d, stderr %q; want 1 and one line", code, h.out.stderr)
			}
			for _, v := range all {
				if strings.Contains(h.out.stderr[0], v) {
					t.Errorf("failure line holds %q: %s", v, h.out.stderr[0])
				}
			}
			if got := strings.Count(h.out.stderr[0], "[REDACTED]"); got != len(all) {
				t.Errorf("failure line holds %d redactions, want %d: %s", got, len(all), h.out.stderr[0])
			}
		})
	}
}

// SCR4, FR33: the strict-mode hint names the provider id and only follows a 400 / 422 in strict mode.
func TestRun_StrictModeHint(t *testing.T) {
	block := func(protocol, mode string) string {
		auth := "  auth_mode: bearer\n"
		if protocol == "anthropic" {
			auth = "  auth_mode: header\n  auth_header: x-api-key\n"
		}
		return "custom_provider:\n  protocol: " + protocol + "\n" + auth + "  structured_output: " + mode + "\n"
	}
	rejected := func(status int) error {
		return apperr.ExternalService(fmt.Sprintf("gateway chat completions returned %d: {\"error\":\"%s\"}", status, gatewayKey))
	}
	const base = "[agent-runner] FAILED: the model call failed (http "
	const hintHead = "If this gateway does not support strict structured output, set Structured output of custom:my-gw to "
	tests := []struct {
		name  string
		block string
		err   error
		want  string
	}{
		{"openai strict 400", block("openai", "strict"), rejected(400), base + "400). " + hintHead + "JSON object or Prompt only."},
		{"openai strict 422", block("openai", "strict"), rejected(422), base + "422). " + hintHead + "JSON object or Prompt only."},
		{"anthropic strict 400", block("anthropic", "strict"), rejected(400), base + "400). " + hintHead + "Prompt only."},
		{"json_object 400", block("openai", "json_object"), rejected(400), base + "400)"},
		{"prompt_only 400", block("openai", "prompt_only"), rejected(400), base + "400)"},
		{"strict 500", block("openai", "strict"), rejected(500), base + "500)"},
		{"strict schema failure", block("openai", "strict"), apperr.ExternalService("failed schema validation for Review"), "[agent-runner] FAILED: the model call failed (schema_failure)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, h := runCustom(t, customManifest(tc.block), customEnv(), failEngine(tc.err))
			if code != 1 || len(h.out.stderr) != 1 || h.out.stderr[0] != tc.want {
				t.Errorf("Run() = %d, stderr %q; want 1 and %q", code, h.out.stderr, tc.want)
			}
			if len(h.files.written) != 0 || h.gh.calls.Load() != 1 {
				t.Errorf("artifact %v, GitHub calls %d; want none written and only the diff fetch", h.files.written, h.gh.calls.Load())
			}
		})
	}
	t.Run("openrouter manifest has no hint", func(t *testing.T) {
		code, h := runCustom(t, manifest(""), baseEnv(), failEngine(rejected(400)))
		if want := base + "400)"; code != 1 || len(h.out.stderr) != 1 || h.out.stderr[0] != want {
			t.Errorf("Run() = %d, stderr %q; want 1 and %q", code, h.out.stderr, want)
		}
	})
}

// P5, FR36, AC46: the manifest price over the reported tokens.
func TestManifestPricer_Estimate(t *testing.T) {
	two8 := &cidomain.Pricing{PromptPerM: 2, CompletionPerM: 8}
	tests := []struct {
		name         string
		price        *cidomain.Pricing
		in, out      int
		want         float64
		wantUnpriced bool
	}{
		{name: "AC46 1M in 500k out", price: two8, in: 1_000_000, out: 500_000, want: 6},
		{name: "zero tokens", price: two8, want: 0},
		{name: "free model", price: &cidomain.Pricing{}, in: 10, out: 10, want: 0},
		{name: "no price", price: nil, in: 1, out: 1, wantUnpriced: true},
		{name: "negative count", price: two8, in: -1, out: 1, wantUnpriced: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := app.ManifestPricer{Pricing: tc.price}.Estimate("any", tc.in, tc.out)
			switch {
			case tc.wantUnpriced && got != nil:
				t.Errorf("Estimate(%d, %d) = %v, want nil", tc.in, tc.out, *got)
			case !tc.wantUnpriced && (got == nil || *got != tc.want):
				t.Errorf("Estimate(%d, %d) = %v, want %v", tc.in, tc.out, got, tc.want)
			}
		})
	}
}
