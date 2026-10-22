package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

func strp(s string) *string { return &s }

func TestExecutor_SuccessfulRun(t *testing.T) {
	h := newHarness(t, Options{MapConcurrency: 2}, fixedClock)
	b := h.batch(1)
	b.Pull.Body = strp("fixes the limiter")
	h.store.skills["agent-1"] = []domain.ReviewSkill{{ID: "sk-1", Name: "pr-quality", Body: "Be nice", Version: 3}}
	doc := "spec text"
	h.projc.result = app.ProjectContextResult{
		Docs: []domain.ContextDoc{
			{Path: "docs/a.md", Status: domain.ContextIncluded, Text: &doc, Tokens: 3, Source: "repo"},
			{Path: "docs/b.md", Status: domain.ContextMissing},
		},
		TokensTotal: 3, BudgetTokens: 16000,
		LogLine: "project context: 1 included, 1 skipped · ~3 tokens",
	}
	h.intel.callers = []domain.CallerSignature{{File: "b.go", Symbol: "Use", Signature: "func Use()"}}
	h.intel.repoMap = domain.RepoMap{Text: "map text", Tokens: 20, Cached: true}
	h.intel.ranks = []domain.PathRank{{Path: "a.go", Percentile: 97}}

	h.exec.Start(t.Context(), b)
	h.wait()

	wantEvents := []ev{
		{reviewer.EventTool, "Loading PR diff…"},
		{reviewer.EventTool, "Loading PR diff done (0ms)"},
		{reviewer.EventInfo, "Diff ready — 1 changed file(s); starting 1 agent run(s)"},
		{reviewer.EventInfo, `Starting review with agent "Agent1" (openai/gpt-x)`},
		{reviewer.EventInfo, "skills: 1 attached (pr-quality v3) · ~6 tokens (repeated in every map-reduce chunk)"},
		{reviewer.EventInfo, "project context: 1 included, 1 skipped · ~3 tokens"},
		{reviewer.EventTool, "Resolving openai provider…"},
		{reviewer.EventTool, "Resolving openai provider done (0ms)"},
		{reviewer.EventInfo, "callers digest: 1 caller signature(s) attached"},
		{reviewer.EventInfo, "repo map: 20 token(s) attached (cached=true)"},
		{reviewer.EventInfo, "file rank: 1/1 changed file(s) in top 5%"},
		{reviewer.EventInfo, "Reviewing 1 changed file(s) in one pass"},
		{reviewer.EventResult, "Persisted review rev-1 with 2 finding(s)"},
		{reviewer.EventInfo, "Run complete; trace persisted"},
	}
	if diff := cmp.Diff(wantEvents, h.events("run-1")); diff != "" {
		t.Errorf("run events mismatch (-want +got):\n%s", diff)
	}

	// Persisted rows.
	if diff := cmp.Diff([]domain.NewReview{{
		WorkspaceID: "ws-1", PRID: "pr-1", AgentID: "agent-1", RunID: "run-1",
		Verdict: reviewer.VerdictComment, Summary: "ok", Score: 85, Model: "gpt-x",
	}}, h.store.reviews); diff != "" {
		t.Errorf("reviews mismatch (-want +got):\n%s", diff)
	}
	if len(h.store.findings) != 1 || len(h.store.findings[0].findings) != 2 || h.store.findings[0].review != "rev-1" {
		t.Fatalf("findings = %+v, want 2 findings of rev-1", h.store.findings)
	}
	if diff := cmp.Diff(map[string]string{"pr-quality": "sk-1"}, h.store.findings[0].skillIDs); diff != "" {
		t.Errorf("skill ids mismatch (-want +got):\n%s", diff)
	}
	if got := h.store.reviewed["pr-1"]; got != "a1b2c3d4e5f6" {
		t.Errorf("last reviewed sha = %q, want the head sha", got)
	}
	rec := h.store.run("run-1")
	if rec.status != domain.StatusDone {
		t.Fatalf("run status = %q, want done", rec.status)
	}
	cost := 0.00042
	zeroOne, zeroScore := 1, 85
	wantCompletion := domain.RunCompletion{
		TokensIn: 1200, TokensOut: 180, CostUSD: &cost, FindingsCount: 2, Grounding: "2/2 passed",
		Score: &zeroScore, Blockers: &zeroOne,
	}
	if diff := cmp.Diff(wantCompletion, *rec.completion); diff != "" {
		t.Errorf("run completion mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]domain.ReviewSkill{{ID: "sk-1", Name: "pr-quality", Body: "Be nice", Version: 3}}, rec.skills); diff != "" {
		t.Errorf("recorded run skills mismatch (-want +got):\n%s", diff)
	}

	// Completed trace.
	tr := rec.trace
	if tr == nil || tr.Ended {
		t.Fatalf("trace = %+v, want a completed trace", tr)
	}
	if tr.Config.Version != "7" || tr.Config.PR != 482 || tr.Config.Source != domain.SourceLocal {
		t.Errorf("trace config = %+v", tr.Config)
	}
	if diff := cmp.Diff([]string{"docs/a.md"}, tr.SpecsRead); diff != "" {
		t.Errorf("trace specs_read mismatch (-want +got):\n%s", diff)
	}
	if tr.ProjectContext == nil || tr.ProjectContext.BudgetTokens != 16000 || len(tr.ProjectContext.Docs) != 2 {
		t.Errorf("trace project_context = %+v", tr.ProjectContext)
	}
	if diff := cmp.Diff([]domain.SkillUsed{{ID: "sk-1", Name: "pr-quality", Version: 3}}, tr.SkillsUsed); diff != "" {
		t.Errorf("trace skills_used mismatch (-want +got):\n%s", diff)
	}
	if n := len(tr.ToolCalls); n != 1 || tr.ToolCalls[0].Args != "all files" || tr.ToolCalls[0].Meta != "single-pass" {
		t.Errorf("trace tool_calls = %+v", tr.ToolCalls)
	}
	if last := tr.Log[len(tr.Log)-1]; !strings.HasPrefix(last.Msg, "Persisted review") {
		t.Errorf("trace log ends with %q, want the Persisted line (taken before Run complete)", last.Msg)
	}
	if !h.bus.isCompleted("run-1") {
		t.Error("bus not completed after the run")
	}

	// What the engine and the LLM binding received.
	in := h.engine.inputs[0]
	if in.Callers != "### b.go\n- `Use` — func Use()" || in.RepoMap != "map text" {
		t.Errorf("engine input callers/repoMap = %q / %q", in.Callers, in.RepoMap)
	}
	if !strings.HasPrefix(in.Task, "Review pull request #482 \"Add limiter\" by ann. ") ||
		!strings.HasSuffix(in.Task, "1 of 1 changed file(s) are in the top 5% most-depended-on (high blast risk) — prioritise their correctness.") {
		t.Errorf("engine task = %q", in.Task)
	}
	if diff := cmp.Diff([]string{"### pr-quality\n\nBe nice"}, in.Skills); diff != "" {
		t.Errorf("engine skills mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]reviewer.ProjectDoc{{Path: "docs/a.md", Text: "spec text"}}, in.Specs); diff != "" {
		t.Errorf("engine specs mismatch (-want +got):\n%s", diff)
	}
	if in.PRDescription != "fixes the limiter" || in.Strategy != reviewer.StrategyAuto || in.SystemPrompt != "be careful" {
		t.Errorf("engine input = %+v", in)
	}
	if got := h.engine.opts[0].MapConcurrency; got != 2 {
		t.Errorf("engine MapConcurrency = %d, want 2", got)
	}
	bind := h.prov.binding("run-1")
	if bind.SessionID != "acme/api#482:Agent1" || bind.Provider != "openai" || bind.Model != "gpt-x" || bind.RunID != "run-1" {
		t.Errorf("LLM binding = %+v", bind)
	}
	pcIn := h.projc.inputs[0]
	if pcIn.AgentID != "agent-1" || pcIn.Base != "main" || pcIn.HeadSHA != "a1b2c3d4e5f6" || len(pcIn.Skills) != 1 {
		t.Errorf("project context input = %+v", pcIn)
	}
}

func TestExecutor_DiffLoadedOnceAndFannedOut(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.exec.Start(t.Context(), h.batch(3))
	h.wait()

	if h.store.filesCalls != 1 {
		t.Errorf("PullFiles called %d times, want 1", h.store.filesCalls)
	}
	for _, id := range []string{"run-1", "run-2", "run-3"} {
		want := []ev{
			{reviewer.EventTool, "Loading PR diff…"},
			{reviewer.EventTool, "Loading PR diff done (0ms)"},
			{reviewer.EventInfo, "Diff ready — 1 changed file(s); starting 3 agent run(s)"},
		}
		if diff := cmp.Diff(want, h.events(id)[:3]); diff != "" {
			t.Errorf("%s shared events mismatch (-want +got):\n%s", id, diff)
		}
		if got := h.store.run(id).status; got != domain.StatusDone {
			t.Errorf("%s status = %q, want done", id, got)
		}
	}
}

func TestExecutor_StepDurationsUseTheClock(t *testing.T) {
	var ticks atomic.Int64
	clock := func() time.Time { return fixedClock().Add(time.Duration(ticks.Add(5)) * time.Millisecond) }
	h := newHarness(t, Options{}, clock)
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()
	if !h.hasEvent("run-1", reviewer.EventTool, "Loading PR diff done (5ms)") {
		t.Errorf("events = %v, want the diff step to report 5ms", h.events("run-1"))
	}
}

func TestExecutor_DiffFailureFailsEveryRun(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.store.filesErr = errors.New("db down")
	h.exec.Start(t.Context(), h.batch(2))
	h.wait()

	for _, id := range []string{"run-1", "run-2"} {
		want := []ev{
			{reviewer.EventTool, "Loading PR diff…"},
			{reviewer.EventError, "Loading PR diff failed (0ms): db down"},
			{reviewer.EventError, "Failed to load PR diff: db down"},
		}
		if diff := cmp.Diff(want, h.events(id)); diff != "" {
			t.Errorf("%s events mismatch (-want +got):\n%s", id, diff)
		}
		rec := h.store.run(id)
		zero := 0.0
		wantC := domain.RunCompletion{CostUSD: &zero, Grounding: domain.NoGrounding, Error: strp("Failed to load PR diff: db down")}
		if rec.status != domain.StatusFailed {
			t.Errorf("%s status = %q, want failed", id, rec.status)
		}
		if diff := cmp.Diff(wantC, *rec.completion); diff != "" {
			t.Errorf("%s completion mismatch (-want +got):\n%s", id, diff)
		}
		if rec.trace == nil || !rec.trace.Ended || len(rec.trace.Log) != 3 {
			t.Errorf("%s trace = %+v, want an ended trace with the 3 log lines", id, rec.trace)
		}
		if !h.bus.isCompleted(id) {
			t.Errorf("%s bus not completed", id)
		}
	}
	if got := h.engine.calls(); got != 0 {
		t.Errorf("engine made %d calls after a diff failure, want 0", got)
	}
}

func TestExecutor_NoPatchesRunsAnEmptyDiff(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.store.files = nil
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()
	if !h.hasEvent("run-1", reviewer.EventInfo, "Diff ready — 0 changed file(s); starting 1 agent run(s)") {
		t.Errorf("events = %v", h.events("run-1"))
	}
	if got := h.store.run("run-1").status; got != domain.StatusDone {
		t.Errorf("status = %q, want done", got)
	}
	// With no changed file neither the callers digest nor the rank is asked for.
	if h.intel.calls != 1 {
		t.Errorf("repo intel calls = %d, want 1 (the repo map only)", h.intel.calls)
	}
}

func TestExecutor_AgentConcurrencyIsBoundedAndFailuresAreIsolated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{AgentConcurrency: 2}, time.Now)
		var inFlight, peak atomic.Int32
		h.engine.script = func(ctx context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(2 * time.Second)
			return defaultOutcome(), nil
		}
		b := h.batch(6)
		b.Jobs[2].Agent.Provider = "openrouter"
		h.prov.missing["openrouter"] = "OPENROUTER_API_KEY is not configured"

		h.exec.Start(t.Context(), b)
		h.wait()

		if got := peak.Load(); got != 2 {
			t.Errorf("peak runs inside the engine = %d, want 2", got)
		}
		for i := 1; i <= 6; i++ {
			id := fmt.Sprintf("run-%d", i)
			want := domain.StatusDone
			if i == 3 {
				want = domain.StatusFailed
			}
			if got := h.store.run(id).status; got != want {
				t.Errorf("%s status = %q, want %q", id, got, want)
			}
		}
		evs := h.events("run-3")
		if !h.hasEvent("run-3", reviewer.EventError, "Resolving openrouter provider failed (0ms): OPENROUTER_API_KEY is not configured") ||
			!h.hasEvent("run-3", reviewer.EventError, "Run failed: OPENROUTER_API_KEY is not configured") {
			t.Errorf("run-3 events = %v", evs)
		}
		rec := h.store.run("run-3")
		if rec.completion.CostUSD == nil || *rec.completion.CostUSD != 0 || rec.completion.TokensIn != 0 {
			t.Errorf("failed run usage = %+v, want zero tokens and cost 0", rec.completion)
		}
		if rec.trace == nil || !rec.trace.Ended {
			t.Errorf("failed run trace = %+v, want an ended trace", rec.trace)
		}
	})
}

func TestExecutor_FailedRunRecordsUsageAndRedactsTheNote(t *testing.T) {
	cheap := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		costs    []*float64
		wantCost *float64
	}{
		{"priced", []*float64{cheap(0.001), cheap(0.001), cheap(0.001)}, cheap(0.003)},
		{"one unpriced", []*float64{cheap(0.001), nil, cheap(0.001)}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Options{}, fixedClock)
			note := "OpenAI structured output failed schema validation for Review after 3 attempt(s). Last issues:\nbad sk-secret RAWMODELOUTPUT"
			h.engine.script = func(_ context.Context, _ reviewerapp.Input, llm reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
				for _, c := range tc.costs {
					llm.(fakeLLM).binding.OnUsage(reviewer.Usage{TokensIn: 100, TokensOut: 10, CostUSD: c})
				}
				return reviewer.Outcome{}, errors.New(note)
			}
			h.exec.Start(t.Context(), h.batch(1))
			h.wait()

			rec := h.store.run("run-1")
			if rec.status != domain.StatusFailed {
				t.Fatalf("status = %q, want failed", rec.status)
			}
			c := rec.completion
			if c.TokensIn != 300 || c.TokensOut != 30 {
				t.Errorf("tokens = %d/%d, want 300/30", c.TokensIn, c.TokensOut)
			}
			if diff := cmp.Diff(tc.wantCost, c.CostUSD, cmpopts.EquateApprox(0, 1e-12)); diff != "" {
				t.Errorf("cost mismatch (-want +got):\n%s", diff)
			}
			if c.FindingsCount != 0 || c.Grounding != "0/0 passed" || c.Score != nil || c.Blockers != nil {
				t.Errorf("completion = %+v, want 0 findings, 0/0 passed, no score", c)
			}
			if c.Error == nil || !strings.Contains(*c.Error, "[redacted]") || strings.Contains(*c.Error, "sk-secret") ||
				!strings.HasPrefix(*c.Error, "OpenAI structured output failed schema validation for Review after 3 attempt(s).") {
				t.Errorf("stored note = %v, want the redacted note", c.Error)
			}
			if len(h.store.reviews) != 0 {
				t.Errorf("reviews = %v, want none", h.store.reviews)
			}
			// AC4073: the mirrored error line has only the note's first line; nothing else leaks.
			logs := h.logs.text()
			if strings.Contains(logs, "RAWMODELOUTPUT") || strings.Contains(logs, "sk-secret") {
				t.Errorf("log lines leak the raw output or the key:\n%s", logs)
			}
			if !strings.Contains(logs, "Run failed: OpenAI structured output failed schema validation for Review after 3 attempt(s). Last issues:") {
				t.Errorf("no mirrored error line with the note's first line:\n%s", logs)
			}
			// The bus event keeps the whole (redacted) note: the UI shows it.
			found := false
			for _, e := range h.events("run-1") {
				if e.Kind == reviewer.EventError && strings.HasPrefix(e.Msg, "Run failed: ") && strings.Contains(e.Msg, "RAWMODELOUTPUT") {
					found = true
				}
			}
			if !found {
				t.Errorf("events = %v, want the full Run failed event", h.events("run-1"))
			}
			if rec.trace == nil || !rec.trace.Ended || rec.trace.Stats.TokensIn != 300 {
				t.Errorf("trace = %+v, want an ended trace with the usage so far", rec.trace)
			}
			if !h.bus.isCompleted("run-1") {
				t.Error("bus not completed")
			}
		})
	}
}

func TestExecutor_CancelDuringTheEngineCall(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	started := make(chan struct{})
	var engineCtxErr atomic.Value
	h.engine.script = func(ctx context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
		close(started)
		<-ctx.Done()
		engineCtxErr.Store(ctx.Err())
		return reviewer.Outcome{}, fmt.Errorf("call aborted: %w", ctx.Err())
	}
	h.exec.Start(t.Context(), h.batch(1))
	<-started
	h.bus.Publish("run-1", reviewer.EventInfo, domain.CancellationRequestedMsg, nil)
	h.bus.Cancel("run-1")
	h.wait()

	if err, _ := engineCtxErr.Load().(error); !errors.Is(err, context.Canceled) {
		t.Errorf("engine ctx error = %v, want context canceled", err)
	}
	rec := h.store.run("run-1")
	if rec.status != domain.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", rec.status)
	}
	if rec.completion.Error == nil || *rec.completion.Error != "Cancelled by user" {
		t.Errorf("note = %v, want Cancelled by user", rec.completion.Error)
	}
	if len(h.store.reviews) != 0 {
		t.Errorf("reviews = %v, want none", h.store.reviews)
	}
	if rec.trace == nil || !rec.trace.Ended {
		t.Fatalf("trace = %+v, want an ended trace", rec.trace)
	}
	var msgs []string
	for _, l := range rec.trace.Log {
		msgs = append(msgs, l.Msg)
	}
	for _, want := range []string{domain.CancellationRequestedMsg, "Run cancelled by user"} {
		if !contains(msgs, want) {
			t.Errorf("trace log %v lacks %q", msgs, want)
		}
	}
	if h.hasEvent("run-1", reviewer.EventError, "Run failed: call aborted: context canceled") {
		t.Error("an aborted call was recorded as a failure, want a cancel")
	}
	if !h.bus.isCompleted("run-1") {
		t.Error("bus not completed")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestExecutor_CancelCheckpointsKeepLaterChunksFromStarting(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	// A two-chunk map-reduce: the cancel lands after chunk 1, the engine polls Cancelled before chunk 2.
	h.engine.script = func(_ context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error) {
		for i := range 2 {
			if opts.Cancelled() {
				return reviewer.Outcome{}, reviewerapp.ErrCancelled
			}
			h.engine.mu.Lock()
			h.engine.chunkCalls++
			h.engine.mu.Unlock()
			if i == 0 {
				h.bus.Cancel("run-1")
			}
		}
		return defaultOutcome(), nil
	}
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	if got := h.engine.calls(); got != 1 {
		t.Errorf("chunk calls = %d, want 1", got)
	}
	if got := h.store.run("run-1").status; got != domain.StatusCancelled {
		t.Errorf("status = %q, want cancelled", got)
	}
}

func TestExecutor_CancelDuringTheLastCallStillWins(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.engine.script = func(_ context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
		h.bus.Cancel("run-1") // the provider ignores the cancel and answers successfully
		return defaultOutcome(), nil
	}
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	if got := h.store.run("run-1").status; got != domain.StatusCancelled {
		t.Errorf("status = %q, want cancelled", got)
	}
	if len(h.store.reviews) != 0 || h.tx.commits != 0 {
		t.Errorf("reviews = %v, commits = %d, want no persist", h.store.reviews, h.tx.commits)
	}
}

func TestExecutor_CancelDuringPersistRollsTheReviewBack(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.store.beforeComplete = func(runID string) {
		// The cancel route: the row becomes cancelled and the flag is raised.
		h.store.mu.Lock()
		h.store.runs[runID].status = domain.StatusCancelled
		h.store.mu.Unlock()
		h.bus.Cancel(runID)
	}
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	if len(h.store.reviews) != 0 || len(h.store.findings) != 0 || len(h.store.reviewed) != 0 {
		t.Errorf("reviews %v findings %v reviewed %v, want everything rolled back", h.store.reviews, h.store.findings, h.store.reviewed)
	}
	if h.tx.aborts != 1 || h.tx.commits != 0 {
		t.Errorf("tx aborts/commits = %d/%d, want 1/0", h.tx.aborts, h.tx.commits)
	}
	if got := h.store.run("run-1").status; got != domain.StatusCancelled {
		t.Errorf("status = %q, want cancelled", got)
	}
	if !h.hasEvent("run-1", reviewer.EventError, "Run cancelled by user") {
		t.Errorf("events = %v, want the cancel event", h.events("run-1"))
	}
}

func TestExecutor_CancelWhileTheDiffLoadsStillRecordsSkillsAndMakesNoCall(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.store.filesGate = make(chan struct{})
	h.store.skills["agent-1"] = []domain.ReviewSkill{{ID: "sk-1", Name: "s", Body: "b", Version: 1}}
	h.exec.Start(t.Context(), h.batch(1))
	h.bus.Cancel("run-1")
	close(h.store.filesGate)
	h.wait()

	if got := h.engine.calls(); got != 0 {
		t.Errorf("engine chunk calls = %d, want 0", got)
	}
	rec := h.store.run("run-1")
	if rec.status != domain.StatusCancelled || len(rec.skills) != 1 {
		t.Errorf("status = %q skills = %v, want cancelled with the skill recorded", rec.status, rec.skills)
	}
}

// queuedHarness starts a 2-run batch with one agent slot; run-1 sits in the engine until release
// is closed, so run-2 waits for its slot. It returns once run-1 is inside the engine.
func queuedHarness(t *testing.T) (h *harness, release chan struct{}) {
	t.Helper()
	h = newHarness(t, Options{AgentConcurrency: 1}, fixedClock)
	release = make(chan struct{})
	entered := make(chan struct{})
	h.engine.script = func(ctx context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return defaultOutcome(), nil
	}
	h.exec.Start(t.Context(), h.batch(2))
	<-entered
	return h, release
}

// A run cancelled while it waited for its slot must stay cancelled when the bus has meanwhile
// forgotten the cancel (TTL eviction): no provider, repo-intel, project-context or LLM work.
func TestExecutor_QueuedRunCancelledAndEvictedDoesNoWork(t *testing.T) {
	h, release := queuedHarness(t)

	// What Service.CancelRun does, then the bus TTL passes.
	h.bus.Publish("run-2", reviewer.EventInfo, domain.CancellationRequestedMsg, nil)
	h.bus.Cancel("run-2")
	h.store.mu.Lock()
	h.store.runs["run-2"].status = domain.StatusCancelled
	h.store.mu.Unlock()
	h.bus.Complete("run-2")
	h.bus.evict("run-2")

	close(release)
	h.wait()

	if got := len(h.engine.inputs); got != 1 {
		t.Errorf("engine runs = %d, want 1 (the cancelled queued run must not review)", got)
	}
	if got := h.prov.binding("run-2"); got.RunID != "" {
		t.Errorf("provider resolved for the cancelled queued run: %+v", got)
	}
	if len(h.projc.inputs) != 1 || h.projc.inputs[0].AgentID != "agent-1" {
		t.Errorf("project-context inputs = %+v, want run-1's only", h.projc.inputs)
	}
	rec := h.store.run("run-2")
	if rec.status != domain.StatusCancelled || rec.ended != 1 || rec.trace == nil || !rec.trace.Ended {
		t.Errorf("run-2 row = %+v, want cancelled (kept) with one ending and an ended trace", rec)
	}
	if rec.completion != nil && (rec.completion.TokensIn != 0 || rec.completion.FindingsCount != 0) {
		t.Errorf("run-2 completion = %+v, want zero usage and no findings", rec.completion)
	}
	if !h.hasEvent("run-2", reviewer.EventError, "Run cancelled by user") || !h.bus.isCompleted("run-2") {
		t.Errorf("run-2 events = %v completed = %v, want the cancelled ending and a completed bus", h.events("run-2"), h.bus.isCompleted("run-2"))
	}
	if got := h.store.run("run-1").status; got != domain.StatusDone {
		t.Errorf("run-1 status = %q, want done", got)
	}
}

func TestExecutor_QueuedRunThatEndedOrVanishedDoesNoWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(h *harness)
		want   domain.RunStatus // "" = the row is gone
	}{
		{"failed meanwhile", func(h *harness) {
			h.store.mu.Lock()
			h.store.runs["run-2"].status = domain.StatusFailed
			h.store.mu.Unlock()
		}, domain.StatusFailed},
		{"done meanwhile", func(h *harness) {
			h.store.mu.Lock()
			h.store.runs["run-2"].status = domain.StatusDone
			h.store.mu.Unlock()
		}, domain.StatusDone},
		{"deleted meanwhile", func(h *harness) { h.store.deleteRun("run-2") }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, release := queuedHarness(t)
			tc.change(h)
			close(release)
			h.wait()

			if got := len(h.engine.inputs); got != 1 {
				t.Errorf("engine runs = %d, want 1", got)
			}
			rec := h.store.run("run-2")
			if rec.status != tc.want || rec.completion != nil || rec.trace != nil || rec.ended != 0 {
				t.Errorf("run-2 row = %+v, want status %q and untouched", rec, tc.want)
			}
		})
	}
}

// A failed status read must not strand the run: it starts as before.
func TestExecutor_FindRunFailureStillRunsTheJob(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.store.findRunEr = errors.New("connection reset")
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	if got := h.store.run("run-1").status; got != domain.StatusDone {
		t.Errorf("status = %q, want done", got)
	}
}

func TestExecutor_PersistFailureFailsTheRunAndRollsBack(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.store.insertReviewEr = errors.New("disk full")
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	rec := h.store.run("run-1")
	if rec.status != domain.StatusFailed || rec.completion.Error == nil || *rec.completion.Error != "insert review: disk full" {
		t.Errorf("status = %q note = %v, want failed with the persist error", rec.status, rec.completion.Error)
	}
}

func TestExecutor_WritesToADeletedRunAreSwallowed(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	b := h.batch(1)
	h.engine.script = func(_ context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
		h.store.deleteRun("run-1") // DELETE /runs/:id while the run is in its LLM call
		return defaultOutcome(), nil
	}
	h.exec.Start(t.Context(), b)
	h.wait()

	if !h.bus.isCompleted("run-1") {
		t.Error("bus not completed after the run's row vanished")
	}
	for _, l := range h.logs.atLevel(t, "ERROR") {
		if msg := fmt.Sprint(l["msg"]); msg != "Run failed: complete run: not found" && msg != `review: agent "Agent1" failed` {
			t.Errorf("unexpected error log line %v", l)
		}
	}
}

func TestExecutor_RepoIntelFailuresDegradeToInfoEvents(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.intel.callersEr = errors.New("scan broke")
	h.intel.repoMap = domain.RepoMap{Text: "ignored", Degraded: true}
	h.intel.ranksEr = errors.New("rank broke")
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	evs := h.events("run-1")
	if !h.hasEvent("run-1", reviewer.EventInfo, "callers digest: repoIntel failed — scan broke") {
		t.Errorf("events = %v, want the callers failure", evs)
	}
	for _, e := range evs {
		if strings.HasPrefix(e.Msg, "repo map:") || strings.HasPrefix(e.Msg, "file rank:") {
			t.Errorf("unexpected event %v", e)
		}
	}
	in := h.engine.inputs[0]
	if in.Callers != "" || in.RepoMap != "" || strings.Contains(in.Task, "top 5%") {
		t.Errorf("engine input = %+v, want no callers, skeleton or hot note", in)
	}
	if got := h.store.run("run-1").status; got != domain.StatusDone {
		t.Errorf("status = %q, want done: repo intel never fails a run", got)
	}
}

func TestExecutor_RepoMapFailureIsAnInfoEvent(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.intel.repoMapEr = errors.New("no index")
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()
	if !h.hasEvent("run-1", reviewer.EventInfo, "repo map: repoIntel failed — no index") {
		t.Errorf("events = %v", h.events("run-1"))
	}
}

func TestExecutor_RepoIntelOffForTheAgentCallsNothing(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	b := h.batch(1)
	b.Jobs[0].Agent.RepoIntel = false
	h.exec.Start(t.Context(), b)
	h.wait()

	if !h.hasEvent("run-1", reviewer.EventInfo, domain.RepoIntelDisabledMsg) {
		t.Errorf("events = %v, want the disabled event", h.events("run-1"))
	}
	if h.intel.calls != 0 {
		t.Errorf("repo intel calls = %d, want 0", h.intel.calls)
	}
}

func TestExecutor_ProjectContextFailureIsAWarning(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.projc.err = errors.New("store unreachable")
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	var got *domain.RunEvent
	for _, e := range h.bus.Buffer("run-1") {
		if strings.HasPrefix(e.Msg, "warning: project context unavailable") {
			got = &e
		}
	}
	if got == nil {
		t.Fatalf("events = %v, want the warning", h.events("run-1"))
	}
	if got.Msg != "warning: project context unavailable — store unreachable" || string(got.Data) != `{"warning":"project_context_unavailable"}` || got.Kind != reviewer.EventInfo {
		t.Errorf("warning event = %+v", got)
	}
	if len(h.engine.inputs[0].Specs) != 0 {
		t.Errorf("engine specs = %v, want none", h.engine.inputs[0].Specs)
	}
	if tr := h.store.run("run-1").trace; tr.ProjectContext != nil {
		t.Errorf("trace project_context = %+v, want none", tr.ProjectContext)
	}
}

func TestExecutor_NoProjectContextAddsNoEventAndNoTraceKey(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()
	for _, e := range h.events("run-1") {
		if strings.Contains(e.Msg, "project context") {
			t.Errorf("unexpected event %v", e)
		}
	}
	if tr := h.store.run("run-1").trace; tr.ProjectContext != nil || len(tr.SpecsRead) != 0 {
		t.Errorf("trace = %+v", tr)
	}
}

func TestExecutor_EngineEventDataIsMarshalledWithoutHTMLEscaping(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.engine.script = func(_ context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, ev reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
		ev.Emit(reviewer.EventTool, "map: reviewing a<b>.go", reviewerapp.FileData{File: "a<b>.go"})
		return defaultOutcome(), nil
	}
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()
	for _, e := range h.bus.Buffer("run-1") {
		if e.Msg == "map: reviewing a<b>.go" {
			if string(e.Data) != `{"file":"a<b>.go"}` {
				t.Errorf("data = %s", e.Data)
			}
			return
		}
	}
	t.Errorf("events = %v, want the engine event", h.events("run-1"))
}

func TestExecutor_PanicOutsideARunIsLoggedOnceAndTheExecutorKeepsWorking(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.store.filesPanic = true
	h.exec.Start(t.Context(), h.batch(2))
	h.wait()

	errs := h.logs.atLevel(t, "ERROR")
	if len(errs) != 1 || errs[0]["msg"] != "review: batch crashed" {
		t.Fatalf("error log lines = %v, want exactly one batch crash", errs)
	}
	for _, id := range []string{"run-1", "run-2"} {
		if got := h.store.run(id).status; got != domain.StatusRunning {
			t.Errorf("%s status = %q, want running (reaped at the next start)", id, got)
		}
	}

	h.store.filesPanic = false
	b := h.batch(1)
	b.Jobs[0].RunID = "run-9"
	h.store.addRun("run-9")
	h.exec.Start(t.Context(), b)
	h.wait()
	if got := h.store.run("run-9").status; got != domain.StatusDone {
		t.Errorf("run after the crash: status = %q, want done", got)
	}
}

func TestExecutor_PanicInARunFailsOnlyThatRun(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	var first atomic.Bool
	h.engine.script = func(_ context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error) {
		if first.CompareAndSwap(false, true) {
			panic("secret internals sk-secret")
		}
		return h.engine.chunk(ev, opts, defaultOutcome())
	}
	h.exec.Start(t.Context(), h.batch(2))
	h.wait()

	var failed, done int
	for _, id := range []string{"run-1", "run-2"} {
		rec := h.store.run(id)
		switch rec.status {
		case domain.StatusFailed:
			failed++
			if *rec.completion.Error != "internal error" {
				t.Errorf("%s note = %q, want the generic internal error", id, *rec.completion.Error)
			}
		case domain.StatusDone:
			done++
		}
	}
	if failed != 1 || done != 1 {
		t.Errorf("failed/done = %d/%d, want 1/1", failed, done)
	}
	if strings.Contains(h.logs.text(), "sk-secret") {
		t.Error("the panic value leaked a key into the log")
	}
}

func TestExecutor_StartWithoutJobsIsANoop(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.exec.Start(t.Context(), domain.Batch{})
	h.wait()
	if h.store.filesCalls != 0 {
		t.Errorf("PullFiles called %d times for an empty batch", h.store.filesCalls)
	}
}

func TestNew_PanicsOnAMissingDependency(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New() with no dependencies did not panic")
		}
	}()
	New(Deps{}, Options{})
}

// Guards against a lost update between concurrent runs of one request.
func TestExecutor_ConcurrentRunsKeepTheirOwnState(t *testing.T) {
	h := newHarness(t, Options{AgentConcurrency: 8}, fixedClock)
	var mu sync.Mutex
	seen := map[string]string{}
	h.engine.script = func(_ context.Context, in reviewerapp.Input, llm reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error) {
		id := llm.(fakeLLM).binding.RunID
		mu.Lock()
		seen[id] = in.SystemPrompt
		mu.Unlock()
		llm.(fakeLLM).binding.OnUsage(reviewer.Usage{TokensIn: 7})
		return h.engine.chunk(ev, opts, defaultOutcome())
	}
	b := h.batch(8)
	for i := range b.Jobs {
		b.Jobs[i].Agent.SystemPrompt = "prompt-" + b.Jobs[i].RunID
	}
	h.exec.Start(t.Context(), b)
	h.wait()
	for _, j := range b.Jobs {
		if seen[j.RunID] != "prompt-"+j.RunID {
			t.Errorf("%s saw prompt %q", j.RunID, seen[j.RunID])
		}
		if got := h.store.run(j.RunID).status; got != domain.StatusDone {
			t.Errorf("%s status = %q", j.RunID, got)
		}
	}
}
