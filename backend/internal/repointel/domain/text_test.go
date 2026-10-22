package domain

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"
	"unicode/utf8"
)

func TestDecodeText(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty", nil, ""},
		{"ascii", []byte("const a = 1;\n"), "const a = 1;\n"},
		{"valid multibyte", []byte("é→🚀"), "é→🚀"},
		{"nul kept", []byte("a\x00b"), "a\x00b"},
		{"bom kept", []byte("\xEF\xBB\xBFx"), "\uFEFFx"},
		{"lone continuation", []byte("a\x80b"), "a\uFFFDb"},
		{"each stray continuation", []byte("\x80\x80"), "\uFFFD\uFFFD"},
		{"invalid lead", []byte("a\xFFb\xC0c\xC1d\xF5e"), "a\uFFFDb\uFFFDc\uFFFDd\uFFFDe"},
		{"truncated 3-byte is one", []byte("\xE2\x82x"), "\uFFFDx"},
		{"truncated 3-byte at end", []byte("a\xE2\x82"), "a\uFFFD"},
		{"truncated 4-byte is one", []byte("\xF0\x9F\x9Ax"), "\uFFFDx"},
		{"lead then lead", []byte("\xE2\xE2\x82\xAC"), "\uFFFD\u20AC"},
		{"surrogate half is three", []byte("\xED\xA0\x80"), "\uFFFD\uFFFD\uFFFD"},
		{"overlong 3-byte", []byte("\xE0\x80\x80"), "\uFFFD\uFFFD\uFFFD"},
		{"above U+10FFFF", []byte("\xF4\x90\x80\x80"), "\uFFFD\uFFFD\uFFFD\uFFFD"},
		{"F0 with short continuation", []byte("\xF0\x80\x80\x80"), "\uFFFD\uFFFD\uFFFD\uFFFD"},
		{"max code point", []byte("\xF4\x8F\xBF\xBF"), "\U0010FFFF"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecodeText(tc.in); got != tc.want {
				t.Errorf("DecodeText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDecodeText_AlwaysValidUTF8(t *testing.T) {
	// Every two-byte and three-byte combination of the interesting boundary bytes.
	edge := []byte{0x00, 0x7F, 0x80, 0x8F, 0x90, 0x9F, 0xA0, 0xBF, 0xC0, 0xC2, 0xDF, 0xE0, 0xED, 0xEF, 0xF0, 0xF4, 0xF5, 0xFF}
	for _, a := range edge {
		for _, b := range edge {
			for _, c := range edge {
				got := DecodeText([]byte{a, b, c})
				if !utf8.ValidString(got) {
					t.Fatalf("DecodeText(%x) = %q, not valid UTF-8", []byte{a, b, c}, got)
				}
			}
		}
	}
}

func TestContentHash(t *testing.T) {
	// SHA-1 of the U+FFFD-replaced text, computed independently.
	text := DecodeText([]byte("a\xFF\x00b"))
	sum := sha1.Sum([]byte("a\uFFFD\x00b"))
	want := hex.EncodeToString(sum[:])
	if got := ContentHash(text); got != want {
		t.Errorf("ContentHash(%q) = %s, want %s", text, got, want)
	}
	if got, want := ContentHash(""), "da39a3ee5e6b4b0d3255bfef95601890afd80709"; got != want {
		t.Errorf("ContentHash(\"\") = %s, want %s", got, want)
	}
}

func TestClampName(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	if got := UTF16Len(ClampName(string(long))); got != MaxNameUnits {
		t.Errorf("UTF16Len(ClampName(5000 chars)) = %d, want %d", got, MaxNameUnits)
	}
	// An emoji at units 255-256 does not fit: 254 units remain (AC3048).
	callee := string(long[:254]) + "🚀" + string(long[:44])
	if got := UTF16Len(ClampName(callee)); got != 254 {
		t.Errorf("UTF16Len(ClampName(callee)) = %d, want 254", got)
	}
}

func TestSanitizeText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "abc", "abc"},
		{"empty", "", ""},
		{"nul", "a\x00b\x00", "a�b�"},
		{"invalid utf8", "a\xffb", "a�b"},
		{"valid non-ascii", "café \U0001F600", "café \U0001F600"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeText(tc.in); got != tc.want {
				t.Errorf("SanitizeText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
