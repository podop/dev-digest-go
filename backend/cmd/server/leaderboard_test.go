package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// Component tests of GET /leaderboard (plan S19, spec AC7-AC11, AC13, EC8) through the real
// server and a real Postgres. Everything is deterministic: the clock is fixed (spec.now), every
// row that reaches the wire has a fixed id, and the PR timestamps are fixed instants, so the
// goldens in testdata/contracts/leaderboard_*.json keep their real values (they are not passed
// through the placeholder normalizer of the other contract goldens).
//
// The fixture (plan C2), "now" = Thu 2026-10-22 12:00 UTC. Week = [Mon 10-19, 10-26) UTC, the
// previous week = [10-12, 10-19). Three repositories of the default workspace, one repository of
// another workspace, the agents alpha and beta and a third agent that is deleted after its
// reviews exist ("Deleted agent"):
//
//	acme/api #1  open, re-reviewed by alpha (3x) and by the deleted agent (2x), new commits, 3 reviewers
//	acme/api #2  merged in the week, clean, a CI run
//	acme/api #3  merged in the week, reviewed only in the previous week (open critical)
//	acme/api #4  merged in the week without any review
//	acme/api #5  status merged but no merge time (counts as merged nowhere)
//	acme/api #6  draft without opened time and without diff stats, reviewed
//	acme/api #7  its only review is created exactly at the end of the week (not a row)
//	acme/api #8  reviewed 21:00 UTC, exactly at the end of the 30d window in Europe/Kyiv
//	acme/api #9  merged in September, #10 open since September (30d only)
//	acme/web #1  XL, three open/accepted criticals; #2 merged, re-reviewed with a dismissed finding
//	acme/ops #1  one open critical; #2 merged unreviewed (repo removed by TestLeaderboard_RepoRemoved)
//
// The expectations below were computed by hand from the spec rules (FR8-FR16, D20-D23), not
// taken from the output; the golden files pin the whole response.

var lbNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func oct(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
}
func sep(day, hour, minute int) time.Time {
	return time.Date(2026, 9, day, hour, minute, 0, 0, time.UTC)
}

// lbID builds a fixed uuid: kind is the table, n the row.
func lbID(kind, n int) string { return fmt.Sprintf("%08d-0000-4000-8000-%012d", kind, n) }

const (
	idWorkspace = 10
	idUser      = 11
	idRepo      = 20
	idAgent     = 30
	idPull      = 40
	idReview    = 50
	idFinding   = 60
	idRun       = 70
)

var (
	repoAPI, repoWeb, repoOps = lbID(idRepo, 1), lbID(idRepo, 2), lbID(idRepo, 3)
	repoOther                 = lbID(idRepo, 9)
)

type lbFinding struct {
	severity, state, file string // state: open | accepted | dismissed
	line                  int
	title                 string
}

type lbPull struct {
	repo                            string
	number                          int
	title, author, status           string
	head, reviewed                  string // reviewed "" = no review of any head
	opened, updated, merged, closed *time.Time
	draft                           bool
	additions, deletions, files     int
}

// lbFixture inserts the fixture rows with fixed ids.
type lbFixture struct {
	t         testing.TB
	db        *gorm.DB
	nReview   int
	nFinding  int
	nRun      int
	workspace string
}

func (f *lbFixture) exec(query string, args ...any) {
	f.t.Helper()
	if err := f.db.Exec(query, args...).Error; err != nil {
		f.t.Fatalf("fixture %q: %v", query, err)
	}
}

func (f *lbFixture) repo(id, workspace, owner, name string, polled *time.Time) {
	f.t.Helper()
	f.exec(`INSERT INTO repos (id, workspace_id, owner, name, full_name, last_polled_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, workspace, owner, name, owner+"/"+name, polled)
}

func (f *lbFixture) agent(n int, workspace, name string) string {
	f.t.Helper()
	id := lbID(idAgent, n)
	f.exec(`INSERT INTO agents (id, workspace_id, name, provider, model, system_prompt) VALUES (?, ?, ?, 'openai', 'gpt', 'p')`, id, workspace, name)
	return id
}

func (f *lbFixture) pull(workspace string, p lbPull) string {
	f.t.Helper()
	id := lbID(idPull, repoNumber(p.repo)*100+p.number)
	var reviewed *string
	if p.reviewed != "" {
		reviewed = &p.reviewed
	}
	f.exec(`INSERT INTO pull_requests (id, workspace_id, repo_id, number, title, author, branch, base, head_sha, last_reviewed_sha,
			additions, deletions, files_count, status, opened_at, updated_at, merged_at, closed_at, draft)
		VALUES (?, ?, ?, ?, ?, ?, 'feat', 'main', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspace, p.repo, p.number, p.title, p.author, p.head, reviewed,
		p.additions, p.deletions, p.files, p.status, p.opened, p.updated, p.merged, p.closed, p.draft)
	return id
}

// repoNumber is the last digit of a fixture repository id (1 api, 2 web, 3 ops, 9 other).
func repoNumber(repo string) int {
	switch repo {
	case repoAPI:
		return 1
	case repoWeb:
		return 2
	case repoOps:
		return 3
	}
	return 9
}

func (f *lbFixture) review(workspace, pr string, agent *string, at time.Time, score *int, findings ...lbFinding) {
	f.t.Helper()
	f.nReview++
	id := lbID(idReview, f.nReview)
	f.exec(`INSERT INTO reviews (id, workspace_id, pr_id, agent_id, kind, score, created_at) VALUES (?, ?, ?, ?, 'review', ?, ?)`,
		id, workspace, pr, agent, score, at)
	for _, fd := range findings {
		f.nFinding++
		var accepted, dismissed *time.Time
		switch fd.state {
		case "accepted":
			accepted = ptr(oct(20, 0, 0))
		case "dismissed":
			dismissed = ptr(oct(20, 0, 0))
		}
		f.exec(`INSERT INTO findings (id, review_id, file, start_line, end_line, severity, category, title, rationale, confidence, accepted_at, dismissed_at)
			VALUES (?, ?, ?, ?, ?, ?, 'bug', ?, 'because', 0.5, ?, ?)`,
			lbID(idFinding, f.nFinding), id, fd.file, fd.line, fd.line+1, fd.severity, fd.title, accepted, dismissed)
	}
}

func (f *lbFixture) run(workspace string, pr *string, at time.Time, status, source string, cost *string) {
	f.t.Helper()
	f.nRun++
	f.exec(`INSERT INTO agent_runs (id, workspace_id, pr_id, ran_at, status, source, cost_usd) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		lbID(idRun, f.nRun), workspace, pr, at, status, source, cost)
}

// newLeaderboardDB returns a migrated database that holds the fixture.
func newLeaderboardDB(t *testing.T) *pgtest.Database {
	t.Helper()
	db := pgtest.Migrated(t)
	f := &lbFixture{t: t, db: db.DB, workspace: lbID(idWorkspace, 1)}
	ws, other := f.workspace, lbID(idWorkspace, 2)
	f.exec(`INSERT INTO workspaces (id, name) VALUES (?, 'default'), (?, 'other')`, ws, other)
	f.exec(`INSERT INTO users (id, email, name) VALUES (?, 'you@local', 'You')`, lbID(idUser, 1))
	f.repo(repoAPI, ws, "acme", "api", ptr(oct(22, 11, 55)))
	f.repo(repoWeb, ws, "acme", "web", nil)
	f.repo(repoOps, ws, "acme", "ops", ptr(oct(22, 10, 0)))
	f.repo(repoOther, other, "zed", "x", nil)

	alpha := f.agent(1, ws, "alpha")
	beta := f.agent(2, ws, "beta")
	doomed := f.agent(3, ws, "doomed")
	gamma := f.agent(4, other, "gamma")

	// ---- acme/api
	a1 := f.pull(ws, lbPull{repo: repoAPI, number: 1, title: "Add payment retries", author: "octo", status: "open",
		head: "h1", reviewed: "h0", opened: ptr(oct(12, 10, 0)), updated: ptr(oct(21, 9, 0)), additions: 120, deletions: 30, files: 5})
	f.review(ws, a1, &alpha, oct(14, 9, 0), ptr(70), lbFinding{"WARNING", "open", "src/retry.go", 10, "Retry has no cap"})
	f.review(ws, a1, &alpha, oct(19, 9, 0), ptr(60), lbFinding{"CRITICAL", "open", "src/retry.go", 12, "Unbounded retry loop"})
	f.review(ws, a1, &beta, oct(20, 10, 0), nil,
		lbFinding{"SUGGESTION", "open", "src/config.go", 3, "Name the constant"},
		lbFinding{"WARNING", "open", "src/retry.go", 30, "Missing jitter"})
	f.review(ws, a1, &doomed, oct(20, 12, 0), ptr(40), lbFinding{"WARNING", "open", "src/queue.go", 7, "Queue not drained"})
	f.review(ws, a1, &doomed, oct(20, 14, 0), ptr(55), lbFinding{"CRITICAL", "open", "src/queue.go", 9, "Queue lock held on panic"})
	f.review(ws, a1, &alpha, oct(21, 9, 0), ptr(85),
		lbFinding{"CRITICAL", "open", "src/retry.go", 12, "Unbounded retry loop"},
		lbFinding{"CRITICAL", "accepted", "src/ledger.go", 40, "Float used for money"},
		lbFinding{"CRITICAL", "dismissed", "src/ledger.go", 41, "Rounding drift"},
		lbFinding{"WARNING", "open", "src/retry.go", 55, "Swallowed error"})
	f.run(ws, &a1, oct(19, 9, 5), "done", "local", ptr("0.250000"))
	f.run(ws, &a1, oct(20, 10, 5), "failed", "ci", nil) // unknown cost counts as 0
	f.run(ws, &a1, oct(21, 9, 5), "running", "local", ptr("1.000000"))
	f.run(ws, &a1, oct(26, 0, 0), "done", "local", ptr("9.000000"))                                              // exactly at the end of the week
	f.run(ws, &a1, time.Date(2026, 10, 18, 23, 59, 59, 999_000_000, time.UTC), "done", "local", ptr("9.000000")) // the previous week's last millisecond

	a2 := f.pull(ws, lbPull{repo: repoAPI, number: 2, title: "Fix flaky test", author: "octo", status: "merged", head: "h2", reviewed: "h2",
		opened: ptr(oct(19, 8, 0)), updated: ptr(oct(20, 8, 0)), merged: ptr(oct(20, 8, 0)), closed: ptr(oct(20, 8, 0)), additions: 30, deletions: 10, files: 2})
	f.review(ws, a2, &beta, oct(19, 12, 0), ptr(95))
	f.run(ws, &a2, oct(19, 12, 5), "done", "local", ptr("0.500000"))
	f.run(ws, &a2, oct(19, 12, 10), "done", "ci", ptr("0.100000"))

	a3 := f.pull(ws, lbPull{repo: repoAPI, number: 3, title: "Refactor ledger", author: "mona", status: "merged", head: "h3", reviewed: "h3",
		opened: ptr(oct(13, 10, 0)), updated: ptr(oct(21, 10, 0)), merged: ptr(oct(21, 10, 0)), closed: ptr(oct(21, 10, 0)), additions: 400, deletions: 100, files: 10})
	f.review(ws, a3, &alpha, oct(15, 12, 0), ptr(80), lbFinding{"CRITICAL", "open", "src/ledger.go", 5, "Float used for money"})

	f.pull(ws, lbPull{repo: repoAPI, number: 4, title: "Hotfix typo", author: "mona", status: "merged", head: "h4",
		opened: ptr(oct(20, 9, 0)), updated: ptr(oct(20, 11, 0)), merged: ptr(oct(20, 11, 0)), closed: ptr(oct(20, 11, 0))})
	f.pull(ws, lbPull{repo: repoAPI, number: 5, title: "Mystery merge", author: "octo", status: "merged", head: "h5",
		opened: ptr(oct(19, 11, 0)), updated: ptr(oct(20, 11, 0))})

	a6 := f.pull(ws, lbPull{repo: repoAPI, number: 6, title: "Add docs", author: "octo", status: "open", head: "h6", reviewed: "h6",
		updated: ptr(oct(22, 8, 0)), draft: true})
	f.review(ws, a6, &alpha, oct(22, 8, 0), ptr(80))

	a7 := f.pull(ws, lbPull{repo: repoAPI, number: 7, title: "Review exactly at the end", author: "octo", status: "open", head: "h7",
		opened: ptr(oct(18, 10, 0)), updated: ptr(oct(18, 10, 0))})
	f.review(ws, a7, &alpha, oct(26, 0, 0), ptr(50), lbFinding{"WARNING", "open", "src/end.go", 1, "Review at the boundary"})

	a8 := f.pull(ws, lbPull{repo: repoAPI, number: 8, title: "Late review", author: "octo", status: "open", head: "h8", reviewed: "h8",
		opened: ptr(oct(22, 9, 0)), updated: ptr(oct(22, 9, 0)), additions: 3, deletions: 2, files: 1})
	f.review(ws, a8, &alpha, oct(22, 21, 0), ptr(90), lbFinding{"SUGGESTION", "open", "src/docs.go", 1, "Typo"})

	a9 := f.pull(ws, lbPull{repo: repoAPI, number: 9, title: "Old merged", author: "octo", status: "merged", head: "h9", reviewed: "h9",
		opened: ptr(sep(28, 10, 0)), updated: ptr(sep(30, 10, 0)), merged: ptr(sep(30, 10, 0)), closed: ptr(sep(30, 10, 0)), additions: 60, deletions: 40, files: 3})
	f.review(ws, a9, &beta, sep(29, 12, 0), ptr(75), lbFinding{"WARNING", "open", "src/old.go", 3, "Old warning"})

	a10 := f.pull(ws, lbPull{repo: repoAPI, number: 10, title: "Long open", author: "octo", status: "open", head: "h10", reviewed: "h10",
		opened: ptr(sep(25, 9, 0)), updated: ptr(sep(25, 9, 0)), additions: 20, deletions: 5, files: 1})
	f.review(ws, a10, &alpha, sep(30, 10, 0), ptr(90))

	// ---- acme/web
	b1 := f.pull(ws, lbPull{repo: repoWeb, number: 1, title: "Dark mode", author: "mona", status: "open", head: "b1", reviewed: "b1",
		opened: ptr(oct(19, 10, 0)), updated: ptr(oct(20, 11, 0)), additions: 1200, deletions: 300, files: 15})
	f.review(ws, b1, &beta, oct(20, 11, 0), ptr(55),
		lbFinding{"CRITICAL", "open", "src/theme.ts", 10, "Hardcoded colors"},
		lbFinding{"CRITICAL", "open", "src/theme.ts", 22, "Inline style injection"},
		lbFinding{"CRITICAL", "accepted", "src/theme.ts", 30, "Unescaped CSS variable"},
		lbFinding{"WARNING", "dismissed", "src/theme.ts", 40, "Duplicate tokens"},
		lbFinding{"SUGGESTION", "open", "src/index.ts", 1, "Barrel file"})
	f.run(ws, &b1, oct(20, 11, 5), "cancelled", "local", ptr("0.750000"))

	b2 := f.pull(ws, lbPull{repo: repoWeb, number: 2, title: "Bump deps", author: "octo", status: "merged", head: "b2", reviewed: "b2",
		opened: ptr(oct(20, 8, 0)), updated: ptr(oct(21, 20, 0)), merged: ptr(oct(21, 20, 0)), closed: ptr(oct(21, 20, 0)), additions: 3, deletions: 1, files: 1})
	f.review(ws, b2, &alpha, oct(20, 9, 0), ptr(100), lbFinding{"SUGGESTION", "open", "go.mod", 1, "Pin the version"})
	f.review(ws, b2, &alpha, oct(21, 10, 0), ptr(100), lbFinding{"SUGGESTION", "dismissed", "go.mod", 1, "Pin the version"})

	// ---- acme/ops (removed in TestLeaderboard_RepoRemoved)
	c1 := f.pull(ws, lbPull{repo: repoOps, number: 1, title: "Rotate keys", author: "octo", status: "open", head: "c1", reviewed: "c1",
		opened: ptr(oct(20, 10, 0)), updated: ptr(oct(21, 10, 0)), additions: 2, deletions: 2, files: 1})
	f.review(ws, c1, &beta, oct(21, 11, 0), ptr(60), lbFinding{"CRITICAL", "open", "src/keys.go", 1, "Key committed"})
	f.run(ws, &c1, oct(21, 11, 5), "done", "local", ptr("0.200000"))
	f.pull(ws, lbPull{repo: repoOps, number: 2, title: "Cleanup", author: "octo", status: "merged", head: "c2",
		opened: ptr(oct(20, 12, 0)), updated: ptr(oct(21, 12, 0)), merged: ptr(oct(21, 12, 0)), closed: ptr(oct(21, 12, 0))})

	// ---- must never show: a run without a PR, and another workspace's PR with a review and a run
	f.run(ws, nil, oct(20, 10, 0), "done", "ci", ptr("9.000000"))
	x1 := f.pull(other, lbPull{repo: repoOther, number: 1, title: "Foreign", author: "zed", status: "open", head: "x1",
		opened: ptr(oct(20, 10, 0)), updated: ptr(oct(20, 10, 0)), additions: 5000, deletions: 0, files: 9})
	f.review(other, x1, &gamma, oct(20, 11, 0), ptr(10), lbFinding{"CRITICAL", "open", "src/x.go", 1, "Foreign critical"})
	f.run(other, &x1, oct(20, 11, 5), "done", "local", ptr("7.000000"))

	// The third agent goes away: its reviews keep existing with agent_id NULL.
	f.exec(`DELETE FROM agents WHERE id = ?`, doomed)
	return db
}

// ---- wire views (only what the assertions read) ----

type lbKpis struct {
	PRsReviewed        int     `json:"prs_reviewed"`
	Merged             int     `json:"merged"`
	AvgHealth          *int    `json:"avg_health"`
	TimeToFirstReviewS *int64  `json:"time_to_first_review_s"`
	TimeToMergeS       *int64  `json:"time_to_merge_s"`
	CriticalFound      int     `json:"critical_found"`
	OpenCritical       int     `json:"open_critical"`
	ReviewCostUSD      float64 `json:"review_cost_usd"`
}

type lbRow struct {
	Repo         string   `json:"repo"`
	Number       int      `json:"number"`
	Status       string   `json:"status"`
	Size         *string  `json:"size"`
	Health       *int     `json:"health"`
	Risk         string   `json:"risk"`
	OpenCritical int      `json:"open_critical"`
	Rounds       int      `json:"rounds"`
	TTFR         *int64   `json:"time_to_first_review_s"`
	TTM          *int64   `json:"time_to_merge_s"`
	OpenAge      *int64   `json:"open_age_s"`
	Cost         float64  `json:"cost_usd"`
	NewCommits   bool     `json:"new_commits_since_review"`
	Draft        bool     `json:"draft"`
	Reviews      []lbRev  `json:"reviews"`
	TopFindings  []lbTop  `json:"top_findings"`
	Findings     lbCounts `json:"findings"`
}

type lbCounts struct {
	Critical   int `json:"CRITICAL"`
	Warning    int `json:"WARNING"`
	Suggestion int `json:"SUGGESTION"`
}

type lbRev struct {
	AgentName string   `json:"agent_name"`
	Findings  lbCounts `json:"findings"`
}

type lbTop struct {
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	AgentName string `json:"agent_name"`
	State     string `json:"state"`
}

type lbAward struct {
	Repo   string  `json:"repo"`
	Number int     `json:"number"`
	Value  float64 `json:"value"`
}

type lbReason struct {
	Kind  string `json:"kind"`
	Count int    `json:"count,omitempty"`
	Days  int    `json:"days,omitempty"`
}

type lbAttention struct {
	Repo    string     `json:"repo"`
	Number  int        `json:"number"`
	Reasons []lbReason `json:"reasons"`
}

type lbHotFile struct {
	Repo     string `json:"repo"`
	Path     string `json:"path"`
	Findings int    `json:"findings"`
	Critical int    `json:"critical"`
	PRs      []int  `json:"prs"`
}

type lbBucket struct {
	Size        string   `json:"size"`
	PRs         int      `json:"prs"`
	AvgHealth   *int     `json:"avg_health"`
	AvgFindings *float64 `json:"avg_findings"`
}

type lbPoint struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Opened   int    `json:"opened"`
	Merged   int    `json:"merged"`
	Critical int    `json:"critical"`
}

type lbResponse struct {
	Period struct {
		Key          string  `json:"key"`
		TZ           string  `json:"tz"`
		From         string  `json:"from"`
		To           string  `json:"to"`
		PreviousFrom *string `json:"previous_from"`
		PreviousTo   *string `json:"previous_to"`
	} `json:"period"`
	Repos []struct {
		FullName     string  `json:"full_name"`
		LastPolledAt *string `json:"last_polled_at"`
	} `json:"repos"`
	Summary         lbKpis              `json:"summary"`
	PreviousSummary *lbKpis             `json:"previous_summary"`
	Awards          map[string]*lbAward `json:"awards"`
	Rows            []lbRow             `json:"rows"`
	NeedsAttention  struct {
		Total int           `json:"total"`
		Items []lbAttention `json:"items"`
	} `json:"needs_attention"`
	HotFiles     []lbHotFile `json:"hot_files"`
	HealthBySize []lbBucket  `json:"health_by_size"`
	Activity     struct {
		Granularity string    `json:"granularity"`
		Points      []lbPoint `json:"points"`
	} `json:"activity"`
}

func decodeLeaderboard(t testing.TB, resp response) lbResponse {
	t.Helper()
	var out lbResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("GET /leaderboard body is not the leaderboard shape (status %d): %v\n%s", resp.Status, err, resp.Body)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /leaderboard = %d %s, want 200", resp.Status, resp.Body)
	}
	return out
}

// checkLeaderboardGolden compares the response with testdata/contracts/<name>.json, or rewrites
// it with -update. Unlike the other contract goldens nothing is normalized: the clock and the
// data are fixed, so ids and times are the real values.
func checkLeaderboardGolden(t *testing.T, name string, resp response, wantStatus int) {
	t.Helper()
	if resp.Status != wantStatus {
		t.Fatalf("%s: status = %d, want %d; body %s", name, resp.Status, wantStatus, resp.Body)
	}
	dec := json.NewDecoder(bytes.NewReader(resp.Body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s: body is not JSON: %v", name, err)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pretty = append(pretty, '\n')

	path := filepath.Join("testdata", "contracts", name+".json")
	if *update {
		if err := os.WriteFile(path, pretty, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run go test ./cmd/server -run TestLeaderboard -update)", name, err)
	}
	if diff := cmp.Diff(string(want), string(pretty)); diff != "" {
		t.Errorf("%s: response differs from %s (-want +got):\n%s", name, path, diff)
	}
}

// lbStack starts the server over the fixture with the Host allow-list on and no GitHub token.
func lbStack(t *testing.T, now time.Time) *stack {
	t.Helper()
	return startStack(t, spec{
		db:           newLeaderboardDB(t),
		noSeed:       true,
		now:          func() time.Time { return now },
		allowedHosts: []string{"localhost", "127.0.0.1", "[::1]", "backend"},
	})
}

// assertReadOnly checks NFR2 for the requests a test made: no GitHub call, no job row.
func assertReadOnly(t *testing.T, s *stack) {
	t.Helper()
	if reqs := s.gh.requests(); len(reqs) != 0 {
		t.Errorf("GitHub requests during GET /leaderboard = %v, want none", reqs)
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM jobs`); n != 0 {
		t.Errorf("jobs after GET /leaderboard = %d, want 0", n)
	}
}

// AC8-AC11, AC13: the week over all repositories, from the fixture, against hand-computed values
// and the golden.
func TestLeaderboard_WeekAllRepos(t *testing.T) {
	s := lbStack(t, lbNow)
	resp := s.get("/leaderboard?period=week")
	checkLeaderboardGolden(t, "leaderboard_week", resp, http.StatusOK)
	got := decodeLeaderboard(t, resp)

	if got.Period.Key != "week" || got.Period.TZ != "UTC" || got.Period.From != "2026-10-19T00:00:00.000+00:00" ||
		got.Period.To != "2026-10-26T00:00:00.000+00:00" || got.Period.PreviousFrom == nil || *got.Period.PreviousFrom != "2026-10-12T00:00:00.000+00:00" ||
		got.Period.PreviousTo == nil || *got.Period.PreviousTo != "2026-10-19T00:00:00.000+00:00" {
		t.Errorf("GET /leaderboard?period=week period = %+v, want the week 10-19..10-26 UTC and the week before", got.Period)
	}
	var repos []string
	for _, r := range got.Repos {
		repos = append(repos, r.FullName)
	}
	if diff := cmp.Diff([]string{"acme/api", "acme/ops", "acme/web"}, repos); diff != "" {
		t.Errorf("GET /leaderboard?period=week repos mismatch (-want +got):\n%s", diff)
	}

	// AC9: KPIs and previous KPIs. 7 rows; merged = api#2,#3,#4, web#2, ops#2 (api#5 has no merge
	// time and counts nowhere); health (60+100+100+99+69+97+90)/7 = 87.9 -> 88.
	wantSummary := lbKpis{PRsReviewed: 7, Merged: 5, AvgHealth: ptr(88), TimeToFirstReviewS: ptr(int64(43200)), TimeToMergeS: ptr(int64(86400)),
		CriticalFound: 8, OpenCritical: 5, ReviewCostUSD: 2.8}
	if diff := cmp.Diff(wantSummary, got.Summary); diff != "" {
		t.Errorf("GET /leaderboard?period=week summary mismatch (-want +got):\n%s", diff)
	}
	wantPrevious := &lbKpis{PRsReviewed: 2, Merged: 0, AvgHealth: ptr(94), TimeToFirstReviewS: ptr(int64(174600)),
		CriticalFound: 1, OpenCritical: 1, ReviewCostUSD: 9}
	if diff := cmp.Diff(wantPrevious, got.PreviousSummary); diff != "" {
		t.Errorf("GET /leaderboard?period=week previous_summary mismatch (-want +got):\n%s", diff)
	}

	// AC8: ranked rows (health desc, then PR number, then repo).
	wantRows := []lbRow{
		{Repo: "api", Number: 2, Status: "merged", Size: ptr("XS"), Health: ptr(100), Risk: "low", Rounds: 1, TTFR: ptr(int64(14400)), TTM: ptr(int64(86400)), Cost: 0.6},
		{Repo: "api", Number: 6, Status: "reviewed", Health: ptr(100), Risk: "low", Rounds: 1, Draft: true},
		{Repo: "api", Number: 8, Status: "reviewed", Size: ptr("XS"), Health: ptr(99), Risk: "low", Rounds: 1, TTFR: ptr(int64(43200)), OpenAge: ptr(int64(10800))},
		{Repo: "web", Number: 2, Status: "merged", Size: ptr("XS"), Health: ptr(97), Risk: "low", Rounds: 2, TTFR: ptr(int64(3600)), TTM: ptr(int64(129600))},
		{Repo: "ops", Number: 1, Status: "reviewed", Size: ptr("XS"), Health: ptr(90), Risk: "medium", OpenCritical: 1, Rounds: 1, TTFR: ptr(int64(90000)), OpenAge: ptr(int64(180000)), Cost: 0.2},
		{Repo: "web", Number: 1, Status: "reviewed", Size: ptr("XL"), Health: ptr(69), Risk: "high", OpenCritical: 2, Rounds: 1, TTFR: ptr(int64(90000)), OpenAge: ptr(int64(266400)), Cost: 0.75},
		{Repo: "api", Number: 1, Status: "needs_review", Size: ptr("S"), Health: ptr(60), Risk: "high", OpenCritical: 2, Rounds: 2, TTFR: ptr(int64(169200)), OpenAge: ptr(int64(871200)), Cost: 1.25, NewCommits: true},
	}
	var gotRows []lbRow
	for _, r := range got.Rows {
		gotRows = append(gotRows, lbRow{Repo: r.Repo, Number: r.Number, Status: r.Status, Size: r.Size, Health: r.Health, Risk: r.Risk,
			OpenCritical: r.OpenCritical, Rounds: r.Rounds, TTFR: r.TTFR, TTM: r.TTM, OpenAge: r.OpenAge, Cost: r.Cost, NewCommits: r.NewCommits, Draft: r.Draft})
	}
	if diff := cmp.Diff(wantRows, gotRows); diff != "" {
		t.Errorf("GET /leaderboard?period=week rows mismatch (-want +got):\n%s", diff)
	}
	// api#1: three reviewers (the deleted agent counts as one), 4 CRITICAL / 2 WARNING / 1 SUGGESTION
	// over all statuses, top findings worst first (queue < retry by file, then the accepted one).
	api1 := got.Rows[6]
	wantReviews := []lbRev{
		{AgentName: "Deleted agent", Findings: lbCounts{Critical: 1}},
		{AgentName: "alpha", Findings: lbCounts{Critical: 3, Warning: 1}},
		{AgentName: "beta", Findings: lbCounts{Warning: 1, Suggestion: 1}},
	}
	if diff := cmp.Diff(wantReviews, api1.Reviews); diff != "" {
		t.Errorf("acme/api#1 reviews mismatch (-want +got):\n%s", diff)
	}
	if want := (lbCounts{Critical: 4, Warning: 2, Suggestion: 1}); api1.Findings != want {
		t.Errorf("acme/api#1 findings = %+v, want %+v", api1.Findings, want)
	}
	wantTop := []lbTop{
		{Severity: "CRITICAL", Title: "Queue lock held on panic", File: "src/queue.go", StartLine: 9, AgentName: "Deleted agent", State: "open"},
		{Severity: "CRITICAL", Title: "Unbounded retry loop", File: "src/retry.go", StartLine: 12, AgentName: "alpha", State: "open"},
		{Severity: "CRITICAL", Title: "Float used for money", File: "src/ledger.go", StartLine: 40, AgentName: "alpha", State: "accepted"},
	}
	if diff := cmp.Diff(wantTop, api1.TopFindings); diff != "" {
		t.Errorf("acme/api#1 top_findings mismatch (-want +got):\n%s", diff)
	}

	// AC10: awards (ties go to the lower table rank), attention, hot files.
	wantAwards := map[string]*lbAward{
		"cleanest":      {Repo: "api", Number: 2, Value: 100},
		"hottest":       {Repo: "api", Number: 1, Value: 4},
		"fastest_merge": {Repo: "api", Number: 2, Value: 86400},
		"most_rounds":   {Repo: "web", Number: 2, Value: 2},
		"monster":       {Repo: "web", Number: 1, Value: 1500},
		"priciest":      {Repo: "api", Number: 1, Value: 1.25},
	}
	if diff := cmp.Diff(wantAwards, got.Awards); diff != "" {
		t.Errorf("GET /leaderboard?period=week awards mismatch (-want +got):\n%s", diff)
	}
	wantAttention := []lbAttention{
		{Repo: "api", Number: 1, Reasons: []lbReason{{Kind: "open_critical", Count: 2}, {Kind: "new_commits"}, {Kind: "open_long", Days: 10}}},
		{Repo: "api", Number: 3, Reasons: []lbReason{{Kind: "merged_open_critical", Count: 1}}},
		{Repo: "web", Number: 1, Reasons: []lbReason{{Kind: "open_critical", Count: 2}}},
		{Repo: "ops", Number: 1, Reasons: []lbReason{{Kind: "open_critical", Count: 1}}},
	}
	if got.NeedsAttention.Total != 4 {
		t.Errorf("GET /leaderboard?period=week needs_attention.total = %d, want 4", got.NeedsAttention.Total)
	}
	if diff := cmp.Diff(wantAttention, got.NeedsAttention.Items); diff != "" {
		t.Errorf("GET /leaderboard?period=week needs_attention mismatch (-want +got):\n%s", diff)
	}
	wantHot := []lbHotFile{
		{Repo: "web", Path: "src/theme.ts", Findings: 3, Critical: 3, PRs: []int{1}},
		{Repo: "api", Path: "src/retry.go", Findings: 3, Critical: 1, PRs: []int{1}},
		{Repo: "ops", Path: "src/keys.go", Findings: 1, Critical: 1, PRs: []int{1}},
		{Repo: "api", Path: "src/ledger.go", Findings: 1, Critical: 1, PRs: []int{1}},
		{Repo: "api", Path: "src/queue.go", Findings: 1, Critical: 1, PRs: []int{1}},
		{Repo: "api", Path: "src/config.go", Findings: 1, Critical: 0, PRs: []int{1}},
	}
	if diff := cmp.Diff(wantHot, got.HotFiles); diff != "" {
		t.Errorf("GET /leaderboard?period=week hot_files mismatch (-want +got):\n%s", diff)
	}

	// AC11: five buckets, nulls for the empty ones; api#6 has no diff stats and is in none.
	wantBuckets := []lbBucket{
		{Size: "XS", PRs: 4, AvgHealth: ptr(97), AvgFindings: ptr(0.8)},
		{Size: "S", PRs: 1, AvgHealth: ptr(60), AvgFindings: ptr(7.0)},
		{Size: "M"}, {Size: "L"},
		{Size: "XL", PRs: 1, AvgHealth: ptr(69), AvgFindings: ptr(5.0)},
	}
	if diff := cmp.Diff(wantBuckets, got.HealthBySize); diff != "" {
		t.Errorf("GET /leaderboard?period=week health_by_size mismatch (-want +got):\n%s", diff)
	}
	wantPoints := []lbPoint{
		{From: "2026-10-19", To: "2026-10-19", Opened: 3},
		{From: "2026-10-20", To: "2026-10-20", Opened: 4, Merged: 2, Critical: 4},
		{From: "2026-10-21", To: "2026-10-21", Merged: 3, Critical: 4},
		{From: "2026-10-22", To: "2026-10-22", Opened: 1},
		{From: "2026-10-23", To: "2026-10-23"}, {From: "2026-10-24", To: "2026-10-24"}, {From: "2026-10-25", To: "2026-10-25"},
	}
	if got.Activity.Granularity != "day" {
		t.Errorf("GET /leaderboard?period=week activity.granularity = %q, want day", got.Activity.Granularity)
	}
	if diff := cmp.Diff(wantPoints, got.Activity.Points); diff != "" {
		t.Errorf("GET /leaderboard?period=week activity points mismatch (-want +got):\n%s", diff)
	}
	critical := 0
	for _, p := range got.Activity.Points {
		critical += p.Critical
	}
	if critical != got.Summary.CriticalFound {
		t.Errorf("activity critical sum = %d, want critical_found %d", critical, got.Summary.CriticalFound)
	}

	assertReadOnly(t, s)
}

// AC8-AC11 for one repository over 30 days in Europe/Kyiv: weekly activity, no comparison, the
// review exactly at the window's end excluded, the golden.
func TestLeaderboard_Repo30dInKyiv(t *testing.T) {
	s := lbStack(t, lbNow)
	resp := s.get("/leaderboard?period=30d&tz=Europe/Kyiv&repo=" + repoAPI)
	checkLeaderboardGolden(t, "leaderboard_repo_30d", resp, http.StatusOK)
	got := decodeLeaderboard(t, resp)

	if got.Period.Key != "30d" || got.Period.TZ != "Europe/Kyiv" || got.Period.From != "2026-09-23T00:00:00.000+03:00" ||
		got.Period.To != "2026-10-23T00:00:00.000+03:00" || got.Period.PreviousFrom != nil || got.Period.PreviousTo != nil {
		t.Errorf("GET /leaderboard?period=30d period = %+v, want 09-23..10-23 +03:00 and no previous window", got.Period)
	}
	if got.PreviousSummary != nil {
		t.Errorf("GET /leaderboard?period=30d previous_summary = %+v, want null", got.PreviousSummary)
	}
	if len(got.Repos) != 1 || got.Repos[0].FullName != "acme/api" {
		t.Errorf("GET /leaderboard?period=30d&repo=acme/api repos = %+v, want only acme/api", got.Repos)
	}

	// api#8 is reviewed at 21:00 UTC = 00:00 Kyiv, the (exclusive) end; api#7's review is later still.
	// Six rows; merged = #2, #3, #4, #9 (api#5 has no merge time); health 544/6 = 90.7 -> 91; the
	// previous week's 9 USD run is inside this window.
	wantSummary := lbKpis{PRsReviewed: 6, Merged: 4, AvgHealth: ptr(91), TimeToFirstReviewS: ptr(int64(169200)), TimeToMergeS: ptr(int64(129600)),
		CriticalFound: 5, OpenCritical: 3, ReviewCostUSD: 10.85}
	if diff := cmp.Diff(wantSummary, got.Summary); diff != "" {
		t.Errorf("GET /leaderboard?period=30d summary mismatch (-want +got):\n%s", diff)
	}
	wantRows := []lbRow{
		{Repo: "api", Number: 2, Status: "merged", Size: ptr("XS"), Health: ptr(100), Risk: "low", Rounds: 1, TTFR: ptr(int64(14400)), TTM: ptr(int64(86400)), Cost: 0.6},
		{Repo: "api", Number: 6, Status: "reviewed", Health: ptr(100), Risk: "low", Rounds: 1, Draft: true},
		{Repo: "api", Number: 10, Status: "stale", Size: ptr("XS"), Health: ptr(100), Risk: "low", Rounds: 1, TTFR: ptr(int64(435600)), OpenAge: ptr(int64(2343600))},
		{Repo: "api", Number: 9, Status: "merged", Size: ptr("S"), Health: ptr(97), Risk: "low", Rounds: 1, TTFR: ptr(int64(93600)), TTM: ptr(int64(172800))},
		{Repo: "api", Number: 3, Status: "merged", Size: ptr("L"), Health: ptr(90), Risk: "medium", OpenCritical: 1, Rounds: 1, TTFR: ptr(int64(180000)), TTM: ptr(int64(691200))},
		{Repo: "api", Number: 1, Status: "needs_review", Size: ptr("S"), Health: ptr(57), Risk: "high", OpenCritical: 2, Rounds: 3, TTFR: ptr(int64(169200)), OpenAge: ptr(int64(871200)), Cost: 10.25, NewCommits: true},
	}
	var gotRows []lbRow
	for _, r := range got.Rows {
		gotRows = append(gotRows, lbRow{Repo: r.Repo, Number: r.Number, Status: r.Status, Size: r.Size, Health: r.Health, Risk: r.Risk,
			OpenCritical: r.OpenCritical, Rounds: r.Rounds, TTFR: r.TTFR, TTM: r.TTM, OpenAge: r.OpenAge, Cost: r.Cost, NewCommits: r.NewCommits, Draft: r.Draft})
	}
	if diff := cmp.Diff(wantRows, gotRows); diff != "" {
		t.Errorf("GET /leaderboard?period=30d rows mismatch (-want +got):\n%s", diff)
	}

	wantAwards := map[string]*lbAward{
		"cleanest":      {Repo: "api", Number: 2, Value: 100},
		"hottest":       {Repo: "api", Number: 1, Value: 4},
		"fastest_merge": {Repo: "api", Number: 2, Value: 86400},
		"most_rounds":   {Repo: "api", Number: 1, Value: 3},
		"monster":       {Repo: "api", Number: 3, Value: 500},
		"priciest":      {Repo: "api", Number: 1, Value: 10.25},
	}
	if diff := cmp.Diff(wantAwards, got.Awards); diff != "" {
		t.Errorf("GET /leaderboard?period=30d awards mismatch (-want +got):\n%s", diff)
	}
	wantAttention := []lbAttention{
		{Repo: "api", Number: 1, Reasons: []lbReason{{Kind: "open_critical", Count: 2}, {Kind: "new_commits"}, {Kind: "open_long", Days: 10}}},
		{Repo: "api", Number: 3, Reasons: []lbReason{{Kind: "merged_open_critical", Count: 1}}},
		{Repo: "api", Number: 10, Reasons: []lbReason{{Kind: "open_long", Days: 27}}},
	}
	if got.NeedsAttention.Total != 3 {
		t.Errorf("GET /leaderboard?period=30d needs_attention.total = %d, want 3", got.NeedsAttention.Total)
	}
	if diff := cmp.Diff(wantAttention, got.NeedsAttention.Items); diff != "" {
		t.Errorf("GET /leaderboard?period=30d needs_attention mismatch (-want +got):\n%s", diff)
	}
	wantHot := []lbHotFile{
		{Repo: "api", Path: "src/retry.go", Findings: 3, Critical: 1, PRs: []int{1}},
		{Repo: "api", Path: "src/ledger.go", Findings: 2, Critical: 2, PRs: []int{1, 3}},
		{Repo: "api", Path: "src/queue.go", Findings: 1, Critical: 1, PRs: []int{1}},
		{Repo: "api", Path: "src/config.go", Findings: 1, Critical: 0, PRs: []int{1}},
		{Repo: "api", Path: "src/old.go", Findings: 1, Critical: 0, PRs: []int{9}},
	}
	if diff := cmp.Diff(wantHot, got.HotFiles); diff != "" {
		t.Errorf("GET /leaderboard?period=30d hot_files mismatch (-want +got):\n%s", diff)
	}
	wantBuckets := []lbBucket{
		{Size: "XS", PRs: 2, AvgHealth: ptr(100), AvgFindings: ptr(0.0)},
		{Size: "S", PRs: 2, AvgHealth: ptr(77), AvgFindings: ptr(4.0)},
		{Size: "M"},
		{Size: "L", PRs: 1, AvgHealth: ptr(90), AvgFindings: ptr(1.0)},
		{Size: "XL"},
	}
	if diff := cmp.Diff(wantBuckets, got.HealthBySize); diff != "" {
		t.Errorf("GET /leaderboard?period=30d health_by_size mismatch (-want +got):\n%s", diff)
	}

	// Monday-started weeks clipped to the window (Kyiv dates); the critical sum is critical_found.
	wantPoints := []lbPoint{
		{From: "2026-09-23", To: "2026-09-27", Opened: 1},
		{From: "2026-09-28", To: "2026-10-04", Opened: 1, Merged: 1},
		{From: "2026-10-05", To: "2026-10-11"},
		{From: "2026-10-12", To: "2026-10-18", Opened: 3, Critical: 1},
		{From: "2026-10-19", To: "2026-10-22", Opened: 4, Merged: 3, Critical: 4},
	}
	if got.Activity.Granularity != "week" {
		t.Errorf("GET /leaderboard?period=30d activity.granularity = %q, want week", got.Activity.Granularity)
	}
	if diff := cmp.Diff(wantPoints, got.Activity.Points); diff != "" {
		t.Errorf("GET /leaderboard?period=30d activity points mismatch (-want +got):\n%s", diff)
	}
	critical := 0
	for _, p := range got.Activity.Points {
		critical += p.Critical
	}
	if critical != got.Summary.CriticalFound {
		t.Errorf("activity critical sum = %d, want critical_found %d", critical, got.Summary.CriticalFound)
	}

	assertReadOnly(t, s)
}

// AC9: previous_summary is null for a previous week without rows; the week itself still answers.
func TestLeaderboard_LastWeekHasNoPreviousRows(t *testing.T) {
	s := lbStack(t, lbNow)
	got := decodeLeaderboard(t, s.get("/leaderboard?period=last_week"))
	if got.Period.Key != "last_week" || got.Period.From != "2026-10-12T00:00:00.000+00:00" || got.Period.To != "2026-10-19T00:00:00.000+00:00" ||
		got.Period.PreviousFrom == nil || *got.Period.PreviousFrom != "2026-10-05T00:00:00.000+00:00" {
		t.Errorf("GET /leaderboard?period=last_week period = %+v, want 10-12..10-19 with the week before", got.Period)
	}
	if got.PreviousSummary != nil {
		t.Errorf("GET /leaderboard?period=last_week previous_summary = %+v, want null (no rows the week before)", got.PreviousSummary)
	}
	if got.Summary.PRsReviewed != 2 || got.Summary.Merged != 0 {
		t.Errorf("GET /leaderboard?period=last_week summary = %+v, want 2 reviewed (api#1, api#3) and 0 merged", got.Summary)
	}
}

// Empty windows keep the full shape: the clock is years after the fixture, so no PR is in the
// window although the repositories exist (EC4 shape, AC11 nulls and zero points).
func TestLeaderboard_EmptyWindow(t *testing.T) {
	s := lbStack(t, time.Date(2031, 1, 15, 12, 0, 0, 0, time.UTC))
	resp := s.get("/leaderboard?period=week")
	checkLeaderboardGolden(t, "leaderboard_empty", resp, http.StatusOK)
	got := decodeLeaderboard(t, resp)
	if len(got.Repos) != 3 || len(got.Rows) != 0 || got.NeedsAttention.Total != 0 || len(got.HotFiles) != 0 || got.PreviousSummary != nil {
		t.Errorf("empty window: repos=%d rows=%d attention=%d hot_files=%d previous_summary=%v, want 3 repos and nothing else",
			len(got.Repos), len(got.Rows), got.NeedsAttention.Total, len(got.HotFiles), got.PreviousSummary)
	}
	for name, award := range got.Awards {
		if award != nil {
			t.Errorf("empty window award %s = %+v, want null", name, award)
		}
	}
	if len(got.HealthBySize) != 5 || len(got.Activity.Points) != 7 {
		t.Errorf("empty window: %d size buckets and %d day points, want 5 and 7", len(got.HealthBySize), len(got.Activity.Points))
	}
}

// AC13: the golden has exactly the fields of the UI contract (frontend/src/vendor/shared/contracts/leaderboard.ts),
// at every level, and the route keeps the Host guard.
func TestLeaderboard_ContractShapeAndHostGuard(t *testing.T) {
	s := lbStack(t, lbNow)
	body := s.get("/leaderboard?period=week").Obj(t)

	keys := func(v any) []string {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("want an object, got %T (%v)", v, v)
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	first := func(v any) any {
		list, ok := v.([]any)
		if !ok || len(list) == 0 {
			t.Fatalf("want a non-empty array, got %v", v)
		}
		return list[0]
	}
	check := func(what string, got []string, want string) {
		t.Helper()
		w := strings.Fields(want)
		slices.Sort(w)
		if diff := cmp.Diff(w, got); diff != "" {
			t.Errorf("%s fields mismatch against the UI contract (-want +got):\n%s", what, diff)
		}
	}
	rows := body["rows"].([]any)
	api1 := rows[len(rows)-1].(map[string]any) // ranked last: three reviewers and three top findings
	attention := body["needs_attention"].(map[string]any)
	activity := body["activity"].(map[string]any)

	check("response", keys(body), "period repos summary previous_summary awards rows needs_attention hot_files health_by_size activity")
	check("period", keys(body["period"]), "key tz from to previous_from previous_to")
	check("repos[]", keys(first(body["repos"])), "id full_name last_polled_at")
	check("summary", keys(body["summary"]), "prs_reviewed merged avg_health time_to_first_review_s time_to_merge_s critical_found open_critical review_cost_usd")
	check("previous_summary", keys(body["previous_summary"]), "prs_reviewed merged avg_health time_to_first_review_s time_to_merge_s critical_found open_critical review_cost_usd")
	check("awards", keys(body["awards"]), "cleanest hottest fastest_merge most_rounds monster priciest")
	check("awards.cleanest", keys(body["awards"].(map[string]any)["cleanest"]), "pr_id repo_id repo number title value")
	check("rows[]", keys(api1), "pr_id repo_id repo number title author status draft additions deletions files_count size health risk findings open_critical rounds "+
		"time_to_first_review_s time_to_merge_s open_age_s cost_usd new_commits_since_review opened_at merged_at reviews top_findings")
	check("rows[].findings", keys(api1["findings"]), "CRITICAL WARNING SUGGESTION")
	check("rows[].reviews[]", keys(first(api1["reviews"])), "review_id agent_id agent_name score findings")
	check("rows[].top_findings[]", keys(first(api1["top_findings"])), "finding_id severity title file start_line agent_name state")
	check("needs_attention", keys(attention), "total items")
	check("needs_attention.items[]", keys(first(attention["items"])), "pr_id repo_id repo number title author opened_at reasons")
	check("hot_files[]", keys(first(body["hot_files"])), "path repo_id repo findings critical prs")
	check("health_by_size[]", keys(first(body["health_by_size"])), "size prs avg_health avg_findings")
	check("activity", keys(activity), "granularity points")
	check("activity.points[]", keys(first(activity["points"])), "from to opened merged critical")

	// A foreign Host is refused like on every route (NFR5) and nothing is read.
	resp := s.do(http.MethodGet, "/leaderboard?period=week", nil, "Host", "evil.example")
	want := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Host not allowed"}}
	if resp.Status != http.StatusForbidden {
		t.Fatalf("GET /leaderboard with Host evil.example = %d %s, want 403", resp.Status, resp.Body)
	}
	if diff := cmp.Diff(want, resp.JSON(t)); diff != "" {
		t.Errorf("GET /leaderboard with Host evil.example body mismatch (-want +got):\n%s", diff)
	}
}

// AC7: every bad query is 422 validation_error with the envelope; an unknown repository, and one
// of another workspace, is 404 not_found.
func TestLeaderboard_QueryValidation(t *testing.T) {
	s := lbStack(t, lbNow)
	for _, query := range []string{
		"period=bogus", "period=", "period=week&period=30d", "tz=Mars/Base", "tz=Local", "tz=" + strings.Repeat("a", 65),
		"repo=nope", "repo=" + repoAPI + "x", "foo=1", "period=week&Period=week",
	} {
		resp := s.get("/leaderboard?" + query)
		if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
			t.Errorf("GET /leaderboard?%s = %d %s, want 422 validation_error", query, resp.Status, resp.Body)
		}
	}
	for _, repo := range []string{lbID(idRepo, 7), repoOther} {
		resp := s.get("/leaderboard?repo=" + repo)
		if resp.Status != http.StatusNotFound || resp.errorCode(t) != "not_found" {
			t.Errorf("GET /leaderboard?repo=%s = %d %s, want 404 not_found", repo, resp.Status, resp.Body)
		}
	}
	// Valid spellings still work.
	for _, query := range []string{"", "period=last_week&tz=Europe/Kyiv&repo=all", "tz=America/New_York&repo=" + repoWeb} {
		if resp := s.get("/leaderboard?" + query); resp.Status != http.StatusOK {
			t.Errorf("GET /leaderboard?%s = %d %s, want 200", query, resp.Status, resp.Body)
		}
	}
	assertReadOnly(t, s)
}

// EC8, AC8: a removed repository disappears from the response with everything it contributed;
// the other repositories keep their data.
func TestLeaderboard_RepoRemoved(t *testing.T) {
	s := lbStack(t, lbNow)
	before := decodeLeaderboard(t, s.get("/leaderboard?period=week"))
	if len(before.Rows) != 7 || before.NeedsAttention.Total != 4 {
		t.Fatalf("before the removal: %d rows and %d attention items, want 7 and 4", len(before.Rows), before.NeedsAttention.Total)
	}

	if err := s.db.DB.Exec(`DELETE FROM repos WHERE id = ?`, repoOps).Error; err != nil {
		t.Fatalf("delete repo: %v", err)
	}
	after := decodeLeaderboard(t, s.get("/leaderboard?period=week"))

	var repos []string
	for _, r := range after.Repos {
		repos = append(repos, r.FullName)
	}
	if diff := cmp.Diff([]string{"acme/api", "acme/web"}, repos); diff != "" {
		t.Errorf("repos after the removal mismatch (-want +got):\n%s", diff)
	}
	var wantRows []lbRow
	for _, r := range before.Rows {
		if r.Repo != "ops" {
			wantRows = append(wantRows, r)
		}
	}
	if diff := cmp.Diff(wantRows, after.Rows); diff != "" {
		t.Errorf("rows after the removal mismatch (-want +got):\n%s", diff)
	}
	// ops#1 (open critical, 0.2 USD) and ops#2 (merged) are gone from every aggregate.
	wantSummary := before.Summary
	wantSummary.PRsReviewed, wantSummary.Merged = 6, 4
	wantSummary.CriticalFound, wantSummary.OpenCritical = 7, 4
	wantSummary.ReviewCostUSD = 2.6
	wantSummary.AvgHealth = ptr(88)                    // (60+100+100+99+69+97)/6 = 87.5 -> 88
	wantSummary.TimeToFirstReviewS = ptr(int64(28800)) // 3600, 14400, 43200, 90000 -> (14400+43200)/2
	wantSummary.TimeToMergeS = ptr(int64(108000))      // 7200, 86400, 129600, 691200 -> (86400+129600)/2
	if diff := cmp.Diff(wantSummary, after.Summary); diff != "" {
		t.Errorf("summary after the removal mismatch (-want +got):\n%s", diff)
	}
	if after.NeedsAttention.Total != 3 {
		t.Errorf("needs_attention.total after the removal = %d, want 3", after.NeedsAttention.Total)
	}
	for _, f := range after.HotFiles {
		if f.Repo == "ops" {
			t.Errorf("hot file %+v of the removed repository is still listed", f)
		}
	}
}

// AC10: needs attention shows at most 20 items, oldest opened first, and the total counts all.
func TestLeaderboard_AttentionCap(t *testing.T) {
	db := pgtest.Migrated(t)
	f := &lbFixture{t: t, db: db.DB}
	ws := lbID(idWorkspace, 1)
	f.exec(`INSERT INTO workspaces (id, name) VALUES (?, 'default')`, ws)
	f.exec(`INSERT INTO users (id, email, name) VALUES (?, 'you@local', 'You')`, lbID(idUser, 1))
	f.repo(repoAPI, ws, "acme", "api", nil)
	for n := 1; n <= 25; n++ { // never reviewed, opened one hour apart: every one needs a review and has been open long
		opened := sep(24, 10, 0).Add(time.Duration(n) * time.Hour)
		f.pull(ws, lbPull{repo: repoAPI, number: n, title: fmt.Sprintf("PR %d", n), author: "octo", status: "open", head: "h",
			opened: &opened, updated: &opened})
	}
	s := startStack(t, spec{db: db, noSeed: true, now: func() time.Time { return lbNow }})

	got := decodeLeaderboard(t, s.get("/leaderboard?period=30d"))
	if len(got.Rows) != 0 {
		t.Errorf("rows = %d, want 0 (nothing was reviewed)", len(got.Rows))
	}
	if got.NeedsAttention.Total != 25 || len(got.NeedsAttention.Items) != 20 {
		t.Fatalf("needs_attention total/items = %d/%d, want 25/20", got.NeedsAttention.Total, len(got.NeedsAttention.Items))
	}
	for i, item := range got.NeedsAttention.Items {
		if item.Number != i+1 {
			t.Errorf("needs_attention item %d is PR #%d, want #%d (oldest opened first)", i, item.Number, i+1)
		}
	}
	// PR #1 was opened 2026-09-24 11:00, 28 days and 1 hour before now.
	wantReasons := []lbReason{{Kind: "no_review"}, {Kind: "open_long", Days: 28}}
	if diff := cmp.Diff(wantReasons, got.NeedsAttention.Items[0].Reasons); diff != "" {
		t.Errorf("reasons of PR #1 mismatch (-want +got):\n%s", diff)
	}
}
