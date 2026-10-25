package github_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// TestListPullRequests_mapsHeadIsFork (D13): the adapter carries the platform client's fork flag
// into the listed PR, for the list and for the detail fetch.
func TestListPullRequests_mapsHeadIsFork(t *testing.T) {
	repo := func(name string) map[string]any { return map[string]any{"full_name": name} }
	pull := func(number int, head any) map[string]any {
		return map[string]any{
			"number": number, "title": "T", "user": nil,
			"head": map[string]any{"ref": "f", "sha": "s", "repo": head}, "base": map[string]any{"ref": "main", "repo": repo("acme/api")},
			"state": "open", "merged_at": nil, "draft": false, "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-03T00:00:00Z",
		}
	}
	a := newAdapter(t, "tok", reply(200, []map[string]any{pull(1, repo("acme/api")), pull(2, repo("mallory/api")), pull(3, nil)}))
	got, err := a.ListPullRequests(t.Context(), "acme", "api")
	if err != nil || len(got) != 3 {
		t.Fatalf("ListPullRequests() = %d items, %v, want 3, nil", len(got), err)
	}
	for i, want := range []bool{false, true, true} {
		if got[i].HeadIsFork != want {
			t.Errorf("ListPullRequests()[%d] (#%d) HeadIsFork = %v, want %v", i, got[i].Number, got[i].HeadIsFork, want)
		}
	}
}

func TestListPullRequests_mapsFields(t *testing.T) {
	a := newAdapter(t, "tok", reply(200, []map[string]any{{
		"number": 3, "title": "T", "user": nil, "head": map[string]string{"ref": "f", "sha": "s"}, "base": map[string]string{"ref": "main"},
		"state": "closed", "merged_at": "2026-10-02T00:00:00Z", "closed_at": "2026-10-02T00:00:01Z", "draft": true, "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-03T00:00:00Z",
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
	merged := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	closed := time.Date(2026, 10, 2, 0, 0, 1, 0, time.UTC)
	if g.MergedAt == nil || !g.MergedAt.Equal(merged) || g.ClosedAt == nil || !g.ClosedAt.Equal(closed) || !g.Draft {
		t.Errorf("ListPullRequests()[0] merged/closed/draft = %v/%v/%v, want %v/%v/true", g.MergedAt, g.ClosedAt, g.Draft, merged, closed)
	}
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
		"base": map[string]string{"ref": "main"}, "state": "open", "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-03T00:00:00Z", "draft": true,
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
	if !got.Draft || got.MergedAt != nil || got.ClosedAt != nil {
		t.Errorf("GetPullRequest() draft/merged/closed = %v/%v/%v, want true/nil/nil", got.Draft, got.MergedAt, got.ClosedAt)
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

func TestListPullRequestsPage_requestsThePageAndMapsFields(t *testing.T) {
	var query map[string][]string
	a := newAdapter(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		reply(200, []map[string]any{{
			"number": 9, "title": "T", "user": map[string]string{"login": "u"}, "head": map[string]string{"ref": "f", "sha": "s"},
			"base": map[string]string{"ref": "main"}, "state": "closed", "merged_at": "2026-10-02T00:00:00Z",
			"closed_at": "2026-10-02T00:00:01Z", "draft": true, "updated_at": "2026-10-03T00:00:00Z",
		}})(w, r)
	})
	got, err := a.ListPullRequestsPage(t.Context(), "acme", "api", 3, 20)
	if err != nil {
		t.Fatalf("ListPullRequestsPage(3, 20) error = %v", err)
	}
	wantQuery := map[string][]string{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"20"}, "page": {"3"}}
	if diff := cmp.Diff(wantQuery, query); diff != "" {
		t.Errorf("ListPullRequestsPage(3, 20) query mismatch (-want +got):\n%s", diff)
	}
	if len(got) != 1 || got[0].Number != 9 || got[0].Status != "merged" || !got[0].Draft || got[0].MergedAt == nil || got[0].ClosedAt == nil {
		t.Errorf("ListPullRequestsPage(3, 20) = %+v, want PR 9 merged and draft with merged/closed times", got)
	}
}

func TestListPullRequestsPage_translatesErrors(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		a := newAdapter(t, "", reply(200, []any{}))
		if _, err := a.ListPullRequestsPage(t.Context(), "acme", "api", 1, 50); !errors.Is(err, app.ErrNoToken) {
			t.Errorf("ListPullRequestsPage() error = %v, want app.ErrNoToken", err)
		}
	})
	t.Run("rate limited keeps the reset", func(t *testing.T) {
		reset := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
		a := newAdapter(t, "tok", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			reply(403, map[string]string{"message": "API rate limit exceeded"})(w, r)
		})
		_, err := a.ListPullRequestsPage(t.Context(), "acme", "api", 1, 50)
		want := &app.GitHubError{Status: 403, Message: "API rate limit exceeded", RateLimited: true, RateLimitReset: reset}
		var ge *app.GitHubError
		if !errors.As(err, &ge) {
			t.Fatalf("ListPullRequestsPage() error = %v, want *app.GitHubError", err)
		}
		if diff := cmp.Diff(want, ge, cmp.Comparer(time.Time.Equal)); diff != "" {
			t.Errorf("ListPullRequestsPage() error mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("plain 500 is not rate limited", func(t *testing.T) {
		a := newAdapter(t, "tok", reply(500, map[string]string{"message": "boom"}))
		_, err := a.ListPullRequestsPage(t.Context(), "acme", "api", 1, 50)
		var ge *app.GitHubError
		if !errors.As(err, &ge) || ge.Status != 500 || ge.RateLimited || !ge.RateLimitReset.IsZero() {
			t.Errorf("ListPullRequestsPage() error = %v, want a 500 without rate-limit state", err)
		}
	})
}

func TestListPullRequestsPage_secondaryLimitCarriesRetryAfter(t *testing.T) {
	t.Run("403 with Retry-After and requests left", func(t *testing.T) {
		a := newAdapter(t, "tok", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "4999")
			w.Header().Set("Retry-After", "60")
			reply(403, map[string]string{"message": "You have exceeded a secondary rate limit"})(w, r)
		})
		before := time.Now()
		_, err := a.ListPullRequestsPage(t.Context(), "acme", "api", 1, 50)
		after := time.Now()
		var ge *app.GitHubError
		if !errors.As(err, &ge) {
			t.Fatalf("ListPullRequestsPage() error = %v, want *app.GitHubError", err)
		}
		if ge.Status != 403 || !ge.RateLimited {
			t.Errorf("ListPullRequestsPage() error = %+v, want a rate-limited 403", ge)
		}
		if ge.RetryAfter.Before(before.Add(60*time.Second)) || ge.RetryAfter.After(after.Add(60*time.Second)) {
			t.Errorf("RetryAfter = %v, want 60 s after the request (%v..%v)", ge.RetryAfter, before, after)
		}
	})
	t.Run("403 saying secondary rate limit without a header", func(t *testing.T) {
		a := newAdapter(t, "tok", reply(403, map[string]string{"message": "You have exceeded a secondary rate limit. Please wait."}))
		_, err := a.ListPullRequestsPage(t.Context(), "acme", "api", 1, 50)
		var ge *app.GitHubError
		if !errors.As(err, &ge) || !ge.RateLimited || !ge.RetryAfter.IsZero() {
			t.Errorf("ListPullRequestsPage() error = %+v, want rate limited without RetryAfter", ge)
		}
	})
	t.Run("a plain 403 is not a rate limit", func(t *testing.T) {
		a := newAdapter(t, "tok", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "4999")
			reply(403, map[string]string{"message": "Resource not accessible by integration"})(w, r)
		})
		_, err := a.ListPullRequestsPage(t.Context(), "acme", "api", 1, 50)
		var ge *app.GitHubError
		if !errors.As(err, &ge) || ge.RateLimited || !ge.RetryAfter.IsZero() {
			t.Errorf("ListPullRequestsPage() error = %+v, want a 403 without rate-limit state", ge)
		}
	})
}

func TestGetPullStats_mapsStatsAndErrors(t *testing.T) {
	t.Run("stats", func(t *testing.T) {
		a := newAdapter(t, "tok", reply(200, map[string]any{
			"number": 4, "additions": 12, "deletions": 5, "changed_files": 3,
			"head": map[string]string{"ref": "f", "sha": "s"}, "base": map[string]string{"ref": "main"},
			"state": "open", "created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-03T00:00:00Z",
		}))
		got, err := a.GetPullStats(t.Context(), "acme", "api", 4)
		if err != nil {
			t.Fatalf("GetPullStats() error = %v", err)
		}
		if want := (domain.DiffStats{Additions: 12, Deletions: 5, FilesCount: 3}); got != want {
			t.Errorf("GetPullStats() = %+v, want %+v", got, want)
		}
	})
	t.Run("no token", func(t *testing.T) {
		a := newAdapter(t, "", reply(200, map[string]any{}))
		if _, err := a.GetPullStats(t.Context(), "acme", "api", 4); !errors.Is(err, app.ErrNoToken) {
			t.Errorf("GetPullStats() error = %v, want app.ErrNoToken", err)
		}
	})
	t.Run("status error", func(t *testing.T) {
		a := newAdapter(t, "tok", reply(404, map[string]string{"message": "Not Found"}))
		_, err := a.GetPullStats(t.Context(), "acme", "api", 4)
		var ge *app.GitHubError
		if !errors.As(err, &ge) || ge.Status != 404 || ge.RateLimited {
			t.Errorf("GetPullStats() error = %v, want a 404 GitHubError", err)
		}
	})
}
