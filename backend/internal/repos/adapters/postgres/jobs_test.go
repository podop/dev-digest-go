package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

// hermeticGit isolates git from the developer's configuration. Tests using it must not be
// parallel (t.Setenv).
func hermeticGit(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	for _, k := range []string{"GIT_CONFIG_COUNT", "GIT_TRACE", "GIT_DIR", "GIT_WORK_TREE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// origin serves bare repositories as file://<base>/<owner>/<name>.git, like github.com does.
type origin struct {
	base string
	work string
}

func newOrigin(t *testing.T, owner, name string) *origin {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(t.TempDir(), "work")
	bare := filepath.Join(base, owner, name+".git")
	for _, d := range []string{work, filepath.Dir(bare)} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	git(t, work, "init", "-q", "-b", "main")
	o := &origin{base: base, work: work}
	o.write(t, "a.txt", "one")
	git(t, base, "clone", "-q", "--bare", work, bare)
	git(t, work, "remote", "add", "origin", bare)
	return o
}

func (o *origin) remoteBase() string { return "file://" + o.base }

// commit writes a file, commits it and pushes main to the bare repository.
func (o *origin) commit(t *testing.T, file, content string) string {
	t.Helper()
	sha := o.write(t, file, content)
	git(t, o.work, "push", "-q", "origin", "main")
	return sha
}

func (o *origin) write(t *testing.T, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(o.work, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, o.work, "add", file)
	git(t, o.work, "commit", "-q", "-m", "add "+file)
	return git(t, o.work, "rev-parse", "HEAD")
}

func jobsFor(t *testing.T, f fixture, root, remote string, now func() time.Time) *app.Jobs {
	t.Helper()
	client, err := gitcli.New(gitcli.Options{CloneRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	return app.NewJobs(f.store, client, app.JobsOptions{RemoteBase: remote, Now: now})
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJobs_CloneAndRefresh(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	src := newOrigin(t, "o", "r")
	root := t.TempDir()
	clock := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	jobs := jobsFor(t, f, root, src.remoteBase(), func() time.Time { return clock })
	repo, _ := add(t, f, f.ws, "o", "r")
	dest := filepath.Join(root, "o", "r")

	// Clone: shallow, path and last_polled_at recorded.
	if err := jobs.Clone(t.Context(), raw(t, app.ClonePayload{RepoID: repo.ID, Owner: "o", Name: "r"})); err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	got, err := f.store.GetByID(t.Context(), repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClonePath == nil || *got.ClonePath != dest {
		t.Errorf("clone_path = %v, want %s", got.ClonePath, dest)
	}
	if got.LastPolledAt == nil || !got.LastPolledAt.Equal(clock) {
		t.Errorf("last_polled_at = %v, want %v", got.LastPolledAt, clock)
	}
	if shallow := git(t, dest, "rev-parse", "--is-shallow-repository"); shallow != "true" {
		t.Errorf("is-shallow-repository = %q, want true (depth 1)", shallow)
	}

	// Refresh: the clone's HEAD moves to the new commit, last_polled_at advances.
	newSHA := src.commit(t, "b.txt", "two")
	clock = clock.Add(time.Hour)
	if err := jobs.Refresh(t.Context(), raw(t, app.RefreshPayload{RepoID: repo.ID})); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if head := git(t, dest, "rev-parse", "HEAD"); head != newSHA {
		t.Errorf("HEAD after refresh = %s, want %s", head, newSHA)
	}
	got, _ = f.store.GetByID(t.Context(), repo.ID)
	if got.LastPolledAt == nil || !got.LastPolledAt.Equal(clock) {
		t.Errorf("last_polled_at after refresh = %v, want %v", got.LastPolledAt, clock)
	}
}

func TestJobs_Clone_ReplacesPartialDirectory(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	src := newOrigin(t, "o", "r")
	root := t.TempDir()
	repo, _ := add(t, f, f.ws, "o", "r")
	dest := filepath.Join(root, "o", "r")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	jobs := jobsFor(t, f, root, src.remoteBase(), nil)
	if err := jobs.Clone(t.Context(), raw(t, app.ClonePayload{RepoID: repo.ID})); err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Errorf("partial directory not replaced by a clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "leftover")); !os.IsNotExist(err) {
		t.Errorf("leftover file still exists (err %v), want it removed", err)
	}
}

func TestJobs_Clone_MissingSourceFails(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	repo, _ := add(t, f, f.ws, "o", "nothere")
	jobs := jobsFor(t, f, t.TempDir(), "file://"+t.TempDir(), nil)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := jobs.Clone(ctx, raw(t, app.ClonePayload{RepoID: repo.ID})); err == nil {
		t.Fatal("Clone(missing source) error = nil, want an error")
	}
	got, _ := f.store.GetByID(t.Context(), repo.ID)
	if got.ClonePath != nil || got.LastPolledAt != nil {
		t.Errorf("repo after failed clone = %+v, want unchanged", got)
	}
}

func TestJobs_Clone_RepoDeletedMeanwhile(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	src := newOrigin(t, "o", "r")
	root := t.TempDir()
	repo, _ := add(t, f, f.ws, "o", "r")
	if err := f.store.Delete(t.Context(), f.ws, repo.ID); err != nil {
		t.Fatal(err)
	}

	jobs := jobsFor(t, f, root, src.remoteBase(), nil)
	if err := jobs.Clone(t.Context(), raw(t, app.ClonePayload{RepoID: repo.ID})); err != nil {
		t.Fatalf("Clone(deleted repo) error = %v, want nil", err)
	}
	if _, err := f.store.GetByID(t.Context(), repo.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetByID() error = %v, want ErrNotFound (no row recreated)", err)
	}
}
