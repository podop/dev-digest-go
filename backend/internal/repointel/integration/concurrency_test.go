package integration_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// AC3056: two index jobs of one repo that run together serialise on the repo lock, both end
// done without a unique violation, and the rows are those of a single index.
func TestIndexJobs_TwoAtOnceEndWithTheRowsOfOne(t *testing.T) {
	e := newEnv(t)
	release := e.holdLock(e.repoLock())
	r := e.runner(time.Minute)
	var ids []string
	for range 2 {
		id, err := r.Enqueue(t.Context(), e.ws, domain.KindIndex, map[string]string{"repoId": e.repo})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Both parsed the clone and now wait for the lock: the transactions overlap.
	e.waitWaiters(2)
	release()
	if err := r.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, id := range ids {
		if rec := e.job(id); rec.Status != "done" || rec.Error != "" {
			t.Errorf("job %s = %+v, want done without an error", id, rec)
		}
	}
	both := e.content()
	e.mustFull()
	if diff := cmp.Diff(e.content(), both); diff != "" {
		t.Errorf("rows of the two concurrent indexes differ from a single index (-single +concurrent):\n%s", diff)
	}
	if len(both["symbols"]) == 0 {
		t.Error("no symbols after the two indexes")
	}
}

// AC3070: an index whose parse is paused while the clone moves to another commit still commits;
// the resync that follows brings the rows to the new commit.
func TestIndex_CloneResetDuringTheParse(t *testing.T) {
	var gate *pauseAnalyzer
	e := newEnv(t, withWorkers(1), withAnalyzer(func(inner app.Analyzer) app.Analyzer {
		gate = newPauseAnalyzer(inner, 4) // after three of the eight files
		return gate
	}))
	oldHead := e.git.currentHead()

	done := make(chan error, 1)
	go func() {
		_, err := e.indexer.Full(t.Context(), e.repo)
		done <- err
	}()
	<-gate.paused
	// Every source file of the fixture changes; the files parsed so far hold the old text.
	next := map[string]string{}
	for p, text := range baseFiles() {
		if domain.IsSupported(p) {
			next[p] = text + "\nexport const moved = 1;\n"
		}
	}
	newHead := e.git.Commit(next)
	close(gate.release)
	if err := <-done; err != nil {
		t.Fatalf("Full() error = %v, want the first index to commit", err)
	}
	if st := e.mustState(); st.LastIndexedSHA != oldHead {
		t.Fatalf("last_indexed_sha after the first index = %q, want the sha read before the reset %q", st.LastIndexedSHA, oldHead)
	}

	if err := e.jobs.Resync(t.Context(), e.payload()); err != nil {
		t.Fatalf("Resync() error = %v", err)
	}
	if st := e.mustState(); st.LastIndexedSHA != newHead || !isIncremental(st) {
		t.Fatalf("state after the resync = {%q incremental %v}, want {%q true}", st.LastIndexedSHA, isIncremental(st), newHead)
	}
	resynced := e.content()
	e.mustFull()
	full := e.content()
	for _, table := range []string{"symbols", `"references"`, "file_edges", "file_rank", "file_facts", "repo_map_cache"} {
		if diff := cmp.Diff(full[table], resynced[table]); diff != "" {
			t.Errorf("%s after the resync differs from a full index of the new commit (-full +resync):\n%s", table, diff)
		}
	}
}
