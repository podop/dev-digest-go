// Package tenancy resolves the single-tenant context every request runs in: the workspace
// named "default" and the system user "you@local". There is no authentication; the context
// never depends on request headers or cookies.
package tenancy

import (
	"context"
	"errors"
	"fmt"
)

// Names of the rows that form the single-tenant context. The seed creates them.
const (
	DefaultWorkspaceName = "default"
	SystemUserEmail      = "you@local"
	SystemUserName       = "You"
)

// ErrNotProvisioned means the default workspace or the system user does not exist, for
// example on a migrated database that was never seeded.
var ErrNotProvisioned = errors.New("default workspace or system user not found")

// Identity is who a request acts as. Both ids are UUID strings.
type Identity struct {
	WorkspaceID string
	UserID      string
}

// store looks the identity rows up by their natural keys. It returns ErrNotProvisioned
// when either row is missing.
type store interface {
	FindIdentity(ctx context.Context, workspaceName, userEmail string) (Identity, error)
}

// Resolver finds the context of a request.
type Resolver struct {
	store store
}

// NewResolver builds a Resolver over a store (see adapters/postgres).
func NewResolver(s store) *Resolver { return &Resolver{store: s} }

// Resolve returns the identity of the default workspace and the system user. It reads the
// database on every call, so rows created after startup (a late seed) are picked up.
// A missing row is a plain error wrapping ErrNotProvisioned, which the HTTP layer renders
// as 500 internal_error.
func (r *Resolver) Resolve(ctx context.Context) (Identity, error) {
	id, err := r.store.FindIdentity(ctx, DefaultWorkspaceName, SystemUserEmail)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve tenancy context: %w", err)
	}
	return id, nil
}
