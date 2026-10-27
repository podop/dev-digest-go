package domain

import (
	"net/url"
	"strconv"
	"strings"
)

// GitLabPlatform is the value of DEVDIGEST_PLATFORM that selects GitLab mode (spec FR16). Any
// other value, or none, leaves the runner in its GitHub Actions behaviour.
const GitLabPlatform = "gitlab_ci"

// The posting modes of DEVDIGEST_POST_AS (Phase 6 7.8.3). A value that is none of them counts as
// PostAsReview, as in GitHub mode.
const (
	PostAsReview  = "github_review"
	PostAsComment = "pr_comment"
	PostAsNone    = "none"
)

// Hard-failure texts of the GitLab environment (spec FR16, FR18, FR19). They never carry a
// variable value.
const (
	msgGitLabNotMRPipeline = "This job must run in a GitLab merge request pipeline"
	msgGitLabFork          = "Merge requests from forks are not reviewed"
	msgGitLabTokenMissing  = "DEVDIGEST_GITLAB_TOKEN is not set: add it as a masked, not protected CI/CD variable (scope api), or set post_as to none"
)

// GitLabEnv is the validated environment of a GitLab merge request pipeline (spec 7.5). Title,
// Description, ProjectPath, ProjectDir and TLSCAFile are untrusted text: whatever is printed from
// them goes through GitLabLogText.
type GitLabEnv struct {
	// MRIID is the merge request iid (CI_MERGE_REQUEST_IID), the artifact's pr_number.
	MRIID int64
	// ProjectID is CI_PROJECT_ID: the API project and the artifact's repository_id. The fork
	// guard has proved it equals CI_MERGE_REQUEST_PROJECT_ID and the source project.
	ProjectID int64
	// ProjectPath is CI_PROJECT_PATH, the artifact's repository.
	ProjectPath string
	// BaseSHA is CI_MERGE_REQUEST_DIFF_BASE_SHA; CommitSHA is CI_COMMIT_SHA (the artifact's
	// commit_sha); SourceBranchSHA is CI_MERGE_REQUEST_SOURCE_BRANCH_SHA, "" outside
	// merged-results pipelines.
	BaseSHA, CommitSHA, SourceBranchSHA string
	// JobID is CI_JOB_ID, the artifact's run_id.
	JobID int64
	// ProjectDir is CI_PROJECT_DIR, "" when unset (the caller uses its working directory).
	ProjectDir string
	// APIURL is CI_API_V4_URL without a trailing slash; "" when PostAs is PostAsNone.
	APIURL string
	// TLSCAFile is CI_SERVER_TLS_CA_FILE, "" when unset.
	TLSCAFile string
	// Title and Description are CI_MERGE_REQUEST_TITLE and _DESCRIPTION, "" when unset.
	Title, Description string
	// PostAs is one of PostAsReview, PostAsComment, PostAsNone.
	PostAs string
	// Token is DEVDIGEST_GITLAB_TOKEN; it is read only when PostAs is not PostAsNone, else "".
	Token string
}

// HeadSHA is the commit the diff ends at: CI_MERGE_REQUEST_SOURCE_BRANCH_SHA when non-empty (a
// merged-results pipeline), else CI_COMMIT_SHA (spec FR20).
func (e GitLabEnv) HeadSHA() string {
	if e.SourceBranchSHA != "" {
		return e.SourceBranchSHA
	}
	return e.CommitSHA
}

// PRDescription is the description the review sees: CI_MERGE_REQUEST_DESCRIPTION when it is not
// blank, else "" (spec FR21).
func (e GitLabEnv) PRDescription() string {
	if strings.TrimSpace(e.Description) == "" {
		return ""
	}
	return e.Description
}

// GitLabModeRequested reports whether DEVDIGEST_PLATFORM selects GitLab mode (spec FR16).
func GitLabModeRequested(lookup func(string) (string, bool)) bool {
	v, ok := lookup("DEVDIGEST_PLATFORM")
	return ok && v == GitLabPlatform
}

// ParseGitLabEnv reads and validates the GitLab variables of spec 7.5 through lookup (os.LookupEnv
// has the shape) and nothing else; the DEVDIGEST_* directory and agent variables and the provider
// variables stay with the caller. It fails, in this order, with a *RunnerError when
//   - GITLAB_CI is not "true" or CI_PIPELINE_SOURCE is not "merge_request_event" (FR16);
//   - CI_MERGE_REQUEST_IID, CI_PROJECT_ID, CI_MERGE_REQUEST_PROJECT_ID or
//     CI_MERGE_REQUEST_SOURCE_PROJECT_ID is not a positive integer (FR17);
//   - the merge request or its source project is not CI_PROJECT_ID (the fork guard, FR18);
//   - CI_PROJECT_PATH is empty, CI_JOB_ID is not a positive integer, or a SHA is not 40 or 64
//     lower-case hex digits (FR17); CI_MERGE_REQUEST_SOURCE_BRANCH_SHA may be empty;
//   - DEVDIGEST_POST_AS is not "none" and DEVDIGEST_GITLAB_TOKEN is empty (FR19), or
//     CI_API_V4_URL is not an https URL without userinfo, query or fragment (FR19).
//
// With DEVDIGEST_POST_AS = "none" the token and the API URL are not read. The messages name the
// variable and never print its value.
func ParseGitLabEnv(lookup func(string) (string, bool)) (GitLabEnv, error) {
	get := func(key string) string { v, _ := lookup(key); return v }

	if get("GITLAB_CI") != "true" || get("CI_PIPELINE_SOURCE") != "merge_request_event" {
		return GitLabEnv{}, NewRunnerError(msgGitLabNotMRPipeline)
	}

	var e GitLabEnv
	var mrProject, srcProject int64
	for _, f := range []struct {
		name string
		dst  *int64
	}{
		{"CI_MERGE_REQUEST_IID", &e.MRIID},
		{"CI_PROJECT_ID", &e.ProjectID},
		{"CI_MERGE_REQUEST_PROJECT_ID", &mrProject},
		{"CI_MERGE_REQUEST_SOURCE_PROJECT_ID", &srcProject},
	} {
		n, ok := positiveInt(get(f.name))
		if !ok {
			return GitLabEnv{}, invalidGitLabVar(f.name)
		}
		*f.dst = n
	}
	if srcProject != mrProject || e.ProjectID != mrProject {
		return GitLabEnv{}, NewRunnerError(msgGitLabFork)
	}

	e.ProjectPath = get("CI_PROJECT_PATH")
	if e.ProjectPath == "" {
		return GitLabEnv{}, invalidGitLabVar("CI_PROJECT_PATH")
	}
	jobID, ok := positiveInt(get("CI_JOB_ID"))
	if !ok {
		return GitLabEnv{}, invalidGitLabVar("CI_JOB_ID")
	}
	e.JobID = jobID
	for _, f := range []struct {
		name     string
		dst      *string
		optional bool
	}{
		{"CI_MERGE_REQUEST_DIFF_BASE_SHA", &e.BaseSHA, false},
		{"CI_COMMIT_SHA", &e.CommitSHA, false},
		{"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", &e.SourceBranchSHA, true},
	} {
		v := get(f.name)
		if v == "" && f.optional {
			continue
		}
		if !validGitLabSHA(v) {
			return GitLabEnv{}, invalidGitLabVar(f.name)
		}
		*f.dst = v
	}

	e.ProjectDir = get("CI_PROJECT_DIR")
	e.TLSCAFile = get("CI_SERVER_TLS_CA_FILE")
	e.Title = get("CI_MERGE_REQUEST_TITLE")
	e.Description = get("CI_MERGE_REQUEST_DESCRIPTION")

	e.PostAs = get("DEVDIGEST_POST_AS")
	if e.PostAs != PostAsComment && e.PostAs != PostAsNone {
		e.PostAs = PostAsReview
	}
	if e.PostAs == PostAsNone {
		return e, nil
	}

	token := get("DEVDIGEST_GITLAB_TOKEN")
	if strings.TrimSpace(token) == "" {
		return GitLabEnv{}, NewRunnerError(msgGitLabTokenMissing)
	}
	if !visibleASCII(token) {
		return GitLabEnv{}, invalidGitLabVar("DEVDIGEST_GITLAB_TOKEN")
	}
	e.Token = token

	apiURL, ok := cleanAPIURL(get("CI_API_V4_URL"))
	if !ok {
		return GitLabEnv{}, NewRunnerError("CI_API_V4_URL must be an https URL without credentials, query or fragment")
	}
	e.APIURL = apiURL
	return e, nil
}

func invalidGitLabVar(name string) *RunnerError {
	return NewRunnerError(name + " is missing or invalid")
}

// positiveInt parses a decimal positive integer of ASCII digits no larger than JavaScript's
// Number.MAX_SAFE_INTEGER (the artifact carries these as JSON numbers).
func positiveInt(s string) (int64, bool) {
	if s == "" || len(s) > 16 {
		return 0, false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > maxSafeInteger {
		return 0, false
	}
	return n, true
}

// validGitLabSHA accepts a full commit id: 40 (SHA-1) or 64 (SHA-256) lower-case hex digits.
func validGitLabSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// visibleASCII reports whether s holds only printable, non-space ASCII: all a token can hold, and
// nothing that could split an HTTP header.
func visibleASCII(s string) bool {
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// cleanAPIURL accepts an https URL with a host and without userinfo, query or fragment (spec
// FR19) and returns it without a trailing slash.
func cleanAPIURL(raw string) (string, bool) {
	if raw == "" || strings.ContainsAny(raw, "?#") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	return strings.TrimRight(raw, "/"), true
}
