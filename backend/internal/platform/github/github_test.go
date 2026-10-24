package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/retry"
)

const testToken = "ghp_fake_token_123"

// stub is a recording GitHub API double.
type stub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
}

func newStub(t *testing.T, handler http.HandlerFunc) *stub {
	t.Helper()
	s := &stub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *stub) request(i int) (*http.Request, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[i], s.bodies[i]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fastRetry() *retry.Options {
	return &retry.Options{Retries: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
}

func newClient(s *stub, opts github.Options) *github.Client {
	opts.BaseURL = s.URL
	if opts.Token == nil {
		opts.Token = func() string { return testToken }
	}
	if opts.Retry == nil {
		opts.Retry = fastRetry()
	}
	return github.New(opts)
}

func prJSON(n int, state string, mergedAt any, user any) map[string]any {
	return map[string]any{
		"number": n, "title": fmt.Sprintf("PR %d", n), "user": user, "state": state, "merged_at": mergedAt,
		"closed_at": nil, "draft": false,
		"head": map[string]any{"ref": "feat", "sha": "abc123"}, "base": map[string]any{"ref": "main"},
		"created_at": "2026-10-01T10:00:00Z", "updated_at": "2026-10-02T11:30:00Z",
		"body": "closes #99", "additions": 7, "deletions": 3, "changed_files": 2,
	}
}

func TestListPullRequests(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		draft := prJSON(3, "open", nil, map[string]any{"login": "octo"})
		draft["draft"] = true
		merged := prJSON(2, "closed", "2026-10-02T00:00:00Z", map[string]any{"login": "merger"})
		merged["closed_at"] = "2026-10-02T00:00:00Z"
		closed := prJSON(1, "closed", nil, nil)
		closed["closed_at"] = "2026-10-03T08:00:00Z"
		writeJSON(w, 200, []any{draft, merged, closed})
	})
	got, err := newClient(s, github.Options{}).ListPullRequests(t.Context(), "acme", "api")
	if err != nil {
		t.Fatalf("ListPullRequests() error: %v", err)
	}
	opened := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 10, 2, 11, 30, 0, 0, time.UTC)
	mergedAt := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	closedAt := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	want := []github.PullSummary{
		{Number: 3, Title: "PR 3", Author: "octo", Branch: "feat", Base: "main", HeadSHA: "abc123", Status: "open", OpenedAt: opened, UpdatedAt: updated, Draft: true},
		{Number: 2, Title: "PR 2", Author: "merger", Branch: "feat", Base: "main", HeadSHA: "abc123", Status: "merged", OpenedAt: opened, UpdatedAt: updated, MergedAt: &mergedAt, ClosedAt: &mergedAt},
		{Number: 1, Title: "PR 1", Author: "unknown", Branch: "feat", Base: "main", HeadSHA: "abc123", Status: "closed", OpenedAt: opened, UpdatedAt: updated, ClosedAt: &closedAt},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListPullRequests() mismatch (-want +got):\n%s", diff)
	}

	req, _ := s.request(0)
	if req.URL.Path != "/repos/acme/api/pulls" {
		t.Errorf("path = %q, want /repos/acme/api/pulls", req.URL.Path)
	}
	wantQuery := map[string][]string{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"50"}}
	if diff := cmp.Diff(wantQuery, map[string][]string(req.URL.Query())); diff != "" {
		t.Errorf("query mismatch (-want +got):\n%s", diff)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want a bearer token", got)
	}
	if got := req.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got)
	}
}

func TestListPullRequestsPage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		page, perPage int
		wantQuery     map[string][]string
	}{
		{"first page", 1, 50, map[string][]string{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"50"}}},
		{"third page", 3, 20, map[string][]string{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"20"}, "page": {"3"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, []any{prJSON(4, "open", nil, map[string]any{"login": "octo"})})
			})
			got, err := newClient(s, github.Options{}).ListPullRequestsPage(t.Context(), "acme", "api", tc.page, tc.perPage)
			if err != nil || len(got) != 1 || got[0].Number != 4 {
				t.Fatalf("ListPullRequestsPage(%d, %d) = (%v, %v), want PR 4 and nil", tc.page, tc.perPage, got, err)
			}
			req, _ := s.request(0)
			if diff := cmp.Diff(tc.wantQuery, map[string][]string(req.URL.Query())); diff != "" {
				t.Errorf("ListPullRequestsPage(%d, %d) query mismatch (-want +got):\n%s", tc.page, tc.perPage, diff)
			}
		})
	}
}

func TestListPullRequestsPage_rejectsOutOfRangePaging(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []any{}) })
	c := newClient(s, github.Options{})
	for _, tc := range []struct{ page, perPage int }{{0, 50}, {-1, 50}, {1, 0}, {1, github.PageSize + 1}} {
		if _, err := c.ListPullRequestsPage(t.Context(), "o", "r", tc.page, tc.perPage); err == nil {
			t.Errorf("ListPullRequestsPage(%d, %d) error = nil, want a range error", tc.page, tc.perPage)
		}
	}
	if n := s.count(); n != 0 {
		t.Errorf("out-of-range paging sent %d requests, want 0", n)
	}
}

func TestStatusError_carriesRateLimitReset(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reset string
		want        time.Time
	}{
		{"unix seconds", "1790000000", time.Unix(1790000000, 0).UTC()},
		{"absent", "", time.Time{}},
		{"malformed", "soon", time.Time{}},
		{"negative", "-5", time.Time{}},
		{"absurd", "99999999999999999", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				if tc.reset != "" {
					w.Header().Set("X-RateLimit-Reset", tc.reset)
				}
				writeJSON(w, 403, map[string]any{"message": "API rate limit exceeded"})
			})
			c := newClient(s, github.Options{Retry: &retry.Options{}})
			_, err := c.ListPullRequestsPage(t.Context(), "o", "r", 1, 50)
			var se *github.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("ListPullRequestsPage() error = %v, want a StatusError", err)
			}
			if !se.RateLimited() || !se.RateLimitReset.Equal(tc.want) {
				t.Errorf("ListPullRequestsPage() StatusError = {limited %v, reset %v}, want {true, %v}", se.RateLimited(), se.RateLimitReset, tc.want)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error leaks the token: %v", err)
			}
		})
	}
}

func TestStatusError_carriesRetryAfterAndDetectsTheSecondaryLimit(t *testing.T) {
	t.Parallel()
	date := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	for _, tc := range []struct {
		name, retryAfter, message string
		remaining                 string
		status                    int
		wantWait                  time.Duration // 0: no Retry-After expected
		wantLimit                 bool
	}{
		{"403 with Retry-After", "60", "You have exceeded a secondary rate limit", "4999", 403, 60 * time.Second, true},
		{"429 with Retry-After", "30", "slow down", "4999", 429, 30 * time.Second, true},
		{"Retry-After as an HTTP date", date, "slow down", "4999", 403, 90 * time.Second, true},
		{"unreadable Retry-After counts as a minute", "soon", "slow down", "4999", 403, time.Minute, true},
		{"absurd Retry-After is cut to a day", "99999999999", "slow down", "4999", 403, 24 * time.Hour, true},
		{"message alone", "", "You have exceeded a SECONDARY RATE LIMIT.", "4999", 403, 0, true},
		{"primary limit", "", "API rate limit exceeded", "0", 403, 0, true},
		{"plain 403", "", "Resource not accessible by integration", "4999", 403, 0, false},
		{"Retry-After on a 503 is no rate limit", "10", "unavailable", "4999", 503, 10 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", tc.remaining)
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				writeJSON(w, tc.status, map[string]any{"message": tc.message})
			})
			c := newClient(s, github.Options{Retry: &retry.Options{}})
			before := time.Now()
			_, err := c.ListPullRequestsPage(t.Context(), "o", "r", 1, 50)
			after := time.Now()
			var se *github.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("ListPullRequestsPage() error = %v, want a StatusError", err)
			}
			if got := se.LimitReached(); got != tc.wantLimit {
				t.Errorf("StatusError{%d, %q}.LimitReached() = %v, want %v", tc.status, tc.message, got, tc.wantLimit)
			}
			if tc.wantWait == 0 {
				if !se.RetryAfter.IsZero() {
					t.Errorf("RetryAfter = %v, want the zero time (no header)", se.RetryAfter)
				}
				return
			}
			// The HTTP date has whole-second precision, so allow two seconds of slack.
			if se.RetryAfter.Before(before.Add(tc.wantWait-2*time.Second)) || se.RetryAfter.After(after.Add(tc.wantWait)) {
				t.Errorf("RetryAfter = %v, want about %v after the request (%v..%v)", se.RetryAfter, tc.wantWait, before, after)
			}
		})
	}
}

func TestGetPullStats_isOneRequestWithTheDiffStats(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/o/r/pulls/5") {
			t.Errorf("GetPullStats requested %s, want only /repos/o/r/pulls/5", r.URL.Path)
		}
		writeJSON(w, 200, prJSON(5, "open", nil, nil))
	})
	got, err := newClient(s, github.Options{}).GetPullStats(t.Context(), "o", "r", 5)
	if err != nil {
		t.Fatalf("GetPullStats() error: %v", err)
	}
	if want := (github.PullStats{Additions: 7, Deletions: 3, FilesCount: 2}); got != want {
		t.Errorf("GetPullStats() = %+v, want %+v", got, want)
	}
	if s.count() != 1 {
		t.Errorf("stub received %d requests, want 1", s.count())
	}
}

func TestGetPullStats_noToken(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(http.ResponseWriter, *http.Request) { t.Error("a request was sent without a token") })
	_, err := newClient(s, github.Options{Token: func() string { return "" }}).GetPullStats(t.Context(), "o", "r", 5)
	if !errors.Is(err, github.ErrNoToken) {
		t.Errorf("GetPullStats() error = %v, want ErrNoToken", err)
	}
}

func TestClient_noToken(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []any{}) })
	c := newClient(s, github.Options{Token: func() string { return "  " }})

	calls := map[string]func() error{
		"ListPullRequests":     func() error { _, err := c.ListPullRequests(t.Context(), "o", "r"); return err },
		"ListPullRequestsPage": func() error { _, err := c.ListPullRequestsPage(t.Context(), "o", "r", 1, 50); return err },
		"GetPullRequest":       func() error { _, err := c.GetPullRequest(t.Context(), "o", "r", 1); return err },
		"GetIssue":             func() error { _, err := c.GetIssue(t.Context(), "o", "r", 1); return err },
		"CurrentLogin":         func() error { _, err := c.CurrentLogin(t.Context()); return err },
		"ListReviewComment":    func() error { _, err := c.ListReviewComments(t.Context(), "o", "r", 1); return err },
		"CreateReviewComment": func() error {
			_, err := c.CreateReviewComment(t.Context(), "o", "r", 1, github.CommentInput{Body: "x"})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, github.ErrNoToken) {
			t.Errorf("%s() error = %v, want ErrNoToken", name, err)
		}
	}
	if n := s.count(); n != 0 {
		t.Errorf("stub received %d requests without a token, want 0", n)
	}
}

func TestClient_readsTokenPerCall(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"login": "octo"}) })
	var token atomic.Value
	token.Store("first")
	c := newClient(s, github.Options{Token: func() string { return token.Load().(string) }})

	for _, tok := range []string{"first", "second"} {
		token.Store(tok)
		if _, err := c.CurrentLogin(t.Context()); err != nil {
			t.Fatalf("CurrentLogin() error: %v", err)
		}
	}
	for i, want := range []string{"Bearer first", "Bearer second"} {
		req, _ := s.request(i)
		if got := req.Header.Get("Authorization"); got != want {
			t.Errorf("request %d Authorization = %q, want %q", i, got, want)
		}
	}
}

func TestListPullRequests_retries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		statuses     []int // per request; the last repeats
		wantRequests int
		wantStatus   int // 0 = success
	}{
		{"503 twice then 200", []int{503, 503, 200}, 3, 0},
		{"429 then 200", []int{429, 200}, 2, 0},
		{"404 is not retried", []int{404}, 1, 404},
		{"401 is not retried", []int{401}, 1, 401},
		{"422 is not retried", []int{422}, 1, 422},
		{"503 forever: 1 + 3 retries", []int{503}, 4, 503},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var n atomic.Int32
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				i := int(n.Add(1)) - 1
				status := tc.statuses[min(i, len(tc.statuses)-1)]
				if status == 200 {
					writeJSON(w, 200, []any{prJSON(1, "open", nil, nil)})
					return
				}
				writeJSON(w, status, map[string]any{"message": "nope"})
			})
			got, err := newClient(s, github.Options{}).ListPullRequests(t.Context(), "o", "r")
			if tc.wantStatus == 0 {
				if err != nil || len(got) != 1 {
					t.Fatalf("ListPullRequests() = (%d items, %v), want 1 item and nil", len(got), err)
				}
			} else {
				var se *github.StatusError
				if !errors.As(err, &se) || se.Status != tc.wantStatus {
					t.Fatalf("ListPullRequests() error = %v, want StatusError %d", err, tc.wantStatus)
				}
			}
			if s.count() != tc.wantRequests {
				t.Errorf("stub received %d requests, want %d", s.count(), tc.wantRequests)
			}
		})
	}
}

func TestListPullRequests_hangingServerTimesOutWithoutRetry(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c := newClient(s, github.Options{Timeout: 50 * time.Millisecond})

	start := time.Now()
	_, err := c.ListPullRequests(t.Context(), "o", "r")
	if err == nil {
		t.Fatal("ListPullRequests() error = nil, want a timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ListPullRequests() error = %v, want context deadline exceeded", err)
	}
	if s.count() != 1 {
		t.Errorf("stub received %d requests, want exactly 1 (timeouts are not retried)", s.count())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v, want about the 50ms timeout", elapsed)
	}
}

func TestListPullRequests_connectionResetIsRetried(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close() // reply with nothing: client sees EOF
			return
		}
		writeJSON(w, 200, []any{})
	})
	if _, err := newClient(s, github.Options{}).ListPullRequests(t.Context(), "o", "r"); err != nil {
		t.Fatalf("ListPullRequests() error: %v", err)
	}
	if s.count() != 2 {
		t.Errorf("stub received %d requests, want 2", s.count())
	}
}

// pagedStub serves a PR with nFiles files and nCommits commits in pages.
func pagedStub(t *testing.T, nFiles, nCommits int) *stub {
	t.Helper()
	return newStub(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items := func(total int, mk func(i int) any) []any {
			var out []any
			for i := (page - 1) * 100; i < min(page*100, total); i++ {
				out = append(out, mk(i))
			}
			return out
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			writeJSON(w, 200, items(nFiles, func(i int) any {
				return map[string]any{"filename": fmt.Sprintf("f%04d.go", i), "additions": 1, "deletions": 0, "patch": "@@ x"}
			}))
		case strings.HasSuffix(r.URL.Path, "/commits"):
			writeJSON(w, 200, items(nCommits, func(i int) any {
				return map[string]any{
					"sha":    fmt.Sprintf("sha%04d", i),
					"commit": map[string]any{"message": "m", "author": map[string]any{"name": "Ann", "date": "2026-10-01T09:00:00Z"}},
				}
			}))
		default:
			pr := prJSON(5, "open", nil, map[string]any{"login": "octo"})
			pr["changed_files"] = nFiles
			writeJSON(w, 200, pr)
		}
	})
}

func TestGetPullRequest_capsFilesAndCommits(t *testing.T) {
	t.Parallel()
	s := pagedStub(t, 3100, 300)
	got, err := newClient(s, github.Options{}).GetPullRequest(t.Context(), "o", "r", 5)
	if err != nil {
		t.Fatalf("GetPullRequest() error: %v", err)
	}
	if len(got.Files) != github.MaxFiles {
		t.Errorf("len(Files) = %d, want %d", len(got.Files), github.MaxFiles)
	}
	if len(got.Commits) != github.MaxCommits {
		t.Errorf("len(Commits) = %d, want %d", len(got.Commits), github.MaxCommits)
	}
	if got.FilesCount != 3100 {
		t.Errorf("FilesCount = %d, want GitHub's changed_files 3100", got.FilesCount)
	}
	// 1 PR + 30 file pages (stops at the cap) + 3 commit pages.
	if s.count() != 34 {
		t.Errorf("stub received %d requests, want 34", s.count())
	}
}

func TestGetPullRequest_shortPageStopsPagination(t *testing.T) {
	t.Parallel()
	s := pagedStub(t, 150, 1)
	got, err := newClient(s, github.Options{}).GetPullRequest(t.Context(), "o", "r", 5)
	if err != nil {
		t.Fatalf("GetPullRequest() error: %v", err)
	}
	if len(got.Files) != 150 || len(got.Commits) != 1 {
		t.Errorf("got %d files and %d commits, want 150 and 1", len(got.Files), len(got.Commits))
	}
	if s.count() != 1+2+1 {
		t.Errorf("stub received %d requests, want 4", s.count())
	}
}

func TestGetPullRequest_mapsFields(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			writeJSON(w, 200, []any{
				map[string]any{"filename": "a.go", "additions": 2, "deletions": 1, "patch": "p1"},
				map[string]any{"filename": "bin.png", "additions": 0, "deletions": 0},
				map[string]any{"filename": "a.go", "additions": 5, "deletions": 4, "patch": "p2"},
			})
		case strings.HasSuffix(r.URL.Path, "/commits"):
			writeJSON(w, 200, []any{
				map[string]any{"sha": "s1", "commit": map[string]any{"message": "one", "author": map[string]any{"name": "Ann", "date": "2026-10-01T09:00:00Z"}}},
				map[string]any{"sha": "s2", "commit": map[string]any{"message": "two"}, "author": map[string]any{"login": "bob"}},
				map[string]any{"sha": "s3", "commit": map[string]any{"message": "three"}},
			})
		default:
			pr := prJSON(5, "closed", "2026-10-03T00:00:00Z", map[string]any{"login": "octo"})
			pr["closed_at"] = "2026-10-03T00:00:00Z"
			pr["body"] = nil
			writeJSON(w, 200, pr)
		}
	})
	got, err := newClient(s, github.Options{}).GetPullRequest(t.Context(), "o", "r", 5)
	if err != nil {
		t.Fatalf("GetPullRequest() error: %v", err)
	}
	p12 := "p1\np2"
	date := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	mergedAt := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	want := github.PullDetail{
		PullSummary: github.PullSummary{
			Number: 5, Title: "PR 5", Author: "octo", Branch: "feat", Base: "main", HeadSHA: "abc123", Status: "merged",
			OpenedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 2, 11, 30, 0, 0, time.UTC),
			MergedAt: &mergedAt, ClosedAt: &mergedAt,
		},
		Additions: 7, Deletions: 3, FilesCount: 2,
		Files: []github.File{
			{Path: "a.go", Additions: 7, Deletions: 5, Patch: &p12},
			{Path: "bin.png"},
		},
		Commits: []github.Commit{
			{SHA: "s1", Message: "one", Author: "Ann", CommittedAt: &date},
			{SHA: "s2", Message: "two", Author: "bob"},
			{SHA: "s3", Message: "three", Author: "unknown"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GetPullRequest() mismatch (-want +got):\n%s", diff)
	}
}

func TestMergeSamePath(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	tests := []struct {
		name string
		in   []github.File
		want []github.File
	}{
		{"empty", nil, []github.File{}},
		{
			"distinct paths keep order",
			[]github.File{{Path: "b"}, {Path: "a"}},
			[]github.File{{Path: "b"}, {Path: "a"}},
		},
		{
			"same path sums stats and joins patches",
			[]github.File{{Path: "a", Additions: 1, Deletions: 2, Patch: str("x")}, {Path: "a", Additions: 3, Deletions: 4, Patch: str("y")}},
			[]github.File{{Path: "a", Additions: 4, Deletions: 6, Patch: str("x\ny")}},
		},
		{
			"empty and missing patches are skipped",
			[]github.File{{Path: "a", Patch: str("")}, {Path: "a", Patch: str("y")}, {Path: "a"}},
			[]github.File{{Path: "a", Patch: str("y")}},
		},
		{
			"no patch at all stays nil",
			[]github.File{{Path: "a"}, {Path: "a"}},
			[]github.File{{Path: "a"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, github.MergeSamePath(tc.in)); diff != "" {
				t.Errorf("MergeSamePath() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMergeSamePath_doesNotMutateInput(t *testing.T) {
	t.Parallel()
	p := "x"
	in := []github.File{{Path: "a", Additions: 1, Patch: &p}, {Path: "a", Additions: 2}}
	_ = github.MergeSamePath(in)
	if in[0].Additions != 1 || *in[0].Patch != "x" {
		t.Errorf("input mutated: %+v", in[0])
	}
}

func TestGetPullRequest_detailTimeoutBoundsWholeFetch(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/files") {
			<-r.Context().Done()
			return
		}
		writeJSON(w, 200, prJSON(5, "open", nil, nil))
	})
	c := newClient(s, github.Options{Timeout: 10 * time.Second, DetailTimeout: 100 * time.Millisecond})
	_, err := c.GetPullRequest(t.Context(), "o", "r", 5)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetPullRequest() error = %v, want context deadline exceeded", err)
	}
	if s.count() != 2 {
		t.Errorf("stub received %d requests, want 2 (PR, then the hanging files page)", s.count())
	}
}

func TestGetIssue(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/issues/99") {
			writeJSON(w, 200, map[string]any{"number": 99, "title": "Bug", "body": nil, "state": "open"})
			return
		}
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	})
	c := newClient(s, github.Options{})

	got, err := c.GetIssue(t.Context(), "o", "r", 99)
	if err != nil {
		t.Fatalf("GetIssue(99) error: %v", err)
	}
	if want := (github.Issue{Number: 99, Title: "Bug", State: "open"}); !cmp.Equal(want, got) {
		t.Errorf("GetIssue(99) = %+v, want %+v", got, want)
	}

	_, err = c.GetIssue(t.Context(), "o", "r", 1)
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != 404 || se.Message != "Not Found" {
		t.Errorf("GetIssue(1) error = %v, want StatusError 404 Not Found", err)
	}
}

func TestCurrentLogin(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"login": "octo"}) })
	got, err := newClient(s, github.Options{}).CurrentLogin(t.Context())
	if err != nil || got != "octo" {
		t.Errorf("CurrentLogin() = (%q, %v), want (\"octo\", nil)", got, err)
	}
	if req, _ := s.request(0); req.URL.Path != "/user" {
		t.Errorf("path = %q, want /user", req.URL.Path)
	}
}

func TestListReviewComments(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []any{
			map[string]any{
				"id": 1, "path": "a.go", "line": nil, "original_line": 4, "side": nil, "body": "outdated",
				"user": map[string]any{"login": "ann"}, "created_at": "2026-10-01T09:00:00Z", "html_url": "https://x/1",
			},
			map[string]any{
				"id": 2, "path": "a.go", "line": 7, "original_line": 7, "side": "LEFT", "body": "reply",
				"user": nil, "created_at": "2026-10-01T09:05:00Z", "html_url": "https://x/2", "in_reply_to_id": 1,
			},
		})
	})
	got, err := newClient(s, github.Options{}).ListReviewComments(t.Context(), "o", "r", 12)
	if err != nil {
		t.Fatalf("ListReviewComments() error: %v", err)
	}
	four, seven, one := 4, 7, int64(1)
	want := []github.ReviewComment{
		{ID: 1, Path: "a.go", OriginalLine: &four, Side: "RIGHT", Body: "outdated", User: "ann",
			CreatedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), HTMLURL: "https://x/1", IsOutdated: true},
		{ID: 2, Path: "a.go", Line: &seven, OriginalLine: &seven, Side: "LEFT", Body: "reply", User: "unknown",
			CreatedAt: time.Date(2026, 10, 1, 9, 5, 0, 0, time.UTC), HTMLURL: "https://x/2", InReplyToID: &one},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListReviewComments() mismatch (-want +got):\n%s", diff)
	}
	req, _ := s.request(0)
	if req.URL.Path != "/repos/o/r/pulls/12/comments" || req.URL.Query().Get("per_page") != "100" {
		t.Errorf("request = %s, want /repos/o/r/pulls/12/comments?per_page=100", req.URL)
	}
}

func commentReply() map[string]any {
	return map[string]any{
		"id": 9, "path": "a.go", "line": 3, "side": "RIGHT", "body": "x", "user": map[string]any{"login": "me"},
		"created_at": "2026-10-01T09:00:00Z", "html_url": "https://x/9",
	}
}

func TestCreateReviewComment(t *testing.T) {
	t.Parallel()
	reply := int64(5)
	tests := []struct {
		name     string
		in       github.CommentInput
		wantPath string
		wantBody map[string]any
	}{
		{
			name:     "new comment defaults side to RIGHT",
			in:       github.CommentInput{CommitID: "head1", Path: "a.go", Line: 3, Body: "x"},
			wantPath: "/repos/o/r/pulls/12/comments",
			wantBody: map[string]any{"body": "x", "commit_id": "head1", "path": "a.go", "line": float64(3), "side": "RIGHT"},
		},
		{
			name:     "explicit side",
			in:       github.CommentInput{CommitID: "head1", Path: "a.go", Line: 3, Side: "LEFT", Body: "x"},
			wantPath: "/repos/o/r/pulls/12/comments",
			wantBody: map[string]any{"body": "x", "commit_id": "head1", "path": "a.go", "line": float64(3), "side": "LEFT"},
		},
		{
			name:     "reply",
			in:       github.CommentInput{CommitID: "head1", Path: "a.go", Line: 3, Body: "x", InReplyTo: &reply},
			wantPath: "/repos/o/r/pulls/12/comments/5/replies",
			wantBody: map[string]any{"body": "x"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 201, commentReply()) })
			got, err := newClient(s, github.Options{}).CreateReviewComment(t.Context(), "o", "r", 12, tc.in)
			if err != nil {
				t.Fatalf("CreateReviewComment() error: %v", err)
			}
			if got.ID != 9 || got.User != "me" {
				t.Errorf("CreateReviewComment() = %+v, want the mapped created comment", got)
			}
			req, body := s.request(0)
			if req.Method != http.MethodPost || req.URL.Path != tc.wantPath {
				t.Errorf("request = %s %s, want POST %s", req.Method, req.URL.Path, tc.wantPath)
			}
			var sent map[string]any
			if err := json.Unmarshal([]byte(body), &sent); err != nil {
				t.Fatalf("request body is not JSON: %v", err)
			}
			if diff := cmp.Diff(tc.wantBody, sent); diff != "" {
				t.Errorf("request body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCreateReviewComment_isNeverRetried(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		body       map[string]any
		wantMsg    string
		wantStatus int
	}{
		{"422 validation", 422, map[string]any{"message": "Validation Failed", "errors": []any{map[string]any{"message": "line must be part of the diff"}}}, "Validation Failed: line must be part of the diff", 422},
		{"503", 503, map[string]any{"message": "Service Unavailable"}, "Service Unavailable", 503},
		{"429", 429, map[string]any{"message": "slow down"}, "slow down", 429},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tc.status, tc.body) })
			_, err := newClient(s, github.Options{}).CreateReviewComment(t.Context(), "o", "r", 1,
				github.CommentInput{CommitID: "h", Path: "a", Line: 1, Body: "x"})
			var se *github.StatusError
			if !errors.As(err, &se) || se.Status != tc.wantStatus || se.Message != tc.wantMsg {
				t.Fatalf("CreateReviewComment() error = %v, want StatusError{%d, %q}", err, tc.wantStatus, tc.wantMsg)
			}
			if s.count() != 1 {
				t.Errorf("stub received %d requests, want exactly 1", s.count())
			}
		})
	}
}

func TestStatusError_withoutJSONBodyUsesStatusText(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	})
	c := newClient(s, github.Options{Retry: &retry.Options{}})
	_, err := c.CurrentLogin(t.Context())
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != 502 || se.Message != "Bad Gateway" {
		t.Errorf("CurrentLogin() error = %v, want StatusError{502, Bad Gateway}", err)
	}
}

func TestClient_neverLeaksToken(t *testing.T) {
	t.Parallel()
	echoing := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		// A hostile or buggy server echoing the credential back in its message.
		writeJSON(w, 401, map[string]any{"message": "Bad credentials for " + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	})
	_, err := newClient(echoing, github.Options{}).CurrentLogin(t.Context())
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("CurrentLogin() error = %v, want an error without the token", err)
	}
	var se *github.StatusError
	if errors.As(err, &se) && strings.Contains(se.Message, testToken) {
		t.Errorf("StatusError.Message leaks the token: %q", se.Message)
	}

	closed := newStub(t, func(w http.ResponseWriter, r *http.Request) {})
	c := newClient(closed, github.Options{Retry: &retry.Options{}})
	closed.Close()
	_, err = c.CurrentLogin(t.Context())
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("CurrentLogin() on a closed server error = %v, want an error without the token", err)
	}

	bad := newStub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{not json")) })
	_, err = newClient(bad, github.Options{}).CurrentLogin(t.Context())
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("CurrentLogin() on a bad body error = %v, want an error without the token", err)
	}
}

func TestCurrentUser_scopes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header func(http.Header)
		want   github.User
	}{
		{"classic token lists scopes", func(h http.Header) { h.Set("X-OAuth-Scopes", "repo, read:org") },
			github.User{Login: "ann", Scopes: []string{"repo", "read:org"}, ScopesReported: true}},
		{"empty header is reported without scopes", func(h http.Header) { h.Set("X-OAuth-Scopes", "") },
			github.User{Login: "ann", Scopes: []string{}, ScopesReported: true}},
		{"absent header is not reported", func(http.Header) {},
			github.User{Login: "ann", Scopes: []string{}, ScopesReported: false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				tc.header(w.Header())
				writeJSON(w, 200, map[string]any{"login": "ann"})
			})
			got, err := newClient(s, github.Options{}).CurrentUser(t.Context())
			if err != nil {
				t.Fatalf("CurrentUser() error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("CurrentUser() mismatch (-want +got):\n%s", diff)
			}
			req, _ := s.request(0)
			if req.URL.Path != "/user" || s.count() != 1 {
				t.Errorf("CurrentUser() sent %d requests, first to %q; want one to /user", s.count(), req.URL.Path)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer "+testToken {
				t.Errorf("Authorization = %q, want the bearer token", got)
			}
		})
	}
}

func TestCurrentUser_noToken(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(http.ResponseWriter, *http.Request) { t.Error("a request was sent without a token") })
	_, err := newClient(s, github.Options{Token: func() string { return " " }}).CurrentUser(t.Context())
	if !errors.Is(err, github.ErrNoToken) {
		t.Errorf("CurrentUser() error = %v, want ErrNoToken", err)
	}
}

func TestCurrentUser_doesNotFollowRedirect(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusFound, http.StatusMovedPermanently, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			other := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-OAuth-Scopes", "repo")
				writeJSON(w, 200, map[string]any{"login": "evil"})
			})
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL+"/user", status)
			})
			got, err := newClient(s, github.Options{}).CurrentUser(t.Context())
			var se *github.StatusError
			if !errors.As(err, &se) || se.Status != status {
				t.Fatalf("CurrentUser() = (%+v, %v), want StatusError{%d}", got, err, status)
			}
			if n := other.count(); n != 0 {
				t.Errorf("redirect target received %d requests, want 0 (the token must not leak)", n)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("CurrentUser() error leaks the token: %v", err)
			}
		})
	}
}

func TestCurrentUser_errorsNeverLeakToken(t *testing.T) {
	t.Parallel()
	echoing := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 401, map[string]any{"message": "Bad credentials for " + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	})
	_, err := newClient(echoing, github.Options{}).CurrentUser(t.Context())
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != 401 || strings.Contains(err.Error(), testToken) || strings.Contains(se.Message, testToken) {
		t.Errorf("CurrentUser() error = %v, want a 401 StatusError without the token", err)
	}

	closed := newStub(t, func(http.ResponseWriter, *http.Request) {})
	c := newClient(closed, github.Options{Retry: &retry.Options{}})
	closed.Close()
	if _, err := c.CurrentUser(t.Context()); err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("CurrentUser() on a closed server error = %v, want an error without the token", err)
	}

	bad := newStub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{not json")) })
	if _, err := newClient(bad, github.Options{}).CurrentUser(t.Context()); err == nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("CurrentUser() on a bad body error = %v, want an error without the token", err)
	}
}

func TestNew_panicsWithoutTokenFunc(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("New(Options{}) did not panic")
		}
	}()
	github.New(github.Options{})
}
