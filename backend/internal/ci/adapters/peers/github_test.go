package peers_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/adapters/peers"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	ghclient "dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/retry"
)

const testToken = "ghp_SECRETTOKEN0123456789"

var repo = app.RepoRef{Owner: "acme", Name: "widgets"}

type recorded struct {
	Method, Path, RawQuery string
	Auth                   string
	Body                   map[string]any
}

// stub is a TLS GitHub: handler answers every request, requests lists what it saw.
type stub struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

func newStub(t *testing.T, handler func(s *stub, r recorded, w http.ResponseWriter)) *stub {
	t.Helper()
	s := &stub{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &rec.Body); err != nil {
				t.Errorf("request body of %s %s is not a JSON object: %v", r.Method, r.URL.Path, err)
			}
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, rec)
		s.mu.Unlock()
		handler(s, rec, w)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.reqs...)
}

func (s *stub) count(method, path string) int {
	n := 0
	for _, r := range s.requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func (s *stub) adapter(token func() string) *peers.GitHub {
	client := ghclient.New(ghclient.Options{
		Token:      token,
		BaseURL:    s.URL,
		HTTPClient: s.Client(),
		Retry:      &retry.Options{Retries: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
	})
	return peers.NewGitHub(client, token)
}

func fixedToken() string { return testToken }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const (
	pathRepo    = "/repos/acme/widgets"
	pathBranch  = pathRepo + "/git/ref/heads/devdigest/ci"
	pathBase    = pathRepo + "/git/ref/heads/main"
	pathCommits = pathRepo + "/git/commits"
)

// publishHandler is a GitHub where the branch exists (or not) and every write succeeds.
func publishHandler(branchExists bool) func(*stub, recorded, http.ResponseWriter) {
	return func(_ *stub, r recorded, w http.ResponseWriter) {
		switch {
		case r.Method == "GET" && r.Path == pathBranch:
			if !branchExists {
				writeJSON(w, 404, map[string]any{"message": "Not Found"})
				return
			}
			writeJSON(w, 200, map[string]any{"object": map[string]any{"sha": "branchhead"}})
		case r.Method == "GET" && r.Path == pathBase:
			writeJSON(w, 200, map[string]any{"object": map[string]any{"sha": "basehead"}})
		case r.Method == "GET" && strings.HasPrefix(r.Path, pathCommits+"/"):
			writeJSON(w, 200, map[string]any{"sha": strings.TrimPrefix(r.Path, pathCommits+"/"), "tree": map[string]any{"sha": "parenttree"}})
		case r.Method == "POST" && r.Path == pathRepo+"/git/blobs":
			writeJSON(w, 201, map[string]any{"sha": "blobsha"})
		case r.Method == "POST" && r.Path == pathRepo+"/git/trees":
			writeJSON(w, 201, map[string]any{"sha": "newtree"})
		case r.Method == "POST" && r.Path == pathCommits:
			writeJSON(w, 201, map[string]any{"sha": "newcommit"})
		case r.Method == "PATCH" && r.Path == pathRepo+"/git/refs/heads/devdigest/ci":
			writeJSON(w, 200, map[string]any{})
		case r.Method == "POST" && r.Path == pathRepo+"/git/refs":
			writeJSON(w, 201, map[string]any{})
		default:
			writeJSON(w, 500, map[string]any{"message": "unexpected " + r.Method + " " + r.Path})
		}
	}
}

func publishInput() app.CommitFilesInput {
	return app.CommitFilesInput{
		Branch: "devdigest/ci", Base: "main", Message: "Add DevDigest CI review",
		Files: []app.CommitFile{
			{Path: ".devdigest/agents/a.yaml", Contents: "name: a\n"},
			{Path: domain.RunnerPath, Contents: "H4sIAAAA\n", AsBlob: true},
		},
	}
}

func TestGitHub_Configured(t *testing.T) {
	t.Parallel()
	s := newStub(t, publishHandler(true))
	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{{"set", testToken, true}, {"empty", "", false}, {"blank", "  \n", false}} {
		if got := s.adapter(func() string { return tc.token }).Configured(); got != tc.want {
			t.Errorf("Configured() with %s token = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestGitHub_CommitFilesNewBranch(t *testing.T) {
	t.Parallel()
	s := newStub(t, publishHandler(false))
	if err := s.adapter(fixedToken).CommitFiles(t.Context(), repo, publishInput()); err != nil {
		t.Fatalf("CommitFiles() error: %v", err)
	}
	reqs := s.requests()
	type step struct{ Method, Path string }
	var got []step
	for _, r := range reqs {
		got = append(got, step{r.Method, r.Path})
		if r.Auth != "Bearer "+testToken {
			t.Errorf("%s %s Authorization = %q, want the bearer token", r.Method, r.Path, r.Auth)
		}
	}
	want := []step{
		{"GET", pathBranch}, {"GET", pathBase}, {"GET", pathCommits + "/basehead"},
		{"POST", pathRepo + "/git/blobs"}, {"POST", pathRepo + "/git/trees"},
		{"POST", pathCommits}, {"POST", pathRepo + "/git/refs"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("CommitFiles() request sequence mismatch (-want +got):\n%s", diff)
	}

	// The runner blob is the base64 of the .gz.b64 text (P21).
	blob := reqs[3].Body
	wantBlob := map[string]any{"content": base64.StdEncoding.EncodeToString([]byte("H4sIAAAA\n")), "encoding": "base64"}
	if diff := cmp.Diff(wantBlob, blob); diff != "" {
		t.Errorf("blob body mismatch (-want +got):\n%s", diff)
	}
	wantTree := map[string]any{
		"base_tree": "parenttree",
		"tree": []any{
			map[string]any{"path": ".devdigest/agents/a.yaml", "mode": "100644", "type": "blob", "content": "name: a\n"},
			map[string]any{"path": domain.RunnerPath, "mode": "100644", "type": "blob", "sha": "blobsha"},
		},
	}
	if diff := cmp.Diff(wantTree, reqs[4].Body); diff != "" {
		t.Errorf("tree body mismatch (-want +got):\n%s", diff)
	}
	wantCommit := map[string]any{"message": "Add DevDigest CI review", "tree": "newtree", "parents": []any{"basehead"}}
	if diff := cmp.Diff(wantCommit, reqs[5].Body); diff != "" {
		t.Errorf("commit body mismatch (-want +got):\n%s", diff)
	}
	wantRef := map[string]any{"ref": "refs/heads/devdigest/ci", "sha": "newcommit"}
	if diff := cmp.Diff(wantRef, reqs[6].Body); diff != "" {
		t.Errorf("create ref body mismatch (-want +got):\n%s", diff)
	}
}

func TestGitHub_CommitFilesExistingBranch(t *testing.T) {
	t.Parallel()
	s := newStub(t, publishHandler(true))
	if err := s.adapter(fixedToken).CommitFiles(t.Context(), repo, publishInput()); err != nil {
		t.Fatalf("CommitFiles() error: %v", err)
	}
	if n := s.count("GET", pathBase); n != 0 {
		t.Errorf("base ref was read %d times, want 0 when the branch exists", n)
	}
	reqs := s.requests()
	last := reqs[len(reqs)-1]
	if last.Method != "PATCH" || last.Path != pathRepo+"/git/refs/heads/devdigest/ci" {
		t.Fatalf("last request = %s %s, want PATCH of the branch ref", last.Method, last.Path)
	}
	if diff := cmp.Diff(map[string]any{"sha": "newcommit", "force": true}, last.Body); diff != "" {
		t.Errorf("update ref body mismatch (-want +got):\n%s", diff)
	}
	if got := s.count("GET", pathCommits+"/branchhead"); got != 1 {
		t.Errorf("parent commit read %d times, want 1 (the branch head)", got)
	}
}

func TestGitHub_CommitFilesRetriesTheWholeSequenceOnce(t *testing.T) {
	t.Parallel()
	var failures int
	var mu sync.Mutex
	base := publishHandler(false)
	s := newStub(t, func(s *stub, r recorded, w http.ResponseWriter) {
		if r.Method == "POST" && r.Path == pathCommits {
			mu.Lock()
			failures++
			first := failures == 1
			mu.Unlock()
			if first {
				writeJSON(w, 503, map[string]any{"message": "try later " + testToken})
				return
			}
		}
		base(s, r, w)
	})
	if err := s.adapter(fixedToken).CommitFiles(t.Context(), repo, publishInput()); err != nil {
		t.Fatalf("CommitFiles() error after one transient failure: %v", err)
	}
	if n := s.count("GET", pathBranch); n != 2 {
		t.Errorf("sequence started %d times, want 2 (one whole re-run)", n)
	}
	if n := s.count("POST", pathCommits); n != 2 {
		t.Errorf("commit created %d times, want 2 (the failed one is not retried on its own)", n)
	}
}

func TestGitHub_CommitFilesGivesUpAfterOneRetry(t *testing.T) {
	t.Parallel()
	base := publishHandler(false)
	s := newStub(t, func(s *stub, r recorded, w http.ResponseWriter) {
		if r.Method == "POST" && r.Path == pathRepo+"/git/trees" {
			writeJSON(w, 502, map[string]any{"message": "bad gateway " + testToken})
			return
		}
		base(s, r, w)
	})
	err := s.adapter(fixedToken).CommitFiles(t.Context(), repo, publishInput())
	var ge *app.GitHubError
	if !errors.As(err, &ge) {
		t.Fatalf("CommitFiles() error = %v, want *app.GitHubError", err)
	}
	if ge.Status != 502 || ge.Transport {
		t.Errorf("GitHubError = %+v, want Status 502", ge)
	}
	if n := s.count("POST", pathRepo+"/git/trees"); n != 2 {
		t.Errorf("tree created %d times, want 2 (first run and one re-run)", n)
	}
	assertClean(t, err)
}

func TestGitHub_CommitFilesDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()
	for _, status := range []int{403, 404, 422} {
		base := publishHandler(false)
		s := newStub(t, func(s *stub, r recorded, w http.ResponseWriter) {
			if r.Method == "POST" && r.Path == pathRepo+"/git/blobs" {
				w.Header().Set("X-RateLimit-Remaining", "0")
				writeJSON(w, status, map[string]any{"message": "denied " + testToken})
				return
			}
			base(s, r, w)
		})
		err := s.adapter(fixedToken).CommitFiles(t.Context(), repo, publishInput())
		var ge *app.GitHubError
		if !errors.As(err, &ge) || ge.Status != status || !ge.RateLimited {
			t.Errorf("status %d: CommitFiles() error = %v (%+v), want Status %d, RateLimited", status, err, ge, status)
		}
		if n := s.count("GET", pathBranch); n != 1 {
			t.Errorf("status %d: sequence started %d times, want 1", status, n)
		}
		assertClean(t, err)
	}
}

func TestGitHub_CommitFilesMissingBaseIs404(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(_ *stub, _ recorded, w http.ResponseWriter) {
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	})
	err := s.adapter(fixedToken).CommitFiles(t.Context(), repo, publishInput())
	var ge *app.GitHubError
	if !errors.As(err, &ge) || ge.Status != 404 {
		t.Fatalf("CommitFiles() error = %v, want a 404 *app.GitHubError", err)
	}
}

func TestGitHub_TransportFailure(t *testing.T) {
	t.Parallel()
	s := newStub(t, publishHandler(true))
	a := s.adapter(fixedToken)
	s.Close() // dial fails
	err := a.CommitFiles(t.Context(), repo, publishInput())
	var ge *app.GitHubError
	if !errors.As(err, &ge) || !ge.Transport || ge.Status != 0 {
		t.Fatalf("CommitFiles() error = %v (%+v), want a Transport *app.GitHubError", err, ge)
	}
	assertClean(t, err)
}

func TestGitHub_TokenVanished(t *testing.T) {
	t.Parallel()
	s := newStub(t, publishHandler(true))
	_, _, err := s.adapter(func() string { return "" }).FindOpenPR(t.Context(), repo, "devdigest/ci")
	var ge *app.GitHubError
	if !errors.As(err, &ge) || !ge.Transport || !errors.Is(err, ghclient.ErrNoToken) {
		t.Fatalf("FindOpenPR() without token error = %v, want a Transport *app.GitHubError wrapping ErrNoToken", err)
	}
	if n := len(s.requests()); n != 0 {
		t.Errorf("%d requests made without a token, want 0", n)
	}
}

func TestGitHub_PullRequests(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(_ *stub, r recorded, w http.ResponseWriter) {
		switch {
		case r.Method == "GET" && r.Path == pathRepo+"/pulls":
			if r.RawQuery == "head=acme%3Adevdigest%2Fci&per_page=1&state=open" {
				writeJSON(w, 200, []any{map[string]any{"number": 7, "html_url": "https://github.com/acme/widgets/pull/7"}})
				return
			}
			writeJSON(w, 200, []any{})
		case r.Method == "POST" && r.Path == pathRepo+"/pulls":
			writeJSON(w, 201, map[string]any{"number": 8, "html_url": "https://github.com/acme/widgets/pull/8"})
		default:
			w.WriteHeader(500)
		}
	})
	a := s.adapter(fixedToken)
	url, found, err := a.FindOpenPR(t.Context(), repo, "devdigest/ci")
	if err != nil || !found || url != "https://github.com/acme/widgets/pull/7" {
		t.Errorf("FindOpenPR() = %q, %v, %v; want the PR 7 URL, true, nil", url, found, err)
	}
	url, found, err = a.FindOpenPR(t.Context(), repo, "other")
	if err != nil || found || url != "" {
		t.Errorf("FindOpenPR(other) = %q, %v, %v; want empty, false, nil", url, found, err)
	}
	url, err = a.OpenPullRequest(t.Context(), repo, app.OpenPullRequestInput{Title: "T", Head: "devdigest/ci", Base: "main", Body: "B"})
	if err != nil || url != "https://github.com/acme/widgets/pull/8" {
		t.Errorf("OpenPullRequest() = %q, %v; want the PR 8 URL", url, err)
	}
	reqs := s.requests()
	post := reqs[len(reqs)-1]
	if diff := cmp.Diff(map[string]any{"title": "T", "head": "devdigest/ci", "base": "main", "body": "B"}, post.Body); diff != "" {
		t.Errorf("create PR body mismatch (-want +got):\n%s", diff)
	}
}

func TestGitHub_OpenPullRequestFailureIsNotRetried(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(_ *stub, _ recorded, w http.ResponseWriter) { writeJSON(w, 503, map[string]any{"message": "down"}) })
	_, err := s.adapter(fixedToken).OpenPullRequest(t.Context(), repo, app.OpenPullRequestInput{Title: "T", Head: "h", Base: "b"})
	var ge *app.GitHubError
	if !errors.As(err, &ge) || ge.Status != 503 {
		t.Fatalf("OpenPullRequest() error = %v, want Status 503", err)
	}
	if n := len(s.requests()); n != 1 {
		t.Errorf("%d requests, want 1 (a POST is not retried)", n)
	}
}

func TestGitHub_ListWorkflowRuns(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(_ *stub, r recorded, w http.ResponseWriter) {
		if r.Path == "/repos/acme/widgets/actions/workflows/devdigest-review-a.yml/runs" {
			writeJSON(w, 200, map[string]any{"workflow_runs": []any{
				map[string]any{
					"id": 11, "run_attempt": 2, "head_sha": "abc", "path": ".github/workflows/devdigest-review-a.yml",
					"event": "pull_request", "conclusion": "success", "html_url": "https://github.com/acme/widgets/actions/runs/11",
					"created_at": "2026-10-20T10:00:00Z", "repository": map[string]any{"id": 99}, "head_repository": map[string]any{"id": 77},
					"pull_requests": []any{map[string]any{"number": 5}, map[string]any{"number": 6}},
				},
				map[string]any{"id": 12, "conclusion": nil, "created_at": "garbage"},
			}})
			return
		}
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	})
	a := s.adapter(fixedToken)
	got, err := a.ListWorkflowRuns(t.Context(), repo, "devdigest-review-a.yml", domain.RunsPerInstallation)
	if err != nil {
		t.Fatalf("ListWorkflowRuns() error: %v", err)
	}
	success := "success"
	want := []domain.WorkflowRun{
		{
			ID: 11, RunAttempt: 2, HeadSHA: "abc", Path: ".github/workflows/devdigest-review-a.yml", Event: "pull_request",
			Conclusion: &success, HTMLURL: "https://github.com/acme/widgets/actions/runs/11", RepositoryID: 99, HeadRepositoryID: 77,
			PRNumbers: []int{5, 6}, CreatedAt: "2026-10-20T10:00:00Z",
		},
		{ID: 12, RunAttempt: 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListWorkflowRuns() mismatch (-want +got):\n%s", diff)
	}
	if q := s.requests()[0].RawQuery; q != "event=pull_request&per_page=30&status=completed" {
		t.Errorf("query = %q, want event=pull_request&per_page=30&status=completed", q)
	}

	_, err = a.ListWorkflowRuns(t.Context(), repo, "missing.yml", domain.RunsPerInstallation)
	var ge *app.GitHubError
	if !errors.As(err, &ge) || ge.Status != 404 {
		t.Errorf("ListWorkflowRuns(missing) error = %v, want Status 404", err)
	}
	short, err := a.ListWorkflowRuns(t.Context(), repo, "devdigest-review-a.yml", 1)
	if err != nil || len(short) != 1 {
		t.Errorf("ListWorkflowRuns(perPage 1) = %d runs, %v; want 1 run", len(short), err)
	}
}

func TestGitHub_ListRunArtifacts(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(_ *stub, _ recorded, w http.ResponseWriter) {
		writeJSON(w, 200, map[string]any{"artifacts": []any{
			map[string]any{"id": 1, "name": "devdigest-result-a", "size_in_bytes": 512, "expired": true, "created_at": "2026-10-20T10:00:00Z"},
		}})
	})
	got, err := s.adapter(fixedToken).ListRunArtifacts(t.Context(), repo, 11)
	if err != nil {
		t.Fatalf("ListRunArtifacts() error: %v", err)
	}
	want := []domain.RunArtifact{{ID: 1, Name: "devdigest-result-a", SizeInBytes: 512, Expired: true, CreatedAt: "2026-10-20T10:00:00Z"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListRunArtifacts() mismatch (-want +got):\n%s", diff)
	}
	r := s.requests()[0]
	if r.Path != pathRepo+"/actions/runs/11/artifacts" || r.RawQuery != "per_page=100" {
		t.Errorf("request = %s?%s, want .../actions/runs/11/artifacts?per_page=100", r.Path, r.RawQuery)
	}
}

func TestGitHub_DownloadArtifact(t *testing.T) {
	t.Parallel()
	const zipPath = pathRepo + "/actions/artifacts/"
	var storageAuth string
	var mu sync.Mutex
	s := newStub(t, func(s *stub, r recorded, w http.ResponseWriter) {
		switch r.Path {
		case zipPath + "1/zip", zipPath + "2/zip":
			w.Header().Set("Location", s.URL+"/storage/"+strings.TrimPrefix(r.Path, zipPath))
			w.WriteHeader(http.StatusFound)
		case "/storage/1/zip":
			mu.Lock()
			storageAuth = r.Auth
			mu.Unlock()
			_, _ = w.Write([]byte("PK-small"))
		case "/storage/2/zip":
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		case zipPath + "3/zip":
			writeJSON(w, 410, map[string]any{"message": "Gone " + testToken})
		case zipPath + "4/zip":
			w.Header().Set("Location", "http://127.0.0.1/insecure")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(404)
		}
	})
	a := s.adapter(fixedToken)

	body, err := a.DownloadArtifact(t.Context(), repo, 1, 64)
	if err != nil || string(body) != "PK-small" {
		t.Errorf("DownloadArtifact(1) = %q, %v; want PK-small", body, err)
	}
	mu.Lock()
	if storageAuth != "" {
		t.Errorf("storage leg carried Authorization %q, want none", storageAuth)
	}
	mu.Unlock()

	if _, err := a.DownloadArtifact(t.Context(), repo, 2, 64); !errors.Is(err, app.ErrArtifactTooLarge) {
		t.Errorf("DownloadArtifact(2) error = %v, want app.ErrArtifactTooLarge", err)
	}

	_, err = a.DownloadArtifact(t.Context(), repo, 3, 64)
	var ge *app.GitHubError
	if !errors.As(err, &ge) || ge.Status != 410 {
		t.Errorf("DownloadArtifact(3) error = %v, want Status 410", err)
	}
	assertClean(t, err)

	_, err = a.DownloadArtifact(t.Context(), repo, 4, 64)
	if !errors.As(err, &ge) || !ge.Transport || !errors.Is(err, ghclient.ErrUnsafeRedirect) {
		t.Errorf("DownloadArtifact(4) error = %v, want a Transport *app.GitHubError wrapping ErrUnsafeRedirect", err)
	}
	if domain.GitHubErrorCode(ge.Status, ge.RateLimited) != domain.CodeUnavailable {
		t.Errorf("an unsafe redirect maps to %q, want github_unavailable", domain.GitHubErrorCode(ge.Status, ge.RateLimited))
	}
}

func TestGitHub_RateLimitedStatus(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(_ *stub, _ recorded, w http.ResponseWriter) {
		w.Header().Set("x-ratelimit-remaining", "0")
		writeJSON(w, 403, map[string]any{"message": "API rate limit exceeded"})
	})
	_, err := s.adapter(fixedToken).ListRunArtifacts(t.Context(), repo, 1)
	var ge *app.GitHubError
	if !errors.As(err, &ge) || ge.Status != 403 || !ge.RateLimited {
		t.Fatalf("ListRunArtifacts() error = %v (%+v), want 403 rate limited", err, ge)
	}
	if got := domain.GitHubErrorCode(ge.Status, ge.RateLimited); got != domain.CodeRateLimited {
		t.Errorf("GitHubErrorCode = %q, want github_rate_limited", got)
	}
	if strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("error text %q carries GitHub's message", err.Error())
	}
}

// assertClean fails when err's text carries the token or a response body.
func assertClean(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	for _, bad := range []string{testToken, "try later", "bad gateway", "denied", "Gone"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("error text %q contains %q", err.Error(), bad)
		}
	}
}
