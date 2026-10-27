// Package gitdiff is the runner's GitLab-mode diff source (spec FR20, NFR10, decision P6): the
// unified diff from a base to a head commit of the job's own checkout, computed by the image's
// `git` program through os/exec. It is not the server's platform/gitcli: the runner links no git
// library, makes no network request for the diff and sends no token.
//
// Everything that could make git run a program or reach the network is switched off: the child
// gets a minimal environment (no GIT_EXTERNAL_DIFF, GIT_DIR, tokens or proxies; no system or global
// configuration), the diff runs with --no-ext-diff and --no-textconv, lazy object fetching is off,
// and the repository's own configuration cannot change the output format (prefixes, colour,
// pager). The two commits are validated as lower-case hexadecimal object names, so no ref or
// option text from the environment reaches the argument vector.
//
// The package imports only the standard library and the runner domain constant for the cap.
package gitdiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dev-digest-go/backend/internal/ci/runner/domain"
)

// Roles of the two commits in a CommitMissingError.
const (
	RoleBase = "Base"
	RoleHead = "Head"
)

const (
	// defaultTimeout is the time budget of one git invocation when the caller's context has none.
	defaultTimeout = 5 * time.Minute
	// stderrBytes is how much of git's standard error reaches an error message.
	stderrBytes = 2 << 10
)

// ErrDiffTooLarge is returned when the diff exceeds the cap (spec FR20: 64 MiB).
var ErrDiffTooLarge = errors.New("diff is larger than the cap")

// ErrInvalidCommit is returned (wrapped) when a commit is not 40 or 64 lower-case hexadecimal
// characters. Nothing is executed in that case.
var ErrInvalidCommit = errors.New("not a full lower-case hexadecimal commit sha")

// CommitMissingError means a commit named for the diff is not in the checkout, typically a
// shallow clone (spec FR20, EC19). Its text is the complete job-log message.
type CommitMissingError struct {
	// Role is RoleBase or RoleHead.
	Role string
	// SHA is the missing commit.
	SHA string
}

func (e *CommitMissingError) Error() string {
	return fmt.Sprintf("%s commit %s is not in the job's clone; keep GIT_DEPTH: \"0\" in the job", e.Role, e.SHA)
}

// CommitMissing reports the missing commit's sha. It lets a consumer recognise the error by an
// interface of its own without importing this adapter.
func (e *CommitMissingError) CommitMissing() string { return e.SHA }

// Differ computes diffs of one checkout. The zero value is not usable; build it with New.
type Differ struct {
	// MaxBytes caps the diff; 0 means domain.MaxDiffBytes (64 MiB).
	MaxBytes int64
	// Timeout bounds one git invocation; 0 means five minutes.
	Timeout time.Duration

	dir     string
	gitPath string // "" = look up "git" on PATH
}

// New returns a Differ for the checkout at dir (CI_PROJECT_DIR).
func New(dir string) *Differ { return &Differ{dir: dir} }

// Diff returns the unified diff from base to head (`git diff base head`: base is the merge-base the
// platform names, so no merge-base is computed). Binary files appear as "Binary files ... differ"
// lines without content; renames are detected. Paths are prefixed a/ and b/ whatever the
// repository's configuration says. Errors: *CommitMissingError when a commit is absent,
// ErrInvalidCommit (wrapped) for a malformed sha, ErrDiffTooLarge past the cap, otherwise a
// generic error carrying git's (cut) standard error.
func (d *Differ) Diff(ctx context.Context, base, head string) (string, error) {
	if err := validateCommit(base); err != nil {
		return "", fmt.Errorf("base commit: %w", err)
	}
	if err := validateCommit(head); err != nil {
		return "", fmt.Errorf("head commit: %w", err)
	}
	if d.dir == "" {
		return "", errors.New("git diff: checkout directory is empty")
	}
	abs, err := filepath.Abs(d.dir)
	if err != nil {
		return "", fmt.Errorf("git diff: checkout directory: %w", err)
	}
	gitPath, err := d.git()
	if err != nil {
		return "", err
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for _, c := range []struct{ role, sha string }{{RoleBase, base}, {RoleHead, head}} {
		present, err := d.hasCommit(ctx, gitPath, abs, c.sha)
		if err != nil {
			return "", err
		}
		if !present {
			return "", &CommitMissingError{Role: c.role, SHA: c.sha}
		}
	}

	limit := d.MaxBytes
	if limit <= 0 {
		limit = domain.MaxDiffBytes
	}
	out := &cappedBuffer{limit: limit, cancel: cancel}
	args := append(baseArgs(abs),
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "-M", "--no-relative",
		"--src-prefix=a/", "--dst-prefix=b/", "-U3", "--inter-hunk-context=0",
		base, head, "--")
	stderr, err := d.run(ctx, gitPath, abs, args, out)
	if out.exceeded {
		return "", ErrDiffTooLarge
	}
	if err != nil {
		return "", gitError("git diff", err, stderr)
	}
	return out.buf.String(), nil
}

func (d *Differ) git() (string, error) {
	if d.gitPath != "" {
		return d.gitPath, nil
	}
	p, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git program not found: %w", err)
	}
	return p, nil
}

// hasCommit reports whether sha names a commit object of the checkout. `rev-parse --verify
// --quiet` exits 1 for a missing object and 128 for a fatal problem (not a repository, ...).
func (d *Differ) hasCommit(ctx context.Context, gitPath, dir, sha string) (bool, error) {
	args := append(baseArgs(dir), "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	var out cappedBuffer
	out.limit = 1 << 10
	stderr, err := d.run(ctx, gitPath, dir, args, &out)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(bytes.TrimSpace(stderr)) == 0 {
		return false, nil
	}
	return false, gitError("git rev-parse", err, stderr)
}

func (d *Differ) run(ctx context.Context, gitPath, dir string, args []string, stdout io.Writer) ([]byte, error) {
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Dir = dir
	cmd.Env = childEnv()
	cmd.Stdin = nil
	cmd.Stdout = stdout
	var errBuf cappedBuffer
	errBuf.limit = stderrBytes
	errBuf.truncate = true
	cmd.Stderr = &errBuf
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return errBuf.buf.Bytes(), err
}

// baseArgs are the options before the subcommand. safe.directory is honoured on the command line
// (never from the repository) and keeps git from refusing a checkout owned by another user, which
// is common in runner containers; the checkout is the job's own. core.fsmonitor, core.hooksPath
// and the pager are neutralised so a repository setting cannot start a program.
func baseArgs(dir string) []string {
	return []string{
		"--no-pager",
		"-c", "safe.directory=" + dir,
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.pager=cat",
		"-c", "diff.external=",
		"-c", "core.quotePath=true",
		"-c", "color.ui=never",
	}
}

// childEnv is the whole environment of a git child: PATH to find helpers, no system or global
// configuration, no prompts, no lazy fetch from a promisor remote (no network), a fixed locale.
// Nothing else is inherited: not GIT_EXTERNAL_DIFF, GIT_DIR, GIT_CONFIG_*, proxies or tokens.
func childEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"LC_ALL=C",
	}
}

// validateCommit accepts a full SHA-1 or SHA-256 object name in lower-case hexadecimal.
func validateCommit(sha string) error {
	if len(sha) != 40 && len(sha) != 64 {
		return fmt.Errorf("%w: length %d", ErrInvalidCommit, len(sha))
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: character %d", ErrInvalidCommit, i)
		}
	}
	return nil
}

func gitError(what string, err error, stderr []byte) error {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s: %w: %s", what, err, msg)
}

// cappedBuffer collects output up to limit. With truncate the rest is dropped silently (standard
// error); otherwise exceeding the limit marks the buffer, stops the child through cancel and fails
// the write.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int64
	truncate bool
	exceeded bool
	cancel   context.CancelFunc
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - int64(b.buf.Len())
	if int64(len(p)) <= room {
		return b.buf.Write(p)
	}
	if b.truncate {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	b.exceeded = true
	if b.cancel != nil {
		b.cancel()
	}
	return 0, ErrDiffTooLarge
}
