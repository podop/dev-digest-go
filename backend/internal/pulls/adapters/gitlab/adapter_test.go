package gitlab_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	glclient "dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
	adapter "dev-digest-go/backend/internal/pulls/adapters/gitlab"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

const (
	token  = "glpat-AbCdEfGhIjKlMnOpQrSt"
	host   = "git.corp.example"
	prefix = "/gl"
)

var repo = domain.Repo{ID: "r1", Owner: "acme/team", Name: "web", Provider: domain.ProviderGitLab, Host: host, ProjectID: 42}

// server is a recording GitLab TLS double mounted under a path prefix.
type server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []string // "METHOD path?query"
	hdrs []http.Header
}

func newServer(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.RequestURI())
		s.hdrs = append(s.hdrs, r.Header.Clone())
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

// adapterFor builds an Adapter whose resolver knows host with the given token.
func (s *server) adapterFor(tok string) *adapter.Adapter {
	hc := s.Client()
	c := glclient.New(glclient.Options{HTTP: hc, Retry: &retry.Options{}})
	return adapter.New(c, adapter.ResolverFunc(func(_ context.Context, h string) (adapter.Conn, bool, error) {
		if h != host {
			return adapter.Conn{}, false, nil
		}
		return adapter.Conn{ID: "c1", Name: "Corp GitLab", BaseURL: s.URL + prefix, Host: host, Token: tok}, true, nil
	}))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestProject(t *testing.T) {
	tests := []struct {
		name   string
		branch any
		want   string
	}{
		{"default branch kept", "develop", "develop"},
		{"empty repository reads as main", nil, "main"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, map[string]any{
					"id": 42, "path": "web-renamed", "path_with_namespace": "acme/team/web-renamed",
					"default_branch": tc.branch, "web_url": "https://evil.example/phish",
					"namespace": map[string]any{"full_path": "acme/team"},
				})
			})
			got, err := srv.adapterFor(token).Project(t.Context(), repo)
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			want := domain.GitLabProject{
				ID: 42, FullName: "acme/team/web-renamed", Owner: "acme/team", Name: "web-renamed",
				DefaultBranch: tc.want, WebURL: srv.URL + prefix + "/acme/team/web-renamed",
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Project() mismatch (-want +got):\n%s", diff)
			}
			reqs := srv.requests()
			if len(reqs) != 1 || reqs[0] != "GET "+prefix+"/api/v4/projects/42" {
				t.Errorf("Project() requests = %v, want one GET %s/api/v4/projects/42 (by id, FR17)", reqs, prefix)
			}
			if got := srv.hdrs[0].Get("PRIVATE-TOKEN"); got != token {
				t.Errorf("Project() PRIVATE-TOKEN = %q, want the connection token", got)
			}
		})
	}
}

func TestResolve_failuresSendNothing(t *testing.T) {
	boom := errors.New("db down")
	srv := newServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	c := glclient.New(glclient.Options{HTTP: srv.Client(), Retry: &retry.Options{}})
	resolver := func(conn adapter.Conn, ok bool, err error) adapter.Resolver {
		return adapter.ResolverFunc(func(context.Context, string) (adapter.Conn, bool, error) { return conn, ok, err })
	}
	tests := []struct {
		name string
		repo domain.Repo
		res  adapter.Resolver
		is   func(error) bool
	}{
		{"no connection for the host", repo, resolver(adapter.Conn{}, false, nil),
			func(err error) bool { return errors.Is(err, app.ErrNotConnected) }},
		{"connection without a token", repo, resolver(adapter.Conn{Name: "Corp GitLab", BaseURL: srv.URL}, true, nil),
			func(err error) bool {
				var nt *app.NoTokenError
				return errors.As(err, &nt) && nt.Conn == "Corp GitLab" && nt.Error() == "gitlab connection Corp GitLab has no token"
			}},
		{"blank token", repo, resolver(adapter.Conn{Name: "Corp GitLab", BaseURL: srv.URL, Token: "  "}, true, nil),
			func(err error) bool { var nt *app.NoTokenError; return errors.As(err, &nt) }},
		{"resolver error", repo, resolver(adapter.Conn{}, false, boom),
			func(err error) bool { return errors.Is(err, boom) && !errors.Is(err, app.ErrNotConnected) }},
		{"GitHub repository", domain.Repo{ID: "g", Owner: "a", Name: "b"}, resolver(adapter.Conn{}, true, nil),
			func(err error) bool { return err != nil }},
		{"GitLab repository without project id", domain.Repo{ID: "g", Provider: "gitlab", Host: host}, resolver(adapter.Conn{}, true, nil),
			func(err error) bool { return err != nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := adapter.New(c, tc.res)
			ctx := t.Context()
			calls := map[string]error{}
			_, calls["Project"] = a.Project(ctx, tc.repo)
			_, calls["ListPage"] = a.ListPage(ctx, tc.repo, 1, 50)
			_, calls["Get"] = a.Get(ctx, tc.repo, 1)
			_, calls["Issue"] = a.Issue(ctx, tc.repo, 1)
			_, calls["Stats"] = a.Stats(ctx, tc.repo, []int{1})
			for name, err := range calls {
				if !tc.is(err) {
					t.Errorf("%s() error = %v, mismatch for case %q", name, err, tc.name)
				}
			}
		})
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("requests sent = %d, want 0", n)
	}
}

func TestListPage(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []map[string]any{
			{ // open, same-project source, merged_at/closed_at leftovers after a reopen
				"iid": 7, "title": "Add x", "state": "opened", "draft": true, "source_branch": "feat", "target_branch": "main",
				"sha": "aaa", "source_project_id": 42, "target_project_id": 42, "created_at": "2026-10-20T10:00:00Z",
				"updated_at": "2026-10-21T10:00:00Z", "merged_at": "2026-10-19T10:00:00Z", "closed_at": "2026-10-19T11:00:00Z",
				"author": map[string]any{"username": "ann"},
			},
			{ // no head sha: skipped
				"iid": 6, "title": "Empty", "state": "opened", "source_branch": "e", "target_branch": "main", "sha": "",
				"created_at": "2026-10-20T10:00:00Z", "updated_at": "2026-10-20T10:00:00Z",
			},
			{ // merged from a fork, no author
				"iid": 5, "title": "Fork", "state": "merged", "source_branch": "f", "target_branch": "main", "sha": "bbb",
				"source_project_id": 99, "target_project_id": 42, "created_at": "2026-10-10T10:00:00Z",
				"updated_at": "2026-10-11T10:00:00Z", "merged_at": "2026-10-11T09:00:00Z", "closed_at": "2026-10-11T09:30:00Z",
			},
			{ // locked reads as closed
				"iid": 4, "title": "Locked", "state": "locked", "source_branch": "l", "target_branch": "main", "sha": "ccc",
				"source_project_id": 42, "target_project_id": 42, "created_at": "2026-10-09T10:00:00Z",
				"updated_at": "2026-10-09T11:00:00Z", "closed_at": "2026-10-09T11:00:00Z", "author": map[string]any{"username": "bob"},
			},
		})
	})
	got, err := srv.adapterFor(token).ListPage(t.Context(), repo, 2, 50)
	if err != nil {
		t.Fatalf("ListPage() error = %v", err)
	}
	want := []domain.Listed{
		{Number: 7, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "aaa", Status: domain.StatusOpen,
			OpenedAt: ts("2026-10-20T10:00:00Z"), UpdatedAt: ts("2026-10-21T10:00:00Z"), Draft: true},
		{Number: 5, Title: "Fork", Author: "unknown", Branch: "f", Base: "main", HeadSHA: "bbb", Status: domain.StatusMerged,
			OpenedAt: ts("2026-10-10T10:00:00Z"), UpdatedAt: ts("2026-10-11T10:00:00Z"),
			MergedAt: ts("2026-10-11T09:00:00Z"), ClosedAt: ts("2026-10-11T09:30:00Z"), HeadIsFork: true},
		{Number: 4, Title: "Locked", Author: "bob", Branch: "l", Base: "main", HeadSHA: "ccc", Status: domain.StatusClosed,
			OpenedAt: ts("2026-10-09T10:00:00Z"), UpdatedAt: ts("2026-10-09T11:00:00Z"), ClosedAt: ts("2026-10-09T11:00:00Z")},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListPage() mismatch (-want +got):\n%s", diff)
	}
	wantReq := "GET " + prefix + "/api/v4/projects/42/merge_requests?order_by=updated_at&page=2&per_page=50&sort=desc&state=all"
	if reqs := srv.requests(); len(reqs) != 1 || reqs[0] != wantReq {
		t.Errorf("ListPage() requests = %v, want [%s]", reqs, wantReq)
	}
}

// detailServer answers the five calls of Get; graphql is the stats answer.
func detailServer(t *testing.T, graphql func(w http.ResponseWriter)) *server {
	t.Helper()
	return newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case prefix + "/api/v4/projects/42/merge_requests/7":
			writeJSON(w, 200, map[string]any{
				"iid": 7, "title": "Add x", "description": "Closes #3", "state": "opened", "source_branch": "feat",
				"target_branch": "main", "sha": "aaa", "source_project_id": 42, "target_project_id": 42,
				"created_at": "2026-10-20T10:00:00Z", "updated_at": "2026-10-21T10:00:00Z",
				"author": map[string]any{"username": "ann"}, "web_url": "https://evil.example/x",
			})
		case prefix + "/api/v4/projects/42/merge_requests/7/diffs":
			writeJSON(w, 200, []map[string]any{
				{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1 +1,2 @@\n-old\n+new\n+more\n"},
				{"old_path": "gone.go", "new_path": "gone.go", "deleted_file": true, "diff": "@@ -1 +0,0 @@\n-x\n"},
				{"old_path": "big.bin", "new_path": "big.bin", "too_large": true},
			})
		case prefix + "/api/v4/projects/42/merge_requests/7/commits": // newest first
			writeJSON(w, 200, []map[string]any{
				{"id": "c3", "message": "third", "author_name": "ann", "committed_date": "2026-10-20T12:00:00Z"},
				{"id": "c2", "message": "second", "author_name": "", "committed_date": "2026-10-20T11:00:00Z"},
				{"id": "c1", "message": "first", "author_name": "bob", "committed_date": "2026-10-20T10:00:00Z"},
			})
		case prefix + "/api/graphql":
			graphql(w)
		default:
			t.Errorf("unexpected request %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
}

func TestGet(t *testing.T) {
	files := []domain.File{
		{Path: "a.go", Additions: 2, Deletions: 1, Patch: ptr("@@ -1 +1,2 @@\n-old\n+new\n+more\n")},
		{Path: "gone.go", Additions: 0, Deletions: 1, Patch: ptr("@@ -1 +0,0 @@\n-x\n")},
		{Path: "big.bin"},
	}
	commits := []domain.Commit{ // newest first, as the client returns them; pulls/app reorders
		{SHA: "c3", Message: "third", Author: "ann", CommittedAt: ts("2026-10-20T12:00:00Z")},
		{SHA: "c2", Message: "second", Author: "unknown", CommittedAt: ts("2026-10-20T11:00:00Z")},
		{SHA: "c1", Message: "first", Author: "bob", CommittedAt: ts("2026-10-20T10:00:00Z")},
	}
	listed := domain.Listed{Number: 7, Title: "Add x", Author: "ann", Branch: "feat", Base: "main", HeadSHA: "aaa",
		Status: domain.StatusOpen, OpenedAt: ts("2026-10-20T10:00:00Z"), UpdatedAt: ts("2026-10-21T10:00:00Z")}

	tests := []struct {
		name    string
		graphql func(w http.ResponseWriter)
		stats   domain.DiffStats
	}{
		{"stats from the GraphQL summary", func(w http.ResponseWriter) {
			writeJSON(w, 200, map[string]any{"data": map[string]any{"projects": map[string]any{"nodes": []map[string]any{{
				"id": "gid://gitlab/Project/42", "mergeRequests": map[string]any{"nodes": []map[string]any{
					{"iid": "7", "diffStatsSummary": map[string]any{"additions": 30, "deletions": 4, "fileCount": 5}},
				}},
			}}}}})
		}, domain.DiffStats{Additions: 30, Deletions: 4, FilesCount: 5}},
		{"no summary: sums of the files", func(w http.ResponseWriter) {
			writeJSON(w, 200, map[string]any{"data": map[string]any{"projects": map[string]any{"nodes": []map[string]any{}}}})
		}, domain.DiffStats{Additions: 2, Deletions: 2, FilesCount: 3}},
		{"GraphQL failure: sums of the files", func(w http.ResponseWriter) {
			writeJSON(w, 500, map[string]any{"message": "boom"})
		}, domain.DiffStats{Additions: 2, Deletions: 2, FilesCount: 3}},
		{"GraphQL errors: sums of the files", func(w http.ResponseWriter) {
			writeJSON(w, 200, map[string]any{"errors": []map[string]any{{"message": "disabled"}}})
		}, domain.DiffStats{Additions: 2, Deletions: 2, FilesCount: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := detailServer(t, tc.graphql)
			got, err := srv.adapterFor(token).Get(t.Context(), repo, 7)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			want := domain.Fetched{Listed: listed, Body: ptr("Closes #3"), DiffStats: tc.stats, Files: files, Commits: commits}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Get() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGet_failures(t *testing.T) {
	for _, failing := range []string{"", "/diffs", "/commits"} {
		t.Run("fails at "+failing, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/merge_requests/7"+failing) {
					writeJSON(w, 404, map[string]any{"message": "404 Not found"})
					return
				}
				writeJSON(w, 200, []any{})
			})
			_, err := srv.adapterFor(token).Get(t.Context(), repo, 7)
			var ge *app.GitLabError
			if failing == "" {
				if !errors.As(err, &ge) || ge.Status != 404 {
					t.Fatalf("Get() error = %v, want *app.GitLabError 404", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Get() error = nil, want the %s failure", failing)
			}
		})
	}
}

func TestIssue(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"iid": 3, "title": "Bug", "description": "details", "state": "opened"})
	})
	got, err := srv.adapterFor(token).Issue(t.Context(), repo, 3)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	want := domain.Issue{Number: 3, Title: "Bug", Body: ptr("details"), State: "open"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Issue() mismatch (-want +got):\n%s", diff)
	}
	if reqs := srv.requests(); len(reqs) != 1 || reqs[0] != "GET "+prefix+"/api/v4/projects/42/issues/3" {
		t.Errorf("Issue() requests = %v", reqs)
	}
}

func TestStats(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != prefix+"/api/graphql" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s, want POST %s/api/graphql", r.Method, r.URL.Path, prefix)
		}
		writeJSON(w, 200, map[string]any{"data": map[string]any{"projects": map[string]any{"nodes": []map[string]any{{
			"id": "gid://gitlab/Project/42", "mergeRequests": map[string]any{"nodes": []map[string]any{
				{"iid": "1", "diffStatsSummary": map[string]any{"additions": 5, "deletions": 1, "fileCount": 2}},
				{"iid": "2", "diffStatsSummary": map[string]any{"additions": 0, "deletions": 0, "fileCount": 0}},
			}},
		}}}}})
	})
	a := srv.adapterFor(token)
	got, err := a.Stats(t.Context(), repo, []int{1, 2, 3})
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	want := map[int]domain.DiffStats{1: {Additions: 5, Deletions: 1, FilesCount: 2}}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Stats() mismatch (-want +got; all-zero and missing summaries are absent):\n%s", diff)
	}

	tooMany := make([]int, domain.GitLabStatsBatch+1)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	before := len(srv.requests())
	if _, err := a.Stats(t.Context(), repo, tooMany); err == nil {
		t.Errorf("Stats(%d numbers) error = nil, want an error", len(tooMany))
	}
	if n := len(srv.requests()); n != before {
		t.Errorf("Stats(%d numbers) sent %d requests, want none", len(tooMany), n-before)
	}
}

func TestErrors(t *testing.T) {
	reset := time.Now().Add(2 * time.Minute).Unix()
	tests := []struct {
		name   string
		status int
		header map[string]string
		check  func(t *testing.T, ge *app.GitLabError)
	}{
		{"429 with Retry-After", 429, map[string]string{"Retry-After": "120"}, func(t *testing.T, ge *app.GitLabError) {
			at, limited := ge.RateLimit()
			if !limited || time.Until(at) < time.Minute || time.Until(at) > 3*time.Minute {
				t.Errorf("RateLimit() = %v, %v, want a limit ending in about 2 minutes", at, limited)
			}
		}},
		{"429 with only RateLimit-Reset", 429, map[string]string{"RateLimit-Reset": itoa(reset)}, func(t *testing.T, ge *app.GitLabError) {
			at, limited := ge.RateLimit()
			if !limited || at.Unix() != reset {
				t.Errorf("RateLimit() = %v, %v, want the reset time %d", at, limited, reset)
			}
		}},
		{"429 without headers", 429, nil, func(t *testing.T, ge *app.GitLabError) {
			if at, limited := ge.RateLimit(); !limited || !at.IsZero() {
				t.Errorf("RateLimit() = %v, %v, want a limit with the zero time", at, limited)
			}
		}},
		{"401 is no rate limit", 401, nil, func(t *testing.T, ge *app.GitLabError) {
			if _, limited := ge.RateLimit(); limited || ge.Status != 401 {
				t.Errorf("error = %+v, want status 401 and no rate limit", ge)
			}
		}},
		{"503 is no rate limit", 503, nil, func(t *testing.T, ge *app.GitLabError) {
			if _, limited := ge.RateLimit(); limited || ge.Status != 503 {
				t.Errorf("error = %+v, want status 503 and no rate limit", ge)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				writeJSON(w, tc.status, map[string]any{"message": "denied for " + token})
			})
			_, err := srv.adapterFor(token).Project(t.Context(), repo)
			var ge *app.GitLabError
			if !errors.As(err, &ge) {
				t.Fatalf("Project() error = %v, want *app.GitLabError", err)
			}
			if ge.Status != tc.status {
				t.Errorf("Status = %d, want %d", ge.Status, tc.status)
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("error text %q holds the token", err.Error())
			}
			tc.check(t, ge)
		})
	}
}

func TestNetworkErrorPassesThrough(t *testing.T) {
	srv := newServer(t, func(http.ResponseWriter, *http.Request) {})
	a := srv.adapterFor(token)
	srv.Close()
	_, err := a.Project(t.Context(), repo)
	var ge *app.GitLabError
	if err == nil || errors.As(err, &ge) {
		t.Errorf("Project() error = %v, want a non-status error", err)
	}
}

func ptr[T any](v T) *T { return &v }

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
