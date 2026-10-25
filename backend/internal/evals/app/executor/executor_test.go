package executor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"dev-digest-go/backend/internal/evals/app/executor"
	"dev-digest-go/backend/internal/evals/domain"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func caseIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%d", i+1)
	}
	return ids
}

func storeOf(ids []string) *fakeStore {
	s := newFakeStore()
	for _, id := range ids {
		s.cases[id] = evalCase(id)
	}
	return s
}

// launch starts the run and waits until every goroutine of the bubble is blocked or gone.
func (h *harness) launch(run domain.Run) {
	h.t.Helper()
	h.exec.Launch(h.t.Context(), run)
}

func TestLaunch_RunsAtMostThreeCasesAtATime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ids := caseIDs(8)
		var cur, peak atomic.Int32
		llm := func(_ context.Context, _ executor.LLMBinding, _ reviewerapp.Call) (reviewerapp.CallResult, error) {
			n := cur.Add(1)
			defer cur.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(200 * time.Millisecond)
			return emptyReview(), nil
		}
		h := newHarness(t, storeOf(ids), llm, executor.Options{}, time.Now)
		h.launch(evalRun(ids...))
		time.Sleep(10 * time.Second) // 8 cases in 3 slots at 200 ms: ends at 600 ms
		synctest.Wait()

		if got := peak.Load(); got != 3 {
			t.Errorf("peak concurrent LLM calls = %d, want 3", got)
		}
		records, finals := h.store.snapshot()
		if len(records) != 8 {
			t.Errorf("recorded %d results, want 8", len(records))
		}
		if len(finals) != 1 || finals[0].Status != domain.RunCompleted {
			t.Errorf("finals = %+v, want one completed run", finals)
		}
		if err := h.exec.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown() = %v, want nil", err)
		}
	})
}

func TestLaunch_ACaseOverItsBudgetIsAnErrorAndTheOthersComplete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ids := caseIDs(8)
		llm := func(ctx context.Context, _ executor.LLMBinding, c reviewerapp.Call) (reviewerapp.CallResult, error) {
			if caseOf(c) == "c3" {
				<-ctx.Done()
				return reviewerapp.CallResult{}, ctx.Err()
			}
			time.Sleep(200 * time.Millisecond)
			return emptyReview(), nil
		}
		h := newHarness(t, storeOf(ids), llm, executor.Options{CaseBudget: 2 * time.Second}, time.Now)
		h.launch(evalRun(ids...))
		time.Sleep(10 * time.Second)
		synctest.Wait()

		records, finals := h.store.snapshot()
		if len(records) != 8 {
			t.Fatalf("recorded %d results, want 8", len(records))
		}
		for _, r := range records {
			switch {
			case r.CaseID == "c3":
				if r.Status != domain.CaseError || r.Error == nil || *r.Error != "Case timed out after 2s" {
					t.Errorf("c3 = %s %v, want error \"Case timed out after 2s\"", r.Status, r.Error)
				}
			case r.Status == domain.CaseError:
				t.Errorf("%s errored: %v", r.CaseID, *r.Error)
			}
		}
		if len(finals) != 1 || finals[0].Status != domain.RunCompleted || finals[0].CasesErrored != 1 {
			t.Errorf("finals = %+v, want one completed run with one errored case", finals)
		}
		_ = h.exec.Shutdown(t.Context())
	})
}

func TestShutdown_AbortsCasesAndTheRunEndsInterrupted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ids := caseIDs(5)
		llm := func(ctx context.Context, _ executor.LLMBinding, _ reviewerapp.Call) (reviewerapp.CallResult, error) {
			<-ctx.Done()
			return reviewerapp.CallResult{}, ctx.Err()
		}
		h := newHarness(t, storeOf(ids), llm, executor.Options{}, time.Now)
		h.launch(evalRun(ids...))
		synctest.Wait() // three cases are inside their LLM call

		start := time.Now()
		if err := h.exec.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown() = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Shutdown took %v of fake time, want it to return as soon as the run recorded itself", elapsed)
		}
		records, finals := h.store.snapshot()
		if len(records) != 0 {
			t.Errorf("records = %d, want none (aborted cases are not results)", len(records))
		}
		if len(finals) != 1 || finals[0].Status != domain.RunFailed || finals[0].Error == nil || *finals[0].Error != "interrupted" {
			t.Fatalf("finals = %+v, want one failed \"interrupted\"", finals)
		}
		if got := len(h.store.loads); got != 3 {
			t.Errorf("loaded %d cases, want 3 (no further case is picked up)", got)
		}
	})
}

func TestShutdown_GivesUpAfterTheDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		llm := func(context.Context, executor.LLMBinding, reviewerapp.Call) (reviewerapp.CallResult, error) {
			time.Sleep(30 * time.Second) // ignores the abort
			return emptyReview(), nil
		}
		h := newHarness(t, storeOf(caseIDs(1)), llm, executor.Options{}, time.Now)
		h.launch(evalRun("c1"))
		synctest.Wait()

		start := time.Now()
		err := h.exec.Shutdown(t.Context())
		if !errors.Is(err, executor.ErrNotDrained) {
			t.Errorf("Shutdown() = %v, want ErrNotDrained", err)
		}
		if elapsed := time.Since(start); elapsed != domain.ShutdownDrain {
			t.Errorf("Shutdown took %v, want the %v drain", elapsed, domain.ShutdownDrain)
		}
		if _, finals := h.store.snapshot(); len(finals) != 0 {
			t.Errorf("finals = %+v, want none yet (the run stays running until the reap)", finals)
		}

		// The abandoned run still ends: it records itself interrupted when the call returns.
		time.Sleep(time.Minute)
		synctest.Wait()
		if _, finals := h.store.snapshot(); len(finals) != 1 || *finals[0].Error != "interrupted" {
			t.Errorf("finals after the late return = %+v, want one failed \"interrupted\"", finals)
		}
	})
}

func TestShutdown_StopsWhenItsContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		llm := func(context.Context, executor.LLMBinding, reviewerapp.Call) (reviewerapp.CallResult, error) {
			time.Sleep(time.Minute)
			return emptyReview(), nil
		}
		h := newHarness(t, storeOf(caseIDs(1)), llm, executor.Options{Drain: time.Hour}, time.Now)
		h.launch(evalRun("c1"))
		synctest.Wait()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := h.exec.Shutdown(ctx); !errors.Is(err, executor.ErrNotDrained) {
			t.Errorf("Shutdown() = %v, want ErrNotDrained", err)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
	})
}

func TestLaunch_AfterShutdownIsRefused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		llm := func(context.Context, executor.LLMBinding, reviewerapp.Call) (reviewerapp.CallResult, error) {
			calls.Add(1)
			return emptyReview(), nil
		}
		h := newHarness(t, storeOf(caseIDs(1)), llm, executor.Options{}, time.Now)
		if err := h.exec.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown() = %v, want nil", err)
		}
		h.launch(evalRun("c1"))
		synctest.Wait()

		if _, finals := h.store.snapshot(); len(finals) != 0 || calls.Load() != 0 || len(h.store.loads) != 0 {
			t.Errorf("a refused run did work: finals %+v, llm calls %d, loads %v", finals, calls.Load(), h.store.loads)
		}
		var warned bool
		for _, l := range h.logs.lines(t) {
			warned = warned || (l.Level == "warn" && l.Obj["runId"] == "run-1")
		}
		if !warned {
			t.Errorf("no warning for the refused launch; log:\n%s", h.logs.text())
		}
		if err := h.exec.Shutdown(t.Context()); err != nil {
			t.Errorf("second Shutdown() = %v, want nil", err)
		}
	})
}

// panicEngine panics for the case named in the task line and otherwise runs the real engine.
func panicEngine(caseID string) executor.Engine {
	return func(ctx context.Context, in reviewerapp.Input, llm reviewerapp.LLM, ev reviewerapp.Events, opts reviewerapp.Options) (reviewer.Outcome, error) {
		if strings.Contains(in.Task, `"Case `+caseID+`"`) {
			panic("boom " + secretMarker)
		}
		return reviewerapp.Run(ctx, in, llm, ev, opts)
	}
}

func TestLaunch_AWorkerPanicEndsOnlyThatRunFailed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ids := caseIDs(4)
		store := storeOf(ids)
		prov := &fakeProviders{llm: func(context.Context, executor.LLMBinding, reviewerapp.Call) (reviewerapp.CallResult, error) {
			time.Sleep(time.Second)
			return emptyReview(), nil
		}}
		logs := &logBuf{}
		exec := executor.New(executor.Deps{Store: store, Providers: prov, Engine: panicEngine("c2"), Log: logs.logger()}, executor.Options{})
		exec.Launch(t.Context(), evalRun(ids...))
		time.Sleep(10 * time.Second)
		synctest.Wait()

		_, finals := store.snapshot()
		if len(finals) != 1 || finals[0].Status != domain.RunFailed || finals[0].Error == nil || *finals[0].Error != "internal error" {
			t.Fatalf("finals = %+v, want one failed \"internal error\"", finals)
		}
		var crash *logLine
		for _, l := range logs.lines(t) {
			if l.Msg == "eval run: background execution crashed" {
				crash = &l
			}
		}
		if crash == nil {
			t.Fatalf("no crash line; log:\n%s", logs.text())
		}
		if crash.Level != "error" || crash.Obj["runId"] != "run-1" || crash.Obj["err"] != "boom [REDACTED]" {
			t.Errorf("crash line = %+v, want error level, runId run-1, redacted err", crash)
		}
		if stack, _ := crash.Obj["stack"].(string); !strings.Contains(stack, "panicEngine") {
			t.Errorf("crash stack does not name the panicking worker:\n%s", stack)
		}
		if strings.Contains(logs.text(), secretMarker) {
			t.Error("the log holds the unredacted secret")
		}
		if err := exec.Shutdown(t.Context()); err != nil {
			t.Errorf("Shutdown() = %v, want nil", err)
		}
	})
}

func TestLaunch_ErrorTextsAreRedactedBeforeTheyAreCut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := strings.Repeat("x", 494) + secretMarker + " tail"
		h := newHarness(t, storeOf(caseIDs(2)), nil, executor.Options{}, time.Now)
		h.prov.resolveErr = errors.New(raw)
		h.store.finishFail = "write " + secretMarker
		h.launch(evalRun("c1", "c2"))
		synctest.Wait()

		records, finals := h.store.snapshot()
		if len(records) != 2 || len(finals) != 1 {
			t.Fatalf("records %d, finals %d, want 2 and 1", len(records), len(finals))
		}
		for _, r := range records {
			if r.Status != domain.CaseError || r.Error == nil {
				t.Fatalf("%s = %s, want an error case", r.CaseID, r.Status)
			}
			if strings.Contains(*r.Error, "sk-or") || len(*r.Error) != domain.ErrorTextMax {
				t.Errorf("%s error = %q, want a redacted text of %d units", r.CaseID, *r.Error, domain.ErrorTextMax)
			}
		}
		if finals[0].Status != domain.RunFailed || finals[0].Error == nil || *finals[0].Error != *records[0].Error {
			t.Errorf("final = %+v, want failed with the first case error", finals[0])
		}
		if strings.Contains(h.logs.text(), secretMarker) {
			t.Errorf("the log holds the unredacted secret:\n%s", h.logs.text())
		}
		_ = h.exec.Shutdown(t.Context())
	})
}

func TestLaunch_LogLinesCarryNoContent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := storeOf(caseIDs(1))
		c := store.cases["c1"]
		c.InputMeta.PRTitle = "Case c1"
		pr := "TOP-SECRET PR TEXT"
		c.InputMeta.PRDescription = &pr
		store.cases["c1"] = c
		llm := func(context.Context, executor.LLMBinding, reviewerapp.Call) (reviewerapp.CallResult, error) {
			return emptyReview(), nil
		}
		h := newHarness(t, store, llm, executor.Options{}, time.Now)
		h.launch(evalRun("c1"))
		synctest.Wait()

		lines := h.logs.lines(t)
		var msgs []string
		for _, l := range lines {
			msgs = append(msgs, l.Msg)
		}
		if want := []string{"eval run started", "eval run finished"}; !slices.Equal(msgs, want) {
			t.Errorf("log messages = %v, want %v", msgs, want)
		}
		for _, banned := range []string{"security reviewer", "src/a.ts", pr} {
			if strings.Contains(h.logs.text(), banned) {
				t.Errorf("a log line holds %q:\n%s", banned, h.logs.text())
			}
		}
		_ = h.exec.Shutdown(t.Context())
	})
}

func TestLaunch_TheLLMReceivesTheBindingOfTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		type binding struct{ Provider, Model, RunID, WorkspaceID, CaseID string }
		var got []binding
		llm := func(_ context.Context, b executor.LLMBinding, _ reviewerapp.Call) (reviewerapp.CallResult, error) {
			mu.Lock()
			got = append(got, binding{b.Provider, b.Model, b.RunID, b.WorkspaceID, b.CaseID})
			mu.Unlock()
			return emptyReview(), nil
		}
		h := newHarness(t, storeOf(caseIDs(1)), llm, executor.Options{}, time.Now)
		h.launch(evalRun("c1"))
		synctest.Wait()

		want := []binding{{"openrouter", "m", "run-1", "ws-1", "c1"}}
		if !slices.Equal(got, want) {
			t.Errorf("bindings = %+v, want %+v", got, want)
		}
		_ = h.exec.Shutdown(t.Context())
	})
}
