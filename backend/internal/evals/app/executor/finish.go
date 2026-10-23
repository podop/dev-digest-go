package executor

import (
	"context"

	"dev-digest-go/backend/internal/evals/domain"
)

// verdict is how the run ends (spec 7.5.7): failed with the store error text when a store failure
// stopped it, failed "interrupted" when the executor is shutting down, else by its outcomes
// (failed when every case errored; completed otherwise, also with no case left).
func (e *Executor) verdict(x *runExec) domain.RunVerdict {
	x.mu.Lock()
	crash := x.crash
	x.mu.Unlock()
	switch {
	case crash != nil:
		msg := domain.ErrorText(crash.Error(), e.redact)
		return domain.RunVerdict{Status: domain.RunFailed, Error: &msg}
	case e.stopCtx.Err() != nil:
		msg := domain.InterruptedError
		return domain.RunVerdict{Status: domain.RunFailed, Error: &msg}
	}
	return domain.Verdict(x.outcomes())
}

// finish writes the run's final state and logs how it ended. The write happens only while the run
// is still running (a run reaped meanwhile keeps its reason) and uses the launch context, which a
// shutdown does not cancel. A failed write is logged, never returned.
func (e *Executor) finish(base context.Context, x *runExec, v domain.RunVerdict) {
	totals := domain.AggregateRun(x.outcomes())
	f := domain.RunFinish{
		Status:       v.Status,
		Error:        v.Error,
		FinishedAt:   e.clock(),
		DurationMS:   e.sinceMS(x.start),
		Metrics:      totals.Metrics,
		CasesPassed:  totals.CasesPassed,
		CasesErrored: totals.CasesErrored,
	}
	run := x.run
	e.log.Info("eval run finished",
		"runId", run.ID, "ownerKind", string(run.OwnerKind), "skillId", ptrAny(run.SkillID),
		"skillVersion", ptrAny(run.SkillVersion), "agentId", run.AgentID, "status", string(f.Status),
		"durationMs", f.DurationMS, "costUsd", ptrAny(f.Metrics.CostUSD), "recall", ptrAny(f.Metrics.Recall),
		"precision", ptrAny(f.Metrics.Precision), "citationAccuracy", ptrAny(f.Metrics.CitationAccuracy),
		"casesPassed", f.CasesPassed, "casesErrored", f.CasesErrored)
	if _, err := e.store.FinishRun(base, run.WorkspaceID, run.ID, f); err != nil {
		e.log.Error("eval run: could not record the final state", "runId", run.ID, "err", e.logText(err.Error()))
	}
}
