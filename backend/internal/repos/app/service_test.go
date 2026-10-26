package app_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

const ws = "ws-1"

func mustParsed(t *testing.T, url string) domain.Parsed {
	t.Helper()
	parsed, err := domain.ParseURL(url)
	if err != nil {
		t.Fatalf("ParseURL(%q) error = %v", url, err)
	}
	return parsed
}

func TestService_Add(t *testing.T) {
	store, queue := &memStore{}, &fakeQueue{}
	svc := app.NewService(store, queue, "/data/workspace")

	repo, created, err := svc.Add(t.Context(), ws, "user-1", mustParsed(t, "https://github.com/o/r.git"))
	if err != nil || !created {
		t.Fatalf("Add() = %+v, %v, %v, want created", repo, created, err)
	}
	if repo.FullName != "o/r" || repo.DefaultBranch != "main" || repo.ClonePath != nil || repo.LastPolledAt != nil {
		t.Errorf("Add() repo = %+v, want o/r on main, not cloned, never polled", repo)
	}
	want := []enqueued{{ws, "clone", app.ClonePayload{RepoID: repo.ID, Owner: "o", Name: "r"}}}
	if diff := cmp.Diff(want, queue.jobs); diff != "" {
		t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
	}

	// The same repo in another URL form exists: same row, nothing enqueued.
	again, created, err := svc.Add(t.Context(), ws, "user-1", mustParsed(t, "git@github.com:o/r"))
	if err != nil || created || again.ID != repo.ID {
		t.Errorf("Add(existing) = %+v, %v, %v, want the same repo, created=false", again, created, err)
	}
	if len(queue.jobs) != 1 {
		t.Errorf("jobs after duplicate add = %d, want 1", len(queue.jobs))
	}

	// Full names are case-sensitive.
	other, created, err := svc.Add(t.Context(), ws, "user-1", mustParsed(t, "https://github.com/O/r"))
	if err != nil || !created || other.ID == repo.ID {
		t.Errorf("Add(O/r) = %+v, %v, %v, want a new repo", other, created, err)
	}
}

func TestService_Add_EnqueueFails(t *testing.T) {
	store, queue := &memStore{}, &fakeQueue{err: errBoom}
	svc := app.NewService(store, queue, "/data")

	_, created, err := svc.Add(t.Context(), ws, "u", mustParsed(t, "https://github.com/o/r"))
	if !errors.Is(err, errBoom) || created {
		t.Errorf("Add() created = %v, error = %v, want errBoom", created, err)
	}
	if got, _ := store.List(t.Context(), ws); len(got) != 1 {
		t.Errorf("repos after failed enqueue = %d, want the row to stay", len(got))
	}
}

func TestService_Refresh(t *testing.T) {
	store, queue := &memStore{}, &fakeQueue{}
	svc := app.NewService(store, queue, "/data")
	fresh, _, _ := svc.Add(t.Context(), ws, "u", mustParsed(t, "https://github.com/o/fresh"))
	cloned, _, _ := svc.Add(t.Context(), ws, "u", mustParsed(t, "https://github.com/o/cloned"))
	if err := store.SetCloned(t.Context(), cloned.ID, "/data/o/cloned", time0); err != nil {
		t.Fatal(err)
	}
	queue.jobs = nil

	tests := []struct {
		name string
		id   string
		want enqueued
	}{
		{"not cloned gets a clone job", fresh.ID, enqueued{ws, "clone", app.ClonePayload{RepoID: fresh.ID, Owner: "o", Name: "fresh"}}},
		{"cloned gets a refresh job", cloned.ID, enqueued{ws, "refresh", app.RefreshPayload{RepoID: cloned.ID}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			queue.jobs = nil
			if err := svc.Refresh(t.Context(), ws, tc.id); err != nil {
				t.Fatalf("Refresh(%q) error = %v", tc.id, err)
			}
			if diff := cmp.Diff([]enqueued{tc.want}, queue.jobs); diff != "" {
				t.Errorf("Refresh(%q) enqueued mismatch (-want +got):\n%s", tc.id, diff)
			}
		})
	}
}

func TestService_NotFound(t *testing.T) {
	store, queue := &memStore{}, &fakeQueue{}
	svc := app.NewService(store, queue, "/data")
	repo, _, _ := svc.Add(t.Context(), "ws-other", "u", mustParsed(t, "https://github.com/o/r"))
	queue.jobs = nil

	for name, call := range map[string]func() error{
		"refresh unknown":  func() error { return svc.Refresh(t.Context(), ws, "nope") },
		"refresh other ws": func() error { return svc.Refresh(t.Context(), ws, repo.ID) },
		"delete unknown":   func() error { return svc.Delete(t.Context(), ws, "nope") },
		"delete other ws":  func() error { return svc.Delete(t.Context(), ws, repo.ID) },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			ae, ok := apperr.As(err)
			if !ok || ae.Kind != apperr.KindNotFound || ae.Message != "Repo not found" {
				t.Errorf("error = %v, want not_found \"Repo not found\"", err)
			}
		})
	}
	if len(queue.jobs) != 0 {
		t.Errorf("jobs = %d, want none", len(queue.jobs))
	}
	if got, _ := store.List(t.Context(), "ws-other"); len(got) != 1 {
		t.Errorf("other workspace repos = %d, want it untouched", len(got))
	}
}

func TestService_DeleteAndOverview(t *testing.T) {
	store, queue := &memStore{}, &fakeQueue{}
	svc := app.NewService(store, queue, "/data/workspace")
	a, _, _ := svc.Add(t.Context(), ws, "u", mustParsed(t, "https://github.com/o/a"))
	b, _, _ := svc.Add(t.Context(), ws, "u", mustParsed(t, "https://github.com/o/b"))

	ov, err := svc.Overview(t.Context(), ws)
	if err != nil {
		t.Fatal(err)
	}
	if ov.WorkspaceID != ws || ov.CloneDir != "/data/workspace" || len(ov.Repos) != 2 {
		t.Errorf("Overview() = %+v, want 2 repos under /data/workspace", ov)
	}

	if err := svc.Delete(t.Context(), ws, a.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	got, err := svc.List(t.Context(), ws)
	if err != nil || len(got) != 1 || got[0].ID != b.ID {
		t.Errorf("List() after delete = %+v, %v, want only %s", got, err, b.ID)
	}
}
