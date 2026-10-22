package executor

import (
	"context"
	"errors"
	"time"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// endInput describes a run that stopped without a persisted review.
type endInput struct {
	job  domain.Job
	pull domain.ReviewPull
	log  *runLog
	// start is when the run began; nil = it never ran (diff failure): duration 0.
	start  *time.Time
	ending domain.Ending
	// announce publishes the end event ("Run cancelled by user" / "Run failed: <note>").
	announce bool
	// usage is what the run spent so far; nil = nothing (zero tokens, zero cost).
	usage  *reviewer.Usage
	skills []domain.ReviewSkill
	pc     *domain.ContextTrace
	// intent is the request's intent trace when its pre-work ran before the run ended.
	intent *domain.IntentTrace
}

// endRun records a failed or cancelled run so that it and the reason survive a reload: the
// status moves to failed or cancelled only from running (the store keeps an earlier cancel and
// never touches a done run), the usage spent so far, the ended trace (whose log is the run's bus
// buffer) and the completed bus. Errors while recording are swallowed: the run may have been
// deleted meanwhile (EC4025).
func (e *Executor) endRun(ctx context.Context, in endInput) {
	if in.announce {
		in.log.errorf(domain.EndMsg(in.ending))
	}
	durationMS := 0
	if in.start != nil {
		durationMS = e.sinceMS(*in.start)
	}
	usage := domain.NoUsage()
	if in.usage != nil {
		usage = *in.usage
	}
	note := in.ending.Note
	err := e.store.EndRun(ctx, in.job.RunID, in.ending.Status, domain.RunCompletion{
		DurationMS:    durationMS,
		TokensIn:      usage.TokensIn,
		TokensOut:     usage.TokensOut,
		CostUSD:       usage.CostUSD,
		FindingsCount: 0,
		Grounding:     domain.NoGrounding,
		Error:         &note,
	})
	e.logRecordFailure(in.job.RunID, "end run", err)

	trace := domain.EndedTrace(domain.EndedTraceInput{
		Agent:          in.job.Agent,
		PRNumber:       in.pull.Number,
		Log:            domain.LogFromEvents(e.bus.Buffer(in.job.RunID)),
		DurationMS:     durationMS,
		Usage:          &usage,
		SkillsUsed:     domain.SkillsUsed(in.skills),
		ProjectContext: in.pc,
		Intent:         in.intent,
	})
	e.logRecordFailure(in.job.RunID, "save ended trace", e.store.SaveTrace(ctx, in.job.RunID, trace))
	e.bus.Complete(in.job.RunID)
}

// logRecordFailure notes a swallowed recording error at debug level, except the expected
// not-found of a deleted run. The text is redacted and cut at its first line.
func (e *Executor) logRecordFailure(runID, what string, err error) {
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		return
	}
	e.log.Debug("review: recording a run failed", "runId", runID, "step", what, "err", e.logText(err.Error()))
}
