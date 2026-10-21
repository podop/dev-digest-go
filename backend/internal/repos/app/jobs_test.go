package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

var time0 = time.Date(2026, 10, 21, 9, 15, 0, 0, time.UTC)

func payload(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newJobs(store *memStore, g *fakeGit) *app.Jobs {
	return app.NewJobs(store, g, app.JobsOptions{Now: func() time.Time { return time0 }})
}

func seedRepo(t *testing.T, store *memStore, url string) domain.Repo {
	t.Helper()
	repo, _, err := app.NewService(store, &fakeQueue{}, "/data").Add(t.Context(), ws, "u", mustRef(t, url))
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestJobs_Clone(t *testing.T) {
	// An SSH-form URL is stored as owner/name only, so the clone always goes over HTTPS.
	store := &memStore{}
	repo := seedRepo(t, store, "git@github.com:o/r.git")
	g := &fakeGit{cloneDir: "/data/workspace/o/r"}

	err := newJobs(store, g).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID, Owner: "o", Name: "r"}))
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if want := "https://github.com/o/r.git"; g.cloneURL != want {
		t.Errorf("git clone url = %q, want %q", g.cloneURL, want)
	}
	if g.cloneDepth != 1 {
		t.Errorf("git clone depth = %d, want 1", g.cloneDepth)
	}
	got := store.get(repo.ID)
	if got.ClonePath == nil || *got.ClonePath != "/data/workspace/o/r" {
		t.Errorf("clone_path = %v, want /data/workspace/o/r", got.ClonePath)
	}
	if got.LastPolledAt == nil || !got.LastPolledAt.Equal(time0) {
		t.Errorf("last_polled_at = %v, want %v", got.LastPolledAt, time0)
	}
}

func TestJobs_Clone_UsesRowNotPayload(t *testing.T) {
	store := &memStore{}
	repo := seedRepo(t, store, "https://github.com/o/r")
	g := &fakeGit{cloneDir: "/d"}

	err := newJobs(store, g).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID, Owner: "../evil", Name: "x"}))
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if g.cloneOwner != "o" || g.cloneName != "r" {
		t.Errorf("git clone target = %s/%s, want the stored o/r", g.cloneOwner, g.cloneName)
	}
}

func TestJobs_Clone_CustomRemote(t *testing.T) {
	store := &memStore{}
	repo := seedRepo(t, store, "https://github.com/o/r")
	g := &fakeGit{cloneDir: "/d"}
	jobs := app.NewJobs(store, g, app.JobsOptions{RemoteBase: "file:///srv/git/"})

	if err := jobs.Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID})); err != nil {
		t.Fatal(err)
	}
	if want := "file:///srv/git/o/r.git"; g.cloneURL != want {
		t.Errorf("git clone url = %q, want %q", g.cloneURL, want)
	}
}

func TestJobs_Clone_GitFailureLeavesRepoUnchanged(t *testing.T) {
	store := &memStore{}
	repo := seedRepo(t, store, "https://github.com/o/r")
	g := &fakeGit{cloneErr: errBoom}

	err := newJobs(store, g).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID}))
	if !errors.Is(err, errBoom) {
		t.Fatalf("Clone() error = %v, want errBoom", err)
	}
	if got := store.get(repo.ID); got.ClonePath != nil || got.LastPolledAt != nil {
		t.Errorf("repo after failed clone = %+v, want unchanged", got)
	}
}

func TestJobs_Clone_DeletedRepo(t *testing.T) {
	t.Run("before start", func(t *testing.T) {
		store := &memStore{}
		g := &fakeGit{}
		err := newJobs(store, g).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: "gone"}))
		if err != nil {
			t.Errorf("Clone() error = %v, want nil", err)
		}
		if g.cloneURL != "" {
			t.Errorf("git clone ran for a deleted repo (url %q)", g.cloneURL)
		}
	})
	t.Run("during clone", func(t *testing.T) {
		store := &memStore{}
		repo := seedRepo(t, store, "https://github.com/o/r")
		g := &fakeGit{cloneDir: "/d", onClone: func() { store.remove(repo.ID) }}

		if err := newJobs(store, g).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID})); err != nil {
			t.Errorf("Clone() error = %v, want nil", err)
		}
		if got, _ := store.List(context.Background(), ws); len(got) != 0 {
			t.Errorf("repos = %+v, want none recreated", got)
		}
	})
}

func TestJobs_BadPayload(t *testing.T) {
	jobs := newJobs(&memStore{}, &fakeGit{})
	if err := jobs.Clone(t.Context(), json.RawMessage(`{"repoId":`)); err == nil {
		t.Error("Clone(bad json) error = nil, want an error")
	}
	if err := jobs.Refresh(t.Context(), nil); err == nil {
		t.Error("Refresh(nil) error = nil, want an error")
	}
}

func cloned(t *testing.T, store *memStore, repo domain.Repo) {
	t.Helper()
	if err := store.SetCloned(t.Context(), repo.ID, "/data/o/r", time0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestJobs_Refresh(t *testing.T) {
	tests := []struct {
		name       string
		git        *fakeGit
		wantBranch string
	}{
		{"remote default branch", &fakeGit{defaultBranch: "develop"}, "develop"},
		{"no symbolic HEAD falls back to stored branch", &fakeGit{defaultBranchErr: gitcli.ErrNoDefaultBranch}, "main"},
		{"ls-remote failure falls back to stored branch", &fakeGit{defaultBranchErr: errBoom}, "main"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &memStore{}
			repo := seedRepo(t, store, "https://github.com/o/r")
			cloned(t, store, repo)

			if err := newJobs(store, tc.git).Refresh(t.Context(), payload(t, app.RefreshPayload{RepoID: repo.ID})); err != nil {
				t.Fatalf("Refresh() error = %v", err)
			}
			if tc.git.syncBranch != tc.wantBranch {
				t.Errorf("synced branch = %q, want %q", tc.git.syncBranch, tc.wantBranch)
			}
			got := store.get(repo.ID)
			if got.LastPolledAt == nil || !got.LastPolledAt.Equal(time0) {
				t.Errorf("last_polled_at = %v, want %v", got.LastPolledAt, time0)
			}
			if tc.git.cloneURL != "" {
				t.Error("refresh must not clone")
			}
		})
	}
}

func TestJobs_Refresh_Failures(t *testing.T) {
	t.Run("sync error keeps last_polled_at", func(t *testing.T) {
		store := &memStore{}
		repo := seedRepo(t, store, "https://github.com/o/r")
		cloned(t, store, repo)
		before := store.get(repo.ID).LastPolledAt

		err := newJobs(store, &fakeGit{defaultBranch: "main", syncErr: errBoom}).
			Refresh(t.Context(), payload(t, app.RefreshPayload{RepoID: repo.ID}))
		if !errors.Is(err, errBoom) {
			t.Fatalf("Refresh() error = %v, want errBoom", err)
		}
		if got := store.get(repo.ID).LastPolledAt; !got.Equal(*before) {
			t.Errorf("last_polled_at = %v, want %v", got, before)
		}
	})
	t.Run("not cloned", func(t *testing.T) {
		store := &memStore{}
		repo := seedRepo(t, store, "https://github.com/o/r")
		g := &fakeGit{}
		if err := newJobs(store, g).Refresh(t.Context(), payload(t, app.RefreshPayload{RepoID: repo.ID})); err == nil {
			t.Error("Refresh(uncloned) error = nil, want an error")
		}
		if g.synced {
			t.Error("Sync ran for a repo without clone")
		}
	})
	t.Run("deleted repo", func(t *testing.T) {
		g := &fakeGit{}
		if err := newJobs(&memStore{}, g).Refresh(t.Context(), payload(t, app.RefreshPayload{RepoID: "gone"})); err != nil {
			t.Errorf("Refresh() error = %v, want nil", err)
		}
		if g.synced {
			t.Error("Sync ran for a deleted repo")
		}
	})
	t.Run("canceled while resolving the branch does not fall back", func(t *testing.T) {
		store := &memStore{}
		repo := seedRepo(t, store, "https://github.com/o/r")
		cloned(t, store, repo)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		g := &fakeGit{defaultBranchErr: context.Canceled}
		if err := newJobs(store, g).Refresh(ctx, payload(t, app.RefreshPayload{RepoID: repo.ID})); !errors.Is(err, context.Canceled) {
			t.Errorf("Refresh() error = %v, want context.Canceled", err)
		}
		if g.synced {
			t.Error("Sync ran after cancellation")
		}
	})
}
