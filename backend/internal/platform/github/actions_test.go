package github_test

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/github"
)

func TestListWorkflowRuns(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"workflow_runs": []any{
			map[string]any{
				"id": 11, "run_attempt": 2, "head_sha": "abc", "path": ".github/workflows/devdigest-review-x.yml@refs/pull/3/merge",
				"event": "pull_request", "conclusion": "success", "html_url": "https://github.com/acme/api/actions/runs/11",
				"repository": map[string]any{"id": 99}, "head_repository": map[string]any{"id": 77}, "pull_requests": []any{map[string]any{"number": 3}, map[string]any{"number": 4}},
				"created_at": "2026-10-20T08:00:00Z",
			},
			map[string]any{"id": 12, "head_sha": "def", "conclusion": nil, "created_at": "garbage"},
		}})
	})
	got, err := newClient(s, github.Options{}).ListWorkflowRuns(t.Context(), "acme", "api", "devdigest-review-x.yml")
	if err != nil {
		t.Fatalf("ListWorkflowRuns() error: %v", err)
	}
	want := []github.WorkflowRun{
		{ID: 11, RunAttempt: 2, HeadSHA: "abc", Path: ".github/workflows/devdigest-review-x.yml@refs/pull/3/merge", Event: "pull_request",
			Conclusion: "success", HTMLURL: "https://github.com/acme/api/actions/runs/11", RepositoryID: 99, HeadRepositoryID: 77, PullNumbers: []int{3, 4},
			CreatedAt: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)},
		{ID: 12, RunAttempt: 1, HeadSHA: "def"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListWorkflowRuns() mismatch (-want +got):\n%s", diff)
	}
	wantRequest(t, s, 0, http.MethodGet, "/repos/acme/api/actions/workflows/devdigest-review-x.yml/runs",
		map[string][]string{"event": {"pull_request"}, "status": {"completed"}, "per_page": {"30"}})
}

func TestListWorkflowRuns_missingWorkflowIs404(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "57")
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	})
	_, err := newClient(s, github.Options{}).ListWorkflowRuns(t.Context(), "acme", "api", "w.yml")
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != 404 || se.RateLimitRemaining != "57" {
		t.Errorf("ListWorkflowRuns() error = %v, want a 404 StatusError with remaining 57", err)
	}
}

func TestListRunArtifacts(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"artifacts": []any{
			map[string]any{"id": 5, "name": "devdigest-result-x", "size_in_bytes": 321, "expired": false, "created_at": "2026-10-20T08:01:00Z"},
			map[string]any{"id": 6, "name": "other", "size_in_bytes": 1, "expired": true},
		}})
	})
	got, err := newClient(s, github.Options{}).ListRunArtifacts(t.Context(), "acme", "api", 11)
	if err != nil {
		t.Fatalf("ListRunArtifacts() error: %v", err)
	}
	want := []github.RunArtifact{
		{ID: 5, Name: "devdigest-result-x", SizeBytes: 321, CreatedAt: time.Date(2026, 10, 20, 8, 1, 0, 0, time.UTC)},
		{ID: 6, Name: "other", SizeBytes: 1, Expired: true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListRunArtifacts() mismatch (-want +got):\n%s", diff)
	}
	wantRequest(t, s, 0, http.MethodGet, "/repos/acme/api/actions/runs/11/artifacts", map[string][]string{"per_page": {"100"}})
}

// storage is an https artifact store that records what it was sent.
type storage struct {
	*httptest.Server
	hits atomic.Int32
	auth atomic.Value
}

func newStorage(t *testing.T, h http.HandlerFunc) *storage {
	t.Helper()
	st := &storage{}
	st.auth.Store("")
	st.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.hits.Add(1)
		st.auth.Store(r.Header.Get("Authorization"))
		h(w, r)
	}))
	t.Cleanup(st.Close)
	return st
}

func TestDownloadArtifact_redirectToStorageWithoutCredentials(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("z", 1000)
	st := newStorage(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, st.URL+"/blob?sig=SECRET", http.StatusFound)
	})
	c := newClient(s, github.Options{ArtifactClient: st.Client()})
	got, err := c.DownloadArtifact(t.Context(), "acme", "api", 5, 1000)
	if err != nil {
		t.Fatalf("DownloadArtifact() error: %v", err)
	}
	if string(got) != body {
		t.Errorf("DownloadArtifact() returned %d bytes, want %d", len(got), len(body))
	}
	wantRequest(t, s, 0, http.MethodGet, "/repos/acme/api/actions/artifacts/5/zip", nil)
	if a := st.auth.Load(); a != "" {
		t.Errorf("storage received Authorization %q, want none", a)
	}
	if got := st.hits.Load(); got != 1 {
		t.Errorf("storage hits = %d, want 1", got)
	}
}

func TestDownloadArtifact_directBodyAndCap(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("a", 11))) })
	c := newClient(s, github.Options{})
	if got, err := c.DownloadArtifact(t.Context(), "o", "r", 1, 11); err != nil || len(got) != 11 {
		t.Errorf("DownloadArtifact(max 11) = %d bytes, %v; want 11, nil", len(got), err)
	}
	if _, err := c.DownloadArtifact(t.Context(), "o", "r", 1, 10); !errors.Is(err, github.ErrArtifactTooLarge) {
		t.Errorf("DownloadArtifact(max 10) error = %v, want ErrArtifactTooLarge", err)
	}
	if _, err := c.DownloadArtifact(t.Context(), "o", "r", 1, 0); err == nil {
		t.Error("DownloadArtifact(max 0) = nil error, want one")
	}
}

func TestDownloadArtifact_storageBodyIsCappedAtMaxPlusOne(t *testing.T) {
	t.Parallel()
	st := newStorage(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("q", 2048))) })
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, st.URL, http.StatusSeeOther) })
	c := newClient(s, github.Options{ArtifactClient: st.Client()})
	if _, err := c.DownloadArtifact(t.Context(), "o", "r", 1, 1024); !errors.Is(err, github.ErrArtifactTooLarge) {
		t.Errorf("DownloadArtifact() error = %v, want ErrArtifactTooLarge", err)
	}
}

func TestDownloadArtifact_refusedRedirects(t *testing.T) {
	t.Parallel()
	plain := newStub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) })
	st := newStorage(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) })
	again := newStorage(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, st.URL, http.StatusFound) })
	tests := []struct {
		name   string
		status int
		loc    string
	}{
		{"http target", 302, plain.URL + "/x"},
		{"missing Location", 302, ""},
		{"301 is not followed", 301, st.URL},
		{"308 is not followed", 308, st.URL},
		{"credentials in the URL", 302, "https://user:pw@" + strings.TrimPrefix(st.URL, "https://")},
		{"relative target", 302, "/elsewhere"},
		{"second redirect", 302, again.URL},
	}
	for _, tc := range tests {
		s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
			if tc.loc != "" {
				w.Header().Set("Location", tc.loc)
			}
			w.WriteHeader(tc.status)
		})
		c := newClient(s, github.Options{ArtifactClient: st.Client()})
		if _, err := c.DownloadArtifact(t.Context(), "o", "r", 1, 100); !errors.Is(err, github.ErrUnsafeRedirect) {
			t.Errorf("%s: DownloadArtifact() error = %v, want ErrUnsafeRedirect", tc.name, err)
		}
	}
	if got := st.hits.Load(); got != 0 {
		t.Errorf("storage hits = %d, want 0 (nothing was followed)", got)
	}
}

func TestDownloadArtifact_statusErrors(t *testing.T) {
	t.Parallel()
	gone := newStorage(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	tests := []struct {
		name   string
		handle http.HandlerFunc
		want   int
	}{
		{"expired", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 410, map[string]any{"message": "gone"}) }, 410},
		{"missing", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 404, map[string]any{"message": "nf"}) }, 404},
		{"storage refuses", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, gone.URL, http.StatusFound) }, 403},
	}
	for _, tc := range tests {
		s := newStub(t, tc.handle)
		c := newClient(s, github.Options{ArtifactClient: gone.Client()})
		_, err := c.DownloadArtifact(t.Context(), "o", "r", 1, 100)
		var se *github.StatusError
		if !errors.As(err, &se) || se.Status != tc.want {
			t.Errorf("%s: DownloadArtifact() error = %v, want status %d", tc.name, err, tc.want)
		}
	}
}

func TestDownloadArtifact_retriesTransientFirstLeg(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			writeJSON(w, 503, map[string]any{"message": "later"})
			return
		}
		_, _ = w.Write([]byte("zip"))
	})
	got, err := newClient(s, github.Options{}).DownloadArtifact(t.Context(), "o", "r", 1, 10)
	if err != nil || string(got) != "zip" {
		t.Errorf("DownloadArtifact() = %q, %v; want zip, nil", got, err)
	}
}

func TestDownloadArtifact_storageErrorHidesSignedURLAndToken(t *testing.T) {
	t.Parallel()
	st := newStorage(t, func(w http.ResponseWriter, r *http.Request) {})
	target := st.URL + "/blob?sig=SECRET-SIGNATURE"
	st.Close() // the redirect now points at a dead host
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) })
	c := newClient(s, github.Options{ArtifactClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{}}}})
	_, err := c.DownloadArtifact(t.Context(), "o", "r", 1, 10)
	if err == nil {
		t.Fatal("DownloadArtifact() = nil error, want a transport error")
	}
	for _, leak := range []string{"SECRET-SIGNATURE", testToken} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q contains %q", err, leak)
		}
	}
}

func TestDownloadArtifact_noTokenSendsNothing(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {})
	c := newClient(s, github.Options{Token: func() string { return "" }})
	if _, err := c.DownloadArtifact(t.Context(), "o", "r", 1, 10); !errors.Is(err, github.ErrNoToken) {
		t.Errorf("DownloadArtifact() error = %v, want ErrNoToken", err)
	}
	if n := s.count(); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}
