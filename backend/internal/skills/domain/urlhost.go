package domain

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

// Limits of a URL import.
const (
	// URLImportTimeout bounds one URL import end to end.
	URLImportTimeout = 10 * time.Second
	// URLImportMaxBytes is the largest document that is downloaded.
	URLImportMaxBytes = 1024 * 1024
	// URLImportMaxRedirects is the most redirects that are followed.
	URLImportMaxRedirects = 3
	// URLImportUserAgent and URLImportAccept are the request headers of a URL import.
	URLImportUserAgent = "DevDigest-skill-import"
	URLImportAccept    = "text/markdown, text/plain, application/zip, */*"
)

// FetchedDocument is a downloaded import document.
type FetchedDocument struct {
	Bytes       []byte
	ContentType string
	// FinalURL is the URL of the last response, after redirects.
	FinalURL *url.URL
}

// ErrInvalidHost means a host does not parse as a browser would parse it.
var ErrInvalidHost = errors.New("invalid host")

var githubBlobRE = regexp.MustCompile(`^/([^/]+)/([^/]+)/blob/(.+)$`)

// hostIDNA converts a domain to ASCII like the WHATWG URL standard does: UTS #46 non-transitional
// mapping, no STD3 rules, no hyphen or length checks.
var hostIDNA = idna.New(
	idna.MapForLookup(),
	idna.StrictDomainName(false),
	idna.VerifyDNSLength(false),
	idna.CheckHyphens(false),
	idna.CheckJoiners(true),
	idna.BidiRule(),
	idna.Transitional(false),
)

// Host is a normalized URL host.
type Host struct {
	// Name is the host as text: lowercase ASCII for a domain, dotted decimal for IPv4, compressed
	// hexadecimal (never with an embedded dotted IPv4) for IPv6, without brackets.
	Name string
	// Addr is the address when the host is an IP literal, else the zero Addr.
	Addr netip.Addr
}

// IsIP reports whether the host is an IP literal.
func (h Host) IsIP() bool { return h.Addr.IsValid() }

// URLHost is the host as it is written in a URL: IPv6 in brackets.
func (h Host) URLHost() string {
	if h.Addr.Is6() {
		return "[" + h.Name + "]"
	}
	return h.Name
}

// ParseHost normalizes a host the way a browser does: lowercase, IDN to ASCII, IPv4 written in
// decimal, hex, octal or with fewer than four parts converted to dotted decimal, IPv6 compressed.
// raw is a host without port; an IPv6 literal is in brackets.
func ParseHost(raw string) (Host, error) {
	if strings.HasPrefix(raw, "[") {
		if !strings.HasSuffix(raw, "]") {
			return Host{}, ErrInvalidHost
		}
		addr, err := netip.ParseAddr(raw[1 : len(raw)-1])
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return Host{}, ErrInvalidHost
		}
		return Host{Name: formatIPv6(addr), Addr: addr}, nil
	}
	if raw == "" {
		return Host{}, ErrInvalidHost
	}
	domain, err := hostIDNA.ToASCII(raw)
	if err != nil || domain == "" {
		return Host{}, ErrInvalidHost
	}
	domain = strings.ToLower(domain)
	for _, r := range domain {
		if isForbiddenHostRune(r) {
			return Host{}, ErrInvalidHost
		}
	}
	if endsInNumber(domain) {
		addr, ok := parseIPv4Host(domain)
		if !ok {
			return Host{}, ErrInvalidHost
		}
		return Host{Name: addr.String(), Addr: addr}, nil
	}
	return Host{Name: domain}, nil
}

// isForbiddenHostRune lists the WHATWG forbidden domain code points.
func isForbiddenHostRune(r rune) bool {
	if r <= 0x20 || r == 0x7F {
		return true
	}
	return strings.ContainsRune("#/:<>?@[\\]^|%", r)
}

// endsInNumber is the WHATWG "ends in a number" check: such a host must parse as IPv4.
func endsInNumber(domain string) bool {
	parts := strings.Split(domain, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := parseIPv4Number(last)
	return ok
}

// parseIPv4Number parses one IPv4 part: decimal, 0x hexadecimal or leading-zero octal. A value
// too large for 64 bits is MaxUint64 (it is out of range for any part either way).
func parseIPv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	radix := 10
	switch {
	case len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X"):
		s, radix = s[2:], 16
	case len(s) >= 2 && s[0] == '0':
		s, radix = s[1:], 8
	}
	if s == "" {
		return 0, true
	}
	n, err := strconv.ParseUint(s, radix, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return math.MaxUint64, true
		}
		return 0, false
	}
	return n, true
}

// parseIPv4Host is the WHATWG IPv4 parser for a host that ends in a number.
func parseIPv4Host(domain string) (netip.Addr, bool) {
	parts := strings.Split(domain, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := parseIPv4Number(p)
		if !ok {
			return netip.Addr{}, false
		}
		nums[i] = n
	}
	for _, n := range nums[:len(nums)-1] {
		if n > 255 {
			return netip.Addr{}, false
		}
	}
	last := nums[len(nums)-1]
	if last >= 1<<(8*(5-len(nums))) {
		return netip.Addr{}, false
	}
	ipv4 := last
	for i, n := range nums[:len(nums)-1] {
		ipv4 += n << (8 * (3 - i))
	}
	return netip.AddrFrom4([4]byte{byte(ipv4 >> 24), byte(ipv4 >> 16), byte(ipv4 >> 8), byte(ipv4)}), true
}

// formatIPv6 writes the address as the URL standard does: lowercase hexadecimal groups, the first
// longest run of two or more zero groups compressed, no embedded dotted IPv4.
func formatIPv6(a netip.Addr) string {
	b := a.As16()
	var g [8]uint16
	for i := range g {
		g[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	bestStart, bestLen := -1, 1
	for i := 0; i < 8; {
		if g[i] != 0 {
			i++
			continue
		}
		j := i
		for j < 8 && g[j] == 0 {
			j++
		}
		if j-i > bestLen {
			bestStart, bestLen = i, j-i
		}
		i = j
	}
	var sb strings.Builder
	for i := 0; i < 8; i++ {
		if i == bestStart {
			sb.WriteString("::")
			i += bestLen - 1
			continue
		}
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), ":") {
			sb.WriteByte(':')
		}
		sb.WriteString(strconv.FormatUint(uint64(g[i]), 16))
	}
	return sb.String()
}

// IsPublicAddress reports whether a is a globally routable unicast address. IPv4-mapped, NAT64
// (64:ff9b::/96) and 6to4 (2002::/16) addresses are judged by the IPv4 address they embed; of the
// rest of IPv6 only 2000::/3 outside 2001:db8::/32 and 2001::/32 is public.
func IsPublicAddress(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.WithZone("")
	if a.Is4In6() {
		a = a.Unmap()
	}
	if a.Is4() {
		return publicIPv4(a.As4())
	}
	b := a.As16()
	switch {
	case b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b && allZero(b[4:12]):
		return publicIPv4([4]byte(b[12:16]))
	case b[0] == 0x20 && b[1] == 0x02:
		return publicIPv4([4]byte(b[2:6]))
	case b[0]&0xe0 != 0x20:
		return false
	case b[0] == 0x20 && b[1] == 0x01 && b[2] == 0x0d && b[3] == 0xb8:
		return false
	case b[0] == 0x20 && b[1] == 0x01 && b[2] == 0 && b[3] == 0:
		return false
	}
	return true
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func publicIPv4(a [4]byte) bool {
	switch {
	case a[0] == 0 || a[0] == 10 || a[0] == 127:
		return false
	case a[0] == 100 && a[1] >= 64 && a[1] <= 127:
		return false
	case a[0] == 169 && a[1] == 254:
		return false
	case a[0] == 172 && a[1] >= 16 && a[1] <= 31:
		return false
	case a[0] == 192 && a[1] == 168:
		return false
	case a[0] == 192 && a[1] == 0 && (a[2] == 0 || a[2] == 2):
		return false
	case a[0] == 198 && (a[1] == 18 || a[1] == 19):
		return false
	case a[0] == 198 && a[1] == 51 && a[2] == 100:
		return false
	case a[0] == 203 && a[1] == 0 && a[2] == 113:
		return false
	case a[0] >= 224:
		return false
	}
	return true
}

// ParseImportURL validates a user-supplied import URL: https only, port 443, no credentials, and
// a host that is not an IP literal of a non-public address. A GitHub blob page becomes its raw
// file. The result is rebuilt from the normalized parts (no fragment, no credentials, no port),
// so the host that was checked is the host that is fetched. Failures are ImportErrors.
func ParseImportURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, InvalidImport("Not a valid URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		if u.Scheme == "" {
			return nil, InvalidImport("Not a valid URL")
		}
		return nil, InvalidImport("Only https:// URLs can be imported")
	}
	if port := u.Port(); port != "" {
		if n, perr := strconv.Atoi(port); perr != nil || n != 443 {
			return nil, InvalidImport("Only the default https port (443) is allowed")
		}
	}
	if u.User != nil {
		return nil, InvalidImport("URLs with credentials are not allowed")
	}
	hostname := u.Hostname()
	if hostname == "" {
		return nil, InvalidImport("The URL has no host")
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	host, err := ParseHost(hostname)
	if err != nil {
		return nil, InvalidImport("Not a valid URL")
	}
	if host.IsIP() && !IsPublicAddress(host.Addr) {
		return nil, InvalidImport(host.Name + " is not a public address")
	}

	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	out := &url.URL{Scheme: "https", Host: host.URLHost(), RawQuery: u.RawQuery}
	if host.Name == "github.com" || host.Name == "www.github.com" {
		if m := githubBlobRE.FindStringSubmatch(path); m != nil {
			out.Host, out.RawQuery = "raw.githubusercontent.com", ""
			path = fmt.Sprintf("/%s/%s/%s", m[1], m[2], m[3])
		}
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return nil, InvalidImport("Not a valid URL")
	}
	out.Path, out.RawPath = decoded, path
	return out, nil
}
