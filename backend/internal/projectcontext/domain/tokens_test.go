package domain_test

import (
	"strings"
	"testing"

	"dev-digest-go/backend/internal/projectcontext/domain"
)

func TestStoreTextTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"one char", "a", 1},
		{"four chars", "abcd", 1},
		{"five chars", "abcde", 2},
		{"code points not bytes", strings.Repeat("é", 8), 2},
		{"astral is one code point", strings.Repeat("😀", 8), 2},
	}
	for _, tc := range tests {
		if got := domain.StoreTextTokens(tc.text); got != tc.want {
			t.Errorf("%s: StoreTextTokens(%q) = %d, want %d", tc.name, tc.text, got, tc.want)
		}
	}
}

func TestUTF8Bytes(t *testing.T) {
	if got := domain.UTF8Bytes("aé😀"); got != 7 {
		t.Errorf("UTF8Bytes = %d, want 7", got)
	}
}

func TestCloneTextTokens(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		units   int
	}{
		{"ascii", []byte("abcdefgh"), 8},
		{"two byte", []byte(strings.Repeat("é", 8)), 8},
		{"astral is two units", []byte(strings.Repeat("😀", 4)), 8},
		{"lone continuation", []byte{0x80, 0x80}, 2},
		{"truncated three byte sequence is one replacement", []byte{'a', 0xE2, 0x82, 'b'}, 3},
		{"truncated four byte sequence", []byte{0xF0, 0x9F, 0x98, 'x'}, 2},
		{"overlong lead", []byte{0xC0, 0xAF}, 2},
		{"surrogate encoding", []byte{0xED, 0xA0, 0x80}, 3},
		{"lead at end", []byte{'a', 0xE2}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.UTF16Units(tc.content); got != tc.units {
				t.Errorf("UTF16Units(% x) = %d, want %d", tc.content, got, tc.units)
			}
			if got, want := domain.CloneTextTokens(tc.content), (tc.units+3)/4; got != want {
				t.Errorf("CloneTextTokens(% x) = %d, want %d", tc.content, got, want)
			}
		})
	}
}

func TestCloneSizeTokens(t *testing.T) {
	tests := []struct {
		size int64
		want int
	}{{0, 0}, {1, 1}, {4, 1}, {300 * 1024, 76800}, {262_145, 65_537}}
	for _, tc := range tests {
		if got := domain.CloneSizeTokens(tc.size); got != tc.want {
			t.Errorf("CloneSizeTokens(%d) = %d, want %d", tc.size, got, tc.want)
		}
	}
}
