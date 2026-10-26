package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/ci/domain/wfrules"
)

// wantWorkflow is the text of spec 7.6.5 for slug security-reviewer, triggers [synchronize] and
// post_as none. It differs from the Node template only by the removed "Set up Node.js" step and
// the run step (D6007).
const wantWorkflow = `name: DevDigest Review · security-reviewer

on:
  pull_request:
    types: [synchronize]

permissions:
  contents: read
  pull-requests: write

jobs:
  review:
    name: DevDigest Review · security-reviewer
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-24.04
    steps:
      - name: Check out the pull request
        uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262
        with:
          ref: ${{ github.event.pull_request.head.sha }}
          persist-credentials: false

      - name: Run the DevDigest review
        shell: bash
        run: |
          base64 -d .devdigest/runner/devdigest-runner-linux-amd64.gz.b64 | gzip -dc > "$RUNNER_TEMP/devdigest-runner"
          cd "$RUNNER_TEMP" && sha256sum --check --strict "$GITHUB_WORKSPACE/.devdigest/runner/devdigest-runner-linux-amd64.sha256"
          chmod +x "$RUNNER_TEMP/devdigest-runner"
          cd "$GITHUB_WORKSPACE" && "$RUNNER_TEMP/devdigest-runner"
        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          DEVDIGEST_AGENT: security-reviewer
          DEVDIGEST_POST_AS: none
          PR_HEAD_SHA: ${{ github.event.pull_request.head.sha }}

      - name: Upload the review result
        if: always()
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: devdigest-result-security-reviewer
          path: devdigest-result.json
`

func TestBuildWorkflow_Text(t *testing.T) {
	got, err := domain.BuildWorkflow(domain.WorkflowOptions{
		Slug: "security-reviewer", Triggers: []domain.Trigger{domain.TriggerSynchronize}, PostAs: domain.PostAsNone,
	})
	if err != nil {
		t.Fatalf("BuildWorkflow() error = %v", err)
	}
	if diff := cmp.Diff(wantWorkflow, got); diff != "" {
		t.Errorf("BuildWorkflow() mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildWorkflow_TriggersAndPostAs(t *testing.T) {
	got, err := domain.BuildWorkflow(domain.WorkflowOptions{
		Slug: "a", Triggers: []domain.Trigger{domain.TriggerOpened, domain.TriggerReopened}, PostAs: domain.PostAsPRComment,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"    types: [opened, reopened]\n", "          DEVDIGEST_POST_AS: pr_comment\n"} {
		if !strings.Contains(got, line) {
			t.Errorf("BuildWorkflow() lacks %q", line)
		}
	}
}

func TestBuildWorkflow_Invalid(t *testing.T) {
	ok := []domain.Trigger{domain.TriggerOpened}
	tests := []struct {
		name string
		o    domain.WorkflowOptions
	}{
		{"slug injection", domain.WorkflowOptions{Slug: "a\n  evil: 1", Triggers: ok, PostAs: domain.PostAsNone}},
		{"empty slug", domain.WorkflowOptions{Slug: "", Triggers: ok, PostAs: domain.PostAsNone}},
		{"no triggers", domain.WorkflowOptions{Slug: "a", PostAs: domain.PostAsNone}},
		{"unknown trigger", domain.WorkflowOptions{Slug: "a", Triggers: []domain.Trigger{"labeled"}, PostAs: domain.PostAsNone}},
		{"unknown post_as", domain.WorkflowOptions{Slug: "a", Triggers: ok, PostAs: "email"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := domain.BuildWorkflow(tc.o); !errors.Is(err, domain.ErrInvalidWorkflowOptions) {
				t.Errorf("BuildWorkflow(%+v) error = %v, want ErrInvalidWorkflowOptions", tc.o, err)
			}
		})
	}
}

func TestNormalizeTriggers(t *testing.T) {
	tests := []struct {
		name   string
		in     []string
		want   []domain.Trigger
		wantOK bool
	}{
		{"all in order", []string{"opened", "synchronize", "reopened"}, []domain.Trigger{"opened", "synchronize", "reopened"}, true},
		{"reordered", []string{"reopened", "opened"}, []domain.Trigger{"opened", "reopened"}, true},
		{"duplicates", []string{"synchronize", "synchronize"}, []domain.Trigger{"synchronize"}, true},
		{"empty", nil, nil, false},
		{"unknown", []string{"opened", "closed"}, nil, false},
		{"case matters", []string{"Opened"}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.NormalizeTriggers(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("NormalizeTriggers(%v) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("NormalizeTriggers(%v) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

// wfOpts are the options of wantWorkflow with the given provider-related fields.
func wfOpts(provider string, runner domain.Runner, headers int) domain.WorkflowOptions {
	return domain.WorkflowOptions{
		Slug: "security-reviewer", Triggers: []domain.Trigger{domain.TriggerSynchronize}, PostAs: domain.PostAsNone,
		Provider: provider, Runner: runner, HeaderCount: headers,
	}
}

const keyLineOpenRouter = "          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}\n"

// P12: an explicit openrouter provider and the GitHub-hosted runner change no byte; the header
// count is ignored for a built-in provider.
func TestBuildWorkflow_OpenRouterByteIdentical(t *testing.T) {
	for _, o := range []domain.WorkflowOptions{
		wfOpts("openrouter", "", 0),
		wfOpts("openrouter", domain.RunnerGitHubHosted, 0),
		wfOpts("openrouter", "", 4),
	} {
		got, err := domain.BuildWorkflow(o)
		if err != nil {
			t.Fatalf("BuildWorkflow(%+v) error = %v", o, err)
		}
		if diff := cmp.Diff(wantWorkflow, got); diff != "" {
			t.Errorf("BuildWorkflow(%+v) mismatch (-want +got):\n%s", o, diff)
		}
	}
}

// FR25: self_hosted changes only the runs-on line.
func TestBuildWorkflow_SelfHostedChangesOnlyRunsOn(t *testing.T) {
	got, err := domain.BuildWorkflow(wfOpts("openrouter", domain.RunnerSelfHosted, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(wantWorkflow, "    runs-on: ubuntu-24.04\n", "    runs-on: [self-hosted, linux, x64]\n", 1)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildWorkflow(self_hosted) mismatch (-want +got):\n%s", diff)
	}
}

// FR35, AC45: openai and anthropic replace only the key line.
func TestBuildWorkflow_BuiltinProviders(t *testing.T) {
	tests := []struct{ provider, secret string }{{"openai", "OPENAI_API_KEY"}, {"anthropic", "ANTHROPIC_API_KEY"}}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			got, err := domain.BuildWorkflow(wfOpts(tc.provider, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(wantWorkflow, keyLineOpenRouter, "          "+tc.secret+": ${{ secrets."+tc.secret+" }}\n", 1)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("BuildWorkflow(%s) mismatch (-want +got):\n%s", tc.provider, diff)
			}
			if strings.Contains(got, "OPENROUTER") {
				t.Errorf("BuildWorkflow(%s) mentions OPENROUTER", tc.provider)
			}
			names, _ := domain.SecretNames(tc.provider)
			if r := wfrules.CheckWith(got, names); !r.OK {
				t.Errorf("CheckWith(BuildWorkflow(%s)) = rule %s, want OK", tc.provider, r.Rule)
			}
			if r := wfrules.Check(got); r.OK {
				t.Errorf("Check(BuildWorkflow(%s)) = OK, want the secrets rule to refuse", tc.provider)
			}
		})
	}
}

// FR26, EC25, AC30: the custom provider env lines come before GITHUB_TOKEN, one header line per
// header in order, and nothing of OPENROUTER remains.
func TestBuildWorkflow_CustomProvider(t *testing.T) {
	got, err := domain.BuildWorkflow(wfOpts("custom:my-gw", domain.RunnerSelfHosted, 2))
	if err != nil {
		t.Fatal(err)
	}
	envLines := "          DEVDIGEST_LLM_BASE_URL: ${{ vars.DEVDIGEST_MY_GW_BASE_URL }}\n" +
		"          DEVDIGEST_LLM_API_KEY: ${{ secrets.DEVDIGEST_MY_GW_API_KEY }}\n" +
		"          DEVDIGEST_LLM_HEADER_1: ${{ secrets.DEVDIGEST_MY_GW_HEADER_1 }}\n" +
		"          DEVDIGEST_LLM_HEADER_2: ${{ secrets.DEVDIGEST_MY_GW_HEADER_2 }}\n"
	want := strings.Replace(wantWorkflow, keyLineOpenRouter, envLines, 1)
	want = strings.Replace(want, "    runs-on: ubuntu-24.04\n", "    runs-on: [self-hosted, linux, x64]\n", 1)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildWorkflow(custom:my-gw) mismatch (-want +got):\n%s", diff)
	}
	names, _ := domain.SecretNames("custom:my-gw")
	if r := wfrules.CheckWith(got, names); !r.OK {
		t.Errorf("CheckWith(BuildWorkflow(custom:my-gw)) = rule %s, want OK", r.Rule)
	}
	// EC20: the same text with another provider's key is refused.
	other := strings.Replace(got, "secrets.DEVDIGEST_MY_GW_API_KEY", "secrets.DEVDIGEST_OTHER_API_KEY", 1)
	if r := wfrules.CheckWith(other, names); r.OK || r.Rule != wfrules.Secrets {
		t.Errorf("CheckWith(text with DEVDIGEST_OTHER_API_KEY) = %+v, want rule secrets", r)
	}
}

func TestBuildWorkflow_CustomProviderHeaderCounts(t *testing.T) {
	for n := 0; n <= domain.MaxExtraHeaders; n++ {
		got, err := domain.BuildWorkflow(wfOpts("custom:gw", "", n))
		if err != nil {
			t.Fatalf("BuildWorkflow(headers=%d) error = %v", n, err)
		}
		if c := strings.Count(got, "DEVDIGEST_LLM_HEADER_"); c != n {
			t.Errorf("BuildWorkflow(headers=%d) has %d header lines", n, c)
		}
		if !strings.Contains(got, "DEVDIGEST_LLM_BASE_URL: ${{ vars.DEVDIGEST_GW_BASE_URL }}\n") {
			t.Errorf("BuildWorkflow(headers=%d) lacks the base URL line", n)
		}
	}
}

// NFR8: nothing but fixed text and derived names gets into the YAML.
func TestBuildWorkflow_InvalidProviderOptions(t *testing.T) {
	tests := []struct {
		name string
		o    domain.WorkflowOptions
	}{
		{"unknown provider", wfOpts("gemini", "", 0)},
		{"empty custom slug", wfOpts("custom:", "", 0)},
		{"slug with newline", wfOpts("custom:a\n  evil: 1", "", 0)},
		{"uppercase slug", wfOpts("custom:ABC", "", 0)},
		{"too many headers", wfOpts("custom:a", "", 6)},
		{"negative headers", wfOpts("custom:a", "", -1)},
		{"unknown runner", wfOpts("openrouter", "ubuntu-latest", 0)},
		{"runner injection", wfOpts("openrouter", "self_hosted\n  evil: 1", 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := domain.BuildWorkflow(tc.o); !errors.Is(err, domain.ErrInvalidWorkflowOptions) {
				t.Errorf("BuildWorkflow(%+v) = (%q, %v), want ErrInvalidWorkflowOptions", tc.o, got, err)
			}
		})
	}
}
