package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/postgres"
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
	want := domain.Repo{ID: id, Owner: "acme/team", Name: "web", Provider: "gitlab", Host: "git.corp.example:8443", ProjectID: 42,
		WebURL: "https://git.corp.example:8443/acme/team/web"}
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
		skipped, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(100, "new/team/web2", "new/team", "web2", "gitlab.com"))
		if err != nil || skipped != "" {
			t.Fatalf("UpdateRepoPath() = %q, %v, want it applied", skipped, err)
		}
		want := repoState{FullName: "new/team/web2", Owner: "new/team", Name: "web2", WebURL: "https://gitlab.com/new/team/web2"}
		if diff := cmp.Diff(want, stateOf(t, f, id)); diff != "" {
			t.Errorf("stored repo mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("same path again is an update", func(t *testing.T) {
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 101, "same/web", "same", "web")
		skipped, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(101, "same/web", "same", "web", "gitlab.com"))
		if err != nil || skipped != "" {
			t.Errorf("UpdateRepoPath(unchanged path) = %q, %v, want it applied", skipped, err)
		}
	})

	t.Run("collision with another repository of the workspace and host changes nothing", func(t *testing.T) {
		keep := gitlabRepo(t, f, f.wsID, "gitlab.com", 110, "taken/web", "taken", "web")
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 111, "mine/web", "mine", "web")
		before := stateOf(t, f, id)
		skipped, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(111, "taken/web", "taken", "web", "gitlab.com"))
		if err != nil || skipped != keep {
			t.Fatalf("UpdateRepoPath(collision) = %q, %v, want skipped for %s, nil", skipped, err, keep)
		}
		if diff := cmp.Diff(before, stateOf(t, f, id)); diff != "" {
			t.Errorf("colliding repo changed (-before +after):\n%s", diff)
		}
		if got := stateOf(t, f, keep).FullName; got != "taken/web" {
			t.Errorf("other repo full_name = %q, want taken/web", got)
		}
	})

	t.Run("the same path on another host or in another workspace is no collision", func(t *testing.T) {
		gitlabRepo(t, f, f.wsID, "other.example", 120, "shared/web", "shared", "web")
		gitlabRepo(t, f, otherWS, "gitlab.com", 121, "shared/web", "shared", "web")
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 122, "mine2/web", "mine2", "web")
		skipped, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(122, "shared/web", "shared", "web", "gitlab.com"))
		if err != nil || skipped != "" {
			t.Errorf("UpdateRepoPath() = %q, %v, want it applied", skipped, err)
		}
	})

	t.Run("collision inside a transaction leaves it usable", func(t *testing.T) {
		holder := gitlabRepo(t, f, f.wsID, "gitlab.com", 130, "tx/taken", "tx", "taken")
		id := gitlabRepo(t, f, f.wsID, "gitlab.com", 131, "tx/mine", "tx", "mine")
		err := postgres.NewTx(f.db).InTx(ctx, func(ctx context.Context) error {
			skipped, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(131, "tx/taken", "tx", "taken", "gitlab.com"))
			if err != nil || skipped != holder {
				t.Errorf("UpdateRepoPath(collision in tx) = %q, %v, want skipped for %s, nil", skipped, err, holder)
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
			if skipped, err := f.store.UpdateRepoPath(ctx, f.wsID, id, project(140, "rb/web9", "rb", "web9", "gitlab.com")); err != nil || skipped != "" {
				t.Errorf("UpdateRepoPath() = %q, %v, want it applied", skipped, err)
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
			skipped, err := f.store.UpdateRepoPath(ctx, tc.ws, tc.repo, tc.p)
			if skipped != "" || !errors.Is(err, domain.ErrRepoNotFound) {
				t.Errorf("UpdateRepoPath(%s) = %q, %v, want \"\", ErrRepoNotFound", tc.name, skipped, err)
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
		if skipped, err := f.store.UpdateRepoPath(ctx, f.wsID, id, p); skipped != "" || err == nil {
			t.Errorf("UpdateRepoPath(no web URL) = %q, %v, want \"\" and an error", skipped, err)
		}
	})
}

func TestFindRepo_webURL(t *testing.T) {
	f := seeded(t)
	id := gitlabRepo(t, f, f.wsID, "gitlab.com", 60, "grp/web", "grp", "web")
	exec(t, f.db, `UPDATE repos SET web_url = 'https://gitlab.com/grp/web-moved' WHERE id = ?`, id)
	got, err := f.store.FindRepo(t.Context(), f.wsID, id)
	if err != nil || got.WebURL != "https://gitlab.com/grp/web-moved" {
		t.Errorf("FindRepo(web_url) = WebURL %q, %v, want the stored address", got.WebURL, err)
	}
	// A GitHub repository has none.
	gh, err := f.store.FindRepo(t.Context(), f.wsID, f.repoID)
	if err != nil || gh.WebURL != "" {
		t.Errorf("FindRepo(GitHub) = WebURL %q, %v, want \"\", nil", gh.WebURL, err)
	}
}

const (
	sha1Head = "0123456789abcdef0123456789abcdef01234567"
	sha2Head = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

type storedDetail struct {
	DiffHead *string
}

func storedDiffHead(t *testing.T, f fixture, prID string) *string {
	t.Helper()
	var s storedDetail
	scan(t, f.db, &s, `SELECT diff_head FROM pull_requests WHERE id = ?`, prID)
	return s.DiffHead
}

// gitlabPull stores one GitLab pull request and returns its id.
func gitlabPull(t *testing.T, f fixture, repoID string, number int) string {
	t.Helper()
	l := domain.Listed{Number: number, Title: "t", Author: "a", Branch: "b", Base: "main", HeadSHA: sha1Head, Status: "open",
		OpenedAt: day(1, 9), UpdatedAt: day(2, 9)}
	if err := f.store.UpsertListed(t.Context(), f.wsID, repoID, []domain.Listed{l}); err != nil {
		t.Fatalf("UpsertListed() error = %v", err)
	}
	var id string
	scan(t, f.db, &id, `SELECT id FROM pull_requests WHERE repo_id = ? AND number = ?`, repoID, number)
	return id
}

// TestReplaceDetail_gitlabDiffHeadAndPreviousPath covers FR35 and AC6 (store part): the detail
// write stores the diff head with the files' previous paths, FindPull and ListFiles return them.
func TestReplaceDetail_gitlabDiffHeadAndPreviousPath(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	repoID := gitlabRepo(t, f, f.wsID, "gitlab.com", 61, "grp/ren", "grp", "ren")
	prID := gitlabPull(t, f, repoID, 5)

	if got, err := f.store.FindPull(ctx, f.wsID, prID); err != nil || got.DiffHead != nil {
		t.Fatalf("FindPull(before detail) DiffHead = %v, %v, want nil", got.DiffHead, err)
	}

	mirror := domain.DetailMirror{
		DiffHead: ptr(sha1Head),
		Files: []domain.File{
			{Path: "new/a.ts", PreviousPath: ptr("old/a.ts"), Patch: ptr("@@ -1 +1 @@")},
			{Path: "keep.go"},
			{Path: "blank.go", PreviousPath: ptr("")},
		},
	}
	if err := f.store.ReplaceDetail(ctx, prID, mirror); err != nil {
		t.Fatalf("ReplaceDetail() error = %v", err)
	}
	pr, err := f.store.FindPull(ctx, f.wsID, prID)
	if err != nil || pr.DiffHead == nil || *pr.DiffHead != sha1Head {
		t.Fatalf("FindPull() DiffHead = %v, %v, want %s", pr.DiffHead, err, sha1Head)
	}
	if got := pr.EffectiveDiffHead(); got != sha1Head {
		t.Errorf("EffectiveDiffHead() = %q, want %q", got, sha1Head)
	}
	files, err := f.store.ListFiles(ctx, prID)
	if err != nil {
		t.Fatalf("ListFiles() error = %v", err)
	}
	want := []domain.File{
		{Path: "blank.go"},
		{Path: "keep.go"},
		{Path: "new/a.ts", PreviousPath: ptr("old/a.ts"), Patch: ptr("@@ -1 +1 @@")},
	}
	if diff := cmp.Diff(want, files); diff != "" {
		t.Errorf("ListFiles() mismatch (-want +got):\n%s", diff)
	}
	if got := files[2].PositionOldPath(); got != "old/a.ts" {
		t.Errorf("PositionOldPath() of the renamed file = %q, want old/a.ts", got)
	}

	// A later fetch of the same file without a rename clears its previous path; a 64-hex head is
	// stored as is.
	next := domain.DetailMirror{DiffHead: ptr(sha2Head), Files: []domain.File{{Path: "new/a.ts"}}}
	if err := f.store.ReplaceDetail(ctx, prID, next); err != nil {
		t.Fatalf("ReplaceDetail(second) error = %v", err)
	}
	files, _ = f.store.ListFiles(ctx, prID)
	if diff := cmp.Diff([]domain.File{{Path: "new/a.ts"}}, files); diff != "" {
		t.Errorf("ListFiles() after refetch mismatch (-want +got):\n%s", diff)
	}
	if got := storedDiffHead(t, f, prID); got == nil || *got != sha2Head {
		t.Errorf("stored diff_head = %v, want the 64-hex head", got)
	}
}

// TestReplaceDetail_githubLeavesDiffHeadEmpty: a GitHub detail write (nil head, no previous
// paths) leaves both columns NULL and clears a stale diff head.
func TestReplaceDetail_githubLeavesDiffHeadEmpty(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	mirror := domain.DetailMirror{Files: []domain.File{{Path: "a.go", Additions: 1}}}
	if err := f.store.ReplaceDetail(ctx, f.prID, mirror); err != nil {
		t.Fatalf("ReplaceDetail() error = %v", err)
	}
	if got := storedDiffHead(t, f, f.prID); got != nil {
		t.Errorf("stored diff_head = %q, want NULL", *got)
	}
	var n int
	scan(t, f.db, &n, `SELECT count(*) FROM pr_files WHERE pr_id = ? AND previous_path IS NOT NULL`, f.prID)
	if n != 0 {
		t.Errorf("files with previous_path = %d, want 0", n)
	}
	pr, _ := f.store.FindPull(ctx, f.wsID, f.prID)
	if pr.DiffHead != nil {
		t.Errorf("FindPull() DiffHead = %q, want nil", *pr.DiffHead)
	}
	if got := pr.EffectiveDiffHead(); got != pr.HeadSHA {
		t.Errorf("EffectiveDiffHead() = %q, want the head SHA %q", got, pr.HeadSHA)
	}
}

// TestReplaceDetail_refillsDiffHeadAfterDownUp (AC30): the down migration drops the values, so
// the head reads empty (a GitLab post then answers diff_changed); the next detail fetch refills it.
func TestReplaceDetail_refillsDiffHeadAfterDownUp(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	repoID := gitlabRepo(t, f, f.wsID, "gitlab.com", 62, "grp/refill", "grp", "refill")
	prID := gitlabPull(t, f, repoID, 6)
	mirror := domain.DetailMirror{DiffHead: ptr(sha1Head), Files: []domain.File{{Path: "n.go", PreviousPath: ptr("o.go")}}}
	if err := f.store.ReplaceDetail(ctx, prID, mirror); err != nil {
		t.Fatalf("ReplaceDetail() error = %v", err)
	}

	// What down then up leaves behind: both columns empty.
	exec(t, f.db, `UPDATE pull_requests SET diff_head = NULL WHERE id = ?`, prID)
	exec(t, f.db, `UPDATE pr_files SET previous_path = NULL WHERE pr_id = ?`, prID)
	pr, _ := f.store.FindPull(ctx, f.wsID, prID)
	if pr.DiffHead != nil || pr.EffectiveDiffHead() != pr.HeadSHA {
		t.Fatalf("after down/up DiffHead = %v, want nil (falls back to the head SHA)", pr.DiffHead)
	}

	if err := f.store.ReplaceDetail(ctx, prID, mirror); err != nil {
		t.Fatalf("ReplaceDetail(refill) error = %v", err)
	}
	if got := storedDiffHead(t, f, prID); got == nil || *got != sha1Head {
		t.Errorf("diff_head after refill = %v, want %s", got, sha1Head)
	}
	files, _ := f.store.ListFiles(ctx, prID)
	if len(files) != 1 || files[0].PreviousPath == nil || *files[0].PreviousPath != "o.go" {
		t.Errorf("files after refill = %+v, want previous path o.go", files)
	}
}

// TestReplaceDetail_neverViolatesDiffHeadCheck: a value pull_requests_diff_head_chk would refuse
// is stored as NULL instead of failing the detail write.
func TestReplaceDetail_neverViolatesDiffHeadCheck(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	repoID := gitlabRepo(t, f, f.wsID, "gitlab.com", 63, "grp/chk", "grp", "chk")
	prID := gitlabPull(t, f, repoID, 7)

	for _, tc := range []struct{ name, head string }{
		{"short", "abc123"},
		{"upper case", "0123456789ABCDEF0123456789ABCDEF01234567"},
		{"41 hex", sha1Head + "0"},
		{"non hex", "g123456789abcdef0123456789abcdef01234567"},
		{"empty", ""},
		{"trailing newline", sha1Head + "\n"},
	} {
		if err := f.store.ReplaceDetail(ctx, prID, domain.DetailMirror{DiffHead: ptr(tc.head), Files: []domain.File{{Path: "x.go"}}}); err != nil {
			t.Errorf("ReplaceDetail(%s head) error = %v, want nil", tc.name, err)
			continue
		}
		if got := storedDiffHead(t, f, prID); got != nil {
			t.Errorf("diff_head after %s head = %q, want NULL", tc.name, *got)
		}
	}
}

// TestReplaceDetail_diffHeadInTransaction [K4]: the head and the previous paths are written in
// the caller's transaction and roll back with it.
func TestReplaceDetail_diffHeadInTransaction(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	repoID := gitlabRepo(t, f, f.wsID, "gitlab.com", 64, "grp/tx", "grp", "tx")
	prID := gitlabPull(t, f, repoID, 8)
	boom := errors.New("rollback")
	err := postgres.NewTx(f.db).InTx(ctx, func(ctx context.Context) error {
		mirror := domain.DetailMirror{DiffHead: ptr(sha1Head), Files: []domain.File{{Path: "n.go", PreviousPath: ptr("o.go")}}}
		if err := f.store.ReplaceDetail(ctx, prID, mirror); err != nil {
			t.Errorf("ReplaceDetail() in tx error = %v", err)
		}
		var inTx *string
		scan(t, postgres.Conn(ctx, f.db), &inTx, `SELECT diff_head FROM pull_requests WHERE id = ?`, prID)
		if inTx == nil || *inTx != sha1Head {
			t.Errorf("diff_head inside the transaction = %v, want %s", inTx, sha1Head)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx error = %v, want the rollback error", err)
	}
	if got := storedDiffHead(t, f, prID); got != nil {
		t.Errorf("diff_head after rollback = %q, want NULL", *got)
	}
	if files, _ := f.store.ListFiles(ctx, prID); len(files) != 0 {
		t.Errorf("files after rollback = %d, want 0", len(files))
	}
}
