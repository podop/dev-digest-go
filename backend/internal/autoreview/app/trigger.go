package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"dev-digest-go/backend/internal/autoreview/domain"
)

// AfterCycle runs one evaluation (FR3). It never fails its caller: errors are logged, a panic is
// recovered (P10), and a cancelled ctx ends it promptly (NFR5).
func (s *Service) AfterCycle(ctx context.Context, workspaceID string) {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(context.WithoutCancel(ctx), "auto review evaluation panicked",
				"workspace_id", workspaceID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	if err := s.evaluate(ctx, workspaceID); err != nil && ctx.Err() == nil {
		s.log.WarnContext(ctx, "auto review evaluation stopped", "workspace_id", workspaceID, "error", err)
	}
}

// evaluation is the state of one AfterCycle.
type evaluation struct {
	ws      string
	setting domain.Setting
	agents  []string
	budget  domain.Budget
	// lastHour is the number of `started` records of the rolling hour when the evaluation began.
	lastHour int
	// started is the number of reviews this evaluation has started.
	started int
}

func (s *Service) evaluate(ctx context.Context, ws string) error {
	d := s.deps
	setting, err := d.Settings.AutomaticReviews(ctx, ws)
	if err != nil {
		return fmt.Errorf("read setting: %w", err)
	}
	if !setting.Armed() || setting.EnabledAt == nil {
		return nil
	}
	cands, err := d.Store.Candidates(ctx, ws, *setting.EnabledAt)
	if err != nil {
		return fmt.Errorf("read candidates: %w", err)
	}
	if len(cands) == 0 {
		return nil
	}
	enabled, err := d.Store.EnabledAgents(ctx, ws, setting.AgentIDs)
	if err != nil {
		return fmt.Errorf("read agents: %w", err)
	}
	lastHour, err := d.Store.StartedSince(ctx, ws, domain.RateWindowStart(s.now()))
	if err != nil {
		return fmt.Errorf("read started count: %w", err)
	}
	ev := &evaluation{
		ws:       ws,
		setting:  setting,
		agents:   domain.EffectiveAgents(setting.AgentIDs, enabled),
		lastHour: lastHour,
	}
	for _, c := range cands {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.decide(ctx, ev, c); err != nil {
			return err
		}
	}
	return nil
}

// decide applies domain.Decide to one candidate and carries the decision out. The budget is asked
// at most once per evaluation (A2). An error ends the evaluation: it is a store failure.
func (s *Service) decide(ctx context.Context, ev *evaluation, c domain.Candidate) error {
	dec := domain.Decide(c, ev.setting, ev.agents, ev.budget, domain.RateLeft(ev.lastHour, ev.started))
	if dec.Action == domain.ActionAskBudget {
		ev.budget = s.askBudget(ctx, ev.ws)
		dec = domain.Decide(c, ev.setting, ev.agents, ev.budget, domain.RateLeft(ev.lastHour, ev.started))
	}
	switch dec.Action {
	case domain.ActionRecord:
		return s.record(ctx, ev.ws, c, dec)
	case domain.ActionStart:
		return s.start(ctx, ev, c, dec.Event)
	default: // deferred: no record, looked at again next cycle
		s.log.DebugContext(ctx, "auto review deferred", s.fields(ev.ws, c, dec.Event)...)
		return nil
	}
}

// askBudget asks the spend cap. A failed check is not an answer the cap gave, so it defers the
// evaluation's starts (fail closed).
func (s *Service) askBudget(ctx context.Context, ws string) domain.Budget {
	switch err := s.deps.Budget.Admit(ctx, ws); {
	case err == nil:
		return domain.BudgetAllowed
	case errors.Is(err, ErrCapReached):
		return domain.BudgetDenied
	default:
		if ctx.Err() == nil {
			s.log.WarnContext(ctx, "auto review budget check failed", "workspace_id", ws, "error", err)
		}
		return domain.BudgetError
	}
}

// record stores a decision that starts nothing.
func (s *Service) record(ctx context.Context, ws string, c domain.Candidate, dec domain.Decision) error {
	dctx, cancel := detached(ctx)
	defer cancel()
	rec := s.recordOf(ws, c, dec.Event, dec.Outcome)
	claimed, err := s.deps.Store.Claim(dctx, rec)
	if err != nil {
		return fmt.Errorf("record %s: %w", dec.Outcome, err)
	}
	if claimed {
		s.logDecision(dctx, rec, c)
	}
	return nil
}

// start claims the head as started/0 before the review starts (FR8: a stop in between leaves the
// head unreviewed rather than reviewed twice), starts it, then settles the record (A4).
func (s *Service) start(ctx context.Context, ev *evaluation, c domain.Candidate, event domain.Event) error {
	dctx, cancel := detached(ctx)
	defer cancel()
	rec := s.recordOf(ev.ws, c, event, domain.OutcomeStarted)
	claimed, err := s.deps.Store.Claim(dctx, rec)
	if err != nil {
		return fmt.Errorf("claim %s: %w", c.PRID, err)
	}
	if !claimed { // another evaluation decided this head first
		return nil
	}
	runs, startErr := s.deps.Starter.StartAuto(dctx, ev.ws, c.PRID, ev.agents)
	switch {
	case startErr == nil:
		rec.RunsStarted = runs
		ev.started++
	case errors.Is(startErr, ErrCapReached):
		rec.Outcome, rec.RunsStarted = domain.OutcomeSkippedSpendCap, 0
		ev.budget = domain.BudgetDenied
	default:
		rec.Outcome, rec.RunsStarted = domain.OutcomeFailed, 0
		s.log.WarnContext(dctx, "auto review start failed", append(s.fields(ev.ws, c, event), "error", startErr)...)
	}
	if err := s.deps.Store.Finish(dctx, ev.ws, c.PRID, c.HeadSHA, rec.Outcome, rec.RunsStarted); err != nil {
		return fmt.Errorf("settle %s: %w", rec.Outcome, err)
	}
	s.logDecision(dctx, rec, c)
	return nil
}

func (s *Service) recordOf(ws string, c domain.Candidate, event domain.Event, outcome domain.Outcome) domain.Record {
	return domain.Record{
		WorkspaceID: ws,
		PRID:        c.PRID,
		HeadSHA:     c.HeadSHA,
		Event:       event,
		Outcome:     outcome,
		DecidedAt:   s.now(),
	}
}

// logDecision writes the one INFO line of a decision (NFR3): identifiers and counts only, never a
// token, key or prompt text.
func (s *Service) logDecision(ctx context.Context, rec domain.Record, c domain.Candidate) {
	s.log.InfoContext(ctx, "auto review decision",
		append(s.fields(rec.WorkspaceID, c, rec.Event), "outcome", string(rec.Outcome), "runs", rec.RunsStarted)...)
}

func (s *Service) fields(ws string, c domain.Candidate, event domain.Event) []any {
	sha := c.HeadSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return []any{
		slog.String("workspace_id", ws),
		slog.String("pr_id", c.PRID),
		slog.String("repo", c.Repo),
		slog.Int("number", c.Number),
		slog.String("head_sha", sha),
		slog.String("event", string(event)),
	}
}
