package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// The export of agents on openai, anthropic and custom providers (FR24-FR29, FR35, FR37).

func agentOn(provider, model string) domain.ExportAgent {
	a := defaultAgent()
	a.Provider, a.Model = provider, model
	return a
}

func gateway() app.CustomExport {
	return app.CustomExport{
		Config: domain.CustomProviderConfig{
			Protocol: "openai", AuthMode: "bearer", ExtraHeaders: []string{"X-Tenant", "X-Project"}, StructuredOutput: "strict",
		},
		KeyConfigured: true,
		Pricing:       &domain.Pricing{PromptPerM: 1.5, CompletionPerM: 6},
	}
}

func customRig(t *testing.T, ce app.CustomExport) (*exportRig, *[]string) {
	t.Helper()
	asked := &[]string{}
	rig := newExportRig(t, rigOpts{
		agent: agentOn("custom:my-gw", "llama-3"), runner: true, configured: true,
		custom: fakeCustom{defined: map[string]app.CustomExport{"custom:my-gw": ce}, asked: asked},
	})
	return rig, asked
}

func filesRequest(runner domain.Runner) app.ExportRequest {
	req := request("acme/widgets")
	req.Action, req.Runner = domain.ActionFiles, runner
	return req
}

func fileMap(ex domain.Export) map[string]string {
	out := map[string]string{}
	for _, f := range ex.Files {
		out[f.Path] = f.Contents
	}
	return out
}

func manifestOf(t *testing.T, ex domain.Export) (map[string]any, string) {
	t.Helper()
	text := fileMap(ex)[domain.ManifestPath("security-reviewer")]
	return parseYAML(t, text), text
}

// topKeys lists the top-level keys of a manifest text in file order.
func topKeys(text string) []string {
	var keys []string
	for _, line := range strings.Split(text, "\n") {
		if line != "" && line[0] != ' ' && line[0] != '-' {
			keys = append(keys, strings.SplitN(line, ":", 2)[0])
		}
	}
	return keys
}

func TestExportProviderStep(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		custom   app.CustomProviders
		wantCode string
		wantMsg  string
		wantKind apperr.Kind
	}{
		{"undefined custom", "custom:ghost", fakeCustom{}, "provider_not_configured", "Custom provider custom:ghost is not configured", apperr.KindValidation},
		{"no custom port", "custom:ghost", nil, "provider_not_configured", "Custom provider custom:ghost is not configured", apperr.KindValidation},
		{"openai passes the provider step", "openai", nil, "invalid_triggers", "", apperr.KindValidation},
		{"anthropic passes the provider step", "anthropic", nil, "invalid_triggers", "", apperr.KindValidation},
		{"openrouter passes the provider step", "openrouter", nil, "invalid_triggers", "", apperr.KindValidation},
		{"defined custom passes the provider step", "custom:my-gw", fakeCustom{defined: map[string]app.CustomExport{"custom:my-gw": gateway()}}, "invalid_triggers", "", apperr.KindValidation},
		{"malformed custom id", "custom:Bad!", fakeCustom{}, "manifest_invalid", "", apperr.KindValidation},
		{"unknown provider", "mistral", nil, "manifest_invalid", "", apperr.KindValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newExportRig(t, rigOpts{agent: agentOn(tc.provider, "m"), runner: true, configured: true, custom: tc.custom})
			req := request("acme/widgets")
			req.Triggers = []string{}
			_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
			ae := wantError(t, err, tc.wantKind, tc.wantCode, tc.wantMsg)
			if ae != nil && ae.Code == "unsupported_provider" {
				t.Errorf("Export() answered unsupported_provider, which no request produces any more")
			}
			if got := rig.rec.snapshot(t); len(got) != 0 {
				t.Errorf("calls before the trigger check = %v, want none (no GitHub call, no store read)", got)
			}
		})
	}
}

func TestExportCustomLookupFailureIsNotAConfigurationAnswer(t *testing.T) {
	boom := errors.New("secrets file unreadable")
	rig := newExportRig(t, rigOpts{agent: agentOn("custom:my-gw", "m"), runner: true, configured: true, custom: fakeCustom{err: boom}})
	_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", request("acme/widgets"))
	if !errors.Is(err, boom) {
		t.Fatalf("Export() error = %v, want the lookup error wrapped", err)
	}
	if ae, ok := apperr.As(err); ok {
		t.Errorf("Export() error = %s %s, want a plain error (500 by the edge)", ae.Kind, ae.Code)
	}
}

func TestExportBuiltInProviders(t *testing.T) {
	tests := []struct {
		provider, model, secret string
		price                   domain.Pricing
	}{
		{"openai", "gpt-4.1", "OPENAI_API_KEY", domain.Pricing{PromptPerM: 2, CompletionPerM: 8}},
		{"anthropic", "claude-sonnet-4-6", "ANTHROPIC_API_KEY", domain.Pricing{PromptPerM: 3, CompletionPerM: 15}},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			prices := fakePrices{table: map[string]domain.Pricing{tc.provider + "/" + tc.model: tc.price}}
			rig := newExportRig(t, rigOpts{agent: agentOn(tc.provider, tc.model), runner: true, configured: true, prices: prices})
			got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
			if err != nil {
				t.Fatalf("Export(files) error = %v", err)
			}
			wf := fileMap(got)[domain.WorkflowPath("security-reviewer")]
			if !strings.Contains(wf, "          "+tc.secret+": ${{ secrets."+tc.secret+" }}\n") || strings.Contains(wf, "OPENROUTER_API_KEY") {
				t.Errorf("workflow keys = %q, want only %s", wf, tc.secret)
			}
			if !strings.Contains(wf, "runs-on: "+domain.RunnerOS+"\n") {
				t.Errorf("workflow has no GitHub-hosted runs-on: %q", wf)
			}
			manifest, text := manifestOf(t, got)
			wantPricing := map[string]any{"promptPerM": tc.price.PromptPerM, "completionPerM": tc.price.CompletionPerM}
			if manifest["provider"] != tc.provider || manifest["custom_provider"] != nil {
				t.Errorf("manifest provider/custom_provider = %v/%v, want %s/none", manifest["provider"], manifest["custom_provider"], tc.provider)
			}
			if diff := cmp.Diff(wantPricing, normalize(t, manifest["pricing"])); diff != "" {
				t.Errorf("manifest pricing mismatch (-want +got):\n%s", diff)
			}
			keys := topKeys(text)
			if keys[len(keys)-2] != "version" || keys[len(keys)-1] != "pricing" {
				t.Errorf("manifest keys = %v, want pricing right after version, last", keys)
			}
			if diff := cmp.Diff([]string{tc.secret}, got.RequiredSecrets); diff != "" {
				t.Errorf("RequiredSecrets mismatch (-want +got):\n%s", diff)
			}
			if got.RequiredVariables == nil || len(got.RequiredVariables) != 0 {
				t.Errorf("RequiredVariables = %#v, want an empty non-nil list", got.RequiredVariables)
			}

			// Another provider's secret in an edited workflow is refused (EC28).
			for _, other := range []string{"OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
				if other == tc.secret {
					continue
				}
				req := filesRequest("")
				edited := strings.ReplaceAll(wf, "secrets."+tc.secret, "secrets."+other)
				req.Workflow = &edited
				_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
				ae := wantError(t, err, apperr.KindValidation, "workflow_insecure", "")
				if diff := cmp.Diff(map[string]any{"rule": "secrets"}, normalize(t, ae.Details)); diff != "" {
					t.Errorf("edited workflow with %s: details mismatch (-want +got):\n%s", other, diff)
				}
			}
		})
	}
}

func TestExportBuiltInPricing(t *testing.T) {
	t.Run("no price omits the key and still succeeds", func(t *testing.T) {
		rig := newExportRig(t, rigOpts{agent: agentOn("openai", "gpt-unpriced"), runner: true, configured: true, prices: fakePrices{}})
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		manifest, _ := manifestOf(t, got)
		if _, has := manifest["pricing"]; has {
			t.Errorf("manifest has a pricing key %v, want none", manifest["pricing"])
		}
	})

	t.Run("no price book omits the key", func(t *testing.T) {
		rig := newExportRig(t, rigOpts{agent: agentOn("anthropic", "claude-sonnet-4-6"), runner: true, configured: true})
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		if manifest, _ := manifestOf(t, got); manifest["pricing"] != nil {
			t.Errorf("manifest pricing = %v, want none", manifest["pricing"])
		}
	})

	t.Run("a price change shows in the next preview only", func(t *testing.T) {
		table := map[string]domain.Pricing{"openai/gpt-4.1": {PromptPerM: 2, CompletionPerM: 8}}
		rig := newExportRig(t, rigOpts{agent: agentOn("openai", "gpt-4.1"), runner: true, configured: true, prices: fakePrices{table: table}})
		first, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		table["openai/gpt-4.1"] = domain.Pricing{PromptPerM: 5, CompletionPerM: 20}
		second, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		for i, want := range []float64{2, 5} {
			m, _ := manifestOf(t, []domain.Export{first, second}[i])
			if got := normalize(t, m["pricing"]).(map[string]any)["promptPerM"]; got != want {
				t.Errorf("preview %d promptPerM = %v, want %v", i+1, got, want)
			}
		}
	})

	t.Run("openrouter never gets a pricing key", func(t *testing.T) {
		prices := fakePrices{table: map[string]domain.Pricing{"openrouter/deepseek/deepseek-v4-flash": {PromptPerM: 1, CompletionPerM: 1}}}
		rig := newExportRig(t, rigOpts{agent: defaultAgent(), runner: true, configured: true, prices: prices})
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		if manifest, _ := manifestOf(t, got); manifest["pricing"] != nil || manifest["custom_provider"] != nil {
			t.Errorf("openrouter manifest has pricing/custom_provider %v/%v, want neither", manifest["pricing"], manifest["custom_provider"])
		}
		if diff := cmp.Diff([]string{"OPENROUTER_API_KEY"}, got.RequiredSecrets); diff != "" {
			t.Errorf("RequiredSecrets mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestExportOpenRouterRunner(t *testing.T) {
	rig := okRig(t)
	hosted, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(domain.RunnerGitHubHosted))
	if err != nil {
		t.Fatalf("Export(github_hosted) error = %v", err)
	}
	def, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
	if err != nil {
		t.Fatalf("Export(default runner) error = %v", err)
	}
	self, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(domain.RunnerSelfHosted))
	if err != nil {
		t.Fatalf("Export(self_hosted) error = %v", err)
	}
	path := domain.WorkflowPath("security-reviewer")
	if fileMap(hosted)[path] != fileMap(def)[path] {
		t.Errorf("an empty runner differs from github_hosted")
	}
	want := strings.Replace(fileMap(hosted)[path], "runs-on: "+domain.RunnerOS, "runs-on: "+domain.RunsOnSelfHosted, 1)
	if diff := cmp.Diff(want, fileMap(self)[path]); diff != "" {
		t.Errorf("self_hosted workflow differs from the hosted one in more than runs-on (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"OPENROUTER_API_KEY"}, hosted.RequiredSecrets); diff != "" {
		t.Errorf("RequiredSecrets mismatch (-want +got):\n%s", diff)
	}
	if hosted.RequiredVariables == nil || len(hosted.RequiredVariables) != 0 {
		t.Errorf("RequiredVariables = %#v, want an empty non-nil list", hosted.RequiredVariables)
	}
}

func TestExportCustomProvider(t *testing.T) {
	t.Run("files", func(t *testing.T) {
		rig, asked := customRig(t, gateway())
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(domain.RunnerSelfHosted))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		if diff := cmp.Diff([]string{"ws-1 custom:my-gw llama-3"}, *asked); diff != "" {
			t.Errorf("lookups mismatch (-want +got):\n%s", diff)
		}
		wf := fileMap(got)[domain.WorkflowPath("security-reviewer")]
		for _, line := range []string{
			"runs-on: " + domain.RunsOnSelfHosted + "\n",
			"          DEVDIGEST_LLM_BASE_URL: ${{ vars.DEVDIGEST_MY_GW_BASE_URL }}\n",
			"          DEVDIGEST_LLM_API_KEY: ${{ secrets.DEVDIGEST_MY_GW_API_KEY }}\n",
			"          DEVDIGEST_LLM_HEADER_1: ${{ secrets.DEVDIGEST_MY_GW_HEADER_1 }}\n",
			"          DEVDIGEST_LLM_HEADER_2: ${{ secrets.DEVDIGEST_MY_GW_HEADER_2 }}\n",
		} {
			if !strings.Contains(wf, line) {
				t.Errorf("workflow lacks %q", line)
			}
		}
		if strings.Contains(wf, "OPENROUTER_API_KEY") || strings.Contains(wf, "DEVDIGEST_LLM_HEADER_3") {
			t.Errorf("workflow keeps the openrouter key or a third header: %q", wf)
		}
		manifest, text := manifestOf(t, got)
		wantBlock := map[string]any{
			"protocol": "openai", "auth_mode": "bearer", "auth_header": nil, "extra_headers": []any{"X-Tenant", "X-Project"},
			"structured_output": "strict", "pricing": map[string]any{"promptPerM": 1.5, "completionPerM": 6.0},
		}
		if diff := cmp.Diff(wantBlock, normalize(t, manifest["custom_provider"])); diff != "" {
			t.Errorf("custom_provider mismatch (-want +got):\n%s", diff)
		}
		if keys := topKeys(text); keys[len(keys)-1] != "custom_provider" || manifest["pricing"] != nil || manifest["provider"] != "custom:my-gw" {
			t.Errorf("manifest keys = %v pricing = %v provider = %v, want custom_provider last and no top-level pricing", keys, manifest["pricing"], manifest["provider"])
		}
		wantSecrets := []string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2"}
		if diff := cmp.Diff(wantSecrets, got.RequiredSecrets); diff != "" {
			t.Errorf("RequiredSecrets mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{"DEVDIGEST_MY_GW_BASE_URL"}, got.RequiredVariables); diff != "" {
			t.Errorf("RequiredVariables mismatch (-want +got):\n%s", diff)
		}
		if got.Installation != nil || len(rig.store.upserts) != 0 {
			t.Errorf("files stored an installation: %+v / %+v", got.Installation, rig.store.upserts)
		}
	})

	t.Run("default runner is GitHub-hosted", func(t *testing.T) {
		rig, _ := customRig(t, gateway())
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		if wf := fileMap(got)[domain.WorkflowPath("security-reviewer")]; !strings.Contains(wf, "runs-on: "+domain.RunnerOS+"\n") {
			t.Errorf("workflow has no GitHub-hosted runs-on: %q", wf)
		}
	})

	t.Run("no stored key does not require the key secret", func(t *testing.T) {
		ce := gateway()
		ce.KeyConfigured, ce.Config.ExtraHeaders, ce.Pricing = false, nil, nil
		rig, _ := customRig(t, ce)
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		if len(got.RequiredSecrets) != 0 || got.RequiredSecrets == nil {
			t.Errorf("RequiredSecrets = %#v, want an empty non-nil list", got.RequiredSecrets)
		}
		if wf := fileMap(got)[domain.WorkflowPath("security-reviewer")]; !strings.Contains(wf, "DEVDIGEST_LLM_API_KEY: ${{ secrets.DEVDIGEST_MY_GW_API_KEY }}") {
			t.Errorf("the key line is always present in the workflow: %q", wf)
		}
		manifest, _ := manifestOf(t, got)
		block := normalize(t, manifest["custom_provider"]).(map[string]any)
		if block["pricing"] != nil {
			t.Errorf("custom_provider.pricing = %v, want null", block["pricing"])
		}
	})

	t.Run("header auth keeps the header name in the manifest only", func(t *testing.T) {
		ce := gateway()
		name := "X-Gateway-Key"
		ce.Config.AuthMode, ce.Config.AuthHeader = "header", &name
		rig, _ := customRig(t, ce)
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", filesRequest(""))
		if err != nil {
			t.Fatalf("Export(files) error = %v", err)
		}
		for path, text := range fileMap(got) {
			isManifest := path == domain.ManifestPath("security-reviewer")
			if strings.Contains(text, name) != isManifest {
				t.Errorf("file %s contains the auth header name = %v, want %v", path, !isManifest, isManifest)
			}
		}
	})

	t.Run("edited workflow may use the provider's names only", func(t *testing.T) {
		rig, _ := customRig(t, gateway())
		base := fileMap(mustExport(t, rig, filesRequest("")))[domain.WorkflowPath("security-reviewer")]
		for _, tc := range []struct {
			name, from, to string
			ok             bool
		}{
			{"own header 5", "secrets.DEVDIGEST_MY_GW_HEADER_2", "secrets.DEVDIGEST_MY_GW_HEADER_5", true},
			{"openrouter key", "secrets.DEVDIGEST_MY_GW_API_KEY", "secrets.OPENROUTER_API_KEY", false},
			{"another provider's key", "secrets.DEVDIGEST_MY_GW_API_KEY", "secrets.DEVDIGEST_OTHER_API_KEY", false},
			{"header 6", "secrets.DEVDIGEST_MY_GW_HEADER_2", "secrets.DEVDIGEST_MY_GW_HEADER_6", false},
		} {
			req := filesRequest("")
			edited := strings.Replace(base, tc.from, tc.to, 1)
			req.Workflow = &edited
			_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
			if tc.ok && err != nil {
				t.Errorf("%s: Export() error = %v, want the edit accepted", tc.name, err)
			}
			if !tc.ok {
				ae := wantError(t, err, apperr.KindValidation, "workflow_insecure", "")
				if ae != nil && cmp.Diff(map[string]any{"rule": "secrets"}, normalize(t, ae.Details)) != "" {
					t.Errorf("%s: details = %v, want rule secrets", tc.name, ae.Details)
				}
			}
		}
	})
}

func mustExport(t *testing.T, rig *exportRig, req app.ExportRequest) domain.Export {
	t.Helper()
	got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	return got
}

func TestExportOpenPRBodyAndStoredProvider(t *testing.T) {
	t.Run("custom lists every secret and the variable", func(t *testing.T) {
		rig, _ := customRig(t, gateway())
		var body string
		rig.gh.openPR = func(_ app.RepoRef, in app.OpenPullRequestInput) (string, error) {
			body = in.Body
			return "https://x/pull/1", nil
		}
		got := mustExport(t, rig, request("acme/widgets"))
		want := "1. Add the repository Actions secrets `DEVDIGEST_MY_GW_API_KEY`, `DEVDIGEST_MY_GW_HEADER_1`, `DEVDIGEST_MY_GW_HEADER_2` and the variable `DEVDIGEST_MY_GW_BASE_URL` (Settings > Secrets and variables > Actions)."
		if !strings.Contains(body, want) || strings.Contains(body, "OPENROUTER_API_KEY") {
			t.Errorf("PR body = %q, want step 1 %q", body, want)
		}
		if len(rig.store.upserts) != 1 || rig.store.upserts[0].Provider != "custom:my-gw" {
			t.Errorf("upserts = %+v, want one with provider custom:my-gw (FR37)", rig.store.upserts)
		}
		if got.Installation == nil || got.Installation.Provider != "custom:my-gw" {
			t.Errorf("installation = %+v, want the stored provider", got.Installation)
		}
		if len(got.RequiredSecrets) != 3 || len(got.RequiredVariables) != 1 {
			t.Errorf("required = %v / %v, want 3 secrets and 1 variable on open_pr too", got.RequiredSecrets, got.RequiredVariables)
		}
	})

	t.Run("openai PR body names its secret", func(t *testing.T) {
		rig := newExportRig(t, rigOpts{agent: agentOn("openai", "gpt-4.1"), runner: true, configured: true})
		var body string
		rig.gh.openPR = func(_ app.RepoRef, in app.OpenPullRequestInput) (string, error) {
			body = in.Body
			return "https://x/pull/1", nil
		}
		mustExport(t, rig, request("acme/widgets"))
		if !strings.Contains(body, "1. Add the repository Actions secret `OPENAI_API_KEY` (Settings") || strings.Contains(body, "OPENROUTER_API_KEY") {
			t.Errorf("PR body = %q, want step 1 naming OPENAI_API_KEY only", body)
		}
		if len(rig.store.upserts) != 1 || rig.store.upserts[0].Provider != "openai" {
			t.Errorf("upserts = %+v, want one with provider openai", rig.store.upserts)
		}
	})

	t.Run("a re-export overwrites the provider, files changes nothing", func(t *testing.T) {
		rig := okRig(t)
		mustExport(t, rig, request("acme/widgets"))
		agent := rig.agents["agent-1"]
		agent.Provider, agent.Model = "anthropic", "claude-sonnet-4-6"
		rig.agents["agent-1"] = agent
		mustExport(t, rig, filesRequest(""))
		if got := providersOf(rig.store.upserts); !slices.Equal(got, []string{"openrouter"}) {
			t.Fatalf("providers after open_pr, switch and files = %v, want [openrouter] (files writes nothing)", got)
		}
		mustExport(t, rig, request("acme/widgets"))
		if got := providersOf(rig.store.upserts); !slices.Equal(got, []string{"openrouter", "anthropic"}) {
			t.Errorf("providers after the re-export = %v, want [openrouter anthropic]", got)
		}
	})
}

func providersOf(in []domain.NewInstallation) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = n.Provider
	}
	return out
}
