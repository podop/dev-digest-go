package app_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

// pollTargets is a fixed list of repositories.
type pollTargets []domain.PollTarget

func (p pollTargets) ListPollTargets(context.Context) ([]domain.PollTarget, error) { return p, nil }

func target(ws, id string) domain.PollTarget {
	return domain.PollTarget{WorkspaceID: ws, Repo: domain.Repo{ID: id, Owner: "acme", Name: id}}
}

// pollIntervals gives each workspace its interval; a workspace without one reads as an error.
type pollIntervals struct {
	mu  sync.Mutex
	set map[string]time.Duration
	err error
}

func (p *pollIntervals) Interval(_ context.Context, ws string) (time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return 0, p.err
	}
	return p.set[ws], nil
}

func (p *pollIntervals) setAll(d time.Duration, workspaces ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ws := range workspaces {
		p.set[ws] = d
	}
}

type pageCall struct {
	repo string
	page int
	at   time.Time
}

// pollGitHub answers page requests through respond, records them and counts the requests in flight.
type pollGitHub struct {
	mu          sync.Mutex
	calls       []pageCall
	inflight    int
	maxInflight int
	delay       time.Duration
	respond     func(repo string, page int) ([]domain.Listed, error)
	flight      *flightCount // shared with the GitLab fake: requests in flight across hosts
}

func (g *pollGitHub) ListPullRequestsPage(ctx context.Context, _, name string, page, perPage int) ([]domain.Listed, error) {
	g.mu.Lock()
	g.calls = append(g.calls, pageCall{repo: name, page: page, at: time.Now()})
	g.inflight++
	g.maxInflight = max(g.maxInflight, g.inflight)
	respond, delay := g.respond, g.delay
	g.mu.Unlock()
	defer g.flight.enter()()
	defer func() {
		g.mu.Lock()
		g.inflight--
		g.mu.Unlock()
	}()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if respond == nil {
		return nil, nil
	}
	return respond(name, page)
}

// firstPageTimes returns when the repo's page 1 was requested, as offsets from base.
func (g *pollGitHub) firstPageTimes(repo string, base time.Time) []time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []time.Duration
	for _, c := range g.calls {
		if c.repo == repo && c.page == 1 {
			out = append(out, c.at.Sub(base))
		}
	}
	return out
}

func (g *pollGitHub) pagesOf(repo string) []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []int
	for _, c := range g.calls {
		if c.repo == repo {
			out = append(out, c.page)
		}
	}
	return out
}

type polled struct {
	repo string
	at   time.Time
}

// storedPull is a PR of the fake store; stats are missing until SetDiffStats fills them.
type storedPull struct {
	id        string
	number    int
	updatedAt time.Time
	stats     domain.DiffStats
}

type pollStore struct {
	mu       sync.Mutex
	upserted map[string]int // repo -> PRs written
	marked   []polled
	pulls    map[string][]storedPull // repo -> stored PRs
	listErr  error
	// UpdateRepoPath: the path updates it was asked for, and what it answers.
	pathUpdates []domain.GitLabProject
	pathUpdated bool
	pathErr     error
}

func (s *pollStore) UpdateRepoPath(_ context.Context, _, _ string, p domain.GitLabProject) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pathUpdates = append(s.pathUpdates, p)
	return s.pathUpdated, s.pathErr
}

func (s *pollStore) ListWithoutDiffStats(_ context.Context, _, repo string, since time.Time, limit int) ([]domain.Pull, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	var pending []storedPull
	for _, p := range s.pulls[repo] {
		if domain.NeedsDiffStats(p.stats) && !p.updatedAt.Before(since) {
			pending = append(pending, p)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].updatedAt.After(pending[j].updatedAt) })
	var out []domain.Pull
	for _, p := range pending[:min(limit, len(pending))] {
		out = append(out, domain.Pull{ID: p.id, Number: p.number})
	}
	return out, nil
}

func (s *pollStore) SetDiffStats(_ context.Context, prID string, stats domain.DiffStats) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for repo, ps := range s.pulls {
		for i := range ps {
			if ps[i].id == prID {
				s.pulls[repo][i].stats = stats
			}
		}
	}
	return nil
}

// filled returns the numbers of the repo's PRs that have stats, ascending.
func (s *pollStore) filled(repo string) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int
	for _, p := range s.pulls[repo] {
		if !domain.NeedsDiffStats(p.stats) {
			out = append(out, p.number)
		}
	}
	sort.Ints(out)
	return out
}

// pollStats answers diff-stat fetches through respond (nil answers with non-zero stats) and
// records the PR numbers asked for.
type pollStats struct {
	mu      sync.Mutex
	asked   []int
	respond func(repo string, number int) (domain.DiffStats, error)
}

func (g *pollStats) GetPullStats(_ context.Context, _, name string, number int) (domain.DiffStats, error) {
	g.mu.Lock()
	g.asked = append(g.asked, number)
	respond := g.respond
	g.mu.Unlock()
	if respond == nil {
		return domain.DiffStats{Additions: 3, Deletions: 1, FilesCount: 2}, nil
	}
	return respond(name, number)
}

func (g *pollStats) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.asked)
}

func (s *pollStore) UpsertListed(_ context.Context, _, repo string, l []domain.Listed) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upserted == nil {
		s.upserted = map[string]int{}
	}
	s.upserted[repo] += len(l)
	return nil
}

func (s *pollStore) MarkPolled(_ context.Context, _, repo string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marked = append(s.marked, polled{repo: repo, at: at})
	return nil
}

func (s *pollStore) markedTimes(repo string, base time.Time) []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []time.Duration
	for _, m := range s.marked {
		if m.repo == repo {
			out = append(out, m.at.Sub(base))
		}
	}
	return out
}

// logSink keeps every log record.
type logSink struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (s *logSink) Enabled(context.Context, slog.Level) bool { return true }
func (s *logSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	return nil
}
func (s *logSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *logSink) WithGroup(string) slog.Handler      { return s }

// warns returns the messages of the WARN records.
func (s *logSink) warns() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.recs {
		if r.Level == slog.LevelWarn {
			out = append(out, r.Message)
		}
	}
	return out
}

type pollRig struct {
	gh        *pollGitHub
	stats     *pollStats
	gl        *pollGitLab
	store     *pollStore
	intervals *pollIntervals
	logs      *logSink
	targets   pollTargets
	opts      app.PollerOptions
	observer  app.CycleObserver
}

func newRig(targets ...domain.PollTarget) *pollRig {
	return &pollRig{
		gh: &pollGitHub{}, stats: &pollStats{}, gl: &pollGitLab{}, store: &pollStore{pathUpdated: true}, logs: &logSink{}, targets: targets,
		intervals: &pollIntervals{set: map[string]time.Duration{}},
	}
}

// start runs the poller inside the current synctest bubble and returns a function that stops it
// and waits for Run to return.
func (r *pollRig) start(t *testing.T) (stop func()) {
	t.Helper()
	p := app.NewPoller(app.PollerDeps{
		Targets: r.targets, Intervals: r.intervals, Pages: r.gh, Stats: r.stats, GitLab: r.gl, Store: r.store, Observer: r.observer, Log: slog.New(r.logs),
	}, r.opts)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// advance moves the fake clock by d and lets every goroutine settle.
func advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func minutes(ns ...int) []time.Duration {
	out := make([]time.Duration, len(ns))
	for i, n := range ns {
		out[i] = time.Duration(n) * time.Minute
	}
	return out
}

func TestPoller_pollsEveryRepoEveryInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"), target("ws", "b"))
		r.intervals.setAll(time.Minute, "ws")
		r.gh.delay = 10 * time.Second
		stop := r.start(t)
		advance(3*time.Minute + 30*time.Second)
		stop()

		for _, repo := range []string{"a", "b"} {
			if got := len(r.gh.firstPageTimes(repo, base)); got < 3 {
				t.Errorf("repo %s polled %d times in 3 min at a 1 min interval, want >= 3", repo, got)
			}
			marks := r.store.markedTimes(repo, base)
			for i := 1; i < len(marks); i++ {
				if marks[i] <= marks[i-1] {
					t.Errorf("repo %s last-polled times = %v, want strictly advancing", repo, marks)
				}
			}
			if len(marks) < 3 {
				t.Errorf("repo %s last-polled marked %d times, want >= 3", repo, len(marks))
			}
		}
		if r.gh.maxInflight != 1 {
			t.Errorf("max concurrent GitHub requests = %d, want 1", r.gh.maxInflight)
		}
	})
}

func TestPoller_aChangedIntervalTakesEffectWithinOneTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"))
		r.intervals.setAll(time.Minute, "ws")
		stop := r.start(t)
		advance(30 * time.Second)
		r.intervals.setAll(2*time.Minute, "ws") // the due time is the last start + the interval of the moment
		advance(5 * time.Minute)
		stop()

		// 0 (due at 2 min once the interval changed: the tick at 1 min skips) -> 2 -> 4.
		if diff := cmp.Diff(minutes(0, 2, 4), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("poll times mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_aLoweredIntervalPollsAtTheNextTickNotAfterTheOldOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"))
		r.intervals.setAll(time.Hour, "ws")
		stop := r.start(t)
		advance(10*time.Minute + 30*time.Second)
		if got := r.gh.firstPageTimes("a", base); len(got) != 1 {
			t.Errorf("polls before the interval is lowered = %v, want only the first", got)
		}
		r.intervals.setAll(time.Minute, "ws")
		advance(2*time.Minute + 30*time.Second) // now 13 min
		stop()

		// The last cycle began at 0; with 1 min the next tick (11 min) is due, then each interval.
		if diff := cmp.Diff(minutes(0, 11, 12, 13), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("poll times mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_aRaisedIntervalDelaysTheNextCycleFromTheLastStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"))
		r.intervals.setAll(time.Minute, "ws")
		stop := r.start(t)
		advance(30 * time.Second)
		r.intervals.setAll(time.Hour, "ws")
		advance(59 * time.Minute) // now 59 min 30 s
		if diff := cmp.Diff(minutes(0), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("poll times before the raised interval ends mismatch (-want +got):\n%s", diff)
		}
		advance(time.Minute) // now 60 min 30 s
		stop()

		if diff := cmp.Diff(minutes(0, 60), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("poll times mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_aRateLimitPauseHoldsWhenALoweredIntervalWouldBeDueEarlier(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"))
		r.intervals.setAll(5*time.Minute, "ws")
		var first sync.Once
		r.gh.respond = func(string, int) ([]domain.Listed, error) {
			var err error
			first.Do(func() {
				err = &app.GitHubError{Status: 403, RateLimited: true, RateLimitReset: base.Add(10 * time.Minute)}
			})
			return nil, err
		}
		stop := r.start(t)
		advance(2 * time.Minute)
		r.intervals.setAll(time.Minute, "ws") // due again at 1 min, but GitHub says wait until 10 (the resume time wins)
		advance(7*time.Minute + 30*time.Second)
		if diff := cmp.Diff(minutes(0), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("poll times during the pause mismatch (-want +got):\n%s", diff)
		}
		advance(3 * time.Minute) // now 12 min 30 s
		stop()

		if diff := cmp.Diff(minutes(0, 10, 11, 12), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("poll times mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_everyWorkspaceHasItsOwnDueTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws1", "a"), target("ws2", "b"))
		r.intervals.setAll(time.Minute, "ws1")
		r.intervals.setAll(3*time.Minute, "ws2")
		stop := r.start(t)
		advance(3*time.Minute + 30*time.Second)
		stop()

		if diff := cmp.Diff(minutes(0, 1, 2, 3), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("ws1 poll times mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(minutes(0, 3), r.gh.firstPageTimes("b", base)); diff != "" {
			t.Errorf("ws2 poll times mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_unreadableIntervalFallsBackToFiveMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"))
		r.intervals.err = errors.New("settings are down")
		stop := r.start(t)
		advance(10*time.Minute + 30*time.Second)
		stop()

		if diff := cmp.Diff(minutes(0, 5, 10), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("poll times mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_aCycleLongerThanTheIntervalDelaysTheNextOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"), target("ws", "b"))
		r.intervals.setAll(time.Minute, "ws")
		r.gh.delay = 50 * time.Second // a cycle takes 100 s
		stop := r.start(t)
		advance(4 * time.Minute)
		stop()

		if r.gh.maxInflight != 1 {
			t.Errorf("max concurrent GitHub requests = %d, want 1", r.gh.maxInflight)
		}
		got := r.gh.firstPageTimes("a", base)
		for i := 1; i < len(got); i++ {
			if gap := got[i] - got[i-1]; gap < 100*time.Second {
				t.Errorf("cycles of repo a started %v apart (%v), want each to start after the previous ended (>= 100s)", gap, got)
			}
		}
	})
}

// recent lists n PRs updated just before now.
func recent(n int) []domain.Listed {
	out := make([]domain.Listed, n)
	for i := range out {
		out[i] = domain.Listed{Number: i + 1, UpdatedAt: ptr(time.Now().Add(-time.Hour))}
	}
	return out
}

// fresh lists n PRs updated a second ago.
func fresh(n int) []domain.Listed {
	out := make([]domain.Listed, n)
	for i := range out {
		out[i] = domain.Listed{Number: i + 1, UpdatedAt: ptr(time.Now().Add(-time.Second))}
	}
	return out
}

func TestPoller_firstPollStopsAtTheHorizonOrAfterTenPages(t *testing.T) {
	t.Run("ten pages of twelve", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(target("ws", "a"))
			r.opts = app.PollerOptions{PageSize: 2}
			r.gh.respond = func(string, int) ([]domain.Listed, error) { return recent(2), nil } // endless full pages
			stop := r.start(t)
			advance(time.Second)
			stop()

			if diff := cmp.Diff([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, r.gh.pagesOf("a")); diff != "" {
				t.Errorf("requested pages mismatch (-want +got):\n%s", diff)
			}
			if r.store.upserted["a"] != 20 {
				t.Errorf("PRs stored = %d, want 20 (every page is stored)", r.store.upserted["a"])
			}
		})
	})
	t.Run("62 day horizon", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(target("ws", "a"))
			r.opts = app.PollerOptions{PageSize: 2}
			r.gh.respond = func(_ string, page int) ([]domain.Listed, error) {
				if page == 3 { // the first PR older than 62 days
					return []domain.Listed{
						{Number: 90, UpdatedAt: ptr(time.Now().Add(-61 * 24 * time.Hour))},
						{Number: 91, UpdatedAt: ptr(time.Now().Add(-63 * 24 * time.Hour))},
					}, nil
				}
				return recent(2), nil
			}
			stop := r.start(t)
			advance(time.Second)
			stop()

			if diff := cmp.Diff([]int{1, 2, 3}, r.gh.pagesOf("a")); diff != "" {
				t.Errorf("requested pages mismatch (-want +got):\n%s", diff)
			}
		})
	})
	t.Run("short page ends it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := newRig(target("ws", "a"))
			r.opts = app.PollerOptions{PageSize: 2}
			r.gh.respond = func(_ string, page int) ([]domain.Listed, error) {
				if page == 2 {
					return recent(1), nil
				}
				return recent(2), nil
			}
			stop := r.start(t)
			advance(time.Second)
			stop()

			if diff := cmp.Diff([]int{1, 2}, r.gh.pagesOf("a")); diff != "" {
				t.Errorf("requested pages mismatch (-want +got):\n%s", diff)
			}
		})
	})
}

func TestPoller_laterPollStopsAtTheFirstPRNotUpdatedSincePreviousPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"))
		r.opts = app.PollerOptions{PageSize: 2}
		r.intervals.setAll(time.Minute, "ws")
		r.gh.respond = func(_ string, page int) ([]domain.Listed, error) {
			if time.Since(base) < time.Minute { // first poll: nothing but fresh pages
				return recent(2), nil
			}
			if page == 2 { // before the previous poll started
				return []domain.Listed{
					{Number: 5, UpdatedAt: ptr(time.Now().Add(-10 * time.Second))},
					{Number: 6, UpdatedAt: ptr(base.Add(-time.Hour))},
				}, nil
			}
			return []domain.Listed{
				{Number: 7, UpdatedAt: ptr(time.Now().Add(-5 * time.Second))},
				{Number: 8, UpdatedAt: ptr(time.Now().Add(-6 * time.Second))},
			}, nil
		}
		stop := r.start(t)
		advance(time.Minute + 30*time.Second)
		stop()

		want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 1, 2}
		if diff := cmp.Diff(want, r.gh.pagesOf("a")); diff != "" {
			t.Errorf("requested pages mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_noTokenSkipsEverythingWithOneWarnPerCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"), target("ws", "b"))
		r.intervals.setAll(time.Minute, "ws")
		r.gh.respond = func(string, int) ([]domain.Listed, error) { return nil, app.ErrNoToken }
		stop := r.start(t)
		advance(2*time.Minute + 30*time.Second) // cycles at 0, 1 and 2 min
		stop()

		if diff := cmp.Diff(minutes(0, 1, 2), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("repo a request times mismatch (-want +got):\n%s", diff)
		}
		if got := r.gh.firstPageTimes("b", base); len(got) != 0 {
			t.Errorf("repo b requested %d times, want 0 (the cycle is skipped after the missing token)", len(got))
		}
		if got := r.logs.warns(); len(got) != 3 {
			t.Errorf("WARN lines = %q, want one per cycle (3)", got)
		}
		if len(r.store.marked) != 0 {
			t.Errorf("repos marked polled = %v, want none", r.store.marked)
		}
	})
}

func TestPoller_rateLimitPausesUntilTheLaterOfResetAndNextDue(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		limit    func(base time.Time) *app.GitHubError
		wantAt   time.Duration // when repo a is requested again
	}{
		{"reset after due", time.Minute, func(base time.Time) *app.GitHubError {
			return &app.GitHubError{Status: 403, RateLimited: true, RateLimitReset: base.Add(10 * time.Minute)}
		}, 10 * time.Minute},
		{"reset before due", 5 * time.Minute, func(base time.Time) *app.GitHubError {
			return &app.GitHubError{Status: 403, RateLimited: true, RateLimitReset: base.Add(2 * time.Minute)}
		}, 5 * time.Minute},
		{"secondary 429 without reset", 3 * time.Minute, func(time.Time) *app.GitHubError {
			return &app.GitHubError{Status: 429}
		}, 3 * time.Minute},
		{"secondary 403 with Retry-After after due", time.Minute, func(base time.Time) *app.GitHubError {
			return &app.GitHubError{Status: 403, RateLimited: true, RetryAfter: base.Add(7 * time.Minute)}
		}, 7 * time.Minute},
		{"secondary 403 with Retry-After before due", 5 * time.Minute, func(base time.Time) *app.GitHubError {
			return &app.GitHubError{Status: 403, RateLimited: true, RetryAfter: base.Add(time.Minute)}
		}, 5 * time.Minute},
		{"429 with Retry-After", time.Minute, func(base time.Time) *app.GitHubError {
			return &app.GitHubError{Status: 429, RetryAfter: base.Add(4 * time.Minute)}
		}, 4 * time.Minute},
		{"Retry-After wins over the primary reset", time.Minute, func(base time.Time) *app.GitHubError {
			return &app.GitHubError{Status: 403, RateLimited: true, RateLimitReset: base.Add(10 * time.Minute), RetryAfter: base.Add(3 * time.Minute)}
		}, 3 * time.Minute},
		{"Retry-After is capped at two hours", time.Minute, func(base time.Time) *app.GitHubError {
			return &app.GitHubError{Status: 403, RateLimited: true, RetryAfter: base.Add(1000 * time.Hour)}
		}, domain.MaxRateLimitWait},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := time.Now()
				r := newRig(target("ws", "a"), target("ws", "b"))
				r.intervals.setAll(tc.interval, "ws")
				var first sync.Once
				r.gh.respond = func(repo string, _ int) ([]domain.Listed, error) {
					var err error
					first.Do(func() { err = tc.limit(base) })
					if err != nil {
						return nil, err
					}
					return nil, nil
				}
				stop := r.start(t)
				advance(tc.wantAt - time.Second)
				if got := r.gh.firstPageTimes("a", base); len(got) != 1 {
					t.Errorf("repo a requests before the pause ends = %v, want only the limited one", got)
				}
				if got := r.gh.firstPageTimes("b", base); len(got) != 0 {
					t.Errorf("repo b requests during the pause = %v, want none (the cycle ended)", got)
				}
				advance(31 * time.Second)
				stop()

				if diff := cmp.Diff([]time.Duration{0, tc.wantAt}, r.gh.firstPageTimes("a", base)); diff != "" {
					t.Errorf("repo a request times mismatch (-want +got):\n%s", diff)
				}
				if got := r.gh.firstPageTimes("b", base); len(got) == 0 || got[0] != tc.wantAt {
					t.Errorf("repo b request times = %v, want the first at %v", got, tc.wantAt)
				}
				if got := r.logs.warns(); len(got) != 1 {
					t.Errorf("WARN lines = %q, want exactly the pause notice", got)
				}
			})
		})
	}
}

func TestPoller_aFailedRepoIsLoggedAndTheCycleGoesOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"), target("ws", "b"))
		r.intervals.setAll(time.Hour, "ws")
		r.gh.respond = func(repo string, _ int) ([]domain.Listed, error) {
			if repo == "a" {
				return nil, &app.GitHubError{Status: 500, Message: "boom"}
			}
			return recent(1), nil
		}
		stop := r.start(t)
		advance(time.Second)
		stop()

		if got := r.gh.firstPageTimes("b", base); len(got) != 1 {
			t.Errorf("repo b requests = %v, want it polled once after repo a failed", got)
		}
		if got := r.store.markedTimes("a", base); len(got) != 0 {
			t.Errorf("repo a marked polled at %v, want never", got)
		}
		if got := r.store.markedTimes("b", base); len(got) != 1 {
			t.Errorf("repo b marked polled at %v, want once", got)
		}
		if got := r.logs.warns(); len(got) != 1 {
			t.Errorf("WARN lines = %q, want one for repo a", got)
		}
	})
}

func TestPoller_aFailedPollDoesNotMoveTheCutoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"))
		r.opts = app.PollerOptions{PageSize: 2}
		r.intervals.setAll(time.Minute, "ws")
		r.gh.respond = func(_ string, page int) ([]domain.Listed, error) {
			switch d := time.Since(base); {
			case d < time.Minute: // success at 0: cutoff becomes 0
				return fresh(1), nil
			case d < 2*time.Minute: // 1 min: page 2 fails
				if page == 2 {
					return nil, &app.GitHubError{Status: 500}
				}
				return fresh(2), nil
			default: // 2 min: the cutoff is still 0, so a PR updated at 0:30 is not old
				if page == 2 {
					return []domain.Listed{{Number: 3, UpdatedAt: ptr(base.Add(30 * time.Second))}, {Number: 4, UpdatedAt: ptr(base.Add(-time.Hour))}}, nil
				}
				return fresh(2), nil
			}
		}
		stop := r.start(t)
		advance(2*time.Minute + 30*time.Second)
		stop()

		want := []int{1, 1, 2, 1, 2}
		if diff := cmp.Diff(want, r.gh.pagesOf("a")); diff != "" {
			t.Errorf("requested pages mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(minutes(0, 2), r.store.markedTimes("a", base)); diff != "" {
			t.Errorf("last-polled times mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_stopsWithinFiveSecondsOfCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(target("ws", "a"))
		r.gh.delay = time.Hour // a request that only the context can end
		p := app.NewPoller(app.PollerDeps{
			Targets: r.targets, Intervals: r.intervals, Pages: r.gh, Stats: r.stats, Store: r.store, Log: slog.New(r.logs),
		}, r.opts)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.Run(ctx)
		}()
		advance(10 * time.Second) // Run is inside the request
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return within 5 s of the cancel")
		}
		if got := r.logs.warns(); len(got) != 0 {
			t.Errorf("WARN lines after a clean shutdown = %q, want none", got)
		}
		if len(r.store.marked) != 0 {
			t.Errorf("repos marked polled = %v, want none for the cancelled poll", r.store.marked)
		}
	})
}

func TestNewPoller_panicsWithoutPorts(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPoller(zero deps) did not panic")
		}
	}()
	app.NewPoller(app.PollerDeps{}, app.PollerOptions{})
}

func TestPoller_aRateLimitPausesEveryWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws1", "a"), target("ws2", "b"))
		r.intervals.setAll(time.Minute, "ws1", "ws2")
		var first sync.Once
		r.gh.respond = func(string, int) ([]domain.Listed, error) {
			var err error
			first.Do(func() {
				err = &app.GitHubError{Status: 403, RateLimited: true, RateLimitReset: base.Add(5 * time.Minute)}
			})
			return nil, err
		}
		stop := r.start(t)
		advance(5*time.Minute - time.Second)
		if got := r.gh.firstPageTimes("b", base); len(got) != 0 {
			t.Errorf("workspace 2 requests during the pause = %v, want none (the token is shared)", got)
		}
		advance(11 * time.Second)
		stop()

		if got := r.gh.firstPageTimes("b", base); len(got) == 0 || got[0] != 5*time.Minute {
			t.Errorf("workspace 2 request times = %v, want the first at 5m", got)
		}
	})
}

// pendingPulls stores n PRs without stats, PR k updated k hours before now (so PR 1 is the newest).
func pendingPulls(repo string, n int) []storedPull {
	out := make([]storedPull, n)
	for i := range out {
		k := i + 1
		out[i] = storedPull{id: fmt.Sprintf("%s-%d", repo, k), number: k, updatedAt: time.Now().Add(-time.Duration(k) * time.Hour)}
	}
	return out
}

func TestPoller_backfillsTwentyNewestStatlessPRsPerCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(target("ws", "a"))
		r.intervals.setAll(time.Minute, "ws")
		r.store.pulls = map[string][]storedPull{"a": pendingPulls("a", 25)}
		var failed sync.Once
		r.stats.respond = func(_ string, number int) (domain.DiffStats, error) {
			if number == 7 {
				var err error
				failed.Do(func() { err = &app.GitHubError{Status: 500, Message: "boom"} })
				if err != nil {
					return domain.DiffStats{}, err
				}
			}
			return domain.DiffStats{Additions: 3, Deletions: 1, FilesCount: 2}, nil
		}
		stop := r.start(t)
		advance(30 * time.Second) // cycle 1 only

		var wantFirst []int // the 20 newest, but PR 7, whose fetch fails
		for n := 1; n <= 20; n++ {
			if n != 7 {
				wantFirst = append(wantFirst, n)
			}
		}
		if diff := cmp.Diff(wantFirst, r.store.filled("a")); diff != "" {
			t.Errorf("PRs with stats after cycle 1 mismatch (-want +got):\n%s", diff)
		}
		if got, want := r.stats.count(), 20; got != want {
			t.Errorf("stats fetches in cycle 1 = %d, want %d (the 20 newest)", got, want)
		}
		if got := len(r.gh.pagesOf("a")) + r.stats.count(); got > 30 {
			t.Errorf("GitHub requests in cycle 1 = %d, want <= 30", got)
		}
		if got := r.logs.warns(); len(got) != 1 {
			t.Errorf("WARN lines after cycle 1 = %q, want exactly the failed stats fetch of PR 7", got)
		}

		advance(time.Minute) // cycle 2: PR 7 (failed once) and the five oldest
		stop()
		if diff := cmp.Diff([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25}, r.store.filled("a")); diff != "" {
			t.Errorf("PRs with stats after cycle 2 mismatch (-want +got):\n%s", diff)
		}
		if got, want := r.stats.count(), 26; got != want {
			t.Errorf("stats fetches in two cycles = %d, want %d (20, then PR 7 and 21-25)", got, want)
		}
	})
}

func TestPoller_backfillSkipsPRsOlderThanTheHorizonOrWithStats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(target("ws", "a"))
		r.intervals.setAll(time.Hour, "ws")
		now := time.Now()
		r.store.pulls = map[string][]storedPull{"a": {
			{id: "a-1", number: 1, updatedAt: now.Add(-time.Hour)},
			{id: "a-2", number: 2, updatedAt: now.Add(-63 * 24 * time.Hour)},                              // too old
			{id: "a-3", number: 3, updatedAt: now.Add(-time.Hour), stats: domain.DiffStats{Additions: 1}}, // has stats
		}}
		stop := r.start(t)
		advance(time.Second)
		stop()

		r.stats.mu.Lock()
		defer r.stats.mu.Unlock()
		if diff := cmp.Diff([]int{1}, r.stats.asked); diff != "" {
			t.Errorf("PRs asked for stats mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPoller_aRateLimitInTheStatsEndsTheCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"), target("ws", "b"))
		r.intervals.setAll(time.Minute, "ws")
		r.store.pulls = map[string][]storedPull{"a": pendingPulls("a", 3)}
		var first sync.Once
		r.stats.respond = func(string, int) (domain.DiffStats, error) {
			var err error
			first.Do(func() {
				err = &app.GitHubError{Status: 403, RateLimited: true, RetryAfter: base.Add(5 * time.Minute)}
			})
			if err != nil {
				return domain.DiffStats{}, err
			}
			return domain.DiffStats{Additions: 1}, nil
		}
		stop := r.start(t)
		advance(5*time.Minute - time.Second)
		if got := r.stats.count(); got != 1 {
			t.Errorf("stats fetches during the pause = %d, want only the limited one", got)
		}
		if got := r.gh.firstPageTimes("b", base); len(got) != 0 {
			t.Errorf("repo b page requests during the pause = %v, want none (the cycle ended)", got)
		}
		advance(11 * time.Second)
		stop()

		if got := r.gh.firstPageTimes("b", base); len(got) == 0 || got[0] != 5*time.Minute {
			t.Errorf("repo b request times = %v, want the first at 5m", got)
		}
	})
}

func TestPoller_aFailedStatsListingIsLoggedAndTheCycleGoesOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"), target("ws", "b"))
		r.intervals.setAll(time.Hour, "ws")
		r.store.listErr = errors.New("db is down")
		stop := r.start(t)
		advance(time.Second)
		stop()

		if got := r.gh.firstPageTimes("b", base); len(got) != 1 {
			t.Errorf("repo b page requests = %v, want it polled once", got)
		}
		if got := r.logs.warns(); len(got) != 2 {
			t.Errorf("WARN lines = %q, want one per repo for the failed listing", got)
		}
	})
}

// cycleLog is a CycleObserver that records its calls; onCall, when set, runs inside AfterCycle.
type cycleLog struct {
	mu     sync.Mutex
	calls  []observed
	onCall func(ctx context.Context, ws string)
}

type observed struct {
	ws string
	at time.Time
}

func (c *cycleLog) AfterCycle(ctx context.Context, ws string) {
	c.mu.Lock()
	c.calls = append(c.calls, observed{ws: ws, at: time.Now()})
	onCall := c.onCall
	c.mu.Unlock()
	if onCall != nil {
		onCall(ctx, ws)
	}
}

func (c *cycleLog) workspaces() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, o := range c.calls {
		out = append(out, o.ws)
	}
	return out
}

func TestPoller_observerIsToldAfterEachSuccessfulCycleOfAWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("w1", "a"), target("w1", "b"), target("w2", "c"))
		r.intervals.setAll(time.Minute, "w1", "w2")
		obs := &cycleLog{}
		var markedAtCall []int
		obs.onCall = func(context.Context, string) {
			r.store.mu.Lock()
			markedAtCall = append(markedAtCall, len(r.store.marked))
			r.store.mu.Unlock()
		}
		r.observer = obs
		stop := r.start(t)
		advance(time.Minute + 30*time.Second) // cycles at 0 and 1 min
		stop()

		if diff := cmp.Diff([]string{"w1", "w2", "w1", "w2"}, obs.workspaces()); diff != "" {
			t.Errorf("AfterCycle workspaces mismatch (-want +got):\n%s", diff)
		}
		// w1 has 2 repos, w2 has 1: each call comes after its workspace's repos were marked polled.
		if diff := cmp.Diff([]int{2, 3, 5, 6}, markedAtCall); diff != "" {
			t.Errorf("repos marked polled at each AfterCycle mismatch (-want +got):\n%s", diff)
		}
		if got := obs.calls[0].at.Sub(base); got != 0 {
			t.Errorf("first AfterCycle at %v, want at once after the first cycle", got)
		}
	})
}

func TestPoller_observerIsNotToldWithoutAToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(target("ws", "a"), target("ws", "b"))
		r.intervals.setAll(time.Minute, "ws")
		r.gh.respond = func(string, int) ([]domain.Listed, error) { return nil, app.ErrNoToken }
		obs := &cycleLog{}
		r.observer = obs
		stop := r.start(t)
		advance(2*time.Minute + 30*time.Second)
		stop()

		if got := obs.workspaces(); len(got) != 0 {
			t.Errorf("AfterCycle calls without a token = %v, want none", got)
		}
	})
}

func TestPoller_observerAfterARateLimit(t *testing.T) {
	limited := func() error { return &app.GitHubError{Status: 429} }
	tests := []struct {
		name       string
		failOnRepo string // the page request of this repo hits the limit
		failStats  bool   // the stats fetch of repo a hits the limit
		want       []string
	}{
		{"limit on the first repo", "a", false, nil},
		{"limit on the second repo after one was polled", "b", false, []string{"ws"}},
		{"limit in the stats of the first polled repo", "", true, []string{"ws"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(target("ws", "a"), target("ws", "b"))
				r.intervals.setAll(time.Hour, "ws")
				r.gh.respond = func(repo string, _ int) ([]domain.Listed, error) {
					if repo == tc.failOnRepo {
						return nil, limited()
					}
					return nil, nil
				}
				if tc.failStats {
					r.store.pulls = map[string][]storedPull{"a": pendingPulls("a", 1)}
					r.stats.respond = func(string, int) (domain.DiffStats, error) { return domain.DiffStats{}, limited() }
				}
				obs := &cycleLog{}
				r.observer = obs
				stop := r.start(t)
				advance(time.Second)
				stop()

				if diff := cmp.Diff(tc.want, obs.workspaces()); diff != "" {
					t.Errorf("AfterCycle workspaces mismatch (-want +got):\n%s", diff)
				}
			})
		})
	}
}

func TestPoller_observerNeedsOneRepoPolledDespiteFailures(t *testing.T) {
	tests := []struct {
		name string
		fail map[string]bool // repos whose page request fails
		want []string
	}{
		{"every repo fails", map[string]bool{"a": true, "b": true}, nil},
		{"one repo fails, the other is polled", map[string]bool{"a": true}, []string{"ws"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRig(target("ws", "a"), target("ws", "b"))
				r.intervals.setAll(time.Hour, "ws")
				r.gh.respond = func(repo string, _ int) ([]domain.Listed, error) {
					if tc.fail[repo] {
						return nil, errors.New("boom")
					}
					return nil, nil
				}
				obs := &cycleLog{}
				r.observer = obs
				stop := r.start(t)
				advance(time.Second)
				stop()

				if diff := cmp.Diff(tc.want, obs.workspaces()); diff != "" {
					t.Errorf("AfterCycle workspaces mismatch (-want +got):\n%s", diff)
				}
			})
		})
	}
}

func TestPoller_observerIsNotToldOfACycleCancelledAfterARepoWasPolled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(target("ws", "a"), target("ws", "b"))
		r.intervals.setAll(time.Hour, "ws")
		obs := &cycleLog{}
		r.observer = obs
		p := app.NewPoller(app.PollerDeps{
			Targets: r.targets, Intervals: r.intervals, Pages: r.gh, Stats: r.stats, Store: r.store,
			Observer: obs, Log: slog.New(r.logs),
		}, r.opts)
		ctx, cancel := context.WithCancel(t.Context())
		// Repo b's request is the one in flight when the shutdown comes; repo a was polled before.
		r.gh.respond = func(repo string, _ int) ([]domain.Listed, error) {
			if repo == "b" {
				cancel()
			}
			return nil, nil
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.Run(ctx)
		}()
		advance(time.Second)
		<-done

		if got := obs.workspaces(); len(got) != 0 {
			t.Errorf("AfterCycle calls for a cancelled cycle = %v, want none", got)
		}
	})
}

func TestPoller_aPanickingObserverIsRecoveredAndPollingGoesOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		r := newRig(target("ws", "a"))
		r.intervals.setAll(time.Minute, "ws")
		obs := &cycleLog{onCall: func(context.Context, string) { panic("observer bug") }}
		r.observer = obs
		stop := r.start(t)
		advance(2*time.Minute + 30*time.Second) // cycles at 0, 1 and 2 min
		stop()

		if got := len(obs.workspaces()); got != 3 {
			t.Errorf("AfterCycle calls = %d, want 3 (the poller survives the panics)", got)
		}
		if diff := cmp.Diff(minutes(0, 1, 2), r.gh.firstPageTimes("a", base)); diff != "" {
			t.Errorf("repo a request times mismatch (-want +got):\n%s", diff)
		}
		r.logs.mu.Lock()
		defer r.logs.mu.Unlock()
		var errs int
		for _, rec := range r.logs.recs {
			if rec.Level == slog.LevelError {
				errs++
			}
		}
		if errs != 3 {
			t.Errorf("ERROR lines = %d, want one per recovered panic (3)", errs)
		}
	})
}

func TestPoller_stopsWithinFiveSecondsOfCancelInsideTheObserver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(target("ws", "a"))
		r.intervals.setAll(time.Hour, "ws")
		obs := &cycleLog{}
		inHook := make(chan struct{})
		obs.onCall = func(ctx context.Context, _ string) {
			close(inHook)
			<-ctx.Done() // an observer that honours its ctx, as the port requires
		}
		r.observer = obs
		stop := r.start(t)
		<-inHook
		synctest.Wait()
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			stop()
		}()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return within 5 s of the cancel while the observer ran")
		}
	})
}
