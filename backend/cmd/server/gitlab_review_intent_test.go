package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
)

// GitLab B fix r3 (S25 BUG-1): the intent a review derives as pre-work (REVIEW_INTENT_ENABLED) must
// know the repository's stored web_url, or every same-project doc and issue URL of a GitLab merge
// request reads "outside this project". Driven through POST /pulls/:id/review and GET
// /pulls/:id/intent against the TLS GitLab stub of the peers tests. Helpers are prefixed gv.

// gvBoot starts the peers stack (gpBoot) with the review intent hook switched on.
func gvBoot(t *testing.T, o gpOpts) (e *giEnv, g *gpStub, pr string) {
	t.Helper()
	files := maps.Clone(tourFixture)
	maps.Copy(files, o.files)
	e = giBoot(t, giOpts{files: files, spec: func(sp *spec) {
		prev := sp.config
		sp.config = func(c *config.Config) {
			if prev != nil {
				prev(c)
			}
			c.ReviewIntentEnabled = true
		}
	}})
	g = &gpStub{e: e, files: map[int][]string{3: {"a.ts", "src/b.ts", "c.ts"}},
		diffStatus: map[int]int{}, issues: map[int]gpIssue{}, raw: map[string]gpFile{},
		inFlight: map[string]int{}, maxInFlight: map[string]int{}}
	g.description = o.description(e.gl.hostKey())
	g.headSHA = gitRun(t, filepath.Join(e.gl.gitRoot, "acme", "web.git"), "rev-parse", "main")
	e.gl.setHook(g.handle)
	e.connect(giToken)
	pr = e.pullID(e.addRepo(giPath))
	if resp := e.s.get("/pulls/" + pr); resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/%s = %d %s, want 200", pr, resp.Status, resp.Body)
	}
	return e, g, pr
}

type gvIntent struct {
	Stale  bool
	Doc    map[string]gpSource // doc sources by ref
	Ticket map[string]gpSource // ticket sources by ref
}

// gvGetIntent reads GET /pulls/:id/intent and indexes the doc and ticket sources by ref.
func gvGetIntent(t *testing.T, e *giEnv, pr string) gvIntent {
	t.Helper()
	resp := e.s.get("/pulls/" + pr + "/intent")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/%s/intent = %d %s, want 200", pr, resp.Status, resp.Body)
	}
	var body struct {
		Intent *struct {
			Sources []struct {
				Kind   string  `json:"kind"`
				Ref    string  `json:"ref"`
				Status string  `json:"status"`
				Detail *string `json:"detail"`
			} `json:"sources"`
		} `json:"intent"`
		Stale bool `json:"stale"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("intent body %s: %v", resp.Body, err)
	}
	if body.Intent == nil {
		t.Fatalf("GET /pulls/%s/intent = %s, want the intent the review derived", pr, resp.Body)
	}
	out := gvIntent{Stale: body.Stale, Doc: map[string]gpSource{}, Ticket: map[string]gpSource{}}
	for _, s := range body.Intent.Sources {
		src := gpSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail}
		switch s.Kind {
		case "doc":
			out.Doc[s.Ref] = src
		case "ticket":
			out.Ticket[s.Ref] = src
		}
	}
	return out
}

// The review's own derivation reads same-project doc links (a /-/blob/ URL) and issue URLs
// (/-/issues/N) as used, skips other projects' links, and reads the doc at the head sha; the stored
// derivation is the one a later GET reports (not stale) and equals what an explicit refresh derives.
func TestGitLabReviewIntent_SameProjectLinksAreUsedByTheReviewHook(t *testing.T) {
	e, g, pr := gvBoot(t, gpOpts{description: func(host string) string {
		return fmt.Sprintf("Closes https://%[1]s/acme/web/-/issues/7 and also acme/other#4.\n"+
			"Design: https://%[1]s/acme/web/-/blob/main/docs/design.md and "+
			"https://%[1]s/acme/other/-/blob/main/docs/other.md\n", host)
	}})
	host := e.gl.hostKey()
	g.set(func(g *gpStub) {
		g.issues[7] = gpIssue{status: http.StatusOK, title: "Seven", body: "Seven's details."}
		g.raw["docs/design.md"] = gpFile{status: http.StatusOK, body: "# Design\n\nA token bucket.\n"}
	})

	n := g.mark()
	reviewDone(t, e.s, pr, agentByName(t, e.s, agentGeneral))
	reviewReqs := g.since(n)

	got := gvGetIntent(t, e, pr)
	if got.Stale {
		t.Error("GET intent after the review: stale = true, want false")
	}
	wantDoc := map[string]gpSource{
		"docs/design.md": {"doc", "docs/design.md", "used", nil},
		"https://" + host + "/acme/other/-/blob/main/docs/other.md": {"doc", "https://" + host + "/acme/other/-/blob/main/docs/other.md",
			"skipped", gpStr("doc link points outside this project (acme/other)")},
	}
	if diff := cmp.Diff(wantDoc, got.Doc); diff != "" {
		t.Errorf("doc sources after the review mismatch (-want +got):\n%s", diff)
	}
	if s := got.Ticket["7"]; s.Status != "used" {
		t.Errorf("ticket 7 (a same-project /-/issues/7 URL) after the review = %+v, want used", s)
	}
	if s, ok := got.Ticket["acme/other#4"]; ok && s.Status != "skipped" {
		t.Errorf("ticket acme/other#4 after the review = %+v, want skipped", s)
	}

	// what GitLab saw during the review: the raw file at the head sha and issue 7, nothing of other.
	var files, issues []string
	for _, r := range reviewReqs {
		gpCheckAuth(t, []gpReq{r})
		switch {
		case strings.Contains(r.Path, "/repository/files/"):
			files = append(files, r.Path)
			if diff := cmp.Diff(url.Values{"ref": {g.headSHA}}, gpQuery(t, r.Query)); diff != "" {
				t.Errorf("%s query mismatch (-want +got):\n%s", r, diff)
			}
		case strings.Contains(r.Path, "/issues/"):
			issues = append(issues, r.Path)
		}
	}
	if diff := cmp.Diff([]string{"/api/v4/projects/7/repository/files/docs%2Fdesign.md/raw"}, files); diff != "" {
		t.Errorf("files read during the review mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"/api/v4/projects/7/issues/7"}, issues); diff != "" {
		t.Errorf("issues read during the review mismatch (-want +got):\n%s", diff)
	}

	// an explicit refresh derives the same sources: the hook and the route agree.
	m := g.mark()
	refreshed := gpIntentSources(t, e, pr, "doc")
	byRef := map[string]gpSource{}
	for _, s := range refreshed {
		byRef[s.Ref] = s
	}
	if diff := cmp.Diff(wantDoc, byRef); diff != "" {
		t.Errorf("doc sources after the refresh mismatch (-want +got):\n%s", diff)
	}
	if reqs := g.since(m); !slices.ContainsFunc(reqs, func(r gpReq) bool { return strings.Contains(r.Path, "design.md") }) {
		t.Errorf("the refresh sent %v, want a read of docs/design.md", reqs)
	}
	gpNoGitHub(t, e)
}
