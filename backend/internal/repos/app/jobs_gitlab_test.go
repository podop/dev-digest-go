package app_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

// seedGitLab inserts a GitLab repo straight into the store.
func seedGitLab(t *testing.T, store *memStore, host string, id int64, full string) domain.Repo {
	t.Helper()
	owner, name := full[:strings.LastIndex(full, "/")], full[strings.LastIndex(full, "/")+1:]
	repo, _, err := store.Add(t.Context(), app.NewRepo{
		WorkspaceID: ws, Ref: domain.Ref{Owner: owner, Name: name}, Provider: codehost.GitLab, Host: host,
		ProjectID: id, WebURL: "https://" + host + "/" + full,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func newGitLabJobs(store *memStore, g *fakeGit, conns *fakeConns) *app.Jobs {
	return app.NewJobs(store, g, app.JobsOptions{
		Now: func() time.Time { return time0 }, Connections: conns,
		CAFile: func() string { return "/data/git-ca-bundle.pem" },
	})
}

func TestJobs_Clone_GitLab(t *testing.T) {
	store := &memStore{}
	repo := seedGitLab(t, store, "git.corp.example:8443", 42, "acme/platform/payments")
	conns := &fakeConns{conns: map[string]app.GitLabConn{"git.corp.example:8443": {
		Name: "Corp", BaseURL: "https://git.corp.example:8443/gitlab", Host: "git.corp.example:8443", Token: "glpat-secret-token-value",
	}}}
	g := &fakeGit{cloneDir: "/data/@gitlab/git.corp.example_8443/42"}

	err := newGitLabJobs(store, g, conns).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID, Owner: "../x", Name: "y"}))
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	want := []gitlabCall{{
		Kind: "clone", First: "@gitlab/git.corp.example_8443", Second: "42",
		Remote: gitcli.Remote{
			URL:    "https://git.corp.example:8443/gitlab/acme/platform/payments.git",
			Prefix: "https://git.corp.example:8443/gitlab/",
			Token:  "glpat-secret-token-value", CAFile: "/data/git-ca-bundle.pem",
		},
	}}
	if diff := cmp.Diff(want, g.remoteCalls); diff != "" {
		t.Errorf("git calls mismatch (-want +got):\n%s", diff)
	}
	if g.cloneURL != "" {
		t.Errorf("GitHub clone ran for a GitLab repo: %q", g.cloneURL)
	}
	if g.cloneDepth != 1 {
		t.Errorf("clone depth = %d, want 1", g.cloneDepth)
	}
	got := store.get(repo.ID)
	if got.ClonePath == nil || *got.ClonePath != g.cloneDir || got.LastPolledAt == nil {
		t.Errorf("repo after clone = %+v, want clone path %q and a poll time", got, g.cloneDir)
	}
}

func TestJobs_Clone_GitLab_AnonymousConnection(t *testing.T) {
	store := &memStore{}
	repo := seedGitLab(t, store, "gitlab.com", 1, "acme/web")
	conns := &fakeConns{conns: map[string]app.GitLabConn{"gitlab.com": {Name: "GitLab.com", BaseURL: "https://gitlab.com", Host: "gitlab.com"}}}
	g := &fakeGit{cloneDir: "/d"}

	if err := app.NewJobs(store, g, app.JobsOptions{Connections: conns}).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID})); err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if r := g.remoteCalls[0].Remote; r.Token != "" || r.CAFile != "" || r.URL != "https://gitlab.com/acme/web.git" {
		t.Errorf("remote = %#v, want anonymous, default roots, plain URL", r)
	}
}

func TestJobs_GitLab_NotConnected(t *testing.T) {
	tests := []struct {
		name  string
		conns *fakeConns
	}{
		{"no connection for the host", &fakeConns{conns: map[string]app.GitLabConn{"other.example": {Host: "other.example", BaseURL: "https://other.example"}}}},
		{"no connection port", nil},
	}
	for _, tc := range tests {
		for _, kind := range []string{"clone", "refresh"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				store := &memStore{}
				repo := seedGitLab(t, store, "gitlab.com", 42, "acme/web")
				cloned(t, store, repo)
				g := &fakeGit{}
				opts := app.JobsOptions{}
				if tc.conns != nil {
					opts.Connections = tc.conns
				}
				jobs := app.NewJobs(store, g, opts)

				var err error
				if kind == "clone" {
					err = jobs.Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID}))
				} else {
					err = jobs.Refresh(t.Context(), payload(t, app.RefreshPayload{RepoID: repo.ID}))
				}
				var nc *app.NotConnectedError
				if !errors.As(err, &nc) || nc.Host != "gitlab.com" {
					t.Fatalf("%s error = %v, want *NotConnectedError for gitlab.com", kind, err)
				}
				if want := "No GitLab connection for gitlab.com. Add one in Settings → GitLab Integration."; err.Error() != want {
					t.Errorf("message = %q, want %q", err.Error(), want)
				}
				if retry.IsTransient(err) {
					t.Error("a not-connected job error is transient, want it not retried")
				}
				if len(g.remoteCalls) != 0 || g.cloneURL != "" || g.synced {
					t.Errorf("git ran for a repo without a connection: %+v", g.remoteCalls)
				}
			})
		}
	}
}

func TestJobs_Refresh_GitLab(t *testing.T) {
	conn := app.GitLabConn{Name: "GitLab.com", BaseURL: "https://gitlab.com", Host: "gitlab.com", Token: "glpat-secret-token-value"}
	tests := []struct {
		name       string
		git        *fakeGit
		wantBranch string
	}{
		{"remote default branch", &fakeGit{defaultBranch: "develop"}, "develop"},
		{"failure falls back to the stored branch", &fakeGit{defaultBranchErr: errBoom}, "main"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &memStore{}
			// The row carries the renamed path: the remote URL follows it, the clone key does not.
			repo := seedGitLab(t, store, "gitlab.com", 42, "acme/core/payments")
			cloned(t, store, repo)
			conns := &fakeConns{conns: map[string]app.GitLabConn{"gitlab.com": conn}}

			if err := newGitLabJobs(store, tc.git, conns).Refresh(t.Context(), payload(t, app.RefreshPayload{RepoID: repo.ID})); err != nil {
				t.Fatalf("Refresh() error = %v", err)
			}
			remote := gitcli.Remote{
				URL: "https://gitlab.com/acme/core/payments.git", Prefix: "https://gitlab.com/",
				Token: conn.Token, CAFile: "/data/git-ca-bundle.pem",
			}
			want := []gitlabCall{
				{Kind: "default-branch", First: "@gitlab/gitlab.com", Second: "42", Remote: remote},
				{Kind: "sync", First: "@gitlab/gitlab.com", Second: "42", Branch: tc.wantBranch, Remote: remote},
			}
			if diff := cmp.Diff(want, tc.git.remoteCalls); diff != "" {
				t.Errorf("git calls mismatch (-want +got):\n%s", diff)
			}
			if tc.git.synced {
				t.Error("the GitHub sync ran for a GitLab repo")
			}
			if got := store.get(repo.ID); got.LastPolledAt == nil || !got.LastPolledAt.Equal(time0) {
				t.Errorf("last_polled_at = %v, want %v", got.LastPolledAt, time0)
			}
		})
	}
}

func TestJobs_GitLab_Failures(t *testing.T) {
	conns := func() *fakeConns {
		return &fakeConns{conns: map[string]app.GitLabConn{"gitlab.com": {BaseURL: "https://gitlab.com", Host: "gitlab.com"}}}
	}
	t.Run("clone failure leaves the repo unchanged", func(t *testing.T) {
		store := &memStore{}
		repo := seedGitLab(t, store, "gitlab.com", 42, "acme/web")
		err := newGitLabJobs(store, &fakeGit{cloneErr: errBoom}, conns()).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID}))
		if !errors.Is(err, errBoom) {
			t.Errorf("Clone() error = %v, want errBoom", err)
		}
		if got := store.get(repo.ID); got.ClonePath != nil {
			t.Errorf("clone path = %v, want none", got.ClonePath)
		}
	})
	t.Run("sync failure", func(t *testing.T) {
		store := &memStore{}
		repo := seedGitLab(t, store, "gitlab.com", 42, "acme/web")
		cloned(t, store, repo)
		err := newGitLabJobs(store, &fakeGit{syncErr: errBoom}, conns()).Refresh(t.Context(), payload(t, app.RefreshPayload{RepoID: repo.ID}))
		if !errors.Is(err, errBoom) {
			t.Errorf("Refresh() error = %v, want errBoom", err)
		}
	})
	t.Run("connection lookup failure", func(t *testing.T) {
		store := &memStore{}
		repo := seedGitLab(t, store, "gitlab.com", 42, "acme/web")
		c := conns()
		c.resolveErr = errBoom
		err := newGitLabJobs(store, &fakeGit{}, c).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID}))
		var nc *app.NotConnectedError
		if !errors.Is(err, errBoom) || errors.As(err, &nc) {
			t.Errorf("Clone() error = %v, want the lookup error, not not-connected", err)
		}
	})
	t.Run("stored path that is not a plain project path", func(t *testing.T) {
		store := &memStore{}
		repo := seedGitLab(t, store, "gitlab.com", 42, "acme/web")
		_ = store.update(repo.ID, func(r *domain.Repo) { r.FullName = "acme/../web?x=1" })
		g := &fakeGit{}
		err := newGitLabJobs(store, g, conns()).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID}))
		if err == nil || len(g.remoteCalls) != 0 {
			t.Errorf("Clone() = %v with %d git calls, want a refusal before git", err, len(g.remoteCalls))
		}
	})
	t.Run("no project id", func(t *testing.T) {
		store := &memStore{}
		repo := seedGitLab(t, store, "gitlab.com", 42, "acme/web")
		_ = store.update(repo.ID, func(r *domain.Repo) { r.ProjectID = 0 })
		g := &fakeGit{}
		if err := newGitLabJobs(store, g, conns()).Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID})); err == nil || len(g.remoteCalls) != 0 {
			t.Errorf("Clone() = %v with %d git calls, want a refusal before git", err, len(g.remoteCalls))
		}
	})
}

func TestJobs_GitLab_IndexIsTriggered(t *testing.T) {
	store := &memStore{}
	repo := seedGitLab(t, store, "gitlab.com", 42, "acme/web")
	conns := &fakeConns{conns: map[string]app.GitLabConn{"gitlab.com": {BaseURL: "https://gitlab.com", Host: "gitlab.com"}}}
	tr := &fakeTrigger{}
	jobs := app.NewJobs(store, &fakeGit{cloneDir: "/d"}, app.JobsOptions{Connections: conns, Index: tr})

	if err := jobs.Clone(t.Context(), payload(t, app.ClonePayload{RepoID: repo.ID})); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{fmt.Sprintf("index %s %s acme/web", ws, repo.ID)}, tr.calls); diff != "" {
		t.Errorf("trigger calls mismatch (-want +got):\n%s", diff)
	}
}
