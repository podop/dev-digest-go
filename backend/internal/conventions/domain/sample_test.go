package domain_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/conventions/domain"
)

func TestPickConfigFiles(t *testing.T) {
	paths := []string{
		"server/AGENTS.md", "tsconfig.base.json", "package.json", "a/b/c/tsconfig.json", "src/a.ts",
		"README.md", "eslint.config.mjs", ".prettierrc.json", "web/.eslintrc.cjs", "Cargo.toml",
	}
	got := domain.PickConfigFiles(paths)
	want := []string{
		".prettierrc.json", "eslint.config.mjs", "package.json", "tsconfig.base.json",
		"server/AGENTS.md", "web/.eslintrc.cjs",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PickConfigFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestPickConfigFiles_CapsAtEight(t *testing.T) {
	var paths []string
	for i := range 12 {
		paths = append(paths, fmt.Sprintf("d%02d/package.json", i))
	}
	if got := domain.PickConfigFiles(paths); len(got) != 8 {
		t.Errorf("PickConfigFiles returned %d files, want 8", len(got))
	}
}

func TestPickFallbackSources(t *testing.T) {
	paths := []string{
		"vite.config.ts", "src/a.ts", "src/a.test.ts", "src/b.min.js", "lib/c.go", "lib/c_test.go",
		"src/types.d.ts", "gen/generated/x.ts", "src/l.ts", "README.md", "src/deep/z.py", "main.rs",
	}
	got := domain.PickFallbackSources(paths, 12)
	want := []string{"main.rs", "lib/c.go", "src/a.ts", "src/deep/z.py", "src/l.ts"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PickFallbackSources mismatch (-want +got):\n%s", diff)
	}
}

func TestPickFallbackSources_RoundRobin(t *testing.T) {
	var paths []string
	for _, f := range []string{"a1", "a2", "a3"} {
		paths = append(paths, "a/"+f+".ts")
	}
	paths = append(paths, "b/b1.ts", "b/b2.ts")
	got := domain.PickFallbackSources(paths, 4)
	want := []string{"a/a1.ts", "b/b1.ts", "a/a2.ts", "b/b2.ts"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PickFallbackSources(n=4) mismatch (-want +got):\n%s", diff)
	}
}

func TestSkipDir(t *testing.T) {
	for name, want := range map[string]bool{
		"node_modules": true, ".git": true, ".github": true, "target": true, "__snapshots__": true,
		"src": false, "Vendor": false, "lib": false,
	} {
		if got := domain.SkipDir(name); got != want {
			t.Errorf("SkipDir(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNumberLines(t *testing.T) {
	var src []string
	for i := 1; i <= 12; i++ {
		src = append(src, fmt.Sprintf("l%d", i))
	}
	got := domain.NumberLines(strings.Join(src, "\r\n")+"\r", 100000)
	lines := strings.Split(got, "\n")
	if lines[0] != " 1| l1" || lines[9] != "10| l10" || lines[12] != "13| " {
		t.Errorf("NumberLines gutter = %q ... %q ... %q, want right-aligned to width 2", lines[0], lines[9], lines[len(lines)-1])
	}
}

func TestNumberLines_Truncates(t *testing.T) {
	content := strings.Repeat(strings.Repeat("x", 49)+"\n", 200)
	got := domain.NumberLines(content, 1000)
	lines := strings.Split(got, "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "… (truncated, ") || !strings.HasSuffix(last, " more lines)") {
		t.Fatalf("NumberLines last line = %q, want the truncation marker", last)
	}
	if utf8.RuneCountInString(got) > 1000+len(last) {
		t.Errorf("NumberLines produced %d chars, want about 1000", utf8.RuneCountInString(got))
	}
	wantMore := fmt.Sprintf("… (truncated, %d more lines)", 201-(len(lines)-1))
	if last != wantMore {
		t.Errorf("NumberLines marker = %q, want %q", last, wantMore)
	}
}

func TestRenderSample_Budgets(t *testing.T) {
	big := strings.Repeat(strings.Repeat("y", 99)+"\n", 100) // 10,000 chars
	small := strings.Repeat(strings.Repeat("z", 49)+"\n", 100)
	files := []domain.SampledFile{{Path: "big.ts", Kind: domain.KindSource, Content: big}}
	for i := range 40 {
		files = append(files, domain.SampledFile{Path: fmt.Sprintf("f%02d.ts", i), Kind: domain.KindSource, Content: small})
	}
	got := domain.RenderSample(files)

	blocks := strings.Split(got.Text, "\n\n=== FILE: ")
	if first := blocks[0]; !strings.Contains(first, "more lines)") || utf8.RuneCountInString(first) > 6100 {
		t.Errorf("RenderSample first block (%d chars) should end truncated within the 6,000 cap", utf8.RuneCountInString(first))
	}
	if n := utf8.RuneCountInString(got.Text); n > 90_000 {
		t.Errorf("RenderSample text = %d chars, want at most 90000", n)
	}
	if len(got.Included) >= len(files) || len(got.Included) == 0 {
		t.Fatalf("RenderSample included %d of %d files, want a strict, non-empty prefix", len(got.Included), len(files))
	}
	if len(blocks) != len(got.Included) {
		t.Errorf("RenderSample has %d blocks for %d included paths", len(blocks), len(got.Included))
	}
	for i, p := range got.Included {
		if p != files[i].Path {
			t.Errorf("RenderSample included[%d] = %q, want %q", i, p, files[i].Path)
		}
	}
}

func TestRenderSample_Format(t *testing.T) {
	got := domain.RenderSample([]domain.SampledFile{
		{Path: "package.json", Kind: domain.KindConfig, Content: "{}\n"},
		{Path: "src/a.ts", Kind: domain.KindSource, Content: "let a\nlet b"},
	})
	want := "=== FILE: package.json (config) ===\n1| {}\n2| \n\n=== FILE: src/a.ts (source) ===\n1| let a\n2| let b"
	if got.Text != want {
		t.Errorf("RenderSample text = %q, want %q", got.Text, want)
	}
	if diff := cmp.Diff([]string{"package.json", "src/a.ts"}, got.Included); diff != "" {
		t.Errorf("RenderSample included mismatch (-want +got):\n%s", diff)
	}
}
