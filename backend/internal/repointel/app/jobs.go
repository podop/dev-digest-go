package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/repointel/domain"
)

// indexRunner is the Indexer as the job handlers use it.
type indexRunner interface {
	Full(ctx context.Context, repoID string) (Result, error)
	Incremental(ctx context.Context, repoID string) (Result, error)
}

// repoReader finds a repo; it returns domain.ErrRepoNotFound for an unknown id.
type repoReader interface {
	GetRepo(ctx context.Context, repoID string) (domain.Repo, error)
}

// syncGit is the part of Git the resync job uses.
type syncGit interface {
	DefaultBranch(ctx context.Context, owner, name string) (string, error)
	Sync(ctx context.Context, owner, name, branch string) (string, error)
}

// JobsDeps are the collaborators of Jobs.
type JobsDeps struct {
	Indexer indexRunner
	Repos   repoReader
	Git     syncGit
	Queue   Enqueuer
	// Redact scrubs provider keys out of error texts before they are logged; nil leaves them.
	Redact Redactor
}

// JobsOptions tune Jobs.
type JobsOptions struct {
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Log receives one repo_intel_index line per attempt; nil discards it.
	Log *slog.Logger
}

// Jobs are the background handlers of repo-intel and the triggers other contexts use. Register
// Index under domain.KindIndex, Refresh under domain.KindRefresh and Resync under
// domain.KindResync, each with jobs.WithTimeout(domain.JobTimeout).
type Jobs struct {
	d   JobsDeps
	now func() time.Time
	log *slog.Logger
}

// NewJobs builds the job handlers.
func NewJobs(d JobsDeps, opts JobsOptions) *Jobs {
	j := &Jobs{d: d, now: opts.Now, log: opts.Log}
	if j.d.Redact == nil {
		j.d.Redact = noRedact{}
	}
	if j.now == nil {
		j.now = time.Now
	}
	if j.log == nil {
		j.log = slog.New(slog.DiscardHandler)
	}
	return j
}

// payload is what the three job kinds read; other fields (owner, name of an index job) are
// ignored, the repo's own row is the source of truth.
type payload struct {
	RepoID string `json:"repoId"`
}

type indexPayload struct {
	RepoID string `json:"repoId"`
	Owner  string `json:"owner"`
	Name   string `json:"name"`
}

// TriggerIndex enqueues a repo-intel-index job (the clone job calls it, FR3006).
func (j *Jobs) TriggerIndex(ctx context.Context, workspaceID, repoID, owner, name string) error {
	if _, err := j.d.Queue.Enqueue(ctx, workspaceID, domain.KindIndex, indexPayload{RepoID: repoID, Owner: owner, Name: name}); err != nil {
		return fmt.Errorf("enqueue %s: %w", domain.KindIndex, err)
	}
	return nil
}

// TriggerRefresh enqueues a repo-intel-refresh job (the refresh job calls it, FR3007).
func (j *Jobs) TriggerRefresh(ctx context.Context, workspaceID, repoID string) error {
	if _, err := j.d.Queue.Enqueue(ctx, workspaceID, domain.KindRefresh, payload{RepoID: repoID}); err != nil {
		return fmt.Errorf("enqueue %s: %w", domain.KindRefresh, err)
	}
	return nil
}

// Index is the handler of repo-intel-index: the full index (FR3010).
func (j *Jobs) Index(ctx context.Context, raw json.RawMessage) error {
	return j.run(ctx, domain.KindIndex, raw, func(ctx context.Context, repoID string) (Result, error) {
		return j.d.Indexer.Full(ctx, repoID)
	})
}

// Refresh is the handler of repo-intel-refresh: the incremental index (FR3010).
func (j *Jobs) Refresh(ctx context.Context, raw json.RawMessage) error {
	return j.run(ctx, domain.KindRefresh, raw, func(ctx context.Context, repoID string) (Result, error) {
		return j.d.Indexer.Incremental(ctx, repoID)
	})
}

// Resync is the handler of repo-intel-resync (FR3009): sync the clone to the remote default
// branch, then run the incremental index. A repo that is gone or not cloned, and a failed sync,
// end the job without writing and without an error; only a sync cut short by the job's own
// context (timeout, shutdown) is returned as an error.
func (j *Jobs) Resync(ctx context.Context, raw json.RawMessage) error {
	return j.run(ctx, domain.KindResync, raw, j.resync)
}

func (j *Jobs) resync(ctx context.Context, repoID string) (Result, error) {
	start := j.now()
	ended := func(reason string) Result {
		return Result{Status: domain.StatusDegraded, Duration: j.now().Sub(start), Reason: reason}
	}
	repo, err := j.d.Repos.GetRepo(ctx, repoID)
	if errors.Is(err, domain.ErrRepoNotFound) {
		return ended("no_clone"), nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("load repo %s: %w", repoID, err)
	}
	if repo.ClonePath == "" {
		return ended("no_clone"), nil
	}
	owner, name := repo.CloneKey()
	branch, err := j.d.Git.DefaultBranch(ctx, owner, name)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("default branch: %w", err)
		}
		branch = repo.DefaultBranch
	}
	if _, err := j.d.Git.Sync(ctx, owner, name, branch); err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("sync clone: %w", err)
		}
		return ended("sync_failed:" + errorText(j.d.Redact, err.Error())), nil
	}
	return j.d.Indexer.Incremental(ctx, repoID)
}

// run decodes the payload, runs do and writes the attempt's log line (FR3039). The error of do
// goes back to the runner (retry of transient errors, job status).
func (j *Jobs) run(ctx context.Context, kind string, raw json.RawMessage, do func(context.Context, string) (Result, error)) error {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decode %s payload: %w", kind, err)
	}
	if p.RepoID == "" {
		return fmt.Errorf("decode %s payload: repoId is required", kind)
	}
	res, err := do(ctx, p.RepoID)
	j.logAttempt(ctx, kind, p.RepoID, res, err)
	if err != nil {
		return fmt.Errorf("%s: %w", kind, err)
	}
	return nil
}

// logAttempt writes the one structured line of an index attempt. It carries counters, ids and
// reasons only: never source text, signatures or map text (NFR3005).
func (j *Jobs) logAttempt(ctx context.Context, kind, repoID string, res Result, err error) {
	ctx = context.WithoutCancel(ctx)
	status, reason := string(res.Status), res.Reason
	attrs := []any{"repo_id", repoID, "kind", kind}
	if err != nil {
		status, reason = string(domain.StatusFailed), string(domain.ReasonIndexFailed)
	}
	attrs = append(attrs, "status", status, "files_indexed", res.FilesIndexed, "files_skipped", res.FilesSkipped,
		"duration_ms", res.Duration.Milliseconds(), "reason", reason)
	if err != nil {
		attrs = append(attrs, "error", errorText(j.d.Redact, err.Error()))
	}
	j.log.InfoContext(ctx, "repo_intel_index", attrs...)
}
