package domain

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"dev-digest-go/backend/internal/llm/custom"
)

// Lengths of the fields of a connection (spec FR1, FR4). Name and token are counted in UTF-16
// code units, which is how the UI counts.
const (
	MaxNameLen  = 80
	MaxTokenLen = 4096
)

// MsgAPIURL is the message of a base URL that ends in /api/v4 (spec FR2).
const MsgAPIURL = "Use the GitLab instance URL, not its API URL"

// githubHosts are the host names that always belong to GitHub (spec FR2); a connection for one of
// them is refused whatever the port.
var githubHosts = map[string]bool{
	"github.com":     true,
	"www.github.com": true,
	"api.github.com": true,
}

// CheckBaseURL validates and normalizes the base URL of a connection (spec FR2, EC1, EC2).
//
// On success msg is "", norm is the URL to store and call (lower-case scheme and host, port 443
// removed, no trailing slash, the path prefix as given) and host is the host key: the lower-case
// host name, plus ":<port>" when the port is not 443 (an IPv6 literal keeps its brackets). On
// failure norm and host are "" and msg is the 422 message for the path ["base_url"].
//
// It applies the address rules of custom model providers (custom.CheckBaseURL: no userinfo, query
// or fragment, at most 2,048 characters, no whitespace or non-ASCII, legacy IPv4 spellings and
// IPv4-in-IPv6 decoded, link-local and unspecified literals refused) and adds: https only; no path
// ending in /api/v4; no empty, "." or ".." path segment and no encoded slash; and the GitHub
// hosts (github.com, www.github.com, api.github.com) refused. A host name is checked again when a
// call connects (the dial guard). Loopback and private addresses are allowed. The input is not
// trimmed.
func CheckBaseURL(raw string) (norm, host, msg string) {
	if raw == "" {
		return "", "", "Base URL is required"
	}
	if len(raw) < len("https://") || !strings.EqualFold(raw[:len("https://")], "https://") {
		return "", "", "Base URL must start with https://"
	}
	n, plainHTTP, m := custom.CheckBaseURL(raw)
	if m != "" {
		return "", "", m
	}
	if plainHTTP {
		// Unreachable for an https URL; kept so a change in custom cannot let http through.
		return "", "", "Base URL must start with https://"
	}
	u, err := url.Parse(n)
	if err != nil || u.Scheme != "https" {
		return "", "", "Base URL must start with https://"
	}
	hostname := u.Hostname()
	if githubHosts[hostname] {
		return "", "", "Base URL must be a GitLab instance, not GitHub"
	}
	if msg := checkPath(u.EscapedPath()); msg != "" {
		return "", "", msg
	}
	key := u.Host
	if u.Port() == "443" {
		key = strings.TrimSuffix(key, ":443")
	}
	return "https://" + key + u.EscapedPath(), key, ""
}

// checkPath validates the path prefix of a base URL (already without trailing slashes).
func checkPath(escaped string) string {
	if escaped == "" {
		return ""
	}
	segments := strings.Split(escaped[1:], "/")
	for _, s := range segments {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" || d == "." || d == ".." || strings.ContainsAny(d, "/\\") ||
			strings.IndexFunc(d, unicode.IsControl) >= 0 {
			return "Base URL path is not valid"
		}
	}
	n := len(segments)
	if n >= 2 && strings.EqualFold(segments[n-2], "api") && strings.EqualFold(segments[n-1], "v4") {
		return MsgAPIURL
	}
	return ""
}

// CheckName validates the display name of a connection (spec FR1) and returns it trimmed. An
// empty name (after trimming) becomes the host key, which must then fit the length limit.
// host is the host key from CheckBaseURL.
func CheckName(raw, host string) (name, msg string) {
	name = strings.TrimFunc(raw, func(r rune) bool {
		return r == 0xFEFF || (r != '\u0085' && unicode.IsSpace(r))
	})
	if name == "" {
		name = host
		if name == "" {
			return "", "Name is required"
		}
		if utf16Len(name) > MaxNameLen {
			return "", fmt.Sprintf("Name is required: the host is longer than %d characters", MaxNameLen)
		}
		return name, ""
	}
	switch {
	case !utf8.ValidString(name):
		return "", "Name must be valid UTF-8"
	case utf16Len(name) > MaxNameLen:
		return "", fmt.Sprintf("Name must be at most %d characters", MaxNameLen)
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "", "Name must not contain control characters"
	}
	return name, ""
}

// CheckToken validates an access token (spec FR4): 1-4,096 characters, no whitespace or control
// characters. It returns "" when the token is acceptable, else the 422 message. The message never
// contains the token. The token is not trimmed: a token with a space is refused.
func CheckToken(raw string) string {
	switch n := utf16Len(raw); {
	case n < 1:
		return "Token must not be empty"
	case n > MaxTokenLen:
		return fmt.Sprintf("Token must be at most %d characters", MaxTokenLen)
	case !utf8.ValidString(raw):
		return "Token must be valid UTF-8"
	case strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == 0xFEFF }) >= 0:
		return "Token must not contain whitespace or control characters"
	}
	return ""
}

// utf16Len is the length of s in UTF-16 code units, which is what JavaScript's String.length counts.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
