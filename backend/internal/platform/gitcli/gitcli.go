// Package gitcli runs the git binary for cloning and refreshing repositories.
//
// Security properties (spec NFR1, NFR4, FR35):
//   - git never prompts (GIT_TERMINAL_PROMPT=0, GCM_INTERACTIVE=never, no credential helper);
//   - the GitHub token reaches git only through the GIT_CONFIG_COUNT/KEY_0/VALUE_0
//     environment of a single process, scoped to https://github.com/. It is never
//     placed in argv, never written to .git/config and scrubbed from errors;
//   - hooks are disabled (core.hooksPath=/dev/null) and no code of a cloned
//     repository is ever executed;
//   - clone paths are exactly <root>/<owner>/<name> with validated segments.
package gitcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// SyncDepth is the depth fetched by Sync: deeper than the shallow clone so
	// the previously seen commit is usually reachable.
	SyncDepth = 50

	tokenUsername = "x-access-token"
	// githubPrefix scopes the auth header: git never sends it to other hosts.
	githubPrefix = "https://github.com/"

	stdoutLimit   = 1 << 20
	stderrKeep    = 8 << 10
	stderrReport  = 2 << 10
	killWaitDelay = 2 * time.Second
)

// ErrNoDefaultBranch means the remote did not report a symbolic HEAD.
var ErrNoDefaultBranch = errors.New("remote default branch not reported")

// TokenProvider returns the GitHub token at call time, so rotations apply
// without a restart. An empty token means anonymous access.
type TokenProvider func(ctx context.Context) (string, error)

// Options configures a Client.
type Options struct {
	// Binary is the git executable; empty means "git" from PATH.
	Binary string
	// CloneRoot is the directory holding <owner>/<name> clones. Required.
	CloneRoot string
	// Token is optional; nil means always anonymous.
	Token TokenProvider
}

// CloneOptions tunes Clone.
type CloneOptions struct {
	// Depth is the shallow clone depth; 0 clones the full history.
	Depth int
}

// Client runs git commands. It is safe for concurrent use.
type Client struct {
	binary string
	root   string
	token  TokenProvider
}

// New validates the options and returns a Client.
func New(opts Options) (*Client, error) {
	if opts.CloneRoot == "" {
		return nil, errors.New("gitcli: clone root is required")
	}
	root, err := filepath.Abs(opts.CloneRoot)
	if err != nil {
		return nil, fmt.Errorf("gitcli: resolve clone root: %w", err)
	}
	bin := opts.Binary
	if bin == "" {
		bin = "git"
	}
	return &Client{binary: bin, root: root, token: opts.Token}, nil
}

// Root returns the absolute clone root.
func (c *Client) Root() string { return c.root }

// Error is a failed git invocation. It carries the subcommand and a scrubbed,
// truncated stderr; never the argument list or the token.
type Error struct {
	Op     string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := "git " + e.Op
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Clone makes sure the clone of owner/name exists and is current. If
// <root>/<owner>/<name>/.git exists it fetches; otherwise it removes any partial
// directory and clones rawURL. It returns the absolute clone directory.
func (c *Client) Clone(ctx context.Context, owner, name, rawURL string, opts CloneOptions) (string, error) {
	dest, err := c.ClonePath(owner, name)
	if err != nil {
		return "", err
	}
	if rawURL == "" {
		return "", errors.New("gitcli: empty clone url")
	}
	if opts.Depth < 0 {
		return "", errors.New("gitcli: negative clone depth")
	}
	if err := c.prepareDest(dest); err != nil {
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(dest, ".git")); err == nil {
		if err := c.scrubOrigin(ctx, dest); err != nil {
			return "", err
		}
		if _, err := c.run(ctx, dest, "fetch"); err != nil {
			return "", err
		}
		return dest, nil
	}
	// A previous attempt may have died mid-write; git refuses a non-empty dest.
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("remove partial clone: %w", err)
	}
	args := []string{"clone"}
	if opts.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(opts.Depth))
	}
	// Credential-free URL; `--` keeps url and dest from being read as options.
	args = append(args, "--", stripCredentials(rawURL), dest)
	if _, err := c.run(ctx, c.root, args...); err != nil {
		return "", err
	}
	return dest, nil
}

// Sync fetches branch with depth SyncDepth, hard-resets the clone to it and
// returns the new HEAD sha. The clone is a read-only mirror, so the reset is safe.
func (c *Client) Sync(ctx context.Context, owner, name, branch string) (string, error) {
	dest, err := c.existingClone(owner, name)
	if err != nil {
		return "", err
	}
	if !validRef(branch) {
		return "", ErrInvalidRef
	}
	if err := c.scrubOrigin(ctx, dest); err != nil {
		return "", err
	}
	// Explicit refspec: a depth-1 clone is single-branch and would not update
	// origin/<branch> for a branch other than the one it was cloned from.
	refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
	if _, err := c.run(ctx, dest, "fetch", "--depth", strconv.Itoa(SyncDepth), "origin", refspec); err != nil {
		return "", err
	}
	if _, err := c.run(ctx, dest, "reset", "--hard", "refs/remotes/origin/"+branch, "--"); err != nil {
		return "", err
	}
	return c.head(ctx, dest)
}

// DefaultBranch asks the remote of an existing clone for its default branch
// (`git ls-remote --symref origin HEAD`).
func (c *Client) DefaultBranch(ctx context.Context, owner, name string) (string, error) {
	dest, err := c.existingClone(owner, name)
	if err != nil {
		return "", err
	}
	out, err := c.run(ctx, dest, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		ref, ok := strings.CutPrefix(line, "ref: refs/heads/")
		if !ok {
			continue
		}
		branch, _, _ := strings.Cut(ref, "\t")
		branch = strings.TrimSpace(branch)
		if !validRef(branch) {
			return "", ErrInvalidRef
		}
		return branch, nil
	}
	return "", ErrNoDefaultBranch
}

// Head returns the sha of HEAD in an existing clone.
func (c *Client) Head(ctx context.Context, owner, name string) (string, error) {
	dest, err := c.existingClone(owner, name)
	if err != nil {
		return "", err
	}
	return c.head(ctx, dest)
}

func (c *Client) existingClone(owner, name string) (string, error) {
	dest, err := c.ClonePath(owner, name)
	if err != nil {
		return "", err
	}
	if err := rejectSymlink(filepath.Dir(dest)); err != nil {
		return "", err
	}
	if err := rejectSymlink(dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (c *Client) head(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// scrubOrigin rewrites a credential-bearing origin URL (left by older clients
// that embedded the token) to its clean form. No-op otherwise.
func (c *Client) scrubOrigin(ctx context.Context, dir string) error {
	out, err := c.run(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	current := strings.TrimSpace(out)
	clean := stripCredentials(current)
	if current == "" || clean == current {
		return nil
	}
	_, err = c.run(ctx, dir, "remote", "set-url", "origin", clean)
	return err
}

// stripCredentials drops user:password@ from a URL; non-URL remotes pass through.
func stripCredentials(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// run executes `git <hardening> <args...>` in dir; args[0] is the subcommand,
// which names the operation in errors. Returns stdout.
func (c *Client) run(ctx context.Context, dir string, args ...string) (string, error) {
	op := args[0]
	var token string
	if c.token != nil {
		t, err := c.token(ctx)
		if err != nil {
			return "", &Error{Op: op, Err: fmt.Errorf("resolve token: %w", err)}
		}
		token = t
	}
	argv := append([]string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "credential.helper=",
	}, args...)

	cmd := exec.CommandContext(ctx, c.binary, argv...)
	cmd.Dir = dir
	cmd.Env = gitEnv(token)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = stdoutLimit, stderrKeep
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = killWaitDelay

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", &Error{Op: op, Err: ctxErr}
		}
		return "", &Error{Op: op, Err: err, Stderr: tail(scrub(stderr.String(), token), stderrReport)}
	}
	return stdout.String(), nil
}

// gitEnv builds the child environment: the parent's, minus variables that could
// redirect git or inject configuration, plus the non-interactive switches and,
// when a token is set, the one-process auth header.
func gitEnv(token string) []string {
	env := make([]string, 0, len(os.Environ())+6)
	for _, kv := range os.Environ() {
		if dropEnv(kv) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	if token != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http."+githubPrefix+".extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic(token),
		)
	}
	return env
}

func basic(token string) string {
	return base64.StdEncoding.EncodeToString([]byte(tokenUsername + ":" + token))
}

// dropEnv reports whether an inherited variable must not reach git.
func dropEnv(kv string) bool {
	key, _, _ := strings.Cut(kv, "=")
	switch key {
	case "GIT_TERMINAL_PROMPT", "GCM_INTERACTIVE", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
		"GIT_ASKPASS", "SSH_ASKPASS", "GIT_CURL_VERBOSE", "GIT_CONFIG_PARAMETERS":
		return true
	}
	return strings.HasPrefix(key, "GIT_CONFIG_COUNT") ||
		strings.HasPrefix(key, "GIT_CONFIG_KEY_") ||
		strings.HasPrefix(key, "GIT_CONFIG_VALUE_") ||
		strings.HasPrefix(key, "GIT_TRACE")
}

var (
	userinfoRE = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@]+@`)
	basicRE    = regexp.MustCompile(`(?i)(Authorization:\s*Basic\s+)\S+`)
)

// scrub removes the token, its Basic-auth encoding and URL userinfo from git output.
func scrub(s, token string) string {
	if token != "" {
		s = strings.NewReplacer(token, "[REDACTED]", basic(token), "[REDACTED]").Replace(s)
	}
	s = basicRE.ReplaceAllString(s, "${1}[REDACTED]")
	return userinfoRE.ReplaceAllString(s, "${1}[REDACTED]@")
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "..." + s[len(s)-n:]
	}
	return s
}

// limitedBuffer keeps at most limit bytes and silently drops the rest, so a
// chatty git cannot grow memory without bound.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
