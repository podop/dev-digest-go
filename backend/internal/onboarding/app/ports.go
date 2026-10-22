// Package app holds the use cases of the onboarding context: reading a repository's stored tour
// and generating one (index reads, bounded blob reads at the indexed commit, one structured
// model call, normalisation, storage), at most one generation per repository at a time.
//
// This file holds the ports the use cases depend on; api.go holds the use cases' own interface,
// which transport depends on. The peers adapters bind the ports of other contexts in cmd/server.
package app

import (
	"context"
	"encoding/json"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/onboarding/domain"
)

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// Store is the persistence of the context.
type Store interface {
	// FindRepo returns a repository of the workspace; domain.ErrNotFound otherwise.
	FindRepo(ctx context.Context, workspaceID, repoID string) (domain.Repo, error)
	// Tour returns the stored tour; ok is false when there is none and also when the stored
	// document no longer satisfies the contract (limits included): it reads as absent, never as
	// an error.
	Tour(ctx context.Context, repoID string) (t domain.Tour, ok bool, err error)
	// SaveTour upserts onboarding(repo_id, json, generated_at = t.GeneratedAt); the adapter
	// sanitises text for jsonb.
	SaveTour(ctx context.Context, t domain.Tour) error
}

// IndexStates reads a repository's index state (the peers adapter over
// repointel/app.Service.IndexState, which never fails: a missing or unreadable state is the
// synthesised degraded one).
type IndexStates interface {
	IndexState(ctx context.Context, repoID string) domain.IndexState
}

// Index is the part of the repo-intel read port a generation uses (the peers adapter over
// repointel/app.Reader). With the feature flag off or no index each operation answers empty; only
// real failures are errors.
type Index interface {
	// RepoMap returns the cached repo map text for the token budget; "" when degraded.
	RepoMap(ctx context.Context, repoID string, budget int) (string, error)
	// TopFilesByRank returns up to n repo-relative paths, most important first.
	TopFilesByRank(ctx context.Context, repoID string, n int) ([]string, error)
	// CriticalPaths returns the dependency chains from the most central files, each a path list.
	CriticalPaths(ctx context.Context, repoID string) ([][]string, error)
	// ListIndexedFiles returns every indexed path (bounded by repo-intel).
	ListIndexedFiles(ctx context.Context, repoID string) ([]string, error)
}

// Blobs reads files of the repository's clone at a commit, without a checkout (the peers adapter
// over platform/gitcli BlobAt).
type Blobs interface {
	// Read returns the file's content; ok is false for a missing, oversize (over max bytes),
	// unsafe or failing read: the file is then absent from the prompt. The text is decoded by the
	// caller (WHATWG replacement).
	Read(ctx context.Context, owner, name, sha, path string, max int64) (data []byte, ok bool)
}

// LLM is the part of *llm.Client the generation uses.
type LLM interface {
	Structured(ctx context.Context, req llm.Request) (llm.Result, error)
}

// FeatureModels reads the workspace's settings bag (the settings app); the service resolves the
// onboarding model with llm.ResolveFeatureModel.
type FeatureModels interface {
	Get(ctx context.Context, workspaceID string) (map[string]json.RawMessage, error)
}
