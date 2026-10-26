package custom_test

import (
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/llm/custom"
)

func TestParseID(t *testing.T) {
	tests := []struct {
		in       string
		wantSlug string
		wantOK   bool
	}{
		{"custom:synapse", "synapse", true},
		{"custom:a", "a", true},
		{"custom:0-9-a", "0-9-a", true},
		{"custom:" + strings.Repeat("a", 32), strings.Repeat("a", 32), true},
		// EC2.
		{"custom:Syn", "", false},
		{"custom:", "", false},
		{"custom:" + strings.Repeat("a", 33), "", false},
		{"synapse", "", false},
		{"openai", "", false},
		{"custom:a_b", "", false},
		// More.
		{"Custom:abc", "", false},
		{"custom:a b", "", false},
		{"custom:a\n", "", false},
		{"custom:%61", "", false},
		{"custom:é", "", false},
		{"custom::x", "", false},
		{" custom:x", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			slug, ok := custom.ParseID(tc.in)
			if slug != tc.wantSlug || ok != tc.wantOK {
				t.Errorf("ParseID(%q) = (%q, %v), want (%q, %v)", tc.in, slug, ok, tc.wantSlug, tc.wantOK)
			}
		})
	}
	if got := custom.ID("synapse"); got != "custom:synapse" {
		t.Errorf("ID(synapse) = %q, want custom:synapse", got)
	}
}

func TestVocabularies(t *testing.T) {
	if !custom.ProtocolOpenAI.Valid() || !custom.ProtocolAnthropic.Valid() || custom.Protocol("x").Valid() || custom.Protocol("").Valid() {
		t.Error("Protocol.Valid wrong")
	}
	if !custom.AuthBearer.Valid() || !custom.AuthHeader.Valid() || custom.AuthMode("x").Valid() {
		t.Error("AuthMode.Valid wrong")
	}
	if custom.ProtocolOpenAI.DefaultAuthMode() != custom.AuthBearer || custom.ProtocolAnthropic.DefaultAuthMode() != custom.AuthHeader {
		t.Error("DefaultAuthMode wrong")
	}
	tests := []struct {
		mode  custom.Mode
		proto custom.Protocol
		want  bool
	}{
		{custom.ModeStrict, custom.ProtocolOpenAI, true},
		{custom.ModeStrict, custom.ProtocolAnthropic, true},
		{custom.ModePromptOnly, custom.ProtocolAnthropic, true},
		{custom.ModeJSONObject, custom.ProtocolOpenAI, true},
		{custom.ModeJSONObject, custom.ProtocolAnthropic, false},
		{"", custom.ProtocolOpenAI, false},
		{"loose", custom.ProtocolOpenAI, false},
		{custom.ModeStrict, "x", false},
	}
	for _, tc := range tests {
		if got := tc.mode.ValidFor(tc.proto); got != tc.want {
			t.Errorf("Mode(%q).ValidFor(%q) = %v, want %v", tc.mode, tc.proto, got, tc.want)
		}
	}
	if custom.DefaultMode != custom.ModeStrict || custom.Mode("").Valid() {
		t.Error("DefaultMode / empty Mode wrong")
	}
}

func TestCheckHeaderName(t *testing.T) {
	tests := []struct {
		name, auth string
		ok         bool
	}{
		{"X-Org-Id", "", true},
		{"x-api-key", "", true},
		{"anthropic-version", "x-api-key", true},
		{strings.Repeat("a", 64), "", true},
		// EC11.
		{"content-type", "", false},
		{"Content-Type", "", false},
		{"Authorization", "", false},
		{"HOST", "", false},
		{"Content-Length", "", false},
		{"Transfer-Encoding", "", false},
		{"Connection", "", false},
		{"Cookie", "", false},
		{"X-Key", "x-key", false},
		{"x-KEY", "X-Key", false},
		// Not a token / bad length.
		{"", "", false},
		{strings.Repeat("a", 65), "", false},
		{"X Org", "", false},
		{"X:Org", "", false},
		{"X-Org\r\nX", "", false},
		{"X-Org\x00", "", false},
		{"é", "", false},
		{"a(b)", "", false},
	}
	for _, tc := range tests {
		got := custom.CheckHeaderName(tc.name, tc.auth)
		if (got == "") != tc.ok {
			t.Errorf("CheckHeaderName(%q, %q) = %q, want ok=%v", tc.name, tc.auth, got, tc.ok)
		}
	}
}

func TestCheckHeaderValue(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{"abc", true},
		{" spaces and tab\t ok", true},
		{strings.Repeat("v", 4096), true},
		{"", false},
		{strings.Repeat("v", 4097), false},
		{"a\rb", false},
		{"a\nb", false},
		{"a\x00b", false},
	}
	for _, tc := range tests {
		if got := custom.CheckHeaderValue(tc.in); (got == "") != tc.ok {
			t.Errorf("CheckHeaderValue(len %d) = %q, want ok=%v", len(tc.in), got, tc.ok)
		}
	}
}

func TestForbiddenAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"169.254.169.254", true},
		{"169.254.0.1", true},
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"fe80::1", true},
		{"fe80::1%eth0", true},
		{"febf::1", true},
		{"::", true},
		{"::ffff:169.254.169.254", true},
		{"::ffff:0.0.0.0", true},
		{"::ffff:a9fe:a9fe", true},
		{"::169.254.169.254", true},
		{"::ffff:0:169.254.169.254", true},
		{"64:ff9b::169.254.169.254", true},
		{"2002:a9fe:a9fe::1", true},
		// Allowed: loopback, private, public, ULA.
		{"127.0.0.1", false},
		{"127.255.255.254", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"10.0.0.5", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"fd00::1", false},
		{"fc00::1", false},
		{"8.8.8.8", false},
		{"169.253.255.255", false},
		{"169.255.0.0", false},
		{"2606:4700::1111", false},
		{"64:ff9b::8.8.8.8", false},
		{"2002:0808:0808::1", false},
		{"fec0::1", false},
	}
	for _, tc := range tests {
		a, err := netip.ParseAddr(tc.addr)
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", tc.addr, err)
		}
		if got := custom.ForbiddenAddr(a); got != tc.want {
			t.Errorf("ForbiddenAddr(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
	if !custom.ForbiddenAddr(netip.Addr{}) {
		t.Error("ForbiddenAddr(zero Addr) = false, want true (fail closed)")
	}
}

func TestCheckBaseURL(t *testing.T) {
	type want struct {
		norm  string
		plain bool
	}
	valid := []struct {
		in string
		want
	}{
		{"https://api.example.com/v1", want{"https://api.example.com/v1", false}},
		{"https://api.example.com/v1/", want{"https://api.example.com/v1", false}},
		{"https://api.example.com/v1///", want{"https://api.example.com/v1", false}},
		{"https://api.example.com/", want{"https://api.example.com", false}},
		{"https://api.example.com", want{"https://api.example.com", false}},
		{"HTTPS://API.Example.COM/V1", want{"https://api.example.com/V1", false}},
		{"https://gw.example.com:8443/v1", want{"https://gw.example.com:8443/v1", false}},
		{"https://gw.example.com./v1", want{"https://gw.example.com/v1", false}},
		{"https://my_service/v1", want{"https://my_service/v1", false}},
		// EC24: plain_http only for the non-loopback host.
		{"http://gateway.corp/v1", want{"http://gateway.corp/v1", true}},
		{"http://127.0.0.1:8080/v1", want{"http://127.0.0.1:8080/v1", false}},
		{"http://[::1]/v1", want{"http://[::1]/v1", false}},
		{"http://localhost/v1", want{"http://localhost/v1", false}},
		{"http://LOCALHOST:11434/v1/", want{"http://localhost:11434/v1", false}},
		{"http://127.1/v1", want{"http://127.0.0.1/v1", false}},
		{"http://[::ffff:127.0.0.1]/v1", want{"http://[::ffff:127.0.0.1]/v1", false}},
		// Private hosts are allowed; plain http to them is still reported.
		{"http://10.1.2.3/v1", want{"http://10.1.2.3/v1", true}},
		{"https://192.168.0.10:1234/v1", want{"https://192.168.0.10:1234/v1", false}},
		{"http://[fd00::1]/v1", want{"http://[fd00::1]/v1", true}},
		{"http://172.16.0.1/v1", want{"http://172.16.0.1/v1", true}},
		{"https://8.8.8.8/v1", want{"https://8.8.8.8/v1", false}},
		{"https://x/v1/@y", want{"https://x/v1/@y", false}},
		{"https://api.example.com/v1%2Fx", want{"https://api.example.com/v1%2Fx", false}},
		// Legacy IPv4 spellings of an allowed address are rewritten to dotted decimal.
		{"http://0x7f.0.0.1/v1", want{"http://127.0.0.1/v1", false}},
		{"http://2130706433/v1", want{"http://127.0.0.1/v1", false}},
		{"http://0177.0.0.1/v1", want{"http://127.0.0.1/v1", false}},
		{"http://169.253.1.1/v1", want{"http://169.253.1.1/v1", true}},
	}
	for _, tc := range valid {
		t.Run(tc.in, func(t *testing.T) {
			norm, plain, msg := custom.CheckBaseURL(tc.in)
			if msg != "" || norm != tc.norm || plain != tc.plain {
				t.Errorf("CheckBaseURL(%q) = (%q, %v, %q), want (%q, %v, \"\")", tc.in, norm, plain, msg, tc.norm, tc.plain)
			}
		})
	}

	invalid := []string{
		// EC3.
		"ftp://x",
		"https://u:p@x/v1",
		"https://x/v1?k=1",
		"https://x/v1#f",
		"http://169.254.169.254",
		"http://[fe80::1]",
		"http://0.0.0.0",
		// Shape.
		"",
		"x",
		"api.example.com/v1",
		"//api.example.com/v1",
		"https://",
		"https:///v1",
		"https:api.example.com",
		"mailto:a@b.c",
		"javascript://x",
		"file:///etc/passwd",
		"https://x/v1?",
		"https://x/v1#",
		"https://@x/v1",
		"https://:@x/v1",
		"https://x@y/v1",
		"https://x:/v1",
		"https://x:0/v1",
		"https://x:65536/v1",
		"https://x:-1/v1",
		"https://x:abc/v1",
		" https://x/v1",
		"https://x/v1 ",
		"https://x /v1",
		"https://x/\nv1",
		"https://x/v1\x00",
		"https://x\\@y/v1",
		"https://x/v\\1",
		"https://exämple.com/v1",
		"https://exa mple.com/v1",
		"https://.example.com/v1",
		"https://a..b/v1",
		"https://a/" + strings.Repeat("p", 2048),
		// Forbidden literals, however spelled.
		"http://169.254.0.1/v1",
		"http://169.254.169.254./v1",
		"http://[::]/v1",
		"http://0/v1",
		"http://0x0/v1",
		"http://0.0.0.0:80/v1",
		"http://0.1.2.3/v1",
		"http://[::ffff:169.254.169.254]/v1",
		"http://[::ffff:a9fe:a9fe]/v1",
		"http://[0:0:0:0:0:ffff:a9fe:a9fe]/v1",
		"http://[::ffff:0.0.0.0]/v1",
		"http://[::169.254.169.254]/v1",
		"http://[64:ff9b::169.254.169.254]/v1",
		"http://[2002:a9fe:a9fe::]/v1",
		"http://[fe80::1%25eth0]/v1",
		"http://[fe80::1%eth0]/v1",
		"http://[FE80::1]/v1",
		"http://[febf::1]/v1",
		"http://[::1%25lo]/v1",
		"http://[::1/v1",
		"http://[fe80::1]:80/v1",
		"http://[::ffff:1.2.3]/v1",
		"http://2852039166/v1",
		"http://0251.0376.0251.0376/v1",
		"http://0xa9.0xfe.0xa9.0xfe/v1",
		"http://0xA9FEA9FE/v1",
		"http://169.254.43518/v1",
		"http://169.16689662/v1",
		"http://0251.254.169.254/v1",
		"http://0xa9fea9fe./v1",
		"http://169.254.169.254:8080/v1",
		"https://%31%36%39.254.169.254/v1",
		"http://169%2e254.169.254/v1",
		"http://1.2.3.4.5/v1",
		"http://256.1.1.1/v1",
		"http://09.1.1.1/v1",
		"http://１６９.２５４.１６９.２５４/v1",
	}
	for _, in := range invalid {
		t.Run(in, func(t *testing.T) {
			norm, plain, msg := custom.CheckBaseURL(in)
			if msg == "" || norm != "" || plain {
				t.Errorf("CheckBaseURL(%q) = (%q, %v, %q), want a refusal", in, norm, plain, msg)
			}
		})
	}
}

// FuzzCheckBaseURL asserts the invariants of every accepted URL: it is stable under a second
// check, parses back to an http(s) URL without userinfo, query or fragment and without trailing
// slash, and its host is never a forbidden address in any spelling the standard library or a
// system resolver would read as an address.
func FuzzCheckBaseURL(f *testing.F) {
	for _, s := range []string{
		"https://api.example.com/v1", "http://localhost:11434/v1/", "http://[::1]/v1",
		"http://169.254.169.254", "http://[fe80::1]", "http://0.0.0.0", "http://2852039166/",
		"http://[::ffff:169.254.169.254]/", "https://u:p@x/v1", "https://x/v1?k=1", "https://x/v1#f",
		"http://0xa9.0xfe.0xa9.0xfe/", "http://0251.254.169.254/", "ftp://x", "http://127.1/",
		"http://[fe80::1%25eth0]/", "https://x:99999/", "http://a\\@169.254.169.254/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		norm, plain, msg := custom.CheckBaseURL(raw)
		if msg != "" {
			if norm != "" || plain {
				t.Fatalf("CheckBaseURL(%q) = (%q, %v, %q): a refusal must return the zero values", raw, norm, plain, msg)
			}
			return
		}
		u, err := url.Parse(norm)
		if err != nil {
			t.Fatalf("CheckBaseURL(%q) accepted %q which does not parse: %v", raw, norm, err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			u.ForceQuery || u.Opaque != "" || strings.ContainsAny(norm, "?# \\") || strings.HasSuffix(norm, "/") ||
			u.Hostname() == "" || len(norm) > custom.MaxBaseURLLen+1 {
			t.Fatalf("CheckBaseURL(%q) accepted %q which breaks the URL rules", raw, norm)
		}
		if again, plain2, msg2 := custom.CheckBaseURL(norm); again != norm || plain2 != plain || msg2 != "" {
			t.Fatalf("CheckBaseURL(%q) = %q is not stable: second pass = (%q, %v, %q)", raw, norm, again, plain2, msg2)
		}
		if a, err := netip.ParseAddr(u.Hostname()); err == nil && custom.ForbiddenAddr(a) {
			t.Fatalf("CheckBaseURL(%q) accepted forbidden address %q", raw, norm)
		}
		if plain && u.Scheme != "http" {
			t.Fatalf("CheckBaseURL(%q) = %q reports plain_http for %s", raw, norm, u.Scheme)
		}
	})
}
