package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"dev-digest-go/backend/internal/reviews/domain"
)

// learnFixture is a review with one finding that has a suggestion.
func learnFixture(t *testing.T) (fixture, domain.FindingRecord) {
	t.Helper()
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	fi := finding("a.go", 1, 2, "N+1 query")
	fi.Suggestion = strp("batch it")
	f.doneReview(t, agent, nil, fi)
	var id string
	if err := f.db.Raw(`SELECT id FROM findings`).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	target, err := f.store.FindLearnTarget(t.Context(), f.ws, id)
	if err != nil {
		t.Fatalf("FindLearnTarget() error = %v", err)
	}
	return f, target.Finding
}

func TestFindLearnTarget(t *testing.T) {
	f, fi := learnFixture(t)
	got, err := f.store.FindLearnTarget(t.Context(), f.ws, fi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RepoID != f.repo || got.PRNumber != 482 || got.Finding.ID != fi.ID || got.Finding.Title != "N+1 query" ||
		got.Finding.Suggestion == nil || *got.Finding.Suggestion != "batch it" || got.Finding.MemoryID != nil {
		t.Errorf("FindLearnTarget() = %+v", got)
	}
	for name, ws := range map[string]string{"other workspace": f.other} {
		if _, err := f.store.FindLearnTarget(t.Context(), ws, fi.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("FindLearnTarget(%s) error = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := f.store.FindLearnTarget(t.Context(), f.ws, missingID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindLearnTarget(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestInsertLearnedMemory_Row(t *testing.T) {
	f, fi := learnFixture(t)
	target, _ := f.store.FindLearnTarget(t.Context(), f.ws, fi.ID)
	m := domain.NewLearnMemory(f.ws, target)
	id, err := f.store.InsertLearnedMemory(t.Context(), m)
	if err != nil {
		t.Fatalf("InsertLearnedMemory() error = %v", err)
	}
	q := func(col string) string { return f.str(t, `SELECT `+col+` FROM memory WHERE id = ?`, id) }
	if got := q("scope || '/' || kind"); got != "repo/learning" {
		t.Errorf("scope/kind = %q", got)
	}
	if got, want := q("content"), "N+1 query\n\nbecause N+1 query\n\nSuggested fix: batch it"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if got := q("repo_id::text"); got != f.repo {
		t.Errorf("repo_id = %q, want %q", got, f.repo)
	}
	if got := q("embedding::text"); got != "<nil>" {
		t.Errorf("embedding = %q, want NULL", got)
	}
	if got := q("confidence::text"); got != "0.9" {
		t.Errorf("confidence = %q", got)
	}
	if n := f.count(t, `SELECT count(*) FROM memory WHERE sources @> ?::jsonb`, `[{"pr":482,"context":"finding:`+fi.ID+`"}]`); n != 1 {
		t.Errorf("sources do not contain pr and context")
	}
}

func TestLearnedMemoryID(t *testing.T) {
	f, fi := learnFixture(t)
	if _, ok, err := f.store.LearnedMemoryID(t.Context(), f.ws, fi.ID); err != nil || ok {
		t.Fatalf("LearnedMemoryID(none) ok = %v, err = %v, want false, nil", ok, err)
	}
	src := `[{"pr": 1, "context": "finding:` + fi.ID + `"}]`
	// Wrong kind and wrong workspace never count; two matches resolve to the oldest.
	f.exec(t, `INSERT INTO memory (workspace_id, scope, kind, content, sources) VALUES (?, 'repo', 'fact', 'c', ?::jsonb)`, f.ws, src)
	f.exec(t, `INSERT INTO memory (workspace_id, scope, kind, content, sources) VALUES (?, 'repo', 'learning', 'c', ?::jsonb)`, f.other, src)
	f.exec(t, `INSERT INTO memory (workspace_id, scope, kind, content, sources) VALUES (?, 'repo', 'learning', 'c', '[{"context":"finding:other"}]'::jsonb)`, f.ws)
	f.exec(t, `INSERT INTO memory (workspace_id, scope, kind, content, sources) VALUES (?, 'repo', 'learning', 'c', NULL)`, f.ws)
	f.insert(t, `INSERT INTO memory (workspace_id, scope, kind, content, sources, created_at) VALUES (?, 'repo', 'learning', 'c', ?::jsonb, '2026-10-22 11:00:00+00') RETURNING id`, f.ws, src)
	older := f.insert(t, `INSERT INTO memory (workspace_id, scope, kind, content, sources, created_at) VALUES (?, 'repo', 'learning', 'c', ?::jsonb, '2026-10-22 10:00:00+00') RETURNING id`, f.ws, src)

	id, ok, err := f.store.LearnedMemoryID(t.Context(), f.ws, fi.ID)
	if err != nil || !ok || id != older {
		t.Errorf("LearnedMemoryID() = %q, %v, %v, want %q, true, nil", id, ok, err, older)
	}
}

// TestLearn_ConcurrentCallsMakeOneMemory is AC4066 at the store: two transactions run the Learn
// sequence (lock, look up, insert) and exactly one memory row results, both seeing its id.
func TestLearn_ConcurrentCallsMakeOneMemory(t *testing.T) {
	f, fi := learnFixture(t)
	target, _ := f.store.FindLearnTarget(t.Context(), f.ws, fi.ID)
	learn := func(ctx context.Context) (string, error) {
		var id string
		err := f.tx.InTx(ctx, func(ctx context.Context) error {
			if err := f.store.LockLearn(ctx, fi.ID); err != nil {
				return err
			}
			existing, ok, err := f.store.LearnedMemoryID(ctx, f.ws, fi.ID)
			if err != nil {
				return err
			}
			if ok {
				id = existing
				return nil
			}
			id, err = f.store.InsertLearnedMemory(ctx, domain.NewLearnMemory(f.ws, target))
			return err
		})
		return id, err
	}

	const callers = 8
	ids := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Go(func() { ids[i], errs[i] = learn(t.Context()) })
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("caller %d error = %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("caller %d got memory %s, want %s", i, ids[i], ids[0])
		}
	}
	if n := f.count(t, `SELECT count(*) FROM memory WHERE kind = 'learning'`); n != 1 {
		t.Errorf("memory rows = %d, want 1", n)
	}
}

// TestLockLearn_SerialisesTransactions: while one transaction holds the lock, another waits; the
// lock is gone once the first one commits.
func TestLockLearn_SerialisesTransactions(t *testing.T) {
	f, fi := learnFixture(t)
	held, release := make(chan struct{}), make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- f.tx.InTx(t.Context(), func(ctx context.Context) error {
			if err := f.store.LockLearn(ctx, fi.ID); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- f.tx.InTx(t.Context(), func(ctx context.Context) error { return f.store.LockLearn(ctx, fi.ID) })
	}()
	// The waiter shows up as an ungranted advisory lock.
	deadline := 0
	for f.count(t, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`) == 0 {
		if deadline++; deadline > 2000 {
			t.Fatal("the second transaction never waited for the advisory lock")
		}
		select {
		case err := <-waiterDone:
			t.Fatalf("second LockLearn returned %v while the first transaction held the lock", err)
		default:
		}
		sleepBriefly()
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	if err := <-waiterDone; err != nil {
		t.Errorf("second LockLearn error = %v", err)
	}
}
