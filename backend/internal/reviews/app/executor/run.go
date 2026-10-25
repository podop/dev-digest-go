package executor

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// runState is one agent run in flight.
type runState struct {
	job  domain.Job
	pull domain.ReviewPull
	repo domain.ReviewRepo
	wsID string
	// source is the batch's run source (empty = local).
	source string
	diff   reviewer.Diff
	log    *runLog
	start  time.Time
	// meter sums the usage of every LLM response: the only record of what a failed or
	// cancelled run spent.
	meter *domain.UsageMeter
	// runCtx ends when the run is cancelled; it bounds the calls that may take long (provider,
	// repo intel, project context, the engine). The database writes use the batch context, which
	// a cancel does not end: a cancelled run still records itself.
	runCtx      context.Context
	isCancelled func() bool

	skills []domain.ReviewSkill
	pc     *domain.ContextTrace
	// intent and intentTrace are the request's shared intent pre-work (spec 7.5), read-only.
	intent      *reviewer.Intent
	intentTrace *domain.IntentTrace
}

// runJob executes one queued agent run and logs how it went; it never panics.
func (e *Executor) runJob(ctx context.Context, br *batchRun, job domain.Job) {
	start := e.clock()
	agent := job.Agent
	log := br.shared.forRun(job.RunID, "agent", agent.Name)
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("review: run crashed", "runId", job.RunID, "agent", agent.Name, "panic", e.logText(panicText(r)), "stack", string(debug.Stack()))
		}
	}()
	// The run may have waited long for its agent slot, and the bus forgets a cancelled run after
	// its TTL: without a cancel flag the row is the truth about whether the run may still start.
	if e.endedWhileQueued(ctx, br, job, log) {
		return
	}
	e.log.Info(fmt.Sprintf("review: agent %q started (%s/%s)", agent.Name, agent.Provider, agent.Model),
		"runId", job.RunID, "agent", agent.Name, "provider", agent.Provider, "model", agent.Model, "prId", br.b.Pull.ID)

	// A run whose request arrived after Shutdown began has no cancel flag on the bus yet.
	if e.isStopped() {
		e.bus.Cancel(job.RunID)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := e.bus.OnCancel(job.RunID, cancel)
	defer stop()

	st := &runState{
		job: job, pull: br.b.Pull, repo: br.b.Repo, wsID: br.b.WorkspaceID, source: br.b.Source, diff: br.diff, log: log,
		start: start, meter: domain.NewUsageMeter(), runCtx: runCtx, intent: br.intent, intentTrace: br.intentTrace,
		isCancelled: func() bool { return runCtx.Err() != nil || e.bus.IsCancelled(job.RunID) },
	}

	findings, err := e.execute(ctx, st)
	if err == nil {
		e.log.Info(fmt.Sprintf("review: agent %q done - %d finding(s)", agent.Name, findings),
			"runId", job.RunID, "agent", agent.Name, "findings", findings, "durationMs", e.sinceMS(start))
		return
	}
	ending := domain.EndingFor(err, st.isCancelled())
	ending.Note = e.redact(ending.Note)
	e.endRun(ctx, endInput{
		job: job, pull: st.pull, source: br.b.Source, log: log, start: &start, ending: ending, announce: true,
		usage: ptr(st.meter.Snapshot()), skills: st.skills, pc: st.pc, intent: st.intentTrace,
	})
	if ending.Status == domain.StatusCancelled {
		e.log.Info(fmt.Sprintf("review: agent %q cancelled", agent.Name),
			"runId", job.RunID, "agent", agent.Name, "durationMs", e.sinceMS(start))
		return
	}
	e.log.Error(fmt.Sprintf("review: agent %q failed", agent.Name),
		"runId", job.RunID, "agent", agent.Name, "err", firstLine(ending.Note), "durationMs", e.sinceMS(start))
}

// endedWhileQueued handles a run whose row is no longer running although the bus holds no cancel
// flag for it (the flag was lost with the bus state, 5 minutes after CancelRun): it must make no
// provider, LLM, repo-intel or project-context call. A cancelled row gets the normal cancelled
// ending (event, ended trace, completed bus; the store keeps its status and note); a failed, done
// or deleted run is left alone and re-creates no bus state. A run with a flag takes the normal
// path, whose cancel checkpoints end it (EC4021). A failed read lets the run start: the
// conditional status updates still guard its ending.
func (e *Executor) endedWhileQueued(ctx context.Context, br *batchRun, job domain.Job, log *runLog) bool {
	if e.bus.IsCancelled(job.RunID) {
		return false
	}
	run, err := e.store.FindRun(ctx, br.b.WorkspaceID, job.RunID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		e.log.Info("review: run skipped, it was deleted while queued", "runId", job.RunID, "agent", job.Agent.Name)
		return true
	case err != nil:
		e.log.Debug("review: reading the queued run failed, starting it", "runId", job.RunID, "err", e.logText(err.Error()))
		return false
	case run.Status == domain.StatusRunning:
		return false
	case run.Status == domain.StatusCancelled:
		e.log.Info("review: run skipped, it was cancelled while queued", "runId", job.RunID, "agent", job.Agent.Name)
		e.endRun(ctx, endInput{
			job: job, pull: br.b.Pull, source: br.b.Source, log: log,
			ending: domain.EndingFor(domain.ErrRunCancelled, true), announce: true, intent: br.intentTrace,
		})
		return true
	}
	e.log.Info("review: run skipped, it is no longer running", "runId", job.RunID, "agent", job.Agent.Name, "status", string(run.Status))
	return true
}

// execute is one agent's review of the pull request; it returns the persisted finding count. On
// error the caller records the ending. A panic becomes a generic internal error.
func (e *Executor) execute(ctx context.Context, st *runState) (findings int, err error) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("review: run panicked", "runId", st.job.RunID, "panic", e.logText(panicText(r)), "stack", string(debug.Stack()))
			findings, err = 0, panicError{}
		}
	}()
	agent := st.job.Agent
	st.log.info(domain.AgentStartMsg(agent.Name, agent.Provider, agent.Model), nil)

	if err := e.attachSkills(ctx, st); err != nil {
		return 0, err
	}
	specs := e.attachProjectContext(st)

	outcome, err := e.review(st, specs)
	if err != nil {
		return 0, err
	}
	// Last in-memory checkpoint: a cancel that arrived during the final LLM call still wins. A
	// later one is caught by the conditional status update inside the persist transaction.
	if st.isCancelled() {
		return 0, domain.ErrRunCancelled
	}

	reviewID, durationMS, err := e.persist(ctx, st, outcome)
	if err != nil {
		return 0, err
	}
	kept := len(outcome.Review.Findings)
	st.log.result(domain.PersistedMsg(reviewID, kept))
	trace := domain.CompletedTrace(domain.CompletedTraceInput{
		Agent:          agent,
		PRNumber:       st.pull.Number,
		Source:         st.source,
		DurationMS:     durationMS,
		Usage:          outcome.Usage,
		Findings:       kept,
		Grounding:      outcome.Grounding,
		Assembly:       outcome.Assembly,
		ChunkLabels:    outcome.ChunkLabels,
		Mode:           outcome.Mode,
		Raw:            outcome.Raw,
		Log:            domain.LogFromEvents(e.bus.Buffer(st.job.RunID)),
		SkillsUsed:     domain.SkillsUsed(st.skills),
		ProjectContext: st.pc,
		Intent:         st.intentTrace,
	})
	st.log.info(domain.RunCompleteMsg, nil)
	if err := e.store.SaveTrace(ctx, st.job.RunID, trace); err != nil {
		return 0, fmt.Errorf("save trace: %w", err)
	}
	e.bus.Complete(st.job.RunID)
	return kept, nil
}

// attachSkills loads the agent's enabled skills (link order), records them on the run at their
// exact versions and logs what the prompt carries.
func (e *Executor) attachSkills(ctx context.Context, st *runState) error {
	skills, err := e.store.EnabledSkills(ctx, st.job.Agent.ID)
	if err != nil {
		return err // not wrapped: the failed run's note is the lookup's own error text
	}
	if len(skills) == 0 {
		return nil
	}
	if err := e.store.RecordRunSkills(ctx, st.job.RunID, skills); err != nil {
		return fmt.Errorf("record run skills: %w", err)
	}
	st.skills = skills
	tokens := domain.SkillsTokens(domain.SkillBlocks(skills))
	repeats := strategyOf(st.job.Agent) != reviewer.StrategySinglePass
	st.log.info(domain.SkillsLogLine(skills, tokens, repeats), domain.SkillsEventData(skills))
	return nil
}

// attachProjectContext resolves the attached documents and logs the one summary line. It returns
// the included documents for the prompt; nothing attached (or a failure, which is a warning
// event) yields none and no trace record. It never fails the run.
func (e *Executor) attachProjectContext(st *runState) []reviewer.ProjectDoc {
	res, err := e.projCtx.ResolveForRun(st.runCtx, app.ProjectContextInput{
		WorkspaceID: st.wsID,
		Repo:        st.repo,
		Base:        st.pull.Base,
		HeadSHA:     st.pull.HeadSHA,
		AgentID:     st.job.Agent.ID,
		Skills:      st.skills,
	})
	if err != nil {
		if !st.isCancelled() {
			st.log.info(domain.ProjectContextUnavailableMsg(err.Error()), domain.ProjectContextWarningData())
		}
		return nil
	}
	if len(res.Docs) == 0 {
		return nil
	}
	st.pc = domain.NewContextTrace(res.Docs, res.TokensTotal)
	st.log.info(res.LogLine, nil)
	var specs []reviewer.ProjectDoc
	for _, d := range res.Docs {
		if d.Status == domain.ContextIncluded && d.Text != nil {
			specs = append(specs, reviewer.ProjectDoc{Path: d.Path, Text: *d.Text})
		}
	}
	return specs
}

// repoContext is the repo-intel part of a prompt.
type repoContext struct {
	callers, repoMap, rankNote string
}

// review resolves the provider, gathers the repo-intel context and runs the engine.
func (e *Executor) review(st *runState, specs []reviewer.ProjectDoc) (reviewer.Outcome, error) {
	agent := st.job.Agent
	binding := app.LLMBinding{
		Provider:    agent.Provider,
		Model:       agent.Model,
		WorkspaceID: st.wsID,
		RunID:       st.job.RunID,
		SessionID:   fmt.Sprintf("%s/%s#%d:%s", st.repo.Owner, st.repo.Name, st.pull.Number, agent.Name),
		OnUsage: func(u reviewer.Usage) {
			st.meter.Add(u.TokensIn, u.TokensOut, u.CostUSD)
		},
	}
	llm, err := step(e, st.log, reviewer.EventTool, domain.ResolveProviderLabel(agent.Provider), func() (reviewerapp.LLM, error) {
		return e.providers.Resolve(st.runCtx, binding)
	})
	if err != nil {
		return reviewer.Outcome{}, err
	}

	var rc repoContext
	if agent.RepoIntel {
		rc = e.gatherRepoContext(st)
	} else {
		st.log.info(domain.RepoIntelDisabledMsg, nil)
	}

	in := reviewerapp.Input{
		SystemPrompt: agent.SystemPrompt,
		Diff:         st.diff,
		Strategy:     strategyOf(agent),
		Skills:       domain.SkillBlocks(st.skills),
		Specs:        specs,
		Callers:      rc.callers,
		RepoMap:      rc.repoMap,
		Task:         domain.TaskLine(st.pull.Number, st.pull.Title, st.pull.Author) + rc.rankNote,
		Intent:       st.intent,
	}
	if st.pull.Body != nil {
		in.PRDescription = *st.pull.Body
	}
	return e.engine(st.runCtx, in, llm, engineEvents{log: st.log}, e.engineOptions(st.isCancelled))
}

// gatherRepoContext reads the callers digest, the repo map and the file ranks. Each part degrades
// on its own: a failure is an info event (callers, repo map) or nothing (rank), never a failed run.
func (e *Executor) gatherRepoContext(st *runState) repoContext {
	var rc repoContext
	repoID := st.pull.RepoID
	changed := make([]string, len(st.diff.Files))
	for i, f := range st.diff.Files {
		changed[i] = f.Path
	}

	if len(changed) > 0 {
		rows, err := e.repoIntel.CallerSignatures(st.runCtx, repoID, changed, domain.CallersLimit)
		switch {
		case err != nil:
			e.repoIntelFailed(st, domain.CallersFailedMsg(err.Error()))
		default:
			if digest := domain.CallersDigest(rows); digest != "" {
				st.log.info(domain.CallersAttachedMsg(len(rows)), nil)
				rc.callers = digest
			}
		}
	}

	m, err := e.repoIntel.RepoMap(st.runCtx, repoID, domain.RepoMapBudget)
	switch {
	case err != nil:
		e.repoIntelFailed(st, domain.RepoMapFailedMsg(err.Error()))
	case !m.Degraded && reviewer.JSTrim(m.Text) != "":
		st.log.info(domain.RepoMapAttachedMsg(m.Tokens, m.Cached), nil)
		rc.repoMap = m.Text
	}

	if len(changed) > 0 {
		if ranks, err := e.repoIntel.FileRank(st.runCtx, repoID, changed); err == nil {
			hot := domain.HotFiles(ranks)
			if len(hot) > 0 {
				st.log.info(domain.FileRankMsg(len(hot), len(changed)), nil)
			}
			rc.rankNote = domain.RankNote(len(hot), len(changed))
		}
	}
	return rc
}

// repoIntelFailed publishes a degraded repo-intel part, unless the run was cancelled meanwhile
// (the lookup then failed only because its context ended).
func (e *Executor) repoIntelFailed(st *runState, msg string) {
	if !st.isCancelled() {
		st.log.info(msg, nil)
	}
}

// persist writes the review, its findings, the pull request's freshness and the run's done record
// in ONE transaction. The run becomes done only from running: a false answer means a cancel
// landed after the last checkpoint, so the transaction rolls back and the run stays cancelled.
func (e *Executor) persist(ctx context.Context, st *runState, outcome reviewer.Outcome) (reviewID string, durationMS int, err error) {
	agent := st.job.Agent
	kept := outcome.Review.Findings
	// Deterministic blocker count (severity at or above the agent's gate), not the model's verdict.
	blockers := reviewer.CountBlockers(kept, agent.CIFailOn)
	score := outcome.Review.Score
	err = e.tx.InTx(ctx, func(ctx context.Context) error {
		id, err := e.store.InsertReview(ctx, domain.NewReview{
			WorkspaceID: st.wsID,
			PRID:        st.pull.ID,
			AgentID:     agent.ID,
			RunID:       st.job.RunID,
			Verdict:     outcome.Review.Verdict,
			Summary:     outcome.Review.Summary,
			Score:       score,
			Model:       agent.Model,
		})
		if err != nil {
			return fmt.Errorf("insert review: %w", err)
		}
		if err := e.store.InsertFindings(ctx, id, kept, domain.SkillIDsByName(st.skills)); err != nil {
			return fmt.Errorf("insert findings: %w", err)
		}
		if err := e.store.MarkReviewed(ctx, st.pull.ID, st.pull.HeadSHA); err != nil {
			return fmt.Errorf("mark reviewed: %w", err)
		}
		ms := e.sinceMS(st.start)
		done, err := e.store.CompleteRunIfRunning(ctx, st.job.RunID, domain.RunCompletion{
			DurationMS:    ms,
			TokensIn:      outcome.Usage.TokensIn,
			TokensOut:     outcome.Usage.TokensOut,
			CostUSD:       outcome.Usage.CostUSD,
			FindingsCount: len(kept),
			Grounding:     outcome.Grounding,
			Score:         &score,
			Blockers:      &blockers,
		})
		if err != nil {
			return fmt.Errorf("complete run: %w", err)
		}
		if !done {
			return domain.ErrRunCancelled
		}
		reviewID, durationMS = id, ms
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	return reviewID, durationMS, nil
}

func ptr[T any](v T) *T { return &v }
