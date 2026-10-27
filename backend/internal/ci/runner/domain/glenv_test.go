package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/domain"
)

const (
	shaA = "0123456789abcdef0123456789abcdef01234567"
	shaB = "fedcba9876543210fedcba9876543210fedcba98"
	sha2 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func glEnv(overrides map[string]string) func(string) (string, bool) {
	env := map[string]string{
		"GITLAB_CI":                          "true",
		"CI_PIPELINE_SOURCE":                 "merge_request_event",
		"CI_MERGE_REQUEST_IID":               "7",
		"CI_PROJECT_ID":                      "42",
		"CI_MERGE_REQUEST_PROJECT_ID":        "42",
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "42",
		"CI_PROJECT_PATH":                    "group/sub/project",
		"CI_MERGE_REQUEST_DIFF_BASE_SHA":     shaA,
		"CI_COMMIT_SHA":                      shaB,
		"CI_JOB_ID":                          "9001",
		"CI_PROJECT_DIR":                     "/builds/group/sub/project",
		"CI_API_V4_URL":                      "https://gitlab.example.com/api/v4",
		"CI_SERVER_TLS_CA_FILE":              "/etc/ca.pem",
		"CI_MERGE_REQUEST_TITLE":             "Fix it",
		"CI_MERGE_REQUEST_DESCRIPTION":       "  ",
		"DEVDIGEST_POST_AS":                  "github_review",
		"DEVDIGEST_GITLAB_TOKEN":             "glpat-secret-token-value",
	}
	for k, v := range overrides {
		if v == glUnset {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

const glUnset = "\x00unset"

func TestParseGitLabEnv_OK(t *testing.T) {
	got, err := domain.ParseGitLabEnv(glEnv(nil))
	if err != nil {
		t.Fatalf("ParseGitLabEnv() error = %v", err)
	}
	want := domain.GitLabEnv{
		MRIID: 7, ProjectID: 42, ProjectPath: "group/sub/project",
		BaseSHA: shaA, CommitSHA: shaB, JobID: 9001,
		ProjectDir: "/builds/group/sub/project", APIURL: "https://gitlab.example.com/api/v4",
		TLSCAFile: "/etc/ca.pem", Title: "Fix it", Description: "  ",
		PostAs: "github_review", Token: "glpat-secret-token-value",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ParseGitLabEnv() mismatch (-want +got):\n%s", diff)
	}
	if got.HeadSHA() != shaB {
		t.Errorf("HeadSHA() = %q, want CI_COMMIT_SHA %q", got.HeadSHA(), shaB)
	}
	if got.PRDescription() != "" {
		t.Errorf("PRDescription() = %q, want empty for a blank description", got.PRDescription())
	}
}

func TestParseGitLabEnv_Variants(t *testing.T) {
	t.Run("merged results pipeline: head is the source branch sha", func(t *testing.T) {
		got, err := domain.ParseGitLabEnv(glEnv(map[string]string{"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": sha2}))
		if err != nil {
			t.Fatal(err)
		}
		if got.HeadSHA() != sha2 || got.CommitSHA != shaB {
			t.Errorf("HeadSHA() = %q, CommitSHA = %q, want %q, %q", got.HeadSHA(), got.CommitSHA, sha2, shaB)
		}
	})
	t.Run("empty source branch sha counts as unset", func(t *testing.T) {
		got, err := domain.ParseGitLabEnv(glEnv(map[string]string{"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": ""}))
		if err != nil || got.HeadSHA() != shaB {
			t.Errorf("ParseGitLabEnv() = head %q, err %v, want %q, nil", got.HeadSHA(), err, shaB)
		}
	})
	t.Run("optional variables may be unset", func(t *testing.T) {
		got, err := domain.ParseGitLabEnv(glEnv(map[string]string{
			"CI_PROJECT_DIR": glUnset, "CI_SERVER_TLS_CA_FILE": glUnset,
			"CI_MERGE_REQUEST_TITLE": glUnset, "CI_MERGE_REQUEST_DESCRIPTION": glUnset,
		}))
		if err != nil || got.ProjectDir != "" || got.TLSCAFile != "" || got.Title != "" || got.PRDescription() != "" {
			t.Errorf("ParseGitLabEnv() = %+v, err %v, want empty optionals", got, err)
		}
	})
	t.Run("non-blank description is kept", func(t *testing.T) {
		got, _ := domain.ParseGitLabEnv(glEnv(map[string]string{"CI_MERGE_REQUEST_DESCRIPTION": "Why\n"}))
		if got.PRDescription() != "Why\n" {
			t.Errorf("PRDescription() = %q, want %q", got.PRDescription(), "Why\n")
		}
	})
	t.Run("sha-256 object ids", func(t *testing.T) {
		if _, err := domain.ParseGitLabEnv(glEnv(map[string]string{"CI_COMMIT_SHA": sha2})); err != nil {
			t.Errorf("ParseGitLabEnv() with a 64-digit sha: %v", err)
		}
	})
	t.Run("API URL keeps a prefix and loses a trailing slash", func(t *testing.T) {
		got, err := domain.ParseGitLabEnv(glEnv(map[string]string{"CI_API_V4_URL": "https://git.example.com:8443/gl/api/v4/"}))
		if err != nil || got.APIURL != "https://git.example.com:8443/gl/api/v4" {
			t.Errorf("APIURL = %q, err %v", got.APIURL, err)
		}
	})
	t.Run("unknown post_as counts as github_review", func(t *testing.T) {
		got, err := domain.ParseGitLabEnv(glEnv(map[string]string{"DEVDIGEST_POST_AS": "bogus"}))
		if err != nil || got.PostAs != domain.PostAsReview {
			t.Errorf("PostAs = %q, err %v, want %q", got.PostAs, err, domain.PostAsReview)
		}
	})
	t.Run("pr_comment needs the token too", func(t *testing.T) {
		_, err := domain.ParseGitLabEnv(glEnv(map[string]string{"DEVDIGEST_POST_AS": "pr_comment", "DEVDIGEST_GITLAB_TOKEN": glUnset}))
		if err == nil {
			t.Errorf("ParseGitLabEnv(pr_comment, no token) error = nil")
		}
	})
}

func TestParseGitLabEnv_PostAsNoneReadsNoToken(t *testing.T) {
	got, err := domain.ParseGitLabEnv(glEnv(map[string]string{
		"DEVDIGEST_POST_AS": "none", "DEVDIGEST_GITLAB_TOKEN": "glpat-present", "CI_API_V4_URL": "http://insecure.example/api/v4",
	}))
	if err != nil {
		t.Fatalf("ParseGitLabEnv(none) error = %v, want none: no token and no API URL are needed", err)
	}
	if got.Token != "" || got.APIURL != "" || got.PostAs != domain.PostAsNone {
		t.Errorf("ParseGitLabEnv(none) = token %q, api %q, post_as %q, want empty, empty, none", got.Token, got.APIURL, got.PostAs)
	}
	if _, err := domain.ParseGitLabEnv(glEnv(map[string]string{"DEVDIGEST_POST_AS": "none", "DEVDIGEST_GITLAB_TOKEN": glUnset, "CI_API_V4_URL": glUnset})); err != nil {
		t.Errorf("ParseGitLabEnv(none, nothing else) error = %v", err)
	}
}

func TestParseGitLabEnv_Failures(t *testing.T) {
	const notMR = "This job must run in a GitLab merge request pipeline"
	const fork = "Merge requests from forks are not reviewed"
	const noToken = "DEVDIGEST_GITLAB_TOKEN is not set: add it as a masked, not protected CI/CD variable (scope api), or set post_as to none"
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"GITLAB_CI unset", map[string]string{"GITLAB_CI": glUnset}, notMR},
		{"GITLAB_CI not true", map[string]string{"GITLAB_CI": "TRUE"}, notMR},
		{"branch pipeline", map[string]string{"CI_PIPELINE_SOURCE": "push"}, notMR},
		{"pipeline source unset", map[string]string{"CI_PIPELINE_SOURCE": glUnset}, notMR},
		{"fork: source project differs", map[string]string{"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "43"}, fork},
		{"fork: run in the parent project", map[string]string{"CI_PROJECT_ID": "41"}, fork},
		{"fork: MR project differs", map[string]string{"CI_MERGE_REQUEST_PROJECT_ID": "43"}, fork},
		{"fork wins over a missing token", map[string]string{"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "43", "DEVDIGEST_GITLAB_TOKEN": glUnset}, fork},
		{"iid unset", map[string]string{"CI_MERGE_REQUEST_IID": glUnset}, "CI_MERGE_REQUEST_IID is missing or invalid"},
		{"iid zero", map[string]string{"CI_MERGE_REQUEST_IID": "0"}, "CI_MERGE_REQUEST_IID is missing or invalid"},
		{"iid negative", map[string]string{"CI_MERGE_REQUEST_IID": "-1"}, "CI_MERGE_REQUEST_IID is missing or invalid"},
		{"iid plus sign", map[string]string{"CI_MERGE_REQUEST_IID": "+7"}, "CI_MERGE_REQUEST_IID is missing or invalid"},
		{"iid not a number", map[string]string{"CI_MERGE_REQUEST_IID": "7a"}, "CI_MERGE_REQUEST_IID is missing or invalid"},
		{"iid too large", map[string]string{"CI_MERGE_REQUEST_IID": "9007199254740992"}, "CI_MERGE_REQUEST_IID is missing or invalid"},
		{"iid overflow", map[string]string{"CI_MERGE_REQUEST_IID": strings.Repeat("9", 40)}, "CI_MERGE_REQUEST_IID is missing or invalid"},
		{"project id invalid", map[string]string{"CI_PROJECT_ID": "x"}, "CI_PROJECT_ID is missing or invalid"},
		{"MR project id unset", map[string]string{"CI_MERGE_REQUEST_PROJECT_ID": glUnset}, "CI_MERGE_REQUEST_PROJECT_ID is missing or invalid"},
		{"source project id unset", map[string]string{"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": glUnset}, "CI_MERGE_REQUEST_SOURCE_PROJECT_ID is missing or invalid"},
		{"project path empty", map[string]string{"CI_PROJECT_PATH": ""}, "CI_PROJECT_PATH is missing or invalid"},
		{"job id invalid", map[string]string{"CI_JOB_ID": "0"}, "CI_JOB_ID is missing or invalid"},
		{"base sha unset", map[string]string{"CI_MERGE_REQUEST_DIFF_BASE_SHA": glUnset}, "CI_MERGE_REQUEST_DIFF_BASE_SHA is missing or invalid"},
		{"base sha upper case", map[string]string{"CI_MERGE_REQUEST_DIFF_BASE_SHA": strings.ToUpper(shaA)}, "CI_MERGE_REQUEST_DIFF_BASE_SHA is missing or invalid"},
		{"base sha short", map[string]string{"CI_MERGE_REQUEST_DIFF_BASE_SHA": "abc123"}, "CI_MERGE_REQUEST_DIFF_BASE_SHA is missing or invalid"},
		{"base sha 41 digits", map[string]string{"CI_MERGE_REQUEST_DIFF_BASE_SHA": shaA + "0"}, "CI_MERGE_REQUEST_DIFF_BASE_SHA is missing or invalid"},
		{"base sha option-like", map[string]string{"CI_MERGE_REQUEST_DIFF_BASE_SHA": "--output=/tmp/x" + strings.Repeat("0", 25)}, "CI_MERGE_REQUEST_DIFF_BASE_SHA is missing or invalid"},
		{"commit sha invalid", map[string]string{"CI_COMMIT_SHA": "HEAD"}, "CI_COMMIT_SHA is missing or invalid"},
		{"source branch sha invalid when set", map[string]string{"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": "main"}, "CI_MERGE_REQUEST_SOURCE_BRANCH_SHA is missing or invalid"},
		{"token unset", map[string]string{"DEVDIGEST_GITLAB_TOKEN": glUnset}, noToken},
		{"token empty", map[string]string{"DEVDIGEST_GITLAB_TOKEN": ""}, noToken},
		{"token blank", map[string]string{"DEVDIGEST_GITLAB_TOKEN": " \t"}, noToken},
		{"token with a line break", map[string]string{"DEVDIGEST_GITLAB_TOKEN": "glpat-x\nX-Evil: 1"}, "DEVDIGEST_GITLAB_TOKEN is missing or invalid"},
		{"token before API URL", map[string]string{"DEVDIGEST_GITLAB_TOKEN": glUnset, "CI_API_V4_URL": glUnset}, noToken},
	}
	apiURL := "CI_API_V4_URL must be an https URL without credentials, query or fragment"
	for _, bad := range []string{
		"", "http://gitlab.example.com/api/v4", "https://user:pw@gitlab.example.com/api/v4", "https://gitlab.example.com/api/v4?x=1",
		"https://gitlab.example.com/api/v4#f", "https://gitlab.example.com/api/v4?", "https:///api/v4", "gitlab.example.com/api/v4",
		"ftp://gitlab.example.com", "https://gitlab.example.com/\x7f", "https://:@gitlab.example.com/api/v4", "https:gitlab.example.com",
	} {
		tests = append(tests, struct {
			name string
			env  map[string]string
			want string
		}{"API URL " + bad, map[string]string{"CI_API_V4_URL": bad}, apiURL})
	}
	tests = append(tests, struct {
		name string
		env  map[string]string
		want string
	}{"API URL unset", map[string]string{"CI_API_V4_URL": glUnset}, apiURL})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ParseGitLabEnv(glEnv(tc.env))
			var re *domain.RunnerError
			if !errors.As(err, &re) {
				t.Fatalf("ParseGitLabEnv() error = %v, want *RunnerError %q", err, tc.want)
			}
			if re.Message != tc.want {
				t.Errorf("ParseGitLabEnv() message = %q, want %q", re.Message, tc.want)
			}
		})
	}
}

func TestParseGitLabEnv_MessagesNeverEchoValues(t *testing.T) {
	secret := "glpat-secret-token-value"
	for _, env := range []map[string]string{
		{"CI_MERGE_REQUEST_IID": secret},
		{"CI_COMMIT_SHA": secret},
		{"CI_API_V4_URL": "http://" + secret + "@x"},
		{"DEVDIGEST_GITLAB_TOKEN": secret + "\n"},
		{"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "99"},
	} {
		_, err := domain.ParseGitLabEnv(glEnv(env))
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "99") {
			t.Errorf("ParseGitLabEnv(%v) error = %v, want one that prints no value", env, err)
		}
	}
}

func TestGitLabModeRequested(t *testing.T) {
	tests := []struct {
		value string
		set   bool
		want  bool
	}{
		{"gitlab_ci", true, true},
		{"", false, false},
		{"", true, false},
		{"GITLAB_CI", true, false},
		{"github", true, false},
	}
	for _, tc := range tests {
		lookup := func(k string) (string, bool) {
			if k != "DEVDIGEST_PLATFORM" {
				t.Errorf("GitLabModeRequested read %q", k)
			}
			return tc.value, tc.set
		}
		if got := domain.GitLabModeRequested(lookup); got != tc.want {
			t.Errorf("GitLabModeRequested(%q, set=%v) = %v, want %v", tc.value, tc.set, got, tc.want)
		}
	}
}

func TestParseGitLabEnv_ReadsOnlyDocumentedVariables(t *testing.T) {
	allowed := map[string]bool{
		"GITLAB_CI": true, "CI_PIPELINE_SOURCE": true, "CI_MERGE_REQUEST_IID": true, "CI_PROJECT_ID": true,
		"CI_MERGE_REQUEST_PROJECT_ID": true, "CI_MERGE_REQUEST_SOURCE_PROJECT_ID": true, "CI_PROJECT_PATH": true,
		"CI_MERGE_REQUEST_DIFF_BASE_SHA": true, "CI_COMMIT_SHA": true, "CI_MERGE_REQUEST_SOURCE_BRANCH_SHA": true,
		"CI_JOB_ID": true, "CI_PROJECT_DIR": true, "CI_API_V4_URL": true, "CI_SERVER_TLS_CA_FILE": true,
		"CI_MERGE_REQUEST_TITLE": true, "CI_MERGE_REQUEST_DESCRIPTION": true,
		"DEVDIGEST_POST_AS": true, "DEVDIGEST_GITLAB_TOKEN": true,
	}
	base := glEnv(nil)
	lookup := func(k string) (string, bool) {
		if !allowed[k] {
			t.Errorf("ParseGitLabEnv read undocumented variable %q", k)
		}
		return base(k)
	}
	if _, err := domain.ParseGitLabEnv(lookup); err != nil {
		t.Fatal(err)
	}
}
