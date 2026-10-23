package domain_test

import (
	"strings"
	"testing"

	"dev-digest-go/backend/internal/evals/domain"
)

func TestErrorText(t *testing.T) {
	maskKey := func(s string) string { return strings.ReplaceAll(s, "sk-or-v1-SECRET", "[redacted]") }
	tests := []struct {
		name   string
		raw    string
		redact func(string) string
		want   string
	}{
		{"plain", "boom", nil, "boom"},
		{"whitespace runs collapse", "a \n\t b\r\n\r\nc", nil, "a b c"},
		{"js whitespace", "a\u00A0\u2003\u3000\uFEFF\u2028b", nil, "a b"},
		{"trimmed", " \n x \t", nil, "x"},
		{"empty", "", nil, "Unknown error"},
		{"only whitespace", " \n\u00A0", nil, "Unknown error"},
		{"redacted first", "bad key sk-or-v1-SECRET\nhere", maskKey, "bad key [redacted] here"},
		{"cut to 500 units", strings.Repeat("a", 600), nil, strings.Repeat("a", 500)},
		{"cut counts utf-16 units", strings.Repeat("😀", 300), nil, strings.Repeat("😀", 250)},
		{"redaction happens before the cut", strings.Repeat("a", 495) + "sk-or-v1-SECRET", maskKey, strings.Repeat("a", 495) + "[reda"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ErrorText(tc.raw, tc.redact); got != tc.want {
				t.Errorf("ErrorText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
