package main

// The runner binary for a manifest whose provider is not openrouter (spec FR30, FR36, NFR7; EC19,
// EC29; AC34, AC44, AC46, AC49): the real adapters, the real HTTP client and the real use case
// against stub gateways and a stub GitHub, with nothing but the environment and the manifest.

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
)

const (
	cpGatewayKey = "sk-RUNNER-SECRET-7"
	cpOpenRouter = "sk-OR-MARKER-5"
	cpOpenAIKey  = "sk-OPENAI-RUNNER-3"
	cpAnthropic  = "sk-ANT-RUNNER-4"
	cpHeader1    = "tenant-value-0001"
	cpHeader2    = "tenant-value-0002"
)

// cpGatewayReq is one request a stub gateway saw.
type cpGatewayReq struct {
	Method, Path string
	Header       http.Header
	Raw          string
	Body         map[string]any
}

// cpGatewayOpts shapes the answers of a stub gateway.
type cpGatewayOpts struct {
	protocol string // "openai" (chat completions) or "anthropic" (messages)
	status   int    // 0 = 200 with a review; else that status with an error body echoing the request
	in, out  int    // reported tokens
	noUsage  bool
}

type cpGateway struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []cpGatewayReq
}

func newCPGateway(t *testing.T, o cpGatewayOpts) *cpGateway {
	t.Helper()
	g := &cpGateway{}
	g.srv = httptest.NewServer(http.HandlerFunc(g.handler(o)))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *cpGateway) handler(o cpGatewayOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		g.mu.Lock()
		g.reqs = append(g.reqs, cpGatewayReq{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Raw: string(raw), Body: body})
		g.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if o.status != 0 {
			// An error body that echoes the whole request: every header and the body.
			echo := "request " + r.URL.Path + " headers: " + strings.Join(cpHeaderLines(r.Header), "; ") + " body: " + string(raw)
			w.WriteHeader(o.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": echo, "type": "invalid_request_error"}})
			return
		}
		review := map[string]any{"verdict": "approve", "summary": "ok", "score": 90, "findings": []any{}}
		if o.protocol == "anthropic" {
			resp := map[string]any{"id": "m1", "type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Review", "input": review}}}
			if !o.noUsage {
				resp["usage"] = map[string]any{"input_tokens": o.in, "output_tokens": o.out}
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		text, _ := json.Marshal(review)
		resp := map[string]any{"id": "c1", "object": "chat.completion", "created": 1, "model": "m1",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(text)}, "finish_reason": "stop"}}}
		if !o.noUsage {
			resp["usage"] = map[string]any{"prompt_tokens": o.in, "completion_tokens": o.out, "total_tokens": o.in + o.out}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func cpHeaderLines(h http.Header) []string {
	var lines []string
	for k, vs := range h {
		lines = append(lines, k+"="+strings.Join(vs, ","))
	}
	slices.Sort(lines)
	return lines
}

func (g *cpGateway) requests() []cpGatewayReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reqs)
}

// cpManifest is a manifest of the given provider and model with extra lines appended.
func cpManifest(provider, model, extra string) string {
	return "name: Rev\nprovider: " + provider + "\nmodel: " + model + "\nsystem_prompt: Review this PR.\nskills: []\n" +
		"strategy: single-pass\nci_fail_on: critical\nversion: 3\n" + extra
}

const cpPrice = "  pricing:\n    promptPerM: 2\n    completionPerM: 8\n"

// cpCustomBlock is the custom_provider block: protocol, auth, two extra headers, mode and price.
func cpCustomBlock(protocol, authMode, authHeader, mode string) string {
	header := "null"
	if authHeader != "" {
		header = `"` + authHeader + `"`
	}
	return "custom_provider:\n  protocol: \"" + protocol + "\"\n  auth_mode: \"" + authMode + "\"\n  auth_header: " + header +
		"\n  extra_headers:\n    - \"X-Tenant\"\n    - \"X-Team\"\n  structured_output: \"" + mode + "\"\n" + cpPrice
}

func cpCustomEnv(baseURL string) map[string]string {
	return map[string]string{
		"DEVDIGEST_LLM_BASE_URL": baseURL,
		"DEVDIGEST_LLM_API_KEY":  cpGatewayKey,
		"DEVDIGEST_LLM_HEADER_1": cpHeader1,
		"DEVDIGEST_LLM_HEADER_2": cpHeader2,
		"OPENROUTER_API_KEY":     cpOpenRouter,
	}
}

// cpRun is the outcome of one run of the binary.
type cpRun struct {
	code           int
	stdout, stderr string
	artifact       string
	hasArtifact    bool
	gh             *githubStub
}

func (r cpRun) posts() int {
	n := 0
	for _, req := range r.gh.requests() {
		if req.Method == http.MethodPost {
			n++
		}
	}
	return n
}

// everything is all the text a run left behind: the job log, the artifact and every request body
// sent to GitHub.
func (r cpRun) everything() string {
	var b strings.Builder
	b.WriteString(r.stdout + "\n" + r.stderr + "\n" + r.artifact + "\n")
	for _, req := range r.gh.requests() {
		body, _ := json.Marshal(req.Body)
		b.Write(body)
	}
	return b.String()
}

func cpExecute(t *testing.T, manifest string, env map[string]string, ep endpoints) cpRun {
	t.Helper()
	w := newWorkspace(t, manifest, env)
	delete(w.env, "OPENROUTER_API_KEY")
	for k, v := range env {
		w.env[k] = v
	}
	gh := gitHubOK(t)
	ep.GitHub = gh.srv.URL
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), w.lookup, &stdout, &stderr, ep)
	art, ok := w.artifact(t)
	return cpRun{code: code, stdout: stdout.String(), stderr: stderr.String(), artifact: art, hasArtifact: ok, gh: gh}
}

func cpArtifactCost(t *testing.T, artifact string) any {
	t.Helper()
	var a map[string]any
	if err := json.Unmarshal([]byte(artifact), &a); err != nil {
		t.Fatalf("artifact is not JSON: %v\n%s", err, artifact)
	}
	return a["cost_usd"]
}

func cpAssertClean(t *testing.T, r cpRun, secrets ...string) {
	t.Helper()
	all := r.everything()
	for _, s := range secrets {
		if strings.Contains(all, s) {
			t.Errorf("the job log, artifact or a posted body contains %q:\n%s", s, all)
		}
	}
}

func cpHeaderOf(r cpGatewayReq, name string) string { return r.Header.Get(name) }

func cpForeignKeys(g *cpGateway, marker string) bool {
	for _, r := range g.requests() {
		if strings.Contains(r.Raw, marker) || strings.Contains(strings.Join(cpHeaderLines(r.Header), ";"), marker) {
			return true
		}
	}
	return false
}

// AC34: an openai-compatible custom provider, bearer auth and two headers.
func TestCustom_OpenAICompatibleGateway(t *testing.T) {
	g := newCPGateway(t, cpGatewayOpts{protocol: "openai", in: 1_000_000, out: 500_000})
	r := cpExecute(t, cpManifest("custom:my-gw", "m1", cpCustomBlock("openai", "bearer", "", "strict")),
		cpCustomEnv(g.srv.URL+"/v1"), defaultEndpoints())

	if r.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	reqs := g.requests()
	if len(reqs) != 1 {
		t.Fatalf("gateway requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.Path != "/v1/chat/completions" {
		t.Errorf("gateway saw %s %s, want POST /v1/chat/completions", got.Method, got.Path)
	}
	if v := cpHeaderOf(got, "Authorization"); v != "Bearer "+cpGatewayKey {
		t.Errorf("Authorization = %q, want the bearer key", v)
	}
	if v := cpHeaderOf(got, "X-Tenant"); v != cpHeader1 {
		t.Errorf("X-Tenant = %q, want %q", v, cpHeader1)
	}
	if v := cpHeaderOf(got, "X-Team"); v != cpHeader2 {
		t.Errorf("X-Team = %q, want %q", v, cpHeader2)
	}
	rf, _ := got.Body["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Errorf("response_format = %v, want json_schema (strict mode)", got.Body["response_format"])
	}
	if cpForeignKeys(g, cpOpenRouter) {
		t.Error("the OpenRouter key reached the custom gateway")
	}
	if n := r.posts(); n != 1 {
		t.Errorf("GitHub posts = %d, want the review", n)
	}
	// Manual price 2 / 8 per 1M over 1,000,000 in and 500,000 out tokens.
	if cost := cpArtifactCost(t, r.artifact); cost != 6.0 {
		t.Errorf("artifact cost_usd = %v, want 6", cost)
	}
	cpAssertClean(t, r, cpGatewayKey, cpHeader1, cpHeader2, cpOpenRouter)
}

// EC19, AC34 second half: an empty key sends no auth header; the gateway's 401 fails the run
// with no artifact and nothing posted, and the echoed headers leak nothing.
func TestCustom_EmptyKeySendsNoAuthAndFailsOn401(t *testing.T) {
	g := newCPGateway(t, cpGatewayOpts{protocol: "openai", status: http.StatusUnauthorized})
	env := cpCustomEnv(g.srv.URL + "/v1")
	env["DEVDIGEST_LLM_API_KEY"] = ""
	r := cpExecute(t, cpManifest("custom:my-gw", "m1", cpCustomBlock("openai", "bearer", "", "strict")), env, defaultEndpoints())

	if r.code != 1 {
		t.Errorf("exit code = %d, want 1\nstderr: %s", r.code, r.stderr)
	}
	reqs := g.requests()
	if len(reqs) == 0 {
		t.Fatal("the gateway was never called")
	}
	for _, q := range reqs {
		if v := cpHeaderOf(q, "Authorization"); v != "" {
			t.Errorf("Authorization = %q, want none for an empty key", v)
		}
		if cpHeaderOf(q, "X-Tenant") != cpHeader1 {
			t.Errorf("X-Tenant = %q, want the header still sent", cpHeaderOf(q, "X-Tenant"))
		}
	}
	if r.hasArtifact {
		t.Errorf("an artifact was written: %s", r.artifact)
	}
	if n := r.posts(); n != 0 {
		t.Errorf("GitHub posts = %d, want 0", n)
	}
	if !strings.Contains(r.stderr, "http 401") {
		t.Errorf("stderr = %q, want the 401 class", r.stderr)
	}
	cpAssertClean(t, r, cpHeader1, cpHeader2, cpOpenRouter)
}

// FR30: the anthropic protocol of a custom provider, header auth, the default anthropic-version.
func TestCustom_AnthropicCompatibleGateway(t *testing.T) {
	g := newCPGateway(t, cpGatewayOpts{protocol: "anthropic", in: 1_000_000, out: 500_000})
	r := cpExecute(t, cpManifest("custom:my-gw", "m1", cpCustomBlock("anthropic", "header", "x-api-key", "strict")),
		cpCustomEnv(g.srv.URL+"/v1"), defaultEndpoints())

	if r.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	reqs := g.requests()
	if len(reqs) != 1 {
		t.Fatalf("gateway requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.Path != "/v1/messages" {
		t.Errorf("gateway saw %s %s, want POST /v1/messages", got.Method, got.Path)
	}
	if v := cpHeaderOf(got, "x-api-key"); v != cpGatewayKey {
		t.Errorf("x-api-key = %q, want the key", v)
	}
	if v := cpHeaderOf(got, "Authorization"); v != "" {
		t.Errorf("Authorization = %q, want none in the header auth mode", v)
	}
	if v := cpHeaderOf(got, "anthropic-version"); v != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", v)
	}
	if cpHeaderOf(got, "X-Tenant") != cpHeader1 || cpHeaderOf(got, "X-Team") != cpHeader2 {
		t.Errorf("extra headers = %q, %q, want both values", cpHeaderOf(got, "X-Tenant"), cpHeaderOf(got, "X-Team"))
	}
	if tc, _ := got.Body["tool_choice"].(map[string]any); tc["type"] != "tool" {
		t.Errorf("tool_choice = %v, want the forced tool", got.Body["tool_choice"])
	}
	if cost := cpArtifactCost(t, r.artifact); cost != 6.0 {
		t.Errorf("artifact cost_usd = %v, want 6", cost)
	}
	cpAssertClean(t, r, cpGatewayKey, cpHeader1, cpHeader2, cpOpenRouter)
}

// AC44: the structured-output mode of the manifest decides the request body.
func TestCustom_StructuredOutputModes(t *testing.T) {
	tests := []struct {
		name, mode string
		want       string // response_format.type, "" = no response_format
	}{
		{"strict", "strict", "json_schema"},
		{"json_object", "json_object", "json_object"},
		{"prompt_only", "prompt_only", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newCPGateway(t, cpGatewayOpts{protocol: "openai", in: 10, out: 5})
			r := cpExecute(t, cpManifest("custom:my-gw", "m1", cpCustomBlock("openai", "bearer", "", tc.mode)),
				cpCustomEnv(g.srv.URL+"/v1"), defaultEndpoints())
			if r.code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", r.code, r.stderr)
			}
			reqs := g.requests()
			if len(reqs) != 1 {
				t.Fatalf("gateway requests = %d, want 1", len(reqs))
			}
			rf, present := reqs[0].Body["response_format"]
			if tc.want == "" {
				if present {
					t.Errorf("response_format = %v, want none in %s mode", rf, tc.mode)
				}
				return
			}
			if typ, _ := rf.(map[string]any)["type"]; typ != tc.want {
				t.Errorf("response_format = %v, want type %q", rf, tc.want)
			}
		})
	}
}

// AC44, FR33: strict mode and a 400 from the gateway: exit 1, the hint, no artifact.
func TestCustom_StrictModeRejectedShowsTheHint(t *testing.T) {
	g := newCPGateway(t, cpGatewayOpts{protocol: "openai", status: http.StatusBadRequest})
	r := cpExecute(t, cpManifest("custom:my-gw", "m1", cpCustomBlock("openai", "bearer", "", "strict")),
		cpCustomEnv(g.srv.URL+"/v1"), defaultEndpoints())

	if r.code != 1 {
		t.Errorf("exit code = %d, want 1", r.code)
	}
	want := "If this gateway does not support strict structured output, set Structured output of custom:my-gw to JSON object or Prompt only."
	if !strings.Contains(r.stderr, want) {
		t.Errorf("stderr = %q, want the hint %q", r.stderr, want)
	}
	if r.hasArtifact || r.posts() != 0 {
		t.Errorf("artifact %v, posts %d; want neither", r.hasArtifact, r.posts())
	}
	cpAssertClean(t, r, cpGatewayKey, cpHeader1, cpHeader2)
}

// AC46: an openai manifest talks to the OpenAI endpoint with the OpenAI key only.
func TestBuiltIn_OpenAIManifest(t *testing.T) {
	g := newCPGateway(t, cpGatewayOpts{protocol: "openai", in: 1_000_000, out: 500_000})
	env := map[string]string{"OPENAI_API_KEY": cpOpenAIKey, "OPENROUTER_API_KEY": cpOpenRouter, "ANTHROPIC_API_KEY": cpAnthropic}
	r := cpExecute(t, cpManifest("openai", "gpt-4.1", "pricing:\n  promptPerM: 2\n  completionPerM: 8\n"),
		env, endpoints{OpenAI: g.srv.URL + "/v1"})

	if r.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	reqs := g.requests()
	if len(reqs) != 1 {
		t.Fatalf("OpenAI requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.Path != "/v1/chat/completions" {
		t.Errorf("stub saw %s %s, want POST /v1/chat/completions", got.Method, got.Path)
	}
	if v := cpHeaderOf(got, "Authorization"); v != "Bearer "+cpOpenAIKey {
		t.Errorf("Authorization = %q, want the OpenAI key as bearer", v)
	}
	rf, _ := got.Body["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true {
		t.Errorf("response_format = %v, want json_schema strict", got.Body["response_format"])
	}
	if cpForeignKeys(g, cpOpenRouter) || cpForeignKeys(g, cpAnthropic) {
		t.Error("another provider's key reached the OpenAI endpoint")
	}
	if n := r.posts(); n != 1 {
		t.Errorf("GitHub posts = %d, want the review", n)
	}
	if cost := cpArtifactCost(t, r.artifact); cost != 6.0 {
		t.Errorf("artifact cost_usd = %v, want 6", cost)
	}
	cpAssertClean(t, r, cpOpenAIKey, cpOpenRouter, cpAnthropic)
}

// AC46: an anthropic manifest talks to the Anthropic endpoint with x-api-key and the forced tool.
func TestBuiltIn_AnthropicManifest(t *testing.T) {
	g := newCPGateway(t, cpGatewayOpts{protocol: "anthropic", in: 1_000_000, out: 500_000})
	env := map[string]string{"ANTHROPIC_API_KEY": cpAnthropic, "OPENROUTER_API_KEY": cpOpenRouter, "OPENAI_API_KEY": cpOpenAIKey}
	r := cpExecute(t, cpManifest("anthropic", "claude-sonnet", "pricing:\n  promptPerM: 3\n  completionPerM: 15\n"),
		env, endpoints{Anthropic: g.srv.URL + "/v1"})

	if r.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	reqs := g.requests()
	if len(reqs) != 1 {
		t.Fatalf("Anthropic requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.Path != "/v1/messages" {
		t.Errorf("stub saw %s %s, want POST /v1/messages", got.Method, got.Path)
	}
	if cpHeaderOf(got, "x-api-key") != cpAnthropic || cpHeaderOf(got, "anthropic-version") != "2023-06-01" {
		t.Errorf("x-api-key %q, anthropic-version %q; want the key and 2023-06-01", cpHeaderOf(got, "x-api-key"), cpHeaderOf(got, "anthropic-version"))
	}
	if tc, _ := got.Body["tool_choice"].(map[string]any); tc["type"] != "tool" {
		t.Errorf("tool_choice = %v, want the forced tool", got.Body["tool_choice"])
	}
	if cpForeignKeys(g, cpOpenRouter) || cpForeignKeys(g, cpOpenAIKey) {
		t.Error("another provider's key reached the Anthropic endpoint")
	}
	// 3 per 1M in, 15 per 1M out: 3 + 7.5.
	if cost := cpArtifactCost(t, r.artifact); cost != 10.5 {
		t.Errorf("artifact cost_usd = %v, want 10.5", cost)
	}
	cpAssertClean(t, r, cpAnthropic, cpOpenRouter, cpOpenAIKey)
}

// EC29: an anthropic manifest with an empty ANTHROPIC_API_KEY and an OpenRouter key set fails at
// the first call; the OpenRouter key is neither read nor sent, anywhere.
func TestBuiltIn_EmptyKeyDoesNotFallBackToOpenRouter(t *testing.T) {
	for _, tc := range []struct{ provider, model, keyVar string }{
		{"anthropic", "claude-sonnet", "ANTHROPIC_API_KEY"},
		{"openai", "gpt-4.1", "OPENAI_API_KEY"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			g := newCPGateway(t, cpGatewayOpts{protocol: tc.provider})
			or := newCPGateway(t, cpGatewayOpts{protocol: "openai"})
			env := map[string]string{tc.keyVar: "", "OPENROUTER_API_KEY": cpOpenRouter}
			r := cpExecute(t, cpManifest(tc.provider, tc.model, ""), env,
				endpoints{OpenRouter: or.srv.URL + "/v1", OpenAI: g.srv.URL + "/v1", Anthropic: g.srv.URL + "/v1"})

			if r.code != 1 {
				t.Errorf("exit code = %d, want 1\nstderr: %s", r.code, r.stderr)
			}
			if !strings.Contains(r.stderr, "not_configured") {
				t.Errorf("stderr = %q, want the not_configured class", r.stderr)
			}
			if r.hasArtifact || r.posts() != 0 {
				t.Errorf("artifact %v, posts %d; want neither", r.hasArtifact, r.posts())
			}
			if n := len(g.requests()) + len(or.requests()); n != 0 {
				t.Errorf("model requests = %d, want 0: an empty key is not sent and OpenRouter is not asked", n)
			}
			cpAssertClean(t, r, cpOpenRouter)
		})
	}
}

// AC49: no manifest price, tokens reported, no usage.cost: the cost is null.
func TestBuiltIn_UnpricedManifestHasNullCost(t *testing.T) {
	g := newCPGateway(t, cpGatewayOpts{protocol: "openai", in: 1_000_000, out: 500_000})
	r := cpExecute(t, cpManifest("openai", "gpt-4.1", ""), map[string]string{"OPENAI_API_KEY": cpOpenAIKey}, endpoints{OpenAI: g.srv.URL + "/v1"})
	if r.code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", r.code, r.stderr)
	}
	if cost := cpArtifactCost(t, r.artifact); cost != nil {
		t.Errorf("artifact cost_usd = %v, want null", cost)
	}
}

// SCR7 (spec D30): a gateway that reports no usage gives estimated tokens, and a manifest price is
// never applied to those: the cost is null.
func TestBuiltIn_ManifestPriceNeverPricesEstimatedTokens(t *testing.T) {
	for _, tc := range []struct {
		provider, model, keyVar, key string
		ep                           func(url string) endpoints
	}{
		{"openai", "gpt-4.1", "OPENAI_API_KEY", cpOpenAIKey, func(u string) endpoints { return endpoints{OpenAI: u + "/v1"} }},
		{"anthropic", "claude-sonnet", "ANTHROPIC_API_KEY", cpAnthropic, func(u string) endpoints { return endpoints{Anthropic: u + "/v1"} }},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			g := newCPGateway(t, cpGatewayOpts{protocol: tc.provider, noUsage: true})
			r := cpExecute(t, cpManifest(tc.provider, tc.model, "pricing:\n  promptPerM: 2\n  completionPerM: 8\n"),
				map[string]string{tc.keyVar: tc.key}, tc.ep(g.srv.URL))
			if r.code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", r.code, r.stderr)
			}
			if !r.hasArtifact {
				t.Fatal("no artifact")
			}
			if cost := cpArtifactCost(t, r.artifact); cost != nil {
				t.Errorf("artifact cost_usd = %v, want null: the tokens were estimated", cost)
			}
		})
	}
}

// FR36: the built-in limits of the runner are the OpenRouter limits, not the studio's.
func TestProviders_BuiltInRunnerLimits(t *testing.T) {
	for _, p := range []string{"openai", "anthropic"} {
		l, err := providers{ep: defaultEndpoints(), stderr: io.Discard}.For(selection(p))
		if err != nil || l == nil {
			t.Fatalf("For(%s) = %v, %v; want a port", p, l, err)
		}
	}
	if builtInPolicy.RequestTimeout.Seconds() != 180 || builtInPolicy.Retries != 2 {
		t.Errorf("builtInPolicy = %+v, want 180 s and 2 retries (FR36)", builtInPolicy)
	}
}

// selection is a selection of the given provider without a custom endpoint.
func selection(provider string) runnerapp.ProviderSelection {
	return runnerapp.ProviderSelection{Provider: provider, Model: "m1", Key: "sk-selection-0001"}
}

func TestProviders_UnknownProviderFails(t *testing.T) {
	for _, sel := range []string{"openrouter", "custom:my-gw", "gemini"} { // custom without its endpoint
		if l, err := (providers{ep: defaultEndpoints(), stderr: io.Discard}).For(selection(sel)); err == nil {
			t.Errorf("For(%q) = %v, nil; want an error", sel, l)
		}
	}
}

// A custom gateway behind a private CA: DEVDIGEST_EXTRA_CA_FILE makes it trusted; without it the
// run fails; a bad file fails before any request.
func TestCustom_ExtraCAFile(t *testing.T) {
	g := &cpGateway{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(g.handler(cpGatewayOpts{protocol: "openai", in: 10, out: 5})))
	g.srv = srv
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := cpManifest("custom:my-gw", "m1", cpCustomBlock("openai", "bearer", "", "strict"))

	t.Run("trusted with the extra CA", func(t *testing.T) {
		env := cpCustomEnv(srv.URL + "/v1")
		env["DEVDIGEST_EXTRA_CA_FILE"] = caFile
		r := cpExecute(t, manifest, env, defaultEndpoints())
		if r.code != 0 {
			t.Errorf("exit code = %d, want 0\nstderr: %s", r.code, r.stderr)
		}
		cpAssertClean(t, r, cpGatewayKey, cpHeader1)
	})
	t.Run("untrusted without it", func(t *testing.T) {
		r := cpExecute(t, manifest, cpCustomEnv(srv.URL+"/v1"), defaultEndpoints())
		if r.code != 1 || r.hasArtifact {
			t.Errorf("exit code %d, artifact %v; want 1 and none (unknown authority)", r.code, r.hasArtifact)
		}
		cpAssertClean(t, r, cpGatewayKey, cpHeader1)
	})
	t.Run("missing file", func(t *testing.T) {
		before := len(g.requests())
		env := cpCustomEnv(srv.URL + "/v1")
		env["DEVDIGEST_EXTRA_CA_FILE"] = filepath.Join(t.TempDir(), "nope.pem")
		r := cpExecute(t, manifest, env, defaultEndpoints())
		if r.code != 1 || !strings.Contains(r.stderr, "DEVDIGEST_EXTRA_CA_FILE") || r.hasArtifact {
			t.Errorf("exit code %d, stderr %q, artifact %v; want 1, the variable named, no artifact", r.code, r.stderr, r.hasArtifact)
		}
		if now := len(g.requests()); now != before {
			t.Errorf("gateway requests grew from %d to %d, want none", before, now)
		}
		cpAssertClean(t, r, cpGatewayKey)
	})
}
