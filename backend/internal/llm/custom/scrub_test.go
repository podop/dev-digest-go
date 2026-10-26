package custom_test

import (
	"strconv"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/llm/custom"
)

func TestScrub(t *testing.T) {
	t.Parallel()
	const key = "gwkey-ABCDEFGH-1234-wxyz"
	tests := []struct {
		name    string
		in      string
		secrets []string
		want    string
	}{
		{"exact", "bad " + key + " and " + key, []string{key}, "bad [R] and [R]"},
		{"plain dots mask keeps neither end", "Incorrect API key provided: gwkey-AB...wxyz", []string{key}, "Incorrect API key provided: [R]...[R]"},
		{"quoted form", "http2: invalid Upgrade request header: " + strconv.Quote("top\xffsecret-9876"), []string{"top\xffsecret-9876"}, "http2: invalid Upgrade request header: \"[R]\""},
		{"escaped form of a secret with a quote", `got "a\"b-secret"`, []string{`a"b-secret`}, `got "[R]"`},
		{"short secret is only matched whole", "abc abcd", []string{"abc"}, "[R] [R]d"},
		{"the longest secret first", "x ab-cd-ef y", []string{"ab-cd", "ab-cd-ef"}, "x [R] y"},
		{"plain text stays", "model not found", []string{key}, "model not found"},
		{"empty secret is ignored", "abc", []string{""}, "abc"},
		{"the mask is not eaten by a later fragment", "[R] gwke", []string{"RRRR", key}, "[R] [R]"},
	}
	for _, tc := range tests {
		if got := custom.Scrub(tc.in, tc.secrets, "[R]"); got != tc.want {
			t.Errorf("Scrub(%q, %q) = %q, want %q", tc.in, tc.secrets, got, tc.want)
		}
	}
}

func TestScrubWholeKeepsJSONStructure(t *testing.T) {
	t.Parallel()
	in := `{"error":{"message":"bad error-token"}}`
	got := custom.ScrubWhole(in, []string{"my-error-token"}, "[R]")
	if got != in {
		t.Errorf("ScrubWhole(%q) = %q, want it unchanged (no fragment pass)", in, got)
	}
	if got := custom.Scrub(in, []string{"my-error-token"}, "[R]"); !strings.Contains(got, "[R]") || strings.Contains(got, "error-token") {
		t.Errorf("Scrub(%q) = %q, want the fragment masked", in, got)
	}
}
