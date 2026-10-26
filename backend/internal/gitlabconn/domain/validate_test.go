package domain_test

import (
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/llm/custom"
)

func TestCheckBaseURL_Normalizes(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantNorm string
		wantHost string
	}{
		{"plain", "https://gitlab.com", "https://gitlab.com", "gitlab.com"},
		{"EC2 case, port 443, trailing slash", "https://GitLab.Corp.Example:443/gitlab/", "https://gitlab.corp.example/gitlab", "gitlab.corp.example"},
		{"EC2 other port", "https://gitlab.corp.example:8443", "https://gitlab.corp.example:8443", "gitlab.corp.example:8443"},
		{"upper-case scheme", "HTTPS://gitlab.com/", "https://gitlab.com", "gitlab.com"},
		{"padded port 443", "https://gitlab.com:0443", "https://gitlab.com", "gitlab.com"},
		{"nested prefix", "https://git.example/a/b///", "https://git.example/a/b", "git.example"},
		{"api is not the last pair", "https://git.example/api/v4/x", "https://git.example/api/v4/x", "git.example"},
		{"v4 alone", "https://git.example/v4", "https://git.example/v4", "git.example"},
		{"loopback allowed", "https://127.0.0.1:8443", "https://127.0.0.1:8443", "127.0.0.1:8443"},
		{"localhost allowed", "https://localhost", "https://localhost", "localhost"},
		{"private allowed", "https://10.1.2.3", "https://10.1.2.3", "10.1.2.3"},
		{"ULA allowed", "https://[fd00::1]", "https://[fd00::1]", "[fd00::1]"},
		{"IPv6 with port", "https://[::1]:8443/x", "https://[::1]:8443/x", "[::1]:8443"},
		{"IPv6 port 443", "https://[::1]:443", "https://[::1]", "[::1]"},
		{"legacy IPv4 spelling", "https://0x7f.1", "https://127.0.0.1", "127.0.0.1"},
		{"trailing dot host", "https://gitlab.com.", "https://gitlab.com", "gitlab.com"},
		{"github look-alike", "https://github.com.evil.example", "https://github.com.evil.example", "github.com.evil.example"},
		{"gitlab on a github subdomain", "https://enterprise.github.com", "https://enterprise.github.com", "enterprise.github.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			norm, host, msg := domain.CheckBaseURL(tc.in)
			if msg != "" || norm != tc.wantNorm || host != tc.wantHost {
				t.Errorf("CheckBaseURL(%q) = (%q, %q, %q), want (%q, %q, \"\")", tc.in, norm, host, msg, tc.wantNorm, tc.wantHost)
			}
		})
	}
}

// TestCheckBaseURL_Rejects covers AC2 / EC1 and the other refusals of FR2.
func TestCheckBaseURL_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantMsg string // "" = any message
	}{
		{"empty", "", "Base URL is required"},
		{"EC1 http", "http://gitlab.example", "Base URL must start with https://"},
		{"http loopback", "http://localhost:8080", "Base URL must start with https://"},
		{"other scheme", "ftp://gitlab.example", "Base URL must start with https://"},
		{"no scheme", "gitlab.example", "Base URL must start with https://"},
		{"EC1 userinfo", "https://u:p@gitlab.example", ""},
		{"user only", "https://u@gitlab.example", ""},
		{"EC1 query", "https://gitlab.example/?a=1", ""},
		{"empty query", "https://gitlab.example?", ""},
		{"EC1 fragment", "https://gitlab.example/#x", ""},
		{"EC1 api url", "https://gitlab.com/api/v4", domain.MsgAPIURL},
		{"api url trailing slash", "https://gitlab.com/api/v4/", domain.MsgAPIURL},
		{"api url with prefix", "https://git.example/gitlab/api/v4", domain.MsgAPIURL},
		{"api url upper case", "https://gitlab.com/API/V4", domain.MsgAPIURL},
		{"EC1 github", "https://github.com", ""},
		{"github upper case", "https://GitHub.com/", ""},
		{"github trailing dot", "https://github.com.", ""},
		{"github with port", "https://github.com:8443", ""},
		{"www github", "https://www.github.com", ""},
		{"api github", "https://api.github.com/x", ""},
		{"EC1 metadata", "https://169.254.169.254", ""},
		{"metadata decimal", "https://2852039166", ""},
		{"metadata hex", "https://0xa9.0xfe.0xa9.0xfe", ""},
		{"metadata octal", "https://0251.0376.0251.0376", ""},
		{"metadata mapped", "https://[::ffff:169.254.169.254]", ""},
		{"EC1 link-local v6", "https://[fe80::1]", ""},
		{"zone id", "https://[fe80::1%25eth0]", ""},
		{"EC1 unspecified", "https://0.0.0.0", ""},
		{"unspecified v6", "https://[::]", ""},
		{"EC1 too long", "https://gitlab.example/" + strings.Repeat("a", 2049-len("https://gitlab.example/")), ""},
		{"port 0", "https://gitlab.example:0", ""},
		{"port too big", "https://gitlab.example:65536", ""},
		{"empty port", "https://gitlab.example:", ""},
		{"no host", "https:///x", ""},
		{"space", "https://gitlab.example/a b", ""},
		{"leading space", " https://gitlab.example", "Base URL must start with https://"},
		{"backslash", "https://gitlab.example\\@evil.example", ""},
		{"non-ASCII host", "https://gítlab.example", ""},
		{"empty segment", "https://gitlab.example/a//b", "Base URL path is not valid"},
		{"dot segment", "https://gitlab.example/a/./b", "Base URL path is not valid"},
		{"dot-dot segment", "https://gitlab.example/a/../b", "Base URL path is not valid"},
		{"encoded dot-dot", "https://gitlab.example/%2e%2e/b", "Base URL path is not valid"},
		{"encoded slash", "https://gitlab.example/a%2Fb", "Base URL path is not valid"},
		{"encoded backslash", "https://gitlab.example/a%5Cb", "Base URL path is not valid"},
		{"encoded control", "https://gitlab.example/a%0Ab", "Base URL path is not valid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			norm, host, msg := domain.CheckBaseURL(tc.in)
			if msg == "" || norm != "" || host != "" {
				t.Fatalf("CheckBaseURL(%q) = (%q, %q, %q), want a refusal with zero values", tc.in, norm, host, msg)
			}
			if tc.wantMsg != "" && msg != tc.wantMsg {
				t.Errorf("CheckBaseURL(%q) message = %q, want %q", tc.in, msg, tc.wantMsg)
			}
		})
	}
}

// FuzzCheckBaseURL asserts the invariants of every accepted URL: it is https without userinfo,
// query, fragment or trailing slash, stable under a second check, its host key matches the URL, it
// carries no :443, no GitHub host, no forbidden address, no API suffix and no odd path segment.
func FuzzCheckBaseURL(f *testing.F) {
	for _, s := range []string{
		"https://gitlab.com", "https://GitLab.Corp.Example:443/gitlab/", "https://gitlab.corp.example:8443",
		"http://gitlab.example", "https://u:p@gitlab.example", "https://gitlab.example/?a=1",
		"https://gitlab.example/#x", "https://gitlab.com/api/v4", "https://github.com", "https://GITHUB.com./",
		"https://169.254.169.254", "https://[fe80::1]", "https://0.0.0.0", "https://2852039166",
		"https://[::ffff:169.254.169.254]/", "https://0xa9.0xfe.0xa9.0xfe/", "https://[::1]:443/x",
		"https://x/a/../b", "https://x/%2e%2e/api/v4", "https://x//", "https://x/a%2fb", "https://x:0443",
		"https://[fe80::1%25eth0]/", "https://x\\@169.254.169.254", "https://x/api/V4/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		norm, host, msg := domain.CheckBaseURL(raw)
		if msg != "" {
			if norm != "" || host != "" {
				t.Fatalf("CheckBaseURL(%q) = (%q, %q, %q): a refusal must return the zero values", raw, norm, host, msg)
			}
			return
		}
		u, err := url.Parse(norm)
		if err != nil {
			t.Fatalf("CheckBaseURL(%q) accepted %q which does not parse: %v", raw, norm, err)
		}
		if u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.Opaque != "" ||
			strings.ContainsAny(norm, "?# \\") || strings.HasSuffix(norm, "/") || u.Hostname() == "" ||
			len(norm) > custom.MaxBaseURLLen {
			t.Fatalf("CheckBaseURL(%q) accepted %q which breaks the URL rules", raw, norm)
		}
		if host != u.Host || host != strings.ToLower(host) || u.Port() == "443" || strings.HasSuffix(host, ".") {
			t.Fatalf("CheckBaseURL(%q) = (%q, %q): bad host key", raw, norm, host)
		}
		switch u.Hostname() {
		case "github.com", "www.github.com", "api.github.com":
			t.Fatalf("CheckBaseURL(%q) accepted GitHub host %q", raw, norm)
		}
		if a, err := netip.ParseAddr(u.Hostname()); err == nil && custom.ForbiddenAddr(a) {
			t.Fatalf("CheckBaseURL(%q) accepted forbidden address %q", raw, norm)
		}
		lower := strings.ToLower(u.EscapedPath())
		if strings.HasSuffix(lower, "/api/v4") {
			t.Fatalf("CheckBaseURL(%q) accepted API url %q", raw, norm)
		}
		if p := u.Path; p != "" {
			for _, seg := range strings.Split(p[1:], "/") {
				if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "\\") {
					t.Fatalf("CheckBaseURL(%q) accepted %q with path segment %q", raw, norm, seg)
				}
			}
		}
		if again, host2, msg2 := domain.CheckBaseURL(norm); again != norm || host2 != host || msg2 != "" {
			t.Fatalf("CheckBaseURL(%q) = %q is not stable: second pass = (%q, %q, %q)", raw, norm, again, host2, msg2)
		}
	})
}

func TestCheckName(t *testing.T) {
	long := strings.Repeat("a", 80)
	tests := []struct {
		name     string
		raw      string
		host     string
		wantName string
		wantMsg  bool
	}{
		{"plain", "Work GitLab", "gitlab.com", "Work GitLab", false},
		{"trimmed", "  Work\t", "gitlab.com", "Work", false},
		{"empty -> host key", "", "gitlab.corp.example:8443", "gitlab.corp.example:8443", false},
		{"blank -> host key", " \n ", "gitlab.com", "gitlab.com", false},
		{"80 characters", long, "h", long, false},
		{"81 characters", long + "a", "h", "", true},
		{"80 astral characters are 160 units", strings.Repeat("😀", 80), "h", "", true},
		{"40 astral characters", strings.Repeat("😀", 40), "h", strings.Repeat("😀", 40), false},
		{"control inside", "a\x00b", "h", "", true},
		{"newline inside", "a\nb", "h", "", true},
		{"invalid UTF-8", "a\xffb", "h", "", true},
		{"host too long to default", "", strings.Repeat("a", 81) + ".example", "", true},
		{"no host, no name", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, msg := domain.CheckName(tc.raw, tc.host)
			if (msg != "") != tc.wantMsg || name != tc.wantName {
				t.Errorf("CheckName(%q, %q) = (%q, %q), want name %q, refusal %v", tc.raw, tc.host, name, msg, tc.wantName, tc.wantMsg)
			}
		})
	}
}

func TestCheckToken(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantMsg bool
	}{
		{"pat", "glpat-abcDEF123_-xyz", false},
		{"1 character", "x", false},
		{"4096 characters", strings.Repeat("a", domain.MaxTokenLen), false},
		{"4097 characters", strings.Repeat("a", domain.MaxTokenLen+1), true},
		{"empty", "", true},
		{"space", "abc def", true},
		{"leading space", " abc", true},
		{"trailing newline", "abc\n", true},
		{"tab", "a\tb", true},
		{"NUL", "a\x00b", true},
		{"DEL", "a\x7fb", true},
		{"NBSP", "a b", true},
		{"BOM", "a\ufeffb", true},
		{"invalid UTF-8", "a\xffb", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := domain.CheckToken(tc.in)
			if (msg != "") != tc.wantMsg {
				t.Errorf("CheckToken(%q) = %q, want refusal %v", tc.in, msg, tc.wantMsg)
			}
			if strings.Contains(msg, tc.in) && tc.in != "" {
				t.Errorf("CheckToken(%q) message %q contains the token", tc.in, msg)
			}
		})
	}
}
