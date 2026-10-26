package codehost_test

import (
	"testing"

	"dev-digest-go/backend/internal/platform/codehost"
)

func TestHostKey(t *testing.T) {
	tests := []struct {
		name       string
		host, port string
		want       string
	}{
		{name: "plain", host: "gitlab.com", want: "gitlab.com"},
		{name: "lower-cased", host: "GitLab.COM", want: "gitlab.com"},
		{name: "port 443 dropped", host: "gitlab.com", port: "443", want: "gitlab.com"},
		{name: "other port kept", host: "git.corp.example", port: "8443", want: "git.corp.example:8443"},
		{name: "port normalised", host: "git.corp.example", port: "08443", want: "git.corp.example:8443"},
		{name: "ssh has no port", host: "git.corp.example", want: "git.corp.example"},
		{name: "ipv6 bracketed", host: "::1", port: "8443", want: "[::1]:8443"},
		{name: "ipv6 already bracketed", host: "[::1]", port: "", want: "[::1]"},
		// The cases of gitlabconn/domain.CheckBaseURL's host key.
		{name: "connection: plain", host: "gitlab.example.com", want: "gitlab.example.com"},
		{name: "connection: mixed case, port 443", host: "GitLab.Example.com", port: "443", want: "gitlab.example.com"},
		{name: "connection: port 8443", host: "gitlab.example.com", port: "8443", want: "gitlab.example.com:8443"},
		{name: "connection: ipv6 with port", host: "[::1]", port: "8443", want: "[::1]:8443"},
		{name: "connection: ipv6 port 443", host: "[::1]", port: "443", want: "[::1]"},
		{name: "connection: trailing dot", host: "GITLAB.example.com.", port: "8443", want: "gitlab.example.com:8443"},
		{name: "connection: trailing dot, no port", host: "gitlab.example.com.", want: "gitlab.example.com"},
		{name: "connection: ipv6 uncompressed", host: "[0:0:0:0:0:0:0:1]", want: "[::1]"},
		{name: "connection: ipv6 upper case", host: "[2001:DB8::1]", port: "9", want: "[2001:db8::1]:9"},
		{name: "connection: ipv4-mapped ipv6", host: "[::FFFF:7f00:1]", want: "[::ffff:127.0.0.1]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codehost.HostKey(tc.host, tc.port); got != tc.want {
				t.Errorf("HostKey(%q, %q) = %q, want %q", tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestCloneKey(t *testing.T) {
	tests := []struct {
		name          string
		prov, host    string
		id            int64
		owner, repo   string
		first, second string
	}{
		{name: "github unchanged", prov: codehost.GitHub, host: "github.com", id: 7, owner: "o", repo: "r", first: "o", second: "r"},
		{name: "github ignores id and host", prov: codehost.GitHub, host: "x", owner: "Acme", repo: "Pay.js", first: "Acme", second: "Pay.js"},
		{name: "gitlab", prov: codehost.GitLab, host: "gitlab.com", id: 42, owner: "acme/platform", repo: "web", first: "@gitlab/gitlab.com", second: "42"},
		{name: "gitlab port", prov: codehost.GitLab, host: "git.corp.example:8443", id: 9, owner: "g", repo: "p", first: "@gitlab/git.corp.example_8443", second: "9"},
		{name: "gitlab ipv6", prov: codehost.GitLab, host: "[::1]:8443", id: 1, owner: "g", repo: "p", first: "@gitlab/__1-_8443", second: "1"},
		{name: "gitlab ipv6 no port", prov: codehost.GitLab, host: "[::1]", id: 1, owner: "g", repo: "p", first: "@gitlab/__1-", second: "1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first, second := codehost.CloneKey(tc.prov, tc.host, tc.id, tc.owner, tc.repo)
			if first != tc.first || second != tc.second {
				t.Errorf("CloneKey(%q, %q, %d, %q, %q) = (%q, %q), want (%q, %q)",
					tc.prov, tc.host, tc.id, tc.owner, tc.repo, first, second, tc.first, tc.second)
			}
		})
	}
}

func TestCloneKey_GitLabNeverFromPath(t *testing.T) {
	a1, a2 := codehost.CloneKey(codehost.GitLab, "gitlab.com", 5, "old/group", "old")
	b1, b2 := codehost.CloneKey(codehost.GitLab, "gitlab.com", 5, "new/group", "renamed")
	if a1 != b1 || a2 != b2 {
		t.Errorf("CloneKey changed with the project path: (%q, %q) vs (%q, %q)", a1, a2, b1, b2)
	}
}

func TestWebURL(t *testing.T) {
	tests := []struct{ base, full, want string }{
		{"https://github.com", "o/r", "https://github.com/o/r"},
		{"https://gitlab.com/", "acme/web", "https://gitlab.com/acme/web"},
		{"https://git.corp.example:8443/gitlab", "g/sub/p", "https://git.corp.example:8443/gitlab/g/sub/p"},
		{"https://gitlab.com", "/acme/web", "https://gitlab.com/acme/web"},
	}
	for _, tc := range tests {
		if got := codehost.WebURL(tc.base, tc.full); got != tc.want {
			t.Errorf("WebURL(%q, %q) = %q, want %q", tc.base, tc.full, got, tc.want)
		}
	}
}

func TestCloneKey_GitLabDirIsInjective(t *testing.T) {
	hosts := []string{"gitlab.com", "gitlab.com:8443", "gitlab_com", "[::1]", "[::1]:8443", "[::1:8443]", "[::18443]", "10.0.0.5:8443", "10.0.0.5"}
	seen := map[string]string{}
	for _, h := range hosts {
		dir, _ := codehost.CloneKey(codehost.GitLab, h, 1, "g", "p")
		if prev, dup := seen[dir]; dup {
			t.Errorf("CloneKey dir %q is shared by host keys %q and %q", dir, prev, h)
		}
		seen[dir] = h
	}
}
