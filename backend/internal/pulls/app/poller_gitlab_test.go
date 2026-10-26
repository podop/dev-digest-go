package app_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

const (
	hostCorp   = "git.corp.example"
	hostGitLab = "gitlab.com"
)

// flightCount counts the code-host requests in flight across every fake of a rig (nil counts nothing).
type flightCount struct {
	mu       sync.Mutex
	cur, max int
}

// enter registers a request and returns the function that ends it.
func (f *flightCount) enter() func() {
	if f == nil {
		return func() {}
	}
	f.mu.Lock()
	f.cur++
	f.max = max(f.max, f.cur)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.cur--
		f.mu.Unlock()
	}
}

type glCall struct {
	op   string // project | page | stats
	repo string
	host string
	at   time.Time
}

// pollGitLab answers the poller's GitLab calls through the optional hooks and records them.
type pollGitLab struct {
	mu      sync.Mutex
	calls   []glCall
	delay   time.Duration
	flight  *flightCount
	project func(repo domain.Repo) (domain.GitLabProject, error)
	page    func(repo domain.Repo, page int) ([]domain.Listed, error)
	stats   func(repo domain.Repo, numbers []int) (map[int]domain.DiffStats, error)
}

func (g *pollGitLab) record(ctx context.Context, op string, repo domain.Repo) error {
	g.mu.Lock()
	g.calls = append(g.calls, glCall{op: op, repo: repo.Name, host: repo.Host, at: time.Now()})
	delay := g.delay
	g.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (g *pollGitLab) Project(ctx context.Context, repo domain.Repo) (domain.GitLabProject, error) {
	defer g.flight.enter()()
	if err := g.record(ctx, "project", repo); err != nil {
		return domain.GitLabProject{}, err
	}
	if g.project != nil {
		return g.project(repo)
	}
	return domain.GitLabProject{ID: repo.ProjectID, FullName: repo.Owner + "/" + repo.Name}, nil
}

func (g *pollGitLab) ListPage(ctx context.Context, repo domain.Repo, page, _ int) ([]domain.Listed, error) {
	defer g.flight.enter()()
	if err := g.record(ctx, "page", repo); err != nil {
		return nil, err
	}
	if g.page != nil {
		return g.page(repo, page)
	}
	return nil, nil
}

func (g *pollGitLab) Stats(ctx context.Context, repo domain.Repo, numbers []int) (map[int]domain.DiffStats, error) {
	defer g.flight.enter()()
	if err := g.record(ctx, "stats", repo); err != nil {
		return nil, err
	}
	if g.stats != nil {
		return g.stats(repo, numbers)
	}
	out := map[int]domain.DiffStats{}
	for _, n := range numbers {
		out[n] = domain.DiffStats{Additions: 3, Deletions: 1, FilesCount: 2}
	}
	return out, nil
}

func (g *pollGitLab) Get(context.Context, domain.Repo, int) (domain.Fetched, error) {
	return domain.Fetched{}, errors.New("the poller never reads a merge request detail")
}

func (g *pollGitLab) Issue(context.Context, domain.Repo, int) (domain.Issue, error) {
	return domain.Issue{}, errors.New("the poller never reads an issue")
}

// times returns when op was called for the repo, as offsets from base.
func (g *pollGitLab) times(op, repo string, base time.Time) []time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []time.Duration
	for _, c := range g.calls {
		if c.op == op && c.repo == repo {
			out = append(out, c.at.Sub(base))
		}
	}
	return out
}

func (g *pollGitLab) count(op, repo string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if c.op == op && c.repo == repo {
			n++
		}
	}
	return n
}

func glTarget(ws, id, host string) domain.PollTarget {
	return domain.PollTarget{WorkspaceID: ws, Repo: domain.Repo{
		ID: id, Owner: "grp", Name: id, Provider: domain.ProviderGitLab, Host: host, ProjectID: 7,
	}}
}

// shareFlight makes both code-host fakes of the rig count their requests in one place.
func (r *pollRig) shareFlight() *flightCount {
	f := &flightCount{}
	r.gh.flight, r.gl.flight = f, f
	return f
}

// TestPoller_oneHostRateLimitedPausesOnlyItsRepos covers AC19 and EC16 (FR27): git.corp.example answers
// 429 Retry-After 120 s; its repos wait, gitlab.com and GitHub keep polling, one request at a time.
func TestPoller_oneHostRateLimitedPausesOnlyItsRepos(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "gh"), glTarget("ws", "com", hostGitLab), glTarget("ws", "corp1", hostCorp), glTarget("ws", "corp2", hostCorp))
		r.intervals.setAll(time.Minute, "ws")
		flight := r.shareFlight()
		r.gh.delay, r.gl.delay = time.Second, time.Second
		var first sync.Once
		r.gl.page = func(repo domain.Repo, _ int) ([]domain.Listed, error) {
			var err error
			if repo.Host == hostCorp {
				first.Do(func() {
					err = &app.GitLabError{Status: 429, RateLimited: true, RetryAfter: base.Add(120 * time.Second)}
				})
			}
			return nil, err
		}
		stop := r.start(t)
		advance(4*time.Minute + 30*time.Second) // cycles at 0, 1, 2, 3 and 4 min
		stop()

		// corp1 is limited at 0 (a cycle takes a few seconds of fake time: compare whole minutes); the
		// pause ends at 120 s.
		if diff := cmp.Diff(minutes(0, 2, 3, 4), truncated(r.gl.times("project", "corp1", base))); diff != "" {
			t.Errorf("corp1 project fetch times mismatch (-want +got):\n%s", diff)
		}
		if got := r.gl.count("project", "corp2"); got != 3 {
			t.Errorf("corp2 polled %d times, want 3 (skipped in the cycle that hit the limit and while paused)", got)
		}
		if got := truncated(r.gl.times("project", "corp2", base)); len(got) == 0 || got[0] != 2*time.Minute {
			t.Errorf("corp2 project fetch times = %v, want the first at 2m", got)
		}
		if got := r.gl.times("project", "com", base); len(got) != 5 {
			t.Errorf("gitlab.com project fetches = %v, want one per cycle (5)", got)
		}
		if got := r.gh.firstPageTimes("gh", base); len(got) != 5 {
			t.Errorf("GitHub page-1 requests = %v, want one per cycle (5)", got)
		}
		if flight.max != 1 {
			t.Errorf("max code-host requests in flight = %d, want 1", flight.max)
		}
		if got := r.logs.warns(); len(got) != 1 {
			t.Errorf("WARN lines = %q, want exactly the pause notice", got)
		}
	})
}

// TestPoller_aGitHubRateLimitDoesNotPauseGitLab is the other direction of the same isolation.
func TestPoller_aGitHubRateLimitDoesNotPauseGitLab(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "gh"), glTarget("ws", "com", hostGitLab))
		r.intervals.setAll(time.Minute, "ws")
		r.gh.respond = func(string, int) ([]domain.Listed, error) {
			return nil, &app.GitHubError{Status: 403, RateLimited: true, RateLimitReset: base.Add(10 * time.Minute)}
		}
		stop := r.start(t)
		advance(2*time.Minute + 30*time.Second)
		stop()

		if diff := cmp.Diff(minutes(0), r.gh.firstPageTimes("gh", base)); diff != "" {
			t.Errorf("GitHub request times mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(minutes(0, 1, 2), r.gl.times("page", "com", base)); diff != "" {
			t.Errorf("gitlab.com page request times mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestPoller_aGitLabRateLimitPausesUntil covers the resume rules for GitLab (A4 and the 2 h cap).
func TestPoller_aGitLabRateLimitPausesUntil(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		limit    func(base time.Time) *app.GitLabError
		wantAt   time.Duration // when the repo is polled again
	}{
		{"429 without hints waits for the next due time (A4)", 3 * time.Minute, func(time.Time) *app.GitLabError {
			return &app.GitLabError{Status: 429, RateLimited: true}
		}, 3 * time.Minute},
		{"Retry-After after the next due time", time.Minute, func(base time.Time) *app.GitLabError {
			return &app.GitLabError{Status: 429, RateLimited: true, RetryAfter: base.Add(7 * time.Minute)}
		}, 7 * time.Minute},
		{"RateLimit-Reset when there is no Retry-After", time.Minute, func(base time.Time) *app.GitLabError {
			return &app.GitLabError{Status: 429, RateLimited: true, RateLimitReset: base.Add(4 * time.Minute)}
		}, 4 * time.Minute},
		{"Retry-After wins over RateLimit-Reset", time.Minute, func(base time.Time) *app.GitLabError {
			return &app.GitLabError{Status: 429, RetryAfter: base.Add(2 * time.Minute), RateLimitReset: base.Add(9 * time.Minute)}
		}, 2 * time.Minute},
		{"a reset before the next due time", 5 * time.Minute, func(base time.Time) *app.GitLabError {
			return &app.GitLabError{Status: 429, RateLimited: true, RetryAfter: base.Add(time.Minute)}
		}, 5 * time.Minute},
		{"capped at two hours", time.Minute, func(base time.Time) *app.GitLabError {
			return &app.GitLabError{Status: 429, RateLimited: true, RetryAfter: base.Add(1000 * time.Hour)}
		}, domain.MaxRateLimitWait},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := time.Now()
				r := newRig(glTarget("ws", "a", hostCorp), glTarget("ws", "b", hostCorp))
				r.intervals.setAll(tc.interval, "ws")
				var first sync.Once
				r.gl.page = func(domain.Repo, int) ([]domain.Listed, error) {
					var err error
					first.Do(func() { err = tc.limit(base) })
					return nil, err
				}
				stop := r.start(t)
				advance(tc.wantAt - time.Second)
				if got := r.gl.count("project", "a") + r.gl.count("project", "b"); got != 1 {
					t.Errorf("GitLab project fetches before the pause ends = %d, want only the limited one", got)
				}
				advance(31 * time.Second)
				stop()

				if got := r.gl.times("project", "a", base); len(got) != 2 || got[1] != tc.wantAt {
					t.Errorf("repo a project fetch times = %v, want [0 %v]", got, tc.wantAt)
				}
				if got := r.gl.times("project", "b", base); len(got) == 0 || got[0] != tc.wantAt {
					t.Errorf("repo b project fetch times = %v, want the first at %v", got, tc.wantAt)
				}
			})
		})
	}
}

// TestPoller_aRateLimitInTheGitLabStatsPausesTheHost covers a limit hit after the pages were read.
func TestPoller_aRateLimitInTheGitLabStatsPausesTheHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "a", hostCorp), glTarget("ws", "b", hostCorp), target("ws", "gh"))
		r.intervals.setAll(time.Minute, "ws")
		r.store.pulls = map[string][]storedPull{"a": pendingPulls("a", 3)}
		r.gl.stats = func(domain.Repo, []int) (map[int]domain.DiffStats, error) {
			return nil, &app.GitLabError{Status: 429, RateLimited: true, RetryAfter: base.Add(3 * time.Minute)}
		}
		obs := &cycleLog{}
		r.observer = obs
		stop := r.start(t)
		advance(2*time.Minute + 30*time.Second)
		if got := r.gl.count("project", "b"); got != 0 {
			t.Errorf("repo b polled %d times while its host is paused, want 0", got)
		}
		stop()

		if got := r.gh.firstPageTimes("gh", base); len(got) != 3 {
			t.Errorf("GitHub page-1 requests = %v, want one per cycle (3)", got)
		}
		if diff := cmp.Diff([]string{"ws", "ws", "ws"}, obs.workspaces()); diff != "" {
			t.Errorf("AfterCycle workspaces mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestPoller_noGitHubTokenSkipsOnlyGitHubRepos covers FR27: GitLab repos of the workspace are polled.
func TestPoller_noGitHubTokenSkipsOnlyGitHubRepos(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "gh1"), glTarget("ws", "com", hostGitLab), target("ws", "gh2"), glTarget("ws", "corp", hostCorp))
		r.intervals.setAll(time.Minute, "ws")
		r.gh.respond = func(string, int) ([]domain.Listed, error) { return nil, app.ErrNoToken }
		obs := &cycleLog{}
		r.observer = obs
		stop := r.start(t)
		advance(time.Minute + 30*time.Second) // cycles at 0 and 1 min
		stop()

		if diff := cmp.Diff(minutes(0, 1), r.gh.firstPageTimes("gh1", base)); diff != "" {
			t.Errorf("gh1 request times mismatch (-want +got):\n%s", diff)
		}
		if got := r.gh.firstPageTimes("gh2", base); len(got) != 0 {
			t.Errorf("gh2 requested %d times, want 0 (GitHub is skipped for the rest of the cycle)", len(got))
		}
		for _, repo := range []string{"com", "corp"} {
			if diff := cmp.Diff(minutes(0, 1), r.gl.times("page", repo, base)); diff != "" {
				t.Errorf("GitLab repo %s page request times mismatch (-want +got):\n%s", repo, diff)
			}
		}
		if got := r.logs.warns(); len(got) != 2 {
			t.Errorf("WARN lines = %q, want one per cycle (2)", got)
		}
		if diff := cmp.Diff([]string{"ws", "ws"}, obs.workspaces()); diff != "" {
			t.Errorf("AfterCycle workspaces mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestPoller_aGitLabConnectionWithoutATokenSkipsItsReposWithOneWarnPerCycle covers EC15 (FR27).
func TestPoller_aGitLabConnectionWithoutATokenSkipsItsReposWithOneWarnPerCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "a", hostCorp), glTarget("ws", "b", hostCorp), target("ws", "gh"))
		r.intervals.setAll(time.Minute, "ws")
		r.gl.project = func(repo domain.Repo) (domain.GitLabProject, error) {
			if repo.Host == hostCorp {
				return domain.GitLabProject{}, &app.NoTokenError{Conn: "corp"}
			}
			return domain.GitLabProject{FullName: repo.Owner + "/" + repo.Name}, nil
		}
		obs := &cycleLog{}
		r.observer = obs
		stop := r.start(t)
		advance(time.Minute + 30*time.Second)
		stop()

		if got := r.gl.count("project", "b"); got != 0 {
			t.Errorf("repo b requested %d times, want 0 (its connection has no token)", got)
		}
		if got := r.gl.count("page", "a"); got != 0 {
			t.Errorf("repo a pages read %d times, want 0", got)
		}
		if got := r.logs.warns(); len(got) != 2 {
			t.Errorf("WARN lines = %q, want one per connection and cycle (2)", got)
		}
		if got := r.gh.firstPageTimes("gh", base); len(got) != 2 {
			t.Errorf("GitHub page-1 requests = %v, want one per cycle (2)", got)
		}
		if got := len(obs.workspaces()); got != 2 {
			t.Errorf("AfterCycle calls = %d, want 2 (the GitHub repo polled)", got)
		}
	})
}

// notConnectedHost fails every call of one host key the way the adapter does: with no request.
func notConnectedHost(host string) func(domain.Repo) (domain.GitLabProject, error) {
	return func(repo domain.Repo) (domain.GitLabProject, error) {
		if repo.Host == host {
			return domain.GitLabProject{}, app.ErrNotConnected
		}
		return domain.GitLabProject{FullName: repo.Owner + "/" + repo.Name}, nil
	}
}

// TestPoller_notConnectedReposAreSkippedWithOneWarnAndNoPause covers AC38 and A5 (FR27, FR48).
func TestPoller_notConnectedReposAreSkippedWithOneWarnAndNoPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "n1", hostCorp), glTarget("ws", "n2", hostCorp), glTarget("ws", "com", hostGitLab), target("ws", "gh"))
		r.intervals.setAll(time.Minute, "ws")
		r.gl.project = notConnectedHost(hostCorp)
		stop := r.start(t)
		advance(2*time.Minute + 30*time.Second) // cycles at 0, 1 and 2 min
		stop()

		// The host is asked once per cycle (the port answers without a request) and never paused: it is
		// asked again at every cycle. The host's second repo is not asked at all.
		if diff := cmp.Diff(minutes(0, 1, 2), r.gl.times("project", "n1", base)); diff != "" {
			t.Errorf("n1 calls mismatch (-want +got):\n%s", diff)
		}
		if got := r.gl.count("project", "n2"); got != 0 {
			t.Errorf("n2 asked %d times, want 0", got)
		}
		if got := r.gl.count("page", "n1") + r.gl.count("page", "n2"); got != 0 {
			t.Errorf("pages read for not-connected repos = %d, want 0", got)
		}
		if diff := cmp.Diff(minutes(0, 1, 2), r.gl.times("page", "com", base)); diff != "" {
			t.Errorf("gitlab.com page request times mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(minutes(0, 1, 2), r.gh.firstPageTimes("gh", base)); diff != "" {
			t.Errorf("GitHub request times mismatch (-want +got):\n%s", diff)
		}
		warns := r.logs.warns()
		if len(warns) != 3 {
			t.Errorf("WARN lines = %q, want exactly one per cycle (3)", warns)
		}
		if marked := r.store.markedTimes("n1", base); len(marked) != 0 {
			t.Errorf("not-connected repo marked polled at %v, want never", marked)
		}
	})
}

// TestPoller_withoutAGitLabPortEveryGitLabRepoIsNotConnected covers the nil-port reading (fail closed).
func TestPoller_withoutAGitLabPortEveryGitLabRepoIsNotConnected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "a", hostGitLab), target("ws", "gh"))
		r.intervals.setAll(time.Minute, "ws")
		p := app.NewPoller(app.PollerDeps{
			Targets: r.targets, Intervals: r.intervals, Pages: r.gh, Stats: r.stats, Store: r.store, Log: slog.New(r.logs),
		}, r.opts)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); p.Run(ctx) }()
		advance(time.Second)
		cancel()
		<-done

		if got := r.gh.firstPageTimes("gh", base); len(got) != 1 {
			t.Errorf("GitHub page-1 requests = %v, want 1", got)
		}
		if got := r.logs.warns(); len(got) != 1 {
			t.Errorf("WARN lines = %q, want one for the GitLab repo", got)
		}
	})
}

// TestPoller_aChangedIntervalAppliesWithinOneTickForGitLab keeps D17 for a workspace of GitLab repos.
func TestPoller_aChangedIntervalAppliesWithinOneTickForGitLab(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "a", hostGitLab))
		r.intervals.setAll(10*time.Minute, "ws")
		stop := r.start(t)
		advance(3*time.Minute + 30*time.Second)
		r.intervals.setAll(time.Minute, "ws") // due = last start (0) + 1 min: already past, so the next tick polls
		advance(time.Minute)
		r.intervals.setAll(30*time.Minute, "ws") // raised again: due from the last start (4 min), not after the old one
		advance(20 * time.Minute)
		stop()

		if diff := cmp.Diff(minutes(0, 4), r.gl.times("page", "a", base)); diff != "" {
			t.Errorf("page request times mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestPoller_aPausedHostDoesNotCountAsACycle: a workspace whose repos were all held back by a pause
// is due again at once when the pause ends (the old resume-time-wins rule).
func TestPoller_aPausedHostDoesNotCountAsACycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "a", hostCorp))
		r.intervals.setAll(5*time.Minute, "ws")
		var first sync.Once
		r.gl.page = func(domain.Repo, int) ([]domain.Listed, error) {
			var err error
			first.Do(func() { err = &app.GitLabError{Status: 429, RateLimited: true, RetryAfter: base.Add(12 * time.Minute)} })
			return nil, err
		}
		obs := &cycleLog{}
		r.observer = obs
		stop := r.start(t)
		advance(20 * time.Minute)
		stop()

		// The pause ends at 12 min: due since 5 min, polled at the first tick after it.
		if diff := cmp.Diff(minutes(0, 12, 17), r.gl.times("page", "a", base)); diff != "" {
			t.Errorf("page request times mismatch (-want +got):\n%s", diff)
		}
		if got := len(obs.workspaces()); got != 2 {
			t.Errorf("AfterCycle calls = %d, want 2 (the cycles at 12 and 17 min)", got)
		}
	})
}

// TestPoller_theProjectIsReadFirstAndItsNewPathStored covers FR47 in the poller.
func TestPoller_theProjectIsReadFirstAndItsNewPathStored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(glTarget("ws", "same", hostGitLab), glTarget("ws", "moved", hostGitLab))
		r.intervals.setAll(time.Minute, "ws")
		r.gl.project = func(repo domain.Repo) (domain.GitLabProject, error) {
			if repo.Name == "moved" {
				return domain.GitLabProject{ID: 7, FullName: "new/home", Owner: "new", Name: "home"}, nil
			}
			return domain.GitLabProject{ID: 7, FullName: repo.Owner + "/" + repo.Name}, nil
		}
		stop := r.start(t)
		advance(time.Second)
		stop()

		want := []domain.GitLabProject{{ID: 7, FullName: "new/home", Owner: "new", Name: "home"}}
		if diff := cmp.Diff(want, r.store.pathUpdates); diff != "" {
			t.Errorf("stored path updates mismatch (-want +got):\n%s", diff)
		}
		var ops []string
		for _, c := range r.gl.calls {
			ops = append(ops, c.repo+":"+c.op)
		}
		if want := []string{"same:project", "same:page", "moved:project", "moved:page"}; !slices.Equal(ops, want) {
			t.Errorf("GitLab calls = %v, want %v (the project first, then the pages)", ops, want)
		}
	})
}

// TestPoller_aPathAnotherRepoHasKeepsTheStoredPathAndTheSyncGoesOn covers the FR47 collision.
func TestPoller_aPathAnotherRepoHasKeepsTheStoredPathAndTheSyncGoesOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "a", hostGitLab))
		r.intervals.setAll(time.Minute, "ws")
		r.store.pathUpdated = false
		r.gl.project = func(domain.Repo) (domain.GitLabProject, error) {
			return domain.GitLabProject{ID: 7, FullName: "taken/path"}, nil
		}
		stop := r.start(t)
		advance(time.Second)
		stop()

		if got := r.gl.times("page", "a", base); len(got) != 1 {
			t.Errorf("page requests = %v, want the sync to go on by project id", got)
		}
		if got := r.store.markedTimes("a", base); len(got) != 1 {
			t.Errorf("repo marked polled %d times, want 1", len(got))
		}
		// FR47: the collision is logged once, by the store, with both repository ids; the poller adds no line.
		if got := r.logs.warns(); len(got) != 0 {
			t.Errorf("WARN lines = %q, want none from the poller for the kept path", got)
		}
	})
}

// TestPoller_aFailedProjectFetchIsAFailedSync covers FR47: no page is read, the repo is not marked.
func TestPoller_aFailedProjectFetchIsAFailedSync(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "a", hostGitLab), glTarget("ws", "b", hostGitLab))
		r.intervals.setAll(time.Minute, "ws")
		r.gl.project = func(repo domain.Repo) (domain.GitLabProject, error) {
			if repo.Name == "a" {
				return domain.GitLabProject{}, &app.GitLabError{Status: 404, Message: "404 Project Not Found"}
			}
			return domain.GitLabProject{FullName: repo.Owner + "/" + repo.Name}, nil
		}
		obs := &cycleLog{}
		r.observer = obs
		stop := r.start(t)
		advance(time.Second)
		stop()

		if got := r.gl.count("page", "a"); got != 0 {
			t.Errorf("pages read after a failed project fetch = %d, want 0", got)
		}
		if got := r.store.markedTimes("a", base); len(got) != 0 {
			t.Errorf("repo a marked polled at %v, want never", got)
		}
		if got := r.store.markedTimes("b", base); len(got) != 1 {
			t.Errorf("repo b marked polled %d times, want 1 (the cycle goes on)", got)
		}
		if got := r.logs.warns(); len(got) != 1 {
			t.Errorf("WARN lines = %q, want one for the failed repo", got)
		}
		if got := len(obs.workspaces()); got != 1 {
			t.Errorf("AfterCycle calls = %d, want 1", got)
		}
	})
}

// TestPoller_gitLabStatsAreBackfilledInBatches covers FR24 in the poller: one query per batch of 20,
// an MR the answer has no stats for stays unset and is tried again.
func TestPoller_gitLabStatsAreBackfilledInBatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(glTarget("ws", "a", hostGitLab))
		r.intervals.setAll(time.Minute, "ws")
		r.store.pulls = map[string][]storedPull{"a": pendingPulls("a", 5)}
		var asked [][]int
		r.gl.stats = func(_ domain.Repo, numbers []int) (map[int]domain.DiffStats, error) {
			asked = append(asked, slices.Clone(numbers))
			out := map[int]domain.DiffStats{}
			for _, n := range numbers {
				if n != 3 { // MR 3 has no summary yet
					out[n] = domain.DiffStats{Additions: n, Deletions: 1, FilesCount: 1}
				}
			}
			return out, nil
		}
		stop := r.start(t)
		advance(time.Second)
		if diff := cmp.Diff([]int{1, 2, 4, 5}, r.store.filled("a")); diff != "" {
			t.Errorf("PRs with stats after the first cycle mismatch (-want +got):\n%s", diff)
		}
		advance(time.Minute)
		stop()

		if diff := cmp.Diff([][]int{{1, 2, 3, 4, 5}, {3}}, asked); diff != "" {
			t.Errorf("stats queries mismatch (-want +got):\n%s", diff)
		}
		if got := r.stats.count(); got != 0 {
			t.Errorf("GitHub stats fetches for a GitLab repo = %d, want 0", got)
		}
	})
}

// TestPoller_aFailedGitLabStatsBatchIsLoggedAndTheCycleGoesOn: a plain failure is the batch's own.
func TestPoller_aFailedGitLabStatsBatchIsLoggedAndTheCycleGoesOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(glTarget("ws", "a", hostGitLab), glTarget("ws", "b", hostGitLab))
		r.intervals.setAll(time.Minute, "ws")
		r.store.pulls = map[string][]storedPull{"a": pendingPulls("a", 2)}
		r.gl.stats = func(domain.Repo, []int) (map[int]domain.DiffStats, error) {
			return nil, &app.GitLabError{Status: 500, Message: "graphql down"}
		}
		stop := r.start(t)
		advance(time.Second)
		stop()

		if got := r.store.markedTimes("b", base); len(got) != 1 {
			t.Errorf("repo b marked polled %d times, want 1", len(got))
		}
		if got := r.logs.warns(); len(got) != 1 {
			t.Errorf("WARN lines = %q, want one for the batch", got)
		}
	})
}

// truncated rounds offsets down to whole minutes.
func truncated(ds []time.Duration) []time.Duration {
	out := make([]time.Duration, len(ds))
	for i, d := range ds {
		out[i] = d.Truncate(time.Minute)
	}
	return out
}
