package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/config"
)

// GitLab B component tests of the failure and leak guarantees of the comment flows (plan S21, spec
// AC10, AC16, AC32, NFR1, NFR4, NFR6) through the real server against a TLS GitLab stub: every FR10
// answer maps to its code, reason and message, the create is sent exactly once, no token and no
// HTML of GitLab reaches a response, a header or a log line, GitHub is never called, and the only
// writes toward GitLab are the two POSTs of FR8 and FR9. The harness is the GitLab A isolation stub
// (giBoot); helpers here start with gf.

const (
	gfAPI       = "/api/v4/projects/7/"
	gfDisc      = "abcdef0123456789abcdef0123456789abcdef01"
	gfNewDisc   = "0123456789abcdef0123456789abcdef01234567"
	gfNoteID    = 901
	gfBase      = "1111111111111111111111111111111111111111"
	gfStart     = "2222222222222222222222222222222222222222"
	gfConnName  = "Local"
	gfBody      = "GF-BODY-MARKER the limiter drops tokens"
	gfHTMLMark  = "GF-HTML-MARKER"
	gfDoc       = "docs/design.md"
	gfDocText   = "# Design\n\nThe limiter is a token bucket.\n"
	gfSkillFile = "skills/limiter.md"
)

// gfEcho is a GitLab message that repeats the token, like a careless proxy would.
var gfEcho = "token " + giToken + " was rejected"

// gfHTML is an HTML error page that repeats the token.
var gfHTML = "<html><head><title>" + gfHTMLMark + "</title></head><body><h1>" + gfHTMLMark + "</h1>Error for " + giToken + "</body></html>"

// gfAnswer is what the stub answers to a request.
type gfAnswer struct {
	status int
	header map[string]string
	ctype  string // default application/json
	body   string
	hang   bool // block until the client gives up
	reset  bool // close the connection without an answer
}

func gfJSON(status int, v any) gfAnswer {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return gfAnswer{status: status, body: string(raw)}
}

func gfMsg(status int, message string) gfAnswer {
	return gfJSON(status, map[string]any{"message": message})
}

func (a gfAnswer) write(w http.ResponseWriter, r *http.Request) {
	switch {
	case a.reset:
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	case a.hang:
		select { // the request body was drained by the stub before the hook ran
		case <-r.Context().Done():
		case <-time.After(2 * time.Minute):
		}
		return
	}
	ct := a.ctype
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	for k, v := range a.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.body)
}

// gfGitLab answers what the comment flows read and write on top of the stub's defaults.
type gfGitLab struct {
	mu     sync.Mutex
	create *gfAnswer            // the answer to a discussion create (nil: 201 with the created thread)
	note   *gfAnswer            // the answer to a reply note (nil: 201 with the created note)
	all    *gfAnswer            // when set, the answer to every request of the project
	reads  map[string]*gfAnswer // answers to GETs whose path ends with the key
	refs   string               // "mr" (default): diff_refs of the MR; "versions": its newest version; "none"
}

func (g *gfGitLab) set(f func(g *gfGitLab)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

func gfNote(id int, body string) map[string]any {
	return map[string]any{"id": id, "type": "DiffNote", "system": false, "body": body,
		"author": map[string]any{"username": "ada"}, "created_at": "2026-10-21T10:00:00Z",
		"position": map[string]any{"position_type": "text", "base_sha": gfBase, "start_sha": gfStart, "head_sha": giHeadSHA,
			"old_path": "a.ts", "new_path": "a.ts", "old_line": nil, "new_line": 3}}
}

func (g *gfGitLab) handle(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, gfAPI) {
		return false
	}
	g.mu.Lock()
	all, create, note, refs := g.all, g.create, g.note, g.refs
	var read *gfAnswer
	if r.Method == http.MethodGet {
		for suffix, a := range g.reads {
			if strings.HasSuffix(p, suffix) {
				read = a
			}
		}
	}
	g.mu.Unlock()
	if all != nil {
		all.write(w, r)
		return true
	}
	if read != nil {
		read.write(w, r)
		return true
	}
	var in struct {
		Body string `json:"body"`
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/merge_requests/3/discussions"):
		giJSON(w, http.StatusOK, []any{map[string]any{"id": gfDisc, "notes": []any{gfNote(gfNoteID, "Looks wrong")}}})
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/merge_requests/3/discussions"):
		if create != nil {
			create.write(w, r)
			return true
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		giJSON(w, http.StatusCreated, map[string]any{"id": gfNewDisc, "notes": []any{gfNote(1001, in.Body)}})
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/merge_requests/3/discussions/"+gfDisc+"/notes"):
		if note != nil {
			note.write(w, r)
			return true
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		giJSON(w, http.StatusCreated, map[string]any{"id": 902, "type": nil, "system": false, "body": in.Body,
			"author": map[string]any{"username": "ada"}, "created_at": "2026-10-21T10:00:00Z"})
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/merge_requests/3/versions"):
		if refs == "versions" {
			giJSON(w, http.StatusOK, []any{map[string]any{"id": 5, "base_commit_sha": gfBase, "start_commit_sha": gfStart, "head_commit_sha": giHeadSHA}})
		} else {
			giJSON(w, http.StatusOK, []any{})
		}
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/merge_requests/3"):
		m := map[string]any{"iid": 3, "title": "Add feature", "state": "opened", "draft": false, "source_branch": "feat",
			"target_branch": "main", "sha": giHeadSHA, "source_project_id": giProjectID, "target_project_id": giProjectID,
			"created_at": "2026-10-19T10:00:00Z", "updated_at": "2026-10-20T10:00:00Z", "merged_at": nil, "closed_at": nil,
			"author": map[string]any{"username": "octo"}, "description": "Fixes #12. Design: " + gfDoc}
		if refs == "" || refs == "mr" {
			m["diff_refs"] = map[string]any{"base_sha": gfBase, "start_sha": gfStart, "head_sha": giHeadSHA}
		}
		giJSON(w, http.StatusOK, m)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/repository/files/"+gfDoc+"/raw"):
		_, _ = io.WriteString(w, gfDocText)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/repository/files/"+gfSkillFile+"/raw"):
		_, _ = io.WriteString(w, "---\nname: limiter\ndescription: Rate limits\n---\nUse a token bucket.\n")
	default:
		return false
	}
	return true
}

// gfEnv is a running server with a connection, the repository acme/web and its merge request !3.
type gfEnv struct {
	*giEnv
	stub *gfGitLab
	conn string
	repo string
	pr   string
}

// gfBoot starts the stack, connects with giToken, adds acme/web, syncs !3 and reads its detail
// (files, diff head, description) before any failure is forced. tune adjusts the server's spec.
func gfBoot(t *testing.T, tune func(*spec)) *gfEnv {
	t.Helper()
	e := &gfEnv{giEnv: giBoot(t, giOpts{spec: tune}), stub: &gfGitLab{}}
	e.conn = e.connect(giToken)
	e.repo = e.addRepo(giPath)
	e.pr = e.pullID(e.repo)
	e.gl.setHook(e.stub.handle)
	if resp := e.s.get("/pulls/" + e.pr); resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/%s = %d %s, want 200", e.pr, resp.Status, resp.Body)
	}
	// FR35: the detail fetch stores the diff head the comments are anchored to.
	var heads int64
	if err := e.s.db.DB.Raw(`SELECT count(*) FROM pull_requests WHERE id = ? AND diff_head IS NOT NULL`, e.pr).Scan(&heads).Error; err != nil || heads != 1 {
		t.Fatalf("stored diff heads after the detail fetch = %d (err %v), want 1", heads, err)
	}
	return e
}

func (e *gfEnv) comments() string { return "/pulls/" + e.pr + "/comments" }

// newComment is a post of the body on a.ts line 3 (an added line of the stub's patch).
func gfNewComment() map[string]any {
	return map[string]any{"path": "a.ts", "line": 3, "side": "RIGHT", "body": gfBody}
}

func gfReply() map[string]any {
	return map[string]any{"path": "a.ts", "line": 3, "body": gfBody, "in_reply_to": gfNoteID}
}

// slowPost is a POST with a client that outlasts the GitLab call timeout (30 s).
func (e *gfEnv) slowPost(path string, body any) response {
	e.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(e.t.Context(), http.MethodPost, e.s.base+path, bytes.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("POST %s: read body: %v", path, err)
	}
	return response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

// gfApp is the stub's requests after the first n that go to a REST or GraphQL endpoint (not git).
func gfApp(e *gfEnv, n int) []giReq {
	var out []giReq
	for _, r := range e.gl.since(n) {
		if strings.HasPrefix(r.Path, "/api/") {
			out = append(out, r)
		}
	}
	return out
}

// gfWrites are the POSTs of the comment flows after the first n: creates (new discussion) and
// notes (reply).
func gfWrites(e *gfEnv, n int) (creates, notes int) {
	for _, r := range e.gl.since(n) {
		switch {
		case r.Method != http.MethodPost:
		case strings.HasSuffix(r.Path, "/merge_requests/3/discussions"):
			creates++
		case strings.HasSuffix(r.Path, "/discussions/"+gfDisc+"/notes"):
			notes++
		}
	}
	return creates, notes
}

// gfWriteSurface fails for every request toward GitLab that writes and is not one of the wantWrites
// POSTs of FR8 or FR9 (NFR1): git (read-only upload-pack) and the read-only GraphQL query are the
// only other non-GET requests the stub may see.
func gfWriteSurface(t *testing.T, e *gfEnv, wantWrites int) {
	t.Helper()
	writes := 0
	for _, r := range e.gl.requests() {
		switch {
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
		case strings.Contains(r.Path, ".git/"):
		case r.Path == "/api/graphql":
			if strings.Contains(strings.ToLower(string(r.Body)), "mutation") {
				t.Errorf("GraphQL request writes: %s", r.Body)
			}
		case r.Method == http.MethodPost && (strings.HasSuffix(r.Path, "/merge_requests/3/discussions") ||
			(strings.HasSuffix(r.Path, "/notes") && strings.Contains(r.Path, "/merge_requests/3/discussions/"))):
			writes++
		default:
			t.Errorf("unexpected write toward GitLab: %s %s", r.Method, r.Path)
		}
	}
	if writes != wantWrites {
		t.Errorf("POSTs of FR8/FR9 sent = %d, want %d", writes, wantWrites)
	}
}

// gfLeaks lists what no response, header or log line may hold: the token (also as git's Basic
// credential), GitLab's HTML and the comment text.
func gfLeaks() []string {
	return []string{giToken, giBasic, "<html", "<h1>", gfHTMLMark}
}

func gfAssertClean(t *testing.T, where, text string, extra ...string) {
	t.Helper()
	assertNoSecret(t, where, text, append(gfLeaks(), extra...)...)
}

func gfAssertResponseClean(t *testing.T, where string, resp response) {
	t.Helper()
	gfAssertClean(t, where, string(resp.Body)+"\n"+fmt.Sprint(resp.Header))
}

// gfErr decodes the error envelope.
func gfErr(t *testing.T, resp response) (code, message string, details map[string]any) {
	t.Helper()
	env, ok := resp.Obj(t)["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no error envelope: %s", resp.Body)
	}
	code, _ = env["code"].(string)
	message, _ = env["message"].(string)
	details, _ = env["details"].(map[string]any)
	return code, message, details
}

// gfActionLog is the content of a "gitlab comment" INFO line.
type gfActionLog struct{ Op, Code, Reason string }

// gfActionLogs reads the "gitlab comment" lines: level, pr id and the comment text are checked
// here, the rest is returned for comparison.
func gfActionLogs(t *testing.T, e *gfEnv) []gfActionLog {
	t.Helper()
	var out []gfActionLog
	for _, l := range logLines(t, e.s.logs, "gitlab comment") {
		if l["level"] != "INFO" {
			t.Errorf("gitlab comment line level = %v, want INFO: %v", l["level"], l)
		}
		if l["pr_id"] != e.pr {
			t.Errorf("gitlab comment line pr_id = %v, want %s", l["pr_id"], e.pr)
		}
		op, _ := l["operation"].(string)
		code, _ := l["code"].(string)
		reason, hasReason := l["reason"].(string)
		if !hasReason {
			t.Errorf("gitlab comment line has no reason field: %v", l)
		}
		out = append(out, gfActionLog{op, code, reason})
	}
	return out
}

// gfCallLines counts the A NFR9 lines of the GitLab client (one per attempt).
func gfCallLines(t *testing.T, e *gfEnv) int {
	t.Helper()
	return len(logLines(t, e.s.logs, "gitlab call")) + len(logLines(t, e.s.logs, "gitlab call failed"))
}

// ---- AC10: every FR10 answer to a create or a reply ----

func TestGitLabCommentErrors_AC10_EveryFR10AnswerMapsAndTheCreateIsSentOnce(t *testing.T) {
	redirectHits := atomic.Int32{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	e := gfBoot(t, nil)
	ghBefore := len(e.s.gh.requests())

	const (
		msgPosition    = "GitLab could not place the comment on a.ts line 3. Reload the PR and try again."
		msgForbidden   = "GitLab refused the comment (403): the token's user may lack the Reporter role, or the discussion is locked."
		msgNotFound    = "GitLab did not find this merge request, or the token cannot see it."
		msgUnavailable = "GitLab is unavailable; the comment was not confirmed. Check the merge request before posting again."
		msgRedirect    = "GitLab answered with a redirect — check the connection's base URL."
		denied         = "gitlab_permission_denied"
		failed         = "gitlab_comment_failed"
	)
	longMsg := "409 Conflict: " + gfEcho + " " + strings.Repeat("é", 400)

	tests := []struct {
		name   string
		reply  bool
		slow   bool
		ans    gfAnswer
		code   string
		reason string
		msg    string
		retry  float64 // 0: no retry_after_s
	}{
		{name: "401", ans: gfMsg(401, "401 Unauthorized: "+gfEcho), code: denied, reason: "token_rejected",
			msg: "GitLab rejected the token of connection " + gfConnName + " (401)."},
		{name: "403 insufficient_scope", ans: gfJSON(403, map[string]any{"error": "insufficient_scope",
			"error_description": "The request requires higher privileges than provided by the access token. " + gfEcho, "scope": "api"}),
			code: denied, reason: "read_only_token", msg: "The token of connection " + gfConnName + " cannot post: it needs the api scope."},
		{name: "403 other", ans: gfMsg(403, "403 Forbidden: "+gfEcho), code: denied, reason: "forbidden", msg: msgForbidden},
		{name: "404", ans: gfMsg(404, "404 Not Found: "+gfEcho), code: failed, reason: "not_found", msg: msgNotFound},
		{name: "400 line_code", ans: gfMsg(400, "400 Bad request - Note {:line_code=>[\"can't be blank\", \"must be a valid line code\"]}"),
			code: failed, reason: "position_rejected", msg: msgPosition},
		{name: "422", ans: gfMsg(422, "Unprocessable Entity: "+gfEcho), code: failed, reason: "position_rejected", msg: msgPosition},
		{name: "500 HTML", ans: gfAnswer{status: 500, ctype: "text/html; charset=utf-8", body: gfHTML}, code: failed,
			reason: "position_rejected", msg: msgPosition},
		{name: "409", ans: gfMsg(409, "409 Conflict: the discussion changed"), code: failed, reason: "rejected",
			msg: "409 Conflict: the discussion changed"},
		{name: "409 long, echoing the token", ans: gfMsg(409, longMsg), code: failed, reason: "rejected"},
		{name: "429 Retry-After 37", ans: gfAnswer{status: 429, header: map[string]string{"Retry-After": "37"},
			body: `{"message":"429 Too Many Requests ` + gfEcho + `"}`}, code: "gitlab_rate_limited", reason: "rate_limited",
			msg: "GitLab limits how many comments can be posted per minute. Try again in 37 s.", retry: 37},
		{name: "429 without Retry-After", ans: gfMsg(429, "slow down"), code: "gitlab_rate_limited", reason: "rate_limited",
			msg: "GitLab limits how many comments can be posted per minute. Try again in 60 s.", retry: 60},
		{name: "302", ans: gfAnswer{status: 302, header: map[string]string{"Location": target.URL + "/steal?t=" + giToken},
			body: gfHTML, ctype: "text/html"}, code: failed, reason: "redirect", msg: msgRedirect},
		{name: "503 HTML", ans: gfAnswer{status: 503, ctype: "text/html", body: gfHTML}, code: failed, reason: "unavailable", msg: msgUnavailable},
		{name: "2xx that is not JSON", ans: gfAnswer{status: 200, ctype: "text/html", body: gfHTML}, code: failed, reason: "unavailable", msg: msgUnavailable},
		{name: "2xx JSON without a note", ans: gfJSON(201, map[string]any{"id": gfNewDisc, "notes": []any{}}), code: failed,
			reason: "unavailable", msg: msgUnavailable},
		{name: "connection closed", ans: gfAnswer{reset: true}, code: failed, reason: "unavailable", msg: msgUnavailable},
		{name: "reply 403 insufficient_scope", reply: true, ans: gfJSON(403, map[string]any{"error": "insufficient_scope"}),
			code: denied, reason: "read_only_token", msg: "The token of connection " + gfConnName + " cannot post: it needs the api scope."},
		{name: "reply 400 is a refusal, not a position", reply: true, ans: gfMsg(400, "400 Bad request: body is invalid"),
			code: failed, reason: "rejected", msg: "400 Bad request: body is invalid"},
		{name: "reply 500 is unavailable", reply: true, ans: gfAnswer{status: 500, ctype: "text/html", body: gfHTML},
			code: failed, reason: "unavailable", msg: msgUnavailable},
		{name: "reply 429 Retry-After 37", reply: true, ans: gfAnswer{status: 429, header: map[string]string{"Retry-After": "37"}, body: "{}"},
			code: "gitlab_rate_limited", reason: "rate_limited",
			msg: "GitLab limits how many comments can be posted per minute. Try again in 37 s.", retry: 37},
		{name: "reply 302", reply: true, ans: gfAnswer{status: 302, header: map[string]string{"Location": target.URL + "/steal"}},
			code: failed, reason: "redirect", msg: msgRedirect},
		{name: "timeout", slow: true, ans: gfAnswer{hang: true}, code: failed, reason: "unavailable", msg: msgUnavailable},
		{name: "reply timeout", slow: true, reply: true, ans: gfAnswer{hang: true}, code: failed, reason: "unavailable", msg: msgUnavailable},
	}

	var wantLogs []gfActionLog
	var writes int
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e.t = t
			ans := tc.ans
			e.stub.set(func(g *gfGitLab) {
				if tc.reply {
					g.note, g.create = &ans, nil
				} else {
					g.create, g.note = &ans, nil
				}
			})
			n := e.gl.count()
			body, op := any(gfNewComment()), "post"
			if tc.reply {
				body, op = gfReply(), "reply"
			}
			var resp response
			if tc.slow {
				resp = e.slowPost(e.comments(), body)
			} else {
				resp = e.s.post(e.comments(), body)
			}

			code, msg, details := gfErr(t, resp)
			if resp.Status != http.StatusBadRequest || code != tc.code {
				t.Fatalf("POST comment = %d %s, want 400 %s", resp.Status, resp.Body, tc.code)
			}
			if tc.msg != "" && msg != tc.msg {
				t.Errorf("message = %q, want %q", msg, tc.msg)
			}
			if details["reason"] != tc.reason {
				t.Errorf("details.reason = %v, want %s", details["reason"], tc.reason)
			}
			if _, ok := details["cause"].(string); !ok {
				t.Errorf("details.cause = %v, want a string", details["cause"])
			}
			wantKeys := []string{"cause", "reason"}
			if tc.retry != 0 {
				wantKeys = append(wantKeys, "retry_after_s")
				if details["retry_after_s"] != tc.retry {
					t.Errorf("details.retry_after_s = %v, want %v", details["retry_after_s"], tc.retry)
				}
			}
			var gotKeys []string
			for k := range details {
				gotKeys = append(gotKeys, k)
			}
			if diff := cmp.Diff(wantKeys, gotKeys, sortStrings); diff != "" {
				t.Errorf("details keys mismatch (-want +got):\n%s", diff)
			}
			if tc.name == "409 long, echoing the token" {
				if utf8.RuneCountInString(msg) > 300 || !strings.HasPrefix(msg, "409 Conflict: token ") || !strings.Contains(msg, "[REDACTED]") {
					t.Errorf("message = %q (%d runes), want the redacted message cut to 300 characters", msg, utf8.RuneCountInString(msg))
				}
			}
			if cause, _ := details["cause"].(string); len(cause) > 1000 {
				t.Errorf("details.cause has %d bytes, want at most 1000", len(cause))
			}
			gfAssertResponseClean(t, "the response", resp)
			gfAssertClean(t, "the response", string(resp.Body), gfBody)

			creates, notes := gfWrites(e, n)
			if tc.reply && (creates != 0 || notes != 1) || !tc.reply && (creates != 1 || notes != 0) {
				t.Errorf("the %s sent %d create(s) and %d note(s), want exactly one request to its own endpoint", op, creates, notes)
			}
			if got := redirectHits.Load(); got != 0 {
				t.Errorf("the redirect target received %d request(s), want none", got)
			}
			writes++
			wantLogs = append(wantLogs, gfActionLog{op, tc.code, tc.reason})
		})
	}
	e.t = t

	if diff := cmp.Diff(wantLogs, gfActionLogs(t, e)); diff != "" {
		t.Errorf("gitlab comment log lines mismatch (-want +got):\n%s", diff)
	}
	gfAssertClean(t, "the log", e.s.logs.String(), gfBody)
	gfWriteSurface(t, e, writes)
	if gh := len(e.s.gh.requests()); gh != ghBefore {
		t.Errorf("the failing posts sent %d GitHub request(s), want none: %v", gh-ghBefore, e.s.gh.requests()[ghBefore:])
	}
}

// FR10 closing paragraph: a rejected diff-refs, versions or discussion-list read uses the same
// table (position_rejected is for the create only), and no create follows.
func TestGitLabCommentErrors_AC10_RejectedReadsOfThePostFlowUseTheSameTable(t *testing.T) {
	e := gfBoot(t, nil)
	tests := []struct {
		name   string
		refs   string
		suffix string
		ans    gfAnswer
		reply  bool
		reason string
		code   string
		msg    string
	}{
		{name: "the MR read answers 401", suffix: "/merge_requests/3", ans: gfMsg(401, "401 Unauthorized: "+gfEcho),
			code: "gitlab_permission_denied", reason: "token_rejected", msg: "GitLab rejected the token of connection " + gfConnName + " (401)."},
		{name: "the MR read answers 404", suffix: "/merge_requests/3", ans: gfMsg(404, "404 Not found"),
			code: "gitlab_comment_failed", reason: "not_found", msg: "GitLab did not find this merge request, or the token cannot see it."},
		{name: "the MR read answers 400 (not a position)", suffix: "/merge_requests/3", ans: gfMsg(400, "400 Bad Request"),
			code: "gitlab_comment_failed", reason: "rejected", msg: "400 Bad Request"},
		{name: "the versions read answers 403", refs: "versions", suffix: "/versions", ans: gfMsg(403, "403 Forbidden: "+gfEcho),
			code: "gitlab_permission_denied", reason: "forbidden"},
		{name: "the versions read answers HTML", refs: "versions", suffix: "/versions", ans: gfAnswer{status: 200, ctype: "text/html", body: gfHTML},
			code: "gitlab_comment_failed", reason: "unavailable"},
		{name: "the discussion list answers 404", reply: true, suffix: "/discussions", ans: gfMsg(404, "404 Not found"),
			code: "gitlab_comment_failed", reason: "not_found"},
		{name: "the discussion list answers 429", reply: true, suffix: "/discussions", ans: gfAnswer{status: 429,
			header: map[string]string{"Retry-After": "37"}, body: "{}"}, code: "gitlab_rate_limited", reason: "rate_limited"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e.t = t
			ans := tc.ans
			e.stub.set(func(g *gfGitLab) {
				g.refs = tc.refs
				g.reads = map[string]*gfAnswer{tc.suffix: &ans}
			})
			n := e.gl.count()
			body := any(gfNewComment())
			if tc.reply {
				body = gfReply()
			}
			resp := e.s.post(e.comments(), body)
			code, msg, details := gfErr(t, resp)
			if resp.Status != http.StatusBadRequest || code != tc.code || details["reason"] != tc.reason {
				t.Fatalf("POST comment = %d %s, want 400 %s %s", resp.Status, resp.Body, tc.code, tc.reason)
			}
			if tc.msg != "" && msg != tc.msg {
				t.Errorf("message = %q, want %q", msg, tc.msg)
			}
			if tc.code == "gitlab_rate_limited" && details["retry_after_s"] != float64(37) {
				t.Errorf("details.retry_after_s = %v, want 37", details["retry_after_s"])
			}
			if creates, notes := gfWrites(e, n); creates+notes != 0 {
				t.Errorf("a failed read was followed by %d create(s) and %d note(s), want none", creates, notes)
			}
			gfAssertResponseClean(t, "the response", resp)
		})
	}
	e.t = t
	gfAssertClean(t, "the log", e.s.logs.String(), gfBody)
	gfWriteSurface(t, e, 0)
}

// ---- AC11 (not connected, no token): the refusal before any request ----

func TestGitLabCommentErrors_NoTokenAndNotConnectedSendNoRequest(t *testing.T) {
	e := gfBoot(t, nil)
	host := e.gl.hostKey()

	t.Run("a connection without a token is gitlab_unavailable", func(t *testing.T) {
		if r := e.s.do(http.MethodPut, "/settings/gitlab-connections/"+e.conn, map[string]any{"token": nil}); r.Status != http.StatusOK {
			t.Fatalf("PUT token null = %d %s, want 200", r.Status, r.Body)
		}
		n := e.gl.count()
		for name, body := range map[string]any{"post": gfNewComment(), "reply": gfReply()} {
			resp := e.s.post(e.comments(), body)
			code, msg, _ := gfErr(t, resp)
			want := "Connect a token for " + host + " in Settings → GitLab Integration to post comments."
			if resp.Status != http.StatusBadRequest || code != "gitlab_unavailable" || msg != want {
				t.Errorf("%s without a token = %d %s, want 400 gitlab_unavailable %q", name, resp.Status, resp.Body, want)
			}
			gfAssertResponseClean(t, name+" response", resp)
		}
		if reqs := e.gl.since(n); len(reqs) != 0 {
			t.Errorf("a connection without a token sent %d request(s), want none: %+v", len(reqs), reqs)
		}
	})
	t.Run("a repository without a connection is repo_host_not_connected", func(t *testing.T) {
		del := e.s.do(http.MethodDelete, "/settings/gitlab-connections/"+e.conn, nil)
		if del.Status != http.StatusOK {
			t.Fatalf("DELETE connection = %d %s, want 200", del.Status, del.Body)
		}
		n := e.gl.count()
		for name, body := range map[string]any{"post": gfNewComment(), "reply": gfReply()} {
			resp := e.s.post(e.comments(), body)
			code, msg, details := gfErr(t, resp)
			want := "No GitLab connection for " + host + ". Add one in Settings → GitLab Integration."
			if resp.Status != http.StatusUnprocessableEntity || code != "repo_host_not_connected" || msg != want {
				t.Errorf("%s without a connection = %d %s, want 422 repo_host_not_connected %q", name, resp.Status, resp.Body, want)
			}
			if diff := cmp.Diff(map[string]any{"host": host, "repo_id": e.repo}, details); diff != "" {
				t.Errorf("%s details mismatch (-want +got):\n%s", name, diff)
			}
		}
		if reqs := e.gl.since(n); len(reqs) != 0 {
			t.Errorf("a repository without a connection sent %d request(s), want none: %+v", len(reqs), reqs)
		}
	})
	if gh := len(e.s.gh.requests()); gh != 0 {
		t.Errorf("the refused posts sent %d GitHub request(s), want none", gh)
	}
	gfWriteSurface(t, e, 0)
}

// ---- AC16: one INFO line per action; no write outside the flow, none from a poll or job ----

func TestGitLabCommentErrors_AC16_OneInfoLinePerActionAndNoOtherWrite(t *testing.T) {
	clock := newPollClock()
	e := gfBoot(t, func(sp *spec) {
		sp.now, sp.pollTick = clock.Now, pollTick
		prev := sp.config
		sp.config = func(c *config.Config) {
			prev(c)
			c.PRPollEnabled = true
		}
	})

	var want []gfActionLog
	n := e.gl.count()
	if resp := e.s.post(e.comments(), gfNewComment()); resp.Status != http.StatusOK {
		t.Fatalf("POST comment = %d %s, want 200", resp.Status, resp.Body)
	}
	want = append(want, gfActionLog{"post", "ok", ""})
	// A post reads the MR (and the versions when its diff_refs are incomplete) and creates: FR7, FR8.
	for _, r := range gfApp(e, n) {
		ok := r.Method == http.MethodGet && strings.HasSuffix(r.Path, gfAPI+"merge_requests/3") ||
			r.Method == http.MethodPost && strings.HasSuffix(r.Path, gfAPI+"merge_requests/3/discussions")
		if !ok && !strings.HasPrefix(r.Path, "/api/graphql") && !strings.HasSuffix(r.Path, "/merge_requests") {
			t.Errorf("the post sent %s %s, want only the requests of FR7 and FR8", r.Method, r.Path)
		}
	}

	n = e.gl.count()
	if resp := e.s.post(e.comments(), gfReply()); resp.Status != http.StatusOK {
		t.Fatalf("POST reply = %d %s, want 200", resp.Status, resp.Body)
	}
	want = append(want, gfActionLog{"reply", "ok", ""})
	for _, r := range gfApp(e, n) {
		ok := r.Method == http.MethodGet && strings.HasSuffix(r.Path, gfAPI+"merge_requests/3/discussions") ||
			r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/discussions/"+gfDisc+"/notes")
		if !ok && !strings.HasPrefix(r.Path, "/api/graphql") && !strings.HasSuffix(r.Path, "/merge_requests") {
			t.Errorf("the reply sent %s %s, want only the requests of FR9", r.Method, r.Path)
		}
	}

	forbidden := gfMsg(403, "403 Forbidden: "+gfEcho)
	e.stub.set(func(g *gfGitLab) { g.create = &forbidden })
	if resp := e.s.post(e.comments(), gfNewComment()); resp.Status != http.StatusBadRequest {
		t.Fatalf("POST comment = %d %s, want 400", resp.Status, resp.Body)
	}
	want = append(want, gfActionLog{"post", "gitlab_permission_denied", "forbidden"})
	limited := gfAnswer{status: 429, header: map[string]string{"Retry-After": "37"}, body: "{}"}
	e.stub.set(func(g *gfGitLab) { g.note = &limited })
	if resp := e.s.post(e.comments(), gfReply()); resp.Status != http.StatusBadRequest {
		t.Fatalf("POST reply = %d %s, want 400", resp.Status, resp.Body)
	}
	want = append(want, gfActionLog{"reply", "gitlab_rate_limited", "rate_limited"})
	// Refusals before any GitLab request log their line too (validation errors are not actions).
	e.stub.set(func(g *gfGitLab) { g.create, g.note = nil, nil })
	if resp := e.s.post(e.comments(), map[string]any{"path": "nope.ts", "line": 3, "body": gfBody}); resp.Status != http.StatusBadRequest {
		t.Fatalf("POST comment on a file outside the diff = %d %s, want 400", resp.Status, resp.Body)
	}
	want = append(want, gfActionLog{"post", "gitlab_comment_failed", "file_not_in_diff"})

	if diff := cmp.Diff(want, gfActionLogs(t, e)); diff != "" {
		t.Errorf("gitlab comment log lines mismatch (-want +got):\n%s", diff)
	}
	for _, l := range logLines(t, e.s.logs, "gitlab comment") {
		if raw, _ := json.Marshal(l); strings.Contains(string(raw), "GF-BODY") || strings.Contains(string(raw), "limiter") {
			t.Errorf("gitlab comment line holds the comment text: %s", raw)
		}
	}

	// A poll cycle after the posts: it reads, and it never writes.
	clock.Advance(10 * time.Minute)
	eventually(t, 30*time.Second, "a poll cycle of the repository", func() bool {
		v := scalar[sql.NullTime](t, e.s.db.DB, `SELECT last_polled_at FROM repos WHERE id = ?`, e.repo)
		return v.Valid && v.Time.Equal(clock.Now())
	})
	time.Sleep(10 * pollTick) // let a cycle that is still running finish before the surface is read

	gfWriteSurface(t, e, 4) // the post, the reply and the two refused ones that reached GitLab
	gfAssertClean(t, "the log", e.s.logs.String(), gfBody)
	if gh := len(e.s.gh.requests()); gh != 0 {
		t.Errorf("the comment flows sent %d GitHub request(s), want none", gh)
	}
}

// ---- AC32: forced errors in every GitLab flow of the slice, the token echoed ----

func TestGitLabCommentErrors_AC32_NoTokenInAnyFlowWhenGitLabEchoesIt(t *testing.T) {
	e := gfBoot(t, nil)
	flows := []struct {
		name string
		run  func() response
	}{
		{"list comments", func() response { return e.s.get(e.comments()) }},
		{"post a comment", func() response { return e.s.post(e.comments(), gfNewComment()) }},
		{"reply", func() response { return e.s.post(e.comments(), gfReply()) }},
		{"prior PRs", func() response { return e.s.get("/pulls/" + e.pr + "/history") }},
		{"blast radius", func() response { return e.s.get("/pulls/" + e.pr + "/blast") }},
		{"brief (issue)", func() response { return e.s.post("/pulls/"+e.pr+"/brief", nil) }},
		{"intent (ticket and doc)", func() response { return e.s.post("/pulls/"+e.pr+"/intent/refresh", nil) }},
		{"skill import", func() response {
			return e.s.post("/skills/import/preview", map[string]any{"kind": "url", "url": e.gl.baseURL() + "/" + giPath + "/-/blob/main/" + gfSkillFile})
		}},
	}
	styles := []struct {
		name string
		ans  gfAnswer
	}{
		{"403 JSON echo", gfAnswer{status: 403, body: `{"message":"403 Forbidden: ` + gfEcho + `","error":"` + giToken + `"}`,
			header: map[string]string{"X-Echo": giToken, "WWW-Authenticate": `Bearer realm="` + giToken + `"`}}},
		{"401 JSON echo", gfMsg(401, "401 Unauthorized: "+gfEcho)},
		{"500 HTML echo", gfAnswer{status: 500, ctype: "text/html", body: gfHTML}},
		{"200 HTML echo", gfAnswer{status: 200, ctype: "text/html", body: gfHTML}},
	}

	ghBefore := len(e.s.gh.requests())
	var seen []string
	for si, st := range styles {
		ans := st.ans
		e.stub.set(func(g *gfGitLab) { g.all = &ans })
		for _, f := range flows {
			t.Run(st.name+"/"+f.name, func(t *testing.T) {
				e.t = t
				n, lines := e.gl.count(), gfCallLines(t, e)
				resp := f.run()
				gfAssertResponseClean(t, "the response", resp)
				seen = append(seen, string(resp.Body)+fmt.Sprint(resp.Header))
				if resp.Status >= 500 {
					t.Errorf("%s = %d %s, want a handled answer, not a server error", f.name, resp.Status, resp.Body)
				}
				// A NFR9: every attempt of the client logged one line.
				requests := len(gfApp(e, n))
				if got := gfCallLines(t, e) - lines; got != requests {
					t.Errorf("%s: %d GitLab request(s) but %d client log line(s), want one line per request", f.name, requests, got)
				}
				if si == 0 && requests == 0 && f.name != "blast radius" { // the blast radius may be served from stored data
					t.Errorf("%s sent no GitLab request: the forced error was never reached", f.name)
				}
			})
		}
	}
	e.t = t

	all := strings.Join(seen, "\n")
	gfAssertClean(t, "a response", all)
	if !strings.Contains(all, "[REDACTED]") {
		t.Error("no response holds [REDACTED]: GitLab's echo never reached the redactor, so the checks above prove nothing")
	}
	gfAssertClean(t, "the log", e.s.logs.String(), gfBody)
	gfAssertClean(t, "the stored rows (intent details included)", e.dump())
	if gh := len(e.s.gh.requests()); gh != ghBefore {
		t.Errorf("the failing flows sent %d GitHub request(s), want none: %v", gh-ghBefore, e.s.gh.requests()[ghBefore:])
	}
	creates, notes := gfWrites(e, 0)
	gfWriteSurface(t, e, creates+notes)
}

// sortStrings orders string slices for cmp.
var sortStrings = cmpopts.SortSlices(func(a, b string) bool { return a < b })
