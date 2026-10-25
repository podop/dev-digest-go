// Package app holds the use cases of the performance context: the cross-agent dashboard and one
// agent's stats. Both read per-run facts from the store and fold them with the domain's pure
// rules; a dashboard row equals the agent's stats for the same period. Nothing here writes or
// calls an LLM.
//
// This file holds the port the use cases depend on; api.go holds the use cases' own interface,
// which transport depends on.
package app

import (
	"context"
	"encoding/json"
	"time"

	"dev-digest-go/backend/internal/performance/domain"
)

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// Reader is the read-only persistence of the context (SELECT only; the adapter reads the tables
// of other contexts with its own row types). Every method is workspace-scoped and counts runs
// with one filter: status done, failed or cancelled, source local, ci or auto, ran_at inside the
// window ([From, To] when ToInclusive, else [From, To)). agentID "" means every agent; otherwise
// only that agent's runs.
type Reader interface {
	// ListAgents returns every agent of the workspace, enabled or not, in no particular order.
	ListAgents(ctx context.Context, workspaceID string) ([]domain.AgentRef, error)
	// FindAgent returns an agent of the workspace; domain.ErrNotFound otherwise.
	FindAgent(ctx context.Context, workspaceID, agentID string) (domain.AgentRef, error)
	// CountedRuns returns the counted runs of the window.
	CountedRuns(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]domain.RunFact, error)
	// FindingCounts returns the findings of those runs grouped by the run's agent.
	FindingCounts(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]domain.FindingCounts, error)
	// FindingFacts returns one agent's findings of its counted runs grouped per run by severity
	// and category.
	FindingFacts(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]domain.FindingFact, error)
	// SkillUsage returns the skills in one agent's counted runs with the number of runs that
	// had each (distinct runs).
	SkillUsage(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]domain.SkillFact, error)
	// MemoryPulled returns trace.memory_pulled of each of one agent's counted runs that has a
	// trace, as raw JSON (null when the key is absent); the domain extracts the texts.
	MemoryPulled(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]json.RawMessage, error)
	// RunHistory returns one agent's newest counted runs of the window, ran_at descending, then
	// id descending, at most limit.
	RunHistory(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string, limit int) ([]domain.RunHistoryItem, error)
}
