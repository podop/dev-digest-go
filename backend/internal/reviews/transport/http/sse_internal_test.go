package reviewshttp

import "testing"

func TestUnescapeLineSeparators(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"none", `abc`, `abc`},
		{"2028", `a b`, "a b"},
		{"2029", `a b`, "a b"},
		{"escaped backslash before u2028 text", `\\u2028`, `\\u2028`},
		{"escaped backslash then a real escape", `\\ `, "\\\\ "},
		{"other escapes", `\n\"\u0001‪`, `\n\"\u0001‪`},
		{"truncated", `\u202`, `\u202`},
		{"trailing backslash", `a\`, `a\`},
	}
	for _, tc := range tests {
		if got := string(unescapeLineSeparators([]byte(tc.in))); got != tc.want {
			t.Errorf("unescapeLineSeparators(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
