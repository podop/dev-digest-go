// Package app holds the use cases of the brief context: reading a pull request's stored brief and
// generating one (facts, one structured model call, normalisation, storage), at most one
// generation per pull request at a time.
//
// This file holds the ports the use cases depend on; api.go holds the use cases' own interface,
// which transport depends on. The peers adapters bind the ports of other contexts in cmd/server.
package app

import (
	"context"
	"encoding/json"
	"time"

	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/llm"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// Store is the persistence of the context. The adapter reads the tables of other contexts with its
// own row types.
type Store interface {
	// FindPull returns a pull request of the workspace with its repository; domain.ErrNotFound
	// otherwise.
	FindPull(ctx context.Context, workspaceID, prID string) (domain.Pull, error)
	// PullFiles returns the pull request's stored files ordered by path in the database default
	// collation, then id.
	PullFiles(ctx context.Context, prID string) ([]domain.StoredFile, error)
	// CurrentReviews returns the newest kind='review' review of each agent of the pull request
	// (NULL agents one group, ties by id), newest first, each with its findings (dismissed
	// included) ordered by file in the database default collation, start line, id.
	CurrentReviews(ctx context.Context, prID string) ([]domain.ReviewFacts, error)
	// Brief returns the stored brief; ok is false when there is none and also when the stored
	// document no longer satisfies the contract (limits included): it reads as absent, never as an
	// error.
	Brief(ctx context.Context, prID string) (b domain.Brief, ok bool, err error)
	// SaveBrief upserts the brief of the pull request (pr_brief.pr_id, json); the adapter
	// sanitises text for jsonb.
	SaveBrief(ctx context.Context, prID string, b domain.Brief) error
}

// Intents reads the stored PR intent (the peers adapter over the intent context).
type Intents interface {
	// Stored returns the pull request's stored intent; nil, nil when there is none.
	Stored(ctx context.Context, prID string) (*reviewer.Intent, error)
}

// Blast reads the blast radius of changed paths (the peers adapter over the blast context's
// RadiusFor, which writes the blast info log line).
type Blast interface {
	RadiusFor(ctx context.Context, repoID string, changed []string) (domain.Blast, error)
}

// DocumentsInput identifies the documents one reviewed agent attaches.
type DocumentsInput struct {
	WorkspaceID string
	Pull        domain.Pull
	AgentID     string
}

// Documents resolves the project-context documents of a reviewed agent: its enabled linked
// skills in link order, then the project-context run resolution (base commit reads, 16,000-token
// budget) as a Phase 4 run does (the peers adapter over the reviews context). Documents come in
// prompt order; the service merges them across reviews, first path wins.
type Documents interface {
	ForAgent(ctx context.Context, in DocumentsInput) ([]domain.Document, error)
}

// Issues reads a linked GitHub issue (the peers adapter over platform/github; a missing token is
// an error). The service races it against domain.IssueTimeout and treats every failure as a
// missing input.
type Issues interface {
	Issue(ctx context.Context, owner, name string, number int) (domain.Issue, error)
}

// GitLabIssues reads a linked issue of a GitLab project (the peers adapter over platform/gitlab,
// resolving the repository's connection by its host key; spec B FR23, FR25). An unknown
// connection, a missing token and every GitLab failure are errors, and then no GitHub request is
// made. The service races it against domain.IssueTimeout and treats every failure as a missing
// input.
type GitLabIssues interface {
	Issue(ctx context.Context, host string, projectID int64, number int) (domain.Issue, error)
}

// Roles tells the smart-diff role of a path of a repository of the given provider (the peers
// adapter over the smart-diff context's classification): "core", "tests", "wiring", "docs" or
// "boilerplate". The provider is the pull request's ("github", "gitlab"; "" reads as github);
// GitLab repositories also hold their CI files in "wiring" (spec B FR32).
type Roles interface {
	RoleFor(provider, path string) string
}

// LLM is the part of *llm.Client the generation uses.
type LLM interface {
	Structured(ctx context.Context, req llm.Request) (llm.Result, error)
}

// FeatureModels reads the workspace's settings bag (the settings app); the service resolves the
// risk_brief model with llm.ResolveFeatureModel.
type FeatureModels interface {
	Get(ctx context.Context, workspaceID string) (map[string]json.RawMessage, error)
}
