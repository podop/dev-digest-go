package domain_test

import (
	"net/netip"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/skills/domain"
)

func TestParseImportURL_AC255(t *testing.T) {
	tests := []struct {
		url     string
		wantMsg string
	}{
		{"http://h/a.md", "Only https:// URLs can be imported"},
		{"https://h:8443/a.md", "Only the default https port (443) is allowed"},
		{"https://u:p@h/a.md", "URLs with credentials are not allowed"},
		{"https://127.0.0.1/a.md", "127.0.0.1 is not a public address"},
		{"https://0x7f.1/a.md", "127.0.0.1 is not a public address"},
		{"https://[::ffff:10.0.0.1]/a.md", "::ffff:a00:1 is not a public address"},
		{"https://10.1.2.3/a.md", "10.1.2.3 is not a public address"},
		{"https://[::1]/a.md", "::1 is not a public address"},
		{"https://2130706433/a.md", "127.0.0.1 is not a public address"},
		{"https://0177.0.0.1/a.md", "127.0.0.1 is not a public address"},
		{"https://0x7f000001/a.md", "127.0.0.1 is not a public address"},
		{"https://127.1/a.md", "127.0.0.1 is not a public address"},
		{"https://127.0.0.1./a.md", "127.0.0.1 is not a public address"},
		{"https://127。0。0。1/a.md", "127.0.0.1 is not a public address"},
		{"https://０x7f.1/a.md", "127.0.0.1 is not a public address"},
		{"https://169.254.169.254/latest/meta-data", "169.254.169.254 is not a public address"},
		{"https://[fe80::1]/a.md", "fe80::1 is not a public address"},
		{"https://[fd00::1]/a.md", "fd00::1 is not a public address"},
		{"https://[64:ff9b::7f00:1]/a.md", "64:ff9b::7f00:1 is not a public address"},
		{"https://[2002:7f00:1::]/a.md", "2002:7f00:1:: is not a public address"},
		{"https://[0:0:0:0:0:0:0:0]/a.md", ":: is not a public address"},
		{"https://224.0.0.1/a.md", "224.0.0.1 is not a public address"},
		{"https://0.0.0.0/a.md", "0.0.0.0 is not a public address"},
		{"https://999.1.1.1/a.md", "Not a valid URL"},
		{"https://1.2.3.4.5/a.md", "Not a valid URL"},
		{"https://a b/a.md", "Not a valid URL"},
		{"not a url", "Not a valid URL"},
		{"", "Not a valid URL"},
		{"https:///a.md", "The URL has no host"},
		{"ftp://example.com/a.md", "Only https:// URLs can be imported"},
		{"file:///etc/passwd", "Only https:// URLs can be imported"},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			u, err := domain.ParseImportURL(tc.url)
			if err == nil {
				t.Fatalf("ParseImportURL(%q) = %v, want error %q", tc.url, u, tc.wantMsg)
			}
			if got := importMessage(t, err); got != tc.wantMsg {
				t.Errorf("ParseImportURL(%q) message = %q, want %q", tc.url, got, tc.wantMsg)
			}
		})
	}
}

func TestParseImportURL_Accepted(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "https://example.com/a.md", "https://example.com/a.md"},
		{"trimmed and lowercased", "  HTTPS://Example.COM/A.md  ", "https://example.com/A.md"},
		{"explicit port 443", "https://example.com:443/a.md", "https://example.com/a.md"},
		{"no path", "https://example.com", "https://example.com/"},
		{"query kept, fragment dropped", "https://example.com/a.md?x=1#frag", "https://example.com/a.md?x=1"},
		{"IDN to ASCII", "https://bücher.example/a.md", "https://xn--bcher-kva.example/a.md"},
		{"public IPv4 literal", "https://93.184.216.34/a.md", "https://93.184.216.34/a.md"},
		{"public IPv4 in hex", "https://0x5db8d822/a.md", "https://93.184.216.34/a.md"},
		{"public IPv6 literal", "https://[2606:2800:220:1:248:1893:25c8:1946]/a.md", "https://[2606:2800:220:1:248:1893:25c8:1946]/a.md"},
		{"double slash path", "https://example.com//a.md", "https://example.com//a.md"},
		{"encoded path kept", "https://example.com/a%20b.md", "https://example.com/a%20b.md"},
		{"github blob", "https://github.com/o/r/blob/main/skills/x.md?plain=1#L3", "https://raw.githubusercontent.com/o/r/main/skills/x.md"},
		{"www github blob", "https://www.github.com/o/r/blob/v1/x.md", "https://raw.githubusercontent.com/o/r/v1/x.md"},
		{"github non-blob page is kept", "https://github.com/o/r", "https://github.com/o/r"},
		{"github tree is kept", "https://github.com/o/r/tree/main/x", "https://github.com/o/r/tree/main/x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := domain.ParseImportURL(tc.in)
			if err != nil {
				t.Fatalf("ParseImportURL(%q) error = %v", tc.in, err)
			}
			if got := u.String(); got != tc.want {
				t.Errorf("ParseImportURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseHost(t *testing.T) {
	tests := []struct {
		in, wantName string
		wantIP       bool
	}{
		{"EXAMPLE.com", "example.com", false},
		{"example.com.", "example.com.", false},
		{"localhost", "localhost", false},
		{"v1.2.example", "v1.2.example", false},
		{"1.2.3.4", "1.2.3.4", true},
		{"0x7f.1", "127.0.0.1", true},
		{"0x7f.0.0.1", "127.0.0.1", true},
		{"017700000001", "127.0.0.1", true},
		{"0300.0250.0.1", "192.168.0.1", true},
		{"192.168.257", "192.168.1.1", true},
		{"4294967295", "255.255.255.255", true},
		{"0x", "0.0.0.0", true},
		{"1.2.3.4.", "1.2.3.4", true},
		{"[::1]", "::1", true},
		{"[0:0:0:0:0:0:0:1]", "::1", true},
		{"[::ffff:10.0.0.1]", "::ffff:a00:1", true},
		{"[2001:DB8:0:0:1:0:0:1]", "2001:db8::1:0:0:1", true},
		{"[1:0:0:2:0:0:0:3]", "1:0:0:2::3", true},
		{"[1:0:2:0:3:0:4:0]", "1:0:2:0:3:0:4:0", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			h, err := domain.ParseHost(tc.in)
			if err != nil {
				t.Fatalf("ParseHost(%q) error = %v", tc.in, err)
			}
			if h.Name != tc.wantName || h.IsIP() != tc.wantIP {
				t.Errorf("ParseHost(%q) = {%q, ip=%v}, want {%q, ip=%v}", tc.in, h.Name, h.IsIP(), tc.wantName, tc.wantIP)
			}
		})
	}

	for _, bad := range []string{"", "a b", "a/b", "a%41", "a@b", "[::1", "[1.2.3.4]", "[::1%eth0]", "1.2.3.4.5", "256.1.1.1", "1.2.3.0x100", "09.1.1.1", "a:b"} {
		t.Run("bad "+bad, func(t *testing.T) {
			if h, err := domain.ParseHost(bad); err == nil {
				t.Errorf("ParseHost(%q) = %+v, want an error", bad, h)
			}
		})
	}
}

func TestIsPublicAddress(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"8.8.8.8", true},
		{"93.184.216.34", true},
		{"1.1.1.1", true},
		{"172.15.255.255", true},
		{"172.32.0.1", true},
		{"100.63.255.255", true},
		{"100.128.0.1", true},
		{"192.0.1.1", true},
		{"198.17.0.1", true},
		{"198.20.0.1", true},
		{"223.255.255.255", true},
		{"0.1.2.3", false},
		{"10.0.0.1", false},
		{"127.255.255.255", false},
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"169.254.1.1", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"192.168.0.1", false},
		{"192.0.0.9", false},
		{"192.0.2.1", false},
		{"198.18.0.1", false},
		{"198.19.255.255", false},
		{"198.51.100.1", false},
		{"203.0.113.1", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
		{"2606:2800:220:1:248:1893:25c8:1946", true},
		{"2a00:1450:4001:81b::200e", true},
		{"::ffff:8.8.8.8", true},
		{"::ffff:10.0.0.1", false},
		{"::ffff:127.0.0.1", false},
		{"64:ff9b::808:808", true},
		{"64:ff9b::a00:1", false},
		{"2002:808:808::", true},
		{"2002:a00:1::", false},
		{"::", false},
		{"::1", false},
		{"fe80::1", false},
		{"fc00::1", false},
		{"fd12::1", false},
		{"ff02::1", false},
		{"2001:db8::1", false},
		{"2001::1", false},
		{"2001:4860:4860::8888", true},
		{"1::1", false},
		{"4000::1", false},
		{"fe80::1%eth0", false},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			a, err := netip.ParseAddr(tc.addr)
			if err != nil {
				t.Fatalf("ParseAddr(%q) error = %v", tc.addr, err)
			}
			if got := domain.IsPublicAddress(a); got != tc.want {
				t.Errorf("IsPublicAddress(%s) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
	if domain.IsPublicAddress(netip.Addr{}) {
		t.Error("IsPublicAddress(zero Addr) = true, want false")
	}
}

func FuzzParseHost(f *testing.F) {
	for _, seed := range []string{
		"example.com", "EXAMPLE.COM.", "127.0.0.1", "0x7f.1", "0177.0.0.1", "2130706433", "127.1", "0x",
		"[::1]", "[::ffff:10.0.0.1]", "[2001:db8::1]", "bücher.example", "127。0。0。1", "０x7f.1",
		"1.2.3.4.5", "a..b", ".", "", "xn--bcher-kva.example", "a_b.example", "1e3", "08.1.1.1",
		strings.Repeat("a.", 100) + "b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		h, err := domain.ParseHost(raw)
		if err != nil {
			return
		}
		if h.IsIP() {
			// An IP host is spelled canonically: no leading zeros or hex, IPv6 compressed.
			if h.Addr.Is4() && h.Name != h.Addr.String() {
				t.Fatalf("ParseHost(%q).Name = %q, want dotted decimal %q", raw, h.Name, h.Addr)
			}
			again, err := domain.ParseHost(h.URLHost())
			if err != nil || again.Name != h.Name || again.Addr != h.Addr {
				t.Fatalf("ParseHost(%q) is not stable: re-parsing %q gives %+v, %v", raw, h.URLHost(), again, err)
			}
			return
		}
		// A domain never survives as something a resolver library could read as a number: a name
		// that ends in a number must have been converted to an IPv4 address.
		if strings.ContainsAny(h.Name, "/:@#?[]\\%<>^| \t\r\n") {
			t.Fatalf("ParseHost(%q).Name = %q holds a forbidden character", raw, h.Name)
		}
		for _, r := range h.Name {
			if r > 0x7e {
				t.Fatalf("ParseHost(%q).Name = %q is not ASCII", raw, h.Name)
			}
		}
		labels := strings.Split(strings.TrimSuffix(h.Name, "."), ".")
		last := labels[len(labels)-1]
		if last != "" && strings.Trim(last, "0123456789") == "" {
			t.Fatalf("ParseHost(%q).Name = %q ends in a number but is not an IP", raw, h.Name)
		}
		if strings.HasPrefix(strings.ToLower(last), "0x") && strings.Trim(last[2:], "0123456789abcdefABCDEF") == "" {
			t.Fatalf("ParseHost(%q).Name = %q ends in a hex number but is not an IP", raw, h.Name)
		}
	})
}
