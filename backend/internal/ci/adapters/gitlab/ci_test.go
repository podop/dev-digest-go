package gitlab_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

const (
	resultFile = "devdigest-result.json"
	signature  = "X-Amz-Signature=SECRETSIGNATURE0123"
)

func TestCI_Pipelines(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"id": 31, "project_id": 7, "ref": "refs/merge-requests/5/head", "sha": sha,
			"source": "merge_request_event", "status": "success", "created_at": "2026-10-27T10:00:00.000Z"},
		map[string]any{"id": 30, "project_id": 8, "ref": "main", "sha": sha, "source": "push", "status": "failed"},
	}))
	got, err := s.reader(nil).Pipelines(t.Context(), ref, "merge_request_event", 1, 30)
	if err != nil {
		t.Fatalf("Pipelines() error = %v", err)
	}
	want := []domain.GitLabPipeline{
		{ID: 31, ProjectID: 7, SHA: sha, Ref: "refs/merge-requests/5/head", Source: "merge_request_event"},
		{ID: 30, ProjectID: 8, SHA: sha, Ref: "main", Source: "push"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Pipelines() mismatch (-want +got):\n%s", diff)
	}
	u, _ := url.ParseRequestURI(s.req(0).URI)
	if u.Path != "/api/v4/projects/7/pipelines" || u.Query().Get("source") != "merge_request_event" || u.Query().Get("per_page") != "30" {
		t.Errorf("request = %s, want the project's merge request pipelines, 30 per page", s.req(0).URI)
	}
}

func TestCI_PipelineJobs(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"id": 501, "name": "devdigest-review-sec", "status": "success", "created_at": "2026-10-27T10:00:05.250Z",
			"artifacts_file": map[string]any{"filename": "artifacts.zip", "size": 1234}, "artifacts_expire_at": "2026-11-26T10:00:05.000Z"},
		map[string]any{"id": 502, "name": "test", "status": "failed"},
	}))
	got, err := s.reader(nil).PipelineJobs(t.Context(), ref, 31, "success", "failed")
	if err != nil {
		t.Fatalf("PipelineJobs() error = %v", err)
	}
	want := []domain.GitLabJob{
		{ID: 501, Name: "devdigest-review-sec", Status: "success", CreatedAt: "2026-10-27T10:00:05.25Z",
			ArtifactsExpireAt: "2026-11-26T10:00:05Z", ArtifactsFile: &domain.GitLabArtifactsFile{Size: 1234}},
		{ID: 502, Name: "test", Status: "failed"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PipelineJobs() mismatch (-want +got):\n%s", diff)
	}
	for _, j := range got {
		if j.CreatedAt != "" {
			if _, ok := domain.ParseGitHubTime(j.CreatedAt); !ok {
				t.Errorf("CreatedAt %q is not read by the domain's time parser", j.CreatedAt)
			}
		}
	}
	u, _ := url.ParseRequestURI(s.req(0).URI)
	if u.Path != "/api/v4/projects/7/pipelines/31/jobs" || strings.Join(u.Query()["scope[]"], ",") != "success,failed" {
		t.Errorf("request = %s, want the jobs of pipeline 31 scoped to success and failed", s.req(0).URI)
	}
}

func TestCI_MergeRequestSourceProject(t *testing.T) {
	mr := func(extra map[string]any) map[string]any {
		m := map[string]any{"iid": 5, "title": "t", "state": "opened", "source_branch": "f", "target_branch": "main",
			"source_project_id": 7, "target_project_id": 7}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name string
		body map[string]any
		want int64
	}{
		{"same project", mr(nil), 7},
		{"fork", mr(map[string]any{"source_project_id": 99}), 0},
		{"no source project", mr(map[string]any{"source_project_id": nil}), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, okJSON(tc.body))
			got, err := s.reader(nil).MergeRequestSourceProject(t.Context(), ref, 5)
			if err != nil || got != tc.want {
				t.Errorf("MergeRequestSourceProject() = %d, %v, want %d", got, err, tc.want)
			}
			if r := s.req(0); r.URI != "/api/v4/projects/7/merge_requests/5" {
				t.Errorf("request = %s, want the merge request of the project", r.URI)
			}
		})
	}
}

// readerCalls runs every CI method once.
func readerCalls(c interface {
	Pipelines(context.Context, app.GitLabRef, string, int, int) ([]domain.GitLabPipeline, error)
	PipelineJobs(context.Context, app.GitLabRef, int64, ...string) ([]domain.GitLabJob, error)
	MergeRequestSourceProject(context.Context, app.GitLabRef, int) (int64, error)
	JobArtifactFile(context.Context, app.GitLabRef, int64, string, int) ([]byte, error)
}) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"pipelines": func(ctx context.Context) error {
			_, err := c.Pipelines(ctx, ref, "merge_request_event", 1, 30)
			return err
		},
		"jobs": func(ctx context.Context) error {
			_, err := c.PipelineJobs(ctx, ref, 31, "success")
			return err
		},
		"merge request": func(ctx context.Context) error {
			_, err := c.MergeRequestSourceProject(ctx, ref, 5)
			return err
		},
		"artifact": func(ctx context.Context) error {
			_, err := c.JobArtifactFile(ctx, ref, 501, resultFile, 4096)
			return err
		},
	}
}

func TestCI_StatusMapsToGitLabError(t *testing.T) {
	for _, code := range []int{401, 403, 404, 429, 500} {
		s := newStub(t, status(code, "boom "+testToken))
		for name, call := range readerCalls(s.reader(nil)) {
			t.Run(name+"/"+http.StatusText(code), func(t *testing.T) {
				ge := gitLabError(t, call(t.Context()))
				if ge.Status != code || ge.Transport || ge.Step != "" {
					t.Errorf("%s answered %d: error = %+v, want Status %d, no Step, no Transport", name, code, ge, code)
				}
				assertNoToken(t, "error text", ge.Error()+ge.Cause.Error())
			})
		}
	}
}

func TestCI_RedirectIsRefusedExceptTheArtifactStorageHop(t *testing.T) {
	other := newStub(t, okJSON(map[string]any{}))
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", other.URL+"/steal?"+signature)
		w.WriteHeader(http.StatusFound)
	})
	for name, call := range readerCalls(s.reader(other.Client())) {
		if name == "artifact" {
			continue // a 302 with an https Location is the storage hop, tested below
		}
		t.Run(name, func(t *testing.T) {
			err := call(t.Context())
			ge := gitLabError(t, err)
			if ge.Status != 0 || ge.Transport || !errors.Is(err, glclient.ErrRedirect) {
				t.Errorf("%s redirected: error = %+v, want Status 0, ErrRedirect", name, ge)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error %q shows the redirect target", err)
			}
		})
	}
	if other.count() != 0 {
		t.Errorf("redirect target saw %d requests, want 0", other.count())
	}
}

func TestCI_ConnectionVanished(t *testing.T) {
	tests := map[string]glclient.ResolveFunc{
		"no connection": fixedResolve(glclient.Binding{}, false, nil),
		"token removed": fixedResolve(glclient.Binding{BaseURL: "https://unused", Token: ""}, true, nil),
	}
	for name, resolve := range tests {
		s := newStub(t, okJSON(map[string]any{}))
		c := newReader(s, withBase(resolve, s.URL))
		for call, f := range readerCalls(c) {
			t.Run(name+"/"+call, func(t *testing.T) {
				if ge := gitLabError(t, f(t.Context())); ge.Status != 401 || ge.Step != "" {
					t.Errorf("error = %+v, want Status 401", ge)
				}
			})
		}
		if s.count() != 0 {
			t.Errorf("%s: %d requests were sent, want none", name, s.count())
		}
	}
}

func TestCI_ArtifactDirect(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	got, err := s.reader(nil).JobArtifactFile(t.Context(), ref, 501, resultFile, 4096)
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("JobArtifactFile() = %q, %v, want the body", got, err)
	}
	if r := s.req(0); r.Method != "GET" || r.URI != "/api/v4/projects/7/jobs/501/artifacts/devdigest-result.json" ||
		r.Header.Get("PRIVATE-TOKEN") != testToken {
		t.Errorf("request = %s %s, want GET of the job's result file with the token", r.Method, r.URI)
	}
}

func TestCI_ArtifactStorageHopCarriesNoCredential(t *testing.T) {
	storage := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"from":"storage"}`) })
	api := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", storage.URL+"/blob?"+signature)
		w.WriteHeader(http.StatusFound)
	})
	got, err := api.reader(storage.Client()).JobArtifactFile(t.Context(), ref, 501, resultFile, 4096)
	if err != nil || string(got) != `{"from":"storage"}` {
		t.Fatalf("JobArtifactFile() = %q, %v, want the storage body", got, err)
	}
	if api.count() != 1 || storage.count() != 1 {
		t.Fatalf("api saw %d requests, storage %d, want 1 and 1", api.count(), storage.count())
	}
	r := storage.req(0)
	if r.URI != "/blob?"+signature {
		t.Errorf("storage URI = %q, want the Location as given", r.URI)
	}
	for _, h := range []string{"Private-Token", "Authorization", "Cookie", "Job-Token"} {
		if v := r.Header.Get(h); v != "" {
			t.Errorf("storage request carries %s = %q, want no credential", h, v)
		}
	}
	if strings.Contains(r.URI+r.Body, testToken) {
		t.Error("the token reached the storage")
	}
}

func TestCI_ArtifactStorageErrorsNeverShowTheSignedURL(t *testing.T) {
	storage := newStub(t, status(403, "denied "+signature))
	api := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", storage.URL+"/blob?"+signature)
		w.WriteHeader(http.StatusFound)
	})
	err := func() error {
		_, err := api.reader(storage.Client()).JobArtifactFile(t.Context(), ref, 501, resultFile, 4096)
		return err
	}()
	ge := gitLabError(t, err)
	if ge.Status != 403 || ge.Transport {
		t.Errorf("error = %+v, want Status 403", ge)
	}
	for _, text := range []string{ge.Error(), ge.Cause.Error()} {
		for _, secret := range []string{"SECRETSIGNATURE", "X-Amz", "/blob"} {
			if strings.Contains(text, secret) {
				t.Errorf("error text %q contains %q (part of the signed storage URL)", text, secret)
			}
		}
	}
}

func TestCI_ArtifactRefusedRedirects(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		location string
	}{
		{"plain http location", http.StatusFound, "http://storage.example/blob?" + signature},
		{"relative location", http.StatusFound, "/elsewhere"},
		{"no location", http.StatusFound, ""},
		{"301", http.StatusMovedPermanently, "https://storage.example/blob?" + signature},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.code)
			})
			_, err := api.reader(nil).JobArtifactFile(t.Context(), ref, 501, resultFile, 4096)
			ge := gitLabError(t, err)
			if ge.Status != 0 || ge.Transport || !errors.Is(err, glclient.ErrRedirect) {
				t.Errorf("error = %+v (%v), want Status 0 and ErrRedirect", ge, err)
			}
			if strings.Contains(err.Error()+ge.Cause.Error(), "SECRETSIGNATURE") {
				t.Errorf("error %q shows the redirect target", err)
			}
		})
	}
}

func TestCI_ArtifactSecondRedirectIsRefused(t *testing.T) {
	storage := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://third.example/x?"+signature)
		w.WriteHeader(http.StatusFound)
	})
	api := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", storage.URL+"/blob")
		w.WriteHeader(http.StatusFound)
	})
	_, err := api.reader(storage.Client()).JobArtifactFile(t.Context(), ref, 501, resultFile, 4096)
	if ge := gitLabError(t, err); ge.Status != 0 || !errors.Is(err, glclient.ErrRedirect) {
		t.Errorf("error = %+v (%v), want Status 0 and ErrRedirect", ge, err)
	}
}

func TestCI_ArtifactLimit(t *testing.T) {
	const limit = 100
	for _, tc := range []struct {
		name    string
		size    int
		wantLen int
	}{
		{"under the limit", 60, 60},
		{"exactly the limit", limit, limit},
		{"over the limit", limit + 50, limit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, strings.Repeat("a", tc.size)) })
			got, err := s.reader(nil).JobArtifactFile(t.Context(), ref, 501, resultFile, limit)
			if err != nil || len(got) != tc.wantLen {
				t.Errorf("JobArtifactFile(limit %d) of %d bytes = %d bytes, %v, want %d bytes", limit, tc.size, len(got), err, tc.wantLen)
			}
		})
	}
}

func TestCI_ArtifactTransportFailure(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{}))
	c := s.reader(nil)
	s.Close()
	ge := gitLabError(t, readerCalls(c)["artifact"](t.Context()))
	if !ge.Transport || ge.Status != 0 {
		t.Errorf("error = %+v, want Transport and Status 0", ge)
	}
}
