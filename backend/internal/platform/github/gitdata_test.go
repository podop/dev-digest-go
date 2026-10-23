package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/github"
)

// wantRequest checks method, path, query and the GitHub headers of the i-th recorded request and
// returns its decoded JSON body (nil when there was none).
func wantRequest(t *testing.T, s *stub, i int, method, path string, query map[string][]string) map[string]any {
	t.Helper()
	req, body := s.request(i)
	if req.Method != method {
		t.Errorf("request %d method = %q, want %q", i, req.Method, method)
	}
	if req.URL.Path != path {
		t.Errorf("request %d path = %q, want %q", i, req.URL.Path, path)
	}
	if diff := cmp.Diff(query, map[string][]string(req.URL.Query())); diff != "" && (len(query) > 0 || len(req.URL.Query()) > 0) {
		t.Errorf("request %d query mismatch (-want +got):\n%s", i, diff)
	}
	for k, want := range map[string]string{
		"Authorization":        "Bearer " + testToken,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	} {
		if got := req.Header.Get(k); got != want {
			t.Errorf("request %d header %s = %q, want %q", i, k, got, want)
		}
	}
	if body == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("request %d body %q is not a JSON object: %v", i, body, err)
	}
	return out
}

func wantBody(t *testing.T, got, want map[string]any) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("request body mismatch (-want +got):\n%s", diff)
	}
}

func TestGetRef(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing") {
			writeJSON(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		writeJSON(w, 200, map[string]any{"ref": "refs/heads/devdigest/ci", "object": map[string]any{"sha": "c0ffee"}})
	})
	c := newClient(s, github.Options{})
	sha, found, err := c.GetRef(t.Context(), "acme", "api", "devdigest/ci")
	if err != nil || !found || sha != "c0ffee" {
		t.Fatalf("GetRef(devdigest/ci) = %q, %v, %v; want c0ffee, true, nil", sha, found, err)
	}
	wantRequest(t, s, 0, http.MethodGet, "/repos/acme/api/git/ref/heads/devdigest/ci", nil)

	sha, found, err = c.GetRef(t.Context(), "acme", "api", "missing")
	if err != nil || found || sha != "" {
		t.Errorf("GetRef(missing) = %q, %v, %v; want empty, false, nil", sha, found, err)
	}
}

func TestGetCommit(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"sha": "c1", "tree": map[string]any{"sha": "t1"}})
	})
	got, err := newClient(s, github.Options{}).GetCommit(t.Context(), "acme", "api", "c1")
	if err != nil {
		t.Fatalf("GetCommit() error: %v", err)
	}
	if want := (github.CommitInfo{SHA: "c1", TreeSHA: "t1"}); got != want {
		t.Errorf("GetCommit() = %+v, want %+v", got, want)
	}
	wantRequest(t, s, 0, http.MethodGet, "/repos/acme/api/git/commits/c1", nil)
}

func TestGitDataWrites(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			writeJSON(w, 201, map[string]any{"number": 12, "html_url": "https://github.com/acme/api/pull/12"})
		case strings.Contains(r.URL.Path, "/git/refs"):
			writeJSON(w, 200, map[string]any{})
		default:
			writeJSON(w, 201, map[string]any{"sha": "new-sha"})
		}
	})
	c := newClient(s, github.Options{})
	ctx := t.Context()
	inline := "hello"

	if sha, err := c.CreateBlob(ctx, "acme", "api", "aGk="); err != nil || sha != "new-sha" {
		t.Fatalf("CreateBlob() = %q, %v", sha, err)
	}
	wantBody(t, wantRequest(t, s, 0, http.MethodPost, "/repos/acme/api/git/blobs", nil),
		map[string]any{"content": "aGk=", "encoding": "base64"})

	tree := []github.TreeEntry{{Path: ".devdigest/a.yml", Content: &inline}, {Path: "runner", SHA: "blob1"}}
	if sha, err := c.CreateTree(ctx, "acme", "api", "base1", tree); err != nil || sha != "new-sha" {
		t.Fatalf("CreateTree() = %q, %v", sha, err)
	}
	wantBody(t, wantRequest(t, s, 1, http.MethodPost, "/repos/acme/api/git/trees", nil), map[string]any{
		"base_tree": "base1",
		"tree": []any{
			map[string]any{"path": ".devdigest/a.yml", "mode": "100644", "type": "blob", "content": "hello"},
			map[string]any{"path": "runner", "mode": "100644", "type": "blob", "sha": "blob1"},
		},
	})

	if sha, err := c.CreateCommit(ctx, "acme", "api", "msg", "tree1", []string{"p1"}); err != nil || sha != "new-sha" {
		t.Fatalf("CreateCommit() = %q, %v", sha, err)
	}
	wantBody(t, wantRequest(t, s, 2, http.MethodPost, "/repos/acme/api/git/commits", nil),
		map[string]any{"message": "msg", "tree": "tree1", "parents": []any{"p1"}})

	if err := c.UpdateRef(ctx, "acme", "api", "devdigest/ci", "sha9"); err != nil {
		t.Fatalf("UpdateRef() error: %v", err)
	}
	wantBody(t, wantRequest(t, s, 3, http.MethodPatch, "/repos/acme/api/git/refs/heads/devdigest/ci", nil),
		map[string]any{"sha": "sha9", "force": true})

	if err := c.CreateRef(ctx, "acme", "api", "devdigest/ci", "sha9"); err != nil {
		t.Fatalf("CreateRef() error: %v", err)
	}
	wantBody(t, wantRequest(t, s, 4, http.MethodPost, "/repos/acme/api/git/refs", nil),
		map[string]any{"ref": "refs/heads/devdigest/ci", "sha": "sha9"})

	pr, err := c.CreatePull(ctx, "acme", "api", github.NewPull{Title: "T", Head: "devdigest/ci", Base: "main", Body: "B"})
	if err != nil {
		t.Fatalf("CreatePull() error: %v", err)
	}
	if want := (github.PullRef{Number: 12, HTMLURL: "https://github.com/acme/api/pull/12"}); pr != want {
		t.Errorf("CreatePull() = %+v, want %+v", pr, want)
	}
	wantBody(t, wantRequest(t, s, 5, http.MethodPost, "/repos/acme/api/pulls", nil),
		map[string]any{"title": "T", "head": "devdigest/ci", "base": "main", "body": "B"})
}

func TestCreateTree_rejectsAmbiguousEntry(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 201, map[string]any{"sha": "x"}) })
	c := newClient(s, github.Options{})
	both := "x"
	for name, e := range map[string]github.TreeEntry{
		"neither": {Path: "a"},
		"both":    {Path: "a", Content: &both, SHA: "s"},
	} {
		if _, err := c.CreateTree(t.Context(), "acme", "api", "b", []github.TreeEntry{e}); err == nil {
			t.Errorf("CreateTree(entry with %s) = nil error, want one", name)
		}
	}
	if n := s.count(); n != 0 {
		t.Errorf("requests sent = %d, want 0", n)
	}
}

func TestFindOpenPull(t *testing.T) {
	t.Parallel()
	empty := false
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if empty {
			writeJSON(w, 200, []any{})
			return
		}
		writeJSON(w, 200, []any{map[string]any{"number": 7, "html_url": "https://github.com/acme/api/pull/7"}})
	})
	c := newClient(s, github.Options{})
	pr, found, err := c.FindOpenPull(t.Context(), "acme", "api", "devdigest/ci")
	if err != nil || !found || pr.Number != 7 || pr.HTMLURL != "https://github.com/acme/api/pull/7" {
		t.Fatalf("FindOpenPull() = %+v, %v, %v", pr, found, err)
	}
	wantRequest(t, s, 0, http.MethodGet, "/repos/acme/api/pulls",
		map[string][]string{"state": {"open"}, "head": {"acme:devdigest/ci"}, "per_page": {"1"}})

	empty = true
	if _, found, err := c.FindOpenPull(t.Context(), "acme", "api", "devdigest/ci"); err != nil || found {
		t.Errorf("FindOpenPull(none) = found %v, err %v; want false, nil", found, err)
	}
}

func TestGitData_getsRetryPostsDoNot(t *testing.T) {
	t.Parallel()
	fails := 2
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && fails > 0 {
			fails--
			writeJSON(w, 503, map[string]any{"message": "unavailable"})
			return
		}
		if r.Method == http.MethodPost {
			writeJSON(w, 503, map[string]any{"message": "unavailable"})
			return
		}
		writeJSON(w, 200, map[string]any{"object": map[string]any{"sha": "ok"}})
	})
	c := newClient(s, github.Options{})
	if sha, found, err := c.GetRef(t.Context(), "acme", "api", "main"); err != nil || !found || sha != "ok" {
		t.Fatalf("GetRef() after two 503s = %q, %v, %v; want ok", sha, found, err)
	}
	if n := s.count(); n != 3 {
		t.Errorf("GetRef requests = %d, want 3", n)
	}

	before := s.count()
	_, err := c.CreateBlob(t.Context(), "acme", "api", "aGk=")
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("CreateBlob() error = %v, want a 503 StatusError", err)
	}
	if n := s.count() - before; n != 1 {
		t.Errorf("CreateBlob requests = %d, want 1 (a POST is not retried)", n)
	}
	before = s.count()
	if _, err := c.CreatePull(t.Context(), "acme", "api", github.NewPull{}); err == nil {
		t.Error("CreatePull() = nil error, want the 503")
	}
	if n := s.count() - before; n != 1 {
		t.Errorf("CreatePull requests = %d, want 1", n)
	}
}

func TestGitData_statusErrorsCarryRateLimitHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status    int
		remaining string
		limited   bool
	}{
		{404, "", false},
		{403, "0", true},
		{403, "4999", false},
		{410, "12", false},
	}
	for _, tc := range tests {
		s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
			if tc.remaining != "" {
				w.Header().Set("X-RateLimit-Remaining", tc.remaining)
			}
			writeJSON(w, tc.status, map[string]any{"message": "nope"})
		})
		err := newClient(s, github.Options{}).UpdateRef(t.Context(), "acme", "api", "b", "sha")
		var se *github.StatusError
		if !errors.As(err, &se) {
			t.Fatalf("UpdateRef() status %d error = %v, want a StatusError", tc.status, err)
		}
		if se.Status != tc.status || se.RateLimitRemaining != tc.remaining || se.RateLimited() != tc.limited {
			t.Errorf("UpdateRef() status %d: got %+v (limited %v), want remaining %q limited %v",
				tc.status, se, se.RateLimited(), tc.remaining, tc.limited)
		}
	}
}

// Every new operation runs on the no-redirect client: a 302 is an error and the target is never
// requested, so the token cannot follow it.
func TestGitDataAndActions_neverFollowRedirects(t *testing.T) {
	t.Parallel()
	var target *stub
	target = newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{}) })
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	})
	c := newClient(s, github.Options{})
	ctx := context.Background()
	ops := map[string]func() error{
		"GetRef":           func() error { _, _, err := c.GetRef(ctx, "o", "r", "b"); return err },
		"GetCommit":        func() error { _, err := c.GetCommit(ctx, "o", "r", "s"); return err },
		"CreateBlob":       func() error { _, err := c.CreateBlob(ctx, "o", "r", "aA=="); return err },
		"CreateRef":        func() error { return c.CreateRef(ctx, "o", "r", "b", "s") },
		"UpdateRef":        func() error { return c.UpdateRef(ctx, "o", "r", "b", "s") },
		"FindOpenPull":     func() error { _, _, err := c.FindOpenPull(ctx, "o", "r", "b"); return err },
		"CreatePull":       func() error { _, err := c.CreatePull(ctx, "o", "r", github.NewPull{}); return err },
		"ListWorkflowRuns": func() error { _, err := c.ListWorkflowRuns(ctx, "o", "r", "w.yml"); return err },
		"ListRunArtifacts": func() error { _, err := c.ListRunArtifacts(ctx, "o", "r", 1); return err },
	}
	for name, op := range ops {
		var se *github.StatusError
		if err := op(); !errors.As(err, &se) || se.Status != http.StatusFound {
			t.Errorf("%s() error = %v, want a 302 StatusError", name, err)
		}
	}
	if n := target.count(); n != 0 {
		t.Errorf("redirect target received %d requests, want 0", n)
	}
}
