package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// fakeIntent scripts app.IntentResolver and records the requests.
type fakeIntent struct {
	mu       sync.Mutex
	script   func(ctx context.Context, req app.IntentRequest, emit app.IntentEmit) app.IntentResult
	requests []app.IntentRequest
	// ctxs holds the context of each call, to look at after the review.
	ctxs []context.Context
}

var _ app.IntentResolver = (*fakeIntent)(nil)

func (f *fakeIntent) Resolve(ctx context.Context, req app.IntentRequest, emit app.IntentEmit) app.IntentResult {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.ctxs = append(f.ctxs, ctx)
	script := f.script
	f.mu.Unlock()
	return script(ctx, req, emit)
}

func (f *fakeIntent) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func testIntent() *reviewer.Intent {
	medium, feature := "medium", "feature"
	return &reviewer.Intent{
		Text: "Add rate limiting.", InScope: []string{"limiter"}, OutOfScope: []string{"auth"},
		ChangeType: &feature, Confidence: &medium, DerivedFrom: "explicit",
	}
}

func usedTrace() *domain.IntentTrace {
	medium, explicit, feature := "medium", "explicit", "feature"
	cost := 0.00042
	return &domain.IntentTrace{
		Status: domain.IntentStatusUsed, Confidence: &medium, DerivedFrom: &explicit, ChangeType: &feature,
		Provider: "mock", Model: "mock-1", TokensIn: 1200, TokensOut: 180, CostUSD: &cost,
	}
}

// usedResult answers every request with the intent, after two events.
func usedResult(_ context.Context, _ app.IntentRequest, emit app.IntentEmit) app.IntentResult {
	emit("info", "PR intent sources: 1 used, 0 skipped, 0 failed", nil)
	emit("result", "PR intent derived (confidence=medium, 1200+180 tok, $0.0004)", nil)
	return app.IntentResult{Intent: testIntent(), Trace: usedTrace()}
}

func withIntent(h *harness, f *fakeIntent) *fakeIntent {
	h.exec.intent = f
	return f
}

func msgs(evs []ev) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = string(e.Kind) + ": " + e.Msg
	}
	return out
}

// AC5032: with no resolver nothing of the hook happens.
func TestIntent_SwitchOffMakesNoRequestAndLeavesTheTraceNull(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	if in := h.engine.inputs[0]; in.Intent != nil {
		t.Errorf("engine input intent = %+v, want nil", in.Intent)
	}
	if tr := h.store.run("run-1").trace; tr == nil || tr.Intent != nil {
		t.Errorf("trace = %+v, want a trace with a nil intent", tr)
	}
}

// FR5023, AC5030: one request per review, after "Diff ready", its events in every run's stream
// before any agent event; the intent feeds every run; the trace carries the resolver's trace; the
// intent call's cost stays out of the run's usage.
func TestIntent_ResolvedOnceAndFannedOutToEveryRun(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	h.store.files = []reviewer.PatchFile{
		{Path: "b.go", Patch: "@@ -1,1 +1,2 @@\n x\n+y"},
		{Path: "a.go", Patch: "@@ -1,1 +1,2 @@\n x\n+y"},
	}
	shared := usedTrace()
	f := withIntent(h, &fakeIntent{script: func(ctx context.Context, req app.IntentRequest, emit app.IntentEmit) app.IntentResult {
		res := usedResult(ctx, req, emit)
		res.Trace = shared
		return res
	}})
	h.exec.Start(t.Context(), h.batch(3))
	h.wait()

	if f.calls() != 1 {
		t.Fatalf("Resolve calls = %d, want 1 for a 3-run request", f.calls())
	}
	req := f.requests[0]
	if req.WorkspaceID != "ws-1" || req.Pull.ID != "pr-1" || req.Pull.HeadSHA != "a1b2c3d4e5f6" || req.Repo.Owner != "acme" || req.Repo.Name != "api" {
		t.Errorf("request = %+v, want the batch's workspace, pull and repo", req)
	}
	if diff := cmp.Diff([]string{"a.go", "b.go"}, req.ChangedFiles); diff != "" {
		t.Errorf("request ChangedFiles mismatch (-want +got):\n%s", diff)
	}
	if want := reviewer.RawDiff(h.store.files); req.DiffExcerpt != want {
		t.Errorf("request DiffExcerpt = %q, want the raw stored diff %q", req.DiffExcerpt, want)
	}

	wantHead := []string{
		"tool: Loading PR diff…",
		"tool: Loading PR diff done (0ms)",
		"info: Diff ready — 2 changed file(s); starting 3 agent run(s)",
		"info: PR intent sources: 1 used, 0 skipped, 0 failed",
		"result: PR intent derived (confidence=medium, 1200+180 tok, $0.0004)",
		`info: Starting review with agent "Agent`,
	}
	for _, id := range []string{"run-1", "run-2", "run-3"} {
		got := msgs(h.events(id))
		if len(got) < len(wantHead) {
			t.Fatalf("%s events = %v", id, got)
		}
		if diff := cmp.Diff(wantHead[:5], got[:5]); diff != "" {
			t.Errorf("%s events mismatch (-want +got):\n%s", id, diff)
		}
		rec := h.store.run(id)
		if rec.status != domain.StatusDone || rec.trace == nil || rec.trace.Ended {
			t.Fatalf("%s = %+v, want done with a completed trace", id, rec)
		}
		if rec.trace.Intent != shared {
			t.Errorf("%s trace intent is not the request's shared trace", id)
		}
		if diff := cmp.Diff(usedTrace(), rec.trace.Intent); diff != "" {
			t.Errorf("%s trace intent mismatch (-want +got):\n%s", id, diff)
		}
		// FR5026: the intent call (1200/180 tokens, $0.00042) is not in the run's usage.
		if c := rec.completion; c.TokensIn != 1200 || c.TokensOut != 180 || *c.CostUSD != 0.00042 {
			t.Errorf("%s completion usage = %+v, want the review call's only", id, c)
		}
	}
	for i, in := range h.engine.inputs {
		if diff := cmp.Diff(testIntent(), in.Intent); diff != "" {
			t.Errorf("engine input %d intent mismatch (-want +got):\n%s", i, diff)
		}
	}
	if h.engine.inputs[0].Intent != h.engine.inputs[1].Intent {
		t.Error("runs got different Intent values, want one shared read-only value")
	}
}

// The events keep the kind and data the resolver sent.
func TestIntent_EventsKeepKindAndData(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	withIntent(h, &fakeIntent{script: func(_ context.Context, _ app.IntentRequest, emit app.IntentEmit) app.IntentResult {
		emit("info", "warning: intent unavailable — OPENROUTER_API_KEY is not configured", json.RawMessage(`{"warning":"intent_unavailable"}`))
		emit("weird", "unknown kind", nil)
		return app.IntentResult{Trace: domain.UnavailableIntentTrace("OPENROUTER_API_KEY is not configured")}
	}})
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	var warn *domain.RunEvent
	for _, e := range h.bus.Buffer("run-1") {
		if e.Msg == "warning: intent unavailable — OPENROUTER_API_KEY is not configured" {
			warn = &e
		}
	}
	if warn == nil || warn.Kind != reviewer.EventInfo || string(warn.Data) != `{"warning":"intent_unavailable"}` {
		t.Errorf("warning event = %+v, want info with the data", warn)
	}
	if !h.hasEvent("run-1", reviewer.EventInfo, "unknown kind") {
		t.Errorf("an unknown event kind was not published as info: %v", h.events("run-1"))
	}
}

// FR5027, AC5033: an unavailable intent leaves the prompt without a section and the trace with
// the warning; the review goes on with the same result.
func TestIntent_UnavailableReviewsWithoutIntent(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	withIntent(h, &fakeIntent{script: func(_ context.Context, _ app.IntentRequest, emit app.IntentEmit) app.IntentResult {
		return app.IntentResult{Trace: domain.UnavailableIntentTrace("OPENROUTER_API_KEY is not configured")}
	}})
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()

	if in := h.engine.inputs[0]; in.Intent != nil {
		t.Errorf("engine input intent = %+v, want nil", in.Intent)
	}
	rec := h.store.run("run-1")
	if rec.status != domain.StatusDone {
		t.Fatalf("status = %q, want done", rec.status)
	}
	if diff := cmp.Diff(domain.UnavailableIntentTrace("OPENROUTER_API_KEY is not configured"), rec.trace.Intent); diff != "" {
		t.Errorf("trace intent mismatch (-want +got):\n%s", diff)
	}
}

// A resolver that breaks its contract never fails the review.
func TestIntent_ResolverPanicDegradesToUnavailable(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	withIntent(h, &fakeIntent{script: func(context.Context, app.IntentRequest, app.IntentEmit) app.IntentResult {
		panic("boom sk-secret")
	}})
	h.exec.Start(t.Context(), h.batch(2))
	h.wait()

	for _, id := range []string{"run-1", "run-2"} {
		if !h.hasEvent(id, reviewer.EventInfo, "warning: intent unavailable — internal error") {
			t.Errorf("%s events = %v, want the warning event", id, h.events(id))
		}
		rec := h.store.run(id)
		if rec.status != domain.StatusDone || rec.trace.Intent == nil || rec.trace.Intent.Status != domain.IntentStatusUnavailable ||
			rec.trace.Intent.Warning != "internal error" {
			t.Errorf("%s = status %q intent %+v, want done with an unavailable intent", id, rec.status, rec.trace.Intent)
		}
	}
	if got := h.logs.atLevel(t, "ERROR"); len(got) != 1 {
		t.Errorf("error log lines = %v, want exactly one for the panic", got)
	}
	if got := h.logs.text(); strings.Contains(got, "sk-secret") {
		t.Errorf("log leaks the panic text unredacted:\n%s", got)
	}
}

// An answer with neither an intent nor a trace counts as unavailable; an intent without a trace
// still gets a used trace so the trace matches the section.
func TestIntent_ContractGaps(t *testing.T) {
	t.Run("empty result", func(t *testing.T) {
		h := newHarness(t, Options{}, fixedClock)
		withIntent(h, &fakeIntent{script: func(context.Context, app.IntentRequest, app.IntentEmit) app.IntentResult { return app.IntentResult{} }})
		h.exec.Start(t.Context(), h.batch(1))
		h.wait()
		if it := h.store.run("run-1").trace.Intent; it == nil || it.Status != domain.IntentStatusUnavailable {
			t.Errorf("trace intent = %+v, want unavailable", it)
		}
	})
	t.Run("intent without a trace", func(t *testing.T) {
		h := newHarness(t, Options{}, fixedClock)
		withIntent(h, &fakeIntent{script: func(context.Context, app.IntentRequest, app.IntentEmit) app.IntentResult {
			return app.IntentResult{Intent: testIntent()}
		}})
		h.exec.Start(t.Context(), h.batch(1))
		h.wait()
		if it := h.store.run("run-1").trace.Intent; it == nil || it.Status != domain.IntentStatusUsed {
			t.Errorf("trace intent = %+v, want used", it)
		}
		if h.engine.inputs[0].Intent == nil {
			t.Error("engine input has no intent")
		}
	})
}

// AC5035: a failed or cancelled run's ended trace carries the same intent; a diff failure ends
// before the hook, so no request is made and the trace has none.
func TestIntent_EndedTracesCarryTheIntent(t *testing.T) {
	t.Run("provider error", func(t *testing.T) {
		h := newHarness(t, Options{}, fixedClock)
		withIntent(h, &fakeIntent{script: usedResult})
		h.engine.script = func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
			return reviewer.Outcome{}, errors.New("provider exploded")
		}
		h.exec.Start(t.Context(), h.batch(1))
		h.wait()
		rec := h.store.run("run-1")
		if rec.status != domain.StatusFailed || rec.trace == nil || !rec.trace.Ended {
			t.Fatalf("run = %+v, want failed with an ended trace", rec)
		}
		if diff := cmp.Diff(usedTrace(), rec.trace.Intent); diff != "" {
			t.Errorf("ended trace intent mismatch (-want +got):\n%s", diff)
		}
		if rec.completion.TokensIn != 0 {
			t.Errorf("failed run usage = %+v, want none (the intent call is not the run's)", rec.completion)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		h := newHarness(t, Options{}, fixedClock)
		withIntent(h, &fakeIntent{script: usedResult})
		started := make(chan struct{})
		h.engine.script = func(ctx context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
			close(started)
			<-ctx.Done()
			return reviewer.Outcome{}, ctx.Err()
		}
		h.exec.Start(t.Context(), h.batch(1))
		<-started
		h.bus.Cancel("run-1")
		h.wait()
		rec := h.store.run("run-1")
		if rec.status != domain.StatusCancelled || rec.trace == nil || !rec.trace.Ended {
			t.Fatalf("run = %+v, want cancelled with an ended trace", rec)
		}
		if diff := cmp.Diff(usedTrace(), rec.trace.Intent); diff != "" {
			t.Errorf("ended trace intent mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("cancelled while queued", func(t *testing.T) {
		h := newHarness(t, Options{AgentConcurrency: 1}, fixedClock)
		withIntent(h, &fakeIntent{script: usedResult})
		release := make(chan struct{})
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
		h.bus.Publish("run-2", reviewer.EventInfo, domain.CancellationRequestedMsg, nil)
		h.bus.Cancel("run-2")
		h.store.mu.Lock()
		h.store.runs["run-2"].status = domain.StatusCancelled
		h.store.mu.Unlock()
		h.bus.Complete("run-2")
		h.bus.evict("run-2")
		close(release)
		h.wait()
		rec := h.store.run("run-2")
		if rec.trace == nil || !rec.trace.Ended {
			t.Fatalf("run-2 = %+v, want an ended trace", rec)
		}
		if diff := cmp.Diff(usedTrace(), rec.trace.Intent); diff != "" {
			t.Errorf("queued run's ended trace intent mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("diff failure", func(t *testing.T) {
		h := newHarness(t, Options{}, fixedClock)
		f := withIntent(h, &fakeIntent{script: usedResult})
		h.store.filesErr = errors.New("db down")
		h.exec.Start(t.Context(), h.batch(2))
		h.wait()
		if f.calls() != 0 {
			t.Errorf("Resolve calls = %d after a diff failure, want 0", f.calls())
		}
		for _, id := range []string{"run-1", "run-2"} {
			if tr := h.store.run(id).trace; tr == nil || !tr.Ended || tr.Intent != nil {
				t.Errorf("%s trace = %+v, want an ended trace with a nil intent", id, tr)
			}
		}
	})
}

// gatedIntent blocks in Resolve until its context ends (reporting that) or release is closed.
type gatedIntent struct {
	entered  chan struct{}
	release  chan struct{}
	aborted  chan struct{} // closed when the call saw its ctx end
	abortedO sync.Once
}

func newGatedIntent() *gatedIntent {
	return &gatedIntent{entered: make(chan struct{}), release: make(chan struct{}), aborted: make(chan struct{})}
}

func (g *gatedIntent) script(ctx context.Context, _ app.IntentRequest, emit app.IntentEmit) app.IntentResult {
	close(g.entered)
	select {
	case <-g.release:
		return usedResult(ctx, app.IntentRequest{}, emit)
	case <-ctx.Done():
		g.abortedO.Do(func() { close(g.aborted) })
		return app.IntentResult{Trace: domain.UnavailableIntentTrace("intent derivation cancelled")}
	}
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// AC5029: the derivation is aborted only when every run of the request is cancelled.
func TestIntent_CancelOneOfTwoKeepsTheDerivationRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{}, time.Now)
		g := newGatedIntent()
		withIntent(h, &fakeIntent{script: g.script})
		h.exec.Start(t.Context(), h.batch(2))
		<-g.entered

		h.bus.Cancel("run-1")
		synctest.Wait()
		if isClosed(g.aborted) {
			t.Fatal("one cancelled run of two aborted the shared derivation")
		}
		close(g.release)
		h.wait()

		if got := h.store.run("run-2"); got.status != domain.StatusDone {
			t.Errorf("run-2 status = %q, want done with the intent", got.status)
		}
		if got := h.store.run("run-1"); got.status != domain.StatusCancelled {
			t.Errorf("run-1 status = %q, want cancelled", got.status)
		}
		if got := h.engine.calls(); got != 1 {
			t.Errorf("engine reviewed %d runs, want 1 (the cancelled one stops at its checkpoint)", got)
		}
		for _, in := range h.engine.inputs {
			if in.Intent == nil {
				t.Errorf("an engine input has no intent, want the shared one in every run")
			}
		}
	})
}

func TestIntent_CancelOfEveryRunAbortsTheDerivation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{}, time.Now)
		g := newGatedIntent()
		withIntent(h, &fakeIntent{script: g.script})
		h.exec.Start(t.Context(), h.batch(3))
		<-g.entered

		h.bus.Cancel("run-1")
		h.bus.Cancel("run-2")
		h.bus.Cancel("run-2") // a repeated cancel does not count twice
		synctest.Wait()
		if isClosed(g.aborted) {
			t.Fatal("two cancelled runs of three aborted the derivation")
		}
		h.bus.Cancel("run-3")
		<-g.aborted
		h.wait()

		for _, id := range []string{"run-1", "run-2", "run-3"} {
			rec := h.store.run(id)
			if rec.status != domain.StatusCancelled {
				t.Errorf("%s status = %q, want cancelled", id, rec.status)
			}
			if rec.trace == nil || !rec.trace.Ended || rec.trace.Intent == nil || rec.trace.Intent.Status != domain.IntentStatusUnavailable {
				t.Errorf("%s trace = %+v, want an ended trace with the unavailable intent", id, rec.trace)
			}
		}
		if got := h.engine.calls(); got != 0 {
			t.Errorf("engine made %d calls after every run was cancelled, want 0", got)
		}
	})
}

// Runs cancelled before the hook counts: the context is already ended when Resolve is called.
func TestIntent_RunsCancelledBeforeTheHookEndTheContextAtOnce(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	gate := make(chan struct{})
	h.store.filesGate = gate
	var seen sync.WaitGroup
	seen.Add(1)
	f := withIntent(h, &fakeIntent{script: func(ctx context.Context, _ app.IntentRequest, _ app.IntentEmit) app.IntentResult {
		defer seen.Done()
		if ctx.Err() == nil {
			t.Error("Resolve ctx is live although every run was cancelled during the diff load")
		}
		return app.IntentResult{Trace: domain.UnavailableIntentTrace("cancelled")}
	}})
	h.exec.Start(t.Context(), h.batch(2))
	h.bus.Cancel("run-1")
	h.bus.Cancel("run-2")
	close(gate)
	h.wait()
	seen.Wait()
	if f.calls() != 1 {
		t.Errorf("Resolve calls = %d, want 1", f.calls())
	}
}

// The executor's Shutdown cancels every run, so it also ends a derivation in flight.
func TestIntent_ShutdownAbortsTheDerivation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{}, time.Now)
		g := newGatedIntent()
		withIntent(h, &fakeIntent{script: g.script})
		h.exec.Start(t.Context(), h.batch(2))
		<-g.entered

		if err := h.exec.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown() = %v, want nil", err)
		}
		if !isClosed(g.aborted) {
			t.Error("Shutdown did not end the derivation's context")
		}
		for _, id := range []string{"run-1", "run-2"} {
			if rec := h.store.run(id); rec.status != domain.StatusCancelled {
				t.Errorf("%s status = %q, want cancelled", id, rec.status)
			}
		}
	})
}

// The derivation's context does not outlive the hook: it is released when Resolve returns.
func TestIntent_ContextIsReleasedAfterTheHook(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	f := withIntent(h, &fakeIntent{script: usedResult})
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()
	if f.ctxs[0].Err() == nil {
		t.Error("the Resolve context is still live after the batch finished")
	}
}
