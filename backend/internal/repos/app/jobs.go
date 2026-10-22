package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/repos/domain"
)

const (
	// cloneDepth is the shallow depth of the first clone.
	cloneDepth = 1
	// DefaultRemoteBase is where repositories are cloned from.
	DefaultRemoteBase = "https://github.com"
)

// jobStore is what the job handlers need from persistence. It is not workspace-scoped: the
// payload comes from our own queue.
type jobStore interface {
	// GetByID returns domain.ErrNotFound when the repo was deleted.
	GetByID(ctx context.Context, id string) (domain.Repo, error)
	// SetCloned records the clone directory and the poll time; domain.ErrNotFound if the repo
	// is gone.
	SetCloned(ctx context.Context, id, clonePath string, at time.Time) error
	// SetPolled records the poll time; domain.ErrNotFound if the repo is gone.
	SetPolled(ctx context.Context, id string, at time.Time) error
}

// gitClient is the part of *gitcli.Client the jobs use.
type gitClient interface {
	Clone(ctx context.Context, owner, name, url string, opts gitcli.CloneOptions) (string, error)
	DefaultBranch(ctx context.Context, owner, name string) (string, error)
	Sync(ctx context.Context, owner, name, branch string) (string, error)
}

// indexTrigger is how the jobs hand a fresh or updated clone to repo-intel: it enqueues the
// indexing job and returns at once.
type indexTrigger interface {
	// TriggerIndex enqueues the first full index of a freshly cloned repo.
	TriggerIndex(ctx context.Context, workspaceID, repoID, owner, name string) error
	// TriggerRefresh enqueues the incremental index after a refresh.
	TriggerRefresh(ctx context.Context, workspaceID, repoID string) error
}

// JobsOptions tunes Jobs.
type JobsOptions struct {
	// RemoteBase is the host serving <owner>/<name>.git; empty means DefaultRemoteBase.
	// Tests point it at a file:// directory of bare repositories.
	RemoteBase string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Log receives warnings; nil discards them.
	Log *slog.Logger
	// Index is told about every clone and refresh that succeeded; nil means no indexing. A
	// failure of the trigger is logged and never fails the job.
	Index indexTrigger
}

// Jobs are the background handlers of the repos context. Register Clone under KindClone and
// Refresh under KindRefresh; both have the job handler signature.
type Jobs struct {
	store  jobStore
	git    gitClient
	remote string
	now    func() time.Time
	log    *slog.Logger
	index  indexTrigger
}

// NewJobs builds the job handlers.
func NewJobs(store jobStore, git gitClient, opts JobsOptions) *Jobs {
	j := &Jobs{store: store, git: git, remote: opts.RemoteBase, now: opts.Now, log: opts.Log, index: opts.Index}
	if j.remote == "" {
		j.remote = DefaultRemoteBase
	}
	if j.now == nil {
		j.now = time.Now
	}
	if j.log == nil {
		j.log = slog.New(slog.DiscardHandler)
	}
	return j
}

// Clone is the handler of the clone job: make sure the shallow clone of the repo exists (an
// existing clone is fetched, a partial directory replaced), then record clone_path and
// last_polled_at and ask repo-intel for the first index (a failure of that is only logged). A repo deleted before or during the clone ends the job without error and
// without touching the database.
func (j *Jobs) Clone(ctx context.Context, payload json.RawMessage) error {
	var p ClonePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode clone payload: %w", err)
	}
	repo, err := j.store.GetByID(ctx, p.RepoID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load repo %s: %w", p.RepoID, err)
	}
	// Owner and name come from the row (validated when added), not from the payload.
	ref := domain.Ref{Owner: repo.Owner, Name: repo.Name}
	dir, err := j.git.Clone(ctx, ref.Owner, ref.Name, ref.CloneURL(j.remote), gitcli.CloneOptions{Depth: cloneDepth})
	if err != nil {
		return fmt.Errorf("clone %s: %w", repo.FullName, err)
	}
	if err := j.store.SetCloned(ctx, repo.ID, dir, j.now()); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("record clone of %s: %w", repo.FullName, err)
	}
	if j.index != nil {
		if err := j.index.TriggerIndex(ctx, repo.WorkspaceID, repo.ID, repo.Owner, repo.Name); err != nil {
			j.log.WarnContext(ctx, "repo-intel index not enqueued", "repo", repo.FullName, "err", err)
		}
	}
	return nil
}

// Refresh is the handler of the refresh job: fetch the remote's default branch with depth 50,
// hard-reset the clone to it and set last_polled_at, then ask repo-intel for an incremental
// index (a failure of that is only logged). The default branch
// is asked from the remote; if that fails the stored default_branch is used. A repo deleted
// meanwhile ends the job without error.
func (j *Jobs) Refresh(ctx context.Context, payload json.RawMessage) error {
	var p RefreshPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode refresh payload: %w", err)
	}
	repo, err := j.store.GetByID(ctx, p.RepoID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load repo %s: %w", p.RepoID, err)
	}
	if !repo.Cloned() {
		return fmt.Errorf("refresh %s: repo is not cloned", repo.FullName)
	}
	branch, err := j.git.DefaultBranch(ctx, repo.Owner, repo.Name)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("default branch of %s: %w", repo.FullName, err)
		}
		j.log.WarnContext(ctx, "remote default branch unknown, using stored one",
			"repo", repo.FullName, "branch", repo.DefaultBranch, "err", err)
		branch = repo.DefaultBranch
	}
	if _, err := j.git.Sync(ctx, repo.Owner, repo.Name, branch); err != nil {
		return fmt.Errorf("sync %s: %w", repo.FullName, err)
	}
	if err := j.store.SetPolled(ctx, repo.ID, j.now()); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("record refresh of %s: %w", repo.FullName, err)
	}
	if j.index != nil {
		if err := j.index.TriggerRefresh(ctx, repo.WorkspaceID, repo.ID); err != nil {
			j.log.WarnContext(ctx, "repo-intel refresh not enqueued", "repo", repo.FullName, "err", err)
		}
	}
	return nil
}
