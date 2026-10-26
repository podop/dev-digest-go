// Package domain holds the repository entity and the GitHub/GitLab URL rules of the repos
// context. It depends on the standard library and the stdlib-only platform/codehost only.
package domain

import (
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/codehost"
)

// Sentinel errors of the repos context.
var (
	// ErrNotFound means no repo matches the id (or it belongs to another workspace).
	ErrNotFound = errors.New("repo not found")
	// ErrInvalidURL means the text is not an accepted GitHub repository URL.
	ErrInvalidURL = errors.New("not a GitHub or GitLab repository URL")
)

// InvalidURLMessage is the validation message the API shows for a rejected URL.
const InvalidURLMessage = "Must be a GitHub or GitLab repository URL, " +
	"e.g. https://github.com/owner/repo or https://gitlab.com/group/project"

// DefaultBranch is the branch a repo starts with, until a refresh learns the remote's.
const DefaultBranch = "main"

// Repo is a GitHub repository tracked in a workspace.
type Repo struct {
	ID            string
	WorkspaceID   string
	Owner         string
	Name          string
	FullName      string
	DefaultBranch string
	// ClonePath is the absolute clone directory, nil until a clone job succeeded.
	ClonePath    *string
	LastPolledAt *time.Time
	CreatedBy    *string
	// Provider is codehost.GitHub or codehost.GitLab; "" counts as GitHub (rows from before
	// the GitLab columns).
	Provider string
	// Host is the host key (codehost.HostKey): "github.com" for GitHub, the connection's host
	// key for GitLab.
	Host string
	// ProjectID is the GitLab project id (0 for GitHub).
	ProjectID int64
	// WebURL is the stored browser address of a GitLab repo ("" for GitHub, whose address is
	// derived).
	WebURL string
	// ConnectionID is the id of the GitLab connection whose host key equals Host, read with the
	// repo; nil for GitHub and for a not-connected GitLab repo (FR16, FR48).
	ConnectionID *string
}

// Cloned reports whether the repo has a clone on disk.
func (r Repo) Cloned() bool { return r.ClonePath != nil }

// Ref identifies a GitHub repository by owner and name.
type Ref struct {
	Owner string
	Name  string
}

// FullName returns "owner/name".
func (r Ref) FullName() string { return r.Owner + "/" + r.Name }

// CloneURL returns the HTTPS clone URL on base (e.g. "https://github.com"); every accepted
// URL form, SSH included, is cloned over HTTPS.
func (r Ref) CloneURL(base string) string {
	return strings.TrimRight(base, "/") + "/" + r.Owner + "/" + r.Name + ".git"
}

// githubURLPattern accepts https://github.com/<owner>/<repo>[.git][/] and
// git@github.com:<owner>/<repo>[.git]; the segment charset matches the clone-path rule.
var githubURLPattern = regexp.MustCompile(
	`^(?:https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?|` +
		`git@github\.com:([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?)$`)

// Path limits. A GitLab project path has 2 to MaxProjectSegments segments after the
// connection's path prefix (FR12); ParseURL, which does not know the prefix, only bounds the
// whole path by maxURLSegments and WithoutPrefix applies the exact limit.
const (
	MaxProjectSegments = codehost.MaxProjectSegments
	maxURLSegments     = 40
	maxURLLength       = 2048
)

// ErrOutsidePrefix means an HTTPS GitLab path does not start with the connection's path prefix.
var ErrOutsidePrefix = errors.New("url is outside the connection's base url")

// Parsed is an accepted repository URL.
type Parsed struct {
	// Provider is codehost.GitHub or codehost.GitLab.
	Provider string
	// Host is the host key (codehost.HostKey): the lower-cased host name, plus ":port" for an
	// HTTPS port other than 443; SSH forms carry the host name only.
	Host string
	// SSH is true for the git@host:path form.
	SSH bool
	// Path is the repository path without ".git" and trailing "/": "owner/repo" for GitHub,
	// for GitLab everything after the host, which still starts with the connection's path
	// prefix (see WithoutPrefix).
	Path string
	// Owner is everything before the last segment of Path, Name the last segment.
	Owner string
	Name  string
}

// Ref returns the owner and name as a Ref.
func (p Parsed) Ref() Ref { return Ref{Owner: p.Owner, Name: p.Name} }

// WithoutPrefix removes the connection's path prefix ("" or e.g. "gitlab" or "/gitlab/") from
// an HTTPS GitLab path and re-derives Owner and Name. A path outside the prefix wraps
// ErrOutsidePrefix; a remaining path of fewer than 2 or more than MaxProjectSegments segments
// wraps ErrInvalidURL. SSH and GitHub paths carry no prefix and are only length-checked.
func (p Parsed) WithoutPrefix(prefix string) (Parsed, error) {
	path := p.Path
	if p.Provider == codehost.GitLab && !p.SSH {
		if pre := strings.Trim(prefix, "/"); pre != "" {
			rest, ok := strings.CutPrefix(path, pre+"/")
			if !ok {
				return Parsed{}, ErrOutsidePrefix
			}
			path = rest
		}
	}
	segs := strings.Split(path, "/")
	if len(segs) < 2 || len(segs) > MaxProjectSegments {
		return Parsed{}, ErrInvalidURL
	}
	p.Path = path
	p.Owner, p.Name = splitPath(path)
	return p, nil
}

// ParseURL parses a GitHub or GitLab repository URL after trimming whitespace.
//
// GitHub forms (https://github.com/o/r[.git][/], git@github.com:o/r[.git]) give the Phase 1
// result. Any other host gives a GitLab result from https://<host>[:<port>]/<path>[.git][/]
// or git@<host>:<path>[.git]; every path segment follows the clone-path rule, so nested
// groups work and GitLab's "/-/..." deep links are refused. http://, ssh://, userinfo, query,
// fragment, percent-escapes and the hosts www.github.com / api.github.com / github.com with a
// port wrap ErrInvalidURL.
func ParseURL(raw string) (Parsed, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLLength {
		return Parsed{}, ErrInvalidURL
	}
	if m := githubURLPattern.FindStringSubmatch(raw); m != nil {
		owner, name := m[1], m[2]
		if owner == "" {
			owner, name = m[3], m[4]
		}
		if !safeSegment(owner) || !safeSegment(name) {
			return Parsed{}, ErrInvalidURL
		}
		return Parsed{
			Provider: codehost.GitHub, Host: codehost.GitHubHost, SSH: strings.HasPrefix(raw, "git@"),
			Path: owner + "/" + name, Owner: owner, Name: name,
		}, nil
	}
	var host, port, path string
	ssh := false
	switch {
	case strings.HasPrefix(raw, "https://"):
		authority, rest, _ := strings.Cut(strings.TrimPrefix(raw, "https://"), "/")
		var ok bool
		if host, port, ok = splitAuthority(authority); !ok {
			return Parsed{}, ErrInvalidURL
		}
		path = strings.TrimSuffix(rest, "/")
	case strings.HasPrefix(raw, "git@"):
		var ok bool
		hostPart, rest, found := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !found || strings.HasPrefix(hostPart, "[") {
			return Parsed{}, ErrInvalidURL
		}
		if host, port, ok = splitAuthority(hostPart); !ok || port != "" {
			return Parsed{}, ErrInvalidURL
		}
		ssh, path = true, rest
	default:
		return Parsed{}, ErrInvalidURL
	}
	switch strings.TrimSuffix(strings.ToLower(host), ".") {
	case "github.com", "www.github.com", "api.github.com":
		// github.com has exactly the forms of githubURLPattern; the others are never a repo host.
		return Parsed{}, ErrInvalidURL
	}
	path = strings.TrimSuffix(path, ".git")
	segs := strings.Split(path, "/")
	if len(segs) < 2 || len(segs) > maxURLSegments {
		return Parsed{}, ErrInvalidURL
	}
	for _, seg := range segs {
		if !segmentPattern.MatchString(seg) || !safeSegment(seg) {
			return Parsed{}, ErrInvalidURL
		}
	}
	owner, name := splitPath(path)
	return Parsed{
		Provider: codehost.GitLab, Host: codehost.HostKey(host, port), SSH: ssh,
		Path: path, Owner: owner, Name: name,
	}, nil
}

var (
	segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	hostPattern    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*\.?$`)
)

// splitAuthority splits "host[:port]" and validates both: a DNS name, an IPv4 address or a
// bracketed IPv6 literal; the port is canonical decimal 1-65535. The host is not lower-cased.
func splitAuthority(a string) (host, port string, ok bool) {
	if strings.HasPrefix(a, "[") {
		end := strings.Index(a, "]")
		if end < 0 {
			return "", "", false
		}
		addr, err := netip.ParseAddr(a[1:end])
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", "", false
		}
		host, a = a[:end+1], a[end+1:]
		if a == "" {
			return host, "", true
		}
		if !strings.HasPrefix(a, ":") {
			return "", "", false
		}
		port = a[1:]
	} else {
		host, port, _ = strings.Cut(a, ":")
		if len(host) > 253 || !hostPattern.MatchString(host) {
			return "", "", false
		}
		if !strings.Contains(a, ":") {
			return host, "", true
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", "", false
	}
	return host, port, true
}

// splitPath returns everything before the last "/" and the part after it.
func splitPath(path string) (owner, name string) {
	i := strings.LastIndex(path, "/")
	return path[:i], path[i+1:]
}

func safeSegment(s string) bool {
	switch strings.ToLower(s) {
	case "", ".", "..", ".git":
		return false
	}
	return !strings.HasPrefix(s, "-")
}
