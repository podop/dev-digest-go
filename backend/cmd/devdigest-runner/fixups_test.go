package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// glEnvFor is the environment of a merge request pipeline in w's checkout.
func glEnvFor(w *workspace, extra map[string]string) {
	for k, v := range map[string]string{
		"DEVDIGEST_PLATFORM": "gitlab_ci", "GITLAB_CI": "true", "CI_PIPELINE_SOURCE": "merge_request_event",
		"CI_MERGE_REQUEST_IID": "7", "CI_PROJECT_ID": "42", "CI_MERGE_REQUEST_PROJECT_ID": "42",
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID": "42", "CI_PROJECT_PATH": "grp/proj", "CI_JOB_ID": "900",
		"CI_MERGE_REQUEST_DIFF_BASE_SHA": strings.Repeat("a", 40), "CI_COMMIT_SHA": strings.Repeat("b", 40),
		"CI_PROJECT_DIR":    w.root,
		"DEVDIGEST_POST_AS": "github_review", "DEVDIGEST_GITLAB_TOKEN": "glpat-0123456789abcdef",
		"CI_API_V4_URL": "https://gl.example/api/v4",
	} {
		w.env[k] = v
	}
	for k, v := range extra {
		w.env[k] = v
	}
}

// A GitLab client that cannot be built is an environment failure: it fails before the diff is
// read and before the paid model call, names the variable, keeps the token out of the log and
// leaves no artifact. With post_as none no client is needed and the review runs.
func TestRunProcess_GitLabUnbuildableClientFailsBeforeTheModelCall(t *testing.T) {
	tests := []struct {
		name  string
		extra func(root string) map[string]string
		want  string // substring of the FAILED line; "" = the run goes on
	}{
		{"unreadable CA file", func(root string) map[string]string {
			return map[string]string{"CI_SERVER_TLS_CA_FILE": filepath.Join(root, "missing-ca.pem")}
		}, "[agent-runner] FAILED: CI_SERVER_TLS_CA_FILE: "},
		{"API URL the client refuses", func(string) map[string]string {
			return map[string]string{"CI_API_V4_URL": "https://gl.example/api/../v4"}
		}, "[agent-runner] FAILED: CI_API_V4_URL has dot segments in its path"},
		{"post_as none reads neither", func(root string) map[string]string {
			return map[string]string{
				"DEVDIGEST_POST_AS": "none", "DEVDIGEST_GITLAB_TOKEN": "",
				"CI_SERVER_TLS_CA_FILE": filepath.Join(root, "missing-ca.pem"), "CI_API_V4_URL": "https://gl.example/api/../v4",
			}
		}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorkspace(t, testManifest, nil)
			glEnvFor(w, tc.extra(w.root))

			var modelCalls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				modelCalls.Add(1)
			}))
			t.Cleanup(model.Close)

			var stdout, stderr bytes.Buffer
			code := runProcess(t.Context(), w.lookup, &stdout, &stderr, endpoints{OpenRouter: model.URL + "/v1", GitHub: model.URL})

			if tc.want == "" {
				// The run goes on: it reaches the diff (this checkout is no git repository, so it
				// stops there) without ever needing a GitLab client.
				if !strings.Contains(stderr.String(), "Could not compute the merge request diff") {
					t.Errorf("runProcess() stderr = %q, want the run to reach the diff", stderr.String())
				}
				return
			}
			if code != 1 {
				t.Errorf("runProcess() = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.want)
			}
			if strings.Contains(stderr.String()+stdout.String(), "glpat-0123456789abcdef") {
				t.Errorf("the token reached the job log: stdout %q stderr %q", stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), "trace") || strings.Contains(stderr.String(), "diff") {
				t.Errorf("the run went past the environment checks: stdout %q stderr %q", stdout.String(), stderr.String())
			}
			if n := modelCalls.Load(); n != 0 {
				t.Errorf("model endpoint received %d requests, want none", n)
			}
			if art, ok := w.artifact(t); ok {
				t.Errorf("artifact = %q, want none", art)
			}
		})
	}
}
