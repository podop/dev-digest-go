package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// GitLab B component tests of the context peers (plan S22, spec AC21, AC22, AC24, AC25): prior
// merged MRs, the brief's linked issue, the intent's tickets and the intent's docs of a GitLab merge
// request, end to end through the real server against the TLS GitLab stub of the isolation tests
// (giServer). Each test asserts the exact GitLab requests (path as sent, query, PRIVATE-TOKEN), the
// HTTP response and that GitHub saw nothing; "not connected" and "no token" send no request.
// Helpers are prefixed gp.

// gpReq is one GitLab API request: the path as it went over the wire (escapes kept), the query, the
// PRIVATE-TOKEN header and whether an Authorization header came with it.
type gpReq struct {
	Method, Path, Query, Private, Auth string
}

func (r gpReq) String() string { return r.Method + " " + r.Path + "?" + r.Query }

type gpIssue struct {
	status      int
	title, body string
	delay       time.Duration
}

type gpFile struct {
	status int
	body   string
}

// gpStub answers what the context peers read on top of the isolation stub: the merge request !3
// (with the stub's description and the real head commit of the clone), the merged list and the diffs
// of other merge requests, issues and raw files of project 7.
type gpStub struct {
	e       *giEnv
	headSHA string

	mu          sync.Mutex
	description string
	reqs        []gpReq
	merged      []map[string]any
	files       map[int][]string // paths of the diffs of a merge request (!3 is the pull request)
	diffStatus  map[int]int      // a diffs request that fails with this status
	issues      map[int]gpIssue
	raw         map[string]gpFile // raw files by decoded path
	inFlight    map[string]int
	maxInFlight map[string]int
}

var gpRoute = regexp.MustCompile(`^/api/v4/projects/7/(merge_requests(?:/(\d+)(?:/(diffs))?)?|issues/(\d+)|repository/files/(.+)/raw)$`)

func (g *gpStub) mrJSON() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	m := g.e.gl.mr(true)
	m["sha"] = g.headSHA
	m["description"] = g.description
	return m
}

// enter counts one request of kind as in flight; the returned func leaves it.
func (g *gpStub) enter(kind string) func() {
	g.mu.Lock()
	g.inFlight[kind]++
	g.maxInFlight[kind] = max(g.maxInFlight[kind], g.inFlight[kind])
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.inFlight[kind]--
		g.mu.Unlock()
	}
}

func gpDiffs(paths []string) []any {
	out := make([]any, 0, len(paths))
	for _, p := range paths {
		out = append(out, map[string]any{"old_path": p, "new_path": p, "new_file": false, "renamed_file": false,
			"deleted_file": false, "diff": "@@ -1 +1,2 @@\n-a\n+b\n+c\n"})
	}
	return out
}

func (g *gpStub) handle(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v4/") {
		return false
	}
	g.mu.Lock()
	g.reqs = append(g.reqs, gpReq{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery,
		Private: r.Header.Get("PRIVATE-TOKEN"), Auth: r.Header.Get("Authorization")})
	g.mu.Unlock()

	m := gpRoute.FindStringSubmatch(r.URL.Path)
	if m == nil || r.Method != http.MethodGet {
		return false
	}
	switch {
	case m[1] == "merge_requests":
		g.mu.Lock()
		merged := g.merged
		g.mu.Unlock()
		if r.URL.Query().Get("state") == "merged" {
			giJSON(w, http.StatusOK, merged)
		} else {
			giJSON(w, http.StatusOK, []any{g.mrJSON()})
		}
	case m[2] != "" && m[3] == "":
		if m[2] != "3" {
			return false
		}
		giJSON(w, http.StatusOK, g.mrJSON())
	case m[3] == "diffs":
		defer g.enter("diffs")()
		iid, _ := strconv.Atoi(m[2])
		time.Sleep(15 * time.Millisecond) // lets concurrent reads overlap
		g.mu.Lock()
		status, files := g.diffStatus[iid], g.files[iid]
		g.mu.Unlock()
		if status != 0 {
			giJSON(w, status, map[string]any{"message": strconv.Itoa(status) + " failure"})
			return true
		}
		giJSON(w, http.StatusOK, gpDiffs(files))
	case m[4] != "":
		defer g.enter("issues")()
		n, _ := strconv.Atoi(m[4])
		g.mu.Lock()
		is, ok := g.issues[n]
		g.mu.Unlock()
		if !ok {
			giJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
			return true
		}
		if is.delay > 0 {
			select {
			case <-r.Context().Done():
				return true
			case <-time.After(is.delay):
			}
		}
		if is.status != http.StatusOK {
			giJSON(w, is.status, map[string]any{"message": strconv.Itoa(is.status) + " Not found"})
			return true
		}
		time.Sleep(15 * time.Millisecond)
		giJSON(w, http.StatusOK, map[string]any{"iid": n, "title": is.title, "description": is.body, "state": "opened"})
	case m[5] != "":
		g.mu.Lock()
		f, ok := g.raw[m[5]]
		g.mu.Unlock()
		if !ok || f.status != http.StatusOK && f.status != 0 {
			status := f.status
			if status == 0 {
				status = http.StatusNotFound
			}
			giJSON(w, status, map[string]any{"message": "404 File Not Found"})
			return true
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(f.body))
	default:
		return false
	}
	return true
}

func (g *gpStub) mark() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reqs)
}

// since lists the API requests after the first n.
func (g *gpStub) since(n int) []gpReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n > len(g.reqs) {
		return nil
	}
	return slices.Clone(g.reqs[n:])
}

func (g *gpStub) maxIn(kind string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxInFlight[kind]
}

func (g *gpStub) set(f func(g *gpStub)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

// gpOpts tunes gpBoot.
type gpOpts struct {
	// description is the merge request's description, given the stub's host key (nil: empty).
	description func(host string) string
	files       map[string]string // extra files of the acme/web clone
}

// gpBoot starts the stack with a connection, acme/web and its merge request !3, whose description
// and head commit (the real head of the clone) the stub decides, and whose diff holds a.ts,
// src/b.ts and c.ts.
func gpBoot(t *testing.T, o gpOpts) (e *giEnv, g *gpStub, connID, pr string) {
	t.Helper()
	files := maps.Clone(tourFixture)
	maps.Copy(files, o.files)
	e = giBoot(t, giOpts{files: files})
	g = &gpStub{e: e, files: map[int][]string{3: {"a.ts", "src/b.ts", "c.ts"}},
		diffStatus: map[int]int{}, issues: map[int]gpIssue{}, raw: map[string]gpFile{},
		inFlight: map[string]int{}, maxInFlight: map[string]int{}}
	if o.description != nil {
		g.description = o.description(e.gl.hostKey())
	}
	g.headSHA = gitRun(t, filepath.Join(e.gl.gitRoot, "acme", "web.git"), "rev-parse", "main")
	e.gl.setHook(g.handle)
	connID = e.connect(giToken)
	repo := e.addRepo(giPath)
	pr = e.pullID(repo)
	if resp := e.s.get("/pulls/" + pr); resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/%s = %d %s, want 200", pr, resp.Status, resp.Body)
	}
	return e, g, connID, pr
}

// gpNoGitHub fails when GitHub saw any request: a GitLab repository never reaches it.
func gpNoGitHub(t *testing.T, e *giEnv) {
	t.Helper()
	if got := e.s.gh.requests(); len(got) != 0 {
		t.Errorf("the GitHub stub saw %d request(s), want none: %v", len(got), got)
	}
}

// gpDropToken removes the connection's token; gpDeleteConnection removes the connection.
func gpDropToken(t *testing.T, e *giEnv, connID string) {
	t.Helper()
	if r := e.s.do(http.MethodPut, "/settings/gitlab-connections/"+connID, map[string]any{"token": nil}); r.Status != http.StatusOK {
		t.Fatalf("PUT token null = %d %s, want 200", r.Status, r.Body)
	}
}

func gpDeleteConnection(t *testing.T, e *giEnv, connID string) {
	t.Helper()
	if r := e.s.do(http.MethodDelete, "/settings/gitlab-connections/"+connID, nil); r.Status/100 != 2 {
		t.Fatalf("DELETE connection = %d %s, want 2xx", r.Status, r.Body)
	}
}

// gpQuery parses a raw query; a malformed one fails the test.
func gpQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("query %q: %v", raw, err)
	}
	return q
}

// gpCheckAuth fails for a request that did not carry the connection's token in PRIVATE-TOKEN alone.
func gpCheckAuth(t *testing.T, reqs []gpReq) {
	t.Helper()
	for _, r := range reqs {
		if r.Private != giToken || r.Auth != "" {
			t.Errorf("%s carried PRIVATE-TOKEN %q and Authorization %q, want the connection's token and no Authorization", r, r.Private, r.Auth)
		}
	}
}

// ---- AC21: prior merge requests ----

type gpPrior struct {
	PRNumber     int      `json:"pr_number"`
	Title        string   `json:"title"`
	MergedAt     string   `json:"merged_at"`
	Author       string   `json:"author"`
	FilesOverlap []string `json:"files_overlap"`
	Notes        string   `json:"notes"`
}

// gpMergedAt is the merge time of !iid: one hour per number, as GitLab prints it.
func gpMergedAt(iid int) string {
	return time.Date(2026, 10, 1, 0, 0, 0, 123_000_000, time.UTC).Add(time.Duration(iid) * time.Hour).Format("2006-01-02T15:04:05.000Z07:00")
}

// gpHistoryFixture is the merged list of the EC19 shape: 30 merge requests, 12 of them overlapping
// the pull request's files a.ts, src/b.ts and c.ts (!3 itself, !105 with an unparsable merged_at,
// !120 with a null one, nine more), the rest in other directories. It returns the list, the diff
// paths of each and the answer the route must give.
func gpHistoryFixture() (list []map[string]any, files map[int][]string, want []gpPrior) {
	overlap := map[int][]string{105: {"a.ts"}, 120: {"a.ts"}, 112: {"c.ts", "c.ts", "a.ts"}}
	for _, iid := range []int{106, 107, 108, 109, 110, 111, 113, 114} {
		if iid%2 == 0 {
			overlap[iid] = []string{"src/b.ts", "a.ts", "other/e.ts"}
		} else {
			overlap[iid] = []string{"a.ts", "x/o.ts"}
		}
	}
	files = map[int][]string{}
	for iid := 101; iid <= 129; iid++ {
		files[iid] = []string{"other/" + strconv.Itoa(iid) + ".ts"}
		if f, ok := overlap[iid]; ok {
			files[iid] = f
		}
	}
	files[3] = []string{"a.ts", "src/b.ts", "c.ts"}

	for _, iid := range append([]int{3}, rangeInts(101, 129)...) {
		m := map[string]any{"iid": iid, "title": "MR " + strconv.Itoa(iid), "state": "merged",
			"merged_at": gpMergedAt(iid), "author": map[string]any{"username": "u" + strconv.Itoa(iid)}}
		switch iid {
		case 105:
			m["merged_at"] = "last Tuesday"
		case 120:
			m["merged_at"] = nil
		case 111:
			m["author"] = nil
		}
		list = append(list, m)
	}
	// The newest ten overlapping merge requests without !3: !114 down to !106, then the ones without a
	// usable time by descending number (!120; !105 falls off the cap of ten).
	for _, iid := range []int{114, 113, 112, 111, 110, 109, 108, 107, 106, 120} {
		p := gpPrior{PRNumber: iid, Title: "MR " + strconv.Itoa(iid), MergedAt: gpMergedAt(iid), Author: "u" + strconv.Itoa(iid),
			FilesOverlap: []string{"a.ts"}}
		switch {
		case iid == 120:
			p.MergedAt = ""
		case iid == 111:
			p.Author = "unknown"
		case iid == 112:
			p.FilesOverlap = []string{"a.ts", "c.ts"}
		case iid%2 == 0:
			p.FilesOverlap = []string{"a.ts", "src/b.ts"}
		}
		want = append(want, p)
	}
	return list, files, want
}

func rangeInts(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func gpHistory(t *testing.T, e *giEnv, pr string) response {
	t.Helper()
	resp := e.s.get("/pulls/" + pr + "/history")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/%s/history = %d %s, want 200", pr, resp.Status, resp.Body)
	}
	return resp
}

// AC21 (FR23, FR24, EC19, NFR5): one merged list and at most 30 first-page diffs reads, at most five
// at once, selected like GitHub's (ten, the current MR left out, null merged_at as ""); one failing
// read, a missing token and a missing connection all answer an empty history, the last two
// without a request; GitHub sees nothing.
func TestGitLabPeers_AC21_PriorMergeRequests(t *testing.T) {
	e, g, connID, pr := gpBoot(t, gpOpts{})
	list, files, want := gpHistoryFixture()
	g.set(func(g *gpStub) {
		g.merged = list
		maps.Copy(g.files, files)
	})

	t.Run("the merged list and the diffs of each", func(t *testing.T) {
		n := g.mark()
		resp := gpHistory(t, e, pr)
		var got struct {
			History []gpPrior `json:"history"`
		}
		if err := json.Unmarshal(resp.Body, &got); err != nil {
			t.Fatalf("history body %s: %v", resp.Body, err)
		}
		if diff := cmp.Diff(want, got.History); diff != "" {
			t.Errorf("GET history mismatch (-want +got):\n%s", diff)
		}
		if !strings.Contains(string(resp.Body), `"merged_at":""`) {
			t.Errorf("history %s holds no merged_at \"\" for the merge request GitLab sent null for (A8)", resp.Body)
		}

		reqs := g.since(n)
		gpCheckAuth(t, reqs)
		var lists, diffs []gpReq
		for _, r := range reqs {
			switch {
			case r.Method == http.MethodGet && r.Path == "/api/v4/projects/7/merge_requests":
				lists = append(lists, r)
			case r.Method == http.MethodGet && strings.HasSuffix(r.Path, "/diffs"):
				diffs = append(diffs, r)
			default:
				t.Errorf("history sent %s, want only the merged list and diffs reads", r)
			}
		}
		if len(lists) != 1 {
			t.Fatalf("history sent %d merged list request(s) %v, want 1", len(lists), lists)
		}
		wantList := url.Values{"state": {"merged"}, "order_by": {"updated_at"}, "sort": {"desc"}, "per_page": {"30"}, "page": {"1"}}
		if diff := cmp.Diff(wantList, gpQuery(t, lists[0].Query)); diff != "" {
			t.Errorf("merged list query mismatch (-want +got):\n%s", diff)
		}
		var paths []string
		for _, d := range diffs {
			paths = append(paths, d.Path)
			if diff := cmp.Diff(url.Values{"per_page": {"100"}, "page": {"1"}}, gpQuery(t, d.Query)); diff != "" {
				t.Errorf("%s query mismatch (-want +got):\n%s", d, diff)
			}
		}
		slices.Sort(paths)
		var wantPaths []string
		for _, iid := range append([]int{3}, rangeInts(101, 129)...) {
			wantPaths = append(wantPaths, "/api/v4/projects/7/merge_requests/"+strconv.Itoa(iid)+"/diffs")
		}
		slices.Sort(wantPaths)
		if diff := cmp.Diff(wantPaths, paths); diff != "" {
			t.Errorf("diffs requests mismatch, want one per listed merge request (-want +got):\n%s", diff)
		}
		if got := g.maxIn("diffs"); got > 5 {
			t.Errorf("%d diffs requests were in flight at once, want at most 5", got)
		}
	})

	t.Run("one failing diffs request empties the history", func(t *testing.T) {
		g.set(func(g *gpStub) { g.diffStatus[117] = http.StatusNotFound })
		defer g.set(func(g *gpStub) { delete(g.diffStatus, 117) })
		n := g.mark()
		resp := gpHistory(t, e, pr)
		if strings.TrimSpace(string(resp.Body)) != `{"history":[]}` {
			t.Errorf("GET history with a failing diffs read = %s, want {\"history\":[]}", resp.Body)
		}
		if reqs := g.since(n); len(reqs) == 0 || len(reqs) > 31 {
			t.Errorf("history sent %d request(s), want the list and at most 30 diffs reads", len(reqs))
		}
		if !strings.Contains(e.s.logs.String(), "prior PR history unavailable") {
			t.Error("the failed read left no log line")
		}
		assertNoSecret(t, "the log", e.s.logs.String(), giToken)
		if got := g.maxIn("diffs"); got > 5 {
			t.Errorf("%d diffs requests were in flight at once, want at most 5", got)
		}
	})

	t.Run("no token sends no request", func(t *testing.T) {
		gpDropToken(t, e, connID)
		n := g.mark()
		resp := gpHistory(t, e, pr)
		if strings.TrimSpace(string(resp.Body)) != `{"history":[]}` {
			t.Errorf("GET history without a token = %s, want {\"history\":[]}", resp.Body)
		}
		if reqs := g.since(n); len(reqs) != 0 {
			t.Errorf("history without a token sent %v, want no request", reqs)
		}
	})

	t.Run("not connected sends no request", func(t *testing.T) {
		gpDeleteConnection(t, e, connID)
		n := g.mark()
		resp := gpHistory(t, e, pr)
		if strings.TrimSpace(string(resp.Body)) != `{"history":[]}` {
			t.Errorf("GET history without a connection = %s, want {\"history\":[]}", resp.Body)
		}
		if reqs := g.since(n); len(reqs) != 0 {
			t.Errorf("history without a connection sent %v, want no request", reqs)
		}
	})

	gpNoGitHub(t, e)
}

// ---- AC22: the brief's linked issue ----

// gpBrief generates the brief and returns its missing inputs as "input/reason" and the prompt of the
// one model call it made.
func gpBrief(t *testing.T, e *giEnv, pr string) (missing []string, prompt string) {
	t.Helper()
	before := countOf(e.sc, "PrBriefLlm")
	resp := e.s.post("/pulls/"+pr+"/brief", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /pulls/%s/brief = %d %s, want 200 (the brief is generated without the issue)", pr, resp.Status, resp.Body)
	}
	for _, m := range obj(t, resp.JSON(t), "brief")["missing_inputs"].([]any) {
		mm := m.(map[string]any)
		missing = append(missing, fmt.Sprint(mm["input"], "/", mm["reason"]))
	}
	attempts := attemptsOf(e.sc, "PrBriefLlm")
	if len(attempts) != before+1 {
		t.Fatalf("the brief made %d model call(s), want 1", len(attempts)-before)
	}
	return missing, userOf(attempts[len(attempts)-1])
}

// AC22 (FR25): an MR "Fixes #7" reads GET /projects/7/issues/7 and the prompt carries its title and
// description; a 404, an answer later than the 10 s bound, a missing token and a missing connection
// all give missing_inputs {linked_issue, fetch_failed} and a stored brief; GitHub sees nothing.
func TestGitLabPeers_AC22_BriefLinkedIssue(t *testing.T) {
	const (
		issueTitle = "Limiter drops bursts"
		issueBody  = "Bursts above one hundred requests per second vanish without a trace."
	)
	e, g, connID, pr := gpBoot(t, gpOpts{description: func(string) string { return "Fixes #7 and tunes the limiter." }})
	issueReqs := func(n int) []gpReq {
		var out []gpReq
		for _, r := range g.since(n) {
			if strings.Contains(r.Path, "/issues/") {
				out = append(out, r)
			}
		}
		return out
	}
	oneRead := func(t *testing.T, n int) {
		t.Helper()
		reqs := g.since(n)
		want := []gpReq{{Method: http.MethodGet, Path: "/api/v4/projects/7/issues/7", Query: "", Private: giToken}}
		if diff := cmp.Diff(want, reqs); diff != "" {
			t.Errorf("the brief sent (-want +got):\n%s", diff)
		}
	}

	t.Run("the issue is in the prompt", func(t *testing.T) {
		g.set(func(g *gpStub) { g.issues[7] = gpIssue{status: http.StatusOK, title: issueTitle, body: issueBody} })
		n := g.mark()
		missing, prompt := gpBrief(t, e, pr)
		oneRead(t, n)
		if !strings.Contains(prompt, issueTitle) || !strings.Contains(prompt, issueBody) {
			t.Errorf("the brief prompt lacks the issue's title and description:\n%s", prompt)
		}
		if slices.ContainsFunc(missing, func(m string) bool { return strings.HasPrefix(m, "linked_issue/") }) {
			t.Errorf("missing_inputs = %v, want no linked_issue entry", missing)
		}
	})
	t.Run("a 404 is fetch_failed", func(t *testing.T) {
		g.set(func(g *gpStub) { g.issues[7] = gpIssue{status: http.StatusNotFound} })
		n := g.mark()
		missing, prompt := gpBrief(t, e, pr)
		oneRead(t, n)
		if !slices.Contains(missing, "linked_issue/fetch_failed") || strings.Contains(prompt, issueTitle) {
			t.Errorf("after a 404: missing_inputs = %v, prompt has the issue = %v, want linked_issue/fetch_failed and no issue", missing, strings.Contains(prompt, issueTitle))
		}
	})
	t.Run("an answer after 11 s is fetch_failed", func(t *testing.T) {
		g.set(func(g *gpStub) {
			g.issues[7] = gpIssue{status: http.StatusOK, title: issueTitle, body: issueBody, delay: 11 * time.Second}
		})
		n := g.mark()
		start := time.Now()
		missing, prompt := gpBrief(t, e, pr)
		if took := time.Since(start); took > 10900*time.Millisecond {
			t.Errorf("the brief waited %v for the issue, want it to give up after 10 s", took)
		}
		if reqs := issueReqs(n); len(reqs) != 1 {
			t.Errorf("the slow read sent %v, want one request", reqs)
		}
		if !slices.Contains(missing, "linked_issue/fetch_failed") || strings.Contains(prompt, issueTitle) {
			t.Errorf("after the timeout: missing_inputs = %v, prompt has the issue = %v, want linked_issue/fetch_failed and no issue", missing, strings.Contains(prompt, issueTitle))
		}
	})
	t.Run("no token sends no request", func(t *testing.T) {
		g.set(func(g *gpStub) { g.issues[7] = gpIssue{status: http.StatusOK, title: issueTitle, body: issueBody} })
		gpDropToken(t, e, connID)
		n := g.mark()
		missing, _ := gpBrief(t, e, pr)
		if !slices.Contains(missing, "linked_issue/fetch_failed") {
			t.Errorf("without a token: missing_inputs = %v, want linked_issue/fetch_failed", missing)
		}
		if reqs := g.since(n); len(reqs) != 0 {
			t.Errorf("the brief without a token sent %v, want no request", reqs)
		}
	})
	t.Run("not connected sends no request", func(t *testing.T) {
		gpDeleteConnection(t, e, connID)
		n := g.mark()
		missing, _ := gpBrief(t, e, pr)
		if !slices.Contains(missing, "linked_issue/fetch_failed") {
			t.Errorf("without a connection: missing_inputs = %v, want linked_issue/fetch_failed", missing)
		}
		if reqs := g.since(n); len(reqs) != 0 {
			t.Errorf("the brief without a connection sent %v, want no request", reqs)
		}
	})

	if rows := count(t, e.s.db.DB, `SELECT count(*) FROM pr_brief WHERE pr_id = ?`, pr); rows != 1 {
		t.Errorf("pr_brief rows = %d, want the one stored brief", rows)
	}
	gpNoGitHub(t, e)
}

// ---- intent sources ----

type gpSource struct {
	Kind, Ref, Status string
	Detail            *string
}

// gpIntentSources refreshes the intent and returns its sources of kind (the title and body are not
// listed), in the order of the answer.
func gpIntentSources(t *testing.T, e *giEnv, pr, kind string) []gpSource {
	t.Helper()
	resp := e.s.post("/pulls/"+pr+"/intent/refresh", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /pulls/%s/intent/refresh = %d %s, want 200", pr, resp.Status, resp.Body)
	}
	var body struct {
		Intent struct {
			Sources []struct {
				Kind   string  `json:"kind"`
				Ref    string  `json:"ref"`
				Status string  `json:"status"`
				Detail *string `json:"detail"`
			} `json:"sources"`
		} `json:"intent"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("intent body %s: %v", resp.Body, err)
	}
	var out []gpSource
	for _, s := range body.Intent.Sources {
		if s.Kind == kind {
			out = append(out, gpSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail})
		}
	}
	return out
}

func gpStr(s string) *string { return &s }

// ---- AC24: intent tickets ----

// AC24 (FR26, FR27, EC20, EC21): tickets of an MR text in the position order of SCR3 (#9, #7, #8 with
// the cap of three) are read one at a time from GET /projects/7/issues/<n> with the token, become
// used, truncated or failed "404 Not found"; references of other projects and hosts and Jira keys
// are skipped with their reasons; no token and no connection fail the tickets with no request.
func TestGitLabPeers_AC24_IntentTickets(t *testing.T) {
	body := func(host string) string {
		return fmt.Sprintf("Plan: web#9 first, then https://%[1]s/acme/web/-/issues/7 and acme/web#8.\n"+
			"Also other#3, acme/other#4, https://gitlab.com/acme/web/-/issues/5, https://%[1]s/acme/web/-/work_items/10, #11, !12, JIRA-1.", host)
	}
	e, g, connID, pr := gpBoot(t, gpOpts{description: body})
	host := e.gl.hostKey()

	g.set(func(g *gpStub) {
		g.issues[9] = gpIssue{status: http.StatusOK, title: "Nine", body: strings.Repeat("n", 3500)}
		g.issues[7] = gpIssue{status: http.StatusOK, title: "Seven", body: "Seven's details."}
		g.issues[8] = gpIssue{status: http.StatusNotFound}
	})

	skipped := []gpSource{
		{"ticket", "other#3", "skipped", gpStr("issue reference from a different project (other)")},
		{"ticket", "acme/other#4", "skipped", gpStr("issue reference from a different project (acme/other)")},
		{"ticket", "https://gitlab.com/acme/web/-/issues/5", "skipped", gpStr("issue reference from another host (gitlab.com)")},
		{"ticket", "JIRA-1", "skipped", gpStr("Jira/Linear-style keys are not supported (server/specs/05-intent-layer.md)")},
		{"ticket", "#10", "skipped", gpStr("ticket limit reached (max 3)")},
		{"ticket", "#11", "skipped", gpStr("ticket limit reached (max 3)")},
	}

	t.Run("read in text order, one at a time", func(t *testing.T) {
		n := g.mark()
		got := gpIntentSources(t, e, pr, "ticket")
		want := append([]gpSource{
			{"ticket", "9", "truncated", nil},
			{"ticket", "7", "used", nil},
			{"ticket", "8", "failed", gpStr("404 Not found")},
		}, skipped...)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("intent ticket sources mismatch (-want +got):\n%s", diff)
		}
		reqs := g.since(n)
		wantReqs := []gpReq{
			{Method: http.MethodGet, Path: "/api/v4/projects/7/issues/9", Private: giToken},
			{Method: http.MethodGet, Path: "/api/v4/projects/7/issues/7", Private: giToken},
			{Method: http.MethodGet, Path: "/api/v4/projects/7/issues/8", Private: giToken},
		}
		if diff := cmp.Diff(wantReqs, reqs); diff != "" {
			t.Errorf("the refresh sent (-want +got):\n%s", diff)
		}
		if got := g.maxIn("issues"); got != 1 {
			t.Errorf("%d issue reads were in flight at once, want one at a time", got)
		}
	})

	t.Run("no token fails the tickets with no request", func(t *testing.T) {
		gpDropToken(t, e, connID)
		n := g.mark()
		got := gpIntentSources(t, e, pr, "ticket")
		noToken := gpStr("GitLab connection Local has no token")
		want := append([]gpSource{
			{"ticket", "9", "failed", noToken},
			{"ticket", "7", "failed", noToken},
			{"ticket", "8", "failed", noToken},
		}, skipped...)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("intent ticket sources without a token mismatch (-want +got):\n%s", diff)
		}
		if reqs := g.since(n); len(reqs) != 0 {
			t.Errorf("the refresh without a token sent %v, want no request", reqs)
		}
	})

	t.Run("not connected fails the tickets with no request", func(t *testing.T) {
		gpDeleteConnection(t, e, connID)
		n := g.mark()
		got := gpIntentSources(t, e, pr, "ticket")
		none := gpStr("No GitLab connection for " + host)
		want := append([]gpSource{
			{"ticket", "9", "failed", none},
			{"ticket", "7", "failed", none},
			{"ticket", "8", "failed", none},
		}, skipped...)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("intent ticket sources without a connection mismatch (-want +got):\n%s", diff)
		}
		if reqs := g.since(n); len(reqs) != 0 {
			t.Errorf("the refresh without a connection sent %v, want no request", reqs)
		}
	})

	gpNoGitHub(t, e)
}

// ---- AC25: intent docs ----

// AC25 (FR28, FR29, EC22): docs missing from the clone are read through GET /projects/7/repository/
// files/<URL-encoded path>/raw?ref=<head sha>; the same-project blob and tree URLs are read (a
// %20 is decoded), a 70,000-byte file only on GitLab and a 404 are "not found", another project's
// blob URL and an https://example.com link are skipped, a 70,000-byte file of the clone fails with
// its size and makes no API call; no token and no connection read "not found" with no request.
func TestGitLabPeers_AC25_IntentDocs(t *testing.T) {
	const bigText = 70_000
	e, g, connID, pr := gpBoot(t, gpOpts{files: map[string]string{"docs/clone-big.md": strings.Repeat("c", bigText)},
		description: func(host string) string {
			return fmt.Sprintf("Design: [notes](https://%[1]s/acme/web/-/blob/main/docs/gitlab-only.md) and "+
				"[mine](https://%[1]s/acme/web/-/blob/main/docs/my%%20notes.md).\n"+
				"Also https://%[1]s/acme/web/-/tree/feature/docs/huge-remote.md and https://%[1]s/acme/web/-/blob/main/docs/missing.md, "+
				"[other](https://%[1]s/acme/other/-/blob/main/docs/other.md), [ext](https://example.com/a.md) and docs/clone-big.md.", host)
		}})
	host := e.gl.hostKey()
	g.set(func(g *gpStub) {
		g.raw["docs/gitlab-only.md"] = gpFile{status: http.StatusOK, body: "# Only on GitLab\n\nThe limiter is a token bucket.\n"}
		g.raw["docs/my notes.md"] = gpFile{status: http.StatusOK, body: "# Notes\n\nA file whose name holds a space.\n"}
		g.raw["docs/huge-remote.md"] = gpFile{status: http.StatusOK, body: strings.Repeat("h", bigText)}
	})
	sha := g.headSHA

	tooLarge := gpStr(fmt.Sprintf("File 'docs/clone-big.md' at %s is too large (%d bytes > 65536)", sha, bigText))
	notFound := gpStr("not found")
	skipped := []gpSource{
		{"doc", "https://" + host + "/acme/other/-/blob/main/docs/other.md", "skipped", gpStr("doc link points outside this project (acme/other)")},
		{"doc", "https://example.com/a.md", "skipped", gpStr("not a blob or tree URL of this GitLab project (no arbitrary URL fetch)")},
	}
	// bySource keys the answer by ref: the order of the docs is Phase 5's, only their results matter here.
	bySource := func(got []gpSource) map[string]gpSource {
		out := map[string]gpSource{}
		for _, s := range got {
			out[s.Ref] = s
		}
		return out
	}

	t.Run("read through the files API at the head sha", func(t *testing.T) {
		n := g.mark()
		got := bySource(gpIntentSources(t, e, pr, "doc"))
		want := map[string]gpSource{
			"docs/gitlab-only.md": {"doc", "docs/gitlab-only.md", "used", nil},
			"docs/my notes.md":    {"doc", "docs/my notes.md", "used", nil},
			"docs/huge-remote.md": {"doc", "docs/huge-remote.md", "failed", notFound},
			"docs/missing.md":     {"doc", "docs/missing.md", "failed", notFound},
			"docs/clone-big.md":   {"doc", "docs/clone-big.md", "failed", tooLarge},
		}
		for _, s := range skipped {
			want[s.Ref] = s
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("intent doc sources mismatch (-want +got):\n%s", diff)
		}

		reqs := g.since(n)
		gpCheckAuth(t, reqs)
		var paths []string
		for _, r := range reqs {
			if r.Method != http.MethodGet || !strings.HasPrefix(r.Path, "/api/v4/projects/7/repository/files/") {
				t.Errorf("the refresh sent %s, want only files reads", r)
				continue
			}
			paths = append(paths, r.Path)
			if diff := cmp.Diff(url.Values{"ref": {sha}}, gpQuery(t, r.Query)); diff != "" {
				t.Errorf("%s query mismatch (-want +got):\n%s", r, diff)
			}
		}
		slices.Sort(paths)
		wantPaths := []string{
			"/api/v4/projects/7/repository/files/docs%2Fgitlab-only.md/raw",
			"/api/v4/projects/7/repository/files/docs%2Fhuge-remote.md/raw",
			"/api/v4/projects/7/repository/files/docs%2Fmissing.md/raw",
			"/api/v4/projects/7/repository/files/docs%2Fmy%20notes.md/raw",
		}
		if diff := cmp.Diff(wantPaths, paths); diff != "" {
			t.Errorf("files requests mismatch, want none for the clone's docs/clone-big.md (-want +got):\n%s", diff)
		}
	})

	failedAll := func(got map[string]gpSource) map[string]gpSource {
		want := map[string]gpSource{"docs/clone-big.md": {"doc", "docs/clone-big.md", "failed", tooLarge}}
		for _, p := range []string{"docs/gitlab-only.md", "docs/my notes.md", "docs/huge-remote.md", "docs/missing.md"} {
			want[p] = gpSource{"doc", p, "failed", notFound}
		}
		for _, s := range skipped {
			want[s.Ref] = s
		}
		return want
	}

	t.Run("no token reads not found with no request", func(t *testing.T) {
		gpDropToken(t, e, connID)
		n := g.mark()
		got := bySource(gpIntentSources(t, e, pr, "doc"))
		if diff := cmp.Diff(failedAll(got), got); diff != "" {
			t.Errorf("intent doc sources without a token mismatch (-want +got):\n%s", diff)
		}
		if reqs := g.since(n); len(reqs) != 0 {
			t.Errorf("the refresh without a token sent %v, want no request", reqs)
		}
	})

	t.Run("not connected reads not found with no request", func(t *testing.T) {
		gpDeleteConnection(t, e, connID)
		n := g.mark()
		got := bySource(gpIntentSources(t, e, pr, "doc"))
		if diff := cmp.Diff(failedAll(got), got); diff != "" {
			t.Errorf("intent doc sources without a connection mismatch (-want +got):\n%s", diff)
		}
		if reqs := g.since(n); len(reqs) != 0 {
			t.Errorf("the refresh without a connection sent %v, want no request", reqs)
		}
	})

	gpNoGitHub(t, e)
}
