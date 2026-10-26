package custom

import (
	"net/netip"
	"strconv"
	"strings"
)

// MsgForbiddenAddr is the message of a call whose host is, or resolves to, a forbidden address.
const MsgForbiddenAddr = "Base URL resolves to a forbidden address"

// ForbiddenAddr reports whether a custom provider may not connect to a. Forbidden are the
// link-local (169.254.0.0/16, fe80::/10) and the unspecified addresses (0.0.0.0, ::) -- the cloud
// metadata range and "this host". Loopback and private (RFC 1918, ULA) addresses are allowed.
//
// The check sees through the encodings that carry an IPv4 address inside an IPv6 one:
// IPv4-mapped (::ffff:a.b.c.d), IPv4-compatible (::a.b.c.d), IPv4-translated (::ffff:0:a.b.c.d),
// NAT64 (64:ff9b::a.b.c.d) and 6to4 (2002:aabb:ccdd::/48), and ignores a zone id. All of
// 0.0.0.0/8 counts as unspecified. An invalid (zero) address is forbidden.
func ForbiddenAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return true
	}
	a = a.WithZone("").Unmap()
	if a.Is4() {
		return forbidden4(a)
	}
	if a.IsUnspecified() || a.IsLinkLocalUnicast() {
		return true
	}
	b := a.As16()
	if a == netip.IPv6Loopback() {
		return false
	}
	switch {
	case allZero(b[:12]): // ::/96 IPv4-compatible
		return forbidden4(netip.AddrFrom4([4]byte(b[12:16])))
	case allZero(b[:8]) && b[8] == 0xff && b[9] == 0xff && b[10] == 0 && b[11] == 0: // ::ffff:0:0/96 translated
		return forbidden4(netip.AddrFrom4([4]byte(b[12:16])))
	case b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b && allZero(b[4:12]): // 64:ff9b::/96
		return forbidden4(netip.AddrFrom4([4]byte(b[12:16])))
	case b[0] == 0x20 && b[1] == 0x02: // 2002::/16 6to4
		return forbidden4(netip.AddrFrom4([4]byte(b[2:6])))
	}
	return false
}

func forbidden4(a netip.Addr) bool {
	return a.As4()[0] == 0 || a.IsLinkLocalUnicast()
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// parseLegacyIPv4 parses the inet_aton spellings a system resolver accepts for an IPv4 host:
// one to four parts, each decimal, octal (leading 0) or hex (0x), the last part filling the
// remaining bytes ("2130706433", "0251.0376.0251.0376", "0xa9.0xfe.0xa9.0xfe", "127.1").
func parseLegacyIPv4(host string) (netip.Addr, bool) {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	var nums [4]uint64
	for i, p := range parts {
		n, ok := parseLegacyPart(p)
		if !ok {
			return netip.Addr{}, false
		}
		nums[i] = n
	}
	last := len(parts) - 1
	var ip uint64
	for i := 0; i < last; i++ {
		if nums[i] > 255 {
			return netip.Addr{}, false
		}
		ip |= nums[i] << (8 * uint(3-i))
	}
	if nums[last] >= 1<<(8*uint(4-last)) {
		return netip.Addr{}, false
	}
	ip |= nums[last]
	return netip.AddrFrom4([4]byte{byte(ip >> 24), byte(ip >> 16), byte(ip >> 8), byte(ip)}), true
}

func parseLegacyPart(p string) (uint64, bool) {
	if p == "" {
		return 0, false
	}
	base := 10
	switch {
	case len(p) >= 2 && p[0] == '0' && (p[1] == 'x' || p[1] == 'X'):
		base, p = 16, p[2:]
		if p == "" {
			return 0, true // glibc reads "0x" as 0
		}
	case len(p) >= 2 && p[0] == '0':
		base, p = 8, p[1:]
	}
	n, err := strconv.ParseUint(p, base, 32)
	if err != nil {
		return 0, false
	}
	return n, true
}

// numericLabel reports whether label is made of digits, or of 0x and hex digits: a host whose
// last label looks like that must be an IPv4 address (WHATWG URL host parser).
func numericLabel(label string) bool {
	if label == "" {
		return false
	}
	if len(label) >= 2 && label[0] == '0' && (label[1] == 'x' || label[1] == 'X') {
		for i := 2; i < len(label); i++ {
			if !isHex(label[i]) {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(label); i++ {
		if label[i] < '0' || label[i] > '9' {
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
