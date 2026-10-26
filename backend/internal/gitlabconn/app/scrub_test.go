package app

import (
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	const tok = "glpat-abcdefghijklmnop"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "token " + tok + " bad", "token [REDACTED] bad"},
		{"json escaped", `"a\"b` + "\"", `"a\"b` + "\""},
		{"other connection's token", "also glrt-zzzzzzzzzzzzzzzz1", "also [REDACTED]"},
		{"nothing", "GitLab is not reachable at https://gitlab.example", "GitLab is not reachable at https://gitlab.example"},
	}
	for _, tc := range tests {
		if got := scrub(tc.in, tok); got != tc.want {
			t.Errorf("%s: scrub(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestScrub_RedactsBeforeCutting(t *testing.T) {
	const tok = "glpat-abcdefghijklmnop"
	in := strings.Repeat("x", maxMessage-8) + tok
	got := scrub(in, tok)
	if strings.Contains(got, "glpat") || strings.Contains(got, "abcdef") || len(got) > maxMessage {
		t.Errorf("scrub() = %q (%d bytes), want the token fully masked and at most %d bytes", got, len(got), maxMessage)
	}
}

func TestTruncate_KeepsUTF8(t *testing.T) {
	if got := truncate("aé", 2); got != "a" {
		t.Errorf("truncate(%q, 2) = %q, want %q", "aé", got, "a")
	}
}

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := newID()
		if !validID(id) || id[14] != '4' || seen[id] {
			t.Fatalf("newID() = %q, want a unique canonical v4 UUID", id)
		}
		seen[id] = true
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"11111111-1111-4111-8111-111111111111":  true,
		"11111111-1111-4111-8111-11111111111":   false,
		"11111111-1111-4111-8111-1111111111111": false,
		"ABCDEFAB-ABCD-4ABC-8ABC-ABCDEFABCDEF":  false,
		"../x":                                  false, "": false, "11111111-1111-4111-8111-11111111111/": false,
		"11111111-1111-4111-8111-111111111111\n": false,
	} {
		if got := validID(id); got != want {
			t.Errorf("validID(%q) = %v, want %v", id, got, want)
		}
	}
}
