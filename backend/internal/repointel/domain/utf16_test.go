package domain

import "testing"

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "abc", 3},
		{"bmp", "héllo→", 6},
		{"astral is a surrogate pair", "a🚀b", 4},
		{"invalid byte counts one", "a\xffb", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UTF16Len(tc.in); got != tc.want {
				t.Errorf("UTF16Len(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestClampUTF16(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter", "abc", 5, "abc"},
		{"exact", "abc", 3, "abc"},
		{"cut ascii", "abcdef", 3, "abc"},
		{"zero", "abc", 0, ""},
		{"negative", "abc", -4, ""},
		{"bmp multibyte kept whole", "ééé", 2, "éé"},
		{"pair fits exactly", "a🚀", 3, "a🚀"},
		{"pair would be split", "a🚀", 2, "a"},
		{"pair at the start would be split", "🚀a", 1, ""},
		{"after a pair", "🚀🚀🚀", 4, "🚀🚀"},
		{"odd limit between pairs", "🚀🚀🚀", 5, "🚀🚀"},
		{"empty", "", 3, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClampUTF16(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("ClampUTF16(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if UTF16Len(got) > max(tc.max, 0) {
				t.Errorf("ClampUTF16(%q, %d) has %d units", tc.in, tc.max, UTF16Len(got))
			}
		})
	}
}

func TestSyntaxForPath(t *testing.T) {
	tests := []struct {
		path string
		want Syntax
		ok   bool
	}{
		{"a.ts", SyntaxTypeScript, true},
		{"src/A.TS", SyntaxTypeScript, true},
		{"a.tsx", SyntaxTSX, true},
		{"a.jsx", SyntaxTSX, true},
		{"a.JSX", SyntaxTSX, true},
		{"a.js", SyntaxJavaScript, true},
		{"a.mjs", SyntaxJavaScript, true},
		{"dir.d/a.cjs", SyntaxJavaScript, true},
		{"a.d.ts", SyntaxTypeScript, true},
		{"a.json", 0, false},
		{"ts", 0, false},
		{"a.", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got, ok := SyntaxForPath(tc.path)
			if got != tc.want || ok != tc.ok {
				t.Errorf("SyntaxForPath(%q) = %v, %v, want %v, %v", tc.path, got, ok, tc.want, tc.ok)
			}
		})
	}
}
