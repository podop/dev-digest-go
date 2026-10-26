// Package codehost holds the pure naming rules shared by the code hosts the app talks to
// (GitHub, GitLab): provider names, host keys, clone-directory keys and web addresses. It
// imports the standard library only, so domain packages and adapters may both use it.
package codehost

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// Provider names, as stored in repos.provider and sent as the API's `provider` field.
const (
	GitHub = "github"
	GitLab = "gitlab"
)

// GitHubHost is the host key of github.com, the only GitHub host.
const GitHubHost = "github.com"

// GitLabCloneRoot is the top-level directory under the clone root that holds GitLab clones.
// Its first character cannot start a GitHub owner name ("@" is not in a GitHub login), so it
// never collides with a GitHub clone.
const GitLabCloneRoot = "@gitlab"

// HostKey returns the identity of a code host: the lower-cased host name without a trailing
// dot, plus ":<port>" when the port is set and is not 443. Pass an empty port for SSH forms,
// which identify a host by name only. An IPv6 literal is bracketed and written in its canonical
// compressed form ("[::1]:8443"). It returns what gitlabconn/domain.CheckBaseURL returns as the
// host key of a connection, so a repository URL binds to the connection of the same host.
func HostKey(host, port string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if inner := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"); strings.Contains(inner, ":") {
		if addr, err := netip.ParseAddr(inner); err == nil {
			inner = addr.String()
		}
		host = "[" + inner + "]"
	}
	if port == "" {
		return host
	}
	if n, err := strconv.Atoi(port); err == nil {
		if n == 443 {
			return host
		}
		port = strconv.Itoa(n)
	}
	return host + ":" + port
}

// CloneKey returns the two directory names a clone lives under: <cloneRoot>/<first>/<second>.
// GitHub keeps (owner, name) unchanged. GitLab derives the location from the host key and the
// numeric project id only, never from the project path, so a renamed or moved project keeps
// its clone: ("@gitlab/" + hostKey with ':' replaced by '_', "<id>"); host is the host key. An
// IPv6 literal is written without "[" and with "]" as "-" ("[::1]:8443" = "__1-_8443"): the
// result is a valid clone-path segment, and unambiguous because DNS names contain no "_" and an
// address contains no "-".
func CloneKey(prov, host string, id int64, owner, name string) (first, second string) {
	if prov != GitLab {
		return owner, name
	}
	dir := strings.NewReplacer(":", "_", "[", "", "]", "-").Replace(strings.ToLower(host))
	return GitLabCloneRoot + "/" + dir, strconv.FormatInt(id, 10)
}

// WebURL returns the browser address of a repository: base (no trailing slash) + "/" + the
// full path, e.g. WebURL("https://gitlab.com", "acme/web") = "https://gitlab.com/acme/web".
func WebURL(base, full string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(full, "/")
}

// MaxProjectSegments is the most segments a GitLab project path may have (groups, subgroups,
// project).
const MaxProjectSegments = 21

// ErrInvalidProjectPath means a GitLab project path is not a plain project path, or contradicts
// the path and namespace it came with. The error holds no path text.
var ErrInvalidProjectPath = errors.New("not a plain gitlab project path")

// ProjectRef checks a project's own answer and returns its owner (the namespace path) and name.
// full is path_with_namespace, path the last segment (the answer's "path") and namespace the
// namespace's full path ("" when the answer has none). It refuses a path that is not a plain GitLab
// path (2 to MaxProjectSegments segments of letters, digits, "_", "." and "-"; no empty, "." , "..",
// ".git" or "-"-leading segment; no ".git" suffix; no "@", "%", whitespace or non-ASCII text) and
// one that contradicts its own parts: the path ends up in a clone URL, in the database and in
// links, and a server is only as trustworthy as its connection. Both adding a repository and
// following a rename apply it.
func ProjectRef(full, path, namespace string) (owner, name string, err error) {
	if len(full) > maxProjectPathLen {
		return "", "", ErrInvalidProjectPath
	}
	segs := strings.Split(full, "/")
	if len(segs) < 2 || len(segs) > MaxProjectSegments {
		return "", "", ErrInvalidProjectPath
	}
	for _, seg := range segs {
		if !safeSegment(seg) {
			return "", "", ErrInvalidProjectPath
		}
	}
	if strings.HasSuffix(full, ".git") {
		return "", "", ErrInvalidProjectPath
	}
	i := strings.LastIndex(full, "/")
	owner, name = full[:i], full[i+1:]
	if path != name || (namespace != "" && namespace != owner) {
		return "", "", ErrInvalidProjectPath
	}
	return owner, name, nil
}

const maxProjectPathLen = 2048

// safeSegment reports whether s is one plain path segment: ASCII letters, digits, "_", "." and
// "-", not "." or "..", not ".git", not starting with "-".
func safeSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "-") || strings.EqualFold(s, ".git") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			return false
		}
	}
	return true
}
