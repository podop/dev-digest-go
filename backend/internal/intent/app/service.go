package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/inflight"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// OpRefresh is the operation name a refresh reports to the spend cap check (spec FR13).
const OpRefresh = "intent_refresh"

// budget is the consumer-side spend cap port (spec FR13). Admit returns nil when the operation may
// start, the 402 spend_cap_reached error when the monthly cap is reached, and any other error
// when the spend cannot be read.
type budget interface {
	Admit(ctx context.Context, workspaceID, op string) error
}

// Service implements the intent use cases: API for the routes, Resolver for the review pre-work
// and Stopper for shutdown. Its only state is the registry of in-flight derivations.
type Service struct {
	d       Deps
	clock   Clock
	log     *slog.Logger
	flights *inflight.Flights[Result, event]
}

var (
	_ API      = (*Service)(nil)
	_ Resolver = (*Service)(nil)
	_ Stopper  = (*Service)(nil)
)

// NewService builds the service. A missing required collaborator is a wiring error and panics.
func NewService(d Deps, o Options) *Service {
	if d.Pulls == nil || d.Settings == nil || d.LLM == nil || d.Docs == nil || d.Tickets == nil || d.Store == nil {
		panic("intent/app: NewService needs Pulls, Settings, LLM, Docs, Tickets and Store")
	}
	budget := o.Budget
	if budget <= 0 {
		budget = domain.Budget
	}
	s := &Service{
		d:     d,
		clock: d.Clock,
		log:   d.Log,
		flights: inflight.NewFlights[Result, event](inflight.FlightsConfig{
			Budget:    budget,
			BudgetErr: fmt.Errorf("intent derivation exceeded its %dms budget", budget.Milliseconds()),
		}),
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

// Shutdown cancels every in-flight derivation, waits for them within ctx and refuses new ones
// (they end unavailable). cmd calls it before Echo drains, because Echo never cancels the context
// of a request in flight (planner decision P2).
func (s *Service) Shutdown(ctx context.Context) error { return s.flights.Shutdown(ctx) }

// InFlight is the number of derivations running now (zero when every one has ended and been
// removed).
func (s *Service) InFlight() int { return s.flights.Len() }

// Get answers GET /pulls/:id/intent.
func (s *Service) Get(ctx context.Context, workspaceID, prID string) (Response, error) {
	pull, err := s.pull(ctx, workspaceID, prID)
	if err != nil {
		return Response{}, err
	}
	rec, ok, err := s.d.Store.Get(ctx, prID)
	if err != nil {
		return Response{}, fmt.Errorf("read intent: %w", err)
	}
	if !ok {
		return Response{}, nil
	}
	repo, err := s.d.Pulls.Repo(ctx, pull.RepoID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return Response{Intent: &rec}, nil
	case err != nil:
		return Response{}, fmt.Errorf("read repo: %w", err)
	}
	return Response{Intent: &rec, Stale: s.stale(ctx, workspaceID, pull, repo, rec)}, nil
}

// Refresh answers POST /pulls/:id/intent/refresh. The spend cap is checked once, after the 404
// checks and before the derivation; its error is returned unchanged. Only this user-triggered
// route is gated: the derivations of Resolve (review pre-work) are not (spec FR13).
func (s *Service) Refresh(ctx context.Context, workspaceID, prID string) (Response, error) {
	pull, err := s.pull(ctx, workspaceID, prID)
	if err != nil {
		return Response{}, err
	}
	repo, err := s.d.Pulls.Repo(ctx, pull.RepoID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return Response{}, apperr.NotFound("Repo not found")
	case err != nil:
		return Response{}, fmt.Errorf("read repo: %w", err)
	}
	files, err := s.d.Pulls.ChangedFiles(ctx, prID)
	if err != nil {
		return Response{}, fmt.Errorf("read changed files: %w", err)
	}
	if s.d.Budget != nil {
		if err := s.d.Budget.Admit(ctx, workspaceID, OpRefresh); err != nil {
			return Response{}, err
		}
	}
	// Node passes no signal here: the derivation finishes and is stored even when the client has
	// gone. WithoutCancel keeps the values; the flight still ends on its budget and on Shutdown.
	res := s.Resolve(context.WithoutCancel(ctx), ResolveInput{WorkspaceID: workspaceID, Pull: pull, Repo: repo, ChangedFiles: files, Force: true}, nil)
	if !res.Used {
		return Response{}, apperr.ExternalService("Intent derivation failed: " + res.Warning).WithCode("intent_unavailable")
	}
	return Response{Intent: &res.Record}, nil
}

// Stored returns the pull request's stored intent for the brief.
func (s *Service) Stored(ctx context.Context, prID string) (*reviewer.Intent, error) {
	rec, ok, err := s.d.Store.Get(ctx, prID)
	if err != nil {
		return nil, fmt.Errorf("read intent: %w", err)
	}
	if !ok {
		return nil, nil
	}
	snap := rec.Snapshot()
	return &snap, nil
}

// pull reads a pull request of the workspace; a missing one is 404.
func (s *Service) pull(ctx context.Context, workspaceID, prID string) (domain.Pull, error) {
	pull, err := s.d.Pulls.Pull(ctx, workspaceID, prID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.Pull{}, apperr.NotFound("Pull request not found")
	case err != nil:
		return domain.Pull{}, fmt.Errorf("read pull request: %w", err)
	}
	return pull, nil
}
