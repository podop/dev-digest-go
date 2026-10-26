// Package app holds the use cases of the GitLab connections context (Settings > GitLab): list,
// create, update and delete a connection, test it, and resolve a host key to a connection for the
// other contexts (repos, pulls, auto reviews, clone readers). It reaches the database, the secrets
// file and GitLab only through the consumer-side ports below, which `cmd/server` wires.
package app

import (
	"context"

	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/gitlab"
)

// Store persists the connections. They are installation-wide (no workspace). Methods that lock
// (GetForUpdate) and every write run inside InTx.
type Store interface {
	// InTx runs fn in one transaction: it commits when fn returns nil and rolls back when it
	// returns an error. The ctx passed to fn carries the transaction, so a RepoCounter call made
	// with it reads inside the same transaction. A failed commit is the error of InTx.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error

	// List returns every connection; the order is not specified.
	List(ctx context.Context) ([]domain.Connection, error)
	// Get returns one connection; domain.ErrNotFound when no connection has the id.
	Get(ctx context.Context, id string) (domain.Connection, error)
	// GetForUpdate is Get that also locks the row FOR UPDATE until the transaction ends.
	GetForUpdate(ctx context.Context, id string) (domain.Connection, error)
	// GetByHost returns the connection for a host key; domain.ErrNotFound when there is none.
	GetByHost(ctx context.Context, host string) (domain.Connection, error)

	// Insert creates the connection. Under a lock that serializes the creates of the installation
	// it returns domain.ErrExists when a connection for c.Host exists, else domain.ErrLimit when
	// limit connections exist already (spec FR3, AC3: two concurrent creates give one 201).
	Insert(ctx context.Context, c domain.Connection, limit int) error
	// Replace overwrites Name, BaseURL and UpdatedAt of the stored connection; the id, the host
	// key and created_at never change. domain.ErrNotFound when missing.
	Replace(ctx context.Context, c domain.Connection) error
	// Delete removes the connection. domain.ErrNotFound when missing.
	Delete(ctx context.Context, id string) error
}

// Secrets is the token of each connection in the secrets file (platform/secrets.Store implements
// it): key gitlab_connections.<id>, no environment fallback. Errors never contain a value.
type Secrets interface {
	// GitLab returns the stored token; "" when none.
	GitLab(id string) string
	// SetGitLab replaces the token (an empty token removes it) and returns the previous one.
	SetGitLab(id, token string) (prev string, err error)
	// DeleteGitLab removes the token and returns what was stored.
	DeleteGitLab(id string) (prev string, err error)
	// RestoreGitLab puts prev back after a write whose transaction did not commit.
	RestoreGitLab(id, prev string) error
}

// RepoCounter counts the repositories of all workspaces bound to host keys (spec FR8: the repos
// whose provider is gitlab and whose host equals the key). Its Postgres adapter reads the repos
// table; called with the ctx of InTx's fn it reads inside that transaction.
type RepoCounter interface {
	// CountBound returns, for each given host key, the number of bound repositories. A host
	// without any is absent or 0.
	CountBound(ctx context.Context, hosts []string) (map[string]int, error)
}

// GitLab is the part of platform/gitlab.Client a connection test uses (spec FR10): three GETs on
// the connection's REST root, the token in PRIVATE-TOKEN, no redirect followed. The Conn's
// BaseURL is the REST root (<base_url>/api/v4). *gitlab.Client implements it.
type GitLab interface {
	User(ctx context.Context, conn gitlab.Conn) (gitlab.User, error)
	TokenSelf(ctx context.Context, conn gitlab.Conn) (gitlab.TokenInfo, error)
	Metadata(ctx context.Context, conn gitlab.Conn) (gitlab.Metadata, error)
}
