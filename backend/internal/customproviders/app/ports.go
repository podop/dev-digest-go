// Package app holds the use cases of the custom model providers context (Settings > API Keys >
// Custom providers): list, get, create, update and delete a provider, test its connection, list
// its models, and resolve it for an LLM call. It reaches the database, the secrets file, the
// agents and settings that may reference a provider, and the HTTP adapters only through the
// consumer-side ports below, which `cmd/server` wires.
package app

import (
	"context"

	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/secrets"
)

// Store persists the providers of a workspace and their manual models. Provider ids are the public
// ids (`custom:<slug>`, checked with custom.ParseID before the call); the store keeps the bare slug.
// Methods that need a lock (GetForUpdate, LockDefined) and every write run inside InTx.
type Store interface {
	// InTx runs fn in one transaction: it commits when fn returns nil and rolls back when it
	// returns an error. The ctx passed to fn carries the transaction, so a References call made
	// with it reads inside the same transaction. A failed commit is returned as the error of InTx.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error

	// List returns every provider of the workspace with its manual models in their stored order.
	// The order of the providers is not specified.
	List(ctx context.Context, workspaceID string) ([]domain.Provider, error)
	// Get returns one provider; domain.ErrNotFound when the workspace does not define it.
	Get(ctx context.Context, workspaceID, id string) (domain.Provider, error)
	// GetForUpdate is Get that also locks the provider row FOR UPDATE until the transaction ends
	// (a delete or update waits for agent writes that hold it FOR SHARE, and vice versa).
	GetForUpdate(ctx context.Context, workspaceID, id string) (domain.Provider, error)
	// Exists reports whether the workspace defines the provider.
	Exists(ctx context.Context, workspaceID, id string) (bool, error)
	// LockDefined is Exists that also locks the provider row FOR SHARE until the transaction
	// ends, so an agent or settings write cannot reference a provider a concurrent delete is
	// removing. Called by the agents and settings validators (spec FR6, FR7; A8).
	LockDefined(ctx context.Context, workspaceID, id string) (bool, error)

	// Insert creates the provider and its manual models (p.Models in order). Under a lock that
	// serializes the creates of the workspace it returns domain.ErrExists when the id is taken,
	// else domain.ErrLimit when the workspace already has limit providers.
	Insert(ctx context.Context, workspaceID string, p domain.Provider, limit int) error
	// Replace overwrites every field of the stored provider except its id and created_at, and
	// replaces its manual models (in order), using p.UpdatedAt. domain.ErrNotFound when missing.
	Replace(ctx context.Context, workspaceID string, p domain.Provider) error
	// Delete removes the provider; its manual models go with it. domain.ErrNotFound when missing.
	Delete(ctx context.Context, workspaceID, id string) error
}

// Secrets is the key and extra-header values of a provider in the secrets file
// (platform/secrets.Store implements it). The secrets are keyed by workspace and public provider
// id; there is no environment fallback (spec D22). Errors never contain a value.
type Secrets interface {
	// Custom returns what is stored; the zero CustomSecret when nothing is.
	Custom(workspaceID, id string) secrets.CustomSecret
	// SetCustom replaces the entry (a zero value removes it) and returns the previous one.
	SetCustom(workspaceID, id string, sec secrets.CustomSecret) (prev secrets.CustomSecret, err error)
	// DeleteCustom removes the entry and returns what was stored.
	DeleteCustom(workspaceID, id string) (prev secrets.CustomSecret, err error)
	// RestoreCustom puts prev back after a write whose transaction did not commit.
	RestoreCustom(workspaceID, id string, prev secrets.CustomSecret) error
}

// References tells what uses a provider; `cmd` adapts the agents and settings stores to it.
type References interface {
	// Find returns the agents (enabled or not), feature_models entries and default_model that name
	// the provider. It runs inside the delete's transaction (ctx carries it), after the provider
	// row is locked; the settings part is best effort (A8). Agents are listed in a stable order.
	Find(ctx context.Context, workspaceID, id string) (domain.Usage, error)
	// DefaultModel returns the model of default_model when its provider is id, else ok false. The
	// connection test checks that model (spec FR11). A failure is logged and the test goes on.
	DefaultModel(ctx context.Context, workspaceID, id string) (model string, ok bool, err error)
}

// Adapters builds the HTTP adapter of a provider: `cmd` binds it to openaicompat.NewCustom or
// anthropic.NewCustom by protocol, with the egress client (no proxy, no redirects, address guard)
// and the extra CA. The adapter carries the base URL, auth mode and header, key and extra headers,
// so it is built per call and never shared. It lists models with exactly one GET and sends no
// completion on ListModels; errors keep StatusCode() and contain no secret.
type Adapters interface {
	Build(p domain.Provider, sec secrets.CustomSecret) (llm.Adapter, error)
}
