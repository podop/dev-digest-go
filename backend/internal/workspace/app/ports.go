// Package app holds the use cases of the workspace context: the display name, the stats, the
// export, the reset of the run history and the trace prune. It reaches the database, the
// settings and the repos context only through the consumer-side ports below, which `cmd/server`
// wires.
package app

import (
	"context"
	"encoding/json"
	"time"

	"dev-digest-go/backend/internal/workspace/domain"
)

// NameStore keeps the workspace display name, the Settings row `workspace_name` (spec D23).
type NameStore interface {
	// WorkspaceName returns the stored name; found is false when no row exists. The text is
	// returned as stored: the caller validates it and falls back to domain.DefaultName.
	WorkspaceName(ctx context.Context, workspaceID string) (name string, found bool, err error)
	// SetWorkspaceName stores an already normalised name.
	SetWorkspaceName(ctx context.Context, workspaceID, userID, name string) error
}

// RepoInfo is a repository as GET /workspace lists it.
type RepoInfo struct {
	ID           string
	FullName     string
	ClonePath    *string
	LastPolledAt *time.Time
}

// Cloned reports whether the repository has a clone on disk.
func (r RepoInfo) Cloned() bool { return r.ClonePath != nil }

// ReposSnapshot is the repos part of GET /workspace: the clone root and the repositories.
type ReposSnapshot struct {
	CloneDir string
	Repos    []RepoInfo
}

// RepoOverview reads the repos context (the former `repos` GET /workspace use case).
type RepoOverview interface {
	Overview(ctx context.Context, workspaceID string) (ReposSnapshot, error)
}

// SettingsSource reads every stored setting of the workspace as key to raw JSON; a stored SQL
// NULL is the JSON literal null. The export filters it with domain.ExportSettings.
type SettingsSource interface {
	Settings(ctx context.Context, workspaceID string) (map[string]json.RawMessage, error)
}

// RetentionSource reads the workspace's effective trace retention. An absent setting is
// domain.DefaultRetention, a stored null is domain.KeepForever, and a stored value outside the
// accepted choices is an error wrapping domain.ErrInvalidRetention (the pruner then logs a
// warning and deletes nothing).
type RetentionSource interface {
	Retention(ctx context.Context, workspaceID string) (domain.Retention, error)
}

// Reader reads the workspace's counts and export records.
type Reader interface {
	Stats(ctx context.Context, workspaceID string) (domain.Stats, error)
	// ExportData returns the configuration records in stable order. Its Settings stay nil: the
	// service fills them from SettingsSource.
	ExportData(ctx context.Context, workspaceID string) (domain.ExportData, error)
}

// History deletes the workspace's run history.
type History interface {
	// Reset deletes the run history in one all-or-nothing operation. When the workspace has an
	// agent run or an eval suite run in status running, active is that count (greater than 0),
	// nothing is deleted and the error is nil.
	Reset(ctx context.Context, workspaceID string) (deleted domain.Deleted, active int, err error)
}

// Workspaces lists the workspaces to prune.
type Workspaces interface {
	WorkspaceIDs(ctx context.Context) ([]string, error)
}

// TracePruner deletes expired run traces.
type TracePruner interface {
	// PruneTraces deletes the traces of the workspace's agent runs started strictly before
	// cutoff, and returns how many it deleted. It changes nothing else.
	PruneTraces(ctx context.Context, workspaceID string, cutoff time.Time) (int64, error)
}
