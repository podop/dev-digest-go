package gitlab_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
)

// testToken is a recognisable personal access token of more than 20 characters (NFR1).
const testToken = "glpat-AbCdEfGhIjKlMnOpQrSt"

type seen struct {
	Method, URI string
	Header      http.Header
	Body        string
}

// stub is a recording GitLab TLS double; the client trusts its certificate.
type stub struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newStub(t *testing.T, handler http.HandlerFunc) *stub {
	t.Helper()
	s := &stub{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, seen{r.Method, r.RequestURI, r.Header.Clone(), string(body)})
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *stub) req(i int) seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[i]
}

func (s *stub) conn() gitlab.Conn { return gitlab.Conn{BaseURL: s.URL + "/api/v4", Token: testToken} }

func (s *stub) client(opts gitlab.Options) *gitlab.Client {
	opts.HTTP = s.Client()
	if opts.Retry == nil {
		opts.Retry = &retry.Options{Retries: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
	}
	return gitlab.New(opts)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func okJSON(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, v) }
}

func TestNew_PanicsWithoutHTTPClient(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New(Options{}) did not panic; a default client would bypass the dial guard")
		}
	}()
	gitlab.New(gitlab.Options{})
}

func TestRequest_TokenInHeaderOnly(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"username": "alice"}))
	c := s.client(gitlab.Options{})
	u, err := c.User(t.Context(), s.conn())
	if err != nil {
		t.Fatalf("User() error = %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("User().Username = %q, want alice", u.Username)
	}
	r := s.req(0)
	if got := r.Header.Get("PRIVATE-TOKEN"); got != testToken {
		t.Errorf("PRIVATE-TOKEN = %q, want the token", got)
	}
	if r.Header.Get("Authorization") != "" {
		t.Errorf("Authorization header set: %q", r.Header.Get("Authorization"))
	}
	if r.Method != "GET" || r.URI != "/api/v4/user" {
		t.Errorf("request = %s %s, want GET /api/v4/user", r.Method, r.URI)
	}
	if strings.Contains(r.URI, testToken) {
		t.Errorf("token in request URI %q", r.URI)
	}
}

func TestRequest_AnonymousSendsNoTokenHeader(t *testing.T) {
	s := newStub(t, okJSON(projectJSON()))
	conn := s.conn()
	conn.Token = "  "
	if _, err := s.client(gitlab.Options{}).Project(t.Context(), conn, 7); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if _, ok := s.req(0).Header["Private-Token"]; ok {
		t.Errorf("PRIVATE-TOKEN sent for an empty token")
	}
}

func TestRequest_PathPrefixKept(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"version": "17.4.1"}))
	conn := gitlab.Conn{BaseURL: s.URL + "/gitlab/api/v4/", Token: testToken}
	m, err := s.client(gitlab.Options{}).Metadata(t.Context(), conn)
	if err != nil {
		t.Fatalf("Metadata() error = %v", err)
	}
	if m.Version != "17.4.1" || s.req(0).URI != "/gitlab/api/v4/metadata" {
		t.Errorf("Metadata() = %+v at %s, want 17.4.1 at /gitlab/api/v4/metadata", m, s.req(0).URI)
	}
}

func TestRequest_RefusesBadConnWithoutRequest(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"username": "alice"}))
	c := s.client(gitlab.Options{})
	tests := []struct {
		name string
		conn gitlab.Conn
		want error
	}{
		{"userinfo", gitlab.Conn{BaseURL: "https://u:p@host/api/v4", Token: testToken}, gitlab.ErrBaseURL},
		{"scheme", gitlab.Conn{BaseURL: "ftp://host/api/v4", Token: testToken}, gitlab.ErrBaseURL},
		{"query", gitlab.Conn{BaseURL: "https://host/api/v4?x=1", Token: testToken}, gitlab.ErrBaseURL},
		{"empty", gitlab.Conn{Token: testToken}, gitlab.ErrBaseURL},
		{"newline token", gitlab.Conn{BaseURL: s.URL + "/api/v4", Token: "glpat-x\r\nHost: evil"}, gitlab.ErrToken},
		{"space in token", gitlab.Conn{BaseURL: s.URL + "/api/v4", Token: "glpat x"}, gitlab.ErrToken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.User(t.Context(), tc.conn)
			if !errors.Is(err, tc.want) {
				t.Errorf("User(%+v) error = %v, want %v", tc.conn.BaseURL, err, tc.want)
			}
		})
	}
	if n := s.count(); n != 0 {
		t.Errorf("%d requests sent, want 0", n)
	}
}

func TestUser_NoUsernameIsNotAPI(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"id": 1}))
	_, err := s.client(gitlab.Options{}).User(t.Context(), s.conn())
	if !errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("User() error = %v, want ErrNotAPI", err)
	}
}

func TestTokenSelfAndMetadata(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/personal_access_tokens/self":
			writeJSON(w, 200, map[string]any{"scopes": []string{"read_api", "read_repository"}, "expires_at": "2026-12-01", "active": true})
		default:
			writeJSON(w, 200, map[string]any{"version": "17.0.1-ee", "revision": "abc"})
		}
	})
	c := s.client(gitlab.Options{})
	info, err := c.TokenSelf(t.Context(), s.conn())
	if err != nil {
		t.Fatalf("TokenSelf() error = %v", err)
	}
	want := gitlab.TokenInfo{Scopes: []string{"read_api", "read_repository"}, ExpiresAt: "2026-12-01"}
	if diff := cmp.Diff(want, info); diff != "" {
		t.Errorf("TokenSelf() mismatch (-want +got):\n%s", diff)
	}
	m, err := c.Metadata(t.Context(), s.conn())
	if err != nil || m.Version != "17.0.1-ee" {
		t.Errorf("Metadata() = %+v, %v, want 17.0.1-ee", m, err)
	}
}

func TestTokenSelf_NullExpiry(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"scopes": []string{"api"}, "expires_at": nil}))
	info, err := s.client(gitlab.Options{}).TokenSelf(t.Context(), s.conn())
	if err != nil || info.ExpiresAt != "" || len(info.Scopes) != 1 {
		t.Errorf("TokenSelf() = %+v, %v, want api scope and no expiry", info, err)
	}
}

func projectJSON() map[string]any {
	return map[string]any{
		"id": 42, "path": "api", "path_with_namespace": "acme/platform/api", "default_branch": nil,
		"web_url": "https://gitlab.example/acme/platform/api", "namespace": map[string]any{"full_path": "acme/platform"},
	}
}

func TestProjectByPath(t *testing.T) {
	s := newStub(t, okJSON(projectJSON()))
	p, err := s.client(gitlab.Options{}).ProjectByPath(t.Context(), s.conn(), "acme/platform/api")
	if err != nil {
		t.Fatalf("ProjectByPath() error = %v", err)
	}
	want := gitlab.Project{ID: 42, PathWithNamespace: "acme/platform/api", Path: "api", NamespaceFullPath: "acme/platform", WebURL: "https://gitlab.example/acme/platform/api"}
	if diff := cmp.Diff(want, p); diff != "" {
		t.Errorf("ProjectByPath() mismatch (-want +got):\n%s", diff)
	}
	if got := s.req(0).URI; got != "/api/v4/projects/acme%2Fplatform%2Fapi" {
		t.Errorf("request URI = %q, want the path URL-encoded", got)
	}
}

func TestProjectByPath_RefusesEscapingPaths(t *testing.T) {
	s := newStub(t, okJSON(projectJSON()))
	c := s.client(gitlab.Options{})
	for _, p := range []string{"", "..", "a/../b", "a//b", "/a", "a/", "a\\b", "a\nb", "./x"} {
		if _, err := c.ProjectByPath(t.Context(), s.conn(), p); err == nil {
			t.Errorf("ProjectByPath(%q) succeeded, want an error", p)
		}
	}
	if n := s.count(); n != 0 {
		t.Errorf("%d requests sent, want 0", n)
	}
}

func TestProject_WithoutIDInAnswerIsNotAPI(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"message": "hello"}))
	_, err := s.client(gitlab.Options{}).Project(t.Context(), s.conn(), 3)
	if !errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("Project() error = %v, want ErrNotAPI", err)
	}
}

func TestNotJSONAnswers(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"html page", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html>login</html>")
		}},
		{"json type with html body", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "<html>login</html>")
		}},
		{"no content type, empty", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.handler)
			_, err := s.client(gitlab.Options{}).User(t.Context(), s.conn())
			if !errors.Is(err, gitlab.ErrNotAPI) {
				t.Errorf("User() error = %v, want ErrNotAPI", err)
			}
			if n := s.count(); n != 1 {
				t.Errorf("%d requests, want 1 (ErrNotAPI is not retried)", n)
			}
		})
	}
}

func TestRedirectIsNeverFollowed(t *testing.T) {
	other := newStub(t, okJSON(map[string]any{"username": "evil"}))
	for _, code := range []int{301, 302, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL+"/api/v4/user", code)
			})
			// A plain client follows redirects; the package must not let it.
			hc := s.Client()
			hc.CheckRedirect = nil
			c := gitlab.New(gitlab.Options{HTTP: hc, Retry: &retry.Options{Retries: 3, BaseDelay: time.Millisecond}})
			_, err := c.User(t.Context(), s.conn())
			if !errors.Is(err, gitlab.ErrRedirect) {
				t.Errorf("User() error = %v, want ErrRedirect", err)
			}
			if s.count() != 1 {
				t.Errorf("%d requests to the first host, want 1", s.count())
			}
		})
	}
	if n := other.count(); n != 0 {
		t.Errorf("redirect target got %d requests (token leak), want 0", n)
	}
}

func TestStatusError(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	tests := []struct {
		name       string
		status     int
		headers    map[string]string
		body       string
		wantMsg    string
		wantAfter  time.Duration // 0 = zero RetryAfter
		wantResets bool
	}{
		{name: "401 message", status: 401, body: `{"message":"401 Unauthorized"}`, wantMsg: "401 Unauthorized"},
		{name: "error key", status: 403, body: `{"error":"insufficient_scope"}`, wantMsg: "insufficient_scope"},
		{name: "object message", status: 400, body: `{"message":{"base":["bad"]}}`, wantMsg: `{"base":["bad"]}`},
		{name: "html body", status: 404, body: `<html>nope</html>`, wantMsg: "Not Found"},
		{name: "429 retry-after", status: 429, headers: map[string]string{"Retry-After": "120"}, body: `{"message":"slow down"}`, wantMsg: "slow down", wantAfter: 120 * time.Second},
		{name: "429 reset", status: 429, headers: map[string]string{"RateLimit-Reset": fmt.Sprint(reset)}, body: `{}`, wantMsg: "Too Many Requests", wantResets: true},
		{name: "retry-after junk", status: 429, headers: map[string]string{"Retry-After": "soon"}, body: `{}`, wantMsg: "Too Many Requests", wantAfter: time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			c := s.client(gitlab.Options{Retry: &retry.Options{}})
			start := time.Now()
			_, err := c.User(t.Context(), s.conn())
			var se *gitlab.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("User() error = %v, want *StatusError", err)
			}
			if se.Status != tc.status || se.Message != tc.wantMsg {
				t.Errorf("StatusError = {%d %q}, want {%d %q}", se.Status, se.Message, tc.status, tc.wantMsg)
			}
			if tc.wantAfter == 0 && !se.RetryAfter.IsZero() {
				t.Errorf("RetryAfter = %v, want zero", se.RetryAfter)
			}
			if tc.wantAfter != 0 {
				if d := se.RetryAfter.Sub(start); d < tc.wantAfter-time.Second || d > tc.wantAfter+5*time.Second {
					t.Errorf("RetryAfter in %v, want about %v", d, tc.wantAfter)
				}
				if !se.ResumeAt().Equal(se.RetryAfter) {
					t.Errorf("ResumeAt() = %v, want RetryAfter %v", se.ResumeAt(), se.RetryAfter)
				}
			}
			if tc.wantResets {
				if se.Reset.Unix() != reset || !se.ResumeAt().Equal(se.Reset) {
					t.Errorf("Reset = %v, ResumeAt = %v, want %d", se.Reset, se.ResumeAt(), reset)
				}
			}
		})
	}
}

func TestStatusError_ScrubsTokenBeforeCut(t *testing.T) {
	// The token sits across the 1000-byte cut: scrub first, cut second.
	echo := strings.Repeat("x", 990) + testToken
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 401, map[string]any{"message": echo})
	})
	_, err := s.client(gitlab.Options{Retry: &retry.Options{}}).User(t.Context(), s.conn())
	assertNoToken(t, err.Error())
	var se *gitlab.StatusError
	if !errors.As(err, &se) || !strings.Contains(se.Message, "[REDACTED]") {
		t.Errorf("message %q lacks the redaction mark", se.Message)
	}
}

func assertNoToken(t *testing.T, text string) {
	t.Helper()
	for i := 0; i+8 <= len(testToken); i++ {
		if frag := testToken[i : i+8]; strings.Contains(text, frag) {
			t.Errorf("text %q contains token fragment %q", text, frag)
			return
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportErrorIsScrubbedAndHasNoURL(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("header PRIVATE-TOKEN: %q rejected", testToken)
	})}
	c := gitlab.New(gitlab.Options{HTTP: hc, Retry: &retry.Options{}})
	_, err := c.User(t.Context(), gitlab.Conn{BaseURL: "https://secret-host.example/api/v4", Token: testToken})
	if err == nil {
		t.Fatal("User() succeeded")
	}
	assertNoToken(t, err.Error())
	if strings.Contains(err.Error(), "secret-host") {
		t.Errorf("error %q embeds the request URL", err)
	}
}

func TestDialGuardRefusal(t *testing.T) {
	c := gitlab.New(gitlab.Options{HTTP: egress.Client{}.HTTPClient(), Retry: &retry.Options{Retries: 3, BaseDelay: time.Millisecond}})
	_, err := c.User(t.Context(), gitlab.Conn{BaseURL: "http://169.254.169.254/api/v4", Token: testToken})
	if !egress.IsForbidden(err) {
		t.Fatalf("User() error = %v, want the forbidden-address refusal", err)
	}
	if strings.Contains(err.Error(), "/api/v4") || strings.Contains(err.Error(), "http://") {
		t.Errorf("error %q embeds the request URL", err)
	}
}

func TestRetry(t *testing.T) {
	tests := []struct {
		name      string
		retry     *retry.Options
		responses []int
		headers   map[string]string
		wantCalls int
		wantErr   bool
	}{
		{"503 then ok", nil, []int{503, 200}, nil, 2, false},
		{"three 503 then ok", nil, []int{503, 502, 500, 200}, nil, 4, false},
		{"no retry configured", &retry.Options{}, []int{503, 200}, nil, 1, true},
		{"404 not retried", nil, []int{404, 200}, nil, 1, true},
		{"401 not retried", nil, []int{401, 200}, nil, 1, true},
		{"429 short pause retried", nil, []int{429, 200}, map[string]string{"Retry-After": "0"}, 2, false},
		{"429 long pause not retried", nil, []int{429, 200}, map[string]string{"Retry-After": "120"}, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			n := 0
			s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				code := tc.responses[min(n, len(tc.responses)-1)]
				n++
				mu.Unlock()
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				writeJSON(w, code, map[string]any{"username": "alice", "message": "m"})
			})
			_, err := s.client(gitlab.Options{Retry: tc.retry}).User(t.Context(), s.conn())
			if (err != nil) != tc.wantErr {
				t.Errorf("User() error = %v, wantErr %v", err, tc.wantErr)
			}
			if s.count() != tc.wantCalls {
				t.Errorf("%d calls, want %d", s.count(), tc.wantCalls)
			}
		})
	}
}

func TestWithRetry_ZeroPolicyMeansNoRetry(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 503, map[string]any{}) })
	c := s.client(gitlab.Options{}).WithRetry(retry.Options{})
	if _, err := c.User(t.Context(), s.conn()); err == nil {
		t.Fatal("User() succeeded")
	}
	if s.count() != 1 {
		t.Errorf("%d calls, want 1", s.count())
	}
}

func mrJSON(iid int, state string, extra map[string]any) map[string]any {
	m := map[string]any{
		"iid": iid, "title": fmt.Sprintf("MR %d", iid), "description": "closes #9", "state": state, "draft": false,
		"source_branch": "feat", "target_branch": "main", "sha": "abc123",
		"source_project_id": 42, "target_project_id": 42,
		"created_at": "2026-10-01T10:00:00.123Z", "updated_at": "2026-10-02T11:30:00Z",
		"merged_at": nil, "closed_at": nil, "author": map[string]any{"username": "alice"},
		"web_url": "https://gitlab.example/acme/api/-/merge_requests/1",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestMergeRequestsPage(t *testing.T) {
	merged := "2026-10-03T09:00:00Z"
	s := newStub(t, okJSON([]any{
		mrJSON(1, "opened", nil),
		mrJSON(2, "merged", map[string]any{"merged_at": merged}),
		mrJSON(3, "closed", map[string]any{"closed_at": merged}),
		mrJSON(4, "locked", nil),
		mrJSON(5, "opened", map[string]any{"draft": true, "author": nil}),
		mrJSON(6, "opened", map[string]any{"source_project_id": 99}),
		mrJSON(7, "opened", map[string]any{"source_project_id": nil}),
		mrJSON(8, "opened", map[string]any{"sha": nil}),
	}))
	mrs, err := s.client(gitlab.Options{}).MergeRequestsPage(t.Context(), s.conn(), 42, 2, 50)
	if err != nil {
		t.Fatalf("MergeRequestsPage() error = %v", err)
	}
	r := s.req(0)
	wantURI := "/api/v4/projects/42/merge_requests?order_by=updated_at&page=2&per_page=50&sort=desc&state=all"
	if r.URI != wantURI {
		t.Errorf("request URI = %q, want %q", r.URI, wantURI)
	}
	type row struct {
		IID    int
		Status string
		Author string
		Draft  bool
		Fork   bool
		SHA    string
	}
	var got []row
	for _, m := range mrs {
		got = append(got, row{m.IID, m.Status, m.Author, m.Draft, m.HeadIsFork, m.HeadSHA})
	}
	want := []row{
		{1, "open", "alice", false, false, "abc123"},
		{2, "merged", "alice", false, false, "abc123"},
		{3, "closed", "alice", false, false, "abc123"},
		{4, "closed", "alice", false, false, "abc123"},
		{5, "open", "unknown", true, false, "abc123"},
		{6, "open", "alice", false, true, "abc123"},
		{7, "open", "alice", false, true, "abc123"},
		{8, "open", "alice", false, false, ""},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("MergeRequestsPage() mismatch (-want +got):\n%s", diff)
	}
	first := mrs[0]
	if first.Branch != "feat" || first.Base != "main" || first.Title != "MR 1" || first.WebURL == "" {
		t.Errorf("MergeRequestsPage()[0] = %+v, want branch feat, base main", first)
	}
	if want := time.Date(2026, 10, 1, 10, 0, 0, 123_000_000, time.UTC); !first.OpenedAt.Equal(want) {
		t.Errorf("OpenedAt = %v, want %v", first.OpenedAt, want)
	}
	if mrs[1].MergedAt == nil || mrs[0].MergedAt != nil {
		t.Errorf("MergedAt = %v / %v, want nil then set", mrs[0].MergedAt, mrs[1].MergedAt)
	}
}

func TestMergeRequestsPage_RejectsBadArguments(t *testing.T) {
	s := newStub(t, okJSON([]any{}))
	c := s.client(gitlab.Options{})
	for _, a := range [][3]int{{0, 1, 50}, {42, 0, 50}, {42, 1, 0}, {42, 1, 101}, {42, gitlab.MaxPage + 1, 50}} {
		if _, err := c.MergeRequestsPage(t.Context(), s.conn(), int64(a[0]), a[1], a[2]); err == nil {
			t.Errorf("MergeRequestsPage(project %d, page %d, per_page %d) succeeded", a[0], a[1], a[2])
		}
	}
	if s.count() != 0 {
		t.Errorf("%d requests, want 0", s.count())
	}
}

func TestMergeRequestsPage_ZeroIIDIsNotAPI(t *testing.T) {
	s := newStub(t, okJSON([]any{map[string]any{"title": "x"}}))
	_, err := s.client(gitlab.Options{}).MergeRequestsPage(t.Context(), s.conn(), 42, 1, 50)
	if !errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("error = %v, want ErrNotAPI", err)
	}
}

func TestMergeRequest(t *testing.T) {
	s := newStub(t, okJSON(mrJSON(5, "opened", nil)))
	d, err := s.client(gitlab.Options{}).MergeRequest(t.Context(), s.conn(), 42, 5)
	if err != nil {
		t.Fatalf("MergeRequest() error = %v", err)
	}
	if d.IID != 5 || d.Description == nil || *d.Description != "closes #9" {
		t.Errorf("MergeRequest() = %+v, want iid 5 with description", d)
	}
	if got := s.req(0).URI; got != "/api/v4/projects/42/merge_requests/5" {
		t.Errorf("request URI = %q", got)
	}
	// An answer for another merge request is not the one asked for.
	other := newStub(t, okJSON(mrJSON(6, "opened", nil)))
	if _, err := other.client(gitlab.Options{}).MergeRequest(t.Context(), other.conn(), 42, 5); !errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("MergeRequest() with wrong iid error = %v, want ErrNotAPI", err)
	}
}

func diffEntries(page, n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = map[string]any{"old_path": "f", "new_path": fmt.Sprintf("p%d-%d", page, i), "diff": "@@ -1 +1 @@\n-a\n+b\n"}
	}
	return out
}

func TestDiffs_MapsFiles(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1,3 +1,3 @@\n ctx\n-old\n--- dashes\n+new\n+++ plus\n\\ No newline at end of file\n"},
		map[string]any{"old_path": "gone.txt", "new_path": "gone.txt", "deleted_file": true, "diff": "@@ -1,2 +0,0 @@\n-x\n-y\n"},
		map[string]any{"old_path": "big.sql", "new_path": "big.sql", "too_large": true, "diff": "@@ -1 +1 @@\n-a\n+b\n"},
		map[string]any{"old_path": "c.bin", "new_path": "c.bin", "collapsed": true, "diff": ""},
		map[string]any{"old_path": "img.png", "new_path": "img.png", "diff": ""},
		map[string]any{"old_path": "old.go", "new_path": "new.go", "renamed_file": true, "diff": "@@ -1 +1 @@\n-a\n+b\n"},
		map[string]any{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -9 +9 @@\n-q\n"},
		map[string]any{"old_path": "", "new_path": "", "diff": "+z"},
	}))
	files, err := s.client(gitlab.Options{}).Diffs(t.Context(), s.conn(), 42, 5)
	if err != nil {
		t.Fatalf("Diffs() error = %v", err)
	}
	type row struct {
		Path       string
		Add, Del   int
		HasPatch   bool
		PatchStart string
	}
	var got []row
	for _, f := range files {
		r := row{Path: f.Path, Add: f.Additions, Del: f.Deletions, HasPatch: f.Patch != nil}
		if f.Patch != nil {
			r.PatchStart = (*f.Patch)[:min(8, len(*f.Patch))]
		}
		got = append(got, r)
	}
	want := []row{
		{"a.go", 2, 3, true, "@@ -1,3 "},
		{"gone.txt", 0, 2, true, "@@ -1,2 "},
		{"big.sql", 0, 0, false, ""},
		{"c.bin", 0, 0, false, ""},
		{"img.png", 0, 0, false, ""},
		{"new.go", 1, 1, true, "@@ -1 +1"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Diffs() mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(*files[0].Patch, "\n@@ -9 +9 @@\n") {
		t.Errorf("same-path patches not joined: %q", *files[0].Patch)
	}
	if got := s.req(0).URI; got != "/api/v4/projects/42/merge_requests/5/diffs?page=1&per_page=100" {
		t.Errorf("request URI = %q", got)
	}
}

func TestDiffs_CapsAt3000FilesAnd30Pages(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		var page int
		_, _ = fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		// A hostile Link header must not be followed.
		w.Header().Set("Link", `<https://evil.example/next>; rel="next"`)
		w.Header().Set("X-Next-Page", "999")
		writeJSON(w, 200, diffEntries(page, 100))
	})
	files, err := s.client(gitlab.Options{}).Diffs(t.Context(), s.conn(), 42, 5)
	if err != nil {
		t.Fatalf("Diffs() error = %v", err)
	}
	if len(files) != gitlab.MaxFiles {
		t.Errorf("Diffs() returned %d files, want %d", len(files), gitlab.MaxFiles)
	}
	if s.count() != 30 {
		t.Errorf("%d requests, want 30 pages", s.count())
	}
	for i := range s.count() {
		if want := fmt.Sprintf("page=%d&", i+1); !strings.Contains(s.req(i).URI, want) {
			t.Errorf("request %d URI = %q, want %q", i, s.req(i).URI, want)
		}
	}
}

func TestDiffs_StopsAtShortPage(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			writeJSON(w, 200, diffEntries(1, 100))
			return
		}
		writeJSON(w, 200, diffEntries(2, 7))
	})
	files, err := s.client(gitlab.Options{}).Diffs(t.Context(), s.conn(), 42, 5)
	if err != nil || len(files) != 107 || s.count() != 2 {
		t.Errorf("Diffs() = %d files, %v after %d requests, want 107 after 2", len(files), err, s.count())
	}
}

func TestDiffs_PageFailureIsAnError(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, 500, map[string]any{"message": "boom"})
			return
		}
		writeJSON(w, 200, diffEntries(1, 100))
	})
	_, err := s.client(gitlab.Options{Retry: &retry.Options{}}).Diffs(t.Context(), s.conn(), 42, 5)
	var se *gitlab.StatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Errorf("Diffs() error = %v, want a 500 StatusError", err)
	}
}

func TestCommits_CapsAt250(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		out := make([]any, 100)
		for i := range out {
			out[i] = map[string]any{"id": fmt.Sprintf("sha%s-%d", r.URL.Query().Get("page"), i), "message": "m",
				"author_name": "", "committed_date": "2026-10-01T10:00:00Z"}
		}
		out[0].(map[string]any)["author_name"] = "Alice"
		writeJSON(w, 200, out)
	})
	commits, err := s.client(gitlab.Options{}).Commits(t.Context(), s.conn(), 42, 5)
	if err != nil {
		t.Fatalf("Commits() error = %v", err)
	}
	if len(commits) != gitlab.MaxCommits || s.count() != 3 {
		t.Errorf("Commits() = %d commits after %d requests, want 250 after 3", len(commits), s.count())
	}
	if commits[0].Author != "Alice" || commits[1].Author != "unknown" || commits[0].SHA != "sha1-0" || commits[0].CommittedAt == nil {
		t.Errorf("Commits()[0..1] = %+v, %+v", commits[0], commits[1])
	}
	if got := s.req(0).URI; got != "/api/v4/projects/42/merge_requests/5/commits?page=1&per_page=100" {
		t.Errorf("request URI = %q", got)
	}
}

func TestIssue(t *testing.T) {
	tests := []struct {
		state, want string
	}{{"opened", "open"}, {"closed", "closed"}}
	for _, tc := range tests {
		t.Run(tc.state, func(t *testing.T) {
			s := newStub(t, okJSON(map[string]any{"iid": 9, "title": "Bug", "description": nil, "state": tc.state}))
			is, err := s.client(gitlab.Options{}).Issue(t.Context(), s.conn(), 42, 9)
			if err != nil {
				t.Fatalf("Issue() error = %v", err)
			}
			want := gitlab.Issue{Number: 9, Title: "Bug", State: tc.want}
			if diff := cmp.Diff(want, is, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Issue() mismatch (-want +got):\n%s", diff)
			}
			if got := s.req(0).URI; got != "/api/v4/projects/42/issues/9" {
				t.Errorf("request URI = %q", got)
			}
		})
	}
}

func statsAnswer(nodes ...any) map[string]any {
	return map[string]any{"data": map[string]any{"projects": map[string]any{"nodes": []any{
		map[string]any{"id": "gid://gitlab/Project/42", "mergeRequests": map[string]any{"nodes": nodes}},
	}}}}
}

func summaryNode(iid string, add, del, files int) map[string]any {
	return map[string]any{"iid": iid, "diffStatsSummary": map[string]any{"additions": add, "deletions": del, "fileCount": files}}
}

func TestDiffStats(t *testing.T) {
	s := newStub(t, okJSON(statsAnswer(
		summaryNode("1", 10, 2, 3),
		summaryNode("2", 0, 0, 0),
		map[string]any{"iid": "3", "diffStatsSummary": nil},
		summaryNode("77", 5, 5, 5), // not asked for
	)))
	got, err := s.client(gitlab.Options{}).DiffStats(t.Context(), s.conn(), 42, []int{3, 1, 2, 1, 4})
	if err != nil {
		t.Fatalf("DiffStats() error = %v", err)
	}
	if diff := cmp.Diff(map[int]gitlab.Stats{1: {Additions: 10, Deletions: 2, FilesCount: 3}}, got); diff != "" {
		t.Errorf("DiffStats() mismatch (-want +got):\n%s", diff)
	}
	r := s.req(0)
	if r.Method != "POST" || r.URI != "/api/graphql" {
		t.Errorf("request = %s %s, want POST /api/graphql", r.Method, r.URI)
	}
	if r.Header.Get("PRIVATE-TOKEN") != testToken || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v, want PRIVATE-TOKEN and JSON", r.Header)
	}
	var body struct {
		Query     string `json:"query"`
		Variables struct {
			IDs  []string `json:"ids"`
			IIDs []string `json:"iids"`
		} `json:"variables"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if strings.Contains(strings.ToLower(body.Query), "mutation") || !strings.HasPrefix(body.Query, "query") {
		t.Errorf("GraphQL document is not a plain query: %q", body.Query)
	}
	if diff := cmp.Diff([]string{"gid://gitlab/Project/42"}, body.Variables.IDs); diff != "" {
		t.Errorf("variables.ids mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"1", "2", "3", "4"}, body.Variables.IIDs); diff != "" {
		t.Errorf("variables.iids mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(r.Body, testToken) {
		t.Errorf("token in the GraphQL body")
	}
}

func TestDiffStats_Batching(t *testing.T) {
	s := newStub(t, okJSON(statsAnswer()))
	c := s.client(gitlab.Options{})
	iids := make([]int, 21)
	for i := range iids {
		iids[i] = i + 1
	}
	if _, err := c.DiffStats(t.Context(), s.conn(), 42, iids); err == nil {
		t.Error("DiffStats() with 21 iids succeeded, want an error")
	}
	if _, err := c.DiffStats(t.Context(), s.conn(), 42, iids[:20]); err != nil {
		t.Errorf("DiffStats() with 20 iids error = %v", err)
	}
	if got, err := c.DiffStats(t.Context(), s.conn(), 42, nil); err != nil || len(got) != 0 {
		t.Errorf("DiffStats() with no iids = %v, %v, want empty", got, err)
	}
	if _, err := c.DiffStats(t.Context(), s.conn(), 42, []int{0}); err == nil {
		t.Error("DiffStats() with iid 0 succeeded")
	}
	if s.count() != 1 {
		t.Errorf("%d requests, want 1 (only the 20-iid call)", s.count())
	}
}

func TestDiffStats_PathPrefix(t *testing.T) {
	s := newStub(t, okJSON(statsAnswer(summaryNode("1", 1, 1, 1))))
	conn := gitlab.Conn{BaseURL: s.URL + "/gl/api/v4", Token: testToken}
	if _, err := s.client(gitlab.Options{}).DiffStats(t.Context(), conn, 42, []int{1}); err != nil {
		t.Fatalf("DiffStats() error = %v", err)
	}
	if got := s.req(0).URI; got != "/gl/api/graphql" {
		t.Errorf("request URI = %q, want /gl/api/graphql", got)
	}
	bad := gitlab.Conn{BaseURL: s.URL + "/other", Token: testToken}
	if _, err := s.client(gitlab.Options{}).DiffStats(t.Context(), bad, 42, []int{1}); !errors.Is(err, gitlab.ErrBaseURL) {
		t.Errorf("DiffStats() on a non-REST root error = %v, want ErrBaseURL", err)
	}
	if s.count() != 1 {
		t.Errorf("%d requests, want 1", s.count())
	}
}

func TestDiffStats_Failures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		check   func(error) bool
	}{
		{"graphql errors", okJSON(map[string]any{"errors": []any{map[string]any{"message": "Field 'x' doesn't exist " + testToken}}}),
			func(err error) bool { return errors.Is(err, gitlab.ErrGraphQL) }},
		{"null data", okJSON(map[string]any{"data": nil}),
			func(err error) bool { return errors.Is(err, gitlab.ErrNotAPI) }},
		{"graphql disabled", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 404, map[string]any{"message": "404 Not Found"})
		},
			func(err error) bool {
				var se *gitlab.StatusError
				return errors.As(err, &se) && se.Status == 404
			}},
		{"html", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>")
		}, func(err error) bool { return errors.Is(err, gitlab.ErrNotAPI) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.handler)
			got, err := s.client(gitlab.Options{Retry: &retry.Options{}}).DiffStats(t.Context(), s.conn(), 42, []int{1})
			if err == nil || got != nil || !tc.check(err) {
				t.Fatalf("DiffStats() = %v, %v", got, err)
			}
			assertNoToken(t, err.Error())
		})
	}
}

func TestDiffStats_IgnoresOtherProjectNodes(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"data": map[string]any{"projects": map[string]any{"nodes": []any{
		map[string]any{"id": "gid://gitlab/Project/1", "mergeRequests": map[string]any{"nodes": []any{summaryNode("1", 9, 9, 9)}}},
	}}}}))
	got, err := s.client(gitlab.Options{}).DiffStats(t.Context(), s.conn(), 42, []int{1})
	if err != nil || len(got) != 0 {
		t.Errorf("DiffStats() = %v, %v, want empty", got, err)
	}
}

// captureLog collects log records as flat text.
type captureLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLog) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.lines = append(c.lines, strings.TrimSpace(string(p)))
		return len(p), nil
	}), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestLogging_OneLinePerAttemptWithoutSecrets(t *testing.T) {
	var mu sync.Mutex
	n := 0
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			writeJSON(w, 503, map[string]any{"message": "down " + testToken})
			return
		}
		writeJSON(w, 200, mrJSON(1, "opened", map[string]any{"title": "Secret title", "description": "Secret description"}))
	})
	var logs captureLog
	c := s.client(gitlab.Options{Log: logs.logger()})
	if _, err := c.MergeRequest(t.Context(), s.conn(), 42, 1); err != nil {
		t.Fatalf("MergeRequest() error = %v", err)
	}
	if len(logs.lines) != 2 {
		t.Fatalf("%d log lines, want 2 (one per attempt):\n%s", len(logs.lines), strings.Join(logs.lines, "\n"))
	}
	for _, l := range logs.lines {
		for _, bad := range []string{testToken, "Secret title", "Secret description", "/api/v4"} {
			if strings.Contains(l, bad) {
				t.Errorf("log line %q contains %q", l, bad)
			}
		}
		assertNoToken(t, l)
	}
	host := strings.TrimPrefix(s.URL, "https://")
	for i, want := range [][]string{
		{"host=" + host, "op=merge_request", "attempt=1", "status=503", "error_class=status"},
		{"host=" + host, "op=merge_request", "attempt=2", "status=200", "latency_ms="},
	} {
		for _, w := range want {
			if !strings.Contains(logs.lines[i], w) {
				t.Errorf("log line %d = %q, missing %q", i, logs.lines[i], w)
			}
		}
	}
}

func TestResponseTooLarge(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"username":"`)
		chunk := bytes.Repeat([]byte("a"), 1<<20)
		for range gitlab.MaxResponseBytes/(1<<20) + 1 {
			_, _ = w.Write(chunk)
		}
		_, _ = io.WriteString(w, `"}`)
	})
	_, err := s.client(gitlab.Options{Retry: &retry.Options{}}).User(t.Context(), s.conn())
	if err == nil || errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("User() error = %v, want a too-large error", err)
	}
}

func TestContextCancelled(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"username": "a"}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.client(gitlab.Options{}).User(ctx, s.conn())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("User() error = %v, want context.Canceled", err)
	}
	if s.count() != 0 {
		t.Errorf("%d requests, want 0", s.count())
	}
}
