package executor

import (
	"context"
	"errors"
	"math"
	"strconv"

	"dev-digest-go/backend/internal/evals/domain"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// errCaseBudget is the cause of a case context that ran out of time.
var errCaseBudget = errors.New("case budget exceeded")

// runCase takes one case end to end: load, review on the engine under the case budget and the stop
// flag, score, record. counted is false for a case that contributes nothing (gone, or aborted by
// the shutdown); a non-nil err is a store failure that stops the run. A panic reaches the caller.
func (e *Executor) runCase(base context.Context, run domain.Run, caseID string) (outcome domain.CaseOutcome, counted bool, err error) {
	c, err := e.store.GetCase(base, run.WorkspaceID, caseID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.CaseOutcome{}, false, e.store.DropRunCase(base, run.WorkspaceID, run.ID)
	case err != nil:
		return domain.CaseOutcome{}, false, err
	}

	start := e.clock()
	usage := domain.NewCaseUsage()
	rec := domain.NewResult{RunID: run.ID, CaseID: caseID}
	score, dropped, rerr := e.review(base, run, c, usage)
	switch {
	case rerr == nil:
		rec.Status = score.Status
		rec.Findings = score.Findings
		rec.DroppedCount = dropped
		rec.HitCount, rec.ViolationCount, rec.UnlabeledCount = score.HitCount, score.ViolationCount, score.UnlabeledCount
	case e.stopCtx.Err() != nil:
		// Aborted by the shutdown: the run is interrupted and this case is not a result.
		return domain.CaseOutcome{}, false, nil
	default:
		msg := domain.ErrorText(rerr.Error(), e.redact)
		rec.Status = domain.CaseError
		rec.Findings = []domain.ResultFinding{}
		rec.Error = &msg
	}
	rec.DurationMS = e.sinceMS(start)
	rec.CostUSD = usage.Cost()

	recorded, err := e.record(base, run, rec)
	if err != nil || !recorded {
		return domain.CaseOutcome{}, false, err
	}
	return domain.CaseOutcome{
		Kind: c.Expectation.Kind, Status: rec.Status,
		HitCount: rec.HitCount, ViolationCount: rec.ViolationCount, UnlabeledCount: rec.UnlabeledCount,
		DroppedCount: rec.DroppedCount, CostUSD: rec.CostUSD, Error: rec.Error,
	}, true, nil
}

// review runs the case through the review engine with the fixed inputs of spec 7.5.4 (the case's
// diff and PR text, the run's frozen configuration; no memory, project context, intent, callers or
// repo map) under the case budget, and scores the kept findings with pure code. The usage of
// every response goes to usage, also when the review fails.
func (e *Executor) review(base context.Context, run domain.Run, c domain.Case, usage *domain.CaseUsage) (domain.CaseScore, int, error) {
	// ctx ends on the budget, on Shutdown, or when the case is done.
	ctx, cancel := context.WithCancelCause(base)
	defer cancel(nil)
	ctx, cancelBudget := context.WithTimeoutCause(ctx, e.opts.CaseBudget, errCaseBudget)
	defer cancelBudget()
	defer context.AfterFunc(e.stopCtx, func() { cancel(context.Canceled) })()

	llm, err := e.providers.Resolve(ctx, LLMBinding{
		Provider: run.Config.Provider,
		Model:    run.Config.Model,
		RunID:    run.ID,
		// The case's own workspace pays; its id is the ledger reference.
		WorkspaceID: c.WorkspaceID,
		CaseID:      c.ID,
		OnUsage:     func(u reviewer.Usage) { usage.Add(u.CostUSD) },
	})
	if err != nil {
		return domain.CaseScore{}, 0, e.budgetOr(ctx, err)
	}

	in := reviewerapp.Input{
		SystemPrompt: run.Config.SystemPrompt,
		Diff:         reviewer.ParseUnifiedDiff(c.InputDiff),
		Strategy:     strategyOf(run.Config.Strategy),
		Skills:       skillBlocks(run.Config.Skills),
		Task:         domain.TaskLine(c.InputMeta.PRNumber, c.InputMeta.PRTitle),
	}
	if d := c.InputMeta.PRDescription; d != nil {
		in.PRDescription = *d
	}
	outcome, err := e.engine(ctx, in, llm, nil, reviewerapp.Options{
		MapConcurrency: e.opts.MapConcurrency,
		Cancelled:      func() bool { return ctx.Err() != nil },
	})
	if err != nil {
		return domain.CaseScore{}, 0, e.budgetOr(ctx, err)
	}
	return domain.ScoreCase(c.Expectation, domain.ToKeptFindings(outcome.Review.Findings)), len(outcome.Dropped), nil
}

// timeoutError is the error of a case that ran out of time; its text is stored on the case.
type timeoutError struct{ seconds int }

func (t timeoutError) Error() string { return "Case timed out after " + strconv.Itoa(t.seconds) + "s" }

// budgetOr is err, or the budget error when the case ran out of time: the budget wins whatever
// error the abort surfaced as.
func (e *Executor) budgetOr(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), errCaseBudget) {
		return timeoutError{seconds: int(math.Round(e.opts.CaseBudget.Seconds()))}
	}
	return err
}

// strategyOf is the agent's stored strategy; an unknown one runs as auto.
func strategyOf(s string) reviewer.Strategy {
	switch st := reviewer.Strategy(s); st {
	case reviewer.StrategySinglePass, reviewer.StrategyMapReduce:
		return st
	}
	return reviewer.StrategyAuto
}

// skillBlocks renders the run's skills in snapshot order; nil when there are none.
func skillBlocks(skills []domain.SkillSnapshot) []string {
	if len(skills) == 0 {
		return nil
	}
	out := make([]string, len(skills))
	for i, s := range skills {
		out[i] = reviewer.SkillBlock(s.Name, s.Description, s.Body)
	}
	return out
}
