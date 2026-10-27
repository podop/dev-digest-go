package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/adapters/glapi"
	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
)

const glToken = "glpat-0123456789abcdef"

var glRef = runnerapp.MRRef{ProjectID: "42", IID: 7}

type glRequest struct {
	Method, Path, Token, Body string
}

// glAdapter is the adapter over a real glapi client talking to a TLS stub; it returns the stub's
// request log.
func glAdapter(t *testing.T, h func(w http.ResponseWriter, r glRequest)) (runnerapp.GitLab, *[]glRequest) {
	t.Helper()
	var seen []glRequest
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		req := glRequest{Method: r.Method, Path: r.URL.Path, Token: r.Header.Get("PRIVATE-TOKEN"), Body: string(b)}
		seen = append(seen, req)
		h(w, req)
	}))
	t.Cleanup(srv.Close)
	c, err := glapi.New(glapi.Options{APIURL: srv.URL + "/api/v4", Transport: srv.Client().Transport, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("glapi.New() error = %v", err)
	}
	return gitLabAdapter{c: c}, &seen
}

func TestGitLabAdapter_MR(t *testing.T) {
	gl, seen := glAdapter(t, func(w http.ResponseWriter, _ glRequest) {
		_, _ = io.WriteString(w, `{"iid":7,"diff_refs":{"base_sha":"b","start_sha":"s","head_sha":"h"}}`)
	})
	got, err := gl.MR(t.Context(), glToken, glRef)
	if err != nil {
		t.Fatalf("MR() error = %v", err)
	}
	want := runnerapp.MergeRequest{IID: 7, DiffRefs: &runnerapp.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("MR() mismatch (-want +got):\n%s", diff)
	}
	wantReq := []glRequest{{Method: "GET", Path: "/api/v4/projects/42/merge_requests/7", Token: glToken}}
	if diff := cmp.Diff(wantReq, *seen); diff != "" {
		t.Errorf("MR() requests mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLabAdapter_MR_WithoutDiffRefs(t *testing.T) {
	gl, _ := glAdapter(t, func(w http.ResponseWriter, _ glRequest) { _, _ = io.WriteString(w, `{"iid":7,"diff_refs":null}`) })
	got, err := gl.MR(t.Context(), glToken, glRef)
	if err != nil {
		t.Fatalf("MR() error = %v", err)
	}
	if got.DiffRefs != nil {
		t.Errorf("MR().DiffRefs = %+v, want nil", got.DiffRefs)
	}
}

func TestGitLabAdapter_ListDrafts(t *testing.T) {
	gl, _ := glAdapter(t, func(w http.ResponseWriter, _ glRequest) { _, _ = io.WriteString(w, `[{"id":3},{"id":9}]`) })
	got, err := gl.ListDrafts(t.Context(), glToken, glRef)
	if err != nil {
		t.Fatalf("ListDrafts() error = %v", err)
	}
	if diff := cmp.Diff([]runnerapp.Draft{{ID: 3}, {ID: 9}}, got); diff != "" {
		t.Errorf("ListDrafts() mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLabAdapter_CreateDraft_MapsThePosition(t *testing.T) {
	gl, seen := glAdapter(t, func(w http.ResponseWriter, _ glRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":11}`)
	})
	got, err := gl.CreateDraft(t.Context(), glToken, glRef, runnerapp.NewDraft{
		Note: "careful",
		Position: &runnerapp.DraftPosition{
			Refs:    runnerapp.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"},
			NewPath: "new.go", OldPath: "old.go", NewLine: 12, OldLine: 10,
		},
	})
	if err != nil || got.ID != 11 {
		t.Fatalf("CreateDraft() = %+v, %v; want ID 11, nil", got, err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte((*seen)[0].Body), &body); err != nil {
		t.Fatalf("request body %q: %v", (*seen)[0].Body, err)
	}
	want := map[string]any{
		"note": "careful",
		"position": map[string]any{
			"position_type": "text", "base_sha": "b", "start_sha": "s", "head_sha": "h",
			"new_path": "new.go", "old_path": "old.go", "new_line": float64(12), "old_line": float64(10),
		},
	}
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("CreateDraft() request body mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLabAdapter_CreateDraft_Summary(t *testing.T) {
	gl, seen := glAdapter(t, func(w http.ResponseWriter, _ glRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":5}`)
	})
	if _, err := gl.CreateDraft(t.Context(), glToken, glRef, runnerapp.NewDraft{Note: "summary"}); err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	if strings.Contains((*seen)[0].Body, "position") {
		t.Errorf("summary draft body = %s, want no position", (*seen)[0].Body)
	}
}

// A 400 on a positioned draft is what the use case retries without the position (FR26): it must
// arrive as a *runnerapp.GitLabAPIError with the status and the complete job-log text.
func TestGitLabAdapter_ErrorMapping(t *testing.T) {
	gl, _ := glAdapter(t, func(w http.ResponseWriter, _ glRequest) {
		http.Error(w, "line_code is invalid", http.StatusBadRequest)
	})
	pos := &runnerapp.DraftPosition{Refs: runnerapp.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"}, NewPath: "a.go", OldPath: "a.go", NewLine: 1}
	calls := []struct {
		name string
		call func() error
	}{
		{"CreateDraft", func() error {
			_, err := gl.CreateDraft(t.Context(), glToken, glRef, runnerapp.NewDraft{Note: "n", Position: pos})
			return err
		}},
		{"MR", func() error { _, err := gl.MR(t.Context(), glToken, glRef); return err }},
		{"ListDrafts", func() error { _, err := gl.ListDrafts(t.Context(), glToken, glRef); return err }},
		{"DeleteDraft", func() error { return gl.DeleteDraft(t.Context(), glToken, glRef, 3) }},
		{"PublishDraft", func() error { return gl.PublishDraft(t.Context(), glToken, glRef, 3) }},
		{"BulkPublish", func() error { return gl.BulkPublish(t.Context(), glToken, glRef) }},
		{"CreateNote", func() error { return gl.CreateNote(t.Context(), glToken, glRef, "hi") }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			var apiErr *runnerapp.GitLabAPIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("%s() error = %T %v, want *runnerapp.GitLabAPIError", tc.name, err, err)
			}
			if apiErr.Status != http.StatusBadRequest {
				t.Errorf("%s() Status = %d, want 400", tc.name, apiErr.Status)
			}
			if !strings.Contains(apiErr.Message, "GitLab API error") || !strings.Contains(apiErr.Message, "400") || !strings.Contains(apiErr.Message, "line_code is invalid") {
				t.Errorf("%s() Message = %q, want the job-log text with the status and body", tc.name, apiErr.Message)
			}
			if strings.Contains(apiErr.Message, glToken) {
				t.Errorf("%s() Message holds the token: %q", tc.name, apiErr.Message)
			}
			var glErr *glapi.APIError
			if errors.As(err, &glErr) {
				t.Errorf("%s() error still wraps a *glapi.APIError", tc.name)
			}
		})
	}
}

func TestGitLabAdapter_Writes(t *testing.T) {
	gl, seen := glAdapter(t, func(w http.ResponseWriter, _ glRequest) { w.WriteHeader(http.StatusOK) })
	ctx := t.Context()
	for _, err := range []error{
		gl.DeleteDraft(ctx, glToken, glRef, 3),
		gl.PublishDraft(ctx, glToken, glRef, 4),
		gl.BulkPublish(ctx, glToken, glRef),
		gl.CreateNote(ctx, glToken, glRef, "hi"),
	} {
		if err != nil {
			t.Fatalf("write error = %v", err)
		}
	}
	var got []string
	for _, r := range *seen {
		got = append(got, r.Method+" "+strings.TrimPrefix(r.Path, "/api/v4/projects/42/merge_requests/7"))
	}
	want := []string{"DELETE /draft_notes/3", "PUT /draft_notes/4/publish", "POST /draft_notes/bulk_publish", "POST /notes"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("write requests mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLabAdapter_TransportErrorPassesThrough(t *testing.T) {
	// a reference the client refuses is not an API answer
	gl, _ := glAdapter(t, func(http.ResponseWriter, glRequest) { t.Error("no request expected") })
	_, err := gl.MR(t.Context(), glToken, runnerapp.MRRef{ProjectID: "../x", IID: 7})
	if err == nil {
		t.Fatal("MR() error = nil, want a refusal")
	}
	var apiErr *runnerapp.GitLabAPIError
	if errors.As(err, &apiErr) {
		t.Errorf("MR() error = %v, want a plain error, not a *GitLabAPIError", err)
	}
}

func TestAppError_Nil(t *testing.T) {
	if err := appError(nil); err != nil {
		t.Errorf("appError(nil) = %v, want nil", err)
	}
}

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestGitLabDeps_PostAsNoneHasNoClient(t *testing.T) {
	gl, diff := gitLabDeps(envOf(map[string]string{"DEVDIGEST_POST_AS": "none", "CI_PROJECT_DIR": t.TempDir()}))
	if gl != nil {
		t.Errorf("gitLabDeps(post_as none) GitLab = %T, want nil", gl)
	}
	if diff == nil {
		t.Error("gitLabDeps(post_as none) GitDiff = nil, want the checkout differ")
	}
}

func TestGitLabDeps_Client(t *testing.T) {
	gl, diff := gitLabDeps(envOf(map[string]string{"CI_API_V4_URL": "https://gitlab.example/api/v4", "CI_PROJECT_DIR": t.TempDir()}))
	if _, ok := gl.(gitLabAdapter); !ok {
		t.Errorf("gitLabDeps() GitLab = %T, want the glapi adapter", gl)
	}
	if diff == nil {
		t.Error("gitLabDeps() GitDiff = nil")
	}
}

func TestGitLabDeps_UnusableClientFailsAtTheFirstCall(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"http url":   {"CI_API_V4_URL": "http://gitlab.example/api/v4"},
		"missing CA": {"CI_API_V4_URL": "https://gitlab.example/api/v4", "CI_SERVER_TLS_CA_FILE": "/nonexistent/ca.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			gl, _ := gitLabDeps(envOf(env))
			if gl == nil {
				t.Fatal("gitLabDeps() GitLab = nil, want a port that fails")
			}
			_, err := gl.MR(context.Background(), glToken, glRef)
			if err == nil || !strings.Contains(err.Error(), "CI_") {
				t.Errorf("MR() error = %v, want the reason naming the variable", err)
			}
			if err := gl.CreateNote(context.Background(), glToken, glRef, "x"); err == nil {
				t.Error("CreateNote() error = nil, want the reason")
			}
		})
	}
}

// GitLab mode prints no GitHub workflow command; GitHub mode still brackets the log with the stop
// and resume lines (FR29, security r1 F2).
func TestRunProcess_StopCommandsOnlyInGitHubMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform map[string]string
		wantStop bool
	}{
		{"github", nil, true},
		{"gitlab", map[string]string{"DEVDIGEST_PLATFORM": "gitlab_ci"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// no manifest directory: the run fails early and deterministically, which is enough to see
			// what the process prints around it
			env := map[string]string{"DEVDIGEST_DIR": t.TempDir() + "/missing", "DEVDIGEST_POST_AS": "none"}
			for k, v := range tc.platform {
				env[k] = v
			}
			var stdout, stderr bytes.Buffer
			code := runProcess(t.Context(), envOf(env), &stdout, &stderr, endpoints{})
			if code != runnerapp.ExitFailed {
				t.Fatalf("runProcess() = %d, want %d; stderr %q", code, runnerapp.ExitFailed, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Error("stderr is empty, want the failure line (the run must have reached the use case)")
			}
			out := stdout.String()
			if got := strings.Contains(out, "::stop-commands::"); got != tc.wantStop {
				t.Errorf("runProcess() stdout = %q; stop-commands line = %t, want %t", out, got, tc.wantStop)
			}
			if !tc.wantStop && out != "" {
				// the run printed nothing but its own lines; none may be a workflow command
				for line := range strings.SplitSeq(out, "\n") {
					if strings.HasPrefix(line, "::") {
						t.Errorf("GitLab stdout line %q starts like a workflow command", line)
					}
				}
			}
			if tc.wantStop && !strings.HasSuffix(strings.TrimRight(out, "\n"), "::") {
				t.Errorf("GitHub stdout = %q, want the resume line last", out)
			}
		})
	}
}
