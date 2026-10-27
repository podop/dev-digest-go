package domain

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"dev-digest-go/backend/internal/platform/codehost"
)

// MaxGitLabRefSplits is the most ref/file splits tried for one GitLab blob link (spec D28).
const MaxGitLabRefSplits = 5

// GitLabFileLink is a GitLab blob or raw link broken into its project and the segments that
// follow "/-/blob/" or "/-/raw/". Which of those segments are the ref and which the file path
// cannot be told from the URL (a ref may contain "/"), see Splits.
type GitLabFileLink struct {
	// Project is the decoded project path without the connection's path prefix: 2 to
	// codehost.MaxProjectSegments segments.
	Project string
	// Rest holds the decoded segments after blob/raw: at least a ref segment and a file segment,
	// none empty, none containing "/".
	Rest []string
}

// GitLabFileSplit is one reading of a link: Ref is the first k segments of Rest joined by "/",
// File the remaining ones.
type GitLabFileSplit struct {
	Ref  string
	File string
}

// Splits returns the readings to try, shortest ref first: ref = the first k segments for
// k = 1, 2, … up to MaxGitLabRefSplits and always leaving at least one segment for the file.
func (l GitLabFileLink) Splits() []GitLabFileSplit {
	n := min(MaxGitLabRefSplits, len(l.Rest)-1)
	out := make([]GitLabFileSplit, 0, max(n, 0))
	for k := 1; k <= n; k++ {
		out = append(out, GitLabFileSplit{
			Ref:  strings.Join(l.Rest[:k], "/"),
			File: strings.Join(l.Rest[k:], "/"),
		})
	}
	return out
}

// GitLabFileLinkMessage is the 422 invalid_import message of a URL on a connection's host that
// is not a blob or raw file link (spec FR30).
func GitLabFileLinkMessage(hostKey string) string {
	return "Only GitLab blob or raw file links can be imported from " + hostKey
}

// ImportHostKey returns the host key (lower-cased host name without a trailing dot, plus ":<port>"
// unless the port is 443; see codehost.HostKey) of an https import URL, so the importer can look
// up a GitLab connection before ParseImportURL runs. ok is false for anything that is not an
// https URL with a parsable host and port: such a URL never matches a connection and
// ParseImportURL reports what is wrong with it. Nothing is checked about the address.
func ImportHostKey(raw string) (key string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	return urlHostKey(u)
}

func urlHostKey(u *url.URL) (string, bool) {
	if !strings.EqualFold(u.Scheme, "https") {
		return "", false
	}
	hostname := u.Hostname()
	if hostname == "" {
		return "", false
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	host, err := ParseHost(hostname)
	if err != nil {
		return "", false
	}
	port := u.Port()
	if port != "" {
		if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
			return "", false
		}
	}
	return codehost.HostKey(host.URLHost(), port), true
}

// ParseGitLabFileURL reads raw as a file link of the GitLab connection whose normalized base
// URL is baseURL ("https://host[:port][/prefix]"; the host key it yields is the connection's).
//
// matched is false when raw is not an https URL of that host key under that path prefix; the
// importer then treats raw like any other URL (ParseImportURL). When matched is true the link
// belongs to the connection and the connection's address rules apply, not the public-address
// rule of ParseImportURL: this function never inspects the address or the port. A matched URL
// that is not an accepted form returns an ImportError (nothing may be sent for it): credentials
// in the URL, a path without "/-/blob/" or "/-/raw/" after a 2 to 21 segment project path
// ("/-/tree/…", "/-/snippets/…", a bare project), or a blob link without a file. The query
// ("ref_type=heads") and the fragment are ignored.
//
//	<base_url>/<project path>/-/blob/<ref…>/<file>
//	<base_url>/<project path>/-/raw/<ref…>/<file>
func ParseGitLabFileURL(raw, baseURL string) (link GitLabFileLink, matched bool, err error) {
	u, perr := url.Parse(strings.TrimSpace(raw))
	if perr != nil {
		return GitLabFileLink{}, false, nil
	}
	key, ok := urlHostKey(u)
	if !ok {
		return GitLabFileLink{}, false, nil
	}
	base, perr := url.Parse(baseURL)
	if perr != nil {
		return GitLabFileLink{}, false, nil
	}
	baseKey, ok := urlHostKey(base)
	if !ok || baseKey != key {
		return GitLabFileLink{}, false, nil
	}
	prefix, ok := splitEscapedPath(base.EscapedPath())
	if !ok {
		return GitLabFileLink{}, false, nil
	}
	// The prefix is compared segment by segment on the decoded text; a segment that does not
	// decode cannot equal a prefix segment. The segments after it must all decode.
	parts := rawSegments(u.EscapedPath())
	if len(parts) < len(prefix) {
		return GitLabFileLink{}, false, nil
	}
	for i, p := range prefix {
		if d, derr := decodeSegment(parts[i]); derr != nil || d != p {
			return GitLabFileLink{}, false, nil
		}
	}
	// From here the URL is the connection's: every failure refuses it.
	if u.User != nil {
		return GitLabFileLink{}, true, InvalidImport("URLs with credentials are not allowed")
	}
	reject := InvalidImport(GitLabFileLinkMessage(key))
	segs, ok := splitEscapedPath("/" + strings.Join(parts[len(prefix):], "/"))
	if !ok {
		return GitLabFileLink{}, true, reject
	}
	dash := -1
	for i, s := range segs {
		if s == "-" {
			dash = i
			break
		}
	}
	if dash < 2 || dash > codehost.MaxProjectSegments || len(segs) < dash+2 {
		return GitLabFileLink{}, true, reject
	}
	if kind := segs[dash+1]; kind != "blob" && kind != "raw" {
		return GitLabFileLink{}, true, reject
	}
	for _, s := range segs[:dash] {
		if !gitLabProjectSegment(s) {
			return GitLabFileLink{}, true, reject
		}
	}
	rest := segs[dash+2:]
	if len(rest) < 2 {
		return GitLabFileLink{}, true, reject
	}
	for _, s := range rest {
		if !gitLabPathSegment(s) {
			return GitLabFileLink{}, true, reject
		}
	}
	return GitLabFileLink{Project: strings.Join(segs[:dash], "/"), Rest: rest}, true, nil
}

// rawSegments splits an escaped URL path at "/" without decoding; a trailing "/" yields a last
// empty segment and the empty path no segment.
func rawSegments(escaped string) []string {
	escaped = strings.TrimPrefix(escaped, "/")
	if escaped == "" {
		return nil
	}
	return strings.Split(escaped, "/")
}

// decodeSegment percent-decodes one path segment. It fails for a bad escape and for a segment
// that decodes to something containing "/" or "\" or a control character: an encoded slash must
// not change how a path is split.
func decodeSegment(p string) (string, error) {
	d, err := url.PathUnescape(p)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(d, "/\\") || strings.IndexFunc(d, unicode.IsControl) >= 0 {
		return "", errUnsafeSegment
	}
	return d, nil
}

var errUnsafeSegment = errors.New("unsafe path segment")

// splitEscapedPath returns the decoded segments of an escaped URL path (see decodeSegment); ok is
// false when any segment fails to decode.
func splitEscapedPath(escaped string) ([]string, bool) {
	parts := rawSegments(escaped)
	out := make([]string, len(parts))
	for i, p := range parts {
		d, err := decodeSegment(p)
		if err != nil {
			return nil, false
		}
		out[i] = d
	}
	return out, true
}

// gitLabProjectSegment is the clone-path segment rule for the groups and the project name:
// letters, digits, "_", "." and "-", not "." / ".." / ".git" and not starting with "-".
func gitLabProjectSegment(s string) bool {
	switch strings.ToLower(s) {
	case "", ".", "..", ".git":
		return false
	}
	if s[0] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// gitLabPathSegment accepts a ref or file segment: non-empty and not a dot segment.
func gitLabPathSegment(s string) bool { return s != "" && s != "." && s != ".." }
