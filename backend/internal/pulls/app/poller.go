package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"dev-digest-go/backend/internal/pulls/domain"
)

// Poller defaults (leaderboard spec FR3, FR4, NFR3).
const (
	DefaultPollMaxPages = 10 // requests per repo per cycle
	DefaultPollPageSize = 50
	DefaultPollTick     = time.Minute
	// PollStatsPerRepo caps the diff-stat fetches per repo per cycle (FR24, NFR3: 10 + 20 <= 30 requests).
	PollStatsPerRepo = 20
	maxPollPageSize  = 100 // GitHub's per_page ceiling

	statusTooManyRequests = 429
)

// PollerOptions tunes the background poll; a zero field takes its default.
type PollerOptions struct {
	// Horizon is how far back the first poll of a repo in this process looks, and how old a PR may be
	// to get its diff stats filled (default domain.PollHorizon, 62 d).
	Horizon time.Duration
	// MaxPages caps the pages one repo poll reads (default 10).
	MaxPages int
	// PageSize is the PRs per page (default 50, at most 100).
	PageSize int
	// Tick is how often the poller checks which workspaces are due (default 1 min).
	Tick time.Duration
}

// PollerDeps are the ports the poller needs. All but Log and Now are required.
type PollerDeps struct {
	Targets   PollTargets
	Intervals IntervalSource
	// Pages must be a GitHub client of its own that never retries, so a repo poll stays within
	// MaxPages requests.
	Pages PageLister
	// Stats must be a GitHub client of its own that never retries; one fetch is one request.
	Stats StatsFetcher
	// GitLab polls the GitLab repositories (spec FR27). Its instance must not retry either
	// (gitlab.Client.WithRetry(retry.Options{}), NFR5). Nil reads every GitLab repository as not
	// connected: it is skipped, with one WARN per host key per cycle.
	GitLab GitLab
	Store  PollStore
	// Observer is told when a workspace's cycle polled successfully (nil = off).
	Observer CycleObserver
	Log      *slog.Logger // nil discards
	Now      func() time.Time
}

// Poller keeps every repository's PRs fresh in the background. Each workspace has its own due
// time: the start of its last cycle plus its CURRENT Settings polling interval, worked out again on
// every tick, so a changed interval applies within one tick (spec D17). A rate limit pauses only
// the host that reported it (domain.HostPauses, keyed by Repo.PauseKey): GitHub and every GitLab
// instance keep polling when another one is paused (spec FR27).
// Repositories are polled one at a time. Run must not be called concurrently with itself.
type Poller struct {
	targets   PollTargets
	intervals IntervalSource
	pages     PageLister
	stats     StatsFetcher
	gitlab    GitLab
	store     PollStore
	observer  CycleObserver
	log       *slog.Logger
	now       func() time.Time
	opts      PollerOptions

	// State below belongs to the goroutine that runs Run.
	lastStart map[string]time.Time // workspace id -> start of its last cycle
	cutoffs   map[string]time.Time // repo id -> start of its last successful background poll
	pauses    *domain.HostPauses   // pause key -> rate-limit resume time
}

// NewPoller builds a Poller. It panics on a missing required port (a wiring bug).
func NewPoller(deps PollerDeps, opts PollerOptions) *Poller {
	if deps.Targets == nil || deps.Intervals == nil || deps.Pages == nil || deps.Stats == nil || deps.Store == nil {
		panic("pulls: NewPoller needs Targets, Intervals, Pages, Stats and Store")
	}
	if opts.Horizon <= 0 {
		opts.Horizon = domain.PollHorizon
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = DefaultPollMaxPages
	}
	if opts.PageSize <= 0 {
		opts.PageSize = DefaultPollPageSize
	}
	opts.PageSize = min(opts.PageSize, maxPollPageSize)
	if opts.Tick <= 0 {
		opts.Tick = DefaultPollTick
	}
	log := deps.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	gl := deps.GitLab
	if gl == nil {
		gl = noGitLab{}
	}
	return &Poller{
		targets: deps.Targets, intervals: deps.Intervals, pages: deps.Pages, stats: deps.Stats, gitlab: gl, store: deps.Store,
		observer: deps.Observer, log: log, now: now, opts: opts,
		lastStart: map[string]time.Time{}, cutoffs: map[string]time.Time{}, pauses: domain.NewHostPauses(),
	}
}

// Run polls until ctx is done: a first round at once, then one every Tick. It returns promptly
// after ctx is cancelled (every call it makes takes ctx).
func (p *Poller) Run(ctx context.Context) {
	tick := time.NewTicker(p.opts.Tick)
	defer tick.Stop()
	for ctx.Err() == nil {
		p.round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// round runs the cycle of every workspace that is due, one workspace after the other. A paused host
// does not stop it: only that host's repositories are skipped inside the cycle.
func (p *Poller) round(ctx context.Context) {
	targets, err := p.targets.ListPollTargets(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.log.WarnContext(ctx, "background poll: listing repositories failed", "err", err)
		}
		return
	}
	p.forget(targets)
	p.pauses.Prune(p.now())
	for _, group := range groupByWorkspace(targets) {
		if ctx.Err() != nil {
			return
		}
		if !domain.IsDue(p.now(), p.dueAt(ctx, group.workspaceID), p.opts.Tick) {
			continue
		}
		p.cycle(ctx, group)
	}
}

// dueAt is when the workspace's next cycle is due: its last cycle start plus the interval it has
// now (spec D17). A workspace never polled by this process is due at once (the zero time), without
// reading its interval.
func (p *Poller) dueAt(ctx context.Context, workspaceID string) time.Time {
	last, ok := p.lastStart[workspaceID]
	if !ok {
		return time.Time{}
	}
	return domain.DueAt(last, p.interval(ctx, workspaceID))
}

// cycle polls one workspace and, when the cycle polled successfully, tells the observer.
func (p *Poller) cycle(ctx context.Context, g workspaceTargets) {
	if p.pollRepos(ctx, g) && p.observer != nil && ctx.Err() == nil {
		p.notify(ctx, g.workspaceID)
	}
}

// notify calls the observer inside the poller goroutine. A panic in it is recovered and logged: it
// must not take the server down, nor end the poller. The observer gets the poller's ctx and must
// return promptly once it is cancelled.
func (p *Poller) notify(ctx context.Context, workspaceID string) {
	defer func() {
		if r := recover(); r != nil {
			p.log.ErrorContext(ctx, "background poll: the cycle observer panicked",
				"workspace_id", workspaceID, "panic", fmt.Sprint(r))
		}
	}()
	p.observer.AfterCycle(ctx, workspaceID)
}

// pollRepos polls every repo of one workspace: its PR pages, then the diff stats of stored PRs that
// have none. A failed repo is logged and skipped. A host that is paused, has no token, or is not
// connected loses only its own repos for the rest of the cycle (one WARN per host and cycle); the
// other hosts' repos are polled (spec FR27). It reports whether the cycle polled successfully: at
// least one repo had its pages read and was marked polled, and the ctx was not cancelled (a rate
// limit after a polled repo does not count against it). A cycle that found every repo's host
// paused did not run: it neither counts as the workspace's last cycle nor tells the observer.
func (p *Poller) pollRepos(ctx context.Context, g workspaceTargets) bool {
	polled, attempted := 0, 0
	start := p.now()
	skipped := map[string]bool{} // pause key -> left out for the rest of this cycle

	for _, repo := range g.repos {
		if ctx.Err() != nil {
			return false
		}
		key := repo.PauseKey()
		if skipped[key] || p.pauses.Paused(key, p.now()) {
			continue
		}
		if attempted++; attempted == 1 {
			p.lastStart[g.workspaceID] = start
		}
		pagesRead, err := p.syncRepo(ctx, g.workspaceID, repo)
		if pagesRead {
			polled++
		}
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		if p.hostBlocked(ctx, g.workspaceID, repo, start, err) {
			skipped[key] = true
			continue
		}
		p.log.WarnContext(ctx, "background poll of a repository failed",
			"workspace_id", g.workspaceID, "repo_id", repo.ID, "repo", repo.Owner+"/"+repo.Name,
			"provider", providerOf(repo), "err", err)
	}
	return polled > 0
}

// hostBlocked handles an error that concerns the repo's whole host: no token, not connected, or a
// rate limit (which pauses the host). It logs the one WARN and reports true; any other error is
// the repo's own and reports false.
func (p *Poller) hostBlocked(ctx context.Context, workspaceID string, repo domain.Repo, start time.Time, err error) bool {
	var noToken *NoTokenError
	switch {
	case errors.Is(err, ErrNoToken):
		p.log.WarnContext(ctx, "background poll skipped: no GitHub token is configured", "workspace_id", workspaceID)
		return true
	case errors.Is(err, ErrNotConnected):
		p.log.WarnContext(ctx, "background poll skipped: no GitLab connection for the host",
			"workspace_id", workspaceID, "host", repo.Host)
		return true
	case errors.As(err, &noToken):
		p.log.WarnContext(ctx, "background poll skipped: the GitLab connection has no token",
			"workspace_id", workspaceID, "host", repo.Host, "connection", noToken.Conn)
		return true
	}
	reset, limited := rateLimit(err)
	if !limited {
		return false
	}
	// Without a reset time (A4) the host waits for the workspace's next due time.
	resume := domain.ResumeAfterRateLimit(start, domain.DueAt(start, p.interval(ctx, workspaceID)), reset)
	p.pauses.Pause(repo.PauseKey(), resume)
	p.log.WarnContext(ctx, "background poll paused: rate limit reached",
		"workspace_id", workspaceID, "repo_id", repo.ID, "provider", providerOf(repo), "host", hostOf(repo), "resume_at", resume)
	return true
}

// syncRepo reads the repo's PR pages and then backfills its diff stats. pagesRead reports that the
// pages were read and the repo marked polled, even when the backfill then failed with err.
func (p *Poller) syncRepo(ctx context.Context, workspaceID string, repo domain.Repo) (pagesRead bool, err error) {
	if err := p.pollRepo(ctx, workspaceID, repo); err != nil {
		return false, err
	}
	return true, p.backfillStats(ctx, workspaceID, repo)
}

func providerOf(repo domain.Repo) string {
	if repo.IsGitLab() {
		return domain.ProviderGitLab
	}
	return domain.ProviderGitHub
}

func hostOf(repo domain.Repo) string {
	if repo.IsGitLab() {
		return repo.Host
	}
	return "github.com"
}

// interval reads the workspace's polling interval; an error reads as the default.
func (p *Poller) interval(ctx context.Context, workspaceID string) time.Duration {
	d, err := p.intervals.Interval(ctx, workspaceID)
	if err != nil {
		if ctx.Err() == nil {
			p.log.WarnContext(ctx, "background poll: reading the polling interval failed; using the default",
				"workspace_id", workspaceID, "err", err)
		}
		d = 0
	}
	return domain.ClampPollInterval(d)
}

// pollRepo pages through the repo's PRs newest-update-first, upserting each page, until a PR not
// updated since the cutoff, a short page or MaxPages. A GitLab repo first reads its project (the
// first request of every list sync, FR47). On success it marks the repo polled and moves the
// cutoff to the start of this poll; on any error neither moves.
func (p *Poller) pollRepo(ctx context.Context, workspaceID string, repo domain.Repo) error {
	start := p.now()
	cutoff, ok := p.cutoffs[repo.ID]
	if !ok {
		cutoff = start.Add(-p.opts.Horizon)
	}
	if repo.IsGitLab() {
		if err := p.syncProject(ctx, workspaceID, repo); err != nil {
			return err
		}
	}
	for page := 1; page <= p.opts.MaxPages; page++ {
		listed, more, err := p.listPage(ctx, repo, page)
		if err != nil {
			return fmt.Errorf("read page %d: %w", page, err)
		}
		if err := p.store.UpsertListed(ctx, workspaceID, repo.ID, listed); err != nil {
			return fmt.Errorf("store page %d: %w", page, err)
		}
		if !more || reachedCutoff(listed, cutoff) {
			break
		}
	}
	if err := p.store.MarkPolled(ctx, workspaceID, repo.ID, start); err != nil {
		return fmt.Errorf("mark polled: %w", err)
	}
	p.cutoffs[repo.ID] = start
	return nil
}

// listPage reads one page and reports whether another may follow. GitLab's adapter drops merge
// requests without a head sha, so it says itself whether the raw page was full; a GitHub page is
// full when it holds PageSize pull requests.
func (p *Poller) listPage(ctx context.Context, repo domain.Repo, page int) (listed []domain.Listed, more bool, err error) {
	if repo.IsGitLab() {
		return p.gitlab.ListPage(ctx, repo, page, p.opts.PageSize)
	}
	listed, err = p.pages.ListPullRequestsPage(ctx, repo.Owner, repo.Name, page, p.opts.PageSize)
	return listed, len(listed) >= p.opts.PageSize, err
}

// syncProject reads the GitLab project and follows a rename (FR47, followProjectPath). A failed
// read is a failed sync. A refused or colliding path, or a failed store, keeps the stored path: the
// sync goes on by project id and the next one tries again.
func (p *Poller) syncProject(ctx context.Context, workspaceID string, repo domain.Repo) error {
	proj, err := p.gitlab.Project(ctx, repo)
	if err != nil {
		return fmt.Errorf("read project: %w", err)
	}
	return followProjectPath(ctx, p.log, p.store, workspaceID, repo, proj)
}

// backfillStats fills the diff stats of up to PollStatsPerRepo stored PRs of the repo that have
// none and were updated within the horizon, newest update first. A failed fetch or store of one PR
// (GitLab: of one batch) is logged and the next goes on; only an error of the whole host (no
// token, not connected, a rate limit) or a cancelled ctx is returned, which ends the repo.
func (p *Poller) backfillStats(ctx context.Context, workspaceID string, repo domain.Repo) error {
	since := p.now().Add(-p.opts.Horizon)
	pending, err := p.store.ListWithoutDiffStats(ctx, workspaceID, repo.ID, since, PollStatsPerRepo)
	if err != nil {
		if ctx.Err() == nil {
			p.log.WarnContext(ctx, "background poll: listing pull requests without diff stats failed",
				"workspace_id", workspaceID, "repo_id", repo.ID, "err", err)
		}
		return nil
	}
	if repo.IsGitLab() {
		return p.backfillGitLabStats(ctx, workspaceID, repo, pending)
	}
	for _, pr := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		stats, err := p.stats.GetPullStats(ctx, repo.Owner, repo.Name, pr.Number)
		if err != nil {
			if endsRepo(ctx, err) {
				return err
			}
			p.log.WarnContext(ctx, "background poll: diff-stat backfill of a pull request failed",
				"workspace_id", workspaceID, "repo_id", repo.ID, "number", pr.Number, "err", err)
			continue
		}
		if err := p.setDiffStats(ctx, workspaceID, repo, pr, stats); err != nil {
			return err
		}
	}
	return nil
}

// backfillGitLabStats reads the stats of the pending merge requests, up to GitLabStatsBatch per
// query. A merge request the answer has no stats for stays unset and is tried again next cycle.
func (p *Poller) backfillGitLabStats(ctx context.Context, workspaceID string, repo domain.Repo, pending []domain.Pull) error {
	for batch := range slices.Chunk(pending, domain.GitLabStatsBatch) {
		if err := ctx.Err(); err != nil {
			return err
		}
		numbers := make([]int, len(batch))
		for i, pr := range batch {
			numbers[i] = pr.Number
		}
		stats, err := p.gitlab.Stats(ctx, repo, numbers)
		if err != nil {
			if endsRepo(ctx, err) {
				return err
			}
			p.log.WarnContext(ctx, "background poll: diff-stat backfill of a merge request batch failed",
				"workspace_id", workspaceID, "repo_id", repo.ID, "count", len(batch), "err", err)
			continue
		}
		for _, pr := range batch {
			s, ok := stats[pr.Number]
			if !ok {
				continue
			}
			if err := p.setDiffStats(ctx, workspaceID, repo, pr, s); err != nil {
				return err
			}
		}
	}
	return nil
}

// setDiffStats stores one PR's stats; a failed store is logged, only a cancelled ctx is returned.
func (p *Poller) setDiffStats(ctx context.Context, workspaceID string, repo domain.Repo, pr domain.Pull, stats domain.DiffStats) error {
	if err := p.store.SetDiffStats(ctx, pr.ID, stats); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.log.WarnContext(ctx, "background poll: diff stats not stored",
			"workspace_id", workspaceID, "repo_id", repo.ID, "number", pr.Number, "err", err)
	}
	return nil
}

// endsRepo reports whether a failed stats fetch must end the repo's backfill: the ctx is done, or
// the error concerns the whole host (it is handled by hostBlocked).
func endsRepo(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var noToken *NoTokenError
	if errors.Is(err, ErrNoToken) || errors.Is(err, ErrNotConnected) || errors.As(err, &noToken) {
		return true
	}
	_, limited := rateLimit(err)
	return limited
}

// forget drops the state of workspaces and repos that no longer exist.
func (p *Poller) forget(targets []domain.PollTarget) {
	repos := make(map[string]bool, len(targets))
	workspaces := map[string]bool{}
	for _, t := range targets {
		repos[t.Repo.ID] = true
		workspaces[t.WorkspaceID] = true
	}
	for id := range p.cutoffs {
		if !repos[id] {
			delete(p.cutoffs, id)
		}
	}
	for id := range p.lastStart {
		if !workspaces[id] {
			delete(p.lastStart, id)
		}
	}
}

func reachedCutoff(listed []domain.Listed, cutoff time.Time) bool {
	for _, l := range listed {
		if domain.ReachedCutoff(l, cutoff) {
			return true
		}
	}
	return false
}

// rateLimit reports whether err is a code-host rate limit (GitHub: primary, secondary or a 429;
// GitLab: a 429) and when it ends: Retry-After when the host sent it, else the reset it reported
// (the zero time when unknown).
func rateLimit(err error) (reset time.Time, limited bool) {
	var ge *GitHubError
	if errors.As(err, &ge) && (ge.RateLimited || ge.Status == statusTooManyRequests) {
		if !ge.RetryAfter.IsZero() {
			return ge.RetryAfter, true
		}
		return ge.RateLimitReset, true
	}
	var le *GitLabError
	if errors.As(err, &le) {
		return le.RateLimit()
	}
	return time.Time{}, false
}

// noGitLab is the GitLab port of a poller built without one: every repository is not connected.
type noGitLab struct{}

func (noGitLab) Project(context.Context, domain.Repo) (domain.GitLabProject, error) {
	return domain.GitLabProject{}, ErrNotConnected
}

func (noGitLab) ListPage(context.Context, domain.Repo, int, int) ([]domain.Listed, bool, error) {
	return nil, false, ErrNotConnected
}

func (noGitLab) Get(context.Context, domain.Repo, int) (domain.Fetched, error) {
	return domain.Fetched{}, ErrNotConnected
}

func (noGitLab) Issue(context.Context, domain.Repo, int) (domain.Issue, error) {
	return domain.Issue{}, ErrNotConnected
}

func (noGitLab) Stats(context.Context, domain.Repo, []int) (map[int]domain.DiffStats, error) {
	return nil, ErrNotConnected
}

type workspaceTargets struct {
	workspaceID string
	repos       []domain.Repo
}

// groupByWorkspace keeps the first-seen order of workspaces and of the repos inside them.
func groupByWorkspace(targets []domain.PollTarget) []workspaceTargets {
	var out []workspaceTargets
	index := map[string]int{}
	for _, t := range targets {
		i, ok := index[t.WorkspaceID]
		if !ok {
			i = len(out)
			index[t.WorkspaceID] = i
			out = append(out, workspaceTargets{workspaceID: t.WorkspaceID})
		}
		out[i].repos = append(out[i].repos, t.Repo)
	}
	return out
}
