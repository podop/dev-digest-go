package github_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	ghclient "dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/retry"
	adapter "dev-digest-go/backend/internal/pulls/adapters/github"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

func newAdapter(t *testing.T, token string, h http.HandlerFunc) *adapter.Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return adapter.New(ghclient.New(ghclient.Options{
		Token:   func() string { return token },
		BaseURL: srv.URL,
		Retry:   &retry.Options{},
	}))
}

func reply(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
}

func TestListPullRequests_mapsFields(t *testing.T) {
	a := newAdapter(t, "tok", reply(200, []map[string]any{{
		"number": 3, "title": "T", "user": nil, "head": map[string]string{"ref": "f", "sha": "s"}, "base": map[string]string{"ref": "main"},
		"state": "closed", "merged_at": "2026-10-02T00:00:00Z", "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-03T00:00:00Z",
	}}))
	got, err := a.ListPullRequests(t.Context(), "acme", "api")
	if err != nil {
		t.Fatalf("ListPullRequests() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListPullRequests() = %d items, want 1", len(got))
	}
	g := got[0]
	opened := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if g.Number != 3 || g.Author != "unknown" || g.Status != "merged" || g.Branch != "f" || g.Base != "main" || g.HeadSHA != "s" ||
		g.OpenedAt == nil || !g.OpenedAt.Equal(opened) || g.UpdatedAt == nil {
		t.Errorf("ListPullRequests()[0] = %+v, want mapped fields with author unknown and status merged", g)
	}
}

func TestErrors_areTranslated(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		a := newAdapter(t, "", reply(200, []any{}))
		if _, err := a.ListPullRequests(t.Context(), "acme", "api"); !errors.Is(err, app.ErrNoToken) {
			t.Errorf("ListPullRequests() error = %v, want app.ErrNoToken", err)
		}
	})
	t.Run("status error", func(t *testing.T) {
		a := newAdapter(t, "tok", reply(422, map[string]string{"message": "Validation Failed"}))
		_, err := a.CreateReviewComment(t.Context(), "acme", "api", 1, domain.CommentInput{Path: "a", Line: 1, Body: "x", CommitID: "s"})
		var ge *app.GitHubError
		if !errors.As(err, &ge) || ge.Status != 422 || ge.Message != "Validation Failed" {
			t.Errorf("CreateReviewComment() error = %v, want *app.GitHubError 422 Validation Failed", err)
		}
	})
	t.Run("other errors pass through", func(t *testing.T) {
		a := newAdapter(t, "tok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) })
		_, err := a.GetIssue(t.Context(), "acme", "api", 1)
		var ge *app.GitHubError
		if err == nil || errors.As(err, &ge) || errors.Is(err, app.ErrNoToken) {
			t.Errorf("GetIssue() error = %v, want a plain decode error", err)
		}
	})
}

func TestGetPullRequest_mergesAndMaps(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/api/pulls/7", reply(200, map[string]any{
		"number": 7, "title": "T", "user": map[string]string{"login": "u"}, "head": map[string]string{"ref": "f", "sha": "s"},
		"base": map[string]string{"ref": "main"}, "state": "open", "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-03T00:00:00Z",
		"body": "b", "additions": 9, "deletions": 2, "changed_files": 2,
	}))
	mux.HandleFunc("/repos/acme/api/pulls/7/files", reply(200, []map[string]any{
		{"filename": "a", "additions": 1, "deletions": 0, "patch": "p1"}, {"filename": "a", "additions": 2, "deletions": 1, "patch": "p2"},
	}))
	mux.HandleFunc("/repos/acme/api/pulls/7/commits", reply(200, []map[string]any{{
		"sha": "c1", "commit": map[string]any{"message": "m", "author": map[string]any{"name": "N", "date": "2026-10-02T00:00:00Z"}},
	}}))
	a := newAdapter(t, "tok", mux.ServeHTTP)

	got, err := a.GetPullRequest(t.Context(), "acme", "api", 7)
	if err != nil {
		t.Fatalf("GetPullRequest() error = %v", err)
	}
	if got.Additions != 9 || got.Deletions != 2 || got.FilesCount != 2 || got.Body == nil || *got.Body != "b" || got.Status != "open" {
		t.Errorf("GetPullRequest() scalars = %+v", got)
	}
	patch := "p1\np2"
	if len(got.Files) != 1 || got.Files[0].Additions != 3 || got.Files[0].Patch == nil || *got.Files[0].Patch != patch {
		t.Errorf("GetPullRequest() files = %+v, want one merged file with patches joined", got.Files)
	}
	if diff := cmp.Diff("N", got.Commits[0].Author); diff != "" {
		t.Errorf("commit author mismatch (-want +got):\n%s", diff)
	}
	if got.Commits[0].CommittedAt == nil {
		t.Errorf("commit committed_at = nil, want the date")
	}
}
