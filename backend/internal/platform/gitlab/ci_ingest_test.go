package gitlab_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/gitlab"
)

func TestPipelines(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"id": 31, "project_id": 7, "ref": "refs/merge-requests/5/head", "sha": ciSHA,
			"source": "merge_request_event", "status": "success", "created_at": "2026-10-27T10:00:00.000Z"},
		map[string]any{"id": 30, "project_id": 8, "ref": "main", "sha": ciSHA, "source": "push", "status": "failed"},
	}))
	got, err := s.client(gitlab.Options{}).Pipelines(t.Context(), s.conn(), 7, "merge_request_event", 1, 30)
	if err != nil {
		t.Fatalf("Pipelines() error = %v", err)
	}
	want := []gitlab.Pipeline{
		{ID: 31, ProjectID: 7, Ref: "refs/merge-requests/5/head", SHA: ciSHA, Source: "merge_request_event", Status: "success",
			CreatedAt: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)},
		{ID: 30, ProjectID: 8, Ref: "main", SHA: ciSHA, Source: "push", Status: "failed"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Pipelines() mismatch (-want +got):\n%s", diff)
	}
	r := s.req(0)
	u, _ := url.ParseRequestURI(r.URI)
	wantQ := url.Values{"source": {"merge_request_event"}, "order_by": {"id"}, "sort": {"desc"}, "per_page": {"30"}, "page": {"1"}}
	if r.Method != "GET" || u.Path != "/api/v4/projects/7/pipelines" || cmp.Diff(wantQ, u.Query()) != "" {
		t.Errorf("request = %s %s, want GET …/pipelines with %v", r.Method, r.URI, wantQ)
	}
	if r.Header.Get("PRIVATE-TOKEN") != testToken {
		t.Errorf("PRIVATE-TOKEN = %q, want the token", r.Header.Get("PRIVATE-TOKEN"))
	}
}

func TestPipelines_RefusesBadArgumentsWithoutRequest(t *testing.T) {
	s := newStub(t, okJSON([]any{}))
	c := s.client(gitlab.Options{})
	calls := []func() error{
		func() error { _, err := c.Pipelines(t.Context(), s.conn(), 0, "", 1, 30); return err },
		func() error { _, err := c.Pipelines(t.Context(), s.conn(), 7, "a&b=c", 1, 30); return err },
		func() error { _, err := c.Pipelines(t.Context(), s.conn(), 7, "", 0, 30); return err },
		func() error { _, err := c.Pipelines(t.Context(), s.conn(), 7, "", 1, 101); return err },
	}
	for i, call := range calls {
		if call() == nil {
			t.Errorf("bad Pipelines call %d succeeded", i)
		}
	}
	if s.count() != 0 {
		t.Errorf("%d requests for invalid input, want 0", s.count())
	}
}

func TestPipelines_ZeroIDIsNotAPI(t *testing.T) {
	s := newStub(t, okJSON([]any{map[string]any{"id": 0}}))
	if _, err := s.client(gitlab.Options{}).Pipelines(t.Context(), s.conn(), 7, "", 1, 30); err == nil {
		t.Error("Pipelines() accepted a pipeline without an id")
	}
}

func TestPipelineJobs(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"id": 501, "name": "devdigest-review-x", "status": "success", "created_at": "2026-10-27T10:01:00.000Z",
			"artifacts_file":      map[string]any{"filename": "artifacts.zip", "size": 1234},
			"artifacts_expire_at": "2026-11-26T10:01:00.000Z"},
		map[string]any{"id": 502, "name": "other", "status": "failed", "artifacts_file": nil, "artifacts_expire_at": nil},
	}))
	got, err := s.client(gitlab.Options{}).PipelineJobs(t.Context(), s.conn(), 7, 31, "success", "failed")
	if err != nil {
		t.Fatalf("PipelineJobs() error = %v", err)
	}
	exp := time.Date(2026, 11, 26, 10, 1, 0, 0, time.UTC)
	want := []gitlab.Job{
		{ID: 501, Name: "devdigest-review-x", Status: "success", CreatedAt: time.Date(2026, 10, 27, 10, 1, 0, 0, time.UTC),
			ArtifactsFile: &gitlab.ArtifactsFile{Filename: "artifacts.zip", Size: 1234}, ArtifactsExpireAt: &exp},
		{ID: 502, Name: "other", Status: "failed"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PipelineJobs() mismatch (-want +got):\n%s", diff)
	}
	u, _ := url.ParseRequestURI(s.req(0).URI)
	wantQ := url.Values{"scope[]": {"success", "failed"}, "per_page": {"100"}, "page": {"1"}}
	if u.Path != "/api/v4/projects/7/pipelines/31/jobs" || cmp.Diff(wantQ, u.Query()) != "" {
		t.Errorf("request URI = %q, want …/pipelines/31/jobs with %v", s.req(0).URI, wantQ)
	}
}

func TestPipelineJobs_RefusesBadArgumentsWithoutRequest(t *testing.T) {
	s := newStub(t, okJSON([]any{}))
	c := s.client(gitlab.Options{})
	if _, err := c.PipelineJobs(t.Context(), s.conn(), 7, 0); err == nil {
		t.Error("PipelineJobs(pipeline 0) succeeded")
	}
	if _, err := c.PipelineJobs(t.Context(), s.conn(), 0, 1); err == nil {
		t.Error("PipelineJobs(project 0) succeeded")
	}
	if _, err := c.PipelineJobs(t.Context(), s.conn(), 7, 1, "success&x=1"); err == nil {
		t.Error("PipelineJobs(bad scope) succeeded")
	}
	if s.count() != 0 {
		t.Errorf("%d requests for invalid input, want 0", s.count())
	}
}
