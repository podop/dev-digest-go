package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Store PollStore
	Log   *slog.Logger // nil discards
	Now   func() time.Time
}

// Poller keeps every repository's PRs fresh in the background. Each workspace has its own due
// time, derived from its Settings polling interval, which is read at the start of each cycle.
// Repositories are polled one at a time. Run must not be called concurrently with itself.
type Poller struct {
	targets   PollTargets
	intervals IntervalSource
	pages     PageLister
	stats     StatsFetcher
	store     PollStore
	log       *slog.Logger
	now       func() time.Time
	opts      PollerOptions

	// State below belongs to the goroutine that runs Run.
	nextDue     map[string]time.Time // workspace id -> next cycle
	cutoffs     map[string]time.Time // repo id -> start of its last successful background poll
	pausedUntil time.Time            // rate limit: nothing is polled before this
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
	return &Poller{
		targets: deps.Targets, intervals: deps.Intervals, pages: deps.Pages, stats: deps.Stats, store: deps.Store,
		log: log, now: now, opts: opts,
		nextDue: map[string]time.Time{}, cutoffs: map[string]time.Time{},
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

// round runs the cycle of every workspace that is due, one workspace after the other.
func (p *Poller) round(ctx context.Context) {
	if p.now().Before(p.pausedUntil) {
		return
	}
	targets, err := p.targets.ListPollTargets(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.log.WarnContext(ctx, "background poll: listing repositories failed", "err", err)
		}
		return
	}
	p.forget(targets)
	for _, group := range groupByWorkspace(targets) {
		now := p.now()
		if ctx.Err() != nil || now.Before(p.pausedUntil) {
			return
		}
		// Ticks are Tick apart, so a cycle that began a hair after its tick would otherwise miss
		// the next one; half a tick of slack keeps a 1-minute interval at one minute.
		if now.Add(p.opts.Tick / 2).Before(p.nextDue[group.workspaceID]) {
			continue
		}
		p.cycle(ctx, group)
	}
}

// cycle polls every repo of one workspace: its PR pages, then the diff stats of stored PRs that
// have none. A failed repo is logged and skipped; a missing token or a rate limit ends the cycle.
func (p *Poller) cycle(ctx context.Context, g workspaceTargets) {
	start := p.now()
	due := start.Add(p.interval(ctx, g.workspaceID))
	p.nextDue[g.workspaceID] = due

	for _, repo := range g.repos {
		if ctx.Err() != nil {
			return
		}
		err := p.pollRepo(ctx, g.workspaceID, repo)
		if err == nil {
			err = p.backfillStats(ctx, g.workspaceID, repo)
		}
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrNoToken) {
			p.log.WarnContext(ctx, "background poll skipped: no GitHub token is configured", "workspace_id", g.workspaceID)
			return
		}
		if reset, limited := rateLimit(err); limited {
			resume := domain.ResumeAfterRateLimit(start, due, reset)
			p.nextDue[g.workspaceID] = resume
			p.pausedUntil = resume
			p.log.WarnContext(ctx, "background poll paused: GitHub rate limit reached",
				"workspace_id", g.workspaceID, "repo_id", repo.ID, "resume_at", resume)
			return
		}
		p.log.WarnContext(ctx, "background poll of a repository failed",
			"workspace_id", g.workspaceID, "repo_id", repo.ID, "repo", repo.Owner+"/"+repo.Name, "err", err)
	}
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
// updated since the cutoff, a short page or MaxPages. On success it marks the repo polled and
// moves the cutoff to the start of this poll; on any error neither moves.
func (p *Poller) pollRepo(ctx context.Context, workspaceID string, repo domain.Repo) error {
	start := p.now()
	cutoff, ok := p.cutoffs[repo.ID]
	if !ok {
		cutoff = start.Add(-p.opts.Horizon)
	}
	for page := 1; page <= p.opts.MaxPages; page++ {
		listed, err := p.pages.ListPullRequestsPage(ctx, repo.Owner, repo.Name, page, p.opts.PageSize)
		if err != nil {
			return fmt.Errorf("read page %d: %w", page, err)
		}
		if err := p.store.UpsertListed(ctx, workspaceID, repo.ID, listed); err != nil {
			return fmt.Errorf("store page %d: %w", page, err)
		}
		if len(listed) < p.opts.PageSize || reachedCutoff(listed, cutoff) {
			break
		}
	}
	if err := p.store.MarkPolled(ctx, workspaceID, repo.ID, start); err != nil {
		return fmt.Errorf("mark polled: %w", err)
	}
	p.cutoffs[repo.ID] = start
	return nil
}

// backfillStats fills the diff stats of up to PollStatsPerRepo stored PRs of the repo that have
// none and were updated within the horizon, newest update first. A failed fetch or store of one PR
// is logged and the next PR goes on; only a missing token, a rate limit or a cancelled ctx is
// returned, which ends the cycle.
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
	for _, pr := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		stats, err := p.stats.GetPullStats(ctx, repo.Owner, repo.Name, pr.Number)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, ErrNoToken) {
				return err
			}
			if _, limited := rateLimit(err); limited {
				return err
			}
			p.log.WarnContext(ctx, "background poll: diff-stat backfill of a pull request failed",
				"workspace_id", workspaceID, "repo_id", repo.ID, "number", pr.Number, "err", err)
			continue
		}
		if err := p.store.SetDiffStats(ctx, pr.ID, stats); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.log.WarnContext(ctx, "background poll: diff stats not stored",
				"workspace_id", workspaceID, "repo_id", repo.ID, "number", pr.Number, "err", err)
		}
	}
	return nil
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
	for id := range p.nextDue {
		if !workspaces[id] {
			delete(p.nextDue, id)
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

// rateLimit reports whether err is a GitHub rate limit (primary, secondary, or a 429) and when
// it ends: Retry-After when GitHub sent it, else the primary limit's reset (the zero time when
// unknown).
func rateLimit(err error) (reset time.Time, limited bool) {
	var ge *GitHubError
	if errors.As(err, &ge) && (ge.RateLimited || ge.Status == statusTooManyRequests) {
		if !ge.RetryAfter.IsZero() {
			return ge.RetryAfter, true
		}
		return ge.RateLimitReset, true
	}
	return time.Time{}, false
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
