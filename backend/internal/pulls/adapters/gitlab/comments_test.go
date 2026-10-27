package gitlab_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	glclient "dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
	adapter "dev-digest-go/backend/internal/pulls/adapters/gitlab"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

const (
	mrPath    = prefix + "/api/v4/projects/42/merge_requests/7"
	discID    = "6a9c1a8b0d3f4e5a7b2c9d8e1f0a3b4c5d6e7f80"
	headSHA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	baseSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	startSHA  = "cccccccccccccccccccccccccccccccccccccccc"
	wantAudit = "must not hold the token"
)

// retryingAdapter has the default read policy (3 retries, 1 ms backoff), to show that writes are
// still sent once.
func (s *server) retryingAdapter() *adapter.Adapter {
	c := glclient.New(glclient.Options{HTTP: s.Client(), Retry: &retry.Options{Retries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}})
	return adapter.New(c, adapter.ResolverFunc(func(context.Context, string) (adapter.Conn, bool, error) {
		return adapter.Conn{ID: "c1", Name: "Corp GitLab", BaseURL: s.URL + prefix, Host: host, Token: token}, true, nil
	}))
}

// hang reads the request body (the server notices a closed connection only then) and answers
// nothing until the client gives up; the timer only bounds a broken test.
func hang(_ http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(10 * time.Second):
	}
}

func noToken(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), token) {
		t.Errorf("%s error text %q %s", what, err.Error(), wantAudit)
	}
}

func apiNote(id int, body string, pos map[string]any) map[string]any {
	n := map[string]any{
		"id": id, "type": "DiffNote", "system": false, "body": body, "created_at": "2026-10-27T10:00:00.000Z",
		"author": map[string]any{"username": "ann"},
	}
	if pos != nil {
		n["position"] = pos
	}
	return n
}

func apiPos() map[string]any {
	return map[string]any{
		"position_type": "text", "base_sha": baseSHA, "start_sha": startSHA, "head_sha": headSHA,
		"old_path": "old/a.go", "new_path": "a.go", "old_line": nil, "new_line": 12,
	}
}

func domainPos() domain.Position {
	n := 12
	return domain.Position{
		Type: "text", BaseSHA: baseSHA, StartSHA: startSHA, HeadSHA: headSHA,
		OldPath: "old/a.go", NewPath: "a.go", NewLine: &n,
	}
}

func TestReady(t *testing.T) {
	srv := newServer(t, func(http.ResponseWriter, *http.Request) { t.Error("Ready sent a request") })
	if err := srv.adapterFor(token).Ready(t.Context(), repo); err != nil {
		t.Errorf("Ready() error = %v, want nil", err)
	}
	if err := srv.adapterFor("").Ready(t.Context(), repo); err == nil {
		t.Errorf("Ready() without a token = nil, want *app.NoTokenError")
	} else if nt := new(app.NoTokenError); !errors.As(err, &nt) {
		t.Errorf("Ready() without a token error = %v, want *app.NoTokenError", err)
	}
	other := repo
	other.Host = "other.example"
	if err := srv.adapterFor(token).Ready(t.Context(), other); !errors.Is(err, app.ErrNotConnected) {
		t.Errorf("Ready() for an unknown host error = %v, want app.ErrNotConnected", err)
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("Ready() sent %d requests, want 0", n)
	}
}

func TestComments_beforeAnyRequest(t *testing.T) {
	srv := newServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	other := repo
	other.Host = "other.example"
	tests := []struct {
		name string
		a    *adapter.Adapter
		repo domain.Repo
		is   func(error) bool
	}{
		{"not connected", srv.adapterFor(token), other, func(err error) bool { return errors.Is(err, app.ErrNotConnected) }},
		{"no token", srv.adapterFor(""), repo, func(err error) bool { var nt *app.NoTokenError; return errors.As(err, &nt) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			calls := map[string]error{}
			_, _, calls["Discussions"] = tc.a.Discussions(ctx, tc.repo, 7, 1)
			_, calls["MergeRequestRefs"] = tc.a.MergeRequestRefs(ctx, tc.repo, 7)
			_, _, calls["LatestVersion"] = tc.a.LatestVersion(ctx, tc.repo, 7)
			_, calls["CreateDiscussion"] = tc.a.CreateDiscussion(ctx, tc.repo, 7, "x", domainPos())
			_, calls["CreateNote"] = tc.a.CreateNote(ctx, tc.repo, 7, discID, "x")
			for name, err := range calls {
				if !tc.is(err) {
					t.Errorf("%s() error = %v, mismatch for %q", name, err, tc.name)
				}
			}
		})
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("requests sent = %d, want 0", n)
	}
}

func TestDiscussions(t *testing.T) {
	page := func(n int) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = map[string]any{"id": discID, "notes": []map[string]any{apiNote(i+1, "hi", apiPos())}}
		}
		return out
	}
	tests := []struct {
		name     string
		n        int
		wantMore bool
	}{
		{"full page may have a next one", 100, true},
		{"short page is the last", 99, false},
		{"empty page", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, page(tc.n)) })
			got, more, err := srv.adapterFor(token).Discussions(t.Context(), repo, 7, 3)
			if err != nil {
				t.Fatalf("Discussions() error = %v", err)
			}
			if len(got) != tc.n || more != tc.wantMore {
				t.Errorf("Discussions() = %d discussions, more=%v, want %d, %v", len(got), more, tc.n, tc.wantMore)
			}
			want := "GET " + mrPath + "/discussions?page=3&per_page=100"
			if reqs := srv.requests(); len(reqs) != 1 || reqs[0] != want {
				t.Errorf("Discussions() requests = %v, want [%s]", reqs, want)
			}
		})
	}
}

func TestDiscussions_mapsTypes(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		reply := apiNote(2, "reply", nil)
		reply["type"] = "DiscussionNote"
		reply["author"] = nil
		writeJSON(w, 200, []map[string]any{{"id": discID, "notes": []map[string]any{apiNote(1, "root", apiPos()), reply}}})
	})
	got, _, err := srv.adapterFor(token).Discussions(t.Context(), repo, 7, 1)
	if err != nil {
		t.Fatalf("Discussions() error = %v", err)
	}
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	want := []domain.Discussion{{ID: discID, Notes: []domain.Note{
		{ID: 1, Type: "DiffNote", Body: "root", Author: "ann", CreatedAt: at, Position: &domain.Position{
			Type: "text", BaseSHA: baseSHA, StartSHA: startSHA, HeadSHA: headSHA,
			OldPath: "old/a.go", NewPath: "a.go", NewLine: ptr(12)}},
		{ID: 2, Type: "DiscussionNote", Body: "reply", Author: "unknown", CreatedAt: at},
	}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Discussions() mismatch (-want +got):\n%s", diff)
	}
}

func TestMergeRequestRefs(t *testing.T) {
	tests := []struct {
		name string
		refs any
		want domain.DiffRefs
	}{
		{"all three", map[string]any{"base_sha": baseSHA, "start_sha": startSHA, "head_sha": headSHA},
			domain.DiffRefs{BaseSHA: baseSHA, StartSHA: startSHA, HeadSHA: headSHA}},
		{"partial", map[string]any{"head_sha": headSHA}, domain.DiffRefs{HeadSHA: headSHA}},
		{"null diff_refs", nil, domain.DiffRefs{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, map[string]any{"iid": 7, "title": "x", "state": "opened", "sha": headSHA, "diff_refs": tc.refs})
			})
			got, err := srv.adapterFor(token).MergeRequestRefs(t.Context(), repo, 7)
			if err != nil {
				t.Fatalf("MergeRequestRefs() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("MergeRequestRefs() mismatch (-want +got):\n%s", diff)
			}
			if reqs := srv.requests(); len(reqs) != 1 || reqs[0] != "GET "+mrPath {
				t.Errorf("MergeRequestRefs() requests = %v, want one GET %s", reqs, mrPath)
			}
		})
	}
}

func TestLatestVersion(t *testing.T) {
	tests := []struct {
		name   string
		body   []map[string]any
		want   domain.DiffRefs
		wantOK bool
	}{
		{"newest first", []map[string]any{
			{"id": 9, "base_commit_sha": baseSHA, "start_commit_sha": startSHA, "head_commit_sha": headSHA},
			{"id": 8, "base_commit_sha": "x", "start_commit_sha": "y", "head_commit_sha": "z"},
		}, domain.DiffRefs{BaseSHA: baseSHA, StartSHA: startSHA, HeadSHA: headSHA}, true},
		{"no version yet", []map[string]any{}, domain.DiffRefs{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, tc.body) })
			got, ok, err := srv.adapterFor(token).LatestVersion(t.Context(), repo, 7)
			if err != nil {
				t.Fatalf("LatestVersion() error = %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("LatestVersion() ok = %v, want %v", ok, tc.wantOK)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("LatestVersion() mismatch (-want +got):\n%s", diff)
			}
			want := "GET " + mrPath + "/versions?page=1&per_page=1"
			if reqs := srv.requests(); len(reqs) != 1 || reqs[0] != want {
				t.Errorf("LatestVersion() requests = %v, want [%s]", reqs, want)
			}
		})
	}
}

func TestCreateDiscussion(t *testing.T) {
	var body map[string]any
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body %q is not JSON: %v", raw, err)
		}
		writeJSON(w, 201, map[string]any{"id": discID, "notes": []map[string]any{apiNote(5, "hello", apiPos())}})
	})
	got, err := srv.adapterFor(token).CreateDiscussion(t.Context(), repo, 7, "hello", domainPos())
	if err != nil {
		t.Fatalf("CreateDiscussion() error = %v", err)
	}
	if got.ID != discID || len(got.Notes) != 1 || got.Notes[0].ID != 5 || got.Notes[0].Body != "hello" {
		t.Errorf("CreateDiscussion() = %+v, want discussion %s with note 5", got, discID)
	}
	wantBody := map[string]any{"body": "hello", "position": map[string]any{
		"position_type": "text", "base_sha": baseSHA, "start_sha": startSHA, "head_sha": headSHA,
		"old_path": "old/a.go", "new_path": "a.go", "new_line": float64(12),
	}}
	if diff := cmp.Diff(wantBody, body); diff != "" {
		t.Errorf("CreateDiscussion() request body mismatch (-want +got):\n%s", diff)
	}
	if reqs := srv.requests(); len(reqs) != 1 || reqs[0] != "POST "+mrPath+"/discussions" {
		t.Errorf("CreateDiscussion() requests = %v, want one POST %s/discussions", reqs, mrPath)
	}
	if got := srv.hdrs[0].Get("PRIVATE-TOKEN"); got != token {
		t.Errorf("CreateDiscussion() PRIVATE-TOKEN = %q, want the connection token", got)
	}
}

func TestCreateNote(t *testing.T) {
	var body map[string]any
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		n := apiNote(6, "thanks", nil)
		n["type"] = "DiscussionNote"
		writeJSON(w, 201, n)
	})
	got, err := srv.adapterFor(token).CreateNote(t.Context(), repo, 7, discID, "thanks")
	if err != nil {
		t.Fatalf("CreateNote() error = %v", err)
	}
	want := domain.Note{ID: 6, Type: "DiscussionNote", Body: "thanks", Author: "ann", CreatedAt: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CreateNote() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]any{"body": "thanks"}, body); diff != "" {
		t.Errorf("CreateNote() request body mismatch (-want +got):\n%s", diff)
	}
	if reqs := srv.requests(); len(reqs) != 1 || reqs[0] != "POST "+mrPath+"/discussions/"+discID+"/notes" {
		t.Errorf("CreateNote() requests = %v", reqs)
	}
}

// A write is sent once whatever the retry policy of the client says (P4): a lost answer may hide a
// created comment. A read with the same policy is retried.
func TestWrites_neverRetried(t *testing.T) {
	for _, status := range []int{500, 503, 429} {
		t.Run(itoa(int64(status)), func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, status, map[string]any{"message": "try later " + token})
			})
			a := srv.retryingAdapter()
			_, err := a.CreateDiscussion(t.Context(), repo, 7, "x", domainPos())
			noToken(t, "CreateDiscussion()", err)
			_, err2 := a.CreateNote(t.Context(), repo, 7, discID, "x")
			noToken(t, "CreateNote()", err2)
			var ge *app.GitLabError
			if !errors.As(err, &ge) || !errors.As(err2, &ge) {
				t.Fatalf("errors = %v, %v, want *app.GitLabError", err, err2)
			}
			if n := len(srv.requests()); n != 2 {
				t.Errorf("requests = %d (%v), want 2: one per write", n, srv.requests())
			}
		})
	}
	t.Run("a read is retried", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 503, map[string]any{"message": "down"}) })
		_, _, err := srv.retryingAdapter().Discussions(t.Context(), repo, 7, 1)
		if err == nil || len(srv.requests()) != 4 {
			t.Errorf("Discussions() error = %v with %d requests, want an error after 4 attempts", err, len(srv.requests()))
		}
	})
}

func TestComments_statusErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		header    map[string]string
		msg       any
		wantRetry int
		wantScope bool
		limited   bool
	}{
		{name: "429 Retry-After 120", status: 429, header: map[string]string{"Retry-After": "120"}, msg: "slow", wantRetry: 120, limited: true},
		{name: "429 without header", status: 429, msg: "slow", wantRetry: 60, limited: true},
		{name: "403 insufficient scope", status: 403, msg: nil, wantScope: true},
		{name: "403 forbidden", status: 403, msg: "403 Forbidden"},
		{name: "401", status: 401, msg: "401 Unauthorized"},
		{name: "404", status: 404, msg: "404 Not found"},
		{name: "400 position", status: 400, msg: "position is invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				if tc.wantScope {
					writeJSON(w, tc.status, map[string]any{"error": "insufficient_scope", "error_description": "needs api " + token})
					return
				}
				writeJSON(w, tc.status, map[string]any{"message": tc.msg.(string) + " " + token})
			})
			a := srv.adapterFor(token)
			ctx := t.Context()
			calls := map[string]error{}
			_, _, calls["Discussions"] = a.Discussions(ctx, repo, 7, 1)
			_, calls["MergeRequestRefs"] = a.MergeRequestRefs(ctx, repo, 7)
			_, _, calls["LatestVersion"] = a.LatestVersion(ctx, repo, 7)
			_, calls["CreateDiscussion"] = a.CreateDiscussion(ctx, repo, 7, "x", domainPos())
			_, calls["CreateNote"] = a.CreateNote(ctx, repo, 7, discID, "x")
			for name, err := range calls {
				noToken(t, name+"()", err)
				var ge *app.GitLabError
				if !errors.As(err, &ge) {
					t.Errorf("%s() error = %v, want *app.GitLabError", name, err)
					continue
				}
				if ge.Conn != "Corp GitLab" {
					t.Errorf("%s() Conn = %q, want the connection's display name %q", name, ge.Conn, "Corp GitLab")
				}
				if ge.Status != tc.status || ge.RateLimited != tc.limited || ge.RetryAfterSeconds != tc.wantRetry || ge.InsufficientScope != tc.wantScope {
					t.Errorf("%s() = {Status:%d RateLimited:%v RetryAfterSeconds:%d InsufficientScope:%v}, want {%d %v %d %v}",
						name, ge.Status, ge.RateLimited, ge.RetryAfterSeconds, ge.InsufficientScope, tc.status, tc.limited, tc.wantRetry, tc.wantScope)
				}
			}
		})
	}
}

func TestComments_noAnswer(t *testing.T) {
	t.Run("redirect is never followed", func(t *testing.T) {
		target := newServer(t, func(http.ResponseWriter, *http.Request) { t.Error("the redirect was followed") })
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
		})
		a := srv.adapterFor(token)
		_, _, e1 := a.Discussions(t.Context(), repo, 7, 1)
		_, e2 := a.CreateDiscussion(t.Context(), repo, 7, "x", domainPos())
		_, e3 := a.CreateNote(t.Context(), repo, 7, discID, "x")
		for i, err := range []error{e1, e2, e3} {
			if !errors.Is(err, app.ErrGitLabRedirect) {
				t.Errorf("call %d error = %v, want app.ErrGitLabRedirect", i, err)
			}
			noToken(t, "redirect", err)
		}
	})
	t.Run("not the API", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>login "+token+"</html>")
		})
		_, err := srv.adapterFor(token).CreateNote(t.Context(), repo, 7, discID, "x")
		if !errors.Is(err, app.ErrGitLabUnavailable) {
			t.Errorf("CreateNote() error = %v, want app.ErrGitLabUnavailable", err)
		}
		noToken(t, "CreateNote()", err)
	})
	t.Run("a 2xx that is no discussion", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 201, map[string]any{"id": discID, "notes": []any{}})
		})
		_, err := srv.adapterFor(token).CreateDiscussion(t.Context(), repo, 7, "x", domainPos())
		if !errors.Is(err, app.ErrGitLabUnavailable) {
			t.Errorf("CreateDiscussion() error = %v, want app.ErrGitLabUnavailable", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		srv := newServer(t, hang)
		c := glclient.New(glclient.Options{HTTP: srv.Client(), Retry: &retry.Options{}, Timeout: 50 * time.Millisecond})
		a := adapter.New(c, adapter.ResolverFunc(func(context.Context, string) (adapter.Conn, bool, error) {
			return adapter.Conn{Name: "c", BaseURL: srv.URL + prefix, Host: host, Token: token}, true, nil
		}))
		_, err := a.CreateDiscussion(t.Context(), repo, 7, "x", domainPos())
		if !errors.Is(err, app.ErrGitLabUnavailable) {
			t.Errorf("CreateDiscussion() error = %v, want app.ErrGitLabUnavailable", err)
		}
		noToken(t, "CreateDiscussion()", err)
	})
	t.Run("TLS error", func(t *testing.T) {
		srv := newServer(t, func(http.ResponseWriter, *http.Request) {})
		c := glclient.New(glclient.Options{HTTP: &http.Client{}, Retry: &retry.Options{}}) // does not trust the test CA
		a := adapter.New(c, adapter.ResolverFunc(func(context.Context, string) (adapter.Conn, bool, error) {
			return adapter.Conn{Name: "c", BaseURL: srv.URL + prefix, Host: host, Token: token}, true, nil
		}))
		_, _, err := a.Discussions(t.Context(), repo, 7, 1)
		if !errors.Is(err, app.ErrGitLabUnavailable) {
			t.Errorf("Discussions() error = %v, want app.ErrGitLabUnavailable", err)
		}
		noToken(t, "Discussions()", err)
		if len(srv.requests()) != 0 {
			t.Errorf("a request reached the server despite the untrusted certificate")
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		srv := newServer(t, func(http.ResponseWriter, *http.Request) {})
		a := srv.adapterFor(token)
		srv.Close()
		_, err := a.CreateNote(t.Context(), repo, 7, discID, "x")
		if !errors.Is(err, app.ErrGitLabUnavailable) {
			t.Errorf("CreateNote() error = %v, want app.ErrGitLabUnavailable", err)
		}
		noToken(t, "CreateNote()", err)
	})
	t.Run("the caller's cancellation is not an outage", func(t *testing.T) {
		srv := newServer(t, hang)
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(50*time.Millisecond, cancel)
		_, err := srv.adapterFor(token).CreateNote(ctx, repo, 7, discID, "x")
		if !errors.Is(err, context.Canceled) || errors.Is(err, app.ErrGitLabUnavailable) {
			t.Errorf("CreateNote() error = %v, want context.Canceled and not app.ErrGitLabUnavailable", err)
		}
	})
}
