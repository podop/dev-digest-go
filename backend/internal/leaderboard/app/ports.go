// Package app holds the use case of the leaderboard context: GET /leaderboard reads the facts
// of the window and its predecessor from the store and folds them with the domain's pure rules.
// Nothing here writes, calls GitHub or an LLM (NFR2).
//
// This file holds the port the use case depends on; api.go holds the use case's own interface,
// which transport depends on.
package app

import (
	"context"
	"time"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// Reader is the read-only persistence of the context (SELECT only; the adapter reads the tables
// of other contexts with its own row types, plan P1/P2). Every method is workspace-scoped, and
// at most nine set-based queries serve one request in total. A failure is returned as is; the
// use case maps it to 500.
type Reader interface {
	// RepoScope returns the repositories of the filter, ordered by full name. repo is
	// domain.RepoAll (every repository of the workspace, possibly none) or a repository UUID;
	// an id that is not in the workspace is domain.ErrNotFound.
	RepoScope(ctx context.Context, workspaceID, repo string) ([]domain.RepoRef, error)
	// WindowFacts returns the facts of the repositories for [win.ReadFrom(), win.To): one read
	// serves the window and its previous window. The membership each slice of domain.Facts must
	// satisfy is documented on domain.Facts; win.From splits the range into the two windows so
	// the store can return only the findings of the two snapshots. repoIDs is never empty.
	WindowFacts(ctx context.Context, workspaceID string, repoIDs []string, win domain.Window) (domain.Facts, error)
}
