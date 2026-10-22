package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/repointel/domain"
)

// Full runs the full index of the repo (FR3011-FR3015). A run that ends with an error has rolled
// back its transaction; unless the cause is a shutdown or a vanished repo it also leaves a
// failed state when no usable index exists (FR3037). The error is returned for the job.
func (ix *Indexer) Full(ctx context.Context, repoID string) (Result, error) {
	return ix.guarded(ctx, repoID, ix.full)
}

// Incremental runs the incremental index of the repo (FR3016-FR3021), falling back to the full
// index where the spec says so. Errors are handled as in Full.
func (ix *Indexer) Incremental(ctx context.Context, repoID string) (Result, error) {
	return ix.guarded(ctx, repoID, ix.incremental)
}

// guarded runs one index attempt, turning a panic into an error and recording the failed state
// of an attempt that ended with an error.
func (ix *Indexer) guarded(ctx context.Context, repoID string, run func(context.Context, string) (Result, error)) (res Result, err error) {
	start := ix.now()
	defer func() {
		if p := recover(); p != nil {
			res, err = Result{}, fmt.Errorf("index panicked: %v", p)
		}
		if err != nil {
			ix.recordFailure(ctx, repoID, start, err)
		}
	}()
	return run(ctx, repoID)
}

// recordFailure applies FR3037 after an attempt ended with cause: a failed state only when the
// repo has no usable (full or partial) index, nothing at all on shutdown or when the repo is
// gone. The write runs on a context that outlives the attempt's own.
func (ix *Indexer) recordFailure(ctx context.Context, repoID string, start time.Time, cause error) {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(cause, domain.ErrRepoNotFound) {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failWriteTimeout)
	defer cancel()
	prev, ok, err := ix.d.Reader.GetState(wctx, repoID)
	if err != nil {
		ix.log.WarnContext(wctx, "repo intel failed state not written: previous state unreadable",
			"repo_id", repoID, "error", errorText(ix.d.Redact, err.Error()))
		return
	}
	var previous *domain.IndexState
	sha := ""
	if ok {
		previous, sha = &prev, prev.LastIndexedSHA
	}
	if !domain.ShouldRecordFailure(previous) {
		return
	}
	st := domain.IndexState{
		RepoID:         repoID,
		LastIndexedSHA: sha,
		IndexerVersion: domain.IndexerVersion,
		Status:         domain.StatusFailed,
		UpdatedAt:      ix.now(),
		Stats: map[string]any{
			domain.StatReason:         string(domain.ReasonIndexFailed),
			domain.StatDegradedReason: string(domain.ReasonIndexFailed),
			"error":                   errorText(ix.d.Redact, cause.Error()),
			domain.StatDurationMs:     int(ix.since(start).Milliseconds()),
		},
	}
	if err := ix.d.Writer.UpsertState(wctx, st); err != nil && !errors.Is(err, domain.ErrRepoNotFound) {
		ix.log.WarnContext(wctx, "repo intel failed state not written",
			"repo_id", repoID, "error", errorText(ix.d.Redact, err.Error()))
	}
}
