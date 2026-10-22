package gitcli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/gitcli"
)

const secretToken = "ghp_secret_x"

// hermeticGit isolates git from the developer's configuration and from any
// inherited GIT_CONFIG_* / GIT_TRACE variables. Tests using it must not be parallel.
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

// remote is a bare repository plus a work tree used to push commits to it.
type remote struct {
	t    *testing.T
	work string
	url  string
}

func newRemote(t *testing.T) *remote {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "work")
	bare := filepath.Join(base, "origin.git")
	if err := os.Mkdir(work, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, work, "init", "-q", "-b", "main")
	r := &remote{t: t, work: work, url: "file://" + bare}
	r.write("a.txt", "one")
	git(t, base, "clone", "-q", "--bare", work, bare)
	git(t, work, "remote", "add", "origin", bare)
	return r
}

// write creates a file and commits it locally; returns the new sha.
func (r *remote) write(file, content string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.work, file), []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
	git(r.t, r.work, "add", file)
	git(r.t, r.work, "commit", "-q", "-m", "add "+file)
	return git(r.t, r.work, "rev-parse", "HEAD")
}

// commit writes a file, commits it and pushes main to the bare remote.
func (r *remote) commit(file, content string) string {
	r.t.Helper()
	sha := r.write(file, content)
	git(r.t, r.work, "push", "-q", "origin", "main")
	return sha
}

// wrapper writes a git wrapper script that logs argv and GIT_* env to a file
// and then runs the real git. It returns the script path and the log path.
func wrapper(t *testing.T, body string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "git-wrapper"), filepath.Join(dir, "calls.log")
	script := fmt.Sprintf(`#!/bin/sh
{ printf 'ARGV:'; for a in "$@"; do printf ' [%%s]' "$a"; done; printf '\n'; env | grep -E '^(GIT_|GCM_)' | sort; echo END; } >> %q
%s
`, log, body)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tokenOf(tok string) gitcli.TokenProvider {
	return func(context.Context) (string, error) { return tok, nil }
}

func TestClone_FreshShallow(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	root := t.TempDir()
	c := newClient(t, gitcli.Options{CloneRoot: root})

	got, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1})
	if err != nil {
		t.Fatalf("Clone() error: %v", err)
	}
	want := filepath.Join(root, "o", "r")
	if got != want {
		t.Errorf("Clone() = %q, want %q", got, want)
	}
	if b := readFile(t, filepath.Join(got, "a.txt")); b != "one" {
		t.Errorf("a.txt = %q, want %q", b, "one")
	}
	if shallow := git(t, got, "rev-parse", "--is-shallow-repository"); shallow != "true" {
		t.Errorf("is-shallow-repository = %q, want true", shallow)
	}
	head, err := c.Head(t.Context(), "o", "r")
	if err != nil {
		t.Fatalf("Head() error: %v", err)
	}
	if want := git(t, r.work, "rev-parse", "HEAD"); head != want {
		t.Errorf("Head() = %s, want %s", head, want)
	}
}

func TestClone_ExistingFetchesAndKeepsFiles(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir()})
	dest, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1})
	if err != nil {
		t.Fatalf("first Clone() error: %v", err)
	}
	marker := filepath.Join(dest, "keep.txt")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.commit("b.txt", "two")

	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("second Clone() error: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("existing clone lost its files: %v", err)
	}
}

func TestClone_ReplacesPartialDirectory(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	root := t.TempDir()
	partial := filepath.Join(root, "o", "r")
	if err := os.MkdirAll(partial, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "junk"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, gitcli.Options{CloneRoot: root})

	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(partial, "junk")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat junk = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(partial, ".git")); err != nil {
		t.Errorf("clone has no .git: %v", err)
	}
}

func TestSync_AdvancesToNewCommit(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir()})
	dest, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1})
	if err != nil {
		t.Fatalf("Clone() error: %v", err)
	}
	branch, err := c.DefaultBranch(t.Context(), "o", "r")
	if err != nil || branch != "main" {
		t.Fatalf("DefaultBranch() = %q, %v, want main", branch, err)
	}
	newSha := r.commit("b.txt", "two")

	head, err := c.Sync(t.Context(), "o", "r", branch)
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if head != newSha {
		t.Errorf("Sync() = %s, want %s", head, newSha)
	}
	if got := git(t, dest, "rev-parse", "HEAD"); got != newSha {
		t.Errorf("clone HEAD = %s, want %s", got, newSha)
	}
	if b := readFile(t, filepath.Join(dest, "b.txt")); b != "two" {
		t.Errorf("b.txt = %q, want two", b)
	}
}

func TestSync_RejectsBadBranch(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir()})
	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"", "--upload-pack=x", "-f", "a..b", "a b", "main;rm", "x/", "/x", "a//b", "x.lock"} {
		if _, err := c.Sync(t.Context(), "o", "r", b); !errors.Is(err, gitcli.ErrInvalidRef) {
			t.Errorf("Sync(branch=%q) error = %v, want ErrInvalidRef", b, err)
		}
	}
}

func TestClone_TokenOnlyInEnvAnonymousOtherwise(t *testing.T) {
	hermeticGit(t)
	t.Setenv("GIT_CONFIG_COUNT", "1") // inherited config injection must not reach git
	t.Setenv("GIT_CONFIG_KEY_0", "core.pager")
	t.Setenv("GIT_CONFIG_VALUE_0", "evil")
	r := newRemote(t)
	bin, log := wrapper(t, `exec git "$@"`)
	header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+secretToken))

	t.Run("with token", func(t *testing.T) {
		root := t.TempDir()
		c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: root, Token: tokenOf(secretToken)})
		dest, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1})
		if err != nil {
			t.Fatalf("Clone() error: %v", err)
		}
		r.commit("b.txt", "two")
		if _, err := c.Sync(t.Context(), "o", "r", "main"); err != nil {
			t.Fatalf("Sync() error: %v", err)
		}

		calls := readFile(t, log)
		for _, line := range strings.Split(calls, "\n") {
			if strings.HasPrefix(line, "ARGV:") && (strings.Contains(line, secretToken) || strings.Contains(line, "Authorization")) {
				t.Errorf("argv leaks credentials: %s", line)
			}
		}
		for _, want := range []string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader",
			"GIT_CONFIG_VALUE_0=" + header,
			"GIT_TERMINAL_PROMPT=0",
			"GCM_INTERACTIVE=never",
			"[core.hooksPath=/dev/null]",
		} {
			if !strings.Contains(calls, want) {
				t.Errorf("git invocations lack %q:\n%s", want, calls)
			}
		}
		if strings.Contains(calls, "GIT_CONFIG_KEY_1") || strings.Contains(calls, "core.pager") || strings.Contains(calls, "evil") {
			t.Errorf("inherited GIT_CONFIG_* reached git:\n%s", calls)
		}
		cfg := readFile(t, filepath.Join(dest, ".git", "config"))
		remotes := git(t, dest, "remote", "-v")
		for name, body := range map[string]string{".git/config": cfg, "git remote -v": remotes} {
			if strings.Contains(body, secretToken) || strings.Contains(body, header) || strings.Contains(body, "Basic") {
				t.Errorf("%s contains credentials:\n%s", name, body)
			}
		}
	})

	t.Run("without token", func(t *testing.T) {
		if err := os.Remove(log); err != nil {
			t.Fatal(err)
		}
		c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir(), Token: tokenOf("")})
		if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); err != nil {
			t.Fatalf("Clone() error: %v", err)
		}
		calls := readFile(t, log)
		for _, bad := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY", "GIT_CONFIG_VALUE", "Authorization"} {
			if strings.Contains(calls, bad) {
				t.Errorf("anonymous run contains %q:\n%s", bad, calls)
			}
		}
		if !strings.Contains(calls, "GIT_TERMINAL_PROMPT=0") {
			t.Errorf("anonymous run lacks GIT_TERMINAL_PROMPT=0:\n%s", calls)
		}
	})
}

func TestClone_ScrubsLegacyCredentialedOrigin(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir()})
	dest, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	git(t, dest, "remote", "set-url", "origin", "https://x-access-token:"+secretToken+"@github.com/o/r.git")

	// The fetch itself fails (no network), but origin must already be clean.
	_, _ = c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1})
	if got := git(t, dest, "remote", "get-url", "origin"); strings.Contains(got, secretToken) {
		t.Errorf("origin url = %q, want credentials stripped", got)
	}
}

func TestErrors_NeverContainTokenOrArgv(t *testing.T) {
	hermeticGit(t)
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + secretToken))
	bin, _ := wrapper(t, fmt.Sprintf(
		`echo "fatal: unable to access 'https://x-access-token:%s@github.com/o/r.git/': token %s header Authorization: Basic %s" >&2; exit 128`,
		secretToken, secretToken, b64))
	c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir(), Token: tokenOf(secretToken)})

	_, err := c.Clone(t.Context(), "o", "r", "https://github.com/o/r.git", gitcli.CloneOptions{Depth: 1})
	if err == nil {
		t.Fatal("Clone() error = nil, want failure")
	}
	var ge *gitcli.Error
	if !errors.As(err, &ge) || ge.Op != "clone" {
		t.Fatalf("Clone() error = %#v, want *gitcli.Error with Op clone", err)
	}
	for _, leak := range []string{secretToken, b64, "x-access-token", "--depth", "core.hooksPath"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q: %v", leak, err)
		}
	}
	if !strings.Contains(err.Error(), "fatal: unable to access") {
		t.Errorf("error = %v, want git's stderr kept", err)
	}
}

func TestErrors_TokenProviderFailure(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	boom := errors.New("secrets unreadable")
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir(), Token: func(context.Context) (string, error) { return "", boom }})
	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{}); !errors.Is(err, boom) {
		t.Errorf("Clone() error = %v, want wrapping %v", err, boom)
	}
}

func TestClone_PathGuard(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	base := t.TempDir()
	root := filepath.Join(base, "clones")
	outside := filepath.Join(base, "outside")
	victim := filepath.Join(outside, "r", "precious.txt")
	if err := os.MkdirAll(filepath.Dir(victim), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sym")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	c := newClient(t, gitcli.Options{CloneRoot: root})

	tests := []struct {
		name        string
		owner, repo string
		wantErr     error
	}{
		{name: "dot dot owner", owner: "..", repo: "outside", wantErr: gitcli.ErrInvalidSegment},
		{name: "dot dot name", owner: "o", repo: "..", wantErr: gitcli.ErrInvalidSegment},
		{name: "traversal name", owner: "o", repo: "../../outside", wantErr: gitcli.ErrInvalidSegment},
		{name: "symlinked owner dir", owner: "sym", repo: "r", wantErr: gitcli.ErrUnsafePath},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Clone(t.Context(), tc.owner, tc.repo, r.url, gitcli.CloneOptions{Depth: 1}); !errors.Is(err, tc.wantErr) {
				t.Errorf("Clone(%q, %q) error = %v, want %v", tc.owner, tc.repo, err, tc.wantErr)
			}
			if _, err := c.Sync(t.Context(), tc.owner, tc.repo, "main"); !errors.Is(err, tc.wantErr) {
				t.Errorf("Sync(%q, %q) error = %v, want %v", tc.owner, tc.repo, err, tc.wantErr)
			}
			if b, err := os.ReadFile(victim); err != nil || string(b) != "keep" {
				t.Errorf("file outside clone root changed: %q, %v", b, err)
			}
			if _, err := os.Stat(filepath.Join(base, "o")); err == nil {
				t.Error("directory created outside the clone root")
			}
			if _, err := os.Stat(filepath.Join(root, "o")); err == nil {
				t.Error("directory created for a rejected repo")
			}
		})
	}
}

func TestClone_SymlinkedDestinationIsRejected(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	base := t.TempDir()
	root := filepath.Join(base, "clones")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "o"), outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "precious.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "o", "r")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	c := newClient(t, gitcli.Options{CloneRoot: root})
	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); !errors.Is(err, gitcli.ErrUnsafePath) {
		t.Errorf("Clone() error = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious.txt")); err != nil {
		t.Errorf("file behind the symlink was touched: %v", err)
	}
}

func TestClone_ContextCancelKillsGit(t *testing.T) {
	hermeticGit(t)
	bin, _ := wrapper(t, `exec sleep 30`)
	c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir()})
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Clone(ctx, "o", "r", "file:///nonexistent", gitcli.CloneOptions{Depth: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Clone() error = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("Clone() took %v after cancel, want prompt return", d)
	}
}

func TestClone_RejectsBadInput(t *testing.T) {
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir()})
	if _, err := c.Clone(t.Context(), "o", "r", "", gitcli.CloneOptions{}); err == nil {
		t.Error("Clone(empty url) error = nil, want error")
	}
	if _, err := c.Clone(t.Context(), "o", "r", "file:///x", gitcli.CloneOptions{Depth: -1}); err == nil {
		t.Error("Clone(depth -1) error = nil, want error")
	}
}

func TestClone_URLCannotBeAnOption(t *testing.T) {
	hermeticGit(t)
	bin, log := wrapper(t, `exit 1`)
	c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir()})
	_, _ = c.Clone(t.Context(), "o", "r", "--upload-pack=touch /tmp/pwned", gitcli.CloneOptions{})
	argv := strings.SplitN(readFile(t, log), "\n", 2)[0]
	if !strings.Contains(argv, "[--] [--upload-pack=touch /tmp/pwned]") {
		t.Errorf("clone argv = %q, want url after `--`", argv)
	}
}

func TestDiffNames_ListsChangedPathsRaw(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	r.commit("keep.txt", "keep")
	r.commit("old name.txt", "same content, long enough to be detected as a rename of the old file")
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir()})
	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{}); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}
	base, err := c.Head(t.Context(), "o", "r")
	if err != nil {
		t.Fatalf("Head() error: %v", err)
	}

	// modify, add (odd names), delete, rename: every case git could quote or fold.
	if err := os.WriteFile(filepath.Join(r.work, "a.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dir/ü ni.txt", "tab\there.txt", "line\nbreak.txt"} {
		p := filepath.Join(r.work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, r.work, "rm", "-q", "keep.txt")
	git(t, r.work, "mv", "old name.txt", "new name.txt")
	git(t, r.work, "add", "-A")
	git(t, r.work, "commit", "-q", "-m", "mixed")
	git(t, r.work, "push", "-q", "origin", "main")
	if _, err := c.Sync(t.Context(), "o", "r", "main"); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	head, err := c.Head(t.Context(), "o", "r")
	if err != nil {
		t.Fatalf("Head() error: %v", err)
	}

	got, err := c.DiffNames(t.Context(), "o", "r", base, head)
	if err != nil {
		t.Fatalf("DiffNames() error: %v", err)
	}
	slices.Sort(got)
	want := []string{"a.txt", "dir/ü ni.txt", "keep.txt", "line\nbreak.txt", "new name.txt", "old name.txt", "tab\there.txt"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DiffNames(%.7s, %.7s) mismatch (-want +got):\n%s", base, head, diff)
	}

	same, err := c.DiffNames(t.Context(), "o", "r", head, head)
	if err != nil || len(same) != 0 {
		t.Errorf("DiffNames(head, head) = %v, %v, want empty, nil", same, err)
	}
}

func TestDiffNames_RejectsNonHexIDs(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	bin, log := wrapper(t, `exec git "$@"`)
	c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir()})
	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	head := git(t, r.work, "rev-parse", "HEAD")
	before := readFile(t, log)
	for _, bad := range []string{"", "abc", "--output=x", "-p", "HEAD", "main..main", "zzzz", head + "g", strings.Repeat("a", 65)} {
		if _, err := c.DiffNames(t.Context(), "o", "r", bad, head); !errors.Is(err, gitcli.ErrInvalidSHA) {
			t.Errorf("DiffNames(base=%q) error = %v, want ErrInvalidSHA", bad, err)
		}
		if _, err := c.DiffNames(t.Context(), "o", "r", head, bad); !errors.Is(err, gitcli.ErrInvalidSHA) {
			t.Errorf("DiffNames(head=%q) error = %v, want ErrInvalidSHA", bad, err)
		}
	}
	if after := readFile(t, log); after != before {
		t.Errorf("git ran for an invalid id:\n%s", strings.TrimPrefix(after, before))
	}
}

func TestDiffNames_ArgvAndUnknownClone(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	bin, log := wrapper(t, `exec git "$@"`)
	c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir()})
	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	head := git(t, r.work, "rev-parse", "HEAD")
	if _, err := c.DiffNames(t.Context(), "o", "r", head, head); err != nil {
		t.Fatalf("DiffNames() error: %v", err)
	}
	wantArgv := "[diff] [--name-only] [--no-renames] [-z] [" + head + "] [" + head + "] [--]"
	if got := readFile(t, log); !strings.Contains(got, wantArgv+"\n") {
		t.Errorf("git argv missing %q in:\n%s", wantArgv, got)
	}
	if _, err := c.DiffNames(t.Context(), "o", "missing", head, head); err == nil {
		t.Error("DiffNames(unknown clone) error = nil, want an error")
	}
}

func TestDiffNames_MissingBaseInShallowCloneIsAnError(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	c := newClient(t, gitcli.Options{CloneRoot: t.TempDir()})
	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	head, err := c.Head(t.Context(), "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	names, err := c.DiffNames(t.Context(), "o", "r", strings.Repeat("1", 40), head)
	var ge *gitcli.Error
	if !errors.As(err, &ge) || names != nil {
		t.Errorf("DiffNames(missing base) = %v, %v, want nil, *gitcli.Error", names, err)
	}
}

func TestDiffNames_OutputAboveTheCapIsRefused(t *testing.T) {
	hermeticGit(t)
	r := newRemote(t)
	body := `if [ "$(printf %s "$*" | grep -c ' diff ')" = 1 ]; then head -c 1048577 /dev/zero | tr '\0' 'a'; exit 0; fi
exec git "$@"`
	bin, _ := wrapper(t, body)
	c := newClient(t, gitcli.Options{Binary: bin, CloneRoot: t.TempDir()})
	if _, err := c.Clone(t.Context(), "o", "r", r.url, gitcli.CloneOptions{Depth: 1}); err != nil {
		t.Fatal(err)
	}
	head := git(t, r.work, "rev-parse", "HEAD")
	names, err := c.DiffNames(t.Context(), "o", "r", head, head)
	if !errors.Is(err, gitcli.ErrOutputTooLarge) || names != nil {
		t.Errorf("DiffNames(output of 1 MiB + 1) = %d names, %v, want nil, ErrOutputTooLarge", len(names), err)
	}
}
