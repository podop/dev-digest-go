package custom

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// MaxBaseURLLen is the longest base URL.
const MaxBaseURLLen = 2048

// CheckBaseURL validates and normalizes the base URL of a custom provider (spec FR3).
//
// On success msg is "" and norm is the URL to store and call: lower-case scheme and host, the
// path as given without trailing slashes, and an IPv4 host spelled as decimal, octal, hex or a
// single number rewritten to dotted decimal. plainHTTP is true when the scheme is http and the
// host is not loopback (localhost, 127.0.0.0/8, ::1): the key then travels unencrypted (D8).
//
// On failure norm is "" and msg is the 422 message. Refused: other schemes than http and
// https; no host; userinfo, query or fragment; more than 2,048 characters; whitespace, control
// characters or backslashes; non-ASCII hosts; zone ids; an invalid port; and a literal host that
// is a forbidden address (see ForbiddenAddr), however it is spelled (IPv4-mapped IPv6,
// decimal/octal/hex IPv4, trailing dot, brackets). A host name is accepted here and checked
// again when the call connects.
func CheckBaseURL(raw string) (norm string, plainHTTP bool, msg string) {
	if len(raw) > MaxBaseURLLen {
		return "", false, "Base URL must be at most 2048 characters"
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c <= 0x20 || c == 0x7f || c == '\\' || c >= 0x80 {
			return "", false, "Base URL contains an invalid character"
		}
	}
	if strings.ContainsAny(raw, "?#") {
		return "", false, "Base URL must not contain a query or fragment"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return "", false, "Base URL is not a valid URL"
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false, "Base URL must start with http:// or https://"
	}
	if u.User != nil || strings.Contains(authorityOf(raw), "@") {
		return "", false, "Base URL must not contain credentials"
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", false, "Base URL must contain a host"
	}

	host, port, hostMsg := splitHostPort(u)
	if hostMsg != "" {
		return "", false, hostMsg
	}
	canon, addr, isIP, hostMsg := canonicalHost(host)
	if hostMsg != "" {
		return "", false, hostMsg
	}
	if isIP && ForbiddenAddr(addr) {
		return "", false, "Base URL must not be a link-local or unspecified address"
	}

	authority := canon
	if port != "" {
		authority += ":" + port
	}
	norm = scheme + "://" + authority + strings.TrimRight(u.EscapedPath(), "/")
	plainHTTP = scheme == "http" && !loopbackHost(canon, addr, isIP)
	return norm, plainHTTP, ""
}

// authorityOf returns the part of raw between "://" and the next "/". raw has no ? or # here.
func authorityOf(raw string) string {
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	authority, _, _ := strings.Cut(rest, "/")
	return authority
}

// splitHostPort returns the lower-cased host (without brackets for IPv6) and the port ("" when
// absent).
func splitHostPort(u *url.URL) (host, port, msg string) {
	if strings.HasSuffix(u.Host, ":") {
		return "", "", "Base URL port is invalid"
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", "", "Base URL port is invalid"
		}
		port = strconv.Itoa(n)
	}
	host = strings.ToLower(u.Hostname())
	if strings.Contains(host, "%") {
		return "", "", "Base URL host must not contain a zone id or escapes"
	}
	return host, port, ""
}

// canonicalHost validates host and returns its normalized spelling (IPv6 in brackets). For a
// literal address it also returns the parsed value.
func canonicalHost(host string) (canon string, addr netip.Addr, isIP bool, msg string) {
	if strings.Contains(host, ":") {
		a, err := netip.ParseAddr(host)
		if err != nil || !a.Is6() {
			return "", netip.Addr{}, false, "Base URL host is not a valid IPv6 address"
		}
		return "[" + a.String() + "]", a, true, ""
	}
	name := strings.TrimSuffix(host, ".")
	if name == "" || len(name) > 253 {
		return "", netip.Addr{}, false, "Base URL host is not valid"
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			return "", netip.Addr{}, false, "Base URL host is not valid"
		}
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if l == "" {
			return "", netip.Addr{}, false, "Base URL host is not valid"
		}
	}
	if a, err := netip.ParseAddr(name); err == nil {
		return a.String(), a, true, ""
	}
	if numericLabel(labels[len(labels)-1]) {
		a, ok := parseLegacyIPv4(name)
		if !ok {
			return "", netip.Addr{}, false, "Base URL host is not a valid IPv4 address"
		}
		return a.String(), a, true, ""
	}
	return name, netip.Addr{}, false, ""
}

func loopbackHost(canon string, addr netip.Addr, isIP bool) bool {
	if isIP {
		return addr.Unmap().IsLoopback()
	}
	return canon == "localhost"
}
