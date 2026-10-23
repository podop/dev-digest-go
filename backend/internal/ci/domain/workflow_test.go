package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
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
