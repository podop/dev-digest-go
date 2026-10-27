package gitlab_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
)

const testDiscID = "6a9c1750b37d513a43987b574953fceb50b03ce7"

func ptr(n int) *int { return &n }

func noteJSON(id int64, body string, extra map[string]any) map[string]any {
	n := map[string]any{
		"id": id, "type": "DiffNote", "system": false, "body": body,
		"created_at": "2026-10-27T10:00:00.000Z", "author": map[string]any{"username": "alice"},
	}
	for k, v := range extra {
		n[k] = v
	}
	return n
}

func goodPosition() gitlab.Position {
	return gitlab.Position{
		BaseSHA: "b", StartSHA: "s", HeadSHA: "h", OldPath: "old.go", NewPath: "new.go", NewLine: ptr(12),
	}
}

func TestDiscussionsPage(t *testing.T) {
	s := newStub(t, okJSON([]any{
		map[string]any{"id": testDiscID, "notes": []any{
			noteJSON(11, "root", map[string]any{"position": map[string]any{
				"position_type": "text", "base_sha": "b", "start_sha": "s", "head_sha": "h",
				"old_path": "a.go", "new_path": "a.go", "old_line": nil, "new_line": 7,
			}}),
			noteJSON(12, "reply", map[string]any{"type": "DiscussionNote", "author": nil}),
		}},
		map[string]any{"id": "abcdef012345", "notes": []any{
			noteJSON(13, "changed the title", map[string]any{"type": nil, "system": true}),
		}},
	}))
	got, err := s.client(gitlab.Options{}).DiscussionsPage(t.Context(), s.conn(), 42, 5, 2, 50)
	if err != nil {
		t.Fatalf("DiscussionsPage() error = %v", err)
	}
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	want := []gitlab.Discussion{
		{ID: testDiscID, Notes: []gitlab.Note{
			{ID: 11, Type: "DiffNote", Body: "root", Author: "alice", CreatedAt: at, Position: &gitlab.Position{
				Type: "text", BaseSHA: "b", StartSHA: "s", HeadSHA: "h", OldPath: "a.go", NewPath: "a.go", NewLine: ptr(7),
			}},
			{ID: 12, Type: "DiscussionNote", Body: "reply", Author: "unknown", CreatedAt: at},
		}},
		{ID: "abcdef012345", Notes: []gitlab.Note{
			{ID: 13, System: true, Body: "changed the title", Author: "alice", CreatedAt: at},
		}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DiscussionsPage() mismatch (-want +got):\n%s", diff)
	}
	if uri := s.req(0).URI; uri != "/api/v4/projects/42/merge_requests/5/discussions?page=2&per_page=50" {
		t.Errorf("request URI = %q", uri)
	}
	if m := s.req(0).Method; m != "GET" {
		t.Errorf("method = %s, want GET", m)
	}
}

func TestDiscussionsPage_RejectsBadInput(t *testing.T) {
	s := newStub(t, okJSON([]any{}))
	c := s.client(gitlab.Options{})
	tests := []struct {
		name          string
		pid           int64
		iid           int
		page, perPage int
	}{
		{"page 0", 1, 1, 0, 50}, {"page past the bound", 1, 1, gitlab.MaxPage + 1, 50},
		{"per_page 0", 1, 1, 1, 0}, {"per_page 101", 1, 1, 1, 101},
		{"project 0", 0, 1, 1, 50}, {"iid 0", 1, 0, 1, 50},
	}
	for _, tc := range tests {
		if _, err := c.DiscussionsPage(t.Context(), s.conn(), tc.pid, tc.iid, tc.page, tc.perPage); err == nil {
			t.Errorf("DiscussionsPage(%s) succeeded, want an error", tc.name)
		}
	}
	if n := s.count(); n != 0 {
		t.Errorf("%d requests for refused input, want 0", n)
	}
}

func TestDiscussionsPage_MalformedAnswersAreNotAPI(t *testing.T) {
	tests := map[string]any{
		"discussion without id": []any{map[string]any{"notes": []any{}}},
		"note without id":       []any{map[string]any{"id": testDiscID, "notes": []any{map[string]any{"body": "x"}}}},
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, okJSON(body))
			_, err := s.client(gitlab.Options{}).DiscussionsPage(t.Context(), s.conn(), 1, 1, 1, 100)
			if !errors.Is(err, gitlab.ErrNotAPI) {
				t.Errorf("DiscussionsPage() error = %v, want ErrNotAPI", err)
			}
		})
	}
}

func TestDiscussionsPage_IgnoresNextPageHeaders(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Next-Page", "9")
		w.Header().Set("Link", `<https://elsewhere.invalid/x>; rel="next"`)
		writeJSON(w, 200, []any{})
	})
	if _, err := s.client(gitlab.Options{}).DiscussionsPage(t.Context(), s.conn(), 1, 1, 1, 100); err != nil {
		t.Fatalf("DiscussionsPage() error = %v", err)
	}
	if n := s.count(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestCreateDiscussion_SendsOneJSONPost(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"id": testDiscID, "notes": []any{noteJSON(77, "hello", nil)}})
	})
	pos := goodPosition()
	got, err := s.client(gitlab.Options{}).CreateDiscussion(t.Context(), s.conn(), 42, 5, "hello\n/assign @bob", pos)
	if err != nil {
		t.Fatalf("CreateDiscussion() error = %v", err)
	}
	if got.ID != testDiscID || len(got.Notes) != 1 || got.Notes[0].ID != 77 {
		t.Errorf("CreateDiscussion() = %+v, want discussion %s with note 77", got, testDiscID)
	}
	r := s.req(0)
	if r.Method != "POST" || r.URI != "/api/v4/projects/42/merge_requests/5/discussions" {
		t.Errorf("request = %s %s, want POST …/discussions", r.Method, r.URI)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if r.Header.Get("PRIVATE-TOKEN") != testToken || strings.Contains(r.URI+r.Body, testToken) {
		t.Errorf("token must travel in PRIVATE-TOKEN only (uri %q)", r.URI)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(r.Body), &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	want := map[string]any{
		"body": "hello\n/assign @bob",
		"position": map[string]any{
			"position_type": "text", "base_sha": "b", "start_sha": "s", "head_sha": "h",
			"old_path": "old.go", "new_path": "new.go", "new_line": float64(12),
		},
	}
	if diff := cmp.Diff(want, sent); diff != "" {
		t.Errorf("request body mismatch (-want +got):\n%s", diff)
	}
}

func TestCreateDiscussion_BothLines(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"id": testDiscID, "notes": []any{noteJSON(1, "b", nil)}})
	})
	pos := goodPosition()
	pos.OldLine, pos.NewLine = ptr(3), ptr(4)
	if _, err := s.client(gitlab.Options{}).CreateDiscussion(t.Context(), s.conn(), 1, 1, "b", pos); err != nil {
		t.Fatalf("CreateDiscussion() error = %v", err)
	}
	var sent struct {
		Position map[string]any `json:"position"`
	}
	if err := json.Unmarshal([]byte(s.req(0).Body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Position["old_line"] != float64(3) || sent.Position["new_line"] != float64(4) {
		t.Errorf("position = %v, want old_line 3 and new_line 4", sent.Position)
	}
}

func TestCreateDiscussion_RefusesIncompleteInputWithoutRequest(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{}))
	c := s.client(gitlab.Options{})
	mut := func(f func(*gitlab.Position)) gitlab.Position { p := goodPosition(); f(&p); return p }
	tests := []struct {
		name string
		pid  int64
		iid  int
		pos  gitlab.Position
	}{
		{"no base sha", 1, 1, mut(func(p *gitlab.Position) { p.BaseSHA = "" })},
		{"no start sha", 1, 1, mut(func(p *gitlab.Position) { p.StartSHA = "" })},
		{"no head sha", 1, 1, mut(func(p *gitlab.Position) { p.HeadSHA = "" })},
		{"no new path", 1, 1, mut(func(p *gitlab.Position) { p.NewPath = "" })},
		{"no old path", 1, 1, mut(func(p *gitlab.Position) { p.OldPath = "" })},
		{"no line", 1, 1, mut(func(p *gitlab.Position) { p.NewLine = nil })},
		{"line 0", 1, 1, mut(func(p *gitlab.Position) { p.NewLine = ptr(0) })},
		{"image position", 1, 1, mut(func(p *gitlab.Position) { p.Type = "image" })},
		{"project 0", 0, 1, goodPosition()},
		{"iid 0", 1, 0, goodPosition()},
	}
	for _, tc := range tests {
		if _, err := c.CreateDiscussion(t.Context(), s.conn(), tc.pid, tc.iid, "b", tc.pos); err == nil {
			t.Errorf("CreateDiscussion(%s) succeeded, want an error", tc.name)
		}
	}
	if n := s.count(); n != 0 {
		t.Errorf("%d requests for refused input, want 0", n)
	}
}

func TestCreateDiscussion_AnswerWithoutNotesIsNotAPI(t *testing.T) {
	for name, body := range map[string]any{
		"no notes": map[string]any{"id": testDiscID, "notes": []any{}},
		"no id":    map[string]any{"notes": []any{noteJSON(1, "b", nil)}},
	} {
		s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 201, body) })
		_, err := s.client(gitlab.Options{}).CreateDiscussion(t.Context(), s.conn(), 1, 1, "b", goodPosition())
		if !errors.Is(err, gitlab.ErrNotAPI) {
			t.Errorf("CreateDiscussion(%s) error = %v, want ErrNotAPI", name, err)
		}
		if s.count() != 1 {
			t.Errorf("CreateDiscussion(%s): %d requests, want 1", name, s.count())
		}
	}
}

func TestCreateNote(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, noteJSON(88, "re", map[string]any{"type": "DiscussionNote"}))
	})
	got, err := s.client(gitlab.Options{}).CreateNote(t.Context(), s.conn(), 42, 5, testDiscID, "re")
	if err != nil {
		t.Fatalf("CreateNote() error = %v", err)
	}
	if got.ID != 88 || got.Body != "re" || got.Type != "DiscussionNote" || got.Author != "alice" {
		t.Errorf("CreateNote() = %+v", got)
	}
	r := s.req(0)
	wantURI := "/api/v4/projects/42/merge_requests/5/discussions/" + testDiscID + "/notes"
	if r.Method != "POST" || r.URI != wantURI {
		t.Errorf("request = %s %s, want POST %s", r.Method, r.URI, wantURI)
	}
	if r.Header.Get("Content-Type") != "application/json" || r.Body != `{"body":"re"}` {
		t.Errorf("body = %q (%s), want {\"body\":\"re\"} as JSON", r.Body, r.Header.Get("Content-Type"))
	}
}

func TestCreateNote_RefusesBadDiscussionID(t *testing.T) {
	s := newStub(t, okJSON(noteJSON(1, "b", nil)))
	c := s.client(gitlab.Options{})
	for _, id := range []string{"", "abc", "../x", testDiscID + "/notes", strings.ToUpper(testDiscID), strings.Repeat("a", 65), "zzzzzzzzzz", "ab cd ef gh"} {
		if _, err := c.CreateNote(t.Context(), s.conn(), 1, 1, id, "b"); err == nil {
			t.Errorf("CreateNote(discussion %q) succeeded, want an error", id)
		}
	}
	if _, err := c.CreateNote(t.Context(), s.conn(), 0, 1, testDiscID, "b"); err == nil {
		t.Error("CreateNote(project 0) succeeded")
	}
	if n := s.count(); n != 0 {
		t.Errorf("%d requests for refused input, want 0", n)
	}
}

func TestCreateNote_AnswerWithoutIDIsNotAPI(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 201, map[string]any{"body": "x"}) })
	if _, err := s.client(gitlab.Options{}).CreateNote(t.Context(), s.conn(), 1, 1, testDiscID, "x"); !errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("CreateNote() error = %v, want ErrNotAPI", err)
	}
}

// The writes are sent once whatever the answer (spec B FR11, P4), though reads retry the same.
func TestWrites_OneAttemptOnFailure(t *testing.T) {
	create := func(c *gitlab.Client, conn gitlab.Conn) error {
		_, err := c.CreateDiscussion(context.Background(), conn, 1, 1, "b", goodPosition())
		return err
	}
	reply := func(c *gitlab.Client, conn gitlab.Conn) error {
		_, err := c.CreateNote(context.Background(), conn, 1, 1, testDiscID, "b")
		return err
	}
	for _, status := range []int{500, 502, 503, 429, 400, 422} {
		for name, call := range map[string]func(*gitlab.Client, gitlab.Conn) error{"CreateDiscussion": create, "CreateNote": reply} {
			t.Run(fmt.Sprintf("%s %d", name, status), func(t *testing.T) {
				s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Retry-After", "0")
					writeJSON(w, status, map[string]any{"message": "boom"})
				})
				// The helper's policy is 3 retries: it must not apply.
				err := call(s.client(gitlab.Options{}), s.conn())
				var se *gitlab.StatusError
				if !errors.As(err, &se) || se.Status != status {
					t.Fatalf("%s error = %v, want *StatusError %d", name, err, status)
				}
				if n := s.count(); n != 1 {
					t.Errorf("%s sent %d requests on a %d, want 1", name, n, status)
				}
			})
		}
	}
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 503, map[string]any{}) })
	if _, err := s.client(gitlab.Options{}).User(t.Context(), s.conn()); err == nil {
		t.Fatal("User() succeeded on a 503")
	}
	if s.count() != 4 {
		t.Errorf("a GET got %d attempts on a 503, want 4 (reads keep the retry policy)", s.count())
	}
}

func TestWrites_OneAttemptOnTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		conn, hc := fakeNetStub(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = io.Copy(io.Discard, r.Body) // the server notices a closed connection only after the body is read
			blockUntilGone(w, r)
		})
		c := gitlab.New(gitlab.Options{HTTP: hc, Timeout: 5 * time.Second, Retry: &retry.Options{Retries: 3, BaseDelay: time.Second}})
		start := time.Now()
		_, err := c.CreateDiscussion(t.Context(), conn, 1, 1, "b", goodPosition())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("CreateDiscussion() error = %v, want a deadline error", err)
		}
		if took := time.Since(start); took != 5*time.Second {
			t.Errorf("gave up after %v, want 5s", took)
		}
		if _, err := c.CreateNote(t.Context(), conn, 1, 1, testDiscID, "b"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("CreateNote() error = %v, want a deadline error", err)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("%d requests for two timed out writes, want 2", n)
		}
	})
}

func TestWrites_OneAttemptOnConnectionReset(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		panic(http.ErrAbortHandler) // closes the connection without an answer
	})
	_, err := s.client(gitlab.Options{}).CreateNote(t.Context(), s.conn(), 1, 1, testDiscID, "b")
	if err == nil {
		t.Fatal("CreateNote() succeeded on a dropped connection")
	}
	if n := s.count(); n != 1 {
		t.Errorf("%d requests after a dropped connection, want 1", n)
	}
	assertNoToken(t, err.Error())
}

func TestWrites_ErrorsCarryNoTokenOrBody(t *testing.T) {
	const secret = "secret finding text"
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 400, map[string]any{"message": "echo " + testToken})
	})
	_, err := s.client(gitlab.Options{}).CreateDiscussion(t.Context(), s.conn(), 1, 1, secret, goodPosition())
	if err == nil {
		t.Fatal("CreateDiscussion() succeeded on a 400")
	}
	assertNoToken(t, err.Error())
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q contains the note body", err)
	}
}

func TestWrites_RefuseRedirectWithoutFollowing(t *testing.T) {
	other := newStub(t, okJSON(map[string]any{}))
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	})
	_, err := s.client(gitlab.Options{}).CreateNote(t.Context(), s.conn(), 1, 1, testDiscID, "b")
	if !errors.Is(err, gitlab.ErrRedirect) {
		t.Errorf("CreateNote() error = %v, want ErrRedirect", err)
	}
	if other.count() != 0 || s.count() != 1 {
		t.Errorf("redirect target got %d requests, origin %d; want 0 and 1", other.count(), s.count())
	}
}

func TestWrites_RefuseBadToken(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{}))
	conn := s.conn()
	conn.Token = "bad\ntoken"
	if _, err := s.client(gitlab.Options{}).CreateNote(t.Context(), conn, 1, 1, testDiscID, "b"); !errors.Is(err, gitlab.ErrToken) {
		t.Errorf("CreateNote() error = %v, want ErrToken", err)
	}
	if s.count() != 0 {
		t.Errorf("%d requests with an invalid token, want 0", s.count())
	}
}

func TestStatusError_RetryAfterSeconds(t *testing.T) {
	date := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(http.TimeFormat) }
	tests := []struct {
		name   string
		header string
		min    int
		max    int
	}{
		{"absent", "", 60, 60},
		{"seconds", "17", 17, 17},
		{"zero", "0", 60, 60},
		{"junk", "soon", 60, 60},
		{"date 30 s away", date(30 * time.Second), 29, 31},
		{"date in the past", date(-time.Hour), 60, 60},
		{"more than a day", "9999999", 86400, 86400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				writeJSON(w, 429, map[string]any{"message": "slow"})
			})
			_, err := s.client(gitlab.Options{Retry: &retry.Options{}}).CreateNote(t.Context(), s.conn(), 1, 1, testDiscID, "b")
			var se *gitlab.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("error = %v, want *StatusError", err)
			}
			if got := se.RetryAfterSeconds(); got < tc.min || got > tc.max {
				t.Errorf("RetryAfterSeconds() = %d, want %d..%d", got, tc.min, tc.max)
			}
		})
	}
	if got := (&gitlab.StatusError{Status: 429}).RetryAfterSeconds(); got != 60 {
		t.Errorf("RetryAfterSeconds() of a bare error = %d, want 60", got)
	}
}

func TestStatusError_InsufficientScope(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"403 error key", 403, `{"error":"insufficient_scope","error_description":"x","scope":"api"}`, true},
		{"403 message", 403, `{"message":"403 Forbidden - insufficient_scope"}`, true},
		{"403 other", 403, `{"message":"403 Forbidden"}`, false},
		{"401 same words", 401, `{"error":"insufficient_scope"}`, false},
		{"html 403", 403, `<html>insufficient_scope</html>`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := s.client(gitlab.Options{Retry: &retry.Options{}}).CreateNote(t.Context(), s.conn(), 1, 1, testDiscID, "b")
			var se *gitlab.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("error = %v, want *StatusError", err)
			}
			if got := se.InsufficientScope(); got != tc.want {
				t.Errorf("InsufficientScope() = %v, want %v (message %q)", got, tc.want, se.Message)
			}
		})
	}
}
