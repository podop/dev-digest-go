// Package executor runs eval suite runs in the background: per run it works through the run's
// cases (at most domain.CaseConcurrency at a time, each under domain.CaseBudget) on the review
// engine with the run's frozen configuration, scores each case with pure code, records every
// result with the run counters in one transaction and ends the run. It owns all the goroutines it
// starts, recovers a panic of any of them, and Shutdown aborts them and waits.
//
// This file is the package's contract: the dependencies, options and the shutdown error.
// The Executor type (New(Deps, Options) *Executor, Launch(ctx, domain.Run) implementing
// app.Launcher, Shutdown(ctx) error) is added by the executor step, one file per concern.
package executor

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ErrNotDrained is returned by Shutdown when some runs had not recorded themselves when the drain
// ended. They stay running in the database and are reaped to failed at the next start.
var ErrNotDrained = errors.New("eval runs did not finish recording in time")

// Store is the part of the persistence the executor uses; one implementation (adapters/postgres)
// serves it together with app.CaseStore and app.RunStore.
type Store interface {
	// GetCase loads a case of the workspace; domain.ErrNotFound when it is gone.
	GetCase(ctx context.Context, workspaceID, caseID string) (domain.Case, error)
	// RecordResult, DropRunCase and FinishRun are as in app.RunStore.
	RecordResult(ctx context.Context, workspaceID string, r domain.NewResult) error
	DropRunCase(ctx context.Context, workspaceID, runID string) error
	FinishRun(ctx context.Context, workspaceID, runID string, f domain.RunFinish) (bool, error)
}

// Engine is the review engine's entry point (reviewer/app.Run); the executor takes it as a
// dependency so tests can script the outcome.
type Engine func(ctx context.Context, in reviewerapp.Input, llm reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error)

// LLMBinding says which model a case's LLM port talks to. A case has no session id.
type LLMBinding struct {
	Provider string
	Model    string
	// RunID is the correlation id of the prompt log line (the suite run's id).
	RunID string
	// WorkspaceID and CaseID say who pays for the case's calls and which case they belong to; the
	// provider adapter turns them into the spend-ledger charge (kind eval_case, ref = the case id).
	WorkspaceID string
	CaseID      string
	// OnUsage receives the usage of every response, schema-invalid attempts included, as it
	// arrives; the executor sums it so an errored case keeps what it spent. It may be called
	// from several goroutines.
	OnUsage func(reviewer.Usage)
}

// Providers resolves a run's LLM provider.
type Providers interface {
	// Resolve returns the engine's LLM port bound to the case. It fails with an error whose text
	// is "<KEY_NAME> is not configured" when the provider's key is missing, without making a call.
	Resolve(ctx context.Context, b LLMBinding) (reviewerapp.LLM, error)
	// Redact scrubs provider keys out of a text; stored error texts pass through it first.
	Redact(s string) string
}

// Deps are the collaborators of an Executor; every field but Clock and Log is required.
type Deps struct {
	Store     Store
	Providers Providers
	// Engine is reviewer/app.Run in production.
	Engine Engine
	// Clock times the runs; nil = time.Now.
	Clock app.Clock
	// Log receives the executor's own lines (spec 7.12); nil = discard.
	Log *slog.Logger
}

// Options tune an Executor. Zero values select the defaults.
type Options struct {
	// CaseBudget is how long one case may take; 0 = domain.CaseBudget (720 s).
	CaseBudget time.Duration
	// Cases bounds the cases of one run in flight; 0 = domain.CaseConcurrency (3).
	Cases int
	// MapConcurrency is passed to the engine as the chunk calls in flight; 0 = the engine's default.
	MapConcurrency int
	// Drain is how long Shutdown waits for aborted runs to record themselves; 0 = domain.ShutdownDrain.
	Drain time.Duration
}
