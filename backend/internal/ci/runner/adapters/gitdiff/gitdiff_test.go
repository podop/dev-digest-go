package gitdiff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/ci/runner/domain"
)

// fixture is a real repository built by the git program in a temporary directory.
type fixture struct {
	dir        string
	base, head string
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main", "-c", "protocol.file.allow=always",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newFixture builds base and head commits: head modifies a file, adds one, renames one, adds a
// binary file and touches .devdigest/ and .gitlab/devdigest/.
func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git program not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, dir, "mod.txt", "one\ntwo\nthree\n")
	writeFile(t, dir, "old_name.txt", "rename me\nline 2\nline 3\nline 4\nline 5\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "base")
	base := runGit(t, dir, "rev-parse", "HEAD")

	writeFile(t, dir, "mod.txt", "one\nTWO\nthree\n")
	writeFile(t, dir, "added.txt", "new file\n")
	runGit(t, dir, "mv", "old_name.txt", "new_name.txt")
	writeFile(t, dir, "bin.dat", "\x00\x01\x02binary\x00payload")
	writeFile(t, dir, ".devdigest/runner.txt", "generated\n")
	writeFile(t, dir, ".gitlab/devdigest/job.yml", "job: {}\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "head")
	head := runGit(t, dir, "rev-parse", "HEAD")
	return fixture{dir: dir, base: base, head: head}
}

func TestDiff_Sections(t *testing.T) {
	f := newFixture(t)
	got, err := New(f.dir).Diff(t.Context(), f.base, f.head)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	var headers []string
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "diff --git") {
			headers = append(headers, l)
		}
	}
	want := []string{
		"diff --git a/.devdigest/runner.txt b/.devdigest/runner.txt",
		"diff --git a/.gitlab/devdigest/job.yml b/.gitlab/devdigest/job.yml",
		"diff --git a/added.txt b/added.txt",
		"diff --git a/bin.dat b/bin.dat",
		"diff --git a/mod.txt b/mod.txt",
		"diff --git a/old_name.txt b/new_name.txt",
	}
	if strings.Join(headers, "\n") != strings.Join(want, "\n") {
		t.Errorf("Diff() headers =\n%s\nwant\n%s", strings.Join(headers, "\n"), strings.Join(want, "\n"))
	}
	for _, sub := range []string{
		"similarity index 100%\nrename from old_name.txt\nrename to new_name.txt\n",
		"Binary files /dev/null and b/bin.dat differ\n",
		"-two\n+TWO\n",
		"+new file\n",
		"new file mode 100644\n",
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("Diff() lacks %q in:\n%s", sub, got)
		}
	}
	if strings.Contains(got, "payload") || strings.Contains(got, "\x00") || strings.Contains(got, "\x1b[") {
		t.Errorf("Diff() holds binary content or colour codes:\n%q", got)
	}
}

func TestDiff_SameCommitIsEmpty(t *testing.T) {
	f := newFixture(t)
	got, err := New(f.dir).Diff(t.Context(), f.head, f.head)
	if err != nil || got != "" {
		t.Errorf("Diff(head, head) = %q, %v, want empty, nil", got, err)
	}
}

// TestDiff_RepositoryConfigCannotChangeFormatOrRunPrograms covers the repository's own configuration
// and .gitattributes: no diff driver, text conversion, hook-like setting or prefix option applies.
func TestDiff_RepositoryConfigCannotChangeFormatOrRunPrograms(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "prog.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, f.dir, ".gitattributes", "*.txt diff=evil\n")
	runGit(t, f.dir, "config", "diff.evil.command", script)
	runGit(t, f.dir, "config", "diff.evil.textconv", script)
	runGit(t, f.dir, "config", "diff.external", script)
	runGit(t, f.dir, "config", "core.fsmonitor", script)
	runGit(t, f.dir, "config", "core.pager", script)
	runGit(t, f.dir, "config", "diff.noprefix", "true")
	runGit(t, f.dir, "config", "diff.mnemonicPrefix", "true")
	runGit(t, f.dir, "config", "color.ui", "always")
	runGit(t, f.dir, "config", "color.diff", "always")

	got, err := New(f.dir).Diff(t.Context(), f.base, f.head)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("a configured program ran")
	}
	if !strings.Contains(got, "diff --git a/mod.txt b/mod.txt\n") || strings.Contains(got, "\x1b[") {
		t.Errorf("Diff() ignored the fixed format:\n%q", got)
	}
}

// TestDiff_EnvironmentIsNotInherited: GIT_EXTERNAL_DIFF, a global configuration and GIT_DIR in the
// runner's environment do not reach git.
func TestDiff_EnvironmentIsNotInherited(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "prog.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	global := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(global, []byte("[diff]\n\texternal = "+script+"\n\tnoprefix = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_EXTERNAL_DIFF", script)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_DIR", filepath.Join(home, "nowhere"))

	got, err := New(f.dir).Diff(t.Context(), f.base, f.head)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("an external diff program ran")
	}
	if !strings.Contains(got, "diff --git a/mod.txt b/mod.txt\n") {
		t.Errorf("Diff() did not use the fixed format:\n%q", got)
	}
}

func TestDiff_ShallowCloneMissingCommit(t *testing.T) {
	f := newFixture(t)
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, t.TempDir(), "clone", "-q", "--depth=1", "file://"+f.dir, clone)

	_, err := New(clone).Diff(t.Context(), f.base, f.head)
	var miss *CommitMissingError
	if !errors.As(err, &miss) {
		t.Fatalf("Diff(missing base) error = %v, want *CommitMissingError", err)
	}
	if miss.SHA != f.base || miss.Role != RoleBase {
		t.Errorf("CommitMissingError = %+v, want base %s", miss, f.base)
	}
	wantMsg := "Base commit " + f.base + " is not in the job's clone; keep GIT_DEPTH: \"0\" in the job"
	if err.Error() != wantMsg {
		t.Errorf("Error() = %q, want %q", err.Error(), wantMsg)
	}
	if miss.CommitMissing() != f.base {
		t.Errorf("CommitMissing() = %q, want %q", miss.CommitMissing(), f.base)
	}

	// Head known only in the full repository, base present: the head is reported.
	absent := strings.Repeat("ab", 20)
	_, err = New(f.dir).Diff(t.Context(), f.base, absent)
	if !errors.As(err, &miss) || miss.Role != RoleHead || miss.SHA != absent {
		t.Errorf("Diff(missing head) error = %v, want head %s missing", err, absent)
	}

	// With the head alone in the shallow clone, the head diff against itself works.
	if got, err := New(clone).Diff(t.Context(), f.head, f.head); err != nil || got != "" {
		t.Errorf("Diff(head, head) in the shallow clone = %q, %v", got, err)
	}
}

func TestDiff_RejectsMalformedCommitsWithoutRunningGit(t *testing.T) {
	f := newFixture(t)
	outFile := filepath.Join(t.TempDir(), "out")
	valid := f.head
	tests := []struct{ name, base, head string }{
		{"empty base", "", valid},
		{"option as base", "--output=" + outFile, valid},
		{"option as head", valid, "--output=" + outFile},
		{"ref name", "HEAD", valid},
		{"branch range", "main..main", valid},
		{"short sha", valid[:12], valid},
		{"upper case", strings.ToUpper(valid), valid},
		{"trailing newline", valid + "\n", valid},
		{"revision suffix", valid[:38] + "^{", valid},
		{"non hex 40", strings.Repeat("g", 40), valid},
		{"41 chars", valid + "a", valid},
	}
	d := New(f.dir)
	d.gitPath = "/nonexistent/git" // a malformed commit must fail before git is looked up
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Diff(t.Context(), tc.base, tc.head)
			if !errors.Is(err, ErrInvalidCommit) {
				t.Errorf("Diff(%q, %q) error = %v, want ErrInvalidCommit", tc.base, tc.head, err)
			}
		})
	}
	if _, err := os.Stat(outFile); err == nil {
		t.Error("--output option reached git")
	}
}

func TestDiff_SHA256ObjectNamesAreWellFormed(t *testing.T) {
	if err := validateCommit(strings.Repeat("0", 64)); err != nil {
		t.Errorf("validateCommit(64 hex) = %v, want nil", err)
	}
}

func TestDiff_SizeCap(t *testing.T) {
	f := newFixture(t)
	d := New(f.dir)
	d.MaxBytes = 100
	if _, err := d.Diff(t.Context(), f.base, f.head); !errors.Is(err, ErrDiffTooLarge) {
		t.Errorf("Diff() with a 100 byte cap error = %v, want ErrDiffTooLarge", err)
	}
	// The default cap is the Phase 6 cap.
	full, err := New(f.dir).Diff(t.Context(), f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	d.MaxBytes = int64(len(full))
	if got, err := d.Diff(t.Context(), f.base, f.head); err != nil || got != full {
		t.Errorf("Diff() with an exact cap error = %v, same output = %v", err, got == full)
	}
	if domain.MaxDiffBytes != 64<<20 {
		t.Errorf("domain.MaxDiffBytes = %d, want 64 MiB", domain.MaxDiffBytes)
	}
}

func TestDiff_Failures(t *testing.T) {
	f := newFixture(t)
	t.Run("not a repository", func(t *testing.T) {
		_, err := New(t.TempDir()).Diff(t.Context(), f.base, f.head)
		var miss *CommitMissingError
		if err == nil || errors.As(err, &miss) {
			t.Errorf("Diff() outside a repository error = %v, want a plain error", err)
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		if _, err := New(filepath.Join(t.TempDir(), "absent")).Diff(t.Context(), f.base, f.head); err == nil {
			t.Error("Diff() in a missing directory succeeded")
		}
	})
	t.Run("empty directory name", func(t *testing.T) {
		if _, err := New("").Diff(t.Context(), f.base, f.head); err == nil {
			t.Error("Diff() with an empty directory succeeded")
		}
	})
	t.Run("git program missing", func(t *testing.T) {
		d := New(f.dir)
		d.gitPath = "/nonexistent/git"
		_, err := d.Diff(t.Context(), f.base, f.head)
		var miss *CommitMissingError
		if err == nil || errors.As(err, &miss) {
			t.Errorf("Diff() without git error = %v, want a plain error", err)
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := New(f.dir).Diff(ctx, f.base, f.head)
		var miss *CommitMissingError
		if err == nil || errors.As(err, &miss) {
			t.Errorf("Diff() with a canceled context error = %v, want a plain error", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		d := New(f.dir)
		d.Timeout = time.Nanosecond
		if _, err := d.Diff(t.Context(), f.base, f.head); err == nil {
			t.Error("Diff() with a 1ns timeout succeeded")
		}
	})
}

func TestChildEnv_HasNoInheritedGitVariables(t *testing.T) {
	t.Setenv("GIT_EXTERNAL_DIFF", "x")
	t.Setenv("GITLAB_TOKEN", "secret")
	t.Setenv("DEVDIGEST_GITLAB_TOKEN", "secret")
	t.Setenv("HTTPS_PROXY", "http://proxy")
	env := strings.Join(childEnv(), "\n")
	for _, bad := range []string{"GIT_EXTERNAL_DIFF", "TOKEN", "PROXY", "secret"} {
		if strings.Contains(env, bad) {
			t.Errorf("childEnv() contains %q:\n%s", bad, env)
		}
	}
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_NO_LAZY_FETCH=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("childEnv() lacks %q", want)
		}
	}
}
