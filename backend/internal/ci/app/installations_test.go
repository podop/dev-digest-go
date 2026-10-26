package app_test

import (
	"context"
	"errors"
	"testing"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
)

type fakeRepoStore struct {
	list []domain.RepoInstallation
	err  error
	ws   string
}

func (f *fakeRepoStore) ListRepoInstallations(_ context.Context, ws string) ([]domain.RepoInstallation, error) {
	f.ws = ws
	return f.list, f.err
}

func TestListByRepo_GroupsAndScopesToTheWorkspace(t *testing.T) {
	t.Parallel()
	store := &fakeRepoStore{list: []domain.RepoInstallation{
		{ID: "2", Repo: "acme/b", AgentName: "x"},
		{ID: "1", Repo: "acme/a", AgentName: "x"},
	}}
	got, err := app.NewInstallationsService(store).ListByRepo(t.Context(), "ws-1")
	if err != nil {
		t.Fatalf("ListByRepo() error = %v, want nil", err)
	}
	if store.ws != "ws-1" {
		t.Errorf("store workspace = %q, want ws-1", store.ws)
	}
	if len(got) != 2 || got[0].Repo != "acme/a" || got[1].Repo != "acme/b" {
		t.Errorf("ListByRepo() = %+v, want acme/a then acme/b", got)
	}
}

func TestListByRepo_StoreFailureIsWrapped(t *testing.T) {
	t.Parallel()
	boom := errors.New("db down")
	got, err := app.NewInstallationsService(&fakeRepoStore{err: boom}).ListByRepo(t.Context(), "ws-1")
	if !errors.Is(err, boom) || got != nil {
		t.Errorf("ListByRepo() = %v, %v, want nil and an error wrapping the store error", got, err)
	}
}
