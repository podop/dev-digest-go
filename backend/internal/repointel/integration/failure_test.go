package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/jobs"
	"dev-digest-go/backend/internal/repointel/domain"
)

// pauseKey names the advisory lock the pause trigger waits on.
const pauseKey = "test-pause"

// AC3029: a transaction that fails after the symbols insert leaves all seven tables as they
// were.
func TestFull_FailureAfterSymbolsRollsBackEverything(t *testing.T) {
	e := newEnv(t)
	e.mustFull()
	before := e.snapshot()

	e.failInserts(`"references"`, "forced failure")
	e.git.Commit(map[string]string{"src/a.ts": withMore("src/a.ts", "runA2")})
	_, err := e.indexer.Full(t.Context(), e.repo)
	if err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("Full() error = %v, want the forced failure", err)
	}
	if diff := cmp.Diff(before, e.snapshot()); diff != "" {
		t.Errorf("tables changed by a failed index (-before +after):\n%s", diff)
	}
}

// AC3030: when HEAD cannot be read, the index is written without a sha and without a cached map.
func TestFull_HeadFailureWritesNoMap(t *testing.T) {
	e := newEnv(t)
	e.git.setHeadErr(errors.New("fatal: not a git repository"))
	e.mustFull()

	if st := e.mustState(); st.LastIndexedSHA != "" || st.Status != domain.StatusFull {
		t.Errorf("state = {%s %q}, want {full \"\"}", st.Status, st.LastIndexedSHA)
	}
	if got := e.count("repo_map_cache"); got != 0 {
		t.Errorf("repo_map_cache rows = %d, want 0", got)
	}
}

// AC3039: a database error leaves a failed state when there was no usable index, and the
// previous full state when there was one.
func TestFull_DatabaseErrorRecordsAFailedStateOnlyWithoutAnIndex(t *testing.T) {
	long := strings.Repeat("z", 900)
	t.Run("no previous state", func(t *testing.T) {
		e := newEnv(t)
		e.failInserts("symbols", "boom "+long)
		if _, err := e.indexer.Full(t.Context(), e.repo); err == nil {
			t.Fatal("Full() error = nil, want the database error")
		}

		st := e.mustState()
		if st.Status != domain.StatusFailed || st.Stats["reason"] != "index_failed" {
			t.Errorf("state = {%s %v}, want {failed index_failed}", st.Status, st.Stats["reason"])
		}
		msg, _ := st.Stats["error"].(string)
		if msg == "" || len([]rune(msg)) > domain.MaxErrorChars || !strings.Contains(msg, "boom") {
			t.Errorf("stats.error has %d characters (%.40q...), want 1..500 starting from the database error", len([]rune(msg)), msg)
		}
		view := e.service.IndexState(t.Context(), e.repo)
		if !view.Degraded || view.DegradedReason != domain.ReasonIndexFailed || view.Status != domain.StatusFailed {
			t.Errorf("IndexState() = {%s degraded %v %s}, want {failed true index_failed}", view.Status, view.Degraded, view.DegradedReason)
		}
		if got := e.count("symbols"); got != 0 {
			t.Errorf("symbols rows = %d, want 0 after the rollback", got)
		}
	})
	t.Run("previous full state", func(t *testing.T) {
		e := newEnv(t)
		e.mustFull()
		before := e.snapshot()
		e.failInserts("symbols", "boom")
		e.git.Commit(map[string]string{"src/a.ts": withMore("src/a.ts", "runA2")})
		if _, err := e.indexer.Full(t.Context(), e.repo); err == nil {
			t.Fatal("Full() error = nil, want the database error")
		}
		if diff := cmp.Diff(before, e.snapshot()); diff != "" {
			t.Errorf("tables changed (-before +after):\n%s", diff)
		}
	})
}

// AC3040: an index blocked past its attempt limit is rolled back and recorded as failed; one
// interrupted by shutdown writes no state and stops within the shutdown timeout.
func TestIndexJob_InterruptedAttemptsAreRolledBack(t *testing.T) {
	prepare := func(t *testing.T, limit time.Duration) (*env, *jobs.Runner, string) {
		e := newEnv(t)
		e.pauseInserts("symbols", pauseKey)
		e.holdLock(pauseKey)
		r := e.runner(limit)
		id, err := r.Enqueue(t.Context(), e.ws, domain.KindIndex, map[string]string{"repoId": e.repo})
		if err != nil {
			t.Fatal(err)
		}
		// The symbols are inserted and the transaction waits in the trigger.
		e.waitWaiters(1)
		return e, r, id
	}

	t.Run("attempt limit", func(t *testing.T) {
		e, r, id := prepare(t, time.Second)
		if err := r.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if rec := e.job(id); rec.Status != "failed" || !strings.Contains(rec.Error, "timed out") {
			t.Errorf("job = %+v, want failed with an attempt timeout", rec)
		}
		st := e.mustState()
		if st.Status != domain.StatusFailed || st.Stats["reason"] != "index_failed" {
			t.Errorf("state = {%s %v}, want {failed index_failed}", st.Status, st.Stats["reason"])
		}
		if got := e.count("symbols"); got != 0 {
			t.Errorf("symbols rows = %d, want 0: the blocked transaction must be rolled back", got)
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		e, r, id := prepare(t, time.Minute)
		start := time.Now()
		if err := r.Shutdown(jobs.DefaultShutdownTimeout); err != nil {
			t.Fatalf("Shutdown() = %v, want the job stopped in time", err)
		}
		if took := time.Since(start); took > jobs.DefaultShutdownTimeout {
			t.Errorf("job stopped after %s, want within %s", took, jobs.DefaultShutdownTimeout)
		}
		if _, ok := e.state(); ok {
			t.Error("a repo_index_state row exists after a shutdown, want none")
		}
		if got := e.count("symbols"); got != 0 {
			t.Errorf("symbols rows = %d, want 0: the interrupted transaction must be rolled back", got)
		}
		if rec := e.job(id); rec.Status != "failed" || !strings.Contains(rec.Error, "aborted by shutdown") {
			t.Errorf("job = %+v, want failed, aborted by shutdown", rec)
		}
	})
}

// AC3041: a repo deleted while the index waits for the repo lock is not resurrected; the job
// ends failed.
func TestIndexJob_RepoDeletedWhileWaitingForTheLock(t *testing.T) {
	e := newEnv(t)
	release := e.holdLock(e.repoLock())
	r := e.runner(time.Minute)
	id, err := r.Enqueue(t.Context(), e.ws, domain.KindIndex, map[string]string{"repoId": e.repo})
	if err != nil {
		t.Fatal(err)
	}
	e.waitWaiters(1)
	e.exec(`DELETE FROM repos WHERE id = ?`, e.repo)
	release()
	if err := r.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, ok := e.state(); ok {
		t.Error("a repo_index_state row exists for the deleted repo, want none")
	}
	if rec := e.job(id); rec.Status != "failed" {
		t.Errorf("job = %+v, want failed", rec)
	}
}

// AC3071: while an index is paused inside its transaction, the state route answers with what was
// committed before; after the commit it answers with the new state.
func TestState_ReadsDuringAnIndexTransaction(t *testing.T) {
	run := func(t *testing.T, e *env) (during domain.StateView, after domain.StateView) {
		t.Helper()
		e.pauseInserts("symbols", pauseKey)
		release := e.holdLock(pauseKey)
		done := make(chan error, 1)
		go func() {
			_, err := e.indexer.Full(t.Context(), e.repo)
			done <- err
		}()
		e.waitWaiters(1)
		during = e.service.IndexState(t.Context(), e.repo)
		release()
		if err := <-done; err != nil {
			t.Fatalf("Full() error = %v", err)
		}
		return during, e.service.IndexState(t.Context(), e.repo)
	}

	t.Run("no earlier state", func(t *testing.T) {
		e := newEnv(t)
		during, after := run(t, e)
		if diff := cmp.Diff(domain.Synthesised(e.repo), during); diff != "" {
			t.Errorf("IndexState() during the index (-want +got):\n%s", diff)
		}
		if after.Status != domain.StatusFull || after.LastIndexedSha != e.git.currentHead() {
			t.Errorf("IndexState() after the commit = {%s %q}, want {full %q}", after.Status, after.LastIndexedSha, e.git.currentHead())
		}
	})
	t.Run("earlier state", func(t *testing.T) {
		e := newEnv(t)
		e.mustFull()
		previous := e.service.IndexState(t.Context(), e.repo)
		e.git.Commit(map[string]string{"src/a.ts": withMore("src/a.ts", "runA2")})
		during, after := run(t, e)
		if diff := cmp.Diff(previous, during); diff != "" {
			t.Errorf("IndexState() during the index (-previous +got):\n%s", diff)
		}
		if after.LastIndexedSha != e.git.currentHead() || after.LastIndexedSha == previous.LastIndexedSha {
			t.Errorf("IndexState().LastIndexedSha after the commit = %q, want %q", after.LastIndexedSha, e.git.currentHead())
		}
	})
}
