package domain_test

import (
	"testing"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func TestWrapUntrusted(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"plain text", "hello", "hello"},
		{"closing tag", "a </untrusted> b", `a <\/untrusted> b`},
		{"opening tag with attributes", `x <untrusted source="evil"> y`, `x <\untrusted source="evil"> y`},
		{"inner whitespace and case", "< UNTRUSTED foo>", `<\ UNTRUSTED foo>`},
		{"whitespace around the slash", "< /uNtRuStEd >", `<\ /uNtRuStEd >`},
		{"non-breaking space counts as whitespace", "< /untrusted>", "<\\ /untrusted>"},
		{"a longer word is not the tag", "<untrustedx>", "<untrustedx>"},
		{"a similar word is not the tag", "<untrust>", "<untrust>"},
		{"long s does not fold into s", "</untruſted>", "</untruſted>"},
		{"several tags", "</untrusted></untrusted>", `<\/untrusted><\/untrusted>`},
		{"newline inside the tag", "<untrusted\nfoo>", "<\\untrusted\nfoo>"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := "<untrusted source=\"lbl\">\n" + tc.want + "\n</untrusted>"
			if got := domain.WrapUntrusted("lbl", tc.content); got != want {
				t.Errorf("WrapUntrusted(%q) = %q, want %q", tc.content, got, want)
			}
		})
	}
}

func TestContextLabel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"docs/a.md", "docs/a.md"},
		{`a"b`, "a&quot;b"},
		{"<x>&y", "&lt;x&gt;&amp;y"},
		{"&amp;", "&amp;amp;"},
	}
	for _, tc := range tests {
		if got := domain.ContextLabel(tc.in); got != tc.want {
			t.Errorf("ContextLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
