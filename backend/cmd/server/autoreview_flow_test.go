package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// Component tests of the automatic-review trigger (Settings C, plan S17, spec AC5-AC8, AC10, AC11,
// AC14) through the real server: a real Postgres, the settings API, the background poller with its
// observer, the review start and the GitHub stub. Setup as the PR-poll tests: no seed, one
// workspace and user, the repository inserted after the stub is programmed, the poller's clock
// (spec.now) moved by the test. The clock starts at capNow, a month far from the seeded demo data,
// and moves forward from there; the spend ledger is seeded at that clock (seedLedgerSpend).

const (
	arToken = "ghp_arsecrettoken456"
	arKey   = "sk-or-arsecretkey789"
	arTick  = 20 * time.Millisecond
	// arDecision is the message of the one INFO line of a decision (NFR3).
	arDecision = "auto review decision"
)

// arPull is one pull request as the GitHub stub lists it.
type arPull struct {
	number           int
	head             string
	created, updated time.Time
	state            string // "" = open
	draft            bool
}

// arHead is the head commit of version v of PR n; its first 12 characters are what the log shows.
func arHead(n, v int) string { return fmt.Sprintf("c0ffee%02d%02dabcdef0123456789", n, v) }

var arPullPath = regexp.MustCompile(`^/repos/o/([^/]+)/pulls(?:/(\d+))?$`)

// arGH is the stub's list of pull requests per repository name of owner o.
type arGH struct {
	mu    sync.Mutex
	pulls map[string][]arPull
}

// put adds or replaces (by number) a PR of repo.
func (g *arGH) put(repo string, p arPull) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pulls == nil {
		g.pulls = map[string][]arPull{}
	}
	for i, q := range g.pulls[repo] {
		if q.number == p.number {
			g.pulls[repo][i] = p
			return
		}
	}
	g.pulls[repo] = append(g.pulls[repo], p)
}

// edit changes the PR repo#number in place.
func (g *arGH) edit(t testing.TB, repo string, number int, fn func(p *arPull)) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range g.pulls[repo] {
		if g.pulls[repo][i].number == number {
			fn(&g.pulls[repo][i])
			return
		}
	}
	t.Fatalf("stub has no PR %s#%d", repo, number)
}

func (p arPull) json() string {
	state, closed := p.state, "null"
	if state == "" {
		state = "open"
	}
	if state == "closed" {
		closed = `"` + p.updated.UTC().Format(time.RFC3339) + `"`
	}
	return fmt.Sprintf(`{"number":%d,"title":"PR %d","user":{"login":"octo"},"head":{"ref":"feat","sha":%q,"repo":{"full_name":"o/r"}},"base":{"ref":"main","repo":{"full_name":"o/r"}},`+
		`"state":%q,"merged_at":null,"closed_at":%s,"draft":%t,"created_at":%q,"updated_at":%q`,
		p.number, p.number, p.head, state, closed, p.draft,
		p.created.UTC().Format(time.RFC3339), p.updated.UTC().Format(time.RFC3339))
}

// handle answers the list and the single-PR requests of owner o; everything else is left to the default stub.
func (g *arGH) handle(w http.ResponseWriter, r *http.Request) bool {
	m := arPullPath.FindStringSubmatch(r.URL.Path)
	if m == nil || r.Method != http.MethodGet {
		return false
	}
	g.mu.Lock()
	pulls := slices.Clone(g.pulls[m[1]])
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if m[2] == "" {
		if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page > 1 {
			_, _ = fmt.Fprint(w, "[]")
			return true
		}
		sort.Slice(pulls, func(i, j int) bool { return pulls[i].updated.After(pulls[j].updated) })
		parts := make([]string, len(pulls))
		for i, p := range pulls {
			parts[i] = p.json() + "}"
		}
		_, _ = fmt.Fprint(w, "["+strings.Join(parts, ",")+"]")
		return true
	}
	n, _ := strconv.Atoi(m[2])
	for _, p := range pulls {
		if p.number == n {
			_, _ = fmt.Fprintf(w, `%s,"additions":3,"deletions":1,"changed_files":2}`, p.json())
			return true
		}
	}
	http.NotFound(w, r)
	return true
}

// arOpts configures arStart.
type arOpts struct {
	token     bool // GITHUB_TOKEN is arToken
	pollOff   bool // PR_POLL_ENABLED=false
	rateLimit bool // the HTTP rate limits are on
	// adapters replace the model providers; nil = llm, or a mock that answers at once.
	adapters map[llm.Provider]llm.Adapter
	llm      *scripted
}

// arHarness is a running server with the poller on, one workspace, no agent and no repository.
type arHarness struct {
	t      *testing.T
	s      *stack
	db     *gorm.DB
	clock  *pollClock
	ws     string
	t0     time.Time // the clock when the harness started
	gh     *arGH
	llm    *scripted
	nRepo  int
	repoID string // the last repository added
}

func arStart(t *testing.T, o arOpts) *arHarness {
	t.Helper()
	db := pgtest.Migrated(t)
	clock := &pollClock{}
	clock.ns.Store(capNow.UnixNano())
	h := &arHarness{t: t, db: db.DB, clock: clock, ws: lbID(idWorkspace, 1), t0: capNow, gh: &arGH{}, llm: o.llm}
	if h.llm == nil {
		h.llm = mockScript()
	}
	adapters := o.adapters
	if adapters == nil {
		adapters = h.llm.forAll()
	}
	f := &lbFixture{t: t, db: db.DB}
	f.exec(`INSERT INTO workspaces (id, name) VALUES (?, 'default')`, h.ws)
	f.exec(`INSERT INTO users (id, email, name) VALUES (?, 'you@local', 'You')`, lbID(idUser, 1))

	env := map[string]string{"OPENROUTER_API_KEY": arKey}
	if o.token {
		env["GITHUB_TOKEN"] = arToken
	}
	h.s = startStack(t, spec{
		db: db, noSeed: true, now: clock.Now, env: env, pollTick: arTick, rateLimit: o.rateLimit,
		llmAdapters: adapters,
		config:      func(c *config.Config) { c.PRPollEnabled = !o.pollOff },
	})
	h.s.gh.setOverride(h.gh.handle)
	return h
}

// advance moves the clock by d without any poll (no repository exists yet, or none is due).
func (h *arHarness) advance(d time.Duration) { h.clock.Advance(d) }

func (h *arHarness) agent(name string, extra ...map[string]any) string {
	h.t.Helper()
	body := map[string]any{"name": name}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	return newAgent(h.t, h.s, body)
}

// setInterval stores a polling interval of one minute.
func (h *arHarness) setInterval() {
	h.t.Helper()
	if resp := h.s.do(http.MethodPut, "/settings", map[string]any{"polling_interval_min": 1}); resp.Status != http.StatusOK {
		h.t.Fatalf("PUT /settings polling_interval_min=1 = %d %s, want 200", resp.Status, resp.Body)
	}
}

// setAuto stores (a part of) the automatic_reviews setting.
func (h *arHarness) setAuto(v map[string]any) {
	h.t.Helper()
	if resp := h.s.do(http.MethodPut, "/settings", map[string]any{"automatic_reviews": v}); resp.Status != http.StatusOK {
		h.t.Fatalf("PUT /settings automatic_reviews=%v = %d %s, want 200", v, resp.Status, resp.Body)
	}
}

// addRepo inserts the repository o/<name> and, in the same transaction, what build inserts, so the
// poller sees them together at its next check.
func (h *arHarness) addRepo(name string, build ...func(tx *gorm.DB, repoID string)) string {
	h.t.Helper()
	h.nRepo++
	id := lbID(idRepo, h.nRepo)
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO repos (id, workspace_id, owner, name, full_name) VALUES (?, ?, 'o', ?, ?)`,
			id, h.ws, name, "o/"+name).Error; err != nil {
			return err
		}
		for _, b := range build {
			b(tx, id)
		}
		return nil
	})
	if err != nil {
		h.t.Fatalf("add repo %s: %v", name, err)
	}
	h.repoID = id
	return id
}

// insertPR stores a PR directly, as an earlier poll, a manual poll or a list request would have; it
// has diff stats, so the poller does not ask GitHub for them, and a known same-repository head (D13). reviewed is the
// last reviewed SHA.
func (h *arHarness) insertPR(tx *gorm.DB, repoID string, p arPull, reviewed string) string {
	h.t.Helper()
	status := "open"
	if p.state != "" {
		status = p.state
	}
	var rev *string
	if reviewed != "" {
		rev = &reviewed
	}
	var id string
	err := tx.Raw(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, last_reviewed_sha,
			additions, deletions, files_count, status, opened_at, updated_at, draft, head_is_fork)
		VALUES (?, ?, ?, 'stored', 'octo', 'feat', 'main', ?, ?, 3, 1, 2, ?, ?, ?, ?, false) RETURNING id::text`,
		h.ws, repoID, p.number, p.head, rev, status, p.created, p.updated, p.draft).Scan(&id).Error
	if err != nil {
		h.t.Fatalf("insert PR %d: %v", p.number, err)
	}
	return id
}

// pullID is the id of the stored PR with that number.
func (h *arHarness) pullID(number int) string {
	h.t.Helper()
	return scalar[string](h.t, h.db, `SELECT id::text FROM pull_requests WHERE number = ?`, number)
}

// cycle moves the clock by d (at least the 1-minute interval) and waits until the poller has
// polled the last repository added at that time. The evaluation of the cycle follows in the same
// goroutine; positive assertions wait for their effect, negative ones call settle.
func (h *arHarness) cycle(d time.Duration) {
	h.t.Helper()
	h.clock.Advance(d)
	h.waitPolled()
}

func (h *arHarness) waitPolled() {
	h.t.Helper()
	at := h.clock.Now()
	eventually(h.t, 30*time.Second, "the poll of the repository at "+at.Format(time.RFC3339), func() bool {
		v := scalar[sql.NullTime](h.t, h.db, `SELECT last_polled_at FROM repos WHERE id = ?`, h.repoID)
		return v.Valid && v.Time.Equal(at)
	})
}

// settle gives the trigger time to (wrongly) act after a cycle: a bounded wait, because "nothing
// happened" has no event to wait for. Passing late is harmless, a wrong decision shows up in the counts.
func (h *arHarness) settle() { time.Sleep(15 * arTick) }

// arRec is one auto-review record with its PR number.
type arRec struct {
	PRID    string `gorm:"column:pr_id"`
	Number  int    `gorm:"column:number"`
	Head    string `gorm:"column:head"`
	Event   string `gorm:"column:event"`
	Outcome string `gorm:"column:outcome"`
	Runs    int    `gorm:"column:runs"`
}

func arWant(number, version int, event, outcome string, runs int) arRec {
	return arRec{Number: number, Head: arHead(number, version), Event: event, Outcome: outcome, Runs: runs}
}

func (h *arHarness) records() []arRec {
	h.t.Helper()
	var out []arRec
	err := h.db.Raw(`SELECT a.pr_id::text AS pr_id, p.number, a.head_sha AS head, a.event, a.outcome, a.runs_started AS runs
		FROM auto_review_records a JOIN pull_requests p ON p.id = a.pr_id ORDER BY p.number, a.head_sha`).Scan(&out).Error
	if err != nil {
		h.t.Fatalf("read auto-review records: %v", err)
	}
	return out
}

// waitRecords waits until the table holds n records, all settled (a claimed start has its runs).
func (h *arHarness) waitRecords(n int) {
	h.t.Helper()
	eventually(h.t, 30*time.Second, fmt.Sprintf("%d settled auto-review record(s)", n), func() bool {
		return count(h.t, h.db, `SELECT count(*) FROM auto_review_records`) >= n &&
			count(h.t, h.db, `SELECT count(*) FROM auto_review_records WHERE outcome = 'started' AND runs_started = 0`) == 0
	})
}

// requireRecords fails unless the records are exactly want (PR ids ignored).
func (h *arHarness) requireRecords(want ...arRec) {
	h.t.Helper()
	got := h.records()
	if want == nil {
		want = []arRec{}
	}
	if got == nil {
		got = []arRec{}
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(arRec{}, "PRID")); diff != "" {
		h.t.Errorf("auto-review records mismatch (-want +got):\n%s", diff)
	}
}

// runsDone waits until no review run is running.
func (h *arHarness) runsDone() {
	h.t.Helper()
	eventually(h.t, 60*time.Second, "all review runs to finish", func() bool {
		return count(h.t, h.db, `SELECT count(*) FROM agent_runs WHERE status = 'running'`) == 0
	})
}

func (h *arHarness) autoRuns() int {
	h.t.Helper()
	return count(h.t, h.db, `SELECT count(*) FROM agent_runs WHERE source = 'auto'`)
}

// requireNoReviewWork fails if an automatic run, a multi-agent run or a model call exists.
func (h *arHarness) requireNoReviewWork() {
	h.t.Helper()
	if got := count(h.t, h.db, `SELECT count(*) FROM agent_runs`); got != 0 {
		h.t.Errorf("agent runs = %d, want 0", got)
	}
	if got := count(h.t, h.db, `SELECT count(*) FROM multi_agent_runs`); got != 0 {
		h.t.Errorf("multi-agent runs = %d, want 0", got)
	}
	if got := h.llm.calls(); got != 0 {
		h.t.Errorf("model calls = %d, want 0", got)
	}
}

// requireDecisionLogs checks AC14 (NFR3): every record has exactly one INFO line with the
// workspace, PR, repository, number, the first 12 characters of the head, event, outcome and run
// count, there is no other decision line, and no line holds a token, a key or the agent's prompt.
func (h *arHarness) requireDecisionLogs() {
	h.t.Helper()
	recs := h.records()
	line := func(workspace, pr, repo string, number any, head, event, outcome string, runs any) string {
		return fmt.Sprintf("%s %s %s #%v %s %s %s runs=%v", workspace, pr, repo, number, head, event, outcome, runs)
	}
	var want, got []string
	for _, r := range recs {
		want = append(want, line(h.ws, r.PRID, "o/r", r.Number, r.Head[:12], r.Event, r.Outcome, r.Runs))
	}
	for _, e := range h.s.logs.entries(h.t) {
		if e["msg"] != arDecision {
			continue
		}
		if e["level"] != "INFO" {
			h.t.Errorf("decision log level = %v, want INFO: %v", e["level"], e)
		}
		got = append(got, line(fmt.Sprint(e["workspace_id"]), fmt.Sprint(e["pr_id"]), fmt.Sprint(e["repo"]), e["number"],
			fmt.Sprint(e["head_sha"]), fmt.Sprint(e["event"]), fmt.Sprint(e["outcome"]), e["runs"]))
	}
	sort.Strings(want)
	sort.Strings(got)
	if diff := cmp.Diff(want, got); diff != "" {
		h.t.Errorf("decision log lines vs records mismatch (-want +got):\n%s", diff)
	}
	logs := h.s.logs.String()
	for _, secret := range []string{arToken, arKey, "Bearer ", "Review the diff."} {
		if strings.Contains(logs, secret) {
			h.t.Errorf("the log holds %q, want no token, key or prompt text", secret)
		}
	}
}

// ---- AC5, AC6 (draft), AC8 (multi-agent), AC14 ----

// A new PR after arming starts one review with the selected agents (source auto); a PR opened
// before arming, a draft and a closed PR start nothing; a PR stored by another path is still a
// candidate; a second and third cycle start nothing again (at-most-once); new commits on a PR opened
// before arming start nothing (D6); a draft marked ready starts as a new PR.
func TestAutoReviewFlow_NewPRsStartOnce(t *testing.T) { // AC5, AC6, AC8, AC14, FR8
	h := arStart(t, arOpts{token: true})
	a1, a2 := h.agent("Ar One"), h.agent("Ar Two")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1, a2}})
	h.advance(2 * time.Hour)
	now := h.clock.Now()
	after, before := h.t0.Add(30*time.Minute), h.t0.Add(-time.Hour)

	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: after, updated: now.Add(-10 * time.Minute)})                 // A
	h.gh.put("r", arPull{number: 2, head: arHead(2, 1), created: before, updated: now.Add(-9 * time.Minute)})                 // B: opened before arming
	h.gh.put("r", arPull{number: 3, head: arHead(3, 1), created: after, updated: now.Add(-8 * time.Minute), draft: true})     // C
	h.gh.put("r", arPull{number: 4, head: arHead(4, 1), created: after, updated: now.Add(-7 * time.Minute), state: "closed"}) // D
	h.addRepo("r", func(tx *gorm.DB, repoID string) {                                                                         // E: stored before the first background cycle
		h.insertPR(tx, repoID, arPull{number: 5, head: arHead(5, 1), created: after.Add(15 * time.Minute), updated: now.Add(-6 * time.Minute)}, "")
	})

	h.waitRecords(2)
	h.requireRecords(arWant(1, 1, "new_pr", "started", 2), arWant(5, 1, "new_pr", "started", 2))
	if got := h.autoRuns(); got != 4 {
		t.Errorf("auto runs = %d, want 4 (2 agents on A and on E)", got)
	}
	if got := count(t, h.db, `SELECT count(*) FROM agent_runs WHERE source <> 'auto'`); got != 0 {
		t.Errorf("runs with another source = %d, want 0", got)
	}
	if got := count(t, h.db, `SELECT count(*) FROM multi_agent_runs`); got != 2 {
		t.Errorf("multi-agent runs = %d, want 2 (one per PR)", got)
	}
	for _, number := range []int{1, 5} {
		got := scalar[string](t, h.db, `SELECT coalesce(string_agg(agent_id::text, ',' ORDER BY agent_id), '') FROM agent_runs WHERE pr_id = ?`, h.pullID(number))
		want := []string{a1, a2}
		slices.Sort(want)
		if got != strings.Join(want, ",") {
			t.Errorf("agents of the runs on PR %d = %q, want %q", number, got, strings.Join(want, ","))
		}
	}
	h.runsDone()

	// Two more cycles decide nothing again (FR8): the heads have records.
	h.cycle(time.Minute)
	h.cycle(time.Minute)
	h.settle()
	h.requireRecords(arWant(1, 1, "new_pr", "started", 2), arWant(5, 1, "new_pr", "started", 2))
	if got := h.autoRuns(); got != 4 {
		t.Errorf("auto runs after two more cycles = %d, want 4", got)
	}

	// D6: B gets a new head after arming; it was opened before, so nothing starts.
	h.gh.edit(t, "r", 2, func(p *arPull) { p.head, p.updated = arHead(2, 2), h.clock.Now() })
	h.cycle(time.Minute)
	h.settle()
	if got := count(t, h.db, `SELECT count(*) FROM pull_requests WHERE number = 2 AND head_sha = ?`, arHead(2, 2)); got != 1 {
		t.Fatalf("PR 2 stored with the new head = %d row(s), want 1: the poll did not update it", got)
	}
	h.requireRecords(arWant(1, 1, "new_pr", "started", 2), arWant(5, 1, "new_pr", "started", 2))

	// EC3: the draft is marked ready; its current head is a new-PR event.
	h.gh.edit(t, "r", 3, func(p *arPull) { p.draft, p.updated = false, h.clock.Now() })
	h.cycle(time.Minute)
	h.waitRecords(3)
	h.requireRecords(arWant(1, 1, "new_pr", "started", 2), arWant(3, 1, "new_pr", "started", 2), arWant(5, 1, "new_pr", "started", 2))
	h.runsDone()

	h.requireDecisionLogs()
}

// ---- AC6, AC8 (one agent): triggers and new commits ----

func TestAutoReviewFlow_TriggerConditions(t *testing.T) { // AC6, AC8, AC14
	h := arStart(t, arOpts{token: true})
	a1 := h.agent("Ar One")
	h.setInterval()
	// With on_new_pr off, a new PR is recorded as skipped and nothing starts.
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}, "on_new_pr": false, "on_new_commits": true})
	h.advance(2 * time.Hour)
	now := h.clock.Now()
	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: h.t0.Add(30 * time.Minute), updated: now.Add(-time.Minute)})
	h.addRepo("r")

	h.waitRecords(1)
	h.requireRecords(arWant(1, 1, "new_pr", "skipped_trigger_off", 0))
	h.settle()
	h.requireNoReviewWork()

	// A later head is a new-commits event (the PR has a record) and starts: one agent, one run, no multi-agent run.
	h.gh.edit(t, "r", 1, func(p *arPull) { p.head, p.updated = arHead(1, 2), h.clock.Now() })
	h.cycle(time.Minute)
	h.waitRecords(2)
	h.requireRecords(arWant(1, 1, "new_pr", "skipped_trigger_off", 0), arWant(1, 2, "new_commits", "started", 1))
	if got := h.autoRuns(); got != 1 {
		t.Errorf("auto runs = %d, want 1", got)
	}
	if got := count(t, h.db, `SELECT count(*) FROM multi_agent_runs`); got != 0 {
		t.Errorf("multi-agent runs for one agent = %d, want 0", got)
	}
	h.runsDone()

	// With on_new_commits off, the next head is recorded as skipped.
	h.setAuto(map[string]any{"on_new_pr": true, "on_new_commits": false})
	h.gh.edit(t, "r", 1, func(p *arPull) { p.head, p.updated = arHead(1, 3), h.clock.Now() })
	h.cycle(time.Minute)
	h.waitRecords(3)
	h.requireRecords(arWant(1, 1, "new_pr", "skipped_trigger_off", 0), arWant(1, 2, "new_commits", "started", 1),
		arWant(1, 3, "new_commits", "skipped_trigger_off", 0))

	// Two head changes between two cycles: only the newest head is decided.
	h.setAuto(map[string]any{"on_new_commits": true})
	h.gh.edit(t, "r", 1, func(p *arPull) { p.head, p.updated = arHead(1, 4), h.clock.Now() })
	h.gh.edit(t, "r", 1, func(p *arPull) { p.head, p.updated = arHead(1, 5), h.clock.Now() })
	h.cycle(time.Minute)
	h.waitRecords(4)
	h.requireRecords(arWant(1, 1, "new_pr", "skipped_trigger_off", 0), arWant(1, 2, "new_commits", "started", 1),
		arWant(1, 3, "new_commits", "skipped_trigger_off", 0), arWant(1, 5, "new_commits", "started", 1))
	h.runsDone()

	h.requireDecisionLogs()
}

// ---- AC7: skips ----

// A running manual run defers without a record, and once it ends on that head the next cycle
// records skipped_already_reviewed; a PR already reviewed at its head is skipped; of the selected
// agents {enabled, disabled, deleted} only the enabled one runs; with only unavailable agents the
// head is skipped_no_agents.
func TestAutoReviewFlow_SkipDecisions(t *testing.T) { // AC7, AC8, AC14
	h := arStart(t, arOpts{token: true})
	enabled, disabled := h.agent("Ar Enabled"), h.agent("Ar Disabled")
	if err := h.db.Exec(`UPDATE agents SET enabled = false WHERE id = ?`, disabled).Error; err != nil {
		t.Fatal(err)
	}
	const ghost = "00000000-0000-4000-8000-0000000000ff" // an agent that was deleted
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{enabled, disabled, ghost}})
	h.advance(2 * time.Hour)
	now, after := h.clock.Now(), h.t0.Add(30*time.Minute)

	var p1 string
	h.addRepo("r", func(tx *gorm.DB, repoID string) {
		p1 = h.insertPR(tx, repoID, arPull{number: 1, head: arHead(1, 1), created: after, updated: now.Add(-3 * time.Minute)}, "")
		h.insertPR(tx, repoID, arPull{number: 2, head: arHead(2, 1), created: after, updated: now.Add(-2 * time.Minute)}, arHead(2, 1))
		h.insertPR(tx, repoID, arPull{number: 3, head: arHead(3, 1), created: after, updated: now.Add(-time.Minute)}, "")
		if err := tx.Exec(`INSERT INTO agent_runs (workspace_id, pr_id, ran_at, status, source) VALUES (?, ?, ?, 'running', 'local')`,
			h.ws, p1, now).Error; err != nil {
			t.Fatal(err)
		}
	})

	h.waitRecords(2)
	h.requireRecords(arWant(2, 1, "new_pr", "skipped_already_reviewed", 0), arWant(3, 1, "new_pr", "started", 1))
	// PR 3: the enabled agent only; PR 1: a manual run is running, so no record.
	if got := scalar[string](t, h.db, `SELECT coalesce(string_agg(agent_id::text, ','), '') FROM agent_runs WHERE source = 'auto'`); got != enabled {
		t.Errorf("agents of the auto runs = %q, want only the enabled agent %q", got, enabled)
	}
	if got := count(t, h.db, `SELECT count(*) FROM multi_agent_runs`); got != 0 {
		t.Errorf("multi-agent runs for one effective agent = %d, want 0", got)
	}
	// The manual run ends on this head (EC12); the setting now names only unavailable agents; a new PR arrives.
	h.exec(`UPDATE agent_runs SET status = 'done' WHERE pr_id = ? AND source = 'local'`, p1)
	h.runsDone()
	h.exec(`UPDATE pull_requests SET last_reviewed_sha = head_sha WHERE id = ?`, p1)
	h.setAuto(map[string]any{"agent_ids": []string{disabled, ghost}})
	h.insertPR(h.db, h.repoID, arPull{number: 4, head: arHead(4, 1), created: after, updated: now}, "")
	h.cycle(time.Minute)
	h.waitRecords(4)
	h.requireRecords(
		arWant(1, 1, "new_pr", "skipped_already_reviewed", 0),
		arWant(2, 1, "new_pr", "skipped_already_reviewed", 0),
		arWant(3, 1, "new_pr", "started", 1),
		arWant(4, 1, "new_pr", "skipped_no_agents", 0),
	)
	if got := h.autoRuns(); got != 1 {
		t.Errorf("auto runs = %d, want 1 (only PR 3 started)", got)
	}
	h.requireDecisionLogs()
}

func (h *arHarness) exec(query string, args ...any) {
	h.t.Helper()
	if err := h.db.Exec(query, args...).Error; err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

// With the spend cap reached the head is recorded as skipped_spend_cap, no run is created and the
// model is never called; lifting the cap does not bring that head back (EC11), a later PR starts.
func TestAutoReviewFlow_SpendCapReached(t *testing.T) { // AC7, EC11, AC14
	h := arStart(t, arOpts{token: true})
	a1 := h.agent("Ar One")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}})
	h.advance(2 * time.Hour)
	setCap(t, h.s, 1)
	seedLedgerSpend(t, h.s, h.ws, h.clock.Now(), 2)
	now, after := h.clock.Now(), h.t0.Add(30*time.Minute)

	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: after, updated: now.Add(-time.Minute)})
	h.addRepo("r")
	h.waitRecords(1)
	h.requireRecords(arWant(1, 1, "new_pr", "skipped_spend_cap", 0))
	h.settle()
	h.requireNoReviewWork()

	setCap(t, h.s, nil)
	h.gh.put("r", arPull{number: 2, head: arHead(2, 1), created: after, updated: h.clock.Now()})
	h.cycle(time.Minute)
	h.waitRecords(2)
	h.requireRecords(arWant(1, 1, "new_pr", "skipped_spend_cap", 0), arWant(2, 1, "new_pr", "started", 1))
	if got := h.autoRuns(); got != 1 {
		t.Errorf("auto runs after the cap was lifted = %d, want 1 (PR 2 only)", got)
	}
	h.runsDone()
	h.requireDecisionLogs()
}

// ---- AC8: a run that fails, a start that fails ----

// An agent whose provider has no key yields a failed run and a started record; the next cycles
// start nothing again.
func TestAutoReviewFlow_ProviderWithoutKeyStillStarted(t *testing.T) { // AC8, EC17, FR8
	mock := mockScript()
	keyed := &scripted{needsKey: true, complete: mockAnswer}
	h := arStart(t, arOpts{token: true, llm: mock, adapters: map[llm.Provider]llm.Adapter{
		llm.OpenAI: keyed, llm.Anthropic: mock, llm.OpenRouter: mock,
	}})
	agent := h.agent("Ar OpenAI", map[string]any{"provider": "openai", "model": "gpt-4o-mini"})
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{agent}})
	h.advance(2 * time.Hour)
	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: h.t0.Add(30 * time.Minute), updated: h.clock.Now().Add(-time.Minute)})
	h.addRepo("r")

	h.waitRecords(1)
	h.requireRecords(arWant(1, 1, "new_pr", "started", 1))
	run := scalar[string](t, h.db, `SELECT id::text FROM agent_runs WHERE source = 'auto'`)
	if got := waitStatus(t, h.s, run, "failed", "done"); got != "failed" {
		t.Errorf("status of the run without a key = %q, want failed", got)
	}
	h.cycle(time.Minute)
	h.cycle(time.Minute)
	h.settle()
	h.requireRecords(arWant(1, 1, "new_pr", "started", 1))
	if got := h.autoRuns(); got != 1 {
		t.Errorf("auto runs after two more cycles = %d, want 1: a failed run is not retried", got)
	}
	h.requireDecisionLogs()
}

// A start that fails (here: the database refuses to create an auto run) is recorded as failed with
// no run, logged as a warning and not retried by the next cycle, not even after the cause is gone.
func TestAutoReviewFlow_StartErrorIsRecordedNotRetried(t *testing.T) { // AC8, FR7, FR8
	h := arStart(t, arOpts{token: true})
	a1, a2 := h.agent("Ar One"), h.agent("Ar Two")
	h.exec(`CREATE FUNCTION ar_refuse_auto() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN IF NEW.source = 'auto' THEN RAISE EXCEPTION 'ar: refused'; END IF; RETURN NEW; END $$`)
	h.exec(`CREATE TRIGGER ar_refuse_auto BEFORE INSERT ON agent_runs FOR EACH ROW EXECUTE FUNCTION ar_refuse_auto()`)
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1, a2}})
	h.advance(2 * time.Hour)
	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: h.t0.Add(30 * time.Minute), updated: h.clock.Now().Add(-time.Minute)})
	h.addRepo("r")

	h.waitRecords(1)
	h.requireRecords(arWant(1, 1, "new_pr", "failed", 0))
	h.requireNoReviewWork()
	var warned bool
	for _, e := range h.s.logs.entries(t) {
		if e["level"] == "WARN" && e["msg"] == "auto review start failed" && e["outcome"] == nil {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no WARN line %q in the log", "auto review start failed")
	}

	h.exec(`DROP TRIGGER ar_refuse_auto ON agent_runs`)
	h.cycle(time.Minute)
	h.settle()
	h.requireRecords(arWant(1, 1, "new_pr", "failed", 0))
	h.requireNoReviewWork()
	h.requireDecisionLogs()
}

// ---- AC10: rate limits ----

// With 8 candidates 5 start in the first evaluation, oldest update first, and 3 in the next; the
// 10 requests per minute of POST /pulls/:id/review are all still free for a person.
func TestAutoReviewFlow_RateLimitPerEvaluation(t *testing.T) { // AC10, FR9, EC13
	h := arStart(t, arOpts{token: true, rateLimit: true})
	a1 := h.agent("Ar One")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}})
	h.advance(2 * time.Hour)
	now, after := h.clock.Now(), h.t0.Add(30*time.Minute)
	for n := 1; n <= 8; n++ { // PR 1 is the oldest update, PR 8 the newest
		h.gh.put("r", arPull{number: n, head: arHead(n, 1), created: after, updated: now.Add(time.Duration(n-10) * time.Minute)})
	}
	h.addRepo("r")

	h.waitRecords(5)
	h.settle()
	var first []arRec
	for i := 1; i <= 5; i++ {
		first = append(first, arWant(i, 1, "new_pr", "started", 1))
	}
	h.requireRecords(first...)
	h.runsDone()

	h.cycle(time.Minute)
	h.waitRecords(8)
	h.settle()
	all := slices.Clone(first)
	for i := 6; i <= 8; i++ {
		all = append(all, arWant(i, 1, "new_pr", "started", 1))
	}
	h.requireRecords(all...)
	if got := h.autoRuns(); got != 8 {
		t.Errorf("auto runs = %d, want 8", got)
	}
	h.runsDone()

	// The automatic starts did not use the HTTP bucket: ten manual starts are admitted, the 11th is refused.
	pr := h.pullID(1)
	for i := 1; i <= 10; i++ {
		if resp := startReview(t, h.s, pr, map[string]any{"agentId": a1}); resp.Status != http.StatusOK {
			t.Fatalf("manual review request %d = %d %s, want 200", i, resp.Status, resp.Body)
		}
	}
	if resp := startReview(t, h.s, pr, map[string]any{"agentId": a1}); resp.Status != http.StatusTooManyRequests {
		t.Errorf("manual review request 11 = %d %s, want 429", resp.Status, resp.Body)
	}
	h.runsDone()
}

// With 20 started records in the last 60 minutes none starts; once one of them ages out exactly
// one does; the window is rolling.
func TestAutoReviewFlow_RateLimitPerHour(t *testing.T) { // AC10, FR9
	h := arStart(t, arOpts{token: true})
	a1 := h.agent("Ar One")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}})
	h.advance(2 * time.Hour)
	now, after := h.clock.Now(), h.t0.Add(30*time.Minute)
	for n := 1; n <= 3; n++ {
		h.gh.put("r", arPull{number: n, head: arHead(n, 1), created: after, updated: now.Add(time.Duration(n-5) * time.Minute)})
	}
	h.addRepo("r", func(tx *gorm.DB, repoID string) {
		for i := 0; i < 20; i++ { // 19 started 10 minutes ago, one 59 minutes ago
			at := now.Add(-10 * time.Minute)
			if i == 0 {
				at = now.Add(-59 * time.Minute)
			}
			id := h.insertPR(tx, repoID, arPull{number: 100 + i, head: arHead(100+i, 1), created: after, updated: now.Add(-time.Hour)}, "")
			if err := tx.Exec(`INSERT INTO auto_review_records (workspace_id, pr_id, head_sha, event, outcome, runs_started, decided_at)
				VALUES (?, ?, ?, 'new_pr', 'started', 1, ?)`, h.ws, id, arHead(100+i, 1), at).Error; err != nil {
				t.Fatal(err)
			}
		}
	})

	h.waitPolled()
	h.cycle(time.Minute)
	h.settle()
	if got := count(t, h.db, `SELECT count(*) FROM auto_review_records`); got != 20 {
		t.Errorf("records with the window full = %d, want 20 (the seeded ones): nothing may start", got)
	}
	if got := h.autoRuns(); got != 0 {
		t.Errorf("auto runs with the window full = %d, want 0", got)
	}

	// Two minutes later the record from 59 minutes ago is 61 minutes old: one slot is free.
	h.cycle(2 * time.Minute)
	h.waitRecords(21)
	h.settle()
	h.cycle(time.Minute)
	h.settle()
	if got := count(t, h.db, `SELECT count(*) FROM auto_review_records WHERE pr_id = ?`, h.pullID(1)); got != 1 {
		t.Errorf("records of the oldest candidate = %d, want 1", got)
	}
	for _, n := range []int{2, 3} {
		if got := count(t, h.db, `SELECT count(*) FROM auto_review_records WHERE pr_id = ?`, h.pullID(n)); got != 0 {
			t.Errorf("records of candidate %d = %d, want 0: the window is full again", n, got)
		}
	}
	if got := h.autoRuns(); got != 1 {
		t.Errorf("auto runs = %d, want 1", got)
	}
	h.runsDone()
}

// ---- AC11: nothing when it must not ----

// Without a GitHub token the poller skips its cycle, so the trigger is never asked.
func TestAutoReviewFlow_NoTokenNothingStarts(t *testing.T) { // AC11, FR3
	h := arStart(t, arOpts{token: false})
	a1 := h.agent("Ar One")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}})
	h.advance(2 * time.Hour)
	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: h.t0.Add(30 * time.Minute), updated: h.clock.Now().Add(-time.Minute)})
	h.addRepo("r")

	eventually(t, 30*time.Second, "the poller to skip its cycle for the missing token", func() bool {
		return strings.Contains(h.s.logs.String(), "background poll skipped: no GitHub token")
	})
	h.settle()
	h.requireRecords()
	h.requireNoReviewWork()
}

// With the background poll switched off there is no trigger; a manual poll stores the PR and starts nothing.
func TestAutoReviewFlow_PollOffManualPollStartsNothing(t *testing.T) { // AC11, AC5
	h := arStart(t, arOpts{token: true, pollOff: true})
	a1 := h.agent("Ar One")
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}})
	h.advance(2 * time.Hour)
	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: h.t0.Add(30 * time.Minute), updated: h.clock.Now().Add(-time.Minute)})
	repo := h.addRepo("r")

	resp := h.s.do(http.MethodPost, "/repos/"+repo+"/poll", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /repos/:id/poll = %d %s, want 200", resp.Status, resp.Body)
	}
	if got := count(t, h.db, `SELECT count(*) FROM pull_requests WHERE repo_id = ?`, repo); got != 1 {
		t.Fatalf("PRs stored by the manual poll = %d, want 1", got)
	}
	h.settle()
	h.requireRecords()
	h.requireNoReviewWork()
}

// A setting that is not armed (disabled, no agent, no trigger) starts nothing; arming it later
// reviews only the PRs opened after that moment (EC15, P7); disabling it while an automatic run is
// in flight lets the run finish and starts nothing more (EC14).
func TestAutoReviewFlow_NotArmedNothingStarts(t *testing.T) { // AC11, EC14, EC15
	gate := newGate()
	t.Cleanup(gate.Open)
	h := arStart(t, arOpts{token: true, llm: gate.scripted})
	a1 := h.agent("Ar One")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": false, "agent_ids": []string{a1}})
	h.advance(2 * time.Hour)
	now := h.clock.Now()
	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: h.t0.Add(30 * time.Minute), updated: now.Add(-time.Minute)})
	h.addRepo("r")
	h.waitPolled()
	h.settle()
	h.requireRecords()

	// A slice, not a map: each case changes the stored value, so a random order would make the
	// state the re-arming below starts from random (only the last case leaves an agent and both triggers).
	for _, tc := range []struct {
		name string
		v    map[string]any
	}{
		{"enabled, no agent", map[string]any{"enabled": true, "agent_ids": []string{}}},
		{"agent, no trigger", map[string]any{"enabled": true, "agent_ids": []string{a1}, "on_new_pr": false, "on_new_commits": false}},
		{"disabled with agent", map[string]any{"enabled": false, "agent_ids": []string{a1}, "on_new_pr": true, "on_new_commits": true}},
	} {
		h.setAuto(tc.v)
		h.cycle(time.Minute)
		h.settle()
		if got := count(t, h.db, `SELECT count(*) FROM auto_review_records`); got != 0 {
			t.Errorf("records with the setting %s = %d, want 0", tc.name, got)
		}
	}
	if got := count(t, h.db, `SELECT count(*) FROM agent_runs`); got != 0 {
		t.Errorf("runs with an unarmed setting = %d, want 0", got)
	}

	// Armed now (every field set, whatever the last case left): PR 1 was opened before this moment, so
	// only the PR opened after it is reviewed.
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{a1}, "on_new_pr": true, "on_new_commits": true})
	h.advance(time.Hour)
	h.gh.put("r", arPull{number: 2, head: arHead(2, 1), created: h.clock.Now().Add(-time.Minute), updated: h.clock.Now()})
	h.cycle(time.Minute)
	h.waitRecords(1)
	h.requireRecords(arWant(2, 1, "new_pr", "started", 1))
	eventually(t, 30*time.Second, "the automatic run to be running", func() bool {
		return count(t, h.db, `SELECT count(*) FROM agent_runs WHERE source = 'auto' AND status = 'running'`) == 1
	})

	// Disabled while the run is in flight: it finishes, and a PR opened afterwards starts nothing.
	h.setAuto(map[string]any{"enabled": false})
	h.gh.put("r", arPull{number: 3, head: arHead(3, 1), created: h.clock.Now(), updated: h.clock.Now()})
	h.cycle(time.Minute)
	h.settle()
	h.requireRecords(arWant(2, 1, "new_pr", "started", 1))
	gate.Open()
	run := scalar[string](t, h.db, `SELECT id::text FROM agent_runs WHERE source = 'auto'`)
	if got := waitStatus(t, h.s, run, "done", "failed"); got == "" {
		t.Errorf("status of the in-flight run = %q, want it to finish", got)
	}
	h.cycle(time.Minute)
	h.settle()
	h.requireRecords(arWant(2, 1, "new_pr", "started", 1))
	if got := h.autoRuns(); got != 1 {
		t.Errorf("auto runs = %d, want 1", got)
	}
	h.requireDecisionLogs()
}
