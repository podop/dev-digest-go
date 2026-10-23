package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Fixed parts of the generated workflow (spec 7.6.5).
const (
	// RunnerOS is the GitHub-hosted runner image the job runs on.
	RunnerOS = "ubuntu-24.04"
	// ForkGuardExpr is the job condition that keeps fork pull requests away from the secrets.
	ForkGuardExpr = "github.event.pull_request.head.repo.full_name == github.repository"

	// ActionCheckout and ActionUploadArtifact are pinned to full commit SHAs, the same ones the
	// Node studio pins.
	ActionCheckout       = "actions/checkout@11d5960a326750d5838078e36cf38b85af677262"
	ActionUploadArtifact = "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02"
)

// SecretNames are the only secrets a workflow may reference.
var SecretNames = []string{"OPENROUTER_API_KEY", "GITHUB_TOKEN"}

// ErrInvalidWorkflowOptions is returned by BuildWorkflow for a slug, trigger list or post_as that
// would put user-controlled text into the YAML.
var ErrInvalidWorkflowOptions = errors.New("invalid workflow options")

// NormalizeTriggers validates the requested triggers: a non-empty list made only of known
// pull_request types. It returns them without duplicates in the order of Triggers, or false.
func NormalizeTriggers(requested []string) ([]Trigger, bool) {
	if len(requested) == 0 {
		return nil, false
	}
	known := Triggers()
	for _, r := range requested {
		if !slices.Contains(known, Trigger(r)) {
			return nil, false
		}
	}
	out := make([]Trigger, 0, len(known))
	for _, t := range known {
		if slices.Contains(requested, string(t)) {
			out = append(out, t)
		}
	}
	return out, true
}

// ValidPostAs reports whether p is one of the posting modes.
func ValidPostAs(p PostAs) bool {
	return p == PostAsGitHubReview || p == PostAsPRComment || p == PostAsNone
}

// WorkflowOptions are the values interpolated into the generated workflow.
type WorkflowOptions struct {
	Slug     string
	Triggers []Trigger
	PostAs   PostAs
}

// workflowTemplate is the text of spec 7.6.5 with @@NAME@@ placeholders. The values are checked
// before they are put in, so nothing user-controlled reaches the YAML.
const workflowTemplate = `name: @@CHECK@@

on:
  pull_request:
    types: [@@TRIGGERS@@]

permissions:
  contents: read
  pull-requests: write

jobs:
  review:
    name: @@CHECK@@
    if: @@FORK_GUARD@@
    runs-on: @@RUNNER_OS@@
    steps:
      - name: Check out the pull request
        uses: @@CHECKOUT@@
        with:
          ref: ${{ github.event.pull_request.head.sha }}
          persist-credentials: false

      - name: Run the DevDigest review
        shell: bash
        run: |
          base64 -d @@RUNNER_PATH@@ | gzip -dc > "$RUNNER_TEMP/@@RUNNER_NAME@@"
          cd "$RUNNER_TEMP" && sha256sum --check --strict "$GITHUB_WORKSPACE/@@SUM_PATH@@"
          chmod +x "$RUNNER_TEMP/@@RUNNER_NAME@@"
          cd "$GITHUB_WORKSPACE" && "$RUNNER_TEMP/@@RUNNER_NAME@@"
        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          DEVDIGEST_AGENT: @@SLUG@@
          DEVDIGEST_POST_AS: @@POST_AS@@
          PR_HEAD_SHA: ${{ github.event.pull_request.head.sha }}

      - name: Upload the review result
        if: always()
        uses: @@UPLOAD@@
        with:
          name: @@ARTIFACT@@
          path: @@RESULT_FILE@@
`

// BuildWorkflow generates the hardened per-agent workflow (spec 7.6.5). It decodes the committed
// runner, verifies its checksum and runs it; there is no Node setup step. The result passes
// checkWorkflow. ErrInvalidWorkflowOptions is returned for a slug that does not match the slug
// shape, an empty or unknown trigger, or an unknown post_as.
func BuildWorkflow(o WorkflowOptions) (string, error) {
	if !ValidSlug(o.Slug) {
		return "", fmt.Errorf("%w: slug", ErrInvalidWorkflowOptions)
	}
	if len(o.Triggers) == 0 {
		return "", fmt.Errorf("%w: triggers", ErrInvalidWorkflowOptions)
	}
	names := make([]string, len(o.Triggers))
	for i, t := range o.Triggers {
		if !slices.Contains(Triggers(), t) {
			return "", fmt.Errorf("%w: triggers", ErrInvalidWorkflowOptions)
		}
		names[i] = string(t)
	}
	if !ValidPostAs(o.PostAs) {
		return "", fmt.Errorf("%w: post_as", ErrInvalidWorkflowOptions)
	}
	return strings.NewReplacer(
		"@@CHECK@@", CheckName(o.Slug),
		"@@TRIGGERS@@", strings.Join(names, ", "),
		"@@FORK_GUARD@@", ForkGuardExpr,
		"@@RUNNER_OS@@", RunnerOS,
		"@@CHECKOUT@@", ActionCheckout,
		"@@RUNNER_PATH@@", RunnerPath,
		"@@RUNNER_NAME@@", RunnerBinaryName,
		"@@SUM_PATH@@", RunnerSumPath,
		"@@SLUG@@", o.Slug,
		"@@POST_AS@@", string(o.PostAs),
		"@@UPLOAD@@", ActionUploadArtifact,
		"@@ARTIFACT@@", ArtifactName(o.Slug),
		"@@RESULT_FILE@@", ResultFileName,
	).Replace(workflowTemplate), nil
}
