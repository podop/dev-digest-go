// Package codehost holds the pure naming rules shared by the code hosts the app talks to
// (GitHub, GitLab): provider names, host keys, clone-directory keys and web addresses. It
// imports the standard library only, so domain packages and adapters may both use it.
package codehost

import (
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
