package app

import (
	"context"
	"encoding/json"
	"time"

	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/llm"
)

// Repo is the part of a repository row the conventions context needs.
type Repo struct {
	ID       string
	FullName string
	// ClonePath is the clone directory; "" when the repo is not cloned.
	ClonePath string
}

// ScanRef identifies a scan and the repo and workspace it belongs to.
type ScanRef struct {
	ID, WorkspaceID, RepoID string
}

// Summary is what a finished scan records.
type Summary struct {
	SampledFiles        []string
	Proposed, Kept      int
	Dropped             []domain.Dropped
	Model               string
	TokensIn, TokensOut int
	// CostUSD is nil when the cost is unknown.
	CostUSD *float64
}

// repoLookup finds a repo of a workspace; it returns domain.ErrNotFound for an unknown id or a
// repo of another workspace.
type repoLookup interface {
	Get(ctx context.Context, workspaceID, repoID string) (Repo, error)
}

// scanStore is what the service needs from persistence.
type scanStore interface {
	// LatestScan returns the repo's newest scan by start time, or domain.ErrNotFound.
	LatestScan(ctx context.Context, repoID string) (domain.Scan, error)
	// List returns every convention of the repo: accepted, pending, rejected, then confidence
	// descending (NULL last), then creation time ascending.
	List(ctx context.Context, repoID string) ([]domain.Convention, error)
	// FailStaleScans fails the repo's running scans that started before the given time.
	FailStaleScans(ctx context.Context, repoID string, before time.Time, reason string, at time.Time) error
	// StartScan inserts a running scan; domain.ErrScanRunning when the repo already has one.
	StartScan(ctx context.Context, workspaceID, repoID string, at time.Time) (domain.Scan, error)
	// FailScan fails a scan that is still running; a scan that is gone or no longer running is
	// left alone and is not an error.
	FailScan(ctx context.Context, scanID, reason string, sampled []string, at time.Time) error
	// FailRunningScans fails every running scan of every repo and returns how many it failed.
	FailRunningScans(ctx context.Context, reason string, at time.Time) (int, error)
	// Update loads the convention of the workspace for update, applies fn and stores the
	// result in one transaction; domain.ErrNotFound for an unknown id. An error from fn aborts.
	Update(ctx context.Context, workspaceID, id string, fn func(domain.Convention) (domain.Convention, error)) (domain.Convention, error)
	// AcceptedIDs returns, among ids, those that are accepted conventions of the repo.
	AcceptedIDs(ctx context.Context, workspaceID, repoID string, ids []string) ([]string, error)
	// SetSkill points the given conventions of the repo at the skill.
	SetSkill(ctx context.Context, workspaceID, repoID string, ids []string, skillID string, at time.Time) error
}

// enqueuer is the job queue port: it records the job and returns at once.
type enqueuer interface {
	Enqueue(ctx context.Context, workspaceID, kind string, payload any) (jobID string, err error)
}

// redactor scrubs provider keys out of a text (llm.Client.Redact).
type redactor interface {
	Redact(s string) string
}

// jobStore is what the extract job needs from persistence.
type jobStore interface {
	List(ctx context.Context, repoID string) ([]domain.Convention, error)
	// CompleteScan, in one transaction, deletes the repo's pending unedited conventions,
	// inserts kept as pending conventions of the scan and sets the scan done. It returns
	// domain.ErrNotFound (and changes nothing) when the scan is no longer running.
	CompleteScan(ctx context.Context, scan ScanRef, kept []domain.KeptConvention, sum Summary, at time.Time) error
	FailScan(ctx context.Context, scanID, reason string, sampled []string, at time.Time) error
}

// cloneFiles reads a clone on disk, read-only and confined to the clone.
type cloneFiles interface {
	// List returns the repo-relative paths of the clone's regular files (bounded walk), or an
	// error wrapping domain.ErrCloneMissing when the directory is gone.
	List(ctx context.Context, root string) ([]string, error)
	// Read returns the text of a regular file inside the clone; ok is false for a missing,
	// unsafe, oversized or binary (NUL) file.
	Read(root, rel string) (text string, ok bool)
}

// structuredLLM is the part of *llm.Client the job uses.
type structuredLLM interface {
	Structured(ctx context.Context, req llm.Request) (llm.Result, error)
}

// featureModels reads the workspace's settings bag (the settings app).
type featureModels interface {
	Get(ctx context.Context, workspaceID string) (map[string]json.RawMessage, error)
}
