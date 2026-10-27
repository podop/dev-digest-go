package peers

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/gitlab"
	repointel "dev-digest-go/backend/internal/repointel/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Git is the part of *gitcli.Client the doc reader uses: a blob of the clone at a commit, size
// checked before it is read.
type Git interface {
	BlobAt(ctx context.Context, owner, name, commit, path string, max int64) ([]byte, error)
}

// Contents is the part of *github.Client the doc reader uses: a file through the contents API.
// found is false for an absent file, a directory, a payload without content and a file over max.
type Contents interface {
	GetContent(ctx context.Context, owner, name, path, ref string, max int64) (content []byte, found bool, err error)
}

// Docs implements app.Docs over the clone, with the GitHub contents API as the fallback (the
// GitLab files API for a GitLab repository, WithGitLab).
type Docs struct {
	git Git
	gh  Contents
	gl  gitLabSide
}

var _ app.Docs = (*Docs)(nil)

// NewDocs wraps the git client and the GitHub client. Either missing is a wiring error and panics.
func NewDocs(git Git, gh Contents) *Docs {
	if git == nil || gh == nil {
		panic("intent/adapters/peers: NewDocs needs a git client and a GitHub client")
	}
	return &Docs{git: git, gh: gh}
}

// WithGitLab enables the API fallback of GitLab repositories: reads is the GitLab client and
// resolve finds the connection by a repository's host key. Without it a GitLab doc that is not in
// the clone is "not found" with no request. It returns d for chaining.
func (d *Docs) WithGitLab(reads GitLabReads, resolve gitlab.ResolveFunc) *Docs {
	d.gl = gitLabSide{reads: reads, resolve: resolve}
	return d
}

// headSHA is the commit form BlobAt is asked for: 7 to 40 hexadecimal digits.
var headSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// Read reads a doc at the head commit (spec 7.4.3, "Doc read"). The extension and the path are
// checked first (a doc that fails them makes no git or GitHub call). The clone is asked next, with
// a cap checked before reading: a doc over it fails with its size and GitHub is not asked; any
// other clone failure (no clone, no such commit or path, a bad head SHA) falls back to GitHub,
// where absent, not a file, over the cap or any error all read as "not found". Text longer than
// the cap in UTF-16 units is cut and marked truncated. The clone of a GitLab repository is found
// by its clone key (codehost.CloneKey); a doc that is not in it is read from the GitLab files API
// of the same project with the repository's connection (readGitLab, FR29), never from GitHub.
func (d *Docs) Read(ctx context.Context, repo domain.Repo, sha, path string) app.DocRead {
	if !domain.HasDocExtension(path) {
		return failed("unsupported file type '" + path + "'")
	}
	if !safeDocPath(path) {
		return failed("unsafe path")
	}
	if headSHA.MatchString(sha) {
		owner, name := codehost.CloneKey(repo.Provider, repo.Host, repo.ProjectID, repo.Owner, repo.Name)
		raw, err := d.git.BlobAt(ctx, owner, name, sha, path, domain.DocFetchMaxBytes)
		if err == nil {
			return read(raw)
		}
		var tooLarge *gitcli.BlobSizeError
		if errors.As(err, &tooLarge) {
			return failed("File '" + path + "' at " + sha + " is too large (" + strconv.FormatInt(tooLarge.Size, 10) +
				" bytes > " + strconv.Itoa(domain.DocFetchMaxBytes) + ")")
		}
		if errors.Is(err, gitcli.ErrBlobTooLarge) { // the cap without its numbers: never expected
			return failed("File '" + path + "' at " + sha + " is too large")
		}
	}
	if ctx.Err() != nil {
		return failed("not found")
	}
	if repo.IsGitLab() {
		return d.readGitLab(ctx, repo, sha, path)
	}
	raw, found, err := d.gh.GetContent(ctx, repo.Owner, repo.Name, path, sha, domain.DocFetchMaxBytes)
	if err != nil || !found {
		return failed("not found")
	}
	return read(raw)
}

// glRef is the ref a GitLab doc is read at: the head commit as hex. Anything else (a bad head)
// reads as not found with no request.
var glRef = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// readGitLab reads GET /projects/<project id>/repository/files/<path>/raw?ref=<head SHA>, at most
// DocFetchMaxBytes bytes (FR29). The project is the repository's own (the extraction only lets
// same-project links through) and the connection the repository's, found by its host key. More
// than the cap, a 404, any other failure, no token and no connection all read as "not found"
// (GitHub parity); the last two make no request.
func (d *Docs) readGitLab(ctx context.Context, repo domain.Repo, sha, path string) app.DocRead {
	b, state := d.gl.connection(ctx, repo)
	if state != connReady || !glRef.MatchString(sha) {
		return failed("not found")
	}
	raw, err := d.gl.reads.RawFile(ctx, b.Conn(), strconv.FormatInt(repo.ProjectID, 10), path, sha, domain.DocFetchMaxBytes)
	if err != nil {
		return failed("not found")
	}
	return read(raw)
}

func failed(reason string) app.DocRead { return app.DocRead{Reason: reason} }

// read decodes the bytes and cuts the text to DocFetchMaxBytes UTF-16 units.
func read(raw []byte) app.DocRead {
	text := repointel.DecodeText(raw)
	if reviewer.Len16(text) > domain.DocFetchMaxBytes {
		return app.DocRead{OK: true, Content: reviewer.PrefixUnits(text, domain.DocFetchMaxBytes), Truncated: true}
	}
	return app.DocRead{OK: true, Content: text}
}

// safeDocPath rejects an empty path, an absolute one, one that starts with "-", one with "..", a
// backslash or NUL anywhere, and one with a ".git" segment.
func safeDocPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "-") ||
		strings.Contains(path, "..") || strings.Contains(path, `\`) || strings.Contains(path, "\x00") {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".git" {
			return false
		}
	}
	return true
}
