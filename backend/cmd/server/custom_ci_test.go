package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"

	"dev-digest-go/backend/internal/llm"
)

// Custom model providers, step S29: export to CI and ingest for openrouter, openai, anthropic and
// custom:<slug> agents (spec 2026-10-24-custom-model-providers.md, AC30-AC33, AC45, AC47-AC50), driven
// through the real routes with the GitHub double of phase6_ci_test.go. The runner side of the same
// criteria (AC34, AC44, AC46 and the runner half of AC49) lives in the runner's own tests
// (cmd/devdigest-runner, internal/ci/runner/app). Helpers are prefixed cx.

const (
	cxBaseURL    = "https://gw.example.test/v1"
	cxKey        = "sk-CX-GATEWAY-KEY-1"
	cxTenantVal  = "TENANT-SECRET-9"
	cxProjectVal = "PROJECT-SECRET-3"
	cxModel      = "gw-model-1"
	cxOpenAI     = "gpt-4.1"           // priced 2 / 8 USD per 1M tokens by the price book
	cxAnthropic  = "claude-sonnet-4-6" // priced 3 / 15
)

// ---- fixtures ----

// cxProviderBody is the POST body of the AC30 provider: openai protocol, bearer key, extra headers
// X-Tenant and X-Project, one manual model with a price; over replaces or (nil) removes keys.
func cxProviderBody(slug string, over map[string]any) map[string]any {
	body := map[string]any{
		"id": "custom:" + slug, "name": "CX " + slug, "protocol": "openai", "base_url": cxBaseURL, "api_key": cxKey,
		"extra_headers": []any{
			map[string]any{"name": "X-Tenant", "value": cxTenantVal},
			map[string]any{"name": "X-Project", "value": cxProjectVal},
		},
		"models": []any{map[string]any{"id": cxModel, "pricing": map[string]any{"promptPerM": 1.5, "completionPerM": 6}}},
	}
	for k, v := range over {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	return body
}

func cxProvider(t *testing.T, s *stack, slug string, over map[string]any) {
	t.Helper()
	if resp := s.post("/settings/custom-providers", cxProviderBody(slug, over)); resp.Status != http.StatusCreated {
		t.Fatalf("POST /settings/custom-providers %q = %d %s, want 201", slug, resp.Status, resp.Body)
	}
}

func cxAgent(t *testing.T, s *stack, name, provider, model string) string {
	t.Helper()
	resp := s.post("/agents", map[string]any{"name": name, "provider": provider, "model": model, "system_prompt": ciPrompt})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /agents %q on %s = %d %s, want 201", name, provider, resp.Status, resp.Body)
	}
	return idOf(t, resp)
}

// cxExported is an export answer with the lists of spec 7.
type cxExported struct {
	ciExportResult
	RequiredSecrets   []string `json:"required_secrets"`
	RequiredVariables []string `json:"required_variables"`
}

func cxExport(t *testing.T, s *stack, agent string, body any, want int) (cxExported, response) {
	t.Helper()
	resp := s.post("/agents/"+agent+"/export-ci", body)
	if resp.Status != want {
		t.Fatalf("POST /agents/%s/export-ci = %d %s, want %d", agent, resp.Status, resp.Body, want)
	}
	var out cxExported
	if want < 300 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			t.Fatalf("export body: %v: %s", err, resp.Body)
		}
	}
	return out, resp
}

// cxFileText returns the contents of the file whose path ends in suffix.
func cxFileText(t *testing.T, files []ciFile, suffix string) string {
	t.Helper()
	for _, f := range files {
		if strings.HasSuffix(f.Path, suffix) {
			return f.Contents
		}
	}
	t.Fatalf("export has no file ending in %q: %v", suffix, files)
	return ""
}

// cxReplaceOnce replaces the one occurrence of old in text.
func cxReplaceOnce(t *testing.T, text, old, new string) string {
	t.Helper()
	if n := strings.Count(text, old); n != 1 {
		t.Fatalf("%q occurs %d times in the expected text, want once:\n%s", old, n, text)
	}
	return strings.Replace(text, old, new, 1)
}

const (
	cxORKeyLine = "          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}\n"
	cxRunsOn    = "    runs-on: ubuntu-24.04\n"
	cxSelfHost  = "[self-hosted, linux, x64]"
)

// cxWorkflow is the Phase 6 workflow of slug (all three triggers, github_review) with spec FR25/FR26/
// FR35 applied: runs-on replaced when runsOn is not empty and the OpenRouter key line replaced by
// the given env lines (written at the env indentation). Built from the independent Phase 6 text.
func cxWorkflow(t *testing.T, slug, runsOn string, keyLines ...string) string {
	t.Helper()
	w := ciWantWorkflow(slug, []string{"opened", "synchronize", "reopened"}, "github_review")
	if runsOn != "" {
		w = cxReplaceOnce(t, w, cxRunsOn, "    runs-on: "+runsOn+"\n")
	}
	var lines strings.Builder
	for _, l := range keyLines {
		lines.WriteString("          " + l + "\n")
	}
	return cxReplaceOnce(t, w, cxORKeyLine, lines.String())
}

// cxCustomEnv are the env lines of FR26 for envName with n extra headers.
func cxCustomEnv(envName string, headers int) []string {
	lines := []string{
		"DEVDIGEST_LLM_BASE_URL: ${{ vars.DEVDIGEST_" + envName + "_BASE_URL }}",
		"DEVDIGEST_LLM_API_KEY: ${{ secrets.DEVDIGEST_" + envName + "_API_KEY }}",
	}
	for n := 1; n <= headers; n++ {
		lines = append(lines, fmt.Sprintf("DEVDIGEST_LLM_HEADER_%d: ${{ secrets.DEVDIGEST_%s_HEADER_%d }}", n, envName, n))
	}
	return lines
}

// cxManifest parses a manifest into its key order and values.
func cxManifest(t *testing.T, text string) ([]string, map[string]any) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(text), &node); err != nil {
		t.Fatalf("manifest is not YAML: %v\n%s", err, text)
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("manifest is not a mapping:\n%s", text)
	}
	var keys []string
	for i := 0; i < len(node.Content[0].Content); i += 2 {
		keys = append(keys, node.Content[0].Content[i].Value)
	}
	var decoded any
	if err := node.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	// One number type: through JSON, every number is a float64.
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return keys, m
}

var cxBaseKeys = []string{"name", "provider", "model", "system_prompt", "skills", "strategy", "ci_fail_on", "version"}

// cxCheckManifest compares a manifest with the agent as the API shows it; extraKeys follow version.
func cxCheckManifest(t *testing.T, s *stack, agent, text, provider, model string, extra map[string]any, extraKeys ...string) {
	t.Helper()
	keys, got := cxManifest(t, text)
	if diff := cmp.Diff(append(append([]string{}, cxBaseKeys...), extraKeys...), keys); diff != "" {
		t.Errorf("manifest key order mismatch (-want +got):\n%s", diff)
	}
	api := s.get("/agents/" + agent).Obj(t)
	want := map[string]any{
		"name": api["name"], "provider": provider, "model": model, "system_prompt": ciPrompt, "skills": []any{},
		"strategy": api["strategy"], "ci_fail_on": api["ci_fail_on"], "version": api["version"],
	}
	for k, v := range extra {
		want[k] = v
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("manifest values mismatch (-want +got):\n%s", diff)
	}
}

func cxPricing(in, out float64) map[string]any {
	return map[string]any{"promptPerM": in, "completionPerM": out}
}

// cxNoSecrets fails when text holds any of the values no export may carry (NFR8).
func cxNoSecrets(t *testing.T, what, text string, values ...string) {
	t.Helper()
	for _, v := range values {
		if strings.Contains(text, v) {
			t.Errorf("%s holds %q, want the base URL, key and header values kept out", what, v)
		}
	}
}

// cxWithSecretRef adds an env line that reads secrets.<name> to a generated workflow.
func cxWithSecretRef(t *testing.T, workflow, name string) string {
	t.Helper()
	const anchor = "          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n"
	return cxReplaceOnce(t, workflow, anchor, anchor+"          EXTRA_CX: ${{ secrets."+name+" }}\n")
}

// ---- AC30: the custom export ----

// AC30, AC31 (custom half), AC44 (manifest block), EC25, EC26, NFR8: the workflow, manifest and lists
// of a custom-provider export.
func TestCiExport_CustomProviderFiles(t *testing.T) {
	s, _ := ciStack(t, ciSpec())
	cxProvider(t, s, "my-gw", nil)
	agent := cxAgent(t, s, "GW Reviewer", "custom:my-gw", cxModel)
	wantSecrets := []string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2"}
	wantVars := []string{"DEVDIGEST_MY_GW_BASE_URL"}
	wantCustom := func(over map[string]any) map[string]any {
		block := map[string]any{
			"protocol": "openai", "auth_mode": "bearer", "auth_header": nil, "extra_headers": []any{"X-Tenant", "X-Project"},
			"structured_output": "strict", "pricing": cxPricing(1.5, 6),
		}
		for k, v := range over {
			block[k] = v
		}
		return map[string]any{"custom_provider": block}
	}

	t.Run("self-hosted runner", func(t *testing.T) {
		out, resp := cxExport(t, s, agent, ciExportBody(ciRepoFull, "files", map[string]any{"runner": "self_hosted"}), http.StatusOK)
		if diff := cmp.Diff([]string{"installation", "files", "pr_url", "required_secrets", "required_variables", "gitlab_ci"}, ciKeyOrder(t, resp.Body)); diff != "" {
			t.Errorf("CiExport key order mismatch (-want +got):\n%s", diff)
		}
		want := cxWorkflow(t, "gw-reviewer", cxSelfHost, cxCustomEnv("MY_GW", 2)...)
		ciCheckFiles(t, out.Files, "gw-reviewer", nil, nil, want)
		cxCheckManifest(t, s, agent, out.Files[0].Contents, "custom:my-gw", cxModel, wantCustom(nil), "custom_provider")
		if diff := cmp.Diff(wantSecrets, out.RequiredSecrets); diff != "" {
			t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(wantVars, out.RequiredVariables); diff != "" {
			t.Errorf("required_variables mismatch (-want +got):\n%s", diff)
		}
		if out.Installation != nil || out.PRURL != nil {
			t.Errorf("files export installation = %+v, pr_url = %v, want both null", out.Installation, out.PRURL)
		}
		if strings.Contains(cxFileText(t, out.Files, ".yml"), "OPENROUTER_API_KEY") {
			t.Error("custom workflow mentions OPENROUTER_API_KEY, want it gone")
		}
		for _, f := range out.Files {
			cxNoSecrets(t, f.Path, f.Contents, cxBaseURL, "gw.example.test", cxKey, cxTenantVal, cxProjectVal)
		}
		ciGoldens(t, s).check("ci_export_files_custom", resp, http.StatusOK)

		// The generated workflow passes the security check: sent back as the edited text it is accepted unchanged.
		again, _ := cxExport(t, s, agent, ciExportBody(ciRepoFull, "files", map[string]any{"runner": "self_hosted", "workflow": want}), http.StatusOK)
		if got := cxFileText(t, again.Files, ".yml"); got != want {
			t.Errorf("workflow after the edited-text round trip = %q, want it unchanged", got)
		}
	})

	t.Run("default runner is github-hosted", func(t *testing.T) {
		out, _ := cxExport(t, s, agent, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
		if diff := cmp.Diff(cxWorkflow(t, "gw-reviewer", "", cxCustomEnv("MY_GW", 2)...), cxFileText(t, out.Files, ".yml")); diff != "" {
			t.Errorf("default-runner workflow mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("unknown runner", func(t *testing.T) {
		resp := s.post("/agents/"+agent+"/export-ci", ciExportBody(ciRepoFull, "files", map[string]any{"runner": "windows"}))
		if e := ciErr(t, resp); resp.Status != http.StatusUnprocessableEntity || e.Code != "validation_error" {
			t.Errorf("runner windows = %d %s, want 422 validation_error", resp.Status, resp.Body)
		}
	})

	t.Run("header auth and json_object mode", func(t *testing.T) { // AC44: structured_output before pricing
		cxProvider(t, s, "hdr-gw", map[string]any{
			"auth_mode": "header", "auth_header": "api-key", "structured_output": "json_object", "extra_headers": []any{},
		})
		a := cxAgent(t, s, "Header Reviewer", "custom:hdr-gw", cxModel)
		out, _ := cxExport(t, s, a, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
		cxCheckManifest(t, s, a, out.Files[0].Contents, "custom:hdr-gw", cxModel, wantCustom(map[string]any{
			"auth_mode": "header", "auth_header": "api-key", "extra_headers": []any{}, "structured_output": "json_object",
		}), "custom_provider")
		raw := out.Files[0].Contents
		if i, j := strings.Index(raw, "structured_output:"), strings.Index(raw, "pricing:"); i < 0 || j < i {
			t.Errorf("manifest holds structured_output at %d and pricing at %d, want structured_output first:\n%s", i, j, raw)
		}
		if diff := cmp.Diff([]string{"DEVDIGEST_HDR_GW_API_KEY"}, out.RequiredSecrets); diff != "" {
			t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
		}
		if got, want := cxFileText(t, out.Files, ".yml"), cxWorkflow(t, "header-reviewer", "", cxCustomEnv("HDR_GW", 0)...); got != want {
			t.Errorf("workflow mismatch (-want +got):\n%s", cmp.Diff(want, got))
		}
	})

	t.Run("five headers, a dashed slug and no price", func(t *testing.T) { // EC25, EC26
		var headers []any
		for _, n := range []string{"X-A", "X-B", "X-C", "X-D", "X-E"} {
			headers = append(headers, map[string]any{"name": n, "value": "value-of-" + n})
		}
		cxProvider(t, s, "x-y-z", map[string]any{"extra_headers": headers, "models": []any{map[string]any{"id": cxModel}}})
		a := cxAgent(t, s, "Five Reviewer", "custom:x-y-z", cxModel)
		out, _ := cxExport(t, s, a, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
		if got, want := cxFileText(t, out.Files, ".yml"), cxWorkflow(t, "five-reviewer", "", cxCustomEnv("X_Y_Z", 5)...); got != want {
			t.Errorf("workflow mismatch (-want +got):\n%s", cmp.Diff(want, got))
		}
		cxCheckManifest(t, s, a, out.Files[0].Contents, "custom:x-y-z", cxModel, wantCustom(map[string]any{
			"extra_headers": []any{"X-A", "X-B", "X-C", "X-D", "X-E"}, "pricing": nil,
		}), "custom_provider")
		wantNames := []string{"DEVDIGEST_X_Y_Z_API_KEY"}
		for n := 1; n <= 5; n++ {
			wantNames = append(wantNames, fmt.Sprintf("DEVDIGEST_X_Y_Z_HEADER_%d", n))
		}
		if diff := cmp.Diff(wantNames, out.RequiredSecrets); diff != "" {
			t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{"DEVDIGEST_X_Y_Z_BASE_URL"}, out.RequiredVariables); diff != "" {
			t.Errorf("required_variables mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("keyless gateway", func(t *testing.T) { // FR26: the key line stays, FR29: the secret is not required
		cxProvider(t, s, "no-key", map[string]any{"api_key": nil, "models": []any{map[string]any{"id": cxModel}}})
		a := cxAgent(t, s, "Keyless Reviewer", "custom:no-key", cxModel)
		out, _ := cxExport(t, s, a, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
		if got, want := cxFileText(t, out.Files, ".yml"), cxWorkflow(t, "keyless-reviewer", "", cxCustomEnv("NO_KEY", 2)...); got != want {
			t.Errorf("workflow mismatch (-want +got):\n%s", cmp.Diff(want, got))
		}
		if diff := cmp.Diff([]string{"DEVDIGEST_NO_KEY_HEADER_1", "DEVDIGEST_NO_KEY_HEADER_2"}, out.RequiredSecrets); diff != "" {
			t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
		}
	})

	if got := s.gh.requests(); len(got) != 0 {
		t.Errorf("GitHub requests during the files exports = %v, want none", ciSeq(got))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
		t.Errorf("ci_installations rows = %d, want 0 (the files action writes nothing)", n)
	}
}

// ---- AC31: the runner choice, the openrouter output ----

// AC31: an openrouter export is the Phase 6 output with the new lists; the self-hosted runner changes
// only the runs-on line.
func TestCiExport_RunnerChoiceChangesOnlyRunsOn(t *testing.T) {
	s, _ := ciStack(t, ciSpec())
	agent := ciCreateAgent(t, s, "CI Reviewer", "openrouter")
	body := func(extra map[string]any) map[string]any { return ciExportBody(ciRepoFull, "files", extra) }

	def, resp := cxExport(t, s, agent, body(nil), http.StatusOK)
	phase6 := ciWantWorkflow("ci-reviewer", []string{"opened", "synchronize", "reopened"}, "github_review")
	ciCheckFiles(t, def.Files, "ci-reviewer", nil, nil, phase6)
	ciCheckManifest(t, s, agent, def.Files[0].Contents, nil)
	if diff := cmp.Diff([]string{"OPENROUTER_API_KEY"}, def.RequiredSecrets); diff != "" {
		t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(string(resp.Body), `"required_variables":[]`) || def.RequiredVariables == nil {
		t.Errorf("required_variables = %v in %s, want an empty list, not null", def.RequiredVariables, resp.Body)
	}

	for _, runner := range []string{"github_hosted", "self_hosted"} {
		out, _ := cxExport(t, s, agent, body(map[string]any{"runner": runner}), http.StatusOK)
		want := phase6
		if runner == "self_hosted" {
			want = cxReplaceOnce(t, phase6, cxRunsOn, "    runs-on: "+cxSelfHost+"\n")
		}
		if got := cxFileText(t, out.Files, ".yml"); got != want {
			t.Errorf("runner %s workflow mismatch (-want +got):\n%s", runner, cmp.Diff(want, got))
		}
		// Every other file is the same as the default export's.
		for i, f := range out.Files {
			if !strings.HasSuffix(f.Path, ".yml") && f != def.Files[i] {
				t.Errorf("runner %s file %s differs from the default export's", runner, f.Path)
			}
		}
	}
}

// ---- open_pr: PR body, stored provider ----

// AC31 (PR body), AC45 (PR body step 1), FR29, FR37: the PR body of each provider lists the required
// secrets and variables, and the installation stores the agent's provider.
func TestCiExport_OpenPRBodyAndStoredProvider(t *testing.T) {
	const prefix = "1. Add the repository Actions "
	const suffix = " (Settings > Secrets and variables > Actions)."
	tests := []struct {
		name, provider, model, step1 string
		prep                         func(t *testing.T, s *stack)
		runner                       string
		golden                       string
		wantSecrets, wantVars        []string
	}{
		{name: "openrouter", provider: "openrouter", model: ciModel, step1: "secret `OPENROUTER_API_KEY`",
			wantSecrets: []string{"OPENROUTER_API_KEY"}},
		{name: "openai", provider: "openai", model: cxOpenAI, step1: "secret `OPENAI_API_KEY`",
			wantSecrets: []string{"OPENAI_API_KEY"}},
		{name: "anthropic", provider: "anthropic", model: cxAnthropic, step1: "secret `ANTHROPIC_API_KEY`",
			wantSecrets: []string{"ANTHROPIC_API_KEY"}},
		{name: "custom", provider: "custom:my-gw", model: cxModel, runner: "self_hosted", golden: "ci_export_open_pr_custom",
			step1:       "secrets `DEVDIGEST_MY_GW_API_KEY`, `DEVDIGEST_MY_GW_HEADER_1`, `DEVDIGEST_MY_GW_HEADER_2` and the variable `DEVDIGEST_MY_GW_BASE_URL`",
			prep:        func(t *testing.T, s *stack) { cxProvider(t, s, "my-gw", nil) },
			wantSecrets: []string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2"},
			wantVars:    []string{"DEVDIGEST_MY_GW_BASE_URL"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := ciStack(t, ciSpec())
			if tc.prep != nil {
				tc.prep(t, s)
			}
			agent := cxAgent(t, s, "CI Reviewer", tc.provider, tc.model)
			extra := map[string]any{}
			if tc.runner != "" {
				extra["runner"] = tc.runner
			}
			out, resp := cxExport(t, s, agent, ciExportBody(ciRepoFull, "open_pr", extra), http.StatusCreated)
			if tc.golden != "" {
				ciGoldens(t, s).check(tc.golden, resp, http.StatusCreated)
			}

			posts := ciWith(s.gh.requests(), "POST", "/pulls")
			if len(posts) != 1 {
				t.Fatalf("POST /pulls requests = %d, want 1", len(posts))
			}
			wantBody := ciWantPRBody("ci-reviewer", ciWantPaths("ci-reviewer"))
			wantBody = cxReplaceOnce(t, wantBody, prefix+"secret `OPENROUTER_API_KEY`"+suffix, prefix+tc.step1+suffix)
			if diff := cmp.Diff(wantBody, ciBodyOf(t, posts[0])["body"]); diff != "" {
				t.Errorf("PR body mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantSecrets, out.RequiredSecrets); diff != "" {
				t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
			}
			wantVars := tc.wantVars
			if wantVars == nil {
				wantVars = []string{}
			}
			if diff := cmp.Diff(wantVars, out.RequiredVariables); diff != "" {
				t.Errorf("required_variables mismatch (-want +got):\n%s", diff)
			}
			cxNoSecrets(t, "PR body", string(posts[0].Body), cxBaseURL, cxKey, cxTenantVal, cxProjectVal)

			// FR37: the installation stores the export-time provider.
			if got := scalar[string](t, s.db.DB, `SELECT provider FROM ci_installations WHERE agent_id = ?`, agent); got != tc.provider {
				t.Errorf("ci_installations.provider after open_pr = %q, want %q", got, tc.provider)
			}
		})
	}
}

// ---- AC45, EC28: openai and anthropic ----

// AC45, EC28, FR35: openai and anthropic agents are exportable; the output is the Phase 6 text with the
// key line replaced and a pricing key from the price book; openrouter keeps the Phase 6 bytes.
func TestCiExport_BuiltInProvidersAreExportable(t *testing.T) {
	s, _ := ciStack(t, ciSpec())
	openai := cxAgent(t, s, "OpenAI Reviewer", "openai", cxOpenAI)
	anthropic := cxAgent(t, s, "Anthropic Reviewer", "anthropic", cxAnthropic)
	router := cxAgent(t, s, "Router Reviewer", "openrouter", ciModel)

	tests := []struct {
		name, agent, slug, provider, model, secret string
		pricing                                    map[string]any
		golden                                     string
	}{
		{"openai", openai, "openai-reviewer", "openai", cxOpenAI, "OPENAI_API_KEY", cxPricing(2, 8), "ci_export_files_openai"},
		{"anthropic", anthropic, "anthropic-reviewer", "anthropic", cxAnthropic, "ANTHROPIC_API_KEY", cxPricing(3, 15), "ci_export_files_anthropic"},
	}
	workflows := map[string]string{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, resp := cxExport(t, s, tc.agent, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
			want := cxWorkflow(t, tc.slug, "", tc.secret+": ${{ secrets."+tc.secret+" }}")
			workflows[tc.name] = want
			ciCheckFiles(t, out.Files, tc.slug, nil, nil, want)
			cxCheckManifest(t, s, tc.agent, out.Files[0].Contents, tc.provider, tc.model, map[string]any{"pricing": tc.pricing}, "pricing")
			if _, m := cxManifest(t, out.Files[0].Contents); m["custom_provider"] != nil {
				t.Errorf("manifest has custom_provider %v, want none for a built-in", m["custom_provider"])
			}
			if diff := cmp.Diff([]string{tc.secret}, out.RequiredSecrets); diff != "" {
				t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
			}
			if !strings.Contains(string(resp.Body), `"required_variables":[]`) {
				t.Errorf("response %s lacks an empty required_variables", resp.Body)
			}
			if strings.Contains(cxFileText(t, out.Files, ".yml"), "OPENROUTER_API_KEY") {
				t.Errorf("%s workflow mentions OPENROUTER_API_KEY, want it gone", tc.name)
			}
			ciGoldens(t, s).check(tc.golden, resp, http.StatusOK)
			// The generated workflow passes the security check: sent back as the edited text it is accepted.
			cxExport(t, s, tc.agent, ciExportBody(ciRepoFull, "files", map[string]any{"workflow": want}), http.StatusOK)
		})
	}

	t.Run("openrouter stays byte-identical, even though the price book prices its model", func(t *testing.T) {
		out, _ := cxExport(t, s, router, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
		ciCheckFiles(t, out.Files, "router-reviewer", nil, nil, ciWantWorkflow("router-reviewer", []string{"opened", "synchronize", "reopened"}, "github_review"))
		keys, _ := cxManifest(t, out.Files[0].Contents)
		if diff := cmp.Diff(cxBaseKeys, keys); diff != "" {
			t.Errorf("openrouter manifest keys mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("edited workflows may only read their own secret", func(t *testing.T) { // EC28
		cases := []struct{ name, agent, secret string }{
			{"openai reads OPENROUTER_API_KEY", openai, "OPENROUTER_API_KEY"},
			{"openai reads ANTHROPIC_API_KEY", openai, "ANTHROPIC_API_KEY"},
			{"anthropic reads OPENAI_API_KEY", anthropic, "OPENAI_API_KEY"},
			{"anthropic reads OPENROUTER_API_KEY", anthropic, "OPENROUTER_API_KEY"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				base := workflows["openai"]
				if tc.agent == anthropic {
					base = workflows["anthropic"]
				}
				resp := s.post("/agents/"+tc.agent+"/export-ci", ciExportBody(ciRepoFull, "files", map[string]any{"workflow": cxWithSecretRef(t, base, tc.secret)}))
				e := ciErr(t, resp)
				if resp.Status != http.StatusUnprocessableEntity || e.Code != "workflow_insecure" || cmp.Diff(map[string]any{"rule": "secrets"}, e.Details) != "" {
					t.Errorf("edited workflow reading %s = %d %s, want 422 workflow_insecure rule secrets", tc.secret, resp.Status, resp.Body)
				}
			})
		}
	})
}

// ---- AC32, AC33: refusals ----

// AC32, EC17, FR24: an undefined custom provider is refused at the provider step; openai and anthropic
// pass it and are refused for their triggers; no export answers unsupported_provider.
func TestCiExport_ProviderStepAndNoUnsupportedProvider(t *testing.T) {
	s, _ := ciStack(t, ciSpec())
	cxProvider(t, s, "my-gw", nil)
	ghost := cxAgent(t, s, "Ghost Reviewer", "openrouter", ciModel)
	// The API refuses an undefined custom provider on an agent: the row is set directly, as if the
	// provider was removed behind the agent's back.
	if err := s.db.DB.Exec(`UPDATE agents SET provider = 'custom:ghost' WHERE id = ?`, ghost).Error; err != nil {
		t.Fatal(err)
	}
	agents := map[string]string{
		"openrouter": cxAgent(t, s, "Router Reviewer", "openrouter", ciModel),
		"openai":     cxAgent(t, s, "OpenAI Reviewer", "openai", cxOpenAI),
		"anthropic":  cxAgent(t, s, "Anthropic Reviewer", "anthropic", cxAnthropic),
		"custom":     cxAgent(t, s, "GW Reviewer", "custom:my-gw", cxModel),
	}

	resp := s.post("/agents/"+ghost+"/export-ci", ciExportBody(ciRepoFull, "files", nil))
	if e := ciErr(t, resp); resp.Status != http.StatusUnprocessableEntity || e.Code != "provider_not_configured" || e.Message != "Custom provider custom:ghost is not configured" {
		t.Errorf("export of custom:ghost = %d %s, want 422 provider_not_configured \"Custom provider custom:ghost is not configured\"", resp.Status, resp.Body)
	}
	if resp := s.post("/agents/"+ghost+"/export-ci", ciExportBody(ciRepoFull, "open_pr", nil)); resp.Status != http.StatusUnprocessableEntity || ciErr(t, resp).Code != "provider_not_configured" {
		t.Errorf("open_pr of custom:ghost = %d %s, want 422 provider_not_configured", resp.Status, resp.Body)
	}

	for name, id := range agents {
		resp := s.post("/agents/"+id+"/export-ci", ciExportBody(ciRepoFull, "files", map[string]any{"triggers": []string{}}))
		if e := ciErr(t, resp); resp.Status != http.StatusUnprocessableEntity || e.Code != "invalid_triggers" {
			t.Errorf("%s export with triggers [] = %d %s, want 422 invalid_triggers (the provider step passed)", name, resp.Status, resp.Body)
		}
	}
	// unsupported_provider is never produced: not for any provider, in any action or with any body.
	for name, id := range agents {
		for _, action := range []string{"files", "open_pr"} {
			resp := s.post("/agents/"+id+"/export-ci", ciExportBody(ciRepoFull, action, nil))
			if strings.Contains(string(resp.Body), "unsupported_provider") {
				t.Errorf("%s %s answered %d %s, want no unsupported_provider anywhere", name, action, resp.Status, resp.Body)
			}
		}
	}
	// Only the open_pr calls above reached GitHub; the refused ones (ghost, triggers) sent nothing.
	for _, r := range s.gh.requests() {
		if !strings.HasPrefix(r.Path, ciRepoAPI) {
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.Path)
		}
	}
	t.Run("zero GitHub requests for the refusals", func(t *testing.T) {
		s, _ := ciStack(t, ciSpec())
		ghost := cxAgent(t, s, "Ghost Reviewer", "openrouter", ciModel)
		openai := cxAgent(t, s, "OpenAI Reviewer", "openai", cxOpenAI)
		if err := s.db.DB.Exec(`UPDATE agents SET provider = 'custom:ghost' WHERE id = ?`, ghost).Error; err != nil {
			t.Fatal(err)
		}
		s.post("/agents/"+ghost+"/export-ci", ciExportBody(ciRepoFull, "open_pr", nil))
		s.post("/agents/"+openai+"/export-ci", ciExportBody(ciRepoFull, "open_pr", map[string]any{"triggers": []string{}}))
		if got := s.gh.requests(); len(got) != 0 {
			t.Errorf("GitHub requests for the refused exports = %v, want none", ciSeq(got))
		}
		if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
			t.Errorf("ci_installations rows = %d, want 0", n)
		}
	})
}

// AC33, EC20, FR28: an edited workflow of a custom export may read GITHUB_TOKEN and the derived
// secrets of its own provider, and nothing else.
func TestCiExport_CustomEditedWorkflowSecretsRule(t *testing.T) {
	s, _ := ciStack(t, ciSpec())
	cxProvider(t, s, "my-gw", nil)
	agent := cxAgent(t, s, "GW Reviewer", "custom:my-gw", cxModel)
	generated := cxWorkflow(t, "gw-reviewer", "", cxCustomEnv("MY_GW", 2)...)

	if out, _ := cxExport(t, s, agent, ciExportBody(ciRepoFull, "files", map[string]any{"workflow": generated}), http.StatusOK); cxFileText(t, out.Files, ".yml") != generated {
		t.Error("the unedited workflow was changed by the export")
	}
	for _, name := range []string{
		"OPENROUTER_API_KEY", "OPENAI_API_KEY", "DEVDIGEST_OTHER_API_KEY", "DEVDIGEST_MY_GW_HEADER_6", "DEVDIGEST_MY_GW_HEADER_0",
		"DEVDIGEST_MY_GW_HEADER_3", // the provider defines 2 extra headers (AC33)
		"DEVDIGEST_MY_GW_BASE_URL", "DEVDIGEST_MY_GW_API_KEYS",
	} {
		t.Run(name, func(t *testing.T) {
			resp := s.post("/agents/"+agent+"/export-ci", ciExportBody(ciRepoFull, "files", map[string]any{"workflow": cxWithSecretRef(t, generated, name)}))
			e := ciErr(t, resp)
			if resp.Status != http.StatusUnprocessableEntity || e.Code != "workflow_insecure" || cmp.Diff(map[string]any{"rule": "secrets"}, e.Details) != "" {
				t.Errorf("edited workflow reading secrets.%s = %d %s, want 422 workflow_insecure rule secrets", name, resp.Status, resp.Body)
			}
		})
	}
	// Its own secrets and the token are allowed.
	if resp := s.post("/agents/"+agent+"/export-ci", ciExportBody(ciRepoFull, "files", map[string]any{"workflow": cxWithSecretRef(t, generated, "DEVDIGEST_MY_GW_HEADER_2")})); resp.Status != http.StatusOK {
		t.Errorf("edited workflow reading its own header secret = %d %s, want 200", resp.Status, resp.Body)
	}
	if got := s.gh.requests(); len(got) != 0 {
		t.Errorf("GitHub requests = %v, want none", ciSeq(got))
	}
}

// ---- AC47, AC48, EC33, EC35: the stored export-time provider and the ingest ----

// cxIngestRun registers a successful run with a verified artifact for the installation of slug in
// ciRepoFull and returns its GitHub run id.
func cxIngestRun(t *testing.T, gh *ciGH, slug string, id int64) {
	t.Helper()
	run := ciRun(slug, ciRepoFull, id, time.Now().Add(-time.Hour))
	gh.addGoodRun(t, ciRepoFull, slug, run, id+9000, map[string]any{"repository": ciRepoFull})
}

func cxRefresh(t *testing.T, s *stack, want int) {
	t.Helper()
	resp := s.post("/ci/refresh", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	if got, _ := resp.Obj(t)["ingested"].(float64); int(got) != want {
		t.Fatalf("POST /ci/refresh ingested %v, want %d: %s", got, want, resp.Body)
	}
}

// cxRunProvider is the provider stored with the agent run and the ledger entry of a GitHub run.
func cxRunProvider(t *testing.T, s *stack, ghRunID int64) (agentRun, ledger string) {
	t.Helper()
	agentRun = scalar[string](t, s.db.DB, `SELECT a.provider FROM ci_runs c JOIN agent_runs a ON a.id = c.agent_run_id WHERE c.github_run_id = ?`, ghRunID)
	ledger = scalar[string](t, s.db.DB, `SELECT provider FROM cost_ledger WHERE kind = 'ci_run' AND dedupe_key = ?`, fmt.Sprintf("ci:%d:1", ghRunID))
	return agentRun, ledger
}

func cxInstalledProvider(t *testing.T, s *stack, agent string) string {
	t.Helper()
	return scalar[string](t, s.db.DB, `SELECT provider FROM ci_installations WHERE agent_id = ?`, agent)
}

// AC47, EC33, FR37: the provider of a CI run is the one stored at the last open_pr, not the agent's
// current one; a files export changes nothing; a re-export updates it and earlier rows keep theirs.
func TestCustomCI_IngestStoresTheExportTimeProvider(t *testing.T) {
	s, gh := ciStack(t, ciSpec())
	cxProvider(t, s, "synapse", nil)
	switched := cxAgent(t, s, "Switched Reviewer", "openai", cxOpenAI)
	custom := cxAgent(t, s, "Synapse Reviewer", "custom:synapse", cxModel)
	router := cxAgent(t, s, "Router Reviewer", "openrouter", ciModel)

	for _, a := range []string{switched, custom, router} {
		cxExport(t, s, a, ciExportBody(ciRepoFull, "open_pr", nil), http.StatusCreated)
	}
	for agent, want := range map[string]string{switched: "openai", custom: "custom:synapse", router: "openrouter"} {
		if got := cxInstalledProvider(t, s, agent); got != want {
			t.Errorf("stored provider after open_pr = %q, want %q", got, want)
		}
	}

	// The studio switches the agent to anthropic; nothing is re-exported.
	if resp := s.do(http.MethodPut, "/agents/"+switched, map[string]any{"provider": "anthropic", "model": cxAnthropic}); resp.Status != http.StatusOK {
		t.Fatalf("PUT /agents/%s = %d %s, want 200", switched, resp.Status, resp.Body)
	}
	if got := ciAgentField(t, s, switched, "provider"); got != "anthropic" {
		t.Fatalf("agent provider after the switch = %v, want anthropic", got)
	}
	cxIngestRun(t, gh, "switched-reviewer", 1001)
	cxIngestRun(t, gh, "synapse-reviewer", 1002)
	cxIngestRun(t, gh, "router-reviewer", 1003)
	cxRefresh(t, s, 3)
	for id, want := range map[int64]string{1001: "openai", 1002: "custom:synapse", 1003: "openrouter"} {
		if run, ledger := cxRunProvider(t, s, id); run != want || ledger != want {
			t.Errorf("run %d: agent run provider %q, ledger provider %q, want %q for both", id, run, ledger, want)
		}
	}

	// A files export after the switch changes nothing.
	cxExport(t, s, switched, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
	if got := cxInstalledProvider(t, s, switched); got != "openai" {
		t.Errorf("stored provider after a files export = %q, want openai", got)
	}
	cxIngestRun(t, gh, "switched-reviewer", 1004)
	cxRefresh(t, s, 1)
	if run, ledger := cxRunProvider(t, s, 1004); run != "openai" || ledger != "openai" {
		t.Errorf("run 1004 after a files export: agent run provider %q, ledger provider %q, want openai for both", run, ledger)
	}

	// A re-export with open_pr stores the current provider; the earlier rows keep theirs.
	cxExport(t, s, switched, ciExportBody(ciRepoFull, "open_pr", nil), http.StatusCreated)
	if got := cxInstalledProvider(t, s, switched); got != "anthropic" {
		t.Errorf("stored provider after the re-export = %q, want anthropic", got)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations WHERE agent_id = ?`, switched); n != 1 {
		t.Errorf("installation rows of the re-exported agent = %d, want 1 (updated in place)", n)
	}
	cxIngestRun(t, gh, "switched-reviewer", 1005)
	cxRefresh(t, s, 1)
	for id, want := range map[int64]string{1001: "openai", 1004: "openai", 1005: "anthropic", 1002: "custom:synapse", 1003: "openrouter"} {
		if run, ledger := cxRunProvider(t, s, id); run != want || ledger != want {
			t.Errorf("run %d at the end: agent run provider %q, ledger provider %q, want %q for both", id, run, ledger, want)
		}
	}
}

// AC48 (ingest half), EC35: an installation that existed before the migration holds openrouter, and its
// next ingested run stores openrouter whatever the agent's provider is now. The migration itself (existing
// installations become openrouter, the down migration refuses while an agent uses a custom provider,
// reverses and goes up again) is TestMigration0009_CustomProviders in internal/platform/postgres/migrate.
func TestCustomCI_PreexistingInstallationIngestsAsOpenRouter(t *testing.T) {
	s, gh := ciStack(t, ciSpec())
	agent := cxAgent(t, s, "Old Reviewer", "openai", cxOpenAI)
	ciInstall(t, s, agent, ciRepoFull, "old-reviewer", 1, time.Now().Add(-24*time.Hour)) // the migrated row: openrouter
	if got := cxInstalledProvider(t, s, agent); got != "openrouter" {
		t.Fatalf("migrated installation provider = %q, want openrouter", got)
	}
	cxIngestRun(t, gh, "old-reviewer", 2001)
	cxRefresh(t, s, 1)
	if run, ledger := cxRunProvider(t, s, 2001); run != "openrouter" || ledger != "openrouter" {
		t.Errorf("run of a pre-existing installation: agent run provider %q, ledger provider %q, want openrouter for both", run, ledger)
	}
}

// ---- AC49, AC50, EC36, EC37: the price book price in the manifest ----

// AC49 (export half), EC36: a model without a price in the price book exports without a pricing key.
// The runner half (artifact cost_usd null) is in the runner's tests.
func TestCiExport_ModelWithoutAPriceHasNoPricingKey(t *testing.T) {
	s, _ := ciStack(t, ciSpec())
	for _, tc := range []struct{ provider, model string }{{"openai", "gpt-unpriced-9"}, {"anthropic", "claude-unpriced-9"}} {
		t.Run(tc.provider, func(t *testing.T) {
			agent := cxAgent(t, s, "Unpriced "+tc.provider, tc.provider, tc.model)
			out, _ := cxExport(t, s, agent, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
			cxCheckManifest(t, s, agent, out.Files[0].Contents, tc.provider, tc.model, nil)
			if strings.Contains(out.Files[0].Contents, "pricing") {
				t.Errorf("manifest of an unpriced model holds pricing:\n%s", out.Files[0].Contents)
			}
			if diff := cmp.Diff([]string{strings.ToUpper(tc.provider) + "_API_KEY"}, out.RequiredSecrets); diff != "" {
				t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// AC50, EC37: the committed manifest of an open_pr keeps the price it was written with; a files preview
// after the price book changed shows the new price. The price book is the live OpenRouter list here.
func TestCiExport_PriceBookChangeShowsInThePreviewNotInTheCommit(t *testing.T) {
	var prompt, completion atomic.Value
	prompt.Store("0.000002")
	completion.Store("0.000008")
	stub := newLLMStub(t, func(int, http.ResponseWriter, *http.Request) {})
	stub.models = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
			"id": cxOpenAI, "name": "gpt 4.1", "context_length": 128000,
			"pricing": map[string]any{"prompt": prompt.Load(), "completion": completion.Load()},
		}}})
	}
	closed := "http://127.0.0.1:1"
	sp := ciSpec()
	sp.env["OPENROUTER_API_KEY"] = "sk-or-cx-price-book"
	sp.llmURLs = llmURLs{openAI: closed, anthropic: closed, openRouter: stub.URL()}
	sp.llm = llm.Options{PriceTTL: time.Millisecond}
	s, _ := ciStack(t, sp)
	agent := cxAgent(t, s, "Priced Reviewer", "openai", cxOpenAI)

	pricing := func(text string) map[string]any {
		_, m := cxManifest(t, text)
		p, _ := m["pricing"].(map[string]any)
		return p
	}
	committed := func() string {
		trees := ciWith(s.gh.requests(), "POST", "/git/trees")
		if len(trees) != 1 {
			t.Fatalf("POST git/trees requests = %d, want 1", len(trees))
		}
		entries, _ := ciBodyOf(t, trees[0])["tree"].([]any)
		for _, raw := range entries {
			e := raw.(map[string]any)
			if e["path"] == ".devdigest/agents/priced-reviewer.yaml" {
				return e["content"].(string)
			}
		}
		t.Fatalf("the commit has no manifest: %v", entries)
		return ""
	}

	cxExport(t, s, agent, ciExportBody(ciRepoFull, "open_pr", nil), http.StatusCreated)
	if diff := cmp.Diff(cxPricing(2, 8), pricing(committed())); diff != "" {
		t.Fatalf("committed pricing mismatch (-want +got):\n%s", diff)
	}

	prompt.Store("0.000004")
	completion.Store("0.000016")
	// The price book never waits for the network: the new price shows once its background fetch landed.
	deadline := time.Now().Add(15 * time.Second)
	var preview map[string]any
	for time.Now().Before(deadline) {
		out, _ := cxExport(t, s, agent, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
		if preview = pricing(out.Files[0].Contents); preview["promptPerM"] == 4.0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if diff := cmp.Diff(cxPricing(4, 16), preview); diff != "" {
		t.Errorf("preview pricing after the price change mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(cxPricing(2, 8), pricing(committed())); diff != "" {
		t.Errorf("committed pricing after the price change mismatch (-want +got):\n%s", diff)
	}
}
