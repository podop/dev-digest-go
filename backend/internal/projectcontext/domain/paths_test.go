package domain_test

import (
	"errors"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/projectcontext/domain"
)

func defaultGlobs(t *testing.T) *domain.GlobSet {
	t.Helper()
	gs, err := domain.NewGlobSet(domain.DefaultGlobs)
	if err != nil {
		t.Fatalf("NewGlobSet(default) error: %v", err)
	}
	return gs
}

func TestIsListablePath_DefaultGlobs(t *testing.T) {
	gs := defaultGlobs(t)
	tests := []struct {
		path string
		want bool
	}{
		{"docs/a.md", true},
		{"x/specs/y/b.md", true},
		{".devdigest/specs/s.md", true},
		{"a/b/insights/c/d/e.md", true},
		{"node_modules/docs/a.md", false},
		{"docs/node_modules/a.md", false},
		{"docs/dist/a.md", false},
		{"docs/a.MD", false},
		{"docs/../a.md", false},
		{"docs//a.md", false},
		{"readme.md", false},
		{"docs/a.txt", false},
		{"/docs/a.md", false},
		{`docs\a.md`, false},
		{"docs/a\x00.md", false},
		{"", false},
		{"docs/" + strings.Repeat("a", 505) + ".md", false},
		{"docs/" + strings.Repeat("a", 504) + ".md", true},
	}
	for _, tc := range tests {
		if got := domain.IsListablePath(tc.path, gs); got != tc.want {
			t.Errorf("IsListablePath(%.40q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestPathLengthCountsUTF16Units(t *testing.T) {
	// 300 emoji = 600 UTF-16 units, 1200 bytes; 255 emoji = 510 units.
	long := "docs/" + strings.Repeat("😀", 300) + ".md"
	short := "docs/" + strings.Repeat("😀", 250) + ".md"
	if domain.CheckPath(long) {
		t.Errorf("CheckPath(300 emoji) = true, want false")
	}
	if !domain.CheckPath(short) {
		t.Errorf("CheckPath(250 emoji) = false, want true")
	}
}

func TestDocTypeOf(t *testing.T) {
	tests := []struct {
		path string
		want domain.DocType
	}{
		{"a/specs/docs/x.md", domain.DocTypeDocs},
		{"insights/x.md", domain.DocTypeInsights},
		{"docs/specs/x.md", domain.DocTypeSpecs},
		{"specs/other/x.md", domain.DocTypeSpecs},
		{"specs.md", domain.DocTypeDocs},
		{"a/b/specs", domain.DocTypeDocs},
		{"x/mydocs/y.md", domain.DocTypeDocs},
	}
	for _, tc := range tests {
		if got := domain.DocTypeOf(tc.path); got != tc.want {
			t.Errorf("DocTypeOf(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestDocName(t *testing.T) {
	for in, want := range map[string]string{"a/b/c.md": "c.md", "c.md": "c.md"} {
		if got := domain.DocName(in); got != want {
			t.Errorf("DocName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckStorePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{".devdigest/specs/a.md", true},
		{".devdigest/specs/a/b/c/d/e/f.md", true},    // 5 folders
		{".devdigest/specs/a/b/c/d/e/g/f.md", false}, // 6 folders
		{".devdigest/specs/My_Spec-1.0.md", true},
		{".devdigest/specs/.md", true},
		{".devdigest/specs/a b.md", false},
		{".devdigest/specs/ä.md", false},
		{".devdigest/specs/../a.md", false},
		{".devdigest/specs/./a.md", false},
		{".devdigest/specs//a.md", false},
		{".devdigest/specs/a.MD", false},
		{".devdigest/specs/a.txt", false},
		{".devdigest/spec/a.md", false},
		{"docs/a.md", false},
		{".devdigest/specs/" + strings.Repeat("a", 500) + ".md", false},
	}
	for _, tc := range tests {
		if got := domain.CheckStorePath(tc.path); got != tc.want {
			t.Errorf("CheckStorePath(%.50q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestWithSuffix(t *testing.T) {
	if got, want := domain.WithSuffix(".devdigest/specs/a/b.md", 2), ".devdigest/specs/a/b-2.md"; got != want {
		t.Errorf("WithSuffix = %q, want %q", got, want)
	}
}

func TestHasNul(t *testing.T) {
	if !domain.HasNul("a\x00b") || domain.HasNul("ab") {
		t.Errorf("HasNul mismatch")
	}
}

func TestValidateAttachmentPaths(t *testing.T) {
	gs := defaultGlobs(t)
	many := make([]string, 51)
	for i := range many {
		many[i] = "docs/" + strings.Repeat("a", i+1) + ".md"
	}
	tests := []struct {
		name  string
		paths []string
		want  error
	}{
		{"empty", nil, nil},
		{"listable and store", []string{"docs/a.md", ".devdigest/specs/x.md"}, nil},
		{"more than 50", many, domain.ErrTooManyPaths},
		{"exactly 50", many[:50], nil},
		{"not listable", []string{"docs/a.md", "readme.md"}, domain.ErrInvalidPath},
		{"duplicate", []string{"docs/a.md", "docs/b.md", "docs/a.md"}, domain.ErrDuplicatePath},
		{"invalid before duplicate", []string{"docs/a.md", "bad.md", "docs/a.md"}, domain.ErrInvalidPath},
		{"count is checked first", append(append([]string{}, many...), "bad"), domain.ErrTooManyPaths},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ValidateAttachmentPaths(tc.paths, gs); !errors.Is(got, tc.want) {
				t.Errorf("ValidateAttachmentPaths(%d paths) = %v, want %v", len(tc.paths), got, tc.want)
			}
		})
	}
}
