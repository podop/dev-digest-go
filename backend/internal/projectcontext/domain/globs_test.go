package domain_test

import (
	"reflect"
	"testing"

	"dev-digest-go/backend/internal/projectcontext/domain"
)

func TestGlobSet_Match(t *testing.T) {
	tests := []struct {
		glob  string
		path  string
		match bool
	}{
		{"**/*.md", "a.md", true},
		{"**/*.md", "a/b/c.md", true},
		{"**/*.md", "a/b/c.txt", false},
		{"docs/**/x.md", "docs/x.md", true},
		{"docs/**/x.md", "docs/a/b/x.md", true},
		{"docs/**", "docs/a/b.md", true},
		{"docs/**", "docs/", true},
		{"docs/**", "doc/a", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/a/b.md", false},
		{"docs/?.md", "docs/a.md", true},
		{"docs/?.md", "docs/ab.md", false},
		{"docs/?.md", "docs//.md", false},
		{"{a,b}/**/*.md", "a/x.md", true},
		{"{a,b}/**/*.md", "b/y/x.md", true},
		{"{a,b}/**/*.md", "c/x.md", false},
		{"*.md", ".hidden.md", true},
		{"a.md", "A.md", false},
		{"a.md", "axmd", false},
		{"a.md", "dir/a.md", false},
		{"a.md", "a.md.bak", false},
		{"x+(y).md", "x+(y).md", true},
		{"a{b", "a{b", true},
		{"a}b", "a}b", true},
		{"{a,{b,c}}", "a}", true}, // alternations do not nest: the inner braces are literal
		{"{a,{b,c}}", "a", false},
		{"a,b", "a,b", true},
		{"a,b", "a", false},
		{"é*.md", "éa.md", true},
		{"**/b/**/*.md", "b/x.md", true},
		{"**/b/**/*.md", "a/b/x.md", true},
	}
	for _, tc := range tests {
		gs, err := domain.NewGlobSet([]string{tc.glob})
		if err != nil {
			t.Fatalf("NewGlobSet(%q) error: %v", tc.glob, err)
		}
		if got := gs.Match(tc.path); got != tc.match {
			t.Errorf("glob %q Match(%q) = %v, want %v", tc.glob, tc.path, got, tc.match)
		}
	}
}

func TestGlobSet_EmptyMatchesNothing(t *testing.T) {
	gs, err := domain.NewGlobSet(nil)
	if err != nil {
		t.Fatal(err)
	}
	if gs.Match("docs/a.md") {
		t.Errorf("empty GlobSet matched a path")
	}
}

func TestGlobSet_Globs(t *testing.T) {
	in := []string{"docs/*.md", "{a,b}/**/*.md"}
	gs, err := domain.NewGlobSet(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := gs.Globs(); !reflect.DeepEqual(got, in) {
		t.Errorf("Globs() = %v, want %v", got, in)
	}
	in[0] = "changed"
	if gs.Globs()[0] != "docs/*.md" {
		t.Errorf("GlobSet shares the caller's slice")
	}
}
