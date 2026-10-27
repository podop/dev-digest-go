package glapi_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/runner/adapters/glapi"
)

const token = "glpat-0123456789abcdef"

var ref = glapi.MRRef{ProjectID: "42", IID: 7}

// rec is what a stub server saw of one request.
type rec struct {
	Method, Path, Query, Token, ContentType string
	Body                                    string
	ContentLength                           int64
}

// stub starts a TLS server and returns a client bound to https://127.0.0.1:port<prefix>.
func stub(t *testing.T, prefix string, h func(http.ResponseWriter, *http.Request, rec)) (*glapi.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h(w, r, rec{
			Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery,
			Token: r.Header.Get("PRIVATE-TOKEN"), ContentType: r.Header.Get("Content-Type"),
			Body: string(b), ContentLength: r.ContentLength,
		})
	}))
	t.Cleanup(srv.Close)
	c, err := glapi.New(glapi.Options{APIURL: srv.URL + prefix, Transport: srv.Client().Transport, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c, srv
}

func TestNew_RefusesUnsafeAPIURLs(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"empty", ""},
		{"http", "http://gitlab.example/api/v4"},
		{"no scheme", "gitlab.example/api/v4"},
		{"user info", "https://u:p@gitlab.example/api/v4"},
		{"query", "https://gitlab.example/api/v4?x=1"},
		{"fragment", "https://gitlab.example/api/v4#x"},
		{"dot segments", "https://gitlab.example/api/../v4"},
		{"backslash", "https://gitlab.example/api\\v4"},
		{"no host", "https:///api/v4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := glapi.New(glapi.Options{APIURL: tc.url}); err == nil {
				t.Errorf("New(%q) error = nil, want a refusal", tc.url)
			}
		})
	}
	if _, err := glapi.New(glapi.Options{APIURL: "https://gitlab.example:8443/gitlab/api/v4/"}); err != nil {
		t.Errorf("New(prefix with port and trailing slash) error = %v, want nil", err)
	}
}

func TestMR_ReadsDiffRefs(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       glapi.MergeRequest
	}{
		{"refs", `{"iid":7,"diff_refs":{"base_sha":"b","start_sha":"s","head_sha":"h"}}`,
			glapi.MergeRequest{IID: 7, DiffRefs: &glapi.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"}}},
		{"null", `{"iid":7,"diff_refs":null}`, glapi.MergeRequest{IID: 7}},
		{"missing", `{"iid":7}`, glapi.MergeRequest{IID: 7}},
		{"incomplete", `{"iid":7,"diff_refs":{"base_sha":"b","start_sha":"","head_sha":"h"}}`, glapi.MergeRequest{IID: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got rec
			c, _ := stub(t, "/gl/api/v4", func(w http.ResponseWriter, _ *http.Request, r rec) {
				got = r
				_, _ = io.WriteString(w, tc.body)
			})
			mr, err := c.MR(t.Context(), token, ref)
			if err != nil {
				t.Fatalf("MR() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, mr); diff != "" {
				t.Errorf("MR() mismatch (-want +got):\n%s", diff)
			}
			if got.Method != "GET" || got.Path != "/gl/api/v4/projects/42/merge_requests/7" || got.Token != token {
				t.Errorf("request = %+v, want GET /gl/api/v4/projects/42/merge_requests/7 with the token", got)
			}
		})
	}
}

func TestListDrafts_FirstPageOf100(t *testing.T) {
	var got rec
	c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, r rec) {
		got = r
		_, _ = io.WriteString(w, `[{"id":5,"note":"x"},{"id":9}]`)
	})
	drafts, err := c.ListDrafts(t.Context(), token, ref)
	if err != nil {
		t.Fatalf("ListDrafts() error = %v", err)
	}
	if diff := cmp.Diff([]glapi.Draft{{ID: 5}, {ID: 9}}, drafts); diff != "" {
		t.Errorf("ListDrafts() mismatch (-want +got):\n%s", diff)
	}
	if got.Method != "GET" || got.Path != "/api/v4/projects/42/merge_requests/7/draft_notes" || got.Query != "per_page=100" {
		t.Errorf("request = %+v, want GET …/draft_notes?per_page=100", got)
	}
}

func TestCreateDraft_Bodies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		draft glapi.NewDraft
		want  map[string]any
	}{
		{"summary has no position", glapi.NewDraft{Note: "a <b> & c"}, map[string]any{"note": "a <b> & c"}},
		{"new-side line", glapi.NewDraft{Note: "n", Position: &glapi.DraftPosition{
			Refs: glapi.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"}, NewPath: "a.go", OldPath: "a.go", NewLine: 12}},
			map[string]any{"note": "n", "position": map[string]any{
				"position_type": "text", "base_sha": "b", "start_sha": "s", "head_sha": "h",
				"new_path": "a.go", "old_path": "a.go", "new_line": float64(12)}}},
		{"context line in a renamed file", glapi.NewDraft{Note: "n", Position: &glapi.DraftPosition{
			Refs: glapi.DiffRefs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"}, NewPath: "new.go", OldPath: "old.go", NewLine: 12, OldLine: 10}},
			map[string]any{"note": "n", "position": map[string]any{
				"position_type": "text", "base_sha": "b", "start_sha": "s", "head_sha": "h",
				"new_path": "new.go", "old_path": "old.go", "new_line": float64(12), "old_line": float64(10)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got rec
			c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, r rec) {
				got = r
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"id":31}`)
			})
			d, err := c.CreateDraft(t.Context(), token, ref, tc.draft)
			if err != nil || d.ID != 31 {
				t.Fatalf("CreateDraft() = %+v, %v, want id 31", d, err)
			}
			if got.Method != "POST" || got.Path != "/api/v4/projects/42/merge_requests/7/draft_notes" || got.ContentType != "application/json" {
				t.Errorf("request = %+v, want POST …/draft_notes as JSON", got)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(got.Body), &body); err != nil {
				t.Fatalf("request body %q is not JSON: %v", got.Body, err)
			}
			if diff := cmp.Diff(tc.want, body); diff != "" {
				t.Errorf("request body mismatch (-want +got):\n%s", diff)
			}
			if strings.Contains(got.Body, "\\u00") {
				t.Errorf("request body = %s, want the note unescaped", got.Body)
			}
		})
	}
}

func TestCreateDraft_AnswerWithoutAnIDIsAnError(t *testing.T) {
	c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"note":"x"}`)
	})
	if _, err := c.CreateDraft(t.Context(), token, ref, glapi.NewDraft{Note: "n"}); err == nil {
		t.Error("CreateDraft(answer without id) error = nil, want an error")
	}
}

func TestWrites_ShapeAndNoReviewerState(t *testing.T) {
	var seen []rec
	c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, r rec) {
		seen = append(seen, r)
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/draft_notes") {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":1}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ctx := t.Context()
	if _, err := c.CreateDraft(ctx, token, ref, glapi.NewDraft{Note: "n"}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name string
		call func() error
	}{
		{"publish", func() error { return c.PublishDraft(ctx, token, ref, 1) }},
		{"bulk", func() error { return c.BulkPublish(ctx, token, ref) }},
		{"delete", func() error { return c.DeleteDraft(ctx, token, ref, 1) }},
		{"note", func() error { return c.CreateNote(ctx, token, ref, "summary") }},
	} {
		if err := step.call(); err != nil {
			t.Fatalf("%s: error = %v", step.name, err)
		}
	}
	want := []struct{ method, path string }{
		{"POST", "/api/v4/projects/42/merge_requests/7/draft_notes"},
		{"PUT", "/api/v4/projects/42/merge_requests/7/draft_notes/1/publish"},
		{"POST", "/api/v4/projects/42/merge_requests/7/draft_notes/bulk_publish"},
		{"DELETE", "/api/v4/projects/42/merge_requests/7/draft_notes/1"},
		{"POST", "/api/v4/projects/42/merge_requests/7/notes"},
	}
	if len(seen) != len(want) {
		t.Fatalf("stub saw %d requests, want %d", len(seen), len(want))
	}
	for i, w := range want {
		if seen[i].Method != w.method || seen[i].Path != w.path || seen[i].Token != token {
			t.Errorf("request %d = %s %s (token ok: %v), want %s %s", i, seen[i].Method, seen[i].Path, seen[i].Token == token, w.method, w.path)
		}
		for _, banned := range []string{"reviewer_state", "approve", "approval"} {
			if strings.Contains(seen[i].Body, banned) || strings.Contains(seen[i].Query, banned) {
				t.Errorf("request %d carries %q", i, banned)
			}
		}
	}
	for _, i := range []int{1, 2, 3} { // publish, bulk_publish, delete: no body at all
		if seen[i].Body != "" || seen[i].ContentType != "" {
			t.Errorf("request %d (%s) has body %q and content type %q, want none", i, seen[i].Path, seen[i].Body, seen[i].ContentType)
		}
	}
	if seen[4].Body != `{"body":"summary"}` {
		t.Errorf("note body = %s, want {\"body\":\"summary\"}", seen[4].Body)
	}
}

func TestRefs_AreValidatedBeforeAnyRequest(t *testing.T) {
	var hits atomic.Int32
	c, _ := stub(t, "/api/v4", func(http.ResponseWriter, *http.Request, rec) { hits.Add(1) })
	ctx := t.Context()
	for _, bad := range []glapi.MRRef{
		{ProjectID: "", IID: 7}, {ProjectID: "42/../1", IID: 7}, {ProjectID: "group%2Fproj", IID: 7},
		{ProjectID: "42?x=1", IID: 7}, {ProjectID: "42", IID: 0}, {ProjectID: "42", IID: -1},
	} {
		if _, err := c.MR(ctx, token, bad); err == nil {
			t.Errorf("MR(%+v) error = nil, want a refusal", bad)
		}
		if err := c.BulkPublish(ctx, token, bad); err == nil {
			t.Errorf("BulkPublish(%+v) error = nil, want a refusal", bad)
		}
	}
	if err := c.DeleteDraft(ctx, token, ref, 0); err == nil {
		t.Error("DeleteDraft(id 0) error = nil, want a refusal")
	}
	for _, bad := range []string{"", "tok en", "tok\nen", "tök"} {
		if _, err := c.MR(ctx, bad, ref); err == nil {
			t.Errorf("MR(token %q) error = nil, want a refusal", bad)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("stub saw %d requests, want 0", hits.Load())
	}
}

func TestRedirect_IsNotFollowedAndTheTokenStaysHome(t *testing.T) {
	var other atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other.Add(1) }))
	t.Cleanup(target.Close)
	for _, method := range []string{"GET", "POST"} {
		c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, r *http.Request, _ rec) {
			http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect)
		})
		var err error
		if method == "GET" {
			_, err = c.MR(t.Context(), token, ref)
		} else {
			err = c.BulkPublish(t.Context(), token, ref)
		}
		var apiErr *glapi.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect {
			t.Errorf("%s: error = %v, want an *APIError with status 307", method, err)
		}
	}
	if other.Load() != 0 {
		t.Errorf("redirect target saw %d requests, want 0", other.Load())
	}
}

func TestRetries_GETTwiceOnTransientErrors_WritesNever(t *testing.T) {
	t.Run("GET succeeds on the third attempt", func(t *testing.T) {
		var n atomic.Int32
		c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
			if n.Add(1) < 3 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = io.WriteString(w, `{"iid":7}`)
		})
		if _, err := c.MR(t.Context(), token, ref); err != nil || n.Load() != 3 {
			t.Errorf("MR() error = %v after %d requests, want nil after 3", err, n.Load())
		}
	})
	t.Run("GET gives up after three attempts", func(t *testing.T) {
		var n atomic.Int32
		c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
			n.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		_, err := c.ListDrafts(t.Context(), token, ref)
		var apiErr *glapi.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 503 || n.Load() != 3 {
			t.Errorf("ListDrafts() error = %v after %d requests, want a 503 after 3", err, n.Load())
		}
	})
	t.Run("GET retried on a dropped connection", func(t *testing.T) {
		var n atomic.Int32
		c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
			if n.Add(1) < 2 {
				panic(http.ErrAbortHandler) // closes the connection without an answer
			}
			_, _ = io.WriteString(w, `{"iid":7}`)
		})
		if _, err := c.MR(t.Context(), token, ref); err != nil || n.Load() != 2 {
			t.Errorf("MR() error = %v after %d requests, want nil after 2", err, n.Load())
		}
	})
	t.Run("GET is not retried on a client error or 429", func(t *testing.T) {
		for _, code := range []int{400, 401, 403, 404, 429} {
			var n atomic.Int32
			c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
				n.Add(1)
				w.WriteHeader(code)
			})
			if _, err := c.MR(t.Context(), token, ref); err == nil || n.Load() != 1 {
				t.Errorf("MR() with status %d: error = %v after %d requests, want an error after 1", code, err, n.Load())
			}
		}
	})
	t.Run("writes are never retried", func(t *testing.T) {
		calls := map[string]func(*glapi.Client) error{
			"create": func(c *glapi.Client) error {
				_, err := c.CreateDraft(t.Context(), token, ref, glapi.NewDraft{Note: "n"})
				return err
			},
			"publish": func(c *glapi.Client) error { return c.PublishDraft(t.Context(), token, ref, 1) },
			"bulk":    func(c *glapi.Client) error { return c.BulkPublish(t.Context(), token, ref) },
			"delete":  func(c *glapi.Client) error { return c.DeleteDraft(t.Context(), token, ref, 1) },
			"note":    func(c *glapi.Client) error { return c.CreateNote(t.Context(), token, ref, "b") },
		}
		for name, call := range calls {
			for _, drop := range []bool{false, true} {
				var n atomic.Int32
				c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
					n.Add(1)
					if drop {
						panic(http.ErrAbortHandler)
					}
					w.WriteHeader(http.StatusInternalServerError)
				})
				if err := call(c); err == nil || n.Load() != 1 {
					t.Errorf("%s (dropped connection: %v): error = %v after %d requests, want an error after 1", name, drop, err, n.Load())
				}
			}
		}
	})
}

func TestRetry_StopsWhenTheContextIsCancelled(t *testing.T) {
	var n atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		cancel()
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	c, err := glapi.New(glapi.Options{APIURL: srv.URL + "/api/v4", Transport: srv.Client().Transport, RetryDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.MR(ctx, token, ref); done <- err }()
	select {
	case err := <-done:
		if err == nil || n.Load() != 1 {
			t.Errorf("MR() error = %v after %d requests, want an error after 1", err, n.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MR() did not return after the context was cancelled")
	}
}

func TestAPIError_MessageIsRedactedThenCutOnACharacter(t *testing.T) {
	// the token straddles byte 1000, and the 1000th byte is inside a three-byte character
	body := strings.Repeat("a", 990) + token + strings.Repeat("€", 100)
	c, _ := stub(t, "/gl/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, body)
	})
	err := c.BulkPublish(t.Context(), token, ref)
	var apiErr *glapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("BulkPublish() error = %v, want an *APIError with status 400", err)
	}
	const head = "GitLab API error posting MR review (/gl/api/v4/projects/42/merge_requests/7/draft_notes/bulk_publish): 400 "
	if !strings.HasPrefix(apiErr.Message, head) {
		t.Fatalf("message = %.150q, want the prefix %q", apiErr.Message, head)
	}
	if strings.Contains(apiErr.Message, token) || strings.Contains(apiErr.Message, "glpat-") {
		t.Errorf("message holds the token or a piece of it: %.200q", apiErr.Message)
	}
	text := strings.TrimPrefix(apiErr.Message, head)
	if len(text) > 1000 || !strings.Contains(text, "[REDACTED]") || !utf8.ValidString(text) {
		t.Errorf("body text is %d bytes (valid UTF-8: %v), want at most 1000, valid, with [REDACTED]", len(text), utf8.ValidString(text))
	}
}

func TestCreateNote_NamesTheCommentModeAndHasNoQueryInTheMessage(t *testing.T) {
	c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"403 Forbidden"}`)
	})
	err := c.CreateNote(t.Context(), token, ref, "b")
	want := `GitLab API error posting MR comment (/api/v4/projects/42/merge_requests/7/notes): 403 {"message":"403 Forbidden"}`
	if err == nil || err.Error() != want {
		t.Errorf("CreateNote() error = %v, want %q", err, want)
	}
	_, err = c.ListDrafts(t.Context(), token, ref)
	if err == nil || strings.Contains(err.Error(), "per_page") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("ListDrafts() error = %v, want the path without host or query", err)
	}
}

func TestTransportError_HasNoTokenAndNoHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	c, err := glapi.New(glapi.Options{APIURL: url + "/api/v4", RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	err = c.BulkPublish(t.Context(), token, ref)
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), url) ||
		!strings.HasPrefix(err.Error(), "GitLab API error posting MR review (/api/v4/projects/42/merge_requests/7/draft_notes/bulk_publish): ") {
		t.Errorf("BulkPublish() error = %v, want a transport error naming the path only", err)
	}
}

func TestUntrustedCertificateIsRefusedWithoutTheToken(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	c, err := glapi.New(glapi.Options{APIURL: srv.URL + "/api/v4", RetryDelay: time.Millisecond}) // system store only
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.MR(t.Context(), token, ref)
	if err == nil || strings.Contains(err.Error(), token) || hits.Load() != 0 {
		t.Errorf("MR() error = %v, handler hits = %d, want a certificate error and no request", err, hits.Load())
	}
}

func TestNewTransport_TrustsTheCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"iid":7}`)
	}))
	t.Cleanup(srv.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := glapi.NewTransport(caFile)
	if err != nil {
		t.Fatalf("NewTransport(CA file) error = %v", err)
	}
	c, err := glapi.New(glapi.Options{APIURL: srv.URL + "/api/v4", Transport: rt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.MR(t.Context(), token, ref); err != nil {
		t.Errorf("MR() with the CA file error = %v, want nil", err)
	}

	if rt, err := glapi.NewTransport(""); err != nil || rt != nil {
		t.Errorf("NewTransport(\"\") = %v, %v, want nil, nil (the default transport)", rt, err)
	}
	for name, path := range map[string]string{
		"missing":    filepath.Join(t.TempDir(), "nope.pem"),
		"not a cert": writeFile(t, "junk.pem", "SECRET-CONTENT"),
	} {
		_, err := glapi.NewTransport(path)
		if err == nil || !strings.Contains(err.Error(), "CI_SERVER_TLS_CA_FILE") || strings.Contains(err.Error(), "SECRET-CONTENT") {
			t.Errorf("NewTransport(%s) error = %v, want one naming CI_SERVER_TLS_CA_FILE and not the content", name, err)
		}
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResponseLargerThanTheCapIsAnError(t *testing.T) {
	c, _ := stub(t, "/api/v4", func(w http.ResponseWriter, _ *http.Request, _ rec) {
		_, _ = io.WriteString(w, `{"iid":7,"description":"`)
		_, _ = io.WriteString(w, strings.Repeat("a", 5<<20))
		_, _ = io.WriteString(w, `"}`)
	})
	if _, err := c.MR(t.Context(), token, ref); err == nil {
		t.Error("MR(5 MiB answer) error = nil, want an error")
	}
}

// The runner links no server code: neither the server's GitLab client nor anything outside the
// runner's allowed closure (the closure test of cmd/devdigest-runner holds the full list).
func TestPackage_ImportsNoServerCode(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	goBin := "go"
	if p, err := exec.LookPath("go"); err == nil {
		goBin = p
	} else {
		goBin = runtime.GOROOT() + "/bin/go"
	}
	cmd := exec.CommandContext(t.Context(), goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, forbidden := range []string{
		"dev-digest-go/backend/internal/platform/gitlab",
		"dev-digest-go/backend/internal/platform/secrets",
		"dev-digest-go/backend/internal/platform/gitcli",
		"dev-digest-go/backend/internal/platform/postgres",
		"dev-digest-go/backend/internal/platform/httpx",
		"dev-digest-go/backend/internal/gitlabconn",
		"dev-digest-go/backend/cmd/server",
		"github.com/labstack/echo",
		"gorm.io/",
		"github.com/jackc/",
	} {
		for _, pkg := range strings.Fields(string(out)) {
			if strings.HasPrefix(pkg, forbidden) {
				t.Errorf("glapi links %s (forbidden prefix %s)", pkg, forbidden)
			}
		}
	}
}
