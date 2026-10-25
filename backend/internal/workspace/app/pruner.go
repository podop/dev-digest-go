package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/workspace/domain"
)

// DefaultPruneInterval is the pause between the end of one prune and the start of the next (spec
// FR8, plan A7).
const DefaultPruneInterval = 24 * time.Hour

// Pruner deletes expired run traces: once when Run starts and then DefaultPruneInterval after the
// previous prune ended. It re-reads every workspace's retention on each prune (FR8) and never lets
// a failure or a panic stop the schedule (FR9).
type Pruner struct {
	workspaces Workspaces
	retention  RetentionSource
	traces     TracePruner
	log        *slog.Logger
	interval   time.Duration
	now        func() time.Time
}

// PrunerOption changes a Pruner's defaults; the server uses it only for tests and the interval seam.
type PrunerOption func(*Pruner)

// WithPruneInterval sets the pause between two prunes; a value of zero or less is ignored.
func WithPruneInterval(d time.Duration) PrunerOption {
	return func(p *Pruner) {
		if d > 0 {
			p.interval = d
		}
	}
}

// WithPruneNow replaces the time source the cutoff is computed from.
func WithPruneNow(now func() time.Time) PrunerOption {
	return func(p *Pruner) {
		if now != nil {
			p.now = now
		}
	}
}

// NewPruner builds a Pruner.
func NewPruner(workspaces Workspaces, retention RetentionSource, traces TracePruner, log *slog.Logger, opts ...PrunerOption) *Pruner {
	p := &Pruner{
		workspaces: workspaces,
		retention:  retention,
		traces:     traces,
		log:        log,
		interval:   DefaultPruneInterval,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Run prunes at once and then repeatedly until ctx is done, and returns only then. It blocks, so
// the caller owns the goroutine and waits for Run to return on shutdown. Cancelling ctx also
// cancels a prune in flight, so Run returns promptly.
func (p *Pruner) Run(ctx context.Context) {
	for {
		p.pruneAll(ctx)
		timer := time.NewTimer(p.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// pruneAll runs one prune over every workspace. Nothing escapes it: a failure is logged and the
// remaining workspaces are still pruned.
func (p *Pruner) pruneAll(ctx context.Context) {
	var ids []string
	if !p.guard("trace prune: list workspaces", "", func() error {
		var err error
		ids, err = p.workspaces.WorkspaceIDs(ctx)
		return err
	}) {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		p.guard("trace prune", id, func() error { return p.pruneWorkspace(ctx, id) })
	}
}

// guard runs fn, logs its error or panic as one ERROR line and reports whether it succeeded.
func (p *Pruner) guard(msg, workspaceID string, fn func() error) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error(msg+" panicked", "workspace_id", workspaceID, "panic", fmt.Sprint(r))
			ok = false
		}
	}()
	if err := fn(); err != nil {
		if !errors.Is(err, context.Canceled) { // a shutdown is not a failure
			p.log.Error(msg+" failed", "workspace_id", workspaceID, "error", err)
		}
		return false
	}
	return true
}

// pruneWorkspace prunes one workspace; the error it returns is logged by guard, the outcomes that
// are not failures are logged here.
func (p *Pruner) pruneWorkspace(ctx context.Context, workspaceID string) error {
	start := p.now()
	ret, err := p.retention.Retention(ctx, workspaceID)
	switch {
	case errors.Is(err, domain.ErrInvalidRetention):
		p.log.Warn("trace prune skipped: stored retention_days is not valid",
			"workspace_id", workspaceID, "error", err)
		return nil
	case err != nil:
		return fmt.Errorf("read retention: %w", err)
	}
	if ret.Forever { // before Cutoff: it has no cutoff to give
		p.log.Info("trace prune skipped: traces are kept forever",
			"workspace_id", workspaceID, "retention_days", nil)
		return nil
	}
	deleted, err := p.traces.PruneTraces(ctx, workspaceID, ret.Cutoff(start))
	if err != nil {
		return fmt.Errorf("prune traces: %w", err)
	}
	p.log.Info("trace prune done",
		"workspace_id", workspaceID,
		"retention_days", ret.Days,
		"deleted", deleted,
		"duration_ms", p.now().Sub(start).Milliseconds())
	return nil
}
