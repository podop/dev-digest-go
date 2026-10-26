package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"
)

// Component tests of the background poll of GitLab repositories (GitLab A, plan S35, spec AC18-AC21,
// AC38, AC40) through the real server: the poller with its injected clock, the connection API, a TLS
// stub GitLab per host and the GitHub stub. The harness is gsHarness (gitlab_sync_test.go).

const (
	gsWarnPaused      = "background poll paused: rate limit reached"
	gsWarnNoToken     = "background poll skipped: the GitLab connection has no token"
	gsWarnNoConn      = "background poll skipped: no GitLab connection for the host"
	gsWarnNoGitHub    = "background poll skipped: no GitHub token is configured"
	gsWarnBatchFailed = "background poll: diff-stat backfill of a merge request batch failed"
)

// gsServeGitHub programs the GitHub stub; its requests count in the shared in-flight gauge.
func (h *gsHarness) gsServeGitHub(fn func(w http.ResponseWriter, r *http.Request)) {
	h.s.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		h.gauge.enter()
		defer h.gauge.leave()
		time.Sleep(3 * time.Millisecond)
		fn(w, r)
		return true
	})
}

// githubHealthy answers every GitHub repository with one open PR #1, updated an hour ago, and its stats.
func (h *gsHarness) githubHealthy(w http.ResponseWriter, r *http.Request) {
	_, number, _ := route(r)
	updated := h.clock.Now().Add(-time.Hour)
	if number > 0 {
		writeStats(w, number, updated)
		return
	}
	writePulls(w, ghPull{number: 1, updated: updated})
}

// githubRepo inserts the GitHub repository o/<name> and returns its id.
func (h *gsHarness) githubRepo(tx *gorm.DB, name string) string {
	h.t.Helper()
	h.nRepo++
	id := lbID(idRepo, h.nRepo)
	(&lbFixture{t: h.t, db: tx}).repo(id, h.ws, "o", name, nil)
	return id
}

// githubLists counts the GitHub list requests of o/<name>.
func (h *gsHarness) githubLists(name string) int {
	n := 0
	for _, r := range h.s.gh.requests() {
		if m := pullPath.FindStringSubmatch(r.Path); r.Method == http.MethodGet && m != nil && m[1] == name && m[2] == "" {
			n++
		}
	}
	return n
}

// tx runs fn in one transaction, so the poller sees all its repositories together.
func (h *gsHarness) tx(fn func(tx *gorm.DB)) {
	h.t.Helper()
	if err := h.db.Transaction(func(tx *gorm.DB) error { fn(tx); return nil }); err != nil {
		h.t.Fatalf("transaction: %v", err)
	}
}

func (h *gsHarness) polledAt(repoID string) sql.NullTime {
	h.t.Helper()
	return scalar[sql.NullTime](h.t, h.db, `SELECT last_polled_at FROM repos WHERE id = ?`, repoID)
}

// waitPolled waits until the repository's last_polled_at is at.
func (h *gsHarness) waitPolled(repoID string, at time.Time) {
	h.t.Helper()
	eventually(h.t, 30*time.Second, "last_polled_at = "+at.Format(time.RFC3339), func() bool {
		v := h.polledAt(repoID)
		return v.Valid && v.Time.Equal(at)
	})
}

// idle gives the poller time to (wrongly) act: a bounded wait, because "nothing happened" has no event.
func (h *gsHarness) idle() { time.Sleep(15 * pollTick) }

func (h *gsHarness) lines(msg string) []map[string]any { return logLines(h.t, h.s.logs, msg) }

func (h *gsHarness) waitLines(msg string, n int) {
	h.t.Helper()
	eventually(h.t, 30*time.Second, fmt.Sprintf("%d log line(s) %q", n, msg), func() bool { return len(h.lines(msg)) >= n })
}

// gsMRs builds n open MRs, iid 1..n, MR i updated i hours before the harness clock started.
func gsMRs(h *gsHarness, project int64, n int) []gsMR {
	out := make([]gsMR, n)
	for i := range out {
		iid := i + 1
		out[i] = gsMR{
			iid: iid, title: fmt.Sprintf("MR %d", iid), author: "ada", state: "opened", sha: fmt.Sprintf("sha%d", iid),
			source: project, target: project, created: h.base.Add(-100 * time.Hour), updated: h.base.Add(-time.Duration(iid) * time.Hour),
		}
	}
	return out
}

// ---- AC18 ----

type gsGraphQLBody struct {
	Query     string `json:"query"`
	Variables struct {
		IDs  []string `json:"ids"`
		IIDs []string `json:"iids"`
	} `json:"variables"`
}

func gsGraphQLRequests(t *testing.T, lab *gsLab) []gsGraphQLBody {
	t.Helper()
	var out []gsGraphQLBody
	for _, r := range lab.requestsMatching("POST /api/graphql") {
		var b gsGraphQLBody
		if err := json.Unmarshal(r.Body, &b); err != nil {
			t.Fatalf("GraphQL request body is not JSON: %v\n%s", err, r.Body)
		}
		out = append(out, b)
	}
	return out
}

// gsWithStats lists the numbers of the stored MRs that have stats.
func (h *gsHarness) withStats(repoID string) []int {
	h.t.Helper()
	var numbers []int
	err := h.db.Raw(`SELECT number FROM pull_requests WHERE repo_id = ? AND NOT (additions = 0 AND deletions = 0 AND files_count = 0) ORDER BY number`, repoID).Scan(&numbers).Error
	if err != nil {
		h.t.Fatalf("read stored stats: %v", err)
	}
	return numbers
}

func gsSeq(from, to int) []int {
	var out []int
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}

// AC18 (FR26, EC19, NFR3, NFR4): the stats of the 20 selected MRs come from one read-only GraphQL request
// in at most 13 requests; a GraphQL error leaves the stats unset with one log line, no diff request, and
// the next cycle retries.
func TestGsPoll_AC18_StatsFromOneGraphQLRequest(t *testing.T) {
	setup := func(t *testing.T) (*gsHarness, *gsLab, string) {
		h := gsStart(t, gsOpts{labs: 1, poll: true})
		lab := h.labs[0]
		h.connect(lab, "Cloud", gsToken)
		lab.setProject(gsProject{id: 42, path: "acme/payments", defaultBranch: "main"})
		lab.setMRs(42, gsMRs(h, 42, 25)...)
		stats := map[int]gsStat{}
		for i := 1; i <= 25; i++ {
			stats[i] = gsStat{add: i, del: 1, files: 2}
		}
		lab.setStats(42, stats)
		return h, lab, ""
	}

	t.Run("one GraphQL request fills the 20 newest", func(t *testing.T) {
		h, lab, _ := setup(t)
		repo := h.addRepo(lab, 42, "acme/payments")
		h.waitPolled(repo, h.base)
		eventually(t, 30*time.Second, "the stats of 20 MRs", func() bool { return len(h.withStats(repo)) == 20 })
		h.idle()

		if diff := cmp.Diff(gsSeq(1, 20), h.withStats(repo)); diff != "" {
			t.Errorf("MRs with stats (the 20 most recently updated) mismatch (-want +got):\n%s", diff)
		}
		if n := len(h.prs(repo)); n != 25 {
			t.Errorf("stored MRs = %d, want 25", n)
		}
		reqs := gsGraphQLRequests(t, lab)
		if len(reqs) != 1 {
			t.Fatalf("GraphQL requests in the cycle = %d, want 1", len(reqs))
		}
		var iids []string
		for _, n := range gsSeq(1, 20) {
			iids = append(iids, fmt.Sprint(n))
		}
		if diff := cmp.Diff(iids, reqs[0].Variables.IIDs); diff != "" {
			t.Errorf("iids of the GraphQL request mismatch (-want +got):\n%s", diff)
		}
		if got := lab.requests(); len(got) > 13 {
			t.Errorf("GitLab requests for the repository in one cycle = %d (%v), want at most 13", len(got), gsStrings(got))
		}
		for _, r := range lab.requests() {
			if strings.Contains(strings.ToLower(string(r.Body)), "mutation") {
				t.Errorf("%s sent a body with a mutation: %s", r, r.Body)
			}
		}
		if n := len(lab.requestsMatching("GET /api/v4/projects/42/merge_requests/")); n != 0 {
			t.Errorf("per-MR requests (diff pages for stats) = %d, want none", n)
		}
	})

	t.Run("a GraphQL error leaves the stats unset and the next cycle retries", func(t *testing.T) {
		h, lab, _ := setup(t)
		lab.setGraphQLError(true)
		repo := h.addRepo(lab, 42, "acme/payments")
		h.waitPolled(repo, h.base)
		h.waitLines(gsWarnBatchFailed, 1)
		h.idle()

		if got := h.withStats(repo); len(got) != 0 {
			t.Errorf("MRs with stats after a failed GraphQL query = %v, want none", got)
		}
		if lines := h.lines(gsWarnBatchFailed); len(lines) != 1 || lines[0]["level"] != "WARN" {
			t.Errorf("batch failure log lines = %v, want exactly one WARN", lines)
		}
		if n := len(lab.requestsMatching("GET /api/v4/projects/42/merge_requests/")); n != 0 {
			t.Errorf("per-MR requests after a GraphQL error = %d, want none (no diff page for stats)", n)
		}
		if n := len(gsGraphQLRequests(t, lab)); n != 1 {
			t.Errorf("GraphQL requests in the failed cycle = %d, want 1", n)
		}

		lab.setGraphQLError(false)
		h.clock.Advance(5 * time.Minute)
		h.waitPolled(repo, h.clock.Now())
		eventually(t, 30*time.Second, "the stats of the retried cycle", func() bool { return len(h.withStats(repo)) == 20 })
		if n := len(gsGraphQLRequests(t, lab)); n != 2 {
			t.Errorf("GraphQL requests after the retry = %d, want 2", n)
		}
	})
}

// ---- AC19 ----

// AC19 (FR27, EC16, NFR4): the rate limit of one host pauses that host only, a connection without a
// token or a missing GitHub token skips only the repositories that need it, requests never overlap and
// shutdown with a poll in flight completes within 5 s.
func TestGsPoll_AC19_PerHostPauseAndTokens(t *testing.T) {
	// two hosts: labs[0] plays gitlab.com, labs[1] plays git.corp.example.
	start := func(t *testing.T, githubToken bool) (h *gsHarness, ids struct{ gh, a, b string }) {
		h = gsStart(t, gsOpts{labs: 2, poll: true, githubToken: githubToken})
		h.setInterval(1)
		h.gsServeGitHub(h.githubHealthy)
		return h, ids
	}
	lab := func(h *gsHarness, i int, project int64, path string) {
		h.labs[i].setProject(gsProject{id: project, path: path, defaultBranch: "main"})
		h.labs[i].setMRs(project, gsMRs(h, project, 1)...)
	}

	t.Run("a rate-limited host is paused alone", func(t *testing.T) {
		h, ids := start(t, true)
		a, b := h.labs[0], h.labs[1]
		h.connect(a, "Cloud", gsToken)
		h.connect(b, "Corp", gsToken)
		lab(h, 0, 1, "acme/a")
		lab(h, 1, 1, "corp/b")
		var limited sync.Map
		limited.Store("on", true)
		b.setHook(func(w http.ResponseWriter, r *http.Request) bool {
			if on, _ := limited.Load("on"); on == true {
				gsError(w, http.StatusTooManyRequests, "Retry later", "Retry-After", "120")
				return true
			}
			return false
		})
		h.tx(func(tx *gorm.DB) {
			ids.gh = h.githubRepo(tx, "gh")
			ids.a = h.repo(tx, a.host, a.URL, 1, "acme/a")
			ids.b = h.repo(tx, b.host, b.URL, 1, "corp/b")
		})

		h.waitPolled(ids.a, h.base)
		h.waitPolled(ids.gh, h.base)
		h.waitLines(gsWarnPaused, 1)
		if lines := h.lines(gsWarnPaused); len(lines) != 1 || lines[0]["host"] != b.host || lines[0]["provider"] != "gitlab" {
			t.Errorf("pause log lines = %v, want one for the GitLab host %s", lines, b.host)
		}
		if n := len(b.requests()); n != 1 {
			t.Errorf("requests of the limited host in its cycle = %d (%v), want 1 (no retry)", n, gsStrings(b.requests()))
		}
		if v := h.polledAt(ids.b); v.Valid {
			t.Errorf("last_polled_at of the limited host's repository = %v, want NULL", v)
		}

		// 60 s later the other hosts are polled again, the limited one (Retry-After 120 s) is not.
		limited.Store("on", false)
		h.clock.Advance(time.Minute)
		h.waitPolled(ids.a, h.clock.Now())
		h.waitPolled(ids.gh, h.clock.Now())
		h.idle()
		if n := len(b.requests()); n != 1 {
			t.Errorf("requests of the limited host 60 s into a 120 s Retry-After = %d, want 1", n)
		}
		if n := h.githubLists("gh"); n != 2 {
			t.Errorf("GitHub list requests after two cycles = %d, want 2", n)
		}
		if n := len(a.requestsMatching("GET /api/v4/projects/1/merge_requests")); n != 2 {
			t.Errorf("list requests of the healthy GitLab host after two cycles = %d, want 2", n)
		}

		// Past the Retry-After the host is polled again (the pause started at the real time of the answer).
		h.clock.Advance(2 * time.Minute)
		h.waitPolled(ids.b, h.clock.Now())
		if got := h.gauge.max.Load(); got != 1 {
			t.Errorf("most code-host requests in flight at once = %d, want 1", got)
		}
	})

	t.Run("a connection without a token is skipped with one WARN per cycle", func(t *testing.T) {
		h, ids := start(t, true)
		a := h.labs[0]
		conn := h.connect(a, "Cloud", gsToken)
		lab(h, 0, 1, "acme/a")
		h.tx(func(tx *gorm.DB) {
			ids.gh = h.githubRepo(tx, "gh")
			ids.a = h.repo(tx, a.host, a.URL, 1, "acme/a")
		})
		h.waitPolled(ids.a, h.base)
		h.waitPolled(ids.gh, h.base)

		h.removeToken(conn)
		before := len(a.requests())
		h.clock.Advance(time.Minute)
		h.waitPolled(ids.gh, h.clock.Now())
		h.waitLines(gsWarnNoToken, 1)
		h.idle()
		if lines := h.lines(gsWarnNoToken); len(lines) != 1 || lines[0]["host"] != a.host || lines[0]["connection"] != "Cloud" {
			t.Errorf("no-token log lines = %v, want one for connection Cloud on %s", lines, a.host)
		}
		if n := len(a.requests()) - before; n != 0 {
			t.Errorf("requests to the host whose token was removed = %d, want none", n)
		}
		if v := h.polledAt(ids.a); !v.Valid || !v.Time.Equal(h.base) {
			t.Errorf("last_polled_at of the skipped repository = %v, want the earlier cycle's %v", v, h.base)
		}
		h.clock.Advance(time.Minute)
		h.waitPolled(ids.gh, h.clock.Now())
		h.waitLines(gsWarnNoToken, 2)
		h.idle()
		if got := len(h.lines(gsWarnNoToken)); got != 2 {
			t.Errorf("no-token warnings after two cycles = %d, want 2 (one per cycle)", got)
		}
	})

	t.Run("without a GitHub token only GitHub repositories are skipped", func(t *testing.T) {
		h, ids := start(t, false)
		a := h.labs[0]
		h.connect(a, "Cloud", gsToken)
		lab(h, 0, 1, "acme/a")
		h.tx(func(tx *gorm.DB) {
			ids.gh = h.githubRepo(tx, "gh")
			ids.a = h.repo(tx, a.host, a.URL, 1, "acme/a")
		})
		h.waitPolled(ids.a, h.base)
		h.waitLines(gsWarnNoGitHub, 1)
		h.idle()
		if got := len(h.lines(gsWarnNoGitHub)); got != 1 {
			t.Errorf("no-GitHub-token warnings in one cycle = %d, want 1", got)
		}
		if reqs := h.s.gh.requests(); len(reqs) != 0 {
			t.Errorf("GitHub requests without a token = %v, want none", reqs)
		}
		if v := h.polledAt(ids.gh); v.Valid {
			t.Errorf("last_polled_at of the GitHub repository = %v, want NULL", v)
		}
	})

	t.Run("shutdown completes within 5 s with a GitLab request in flight", func(t *testing.T) {
		h, ids := start(t, true)
		a := h.labs[0]
		h.connect(a, "Cloud", gsToken)
		lab(h, 0, 1, "acme/a")
		started, _ := gsHold(a, func(r *http.Request) bool { return true })
		ids.a = h.addRepo(a, 1, "acme/a")
		select {
		case <-started:
		case <-time.After(30 * time.Second):
			t.Fatal("the poller sent no request to the GitLab stub")
		}
		begin := time.Now()
		if err := h.s.stop(); err != nil {
			t.Errorf("stop with a poll in flight = %v, want nil", err)
		}
		if took := time.Since(begin); took > 5*time.Second {
			t.Errorf("the server took %s to stop with a poll blocked in a GitLab request, want at most 5s", took)
		}
		if v := h.polledAt(ids.a); v.Valid {
			t.Errorf("last_polled_at after an aborted poll = %v, want NULL", v)
		}
		if lines := h.lines("background poll of a repository failed"); len(lines) != 0 {
			t.Errorf("an aborted poll was logged as a repository failure: %v", lines)
		}
	})
}

// ---- AC20 ----

// AC20 (FR6, EC10): a token rotated while a cycle's request is in flight lets that request complete and
// the next request of the repository carries the new token.
func TestGsPoll_AC20_TokenRotatedWhileACycleRuns(t *testing.T) {
	h := gsStart(t, gsOpts{labs: 1, poll: true})
	lab := h.labs[0]
	conn := h.connect(lab, "Cloud", gsToken)
	lab.setProject(gsProject{id: 42, path: "acme/payments", defaultBranch: "main"})
	lab.setMRs(42, gsMRs(h, 42, 2)...)
	started, release := gsHold(lab, func(r *http.Request) bool { return r.URL.Path == "/api/v4/projects/42" })
	repo := h.addRepo(lab, 42, "acme/payments")

	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the poller did not read the project")
	}
	h.setToken(conn, gsToken2)
	release()
	h.waitPolled(repo, h.base)

	reqs := lab.requests()
	if len(reqs) < 2 {
		t.Fatalf("GitLab requests of the cycle = %v, want the project and the list", gsStrings(reqs))
	}
	if reqs[0].Path != "/api/v4/projects/42" || reqs[0].Token != gsToken {
		t.Errorf("the request in flight = %s with token %q, want the project request with the old token", reqs[0], reqs[0].Token)
	}
	if n := len(h.prs(repo)); n != 2 {
		t.Errorf("stored MRs after the cycle = %d, want 2 (the request in flight completed)", n)
	}
	for _, r := range reqs[1:] {
		if r.Token != gsToken2 {
			t.Errorf("%s after the rotation carried %q, want the new token", r, r.Token)
		}
	}
	before := len(reqs)
	h.clock.Advance(5 * time.Minute)
	h.waitPolled(repo, h.clock.Now())
	for _, r := range lab.requests()[before:] {
		if r.Token != gsToken2 {
			t.Errorf("%s in the next cycle carried %q, want the new token", r, r.Token)
		}
	}
	if strings.Contains(h.s.logs.String(), gsToken) || strings.Contains(h.s.logs.String(), gsToken2) {
		t.Errorf("a log line contains a GitLab token:\n%s", h.s.logs.String())
	}
}

// ---- AC21 ----

// AC21 (FR28): an armed workspace starts a review for a new GitLab MR (source auto) and the status
// state reports the missing code-host token.
func TestGsPoll_AC21_AutomaticReviews(t *testing.T) {
	t.Run("a new MR starts one automatic review", func(t *testing.T) {
		llm := mockScript()
		h := gsStart(t, gsOpts{labs: 1, poll: true, spec: func(sp *spec) {
			sp.env["OPENROUTER_API_KEY"] = arKey
			sp.llmAdapters = llm.forAll()
		}})
		ar := &arHarness{t: t, s: h.s, db: h.db, clock: h.clock, ws: h.ws, t0: h.base, llm: llm}
		lab := h.labs[0]
		h.connect(lab, "Cloud", gsToken)
		a1 := ar.agent("Gs One")
		ar.setInterval()
		ar.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}})
		ar.advance(2 * time.Hour)
		now := h.clock.Now()
		after := h.base.Add(30 * time.Minute)

		lab.setProject(gsProject{id: 42, path: "acme/payments", defaultBranch: "main"})
		lab.setMRs(42,
			gsMR{iid: 1, title: "New MR", author: "ada", state: "opened", sha: arHead(1, 1), source: 42, target: 42, created: after, updated: now.Add(-10 * time.Minute)},
			gsMR{iid: 2, title: "Opened before arming", author: "ada", state: "opened", sha: arHead(2, 1), source: 42, target: 42, created: h.base.Add(-time.Hour), updated: now.Add(-9 * time.Minute)},
			gsMR{iid: 3, title: "Draft", author: "ada", state: "opened", sha: arHead(3, 1), draft: true, source: 42, target: 42, created: after, updated: now.Add(-8 * time.Minute)},
		)
		ar.repoID = h.addRepo(lab, 42, "acme/payments")

		ar.waitRecords(1)
		ar.requireRecords(arWant(1, 1, "new_pr", "started", 1))
		if got := ar.autoRuns(); got != 1 {
			t.Errorf("automatic runs = %d, want 1 (only the new, ready MR)", got)
		}
		if got := count(t, h.db, `SELECT count(*) FROM agent_runs WHERE source <> 'auto'`); got != 0 {
			t.Errorf("runs with another source = %d, want 0", got)
		}
		ar.runsDone()
		if strings.Contains(h.s.logs.String(), gsToken) {
			t.Errorf("a log line contains the GitLab token:\n%s", h.s.logs.String())
		}
	})

	t.Run("the status state follows the code-host tokens", func(t *testing.T) {
		h := gsStart(t, gsOpts{labs: 1, poll: true})
		lab := h.labs[0]
		putAuto(t, h.s, true)

		// A GitHub-only workspace without a token.
		h.tx(func(tx *gorm.DB) { h.githubRepo(tx, "gh") })
		if got := autoStatus(t, h.s)["state"]; got != "no_github_token" {
			t.Errorf("status of a GitHub-only workspace without a token = %v, want no_github_token", got)
		}

		// A GitLab repository whose connection has no token: no_github_token is never answered then.
		conn := h.connect(lab, "Cloud", "")
		h.addRepo(lab, 42, "acme/payments")
		if got := autoStatus(t, h.s)["state"]; got != "no_code_host_token" {
			t.Errorf("status with a GitLab repository and no token anywhere = %v, want no_code_host_token", got)
		}

		// A not-connected GitLab repository has no token either.
		h.removeToken(conn)
		if got := autoStatus(t, h.s)["state"]; got != "no_code_host_token" {
			t.Errorf("status after removing the token = %v, want no_code_host_token", got)
		}

		// A token for the GitLab repository's connection is enough.
		h.setToken(conn, gsToken)
		if got := autoStatus(t, h.s)["state"]; got != "no_agents" {
			t.Errorf("status with the connection's token = %v, want no_agents (the token is no longer missing)", got)
		}
	})
}

// ---- AC38 ----

// AC38 (FR27, FR48, EC32, NFR4): not-connected repositories get no request and one WARN per cycle,
// no host is paused, and the connected ones and GitHub are polled in the same cycle.
func TestGsPoll_AC38_NotConnectedRepositoriesAreSkippedWithoutAPause(t *testing.T) {
	h := gsStart(t, gsOpts{labs: 1, poll: true, githubToken: true})
	h.setInterval(1)
	a := h.labs[0]
	h.connect(a, "Cloud", gsToken)
	h.gsServeGitHub(h.githubHealthy)
	a.setProject(gsProject{id: 7, path: "acme/a", defaultBranch: "main"})
	a.setMRs(7, gsMRs(h, 7, 1)...)
	const corp = "git.corp.example"
	var gh, glA, x, y string
	h.tx(func(tx *gorm.DB) {
		x = h.repo(tx, corp, "https://"+corp, 1, "corp/x")
		y = h.repo(tx, corp, "https://"+corp, 2, "corp/y")
		gh = h.githubRepo(tx, "gh")
		glA = h.repo(tx, a.host, a.URL, 7, "acme/a")
	})

	h.waitPolled(glA, h.base)
	h.waitPolled(gh, h.base)
	h.waitLines(gsWarnNoConn, 1)
	h.idle()
	if lines := h.lines(gsWarnNoConn); len(lines) != 1 || lines[0]["host"] != corp || lines[0]["level"] != "WARN" {
		t.Errorf("not-connected log lines = %v, want exactly one WARN for %s", lines, corp)
	}
	for _, id := range []string{x, y} {
		if v := h.polledAt(id); v.Valid {
			t.Errorf("last_polled_at of a not-connected repository = %v, want NULL", v)
		}
	}
	for _, r := range a.requests() {
		if !strings.Contains(r.Path, "/projects/7") && !(r.Path == "/api/graphql" && strings.Contains(string(r.Body), "gid://gitlab/Project/7")) {
			t.Errorf("the connected host received %s, want requests for its own project 7 only", r)
		}
	}
	if got := len(h.lines(gsWarnPaused)); got != 0 {
		t.Errorf("rate-limit pauses logged = %d, want none: a not-connected host is not paused", got)
	}

	// The next cycle skips them again with one more WARN and polls the others again: nothing was paused.
	h.clock.Advance(time.Minute)
	h.waitPolled(glA, h.clock.Now())
	h.waitPolled(gh, h.clock.Now())
	h.waitLines(gsWarnNoConn, 2)
	h.idle()
	if got := len(h.lines(gsWarnNoConn)); got != 2 {
		t.Errorf("not-connected warnings after two cycles = %d, want 2 (one per cycle)", got)
	}
	if got := len(h.lines(gsWarnPaused)); got != 0 {
		t.Errorf("rate-limit pauses logged after two cycles = %d, want none", got)
	}
}

// ---- AC40 (background) ----

// AC40 (FR47, EC20), background channel: the cycle follows a renamed project by its id; the other
// channels are in gitlab_sync_test.go.
func TestGsPoll_AC40_BackgroundCycleFollowsARenamedProject(t *testing.T) {
	h := gsStart(t, gsOpts{labs: 1, poll: true, git: true})
	f := gsRenamed(t, h)
	h.waitPolled(f.repo, h.base)
	f.check(t, true)
	gsFirstRequestsAreByID(t, f.lab)
}

// ---- AC26 (background poll) ----

// AC26 (NFR9, NFR10), background channel: every GitLab call of a poll cycle logs exactly one line with the
// operation, host, status, attempt and latency, and no line holds a token, title, description or diff text.
// The connection test, the add and the list sync are covered by TestGitLabConn_AC26_EveryCallLogsOneCleanLine.
func TestGsPoll_AC26_EveryPollCallLogsOneCleanLine(t *testing.T) {
	const (
		title = `<img src=x onerror=alert("TITLE-MARK")>`
		descr = "DESCRIPTION-MARK secret plans"
		diff  = "DIFF-MARK"
	)
	h := gsStart(t, gsOpts{labs: 1, poll: true})
	lab := h.labs[0]
	h.connect(lab, "Cloud", gsToken)
	lab.setProject(gsProject{id: 42, path: "acme/payments", defaultBranch: "main"})
	d := descr
	lab.setMRs(42, gsMR{
		iid: 3, title: title, author: "ann", state: "opened", sha: "0123456789abcdef0123456789abcdef01234567",
		source: 42, target: 42, created: h.base.Add(-2 * time.Hour), updated: h.base.Add(-time.Hour), description: &d,
	})
	lab.setDiffs(42, 3, gsDiffEntry{oldPath: "a.go", newPath: "a.go", diff: "@@ -1 +1 @@\n-" + diff + "\n+x\n"})
	lab.setStats(42, map[int]gsStat{3: {add: 4, del: 1, files: 2}})
	// A second repository whose project read fails: its failed call is logged once as well (no retry in the poll).
	lab.setProject(gsProject{id: 43, path: "acme/broken", defaultBranch: "main"})
	failing := true
	lab.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		if failing && r.URL.Path == "/api/v4/projects/43" {
			gsError(w, http.StatusInternalServerError, "boom")
			return true
		}
		return false
	})
	var ok, broken string
	h.tx(func(tx *gorm.DB) {
		ok = h.repo(tx, lab.host, lab.URL, 42, "acme/payments")
		broken = h.repo(tx, lab.host, lab.URL, 43, "acme/broken")
	})
	h.waitPolled(ok, h.base)
	eventually(t, 30*time.Second, "the stats of MR 3", func() bool { return len(h.withStats(ok)) == 1 })
	h.idle()
	if v := h.polledAt(broken); v.Valid {
		t.Errorf("last_polled_at of the failing repository = %v, want NULL", v)
	}

	var ops, failed []string
	for _, msg := range []string{"gitlab call", "gitlab call failed"} {
		for _, line := range h.lines(msg) {
			op, _ := line["op"].(string)
			ops = append(ops, op)
			if msg == "gitlab call failed" {
				failed = append(failed, op)
			}
			if line["host"] != lab.host {
				t.Errorf("%s line host = %v, want %s", op, line["host"], lab.host)
			}
			if st, ok := line["status"].(float64); !ok || st == 0 {
				t.Errorf("%s line has no status: %v", op, line)
			}
			if a, ok := line["attempt"].(float64); !ok || a != 1 {
				t.Errorf("%s line attempt = %v, want 1 (the poll does not retry)", op, line["attempt"])
			}
			if _, ok := line["latency_ms"].(float64); !ok {
				t.Errorf("%s line has no latency_ms: %v", op, line)
			}
		}
	}
	slices.Sort(ops)
	want := []string{"graphql_stats", "merge_requests", "project", "project"}
	if diff := cmp.Diff(want, ops); diff != "" {
		t.Errorf("GitLab call log lines (op) in one cycle mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"project"}, failed); diff != "" {
		t.Errorf("failed GitLab call log lines (op) mismatch (-want +got):\n%s", diff)
	}
	if n := len(lab.requests()); n != len(want) {
		t.Errorf("the stub saw %d request(s) in the cycle, want %d (one per log line): %v", n, len(want), gsStrings(lab.requests()))
	}
	for _, line := range strings.Split(h.s.logs.String(), "\n") {
		for _, secret := range []string{gsToken, title, "TITLE-MARK", descr, "DESCRIPTION-MARK", diff} {
			if strings.Contains(line, secret) {
				t.Errorf("a log line contains %q: %.300s", secret, line)
			}
		}
	}
}
