package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

func backfillStore() *fakeStore {
	s := newFakeStore()
	add := func(id, ws string, cloned bool, version int) {
		r := domain.Repo{ID: id, WorkspaceID: ws, Owner: "acme", Name: id}
		if cloned {
			r.ClonePath = "/clones/" + id
		}
		s.repos[id] = r
		if version > 0 {
			s.states[id] = domain.IndexState{RepoID: id, IndexerVersion: version, Status: domain.StatusFull}
		}
	}
	add("r-b", "ws-2", true, 1)  // old version
	add("r-a", "ws-1", true, 0)  // no state
	add("r-c", "ws-1", true, 2)  // current version
	add("r-d", "ws-1", false, 0) // not cloned
	return s
}

func newBackfill(s *fakeStore, q *fakeQueue) *app.Backfill {
	jobs := app.NewJobs(app.JobsDeps{Queue: q}, app.JobsOptions{})
	return app.NewBackfill(s, jobs)
}

func TestBackfill_EnqueuesAnIndexForEveryRepoWithoutACurrentIndex(t *testing.T) {
	q := &fakeQueue{}
	n, err := newBackfill(backfillStore(), q).Run(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("Run() = %d, %v, want 2 jobs and no error", n, err)
	}
	want := []queuedJob{
		{"ws-1", "repo-intel-index", `{"repoId":"r-a","owner":"acme","name":"r-a"}`},
		{"ws-2", "repo-intel-index", `{"repoId":"r-b","owner":"acme","name":"r-b"}`},
	}
	if diff := cmp.Diff(want, q.jobs); diff != "" {
		t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
	}
}

func TestBackfill_ListingFailureIsReturned(t *testing.T) {
	s := backfillStore()
	boom := errors.New(`relation "repos" does not exist`)
	s.hooks["ListBackfill"] = func(context.Context) error { return boom }
	q := &fakeQueue{}
	n, err := newBackfill(s, q).Run(t.Context())
	if n != 0 || !errors.Is(err, boom) || len(q.jobs) != 0 {
		t.Errorf("Run() = %d, %v with %d jobs, want 0 and the listing error", n, err, len(q.jobs))
	}
}

func TestBackfill_OneFailedEnqueueDoesNotStopTheRest(t *testing.T) {
	q := &fakeQueue{failFor: `"r-a"`}
	n, err := newBackfill(backfillStore(), q).Run(t.Context())
	if n != 1 || err == nil {
		t.Fatalf("Run() = %d, %v, want 1 job and the enqueue error", n, err)
	}
	if len(q.jobs) != 1 || q.jobs[0].Workspace != "ws-2" {
		t.Errorf("enqueued jobs = %+v, want only r-b's", q.jobs)
	}
}
