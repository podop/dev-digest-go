package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/postgres"
	pgstore "dev-digest-go/backend/internal/pulls/adapters/postgres"
	"dev-digest-go/backend/internal/pulls/domain"
)

// gitlabRepo inserts a GitLab repository of the workspace and returns its id.
func gitlabRepo(t *testing.T, f fixture, wsID, host string, projectID int64, fullName, owner, name string) string {
	t.Helper()
	var id string
	scan(t, f.db, &id, `INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url)
		VALUES (?, ?, ?, ?, 'gitlab', ?, ?, ?) RETURNING id`,
		wsID, owner, name, fullName, host, projectID, "https://"+host+"/"+fullName)
	return id
}

type repoState struct {
	FullName, Owner, Name, WebURL string
}

func stateOf(t *testing.T, f fixture, id string) repoState {
	t.Helper()
	var s repoState
	scan(t, f.db, &s, `SELECT full_name, owner, name, web_url FROM repos WHERE id = ?`, id)
	return s
}

func TestFindRepo_gitlabAddress(t *testing.T) {
	f := seeded(t)
	id := gitlabRepo(t, f, f.wsID, "git.corp.example:8443", 42, "acme/team/web", "acme/team", "web")

	got, err := f.store.FindRepo(t.Context(), f.wsID, id)
	if err != nil {
		t.Fatalf("FindRepo() error = %v", err)
	}
	want := domain.Repo{ID: id, Owner: "acme/team", Name: "web", Provider: "gitlab", Host: "git.corp.example:8443", ProjectID: 42}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindRepo() mismatch (-want +got):\n%s", diff)
	}

	targets, err := f.store.ListPollTargets(t.Context())
	if err != nil {
		t.Fatalf("ListPollTargets() error = %v", err)
	}
	var found bool
	for _, tg := range targets {
		if tg.Repo.ID == id {
			found = true
			if diff := cmp.Diff(domain.PollTarget{WorkspaceID: f.wsID, Repo: want}, tg); diff != "" {
				t.Errorf("ListPollTargets() GitLab target mismatch (-want +got):\n%s", diff)
			}
		}
	}
	if !found {
		t.Errorf("ListPollTargets() lacks the GitLab repository %s", id)
	}
}

func TestUpsertListed_gitlabRepoKeepsForkFlag(t *testing.T) {
	f := seeded(t)
	id := gitlabRepo(t, f, f.wsID, "gitlab.com", 7, "grp/proj", "grp", "proj")
	l := domain.Listed{Number: 3, Title: "t", Author: "a", Branch: "b", Base: "main", HeadSHA: "h1", Status: "open",
		OpenedAt: day(1, 9), UpdatedAt: day(2, 9), HeadIsFork: false}
	if err := f.store.UpsertListed(t.Context(), f.wsID, id, []domain.Listed{l}); err != nil {
		t.Fatalf("UpsertListed() error = %v", err)
	}
	l.HeadIsFork, l.HeadSHA = true, "h2"
	if err := f.store.UpsertListed(t.Context(), f.wsID, id, []domain.Listed{l}); err != nil {
		t.Fatalf("UpsertListed() second error = %v", err)
	}
	var got struct {
		HeadIsFork bool
		HeadSHA    string
	}
	scan(t, f.db, &got, `SELECT head_is_fork, head_sha FROM pull_requests WHERE repo_id = ? AND number = 3`, id)
	if !got.HeadIsFork || got.HeadSHA != "h2" {
		t.Errorf("stored head_is_fork, head_sha = %v, %q, want true, h2 (DoUpdates list)", got.HeadIsFork, got.HeadSHA)
	}
}

func project(id int64, full, owner, name, host string) domain.GitLabProject {
	return domain.GitLabProject{ID: id, FullName: full, Owner: owner, Name: name, DefaultBranch: "main", WebURL: "https://" + host + "/" + full}
}

func TestUpdateRepoPath(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	var otherWS string
	scan(t, f.db, &otherWS, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)

	t.Run("renamed project gets the new path and web URL", func(t *testing.T) {
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 100, "old/web", "old", "web")
		ok, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(100, "new/team/web2", "new/team", "web2", "gitlab.com"))
		if err != nil || !ok {
			t.Fatalf("UpdateRepoPath() = %v, %v, want true, nil", ok, err)
		}
		want := repoState{FullName: "new/team/web2", Owner: "new/team", Name: "web2", WebURL: "https://gitlab.com/new/team/web2"}
		if diff := cmp.Diff(want, stateOf(t, f, id)); diff != "" {
			t.Errorf("stored repo mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("same path again is an update", func(t *testing.T) {
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 101, "same/web", "same", "web")
		ok, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(101, "same/web", "same", "web", "gitlab.com"))
		if err != nil || !ok {
			t.Errorf("UpdateRepoPath(unchanged path) = %v, %v, want true, nil", ok, err)
		}
	})

	t.Run("collision with another repository of the workspace and host changes nothing", func(t *testing.T) {
		var logs bytes.Buffer
		f.store = pgstore.NewStore(f.db, pgstore.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
		keep := gitlabRepo(t, f, f.wsID, "gitlab.com", 110, "taken/web", "taken", "web")
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 111, "mine/web", "mine", "web")
		before := stateOf(t, f, id)
		ok, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(111, "taken/web", "taken", "web", "gitlab.com"))
		if err != nil || ok {
			t.Fatalf("UpdateRepoPath(collision) = %v, %v, want false, nil", ok, err)
		}
		if diff := cmp.Diff(before, stateOf(t, f, id)); diff != "" {
			t.Errorf("colliding repo changed (-before +after):\n%s", diff)
		}
		line := logs.String()
		if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "repo_id="+id) || !strings.Contains(line, "other_repo_id="+keep) {
			t.Errorf("WARN line = %q, want a WARN naming repo_id=%s and other_repo_id=%s", line, id, keep)
		}
		if strings.Contains(line, "taken/web") || strings.Contains(line, "mine/web") {
			t.Errorf("WARN line = %q, must not name paths", line)
		}
		if got := stateOf(t, f, keep).FullName; got != "taken/web" {
			t.Errorf("other repo full_name = %q, want taken/web", got)
		}
	})

	t.Run("the same path on another host or in another workspace is no collision", func(t *testing.T) {
		gitlabRepo(t, f, f.wsID, "other.example", 120, "shared/web", "shared", "web")
		gitlabRepo(t, f, otherWS, "gitlab.com", 121, "shared/web", "shared", "web")
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 122, "mine2/web", "mine2", "web")
		ok, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(122, "shared/web", "shared", "web", "gitlab.com"))
		if err != nil || !ok {
			t.Errorf("UpdateRepoPath() = %v, %v, want true, nil", ok, err)
		}
	})

	t.Run("collision inside a transaction leaves it usable", func(t *testing.T) {
		gitlabRepo(t, f, f.wsID, "gitlab.com", 130, "tx/taken", "tx", "taken")
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 131, "tx/mine", "tx", "mine")
		err := postgres.NewTx(f.db).InTx(ctx, func(ctx context.Context) error {
			ok, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(131, "tx/taken", "tx", "taken", "gitlab.com"))
			if err != nil || ok {
				t.Errorf("UpdateRepoPath(collision in tx) = %v, %v, want false, nil", ok, err)
			}
			_, err = f.store.FindRepo(ctx, f.wsID, id)
			return err
		})
		if err != nil {
			t.Errorf("transaction after a collision error = %v, want nil", err)
		}
	})

	t.Run("joins the caller's transaction", func(t *testing.T) {
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 140, "rb/web", "rb", "web")
		boom := errors.New("rollback")
		err := postgres.NewTx(f.db).InTx(ctx, func(ctx context.Context) error {
			if ok, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(140, "rb/web9", "rb", "web9", "gitlab.com")); err != nil || !ok {
				t.Errorf("UpdateRepoPath() = %v, %v, want true, nil", ok, err)
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("InTx error = %v, want the rollback error", err)
		}
		if got := stateOf(t, f, id).FullName; got != "rb/web" {
			t.Errorf("full_name after rollback = %q, want rb/web", got)
		}
	})

	t.Run("not found cases", func(t *testing.T) {
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 150, "nf/web", "nf", "web")
		tests := []struct {
			name string
			ws   string
			repo string
			p    domain.GitLabProject
		}{
			{"other workspace", otherWS, id, project(150, "nf/x", "nf", "x", "gitlab.com")},
			{"other project id", f.wsID, id, project(151, "nf/x", "nf", "x", "gitlab.com")},
			{"GitHub repository", f.wsID, f.repoID, project(150, "nf/x", "nf", "x", "gitlab.com")},
		}
		for _, tc := range tests {
			ok, err := f.store.UpdateRepoPath(ctx, tc.ws, tc.repo, tc.p)
			if ok || !errors.Is(err, domain.ErrRepoNotFound) {
				t.Errorf("UpdateRepoPath(%s) = %v, %v, want false, ErrRepoNotFound", tc.name, ok, err)
			}
		}
		if got := stateOf(t, f, id).FullName; got != "nf/web" {
			t.Errorf("full_name = %q, want nf/web unchanged", got)
		}
	})

	t.Run("incomplete project is refused", func(t *testing.T) {
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 160, "inc/web", "inc", "web")
		p := project(160, "inc/x", "inc", "x", "gitlab.com")
		p.WebURL = ""
		if ok, err := f.store.UpdateRepoPath(ctx, f.wsID, id, p); ok || err == nil {
			t.Errorf("UpdateRepoPath(no web URL) = %v, %v, want false and an error", ok, err)
		}
	})
}
