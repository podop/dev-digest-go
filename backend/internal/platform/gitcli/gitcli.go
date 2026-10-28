// Package gitcli runs the git binary for cloning and refreshing repositories.
//
// Security properties (spec NFR1, NFR4, FR35):
//   - git never prompts (GIT_TERMINAL_PROMPT=0, GCM_INTERACTIVE=never, no credential helper);
//   - the GitHub token reaches git only through the GIT_CONFIG_COUNT/KEY_0/VALUE_0
//     environment of a single process, scoped to https://github.com/. It is never
//     placed in argv, never written to .git/config and scrubbed from errors;
//   - hooks are disabled (core.hooksPath=/dev/null) and no code of a cloned
//     repository is ever executed;
//   - clone paths are exactly <root>/<owner>/<name> with validated segments, or the GitLab
//     form <root>/@gitlab/<host>[_<port>]/<project id>;
//   - a GitLab remote ([Remote]) gets its own token, scoped to the remote's URL prefix, plus no
//     redirects, no proxy, an optional CA bundle and a pinned, checked address (curloptResolve).
package gitcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/egress"
)

const (
	// SyncDepth is the depth fetched by Sync: deeper than the shallow clone so
	// the previously seen commit is usually reachable.
	SyncDepth = 50

	tokenUsername = "x-access-token"
	// gitlabUsername is the Basic user name for a GitLab access token: GitLab accepts any
	// non-empty name (spec D32).
	gitlabUsername = "oauth2"
	// githubPrefix scopes the auth header: git never sends it to other hosts.
	githubPrefix = "https://github.com/"

	stdoutLimit   = 1 << 20
	stderrKeep    = 8 << 10
	stderrReport  = 2 << 10
	killWaitDelay = 2 * time.Second
)

var (
	// ErrNoDefaultBranch means the remote did not report a symbolic HEAD.
	ErrNoDefaultBranch = errors.New("remote default branch not reported")
	// ErrInvalidSHA reports a commit id that is not 4 to 64 hexadecimal digits.
	ErrInvalidSHA = errors.New("invalid commit sha")
	// ErrOutputTooLarge means git printed more than the stdout cap, so the output is incomplete.
	ErrOutputTooLarge = errors.New("git output exceeds the size limit")
	// ErrBlobTooLarge means a file at a commit is larger than the caller's limit; it was not read.
	ErrBlobTooLarge = errors.New("file at commit exceeds the size limit")
	// ErrBlobMissing means the commit, the path or the clone does not exist.
	ErrBlobMissing = errors.New("file not found at commit")
	// ErrInvalidBlobPath reports a path that is not a plain relative path inside a commit.
	ErrInvalidBlobPath = errors.New("invalid file path")
	// ErrNotBlob means the path exists at the commit but is not a regular file (a directory or a submodule).
	ErrNotBlob = errors.New("path at commit is not a file")
	// ErrInvalidRemote reports a Remote that is not a credential-free https URL, whose prefix is
	// not a prefix of that URL, or whose CA file is not an absolute path.
	ErrInvalidRemote = errors.New("invalid git remote")
	// ErrForbiddenAddress means the remote host resolves to an address git must not connect to.
	ErrForbiddenAddress = errors.New("git remote resolves to a forbidden address")
	// ErrGitLabKey means Clone, Sync or DefaultBranch (GitHub only) was given a GitLab clone key;
	// those need CloneRemote, SyncRemote or DefaultBranchRemote.
	ErrGitLabKey = errors.New("GitLab clone key needs the *Remote calls")
	// ErrNotGitLabKey means a *Remote call was given a clone key that is not the GitLab form.
	ErrNotGitLabKey = errors.New("clone key is not a GitLab key")
)

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
	// Resolver looks up the addresses of a GitLab remote host; nil means the system resolver.
	Resolver func(ctx context.Context, host string) ([]netip.Addr, error)
	// AddrGuard reports whether an address must not be connected to; nil means
	// egress.ForbiddenAddr (link-local and unspecified, IPv4-mapped IPv6 judged as IPv4;
	// loopback and private addresses stay allowed: self-hosted GitLab often runs there).
	// cmd injects the stricter custom.ForbiddenAddr (it also catches IPv4-compatible, NAT64
	// and 6to4 forms), keeping this platform package free of the llm layer.
	AddrGuard func(netip.Addr) bool
}

// Remote is where a GitLab clone fetches from and how it authenticates. Its token never reaches
// argv, the URL, the clone's git config, errors or logs.
type Remote struct {
	// URL is the credential-free https clone URL, e.g. https://gitlab.example.com/g/p.git.
	URL string
	// Prefix scopes the auth header (http.<Prefix>.extraHeader): an https URL ending in "/" that
	// URL starts with, e.g. https://gitlab.example.com/. Git never sends the token elsewhere.
	Prefix string
	// Token is the access token; empty means anonymous.
	Token string
	// CAFile is a PEM bundle for http.sslCAInfo (see WriteCABundle); empty means git's default.
	CAFile string
}

// String omits the token, so a Remote printed with %v or %+v cannot leak it.
func (r Remote) String() string { return "Remote{" + r.URL + "}" }

// GoString omits the token, so %#v cannot leak it.
func (r Remote) GoString() string { return r.String() }

// LogValue omits the token for slog.
func (r Remote) LogValue() slog.Value {
	return slog.GroupValue(slog.String("url", r.URL), slog.String("prefix", r.Prefix))
}

// CloneOptions tunes Clone.
type CloneOptions struct {
	// Depth is the shallow clone depth; 0 clones the full history.
	Depth int
}

// Client runs git commands. It is safe for concurrent use.
type Client struct {
	binary   string
	root     string
	token    TokenProvider
	resolver func(ctx context.Context, host string) ([]netip.Addr, error)
	guard    func(netip.Addr) bool
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
	c := &Client{binary: bin, root: root, token: opts.Token, resolver: opts.Resolver, guard: opts.AddrGuard}
	if c.resolver == nil {
		c.resolver = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if c.guard == nil {
		c.guard = egress.ForbiddenAddr
	}
	return c, nil
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

// BlobSizeError is the ErrBlobTooLarge failure of BlobAt with the numbers behind it: the blob's
// size in bytes and the limit it exceeded (the caller's max, or git's 1 MiB output cap when that
// is what stopped the read). It matches ErrBlobTooLarge with errors.Is and is found with
// errors.As through the *Error that wraps it.
type BlobSizeError struct {
	Size int64
	Max  int64
}

func (e *BlobSizeError) Error() string {
	return fmt.Sprintf("%s (%d bytes > %d)", ErrBlobTooLarge.Error(), e.Size, e.Max)
}

// Is reports ErrBlobTooLarge, so existing errors.Is checks keep working.
func (e *BlobSizeError) Is(target error) bool { return target == ErrBlobTooLarge }

// Clone makes sure the clone of owner/name exists and is current. If
// <root>/<owner>/<name>/.git exists and its HEAD is a commit it fetches; otherwise it removes
// any partial directory and clones rawURL. It returns the absolute clone directory.
func (c *Client) Clone(ctx context.Context, owner, name, rawURL string, opts CloneOptions) (string, error) {
	if _, ok := gitlabHostSegment(owner); ok {
		return "", ErrGitLabKey
	}
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
	cloned, err := c.isClone(ctx, runCfg{}, dest)
	if err != nil {
		return "", err
	}
	if cloned {
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

// CloneRemote is Clone for a GitLab project: owner and name are the GitLab clone key
// ("@gitlab/<host>[_<port>]", "<project id>"), and the clone fetches from r.URL with r's token.
// An existing clone (see Clone) gets origin := r.URL first, so a project renamed on GitLab keeps
// syncing.
// Nothing is created when the remote is invalid or its host resolves to a forbidden address.
func (c *Client) CloneRemote(ctx context.Context, owner, name string, r Remote, opts CloneOptions) (string, error) {
	dest, err := c.gitlabClonePath(owner, name)
	if err != nil {
		return "", err
	}
	if opts.Depth < 0 {
		return "", errors.New("gitcli: negative clone depth")
	}
	rcfg, err := c.remoteCfg(ctx, r)
	if err != nil {
		return "", err
	}
	if err := c.prepareDest(dest); err != nil {
		return "", err
	}
	cloned, err := c.isClone(ctx, runCfg{remote: true}, dest)
	if err != nil {
		return "", err
	}
	if cloned {
		if err := c.setOrigin(ctx, dest, r.URL); err != nil {
			return "", err
		}
		if _, _, err := c.exec(ctx, rcfg, dest, "", "fetch"); err != nil {
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
	args = append(args, "--", r.URL, dest)
	if _, _, err := c.exec(ctx, rcfg, c.root, "", args...); err != nil {
		return "", err
	}
	return dest, nil
}

// SyncRemote is Sync for a GitLab project (see CloneRemote for the key and the remote).
func (c *Client) SyncRemote(ctx context.Context, owner, name, branch string, r Remote) (string, error) {
	dest, err := c.existingGitLabClone(owner, name)
	if err != nil {
		return "", err
	}
	if !validRef(branch) {
		return "", ErrInvalidRef
	}
	rcfg, err := c.remoteCfg(ctx, r)
	if err != nil {
		return "", err
	}
	if err := c.setOrigin(ctx, dest, r.URL); err != nil {
		return "", err
	}
	refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
	if _, _, err := c.exec(ctx, rcfg, dest, "", "fetch", "--depth", strconv.Itoa(SyncDepth), "origin", refspec); err != nil {
		return "", err
	}
	local := runCfg{remote: true}
	if _, _, err := c.exec(ctx, local, dest, "", "reset", "--hard", "refs/remotes/origin/"+branch, "--"); err != nil {
		return "", err
	}
	out, _, err := c.exec(ctx, local, dest, "", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// DefaultBranchRemote is DefaultBranch for a GitLab project (see CloneRemote for the key and the remote).
func (c *Client) DefaultBranchRemote(ctx context.Context, owner, name string, r Remote) (string, error) {
	dest, err := c.existingGitLabClone(owner, name)
	if err != nil {
		return "", err
	}
	rcfg, err := c.remoteCfg(ctx, r)
	if err != nil {
		return "", err
	}
	if err := c.setOrigin(ctx, dest, r.URL); err != nil {
		return "", err
	}
	out, _, err := c.exec(ctx, rcfg, dest, "", "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	return parseDefaultBranch(out)
}

func (c *Client) existingGitLabClone(owner, name string) (string, error) {
	if _, ok := gitlabHostSegment(owner); !ok {
		return "", ErrNotGitLabKey
	}
	return c.existingClone(owner, name)
}

func (c *Client) gitlabClonePath(owner, name string) (string, error) {
	if !strings.HasPrefix(owner, gitlabDir+"/") {
		return "", ErrNotGitLabKey
	}
	return c.ClonePath(owner, name)
}

// isClone reports whether dest holds a finished clone: a .git whose HEAD is a commit. A clone
// killed before its first pack arrived (attempt timeout) leaves a .git whose HEAD names an unborn
// branch; fetching into it would report success with nothing checked out. Any other git failure
// also means "not a clone" (the caller re-clones the read-only mirror); only ctx's end is an error.
func (c *Client) isClone(ctx context.Context, cfg runCfg, dest string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(dest, ".git")); err != nil {
		return false, nil
	}
	_, _, err := c.exec(ctx, cfg, dest, "", "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil && ctx.Err() != nil {
		return false, err
	}
	return err == nil, nil
}

// setOrigin points origin at rawURL (a credential-free URL checked by remoteCfg).
func (c *Client) setOrigin(ctx context.Context, dir, rawURL string) error {
	_, _, err := c.exec(ctx, runCfg{remote: true}, dir, "", "remote", "set-url", "origin", rawURL)
	return err
}

// remoteCfg validates r and builds the configuration of its network commands: the token scoped
// to r.Prefix, no redirects, no proxy, certificate verification on, r.CAFile as the trust store
// and the host pinned to the addresses checked here.
func (c *Client) remoteCfg(ctx context.Context, r Remote) (runCfg, error) {
	u, err := checkRemote(r)
	if err != nil {
		return runCfg{}, err
	}
	pre := []string{
		"-c", "http.followRedirects=false",
		"-c", "http.proxy=",
		"-c", "http.sslVerify=true",
	}
	if r.CAFile != "" {
		pre = append(pre, "-c", "http.sslCAInfo="+r.CAFile)
	}
	pin, err := c.pin(ctx, u)
	if err != nil {
		return runCfg{}, err
	}
	if pin != "" {
		pre = append(pre, "-c", "http.curloptResolve="+pin)
	}
	return runCfg{token: r.Token, user: gitlabUsername, prefix: r.Prefix, pre: pre, remote: true}, nil
}

// checkRemote accepts only a credential-free https URL that starts with a clean https prefix of
// the same host (fail closed: git would otherwise scope the header by its own matching rules).
func checkRemote(r Remote) (*url.URL, error) {
	for _, s := range []string{r.URL, r.Prefix, r.CAFile} {
		for _, ch := range s {
			if ch < 0x21 || ch == 0x7f {
				return nil, ErrInvalidRemote
			}
		}
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrInvalidRemote
	}
	p, err := url.Parse(r.Prefix)
	if err != nil || p.Scheme != "https" || p.Host != u.Host || p.User != nil ||
		p.RawQuery != "" || p.Fragment != "" || strings.Contains(r.Prefix, "*") ||
		!strings.HasSuffix(r.Prefix, "/") || !strings.HasPrefix(r.URL, r.Prefix) {
		return nil, ErrInvalidRemote
	}
	if r.CAFile != "" && !filepath.IsAbs(r.CAFile) {
		return nil, ErrInvalidRemote
	}
	return u, nil
}

// pin resolves the remote host, refuses it when any address is forbidden and returns the
// http.curloptResolve entry that makes git connect to exactly those addresses ("" for an IP
// literal, which needs no lookup). Checking and pinning the same answer closes the window in
// which git could resolve the name again and reach another address (DNS rebinding).
func (c *Client) pin(ctx context.Context, u *url.URL) (string, error) {
	host := strings.ToLower(u.Hostname())
	if a, err := netip.ParseAddr(host); err == nil {
		if c.guard(a) {
			return "", ErrForbiddenAddress
		}
		return "", nil
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	addrs, err := c.resolver(ctx, host)
	if err != nil {
		return "", &Error{Op: "resolve", Err: fmt.Errorf("resolve git host: %w", err)}
	}
	if len(addrs) == 0 {
		return "", &Error{Op: "resolve", Err: errors.New("git host has no address")}
	}
	list := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if c.guard(a) {
			return "", &Error{Op: "resolve", Err: ErrForbiddenAddress}
		}
		a = a.WithZone("")
		if a.Is6() && !a.Is4In6() {
			list = append(list, "["+a.String()+"]")
		} else {
			list = append(list, a.Unmap().String())
		}
	}
	return host + ":" + port + ":" + strings.Join(list, ","), nil
}

// Sync fetches branch with depth SyncDepth, hard-resets the clone to it and
// returns the new HEAD sha. The clone is a read-only mirror, so the reset is safe.
func (c *Client) Sync(ctx context.Context, owner, name, branch string) (string, error) {
	if _, ok := gitlabHostSegment(owner); ok {
		return "", ErrGitLabKey
	}
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
	if _, ok := gitlabHostSegment(owner); ok {
		return "", ErrGitLabKey
	}
	dest, err := c.existingClone(owner, name)
	if err != nil {
		return "", err
	}
	out, err := c.run(ctx, dest, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	return parseDefaultBranch(out)
}

// parseDefaultBranch reads the symbolic HEAD out of `git ls-remote --symref` output.
func parseDefaultBranch(out string) (string, error) {
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

// DiffNames lists the paths that differ between the commits base and head of an existing clone
// (`git diff --name-only --no-renames -z base head --`): a rename shows as its two paths. Both
// ids must be hexadecimal, so neither can be read as an option or a range. Paths are returned
// as git prints them, in git's order, raw (no quoting). A missing commit, as in a shallow
// clone, is an *Error; output above the stdout cap is ErrOutputTooLarge, never a short list.
func (c *Client) DiffNames(ctx context.Context, owner, name, base, head string) ([]string, error) {
	dest, err := c.existingClone(owner, name)
	if err != nil {
		return nil, err
	}
	if !validSHA(base) || !validSHA(head) {
		return nil, ErrInvalidSHA
	}
	out, truncated, err := c.runCapped(ctx, dest, "diff", "--name-only", "--no-renames", "-z", base, head, "--")
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, &Error{Op: "diff", Err: ErrOutputTooLarge}
	}
	names := []string{}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			names = append(names, p)
		}
	}
	return names, nil
}

var (
	baseRefRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	headSHARE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	fullSHARE = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// BaseCommit resolves the commit a pull request is based on: the merge base of baseRef and
// headSHA in an existing clone, else the tip of baseRef. Candidates are origin/<baseRef> then
// <baseRef>; each is tried with `merge-base <ref> <head>` and then `rev-parse --verify
// <ref>^{commit}`; the first 40-hex answer wins. It returns "" and no error when nothing
// resolves: an unsafe ref or head (see below), a missing clone, a ref or commit the clone lacks.
// baseRef must match ^[A-Za-z0-9._/-]+$, must not start with "-" or contain ".."; headSHA must
// match ^[0-9a-f]{7,40}$, so neither can be read as an option, a range or a revision expression.
// A cancelled ctx or an unusable clone path is an error.
func (c *Client) BaseCommit(ctx context.Context, owner, name, baseRef, headSHA string) (string, error) {
	dest, err := c.existingClone(owner, name)
	if err != nil {
		return "", err
	}
	if !baseRefRE.MatchString(baseRef) || strings.HasPrefix(baseRef, "-") ||
		strings.Contains(baseRef, "..") || !headSHARE.MatchString(headSHA) {
		return "", nil
	}
	for _, ref := range []string{"origin/" + baseRef, baseRef} {
		for _, args := range [][]string{
			{"merge-base", ref, headSHA},
			{"rev-parse", "--verify", ref + "^{commit}"},
		} {
			out, err := c.run(ctx, dest, args...)
			if err != nil {
				if ctx.Err() != nil {
					return "", err
				}
				continue // this candidate does not exist in the clone
			}
			if sha := strings.TrimSpace(out); fullSHARE.MatchString(sha) {
				return sha, nil
			}
		}
	}
	return "", nil
}

// BlobAt returns the content of path at commit in an existing clone, without a checkout and
// without following anything on disk. The size is checked first: a file over max bytes (or over
// git's 1 MiB output cap) is ErrBlobTooLarge and is not read. A missing clone, commit or path is
// ErrBlobMissing; a directory or submodule is ErrNotBlob; a path that is not a plain relative
// path (empty, absolute, "." or ".." segments, backslash, control characters, trailing slash) is
// ErrInvalidBlobPath and commit must be hexadecimal (ErrInvalidSHA), so neither reaches git as
// an option or a revision expression.
func (c *Client) BlobAt(ctx context.Context, owner, name, commit, path string, max int64) ([]byte, error) {
	dest, err := c.existingClone(owner, name)
	if err != nil {
		return nil, err
	}
	if !validSHA(commit) {
		return nil, ErrInvalidSHA
	}
	if !validBlobPath(path) {
		return nil, ErrInvalidBlobPath
	}
	if max <= 0 {
		return nil, errors.New("gitcli: blob size limit must be positive")
	}
	if _, err := os.Lstat(dest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrBlobMissing
		}
		return nil, fmt.Errorf("inspect clone path: %w", err)
	}

	// batch-check answers "<input> missing" with exit 0 for an absent object, so a missing file
	// is told from a failing git without reading stderr.
	out, _, err := c.runStdin(ctx, dest, commit+":"+path+"\n",
		"cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(out)
	fields := strings.Split(line, " ")
	if len(fields) != 3 || !validSHA(fields[0]) {
		if strings.HasSuffix(line, " missing") {
			return nil, ErrBlobMissing
		}
		return nil, &Error{Op: "cat-file", Err: errors.New("unexpected batch-check answer")}
	}
	if fields[1] != "blob" {
		return nil, &Error{Op: "cat-file", Err: ErrNotBlob}
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return nil, &Error{Op: "cat-file", Err: errors.New("unexpected object size")}
	}
	if size > max {
		return nil, &Error{Op: "cat-file", Err: &BlobSizeError{Size: size, Max: max}}
	}
	body, truncated, err := c.runCapped(ctx, dest, "cat-file", "blob", fields[0])
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, &Error{Op: "cat-file", Err: &BlobSizeError{Size: size, Max: stdoutLimit}}
	}
	return []byte(body), nil
}

// validBlobPath accepts a plain relative path inside a commit: no empty, "." or ".." segment,
// no leading or trailing slash, no backslash, no NUL, newline or other control character.
func validBlobPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// validSHA accepts 4 to 64 hexadecimal digits (SHA-1 and SHA-256 object ids, abbreviations too).
func validSHA(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func (c *Client) existingClone(owner, name string) (string, error) {
	dest, err := c.ClonePath(owner, name)
	if err != nil {
		return "", err
	}
	if err := c.rejectSymlinks(dest); err != nil {
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
	out, _, err := c.runCapped(ctx, dir, args...)
	return out, err
}

// runCapped is run that also reports whether stdout hit the cap and was cut short.
func (c *Client) runCapped(ctx context.Context, dir string, args ...string) (string, bool, error) {
	return c.runStdin(ctx, dir, "", args...)
}

// runStdin is runCapped that feeds stdin to git; empty stdin means none. It authenticates with
// the GitHub token, scoped to https://github.com/.
func (c *Client) runStdin(ctx context.Context, dir, stdin string, args ...string) (string, bool, error) {
	cfg := runCfg{}
	if c.token != nil {
		t, err := c.token(ctx)
		if err != nil {
			return "", false, &Error{Op: args[0], Err: fmt.Errorf("resolve token: %w", err)}
		}
		cfg = runCfg{token: t, user: tokenUsername, prefix: githubPrefix}
	}
	return c.exec(ctx, cfg, dir, stdin, args...)
}

// runCfg is how one git process is hardened and authenticated.
type runCfg struct {
	token  string   // empty = anonymous
	user   string   // Basic user name
	prefix string   // URL prefix the auth header is scoped to
	pre    []string // extra "-c key=value" pairs, placed after the hardening flags
	remote bool     // GitLab remote: proxy and TLS overrides of the environment are dropped
}

// exec runs `git <hardening> <cfg.pre> <args...>` in dir; args[0] is the subcommand.
func (c *Client) exec(ctx context.Context, cfg runCfg, dir, stdin string, args ...string) (string, bool, error) {
	op := args[0]
	argv := append([]string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "credential.helper=",
	}, cfg.pre...)
	argv = append(argv, args...)

	cmd := exec.CommandContext(ctx, c.binary, argv...)
	cmd.Dir = dir
	cmd.Env = gitEnv(cfg)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = stdoutLimit, stderrKeep
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.WaitDelay = killWaitDelay

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, &Error{Op: op, Err: ctxErr}
		}
		return "", false, &Error{Op: op, Err: err, Stderr: tail(scrubAs(stderr.String(), cfg.user, cfg.token), stderrReport)}
	}
	return stdout.String(), stdout.truncated, nil
}

// gitEnv builds the child environment: the parent's, minus variables that could
// redirect git or inject configuration, plus the non-interactive switches and,
// when a token is set, the one-process auth header.
func gitEnv(cfg runCfg) []string {
	env := make([]string, 0, len(os.Environ())+6)
	for _, kv := range os.Environ() {
		if dropEnv(kv) || (cfg.remote && dropRemoteEnv(kv)) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	if cfg.token != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http."+cfg.prefix+".extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basicAs(cfg.user, cfg.token),
		)
	}
	return env
}

func basicAs(user, token string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
}

// dropRemoteEnv reports whether an inherited variable must not reach git for a GitLab remote:
// proxies (SCR2) and TLS overrides that could turn verification off or swap the trust store.
func dropRemoteEnv(kv string) bool {
	key, _, _ := strings.Cut(kv, "=")
	switch strings.ToLower(key) {
	case "http_proxy", "https_proxy", "all_proxy", "ftp_proxy", "no_proxy",
		"git_ssl_no_verify", "git_ssl_cainfo", "git_ssl_capath", "git_ssl_cert", "git_ssl_key",
		"git_http_proxy_authmethod", "git_proxy_command", "curl_ca_bundle":
		return true
	}
	return false
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

// scrubAs removes the token, its Basic-auth encoding (sent under user) and URL userinfo from git output.
func scrubAs(s, user, token string) string {
	if token != "" {
		s = strings.NewReplacer(token, "[REDACTED]", basicAs(user, token), "[REDACTED]").Replace(s)
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
// chatty git cannot grow memory without bound. truncated records that bytes were dropped.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := max(b.limit-b.buf.Len(), 0)
	if len(p) > room {
		b.truncated = true
	}
	b.buf.Write(p[:min(room, len(p))])
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
