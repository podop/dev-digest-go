package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/repointel/domain"
)

// backfillSource lists the repos that need an index job at startup.
type backfillSource interface {
	ListBackfill(ctx context.Context) ([]domain.Repo, error)
}

// indexTrigger enqueues a repo-intel-index job (implemented by Jobs).
type indexTrigger interface {
	TriggerIndex(ctx context.Context, workspaceID, repoID, owner, name string) error
}

// Backfill enqueues one repo-intel-index job for every cloned repo without an index of the
// current version (FR3008).
type Backfill struct {
	src     backfillSource
	trigger indexTrigger
}

// NewBackfill builds the startup backfill.
func NewBackfill(src backfillSource, trigger indexTrigger) *Backfill {
	return &Backfill{src: src, trigger: trigger}
}

// Run enqueues the jobs in repo id order and returns how many it enqueued. It tries every repo:
// the returned error joins the listing failure or the failures of single enqueues. The caller
// logs it and carries on starting (it must not be fatal).
func (b *Backfill) Run(ctx context.Context) (int, error) {
	repos, err := b.src.ListBackfill(ctx)
	if err != nil {
		return 0, fmt.Errorf("list repos to index: %w", err)
	}
	var errs []error
	enqueued := 0
	for _, r := range repos {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := b.trigger.TriggerIndex(ctx, r.WorkspaceID, r.ID, r.Owner, r.Name); err != nil {
			errs = append(errs, fmt.Errorf("enqueue index of repo %s: %w", r.ID, err))
			continue
		}
		enqueued++
	}
	return enqueued, errors.Join(errs...)
}
