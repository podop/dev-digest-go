package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// Component tests of the background PR poll (plan S20 with amendments A1/A2, spec AC2-AC5, AC21)
// through the composition root: the real server, a real Postgres, the settings-backed interval
// and the GitHub stub. The poller reads its clock from spec.now and checks which workspaces are
// due every spec.pollTick (20 ms here), so a test moves the clock instead of waiting for
// minutes. The clock starts at the real "now" (truncated to the second) because GitHub's
// Retry-After is turned into a time by the real clock.

const (
	pollToken = "ghp_pollsecrettoken123"
	pollTick  = 20 * time.Millisecond
)

// pollClock is the poller's injected clock.
type pollClock struct{ ns atomic.Int64 }

func newPollClock() *pollClock {
	c := &pollClock{}
	c.ns.Store(time.Now().UTC().Truncate(time.Second).UnixNano())
	return c
}

func (c *pollClock) Now() time.Time { return time.Unix(0, c.ns.Load()).UTC() }

func (c *pollClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

// pollHarness is a running server with the poller on, one default workspace and no repository
// (repositories are added after the GitHub stub is programmed, so no cycle runs on the default stub).
type pollHarness struct {
	t     *testing.T
	s     *stack
	db    *gorm.DB
	clock *pollClock
	ws    string
	base  time.Time // the clock when the harness started

	inflight, maxInflight atomic.Int32
	nRepo                 int
}

// newPollHarness starts the server. withToken sets GITHUB_TOKEN to pollToken.
func newPollHarness(t *testing.T, withToken bool) *pollHarness {
	t.Helper()
	db := pgtest.Migrated(t)
	h := &pollHarness{t: t, db: db.DB, clock: newPollClock(), ws: lbID(idWorkspace, 1)}
	h.base = h.clock.Now()
	f := &lbFixture{t: t, db: db.DB}
	f.exec(`INSERT INTO workspaces (id, name) VALUES (?, 'default')`, h.ws)
	f.exec(`INSERT INTO users (id, email, name) VALUES (?, 'you@local', 'You')`, lbID(idUser, 1))

	env := map[string]string{}
	if withToken {
		env["GITHUB_TOKEN"] = pollToken
	}
	h.s = startStack(t, spec{
		db: db, noSeed: true, now: h.clock.Now, env: env, pollTick: pollTick,
		config: func(c *config.Config) { c.PRPollEnabled = true },
	})
	return h
}

// serve programs the GitHub stub. A short sleep in the handler widens the window in which two
// overlapping requests would show up in maxInflight.
func (h *pollHarness) serve(fn func(w http.ResponseWriter, r *http.Request)) {
	h.s.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		n := h.inflight.Add(1)
		defer h.inflight.Add(-1)
		for {
			m := h.maxInflight.Load()
			if n <= m || h.maxInflight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		fn(w, r)
		return true
	})
}

// addRepo inserts the repository o/<name> of the default workspace and returns its id; the
// poller picks it up at its next check. build, when set, inserts rows in the same transaction,
// so the poller sees the repository and its pull requests at once.
func (h *pollHarness) addRepo(name string, build ...func(f *lbFixture, repoID string)) string {
	h.t.Helper()
	return h.addRepos([]string{name}, build...)[0]
}

// addRepos inserts several repositories in one transaction: the poller's first cycle covers all of them.
func (h *pollHarness) addRepos(names []string, build ...func(f *lbFixture, repoID string)) []string {
	h.t.Helper()
	ids := make([]string, len(names))
	err := h.db.Transaction(func(tx *gorm.DB) error {
		f := &lbFixture{t: h.t, db: tx}
		for i, name := range names {
			h.nRepo++
			ids[i] = lbID(idRepo, h.nRepo)
			f.repo(ids[i], h.ws, "o", name, nil)
			for _, b := range build {
				b(f, ids[i])
			}
		}
		return nil
	})
	if err != nil {
		h.t.Fatalf("add repos %v: %v", names, err)
	}
	return ids
}

// setInterval writes the workspace's Settings polling_interval_min through the API.
func (h *pollHarness) setInterval(minutes int) {
	h.t.Helper()
	resp := h.s.do(http.MethodPut, "/settings", map[string]any{"polling_interval_min": minutes})
	if resp.Status != http.StatusOK {
		h.t.Fatalf("PUT /settings polling_interval_min=%d = %d %s, want 200", minutes, resp.Status, resp.Body)
	}
}

var pullPath = regexp.MustCompile(`^/repos/o/([^/]+)/pulls(?:/(\d+))?$`)

// lists are the GitHub requests that list a repository's pull requests, in arrival order.
func (h *pollHarness) lists(repo string) []ghRequest {
	var out []ghRequest
	for _, r := range h.s.gh.requests() {
		if m := pullPath.FindStringSubmatch(r.Path); r.Method == http.MethodGet && m != nil && m[1] == repo && m[2] == "" {
			out = append(out, r)
		}
	}
	return out
}

// stats are the GitHub requests that fetch one pull request of a repository.
func (h *pollHarness) stats(repo string) []ghRequest {
	var out []ghRequest
	for _, r := range h.s.gh.requests() {
		if m := pullPath.FindStringSubmatch(r.Path); r.Method == http.MethodGet && m != nil && m[1] == repo && m[2] != "" {
			out = append(out, r)
		}
	}
	return out
}

func (h *pollHarness) waitLists(repo string, n int) {
	h.t.Helper()
	eventually(h.t, 30*time.Second, fmt.Sprintf("%d list request(s) of o/%s", n, repo), func() bool { return len(h.lists(repo)) >= n })
	if got := len(h.lists(repo)); got != n {
		h.t.Fatalf("list requests of o/%s = %d, want %d", repo, got, n)
	}
}

// waitPolled waits until the repository's last_polled_at is at.
func (h *pollHarness) waitPolled(repoID string, at time.Time) {
	h.t.Helper()
	eventually(h.t, 30*time.Second, "last_polled_at = "+at.Format(time.RFC3339), func() bool {
		v := h.polledAt(repoID)
		return v.Valid && v.Time.Equal(at)
	})
}

func (h *pollHarness) polledAt(repoID string) sql.NullTime {
	h.t.Helper()
	return scalar[sql.NullTime](h.t, h.db, `SELECT last_polled_at FROM repos WHERE id = ?`, repoID)
}

// idle gives the poller time to (wrongly) act: a few dozen checks. A test that asserts "nothing
// happened" cannot wait for an event, so it waits for this bounded time; passing late is harmless,
// a wrong poll shows up in the counts.
func (h *pollHarness) idle() { time.Sleep(15 * pollTick) }

// ---- stub answers ----

type ghPull struct {
	number         int
	state          string // open | closed
	updated        time.Time
	merged, closed *time.Time
	draft          bool
}

func (p ghPull) json() string {
	ts := func(t *time.Time) string {
		if t == nil {
			return "null"
		}
		return `"` + t.UTC().Format(time.RFC3339) + `"`
	}
	state := p.state
	if state == "" {
		state = "open"
	}
	return fmt.Sprintf(`{"number":%d,"title":"PR %d","user":{"login":"octo"},"head":{"ref":"feat","sha":"sha%d"},"base":{"ref":"main"},`+
		`"state":%q,"merged_at":%s,"closed_at":%s,"draft":%t,"created_at":%q,"updated_at":%q`,
		p.number, p.number, p.number, state, ts(p.merged), ts(p.closed), p.draft,
		p.updated.UTC().Format(time.RFC3339), p.updated.UTC().Format(time.RFC3339))
}

func writePulls(w http.ResponseWriter, pulls ...ghPull) {
	parts := make([]string, len(pulls))
	for i, p := range pulls {
		parts[i] = p.json() + "}"
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, "["+strings.Join(parts, ",")+"]")
}

func writeStats(w http.ResponseWriter, number int, updated time.Time) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `%s,"additions":%d,"deletions":1,"changed_files":2}`, ghPull{number: number, updated: updated}.json(), number)
}

// route splits a request for o/<repo> into the repository, the PR number (0 for a list) and the page.
func route(r *http.Request) (repo string, number, page int) {
	m := pullPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return "", 0, 0
	}
	number, _ = strconv.Atoi(m[2])
	page = 1
	if p := r.URL.Query().Get("page"); p != "" {
		page, _ = strconv.Atoi(p)
	}
	return m[1], number, page
}

func writeJSONError(w http.ResponseWriter, status int, message string, headers ...string) {
	for i := 0; i+1 < len(headers); i += 2 {
		w.Header().Set(headers[i], headers[i+1])
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"message":%q}`, message)
}

// healthy answers every repository with one open PR #1, updated an hour ago, and its stats.
func (h *pollHarness) healthy(w http.ResponseWriter, r *http.Request) {
	_, number, _ := route(r)
	updated := h.clock.Now().Add(-time.Hour)
	if number > 0 {
		writeStats(w, number, updated)
		return
	}
	writePulls(w, ghPull{number: 1, updated: updated})
}

// AC3 (FR3, EC11, NFR3): the interval is the workspace's Settings polling_interval_min, repositories
// are polled one at a time, in turn, every cycle; a changed setting applies from the next cycle.
func TestPRPoll_IntervalComesFromSettings(t *testing.T) {
	h := newPollHarness(t, true)
	h.setInterval(1)
	h.serve(h.healthy)
	repos := h.addRepos([]string{"r1", "r2"})
	r1, r2 := repos[0], repos[1]

	// Polled once at once (the first round), then once per minute of the fake clock.
	h.waitLists("r1", 1)
	h.waitLists("r2", 1)
	h.waitPolled(r1, h.base)
	h.waitPolled(r2, h.base)
	for minute := 1; minute <= 3; minute++ {
		h.clock.Advance(time.Minute)
		h.waitLists("r1", minute+1)
		h.waitLists("r2", minute+1)
		h.waitPolled(r1, h.clock.Now())
		h.waitPolled(r2, h.clock.Now())
	}

	var order []string
	for _, r := range h.s.gh.requests() {
		if m := pullPath.FindStringSubmatch(r.Path); m != nil && m[2] == "" {
			order = append(order, m[1])
		}
	}
	if diff := cmp.Diff([]string{"r1", "r2", "r1", "r2", "r1", "r2", "r1", "r2"}, order); diff != "" {
		t.Errorf("order of the list requests over 3 minutes mismatch (-want +got):\n%s", diff)
	}
	if got := h.maxInflight.Load(); got != 1 {
		t.Errorf("most GitHub requests in flight at once = %d, want 1", got)
	}

	// The cycle at +3 min began at +3. The due time is its start plus the CURRENT interval (spec D17),
	// so changing the setting to 2 minutes moves it to +5: +4 polls nothing, +5 polls, +6 nothing, +7 polls.
	h.setInterval(2)
	h.idle()
	if got := len(h.lists("r1")); got != 4 {
		t.Fatalf("list requests of o/r1 after the setting changed = %d, want 4 (nothing is due yet)", got)
	}
	h.clock.Advance(time.Minute) // +4 min: one minute would be due, two are not
	h.idle()
	if got := len(h.lists("r1")); got != 4 {
		t.Errorf("list requests of o/r1 one minute after a cycle at a 2-minute interval = %d, want 4", got)
	}
	h.clock.Advance(time.Minute) // +5 min
	h.waitLists("r1", 5)
	h.waitLists("r2", 5)
	h.waitPolled(r2, h.clock.Now())
	h.clock.Advance(time.Minute) // +6 min
	h.idle()
	if got := len(h.lists("r1")); got != 5 {
		t.Errorf("list requests of o/r1 one minute after a 2-minute cycle = %d, want 5", got)
	}
	h.clock.Advance(time.Minute) // +7 min
	h.waitLists("r1", 6)
	h.waitLists("r2", 6)
}

// AC2 (FR2): a background poll stores merge time, close time and draft, and the next poll (and a
// manual poll) updates them.
func TestPRPoll_StoresMergeState(t *testing.T) {
	h := newPollHarness(t, true)
	merged := h.base.Add(-3 * time.Hour).Truncate(time.Second)
	closed := merged
	var second atomic.Bool
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		_, number, _ := route(r)
		if number > 0 {
			writeStats(w, number, h.base)
			return
		}
		updated := h.base.Add(-2 * time.Hour)
		if !second.Load() {
			writePulls(w,
				ghPull{number: 1, state: "closed", updated: updated, merged: &merged, closed: &closed},
				ghPull{number: 2, updated: updated})
			return
		}
		newer := merged.Add(time.Hour)
		writePulls(w,
			ghPull{number: 1, state: "closed", updated: updated, merged: &newer, closed: &newer},
			ghPull{number: 2, updated: updated, draft: true})
	})
	repo := h.addRepo("r")

	type state struct {
		Status             string
		MergedAt, ClosedAt sql.NullTime
		Draft              bool
	}
	read := func(number int) state {
		var st state
		if err := h.db.Raw(`SELECT status, merged_at, closed_at, draft FROM pull_requests WHERE repo_id = ? AND number = ?`, repo, number).Scan(&st).Error; err != nil {
			t.Fatalf("read PR #%d: %v", number, err)
		}
		return st
	}
	h.waitPolled(repo, h.base)
	at := func(v time.Time) sql.NullTime { return sql.NullTime{Time: v, Valid: true} }
	if diff := cmp.Diff(state{Status: "merged", MergedAt: at(merged), ClosedAt: at(closed)}, read(1)); diff != "" {
		t.Errorf("PR #1 after the first poll mismatch (-want +got):\n%s", diff)
	}
	if st := read(2); st.Draft || st.MergedAt.Valid || st.ClosedAt.Valid {
		t.Errorf("PR #2 after the first poll = %+v, want an open PR that is not a draft", st)
	}

	// Changed values, second cycle (the default interval is 5 minutes).
	second.Store(true)
	h.clock.Advance(5 * time.Minute)
	h.waitPolled(repo, h.clock.Now())
	newer := merged.Add(time.Hour)
	if diff := cmp.Diff(state{Status: "merged", MergedAt: at(newer), ClosedAt: at(newer)}, read(1)); diff != "" {
		t.Errorf("PR #1 after the second poll mismatch (-want +got):\n%s", diff)
	}
	if st := read(2); !st.Draft {
		t.Errorf("PR #2 after the second poll: draft = false, want true")
	}

	// Any sync: the manual poll writes them as well.
	second.Store(false)
	if resp := h.s.post("/repos/"+repo+"/poll", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST /repos/%s/poll = %d %s, want 200", repo, resp.Status, resp.Body)
	}
	if st := read(2); st.Draft {
		t.Errorf("PR #2 after the manual poll: draft = true, want false")
	}
	if diff := cmp.Diff(state{Status: "merged", MergedAt: at(merged), ClosedAt: at(closed)}, read(1)); diff != "" {
		t.Errorf("PR #1 after the manual poll mismatch (-want +got):\n%s", diff)
	}
}

// fullPage answers page `page` with 50 pull requests numbered from first, all updated at `updated`.
func fullPage(first int, updated time.Time) []ghPull {
	out := make([]ghPull, 50)
	for i := range out {
		out[i] = ghPull{number: first + i, updated: updated}
	}
	return out
}

// AC4 (FR4, NFR3): the first poll stops after 10 pages or at the 62-day horizon; a later poll stops
// at the first PR not updated since the previous background poll; a manual poll does not move that cutoff.
func TestPRPoll_PagesAndCutoff(t *testing.T) {
	h := newPollHarness(t, true)
	recent := h.base.Add(-time.Hour)
	ancient := h.base.Add(-70 * 24 * time.Hour)
	var cycle atomic.Int32 // 1 = first cycle, 2 = later ones
	cycle.Store(1)
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		repo, number, page := route(r)
		if number > 0 {
			writeStats(w, number, recent)
			return
		}
		switch repo {
		case "big": // 12 pages, every PR recent
			if cycle.Load() == 1 {
				if page <= 12 {
					writePulls(w, fullPage((page-1)*50+1, recent)...)
				} else {
					writePulls(w)
				}
				return
			}
			// Later: 10 PRs updated since the last poll, then PRs it already had; page 2 exists but is not asked for.
			pulls := fullPage(1000+(page-1)*50, h.base.Add(-time.Hour))
			for i := range 10 {
				pulls[i].updated = h.base.Add(4 * time.Minute)
			}
			writePulls(w, pulls...)
		case "old": // page 2 reaches PRs older than the 62-day horizon
			switch page {
			case 1:
				writePulls(w, fullPage(1, recent)...)
			case 2:
				pulls := fullPage(51, recent)
				for i := 5; i < 50; i++ {
					pulls[i].updated = ancient
				}
				writePulls(w, pulls...)
			default:
				writePulls(w, fullPage((page-1)*50+1, ancient)...)
			}
		}
	})
	h.addRepos([]string{"big", "old"})

	// First poll. big: 10 pages although a 11th and 12th exist. old: stops on page 2, which holds the first old PR.
	h.waitLists("big", 10)
	h.waitLists("old", 2)
	eventually(t, 30*time.Second, "the diff stats of both repositories", func() bool {
		return len(h.stats("big")) == 20 && len(h.stats("old")) == 20
	})
	if got := len(h.stats("big")) + len(h.lists("big")); got > 30 {
		t.Errorf("GitHub requests for o/big in the first cycle = %d, want at most 30 (10 pages + 20 stats)", got)
	}
	var pages []string
	for _, r := range h.lists("big") {
		pages = append(pages, r.Query)
	}
	for i, q := range pages {
		want := "direction=desc&per_page=50&sort=updated&state=all"
		if i > 0 { // url.Values.Encode sorts the keys
			want = "direction=desc&page=" + strconv.Itoa(i+1) + "&per_page=50&sort=updated&state=all"
		}
		if q != want {
			t.Errorf("list request %d of o/big has query %q, want %q", i+1, q, want)
		}
	}
	if n := scalar[int](t, h.db, `SELECT count(*) FROM pull_requests WHERE number <= 500 AND repo_id = ?`, lbID(idRepo, 1)); n != 500 {
		t.Errorf("stored PRs of o/big after the first poll = %d, want 500 (10 pages of 50)", n)
	}

	// Later poll: page 1 holds PRs older than the previous poll's start, so it is the last page read.
	cycle.Store(2)
	h.clock.Advance(5 * time.Minute)
	h.waitLists("big", 11)
	h.waitLists("old", 3)
	eventually(t, 30*time.Second, "the next diff stats", func() bool { return len(h.stats("big")) == 40 })
	h.idle()
	if got := len(h.lists("big")); got != 11 {
		t.Errorf("list requests of o/big after the second cycle = %d, want 11 (10 + 1)", got)
	}
}

// FR4: a manual poll sets last_polled_at but never moves the background cutoff.
func TestPRPoll_ManualPollDoesNotMoveTheCutoff(t *testing.T) {
	h := newPollHarness(t, true)
	manualUpdate := h.base.Add(2 * time.Minute) // after the first background poll, before the manual one
	var cycle atomic.Int32
	cycle.Store(1)
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		_, number, page := route(r)
		if number > 0 {
			writeStats(w, number, h.base)
			return
		}
		if cycle.Load() == 1 {
			writePulls(w, ghPull{number: 1, updated: h.base.Add(-time.Hour)})
			return
		}
		// Page 1: 50 PRs updated at +2 min; page 2: one PR from before the first poll.
		if page == 1 {
			writePulls(w, fullPage(100, manualUpdate)...)
			return
		}
		writePulls(w, ghPull{number: 7, updated: h.base.Add(-time.Hour)})
	})
	repo := h.addRepo("r")
	h.waitLists("r", 1)
	h.waitPolled(repo, h.base)

	cycle.Store(2)
	h.clock.Advance(4 * time.Minute) // before the next background cycle (+5 min)
	if resp := h.s.post("/repos/"+repo+"/poll", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST /repos/%s/poll = %d %s, want 200", repo, resp.Status, resp.Body)
	}
	// The manual poll marks the repository polled (with the service's own clock, not the poller's).
	if got := h.polledAt(repo); !got.Valid || got.Time.Equal(h.base) {
		t.Fatalf("last_polled_at after the manual poll = %v, want it moved on from the background poll's %v", got, h.base)
	}
	lists := len(h.lists("r")) // 1 background + 1 manual

	h.clock.Advance(time.Minute) // +5 min: the second background cycle
	eventually(t, 30*time.Second, "the second background cycle", func() bool { return len(h.lists("r")) > lists })
	h.waitPolled(repo, h.clock.Now())
	// Cutoff = start of the first background poll: page 1 (updated +2 min) is not old enough to stop at,
	// so page 2 is read. With the cutoff moved to the manual poll (+4 min) page 1 would have stopped it.
	if got := len(h.lists("r")) - lists; got != 2 {
		t.Errorf("list requests of the second background cycle = %d, want 2 (pages 1 and 2)", got)
	}
}

// AC5 (FR5, Q11): a primary rate limit ends the cycle and pauses the poller until the reset.
func TestPRPoll_PrimaryRateLimitPausesUntilReset(t *testing.T) {
	h := newPollHarness(t, true)
	var limited atomic.Bool
	limited.Store(true)
	reset := h.base.Add(10 * time.Minute)
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		repo, _, _ := route(r)
		if limited.Load() && repo == "r1" {
			writeJSONError(w, http.StatusForbidden, "API rate limit exceeded",
				"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			return
		}
		h.healthy(w, r)
	})
	repos := h.addRepos([]string{"r1", "r2"})
	r1, r2 := repos[0], repos[1]

	h.waitLists("r1", 1)
	eventually(t, 30*time.Second, "the pause to be logged", func() bool {
		return len(logLines(t, h.s.logs, "background poll paused: GitHub rate limit reached")) == 1
	})
	h.idle()
	if got := len(h.lists("r2")); got != 0 {
		t.Errorf("list requests of o/r2 in the limited cycle = %d, want 0 (the cycle stops at the limit)", got)
	}
	if v := h.polledAt(r1); v.Valid {
		t.Errorf("last_polled_at of the limited repository = %v, want NULL", v)
	}

	limited.Store(false)
	h.clock.Advance(5 * time.Minute) // the interval has passed, the reset has not
	h.idle()
	if got := len(h.lists("r1")); got != 1 {
		t.Errorf("list requests of o/r1 before the reset = %d, want 1 (paused)", got)
	}
	h.clock.Advance(5 * time.Minute) // the reset
	h.waitLists("r1", 2)
	h.waitLists("r2", 1)
	h.waitPolled(r2, h.clock.Now())
}

// AC5: the secondary limit (403 with Retry-After while requests remain) pauses it as well.
func TestPRPoll_SecondaryRateLimitPausesForRetryAfter(t *testing.T) {
	h := newPollHarness(t, true)
	var limited atomic.Bool
	limited.Store(true)
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		if repo, _, _ := route(r); limited.Load() && repo == "r1" {
			writeJSONError(w, http.StatusForbidden, "You have exceeded a secondary rate limit.",
				"X-RateLimit-Remaining", "57", "Retry-After", "1800")
			return
		}
		h.healthy(w, r)
	})
	repos := h.addRepos([]string{"r1", "r2"})
	r2 := repos[1]

	h.waitLists("r1", 1)
	eventually(t, 30*time.Second, "the pause to be logged", func() bool {
		return len(logLines(t, h.s.logs, "background poll paused: GitHub rate limit reached")) == 1
	})
	limited.Store(false)
	// Retry-After is 30 minutes after the (real) moment of the answer: neither the interval (5 min)
	// nor 25 minutes lets the poller back in; 45 minutes do, with a margin for a slow machine.
	h.clock.Advance(25 * time.Minute)
	h.idle()
	if got := len(h.lists("r1")); got != 1 {
		t.Errorf("list requests of o/r1 25 minutes into a 30-minute Retry-After = %d, want 1", got)
	}
	if got := len(h.lists("r2")); got != 0 {
		t.Errorf("list requests of o/r2 while paused = %d, want 0", got)
	}
	h.clock.Advance(20 * time.Minute)
	h.waitLists("r1", 2)
	h.waitLists("r2", 1)
	h.waitPolled(r2, h.clock.Now())
}

// AC5 (FR5, NFR3): a 500 for one repository is logged and the next repository is still polled;
// the failed repository is not retried inside the cycle and keeps its last-polled time.
func TestPRPoll_ServerErrorOnOneRepoGoesOn(t *testing.T) {
	h := newPollHarness(t, true)
	var failing atomic.Bool
	failing.Store(true)
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		if repo, _, _ := route(r); failing.Load() && repo == "r1" {
			writeJSONError(w, http.StatusInternalServerError, "Server Error")
			return
		}
		h.healthy(w, r)
	})
	repos := h.addRepos([]string{"r1", "r2"})
	r1, r2 := repos[0], repos[1]

	h.waitLists("r2", 1)
	h.waitPolled(r2, h.base)
	h.idle()
	if got := len(h.lists("r1")); got != 1 {
		t.Errorf("list requests of the failing o/r1 in one cycle = %d, want 1 (no retry)", got)
	}
	if v := h.polledAt(r1); v.Valid {
		t.Errorf("last_polled_at of the failed repository = %v, want NULL", v)
	}
	lines := logLines(t, h.s.logs, "background poll of a repository failed")
	if len(lines) != 1 || lines[0]["repo"] != "o/r1" || lines[0]["level"] != "WARN" {
		t.Errorf("failure log lines = %v, want one WARN for o/r1", lines)
	}
	if got := len(logLines(t, h.s.logs, "background poll paused: GitHub rate limit reached")); got != 0 {
		t.Errorf("rate-limit pauses logged for a 500 = %d, want 0", got)
	}

	// The failure does not pause anything: the next cycle tries o/r1 again.
	failing.Store(false)
	h.clock.Advance(5 * time.Minute)
	h.waitLists("r1", 2)
	h.waitPolled(r1, h.clock.Now())
}

// AC5 (FR5): without a token the cycle is skipped with one WARN, not one per repository, and
// GitHub is not called.
func TestPRPoll_NoTokenSkipsTheCycleWithOneWarning(t *testing.T) {
	h := newPollHarness(t, false)
	h.serve(h.healthy)
	r1 := h.addRepos([]string{"r1", "r2", "r3"})[0]

	warning := "background poll skipped: no GitHub token is configured"
	eventually(t, 30*time.Second, "the warning", func() bool { return len(logLines(t, h.s.logs, warning)) == 1 })
	h.idle()
	if got := len(logLines(t, h.s.logs, warning)); got != 1 {
		t.Errorf("warnings for three repositories in one cycle = %d, want 1", got)
	}
	if reqs := h.s.gh.requests(); len(reqs) != 0 {
		t.Errorf("GitHub requests without a token = %v, want none", reqs)
	}
	if v := h.polledAt(r1); v.Valid {
		t.Errorf("last_polled_at without a token = %v, want NULL", v)
	}

	h.clock.Advance(5 * time.Minute)
	eventually(t, 30*time.Second, "the warning of the second cycle", func() bool { return len(logLines(t, h.s.logs, warning)) == 2 })
}

// AC5, NFR3: the token travels only in the Authorization header: never in a URL, and never in a
// log line, also not on a failed repository.
func TestPRPoll_TokenIsOnlySentAsAHeader(t *testing.T) {
	h := newPollHarness(t, true)
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		if repo, _, _ := route(r); repo == "r2" {
			writeJSONError(w, http.StatusInternalServerError, "Server Error")
			return
		}
		h.healthy(w, r)
	})
	r1 := h.addRepos([]string{"r1", "r2"})[0]
	h.waitPolled(r1, h.base)
	eventually(t, 30*time.Second, "the failed repository to be logged", func() bool {
		return len(logLines(t, h.s.logs, "background poll of a repository failed")) == 1
	})
	eventually(t, 30*time.Second, "the stats of PR #1", func() bool { return len(h.stats("r1")) == 1 })

	reqs := h.s.gh.requests()
	if len(reqs) < 3 {
		t.Fatalf("GitHub requests = %v, want the list and stats of o/r1 and the list of o/r2", reqs)
	}
	for _, r := range reqs {
		if r.Auth != "Bearer "+pollToken {
			t.Errorf("%s %s Authorization = %q, want the bearer token", r.Method, r.Path, r.Auth)
		}
		if strings.Contains(r.Path+"?"+r.Query, pollToken) || len(r.Body) != 0 {
			t.Errorf("%s %s?%s carries the token or a body: %q", r.Method, r.Path, r.Query, r.Body)
		}
	}
	if strings.Contains(h.s.logs.String(), pollToken) {
		t.Errorf("a log line contains the GitHub token:\n%s", h.s.logs.String())
	}
}

// AC5 (FR5): a poll blocked in a GitHub request stops within 5 s of the shutdown signal, without
// another request or a last-polled time.
func TestPRPoll_ShutdownStopsABlockedPollWithinFiveSeconds(t *testing.T) {
	h := newPollHarness(t, true)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	repo := h.addRepo("r")
	h.waitLists("r", 1)

	start := time.Now()
	if err := h.s.stop(); err != nil {
		t.Errorf("stop with a poll in flight = %v, want nil", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the server took %s to stop with a poll blocked in a GitHub request, want at most 5s", took)
	}
	if got := len(h.lists("r")); got != 1 {
		t.Errorf("list requests after the stop = %d, want 1", got)
	}
	if v := h.polledAt(repo); v.Valid {
		t.Errorf("last_polled_at after an aborted poll = %v, want NULL", v)
	}
	if lines := logLines(t, h.s.logs, "background poll of a repository failed"); len(lines) != 0 {
		t.Errorf("an aborted poll was logged as a repository failure: %v", lines)
	}
}

// AC21 (FR24, NFR3): a cycle fills the diff stats of the 20 most recently updated PRs without
// stats, newest first, within 30 requests; a failed fetch is logged and the cycle goes on; the
// next cycle fills the rest (and retries the failure).
func TestPRPoll_DiffStatBackfillIsBounded(t *testing.T) {
	h := newPollHarness(t, true)
	var failing atomic.Bool
	failing.Store(true)
	h.serve(func(w http.ResponseWriter, r *http.Request) {
		_, number, _ := route(r)
		switch {
		case number == 0:
			writePulls(w) // nothing new on GitHub
		case number == 7 && failing.Load():
			writeJSONError(w, http.StatusInternalServerError, "Server Error")
		default:
			writeStats(w, number, h.base)
		}
	})
	repo := h.addRepo("stats", func(f *lbFixture, repoID string) {
		// #1..#25: no stats, #n updated n hours ago; #26 is older than the 62-day horizon; #27 has stats.
		for n := 1; n <= 26; n++ {
			updated := h.base.Add(-time.Duration(n) * time.Hour)
			if n == 26 {
				updated = h.base.Add(-70 * 24 * time.Hour)
			}
			f.pull(h.ws, lbPull{repo: repoID, number: n, title: "PR", author: "octo", status: "needs_review", head: "h", updated: &updated, opened: &updated})
		}
		updated := h.base.Add(-time.Minute)
		f.pull(h.ws, lbPull{repo: repoID, number: 27, title: "PR", author: "octo", status: "needs_review", head: "h", updated: &updated, opened: &updated,
			additions: 5, deletions: 5, files: 1})
	})

	filled := func() []int {
		var numbers []int
		if err := h.db.Raw(`SELECT number FROM pull_requests WHERE repo_id = ? AND NOT (additions = 0 AND deletions = 0 AND files_count = 0) ORDER BY number`, repo).Scan(&numbers).Error; err != nil {
			t.Fatalf("read stored stats: %v", err)
		}
		return numbers
	}
	numbersOf := func(reqs []ghRequest) []int {
		var out []int
		for _, r := range reqs {
			n, _ := strconv.Atoi(r.Path[strings.LastIndex(r.Path, "/")+1:])
			out = append(out, n)
		}
		return out
	}
	seq := func(from, to int) []int {
		var out []int
		for n := from; n <= to; n++ {
			out = append(out, n)
		}
		return out
	}

	// Cycle 1: the list page, then #1..#20 newest first; #7 fails and the cycle still reaches #20.
	h.waitPolled(repo, h.base)
	eventually(t, 30*time.Second, "19 stored stats", func() bool { return len(filled()) == 20 }) // 19 fetched + #27
	h.idle()
	if got, want := numbersOf(h.stats("stats")), seq(1, 20); !cmp.Equal(got, want) {
		t.Errorf("stats requests of cycle 1 = %v, want %v (newest update first, at most 20)", got, want)
	}
	if got := len(h.s.gh.requests()); got > 30 {
		t.Errorf("GitHub requests in cycle 1 = %d, want at most 30", got)
	}
	if got, want := filled(), append(append(seq(1, 6), seq(8, 20)...), 27); !cmp.Equal(got, want) {
		t.Errorf("PRs with stats after cycle 1 = %v, want %v", got, want)
	}
	lines := logLines(t, h.s.logs, "background poll: diff-stat backfill of a pull request failed")
	if len(lines) != 1 || lines[0]["number"] != float64(7) || lines[0]["level"] != "WARN" {
		t.Errorf("backfill failure log lines = %v, want one WARN for PR #7", lines)
	}

	// Cycle 2: the failure is gone; #7 is retried and the remaining #21..#25 follow, #26 (too old) never.
	failing.Store(false)
	before := len(h.s.gh.requests())
	h.clock.Advance(5 * time.Minute)
	eventually(t, 30*time.Second, "all stats stored", func() bool { return len(filled()) == 26 })
	h.idle()
	if got, want := numbersOf(h.stats("stats")[20:]), []int{7, 21, 22, 23, 24, 25}; !cmp.Equal(got, want) {
		t.Errorf("stats requests of cycle 2 = %v, want %v", got, want)
	}
	if got := len(h.s.gh.requests()) - before; got != 7 { // the list page and six PRs
		t.Errorf("GitHub requests in cycle 2 = %d, want 7", got)
	}
}
