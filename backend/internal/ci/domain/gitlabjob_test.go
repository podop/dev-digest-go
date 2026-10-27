package domain_test

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"

	"dev-digest-go/backend/internal/ci/domain"
)

// golden reads testdata/<name>; {{IMAGE}} stands for domain.JobImage, so pinning the real digest
// changes no golden.
func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return strings.ReplaceAll(string(b), "{{IMAGE}}", domain.JobImage)
}

func TestBuildGitLabJob_Golden(t *testing.T) {
	tests := []struct {
		name   string
		opts   domain.GitLabJobOptions
		golden string
	}{
		{
			name:   "openrouter on shared runners has no tags and no provider lines (AC7)",
			opts:   domain.GitLabJobOptions{Slug: "security-reviewer", PostAs: domain.PostAsGitHubReview},
			golden: "gitlab_job_default.golden",
		},
		{
			name: "custom provider with two headers on a self-managed runner (AC9)",
			opts: domain.GitLabJobOptions{
				Slug: "my-gw", PostAs: domain.PostAsNone, Provider: "custom:my-gw",
				Runner: domain.RunnerSelfHosted, HeaderCount: 2,
			},
			golden: "gitlab_job_custom_selfhosted.golden",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.BuildGitLabJob(tc.opts)
			if err != nil {
				t.Fatalf("BuildGitLabJob(%+v) error = %v", tc.opts, err)
			}
			if diff := cmp.Diff(golden(t, tc.golden), got); diff != "" {
				t.Errorf("BuildGitLabJob(%+v) mismatch (-want +got):\n%s", tc.opts, diff)
			}
		})
	}
}

// jobDoc is the part of a job file the structure tests read.
type jobDoc map[string]struct {
	Stage string `yaml:"stage"`
	Image struct {
		Name       string   `yaml:"name"`
		Entrypoint []string `yaml:"entrypoint"`
	} `yaml:"image"`
	Tags      []string          `yaml:"tags"`
	Variables map[string]string `yaml:"variables"`
	Script    []string          `yaml:"script"`
	Artifacts struct {
		When  string   `yaml:"when"`
		Name  string   `yaml:"name"`
		Paths []string `yaml:"paths"`
	} `yaml:"artifacts"`
}

// derivedName is the only shape of a variable reference NFR4 lets a generated job carry besides
// the CI_* names of the script.
var (
	derivedName  = regexp.MustCompile(`^DEVDIGEST_[A-Z0-9_]{1,32}_(BASE_URL|API_KEY|HEADER_[1-5])$`)
	imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9._-]{1,128})?@sha256:[0-9a-f]{64}$`)
)

func TestBuildGitLabJob_EveryCombination(t *testing.T) {
	providers := []struct {
		provider string
		headers  int
	}{
		{"", 0}, {"openrouter", 0}, {"openai", 0}, {"anthropic", 0},
		{"custom:my-gw", 0}, {"custom:my-gw", 3}, {"custom:a", 5},
	}
	for _, p := range providers {
		for _, runner := range []domain.Runner{"", domain.RunnerGitHubHosted, domain.RunnerSelfHosted} {
			for _, postAs := range []domain.PostAs{domain.PostAsGitHubReview, domain.PostAsPRComment, domain.PostAsNone} {
				o := domain.GitLabJobOptions{Slug: "agent-1", PostAs: postAs, Provider: p.provider, Runner: runner, HeaderCount: p.headers}
				text, err := domain.BuildGitLabJob(o)
				if err != nil {
					t.Fatalf("BuildGitLabJob(%+v) error = %v", o, err)
				}
				var doc jobDoc
				if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
					t.Fatalf("BuildGitLabJob(%+v) is not YAML: %v", o, err)
				}
				job, ok := doc[domain.GitLabJobName("agent-1")]
				if len(doc) != 1 || !ok {
					t.Fatalf("BuildGitLabJob(%+v) keys = %v, want the one job %q", o, doc, domain.GitLabJobName("agent-1"))
				}
				if job.Stage != "test" || len(job.Script) != 6 || job.Artifacts.When != "always" {
					t.Errorf("BuildGitLabJob(%+v) stage/script/artifacts = %q/%d/%q", o, job.Stage, len(job.Script), job.Artifacts.When)
				}
				if job.Image.Name != domain.JobImage || !imagePattern.MatchString(job.Image.Name) || len(job.Image.Entrypoint) != 1 || job.Image.Entrypoint[0] != "" {
					t.Errorf("BuildGitLabJob(%+v) image = %+v, want name %q and entrypoint [\"\"]", o, job.Image, domain.JobImage)
				}
				wantTags := []string(nil)
				if runner == domain.RunnerSelfHosted {
					wantTags = []string{"devdigest"}
				}
				if diff := cmp.Diff(wantTags, job.Tags); diff != "" {
					t.Errorf("BuildGitLabJob(%+v) tags mismatch (-want +got):\n%s", o, diff)
				}
				if job.Variables["DEVDIGEST_POST_AS"] != string(postAs) || job.Variables["DEVDIGEST_AGENT"] != "agent-1" ||
					job.Variables["DEVDIGEST_PLATFORM"] != "gitlab_ci" || job.Variables["GIT_DEPTH"] != "0" {
					t.Errorf("BuildGitLabJob(%+v) variables = %v", o, job.Variables)
				}
				custom := domain.IsCustomProvider(p.provider)
				wantVars := 4
				if custom {
					wantVars += 2 + p.headers
				}
				if len(job.Variables) != wantVars {
					t.Errorf("BuildGitLabJob(%+v) has %d variables, want %d: %v", o, len(job.Variables), wantVars, job.Variables)
				}
				for k, v := range job.Variables {
					if strings.HasPrefix(k, "DEVDIGEST_LLM_") && !derivedName.MatchString(strings.TrimPrefix(v, "$")) {
						t.Errorf("BuildGitLabJob(%+v) %s = %q, want a derived name", o, k, v)
					}
				}
				for _, secret := range []string{"OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "TOKEN"} {
					if strings.Contains(text, secret) {
						t.Errorf("BuildGitLabJob(%+v) mentions %s; built-in keys are read by the runner from the project variables", o, secret)
					}
				}
			}
		}
	}
}

func TestBuildGitLabJob_Invalid(t *testing.T) {
	ok := domain.GitLabJobOptions{Slug: "a", PostAs: domain.PostAsNone}
	tests := []struct {
		name string
		edit func(o *domain.GitLabJobOptions)
	}{
		{"slug with a newline", func(o *domain.GitLabJobOptions) { o.Slug = "a\nb" }},
		{"slug with a dollar", func(o *domain.GitLabJobOptions) { o.Slug = "a$b" }},
		{"empty slug", func(o *domain.GitLabJobOptions) { o.Slug = "" }},
		{"unknown post_as", func(o *domain.GitLabJobOptions) { o.PostAs = "x" }},
		{"empty post_as", func(o *domain.GitLabJobOptions) { o.PostAs = "" }},
		{"unknown runner", func(o *domain.GitLabJobOptions) { o.Runner = "circle" }},
		{"unknown provider", func(o *domain.GitLabJobOptions) { o.Provider = "gemini" }},
		{"malformed custom provider", func(o *domain.GitLabJobOptions) { o.Provider = "custom:A_B" }},
		{"six headers", func(o *domain.GitLabJobOptions) { o.Provider = "custom:a"; o.HeaderCount = 6 }},
		{"negative headers", func(o *domain.GitLabJobOptions) { o.Provider = "custom:a"; o.HeaderCount = -1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := ok
			tc.edit(&o)
			got, err := domain.BuildGitLabJob(o)
			if !errors.Is(err, domain.ErrInvalidWorkflowOptions) || got != "" {
				t.Errorf("BuildGitLabJob(%+v) = %q, %v, want \"\" and ErrInvalidWorkflowOptions", o, got, err)
			}
		})
	}
}

func TestJobImage(t *testing.T) {
	if !imagePattern.MatchString(domain.JobImage) {
		t.Errorf("JobImage = %q, want <name>[:<tag>]@sha256:<64 hex> (FR8)", domain.JobImage)
	}
	if !strings.HasPrefix(domain.JobImage, "buildpack-deps:trixie-scm@sha256:") {
		t.Errorf("JobImage = %q, want the buildpack-deps trixie-scm image (D19, SCR9)", domain.JobImage)
	}
}

func TestGitLabNames(t *testing.T) {
	if got, want := domain.GitLabJobName("my-gw"), "devdigest-review-my-gw"; got != want {
		t.Errorf("GitLabJobName = %q, want %q", got, want)
	}
	if got, want := domain.GitLabJobPath("my-gw"), ".gitlab/devdigest/review-my-gw.yml"; got != want {
		t.Errorf("GitLabJobPath = %q, want %q", got, want)
	}
	if got, want := domain.IncludeSnippet("my-gw"), "include:\n  - local: .gitlab/devdigest/review-my-gw.yml\n"; got != want {
		t.Errorf("IncludeSnippet = %q, want %q", got, want)
	}
	wantRoot := "# Added by DevDigest. Add your own jobs below or in other included files.\n" +
		"include:\n  - local: .gitlab/devdigest/review-my-gw.yml\n"
	if got := domain.BuildRootInclude("my-gw"); got != wantRoot {
		t.Errorf("BuildRootInclude = %q, want %q", got, wantRoot)
	}
	if domain.TargetGitLabCI != "gitlab_ci" || domain.RunSourceGitLab != "gitlab_ci" {
		t.Errorf("TargetGitLabCI = %q, RunSourceGitLab = %q, want gitlab_ci", domain.TargetGitLabCI, domain.RunSourceGitLab)
	}
}

func TestGitLabRequiredSecrets(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		keyed    bool
		headers  int
		postAs   domain.PostAs
		want     []string
	}{
		{"openrouter with posting", "openrouter", false, 0, domain.PostAsGitHubReview, []string{"OPENROUTER_API_KEY", "DEVDIGEST_GITLAB_TOKEN"}},
		{"empty provider is openrouter", "", false, 0, domain.PostAsPRComment, []string{"OPENROUTER_API_KEY", "DEVDIGEST_GITLAB_TOKEN"}},
		{"openai without posting", "openai", false, 0, domain.PostAsNone, []string{"OPENAI_API_KEY"}},
		{"anthropic without posting", "anthropic", false, 0, domain.PostAsNone, []string{"ANTHROPIC_API_KEY"}},
		{"custom with key and 2 headers", "custom:my-gw", true, 2, domain.PostAsGitHubReview,
			[]string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2", "DEVDIGEST_GITLAB_TOKEN"}},
		{"custom without key, no posting", "custom:my-gw", false, 0, domain.PostAsNone, []string{}},
		{"invalid provider", "gemini", true, 0, domain.PostAsGitHubReview, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.GitLabRequiredSecrets(tc.provider, tc.keyed, tc.headers, tc.postAs)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GitLabRequiredSecrets(%q, %v, %d, %q) mismatch (-want +got):\n%s", tc.provider, tc.keyed, tc.headers, tc.postAs, diff)
			}
		})
	}
}

func TestTriggersNote(t *testing.T) {
	all := domain.Triggers()
	tests := []struct {
		name string
		in   []domain.Trigger
		want string
	}{
		{"nil is the default", nil, ""},
		{"all three", all, ""},
		{"all three reordered with a duplicate", []domain.Trigger{domain.TriggerReopened, domain.TriggerOpened, domain.TriggerSynchronize, domain.TriggerOpened}, ""},
		{"only synchronize", []domain.Trigger{domain.TriggerSynchronize}, domain.TriggersNoteText},
		{"two of three", []domain.Trigger{domain.TriggerOpened, domain.TriggerReopened}, domain.TriggersNoteText},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.TriggersNote(tc.in); got != tc.want {
				t.Errorf("TriggersNote(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if domain.TriggersNoteText != "GitLab runs merge request pipelines on every push; trigger choices do not apply" {
		t.Errorf("TriggersNoteText = %q, want the FR4 text", domain.TriggersNoteText)
	}
}
