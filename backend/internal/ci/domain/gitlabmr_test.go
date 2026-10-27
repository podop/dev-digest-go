package domain_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/domain"
)

func TestBuildMRDescription_Golden(t *testing.T) {
	tests := []struct {
		name   string
		in     domain.MRDescInput
		golden string
	}{
		{
			name: "root config created: no include step",
			in: domain.MRDescInput{
				Slug: "security-reviewer",
				Paths: []string{
					".devdigest/agents/security-reviewer.yaml", ".devdigest/memory.jsonl",
					".gitlab/devdigest/review-security-reviewer.yml", ".gitlab-ci.yml",
				},
				RunnerSHA256: "abc123", BuildID: "0123456789ab",
				Secrets:    domain.GitLabRequiredSecrets("openrouter", false, 0, domain.PostAsGitHubReview),
				Variables:  domain.RequiredVariables("openrouter"),
				RootConfig: domain.RootConfigCreate, ConfigPath: ".gitlab-ci.yml",
			},
			golden: "gitlab_mr_create.golden",
		},
		{
			name: "custom path, custom provider and a triggers note",
			in: domain.MRDescInput{
				Slug:         "my-gw",
				Paths:        []string{".devdigest/agents/my-gw.yaml", ".gitlab/devdigest/review-my-gw.yml"},
				RunnerSHA256: "abc123", BuildID: "0123456789ab",
				Secrets:    domain.GitLabRequiredSecrets("custom:my-gw", true, 1, domain.PostAsGitHubReview),
				Variables:  domain.RequiredVariables("custom:my-gw"),
				RootConfig: domain.RootConfigCustomPath, ConfigPath: "ci/main.yml",
				TriggersNote: domain.TriggersNote([]domain.Trigger{domain.TriggerOpened}),
			},
			golden: "gitlab_mr_keep_existing.golden",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := strings.TrimSuffix(golden(t, tc.golden), "\n")
			if diff := cmp.Diff(want, domain.BuildMRDescription(tc.in)); diff != "" {
				t.Errorf("BuildMRDescription(%s) mismatch (-want +got):\n%s", tc.in.Slug, diff)
			}
		})
	}
}

// TestBuildMRDescription_RootConfigWarnings is AC16: both static warnings (SCR3) go with the
// include step, whatever the root configuration says, and only there.
func TestBuildMRDescription_RootConfigWarnings(t *testing.T) {
	tests := []struct {
		root     domain.RootConfig
		wantStep bool
	}{
		{domain.RootConfigCreate, false},
		{domain.RootConfigKeepExisting, true},
		{domain.RootConfigCustomPath, true},
	}
	for _, tc := range tests {
		t.Run(string(tc.root), func(t *testing.T) {
			in := domain.MRDescInput{Slug: "a", Paths: []string{"x"}, Secrets: []string{"OPENAI_API_KEY"}, RootConfig: tc.root}
			if tc.root == domain.RootConfigCustomPath {
				in.ConfigPath = "ci/main.yml"
			}
			got := domain.BuildMRDescription(in)
			for _, want := range []string{domain.WarnWorkflowRules, domain.WarnStagesTest, "- local: .gitlab/devdigest/review-a.yml"} {
				if has := strings.Contains(got, want); has != tc.wantStep {
					t.Errorf("BuildMRDescription(root %s) contains %q = %v, want %v", tc.root, want, has, tc.wantStep)
				}
			}
			if has := strings.Contains(got, "Add this to `.gitlab-ci.yml`:"); has != (tc.root == domain.RootConfigKeepExisting) {
				t.Errorf("BuildMRDescription(root %s) names the default config path = %v", tc.root, has)
			}
		})
	}
	if domain.WarnWorkflowRules != "If your workflow:rules exclude merge request pipelines, the review job cannot run" ||
		domain.WarnStagesTest != "Your stages must include test, or change the job's stage" {
		t.Errorf("warnings = %q, %q, want the EC14 and EC15 texts", domain.WarnWorkflowRules, domain.WarnStagesTest)
	}
}

func TestBuildMRDescription_Steps(t *testing.T) {
	tests := []struct {
		name string
		in   domain.MRDescInput
		want []string
		not  []string
	}{
		{
			name: "post_as none: no token item, steps renumbered without step 2",
			in: domain.MRDescInput{Slug: "a", Paths: []string{"x"}, RootConfig: domain.RootConfigCreate,
				Secrets: domain.GitLabRequiredSecrets("openai", false, 0, domain.PostAsNone)},
			want: []string{"   - `OPENAI_API_KEY`\n", "2. Merge this merge request.", "3. To block merges"},
			not:  []string{"DEVDIGEST_GITLAB_TOKEN", "visible variable", "4. "},
		},
		{
			name: "no key and no posting: only the visible variable",
			in: domain.MRDescInput{Slug: "a", Paths: []string{"x"}, RootConfig: domain.RootConfigCreate,
				Secrets:   domain.GitLabRequiredSecrets("custom:a", false, 0, domain.PostAsNone),
				Variables: domain.RequiredVariables("custom:a")},
			want: []string{"1. Add a visible variable (Settings > CI/CD > Variables): `DEVDIGEST_A_BASE_URL` = the gateway base URL.\n"},
			not:  []string{"masked"},
		},
		{
			name: "a hostile config path cannot end the code span or the list item",
			in: domain.MRDescInput{Slug: "a", Paths: []string{"x"}, RootConfig: domain.RootConfigCustomPath,
				ConfigPath: "x`\n9. injected"},
			want: []string{"Add this to `x9. injected`:"},
			not:  []string{"\n9. injected"},
		},
		{
			name: "empty config path falls back to the default",
			in:   domain.MRDescInput{Slug: "a", Paths: []string{"x"}, RootConfig: domain.RootConfigKeepExisting},
			want: []string{"Add this to `.gitlab-ci.yml`:"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.BuildMRDescription(tc.in)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("BuildMRDescription = %q, want it to contain %q", got, w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("BuildMRDescription = %q, want it not to contain %q", got, n)
				}
			}
		})
	}
}
