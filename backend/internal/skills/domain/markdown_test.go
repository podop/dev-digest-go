package domain_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/skills/domain"
)

func importMessage(t *testing.T, err error) string {
	t.Helper()
	var ie *domain.ImportError
	if !errors.As(err, &ie) {
		t.Fatalf("error = %v (%T), want *domain.ImportError", err, err)
	}
	return ie.Message
}

func TestSkillFromMarkdown_AC250(t *testing.T) {
	input := "---\nname: \"My Skill\"\ntype: Rubric\n---\n<!-- hidden -->Be careful.​ Really.\n\n## Second\n"
	got, err := domain.SkillFromMarkdown(input, "My Skill")
	if err != nil {
		t.Fatalf("SkillFromMarkdown() error = %v", err)
	}
	want := domain.Candidate{
		Name:        "my-skill",
		Description: "Be careful. Really.",
		Type:        domain.TypeCustom,
		Body:        "Be careful. Really.\n\n## Second",
		Warnings: []string{
			"Removed 1 HTML comment(s) (hidden text)",
			"Removed 1 zero-width character(s)",
			`Name normalized to "my-skill"`,
			`Unknown type "Rubric" — using "custom"`,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SkillFromMarkdown() mismatch (-want +got):\n%s", diff)
	}
}

func TestSkillFromMarkdown_Limits(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantMsg string
	}{
		{"body over 20000 chars", strings.Repeat("a", 20_001), "The skill body is 20001 chars; the limit is 20000"},
		{"body counts UTF-16 units", strings.Repeat("😀", 10_001), "The skill body is 20002 chars; the limit is 20000"},
		{"empty after front matter", "---\nname: x\n---\n  \n", "The skill has no body"},
		{"only a comment", "<!-- nothing -->", "The skill has no body"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.SkillFromMarkdown(tc.input, "f")
			if got := importMessage(t, err); got != tc.wantMsg {
				t.Errorf("SkillFromMarkdown() message = %q, want %q", got, tc.wantMsg)
			}
		})
	}

	if _, err := domain.SkillFromMarkdown(strings.Repeat("a", 20_000), "f"); err != nil {
		t.Errorf("SkillFromMarkdown(20000 chars) error = %v, want none", err)
	}
}

func TestSkillFromMarkdown_Description(t *testing.T) {
	long := strings.Repeat("x", 400)
	got, err := domain.SkillFromMarkdown("---\ndescription: "+long+"\n---\nbody", "f")
	if err != nil {
		t.Fatalf("SkillFromMarkdown() error = %v", err)
	}
	if want := strings.Repeat("x", 299) + "…"; got.Description != want {
		t.Errorf("description = %d chars, want 299 x plus an ellipsis", len([]rune(got.Description)))
	}
	if diff := cmp.Diff([]string{"Description shortened to 300 chars"}, got.Warnings); diff != "" {
		t.Errorf("warnings mismatch (-want +got):\n%s", diff)
	}

	t.Run("exactly 300 is kept", func(t *testing.T) {
		got, err := domain.SkillFromMarkdown("---\ndescription: "+strings.Repeat("y", 300)+"\n---\nbody", "f")
		if err != nil || len(got.Description) != 300 || len(got.Warnings) != 0 {
			t.Errorf("description = %d bytes, warnings = %v, err = %v; want 300, none, nil", len(got.Description), got.Warnings, err)
		}
	})

	t.Run("cut trims trailing space and never splits a pair", func(t *testing.T) {
		desc := strings.Repeat("a", 297) + "  😀😀"
		got, err := domain.SkillFromMarkdown("---\ndescription: "+desc+"\n---\nbody", "f")
		if err != nil {
			t.Fatalf("SkillFromMarkdown() error = %v", err)
		}
		if want := strings.Repeat("a", 297) + "…"; got.Description != want {
			t.Errorf("description = %q, want %q", got.Description, want)
		}
	})
}

func TestSkillFromMarkdown_Fallbacks(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		fallback     string
		wantName     string
		wantDesc     string
		wantType     domain.SkillType
		wantWarnings []string
	}{
		{"fallback name and first paragraph", "# Title\n\n```go\n\nProse   line\none more\n\nlater", "Some_File.v2", "some-file-v2", "Prose line one more", domain.TypeCustom, nil},
		{"valid type", "---\ntype: security\n---\nbody", "x", "x", "body", domain.TypeSecurity, nil},
		{"unchanged name adds no warning", "---\nname: plain-name\n---\nbody", "x", "plain-name", "body", domain.TypeCustom, nil},
		{"quotes and key case", "---\nName: 'Q Name'\nTYPE:rubric\n---\nbody", "x", "q-name", "body", domain.TypeRubric, []string{`Name normalized to "q-name"`}},
		{"empty value is ignored", "---\nname:\ndescription:   \n---\nbody", "fb", "fb", "body", domain.TypeCustom, nil},
		{"CRLF", "---\r\nname: crlf\r\n---\r\nbody\r\n", "x", "crlf", "body", domain.TypeCustom, nil},
		{"unterminated front matter is body", "---\nname: x\nbody", "fb", "fb", "--- name: x body", domain.TypeCustom, nil},
		{"bidi controls", "a‮b⁦c", "x", "x", "abc", domain.TypeCustom, []string{"Removed 2 bidi control character(s)"}},
		{"unterminated comment runs to the end", "keep<!-- never closed\nmore", "x", "x", "keep", domain.TypeCustom, []string{"Removed 1 HTML comment(s) (hidden text)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.SkillFromMarkdown(tc.input, tc.fallback)
			if err != nil {
				t.Fatalf("SkillFromMarkdown() error = %v", err)
			}
			if got.Name != tc.wantName || got.Description != tc.wantDesc || got.Type != tc.wantType {
				t.Errorf("name/description/type = %q/%q/%q, want %q/%q/%q", got.Name, got.Description, got.Type, tc.wantName, tc.wantDesc, tc.wantType)
			}
			if diff := cmp.Diff(tc.wantWarnings, got.Warnings); diff != "" {
				t.Errorf("warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeUTF8(t *testing.T) {
	tests := []struct {
		name   string
		in     []byte
		want   string
		wantOK bool
	}{
		{"plain", []byte("héllo"), "héllo", true},
		{"BOM dropped", []byte("\xEF\xBB\xBFhi"), "hi", true},
		{"invalid", []byte{0xff, 0xfe, 'a'}, "", false},
		{"truncated rune", []byte{0xe2, 0x82}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.DecodeUTF8(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("DecodeUTF8(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFileNames(t *testing.T) {
	tests := []struct {
		path              string
		ext, stem         string
		isMarkdown, isZip bool
	}{
		{"My Skill.md", ".md", "My Skill", true, false},
		{"dir/a.TXT", ".txt", "a", true, false},
		{"a.markdown", ".markdown", "a", true, false},
		{"pack.zip", ".zip", "pack", false, true},
		{".md", "", ".md", false, false},
		{"noext", "", "noext", false, false},
		{"a.pdf", ".pdf", "a", false, false},
		{"x/y.tar.gz", ".gz", "y.tar", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := domain.FileExt(tc.path); got != tc.ext {
				t.Errorf("FileExt(%q) = %q, want %q", tc.path, got, tc.ext)
			}
			if got := domain.StemOf(tc.path); got != tc.stem {
				t.Errorf("StemOf(%q) = %q, want %q", tc.path, got, tc.stem)
			}
			if got := domain.IsMarkdownName(tc.path); got != tc.isMarkdown {
				t.Errorf("IsMarkdownName(%q) = %v, want %v", tc.path, got, tc.isMarkdown)
			}
			if got := domain.IsZipName(tc.path); got != tc.isZip {
				t.Errorf("IsZipName(%q) = %v, want %v", tc.path, got, tc.isZip)
			}
		})
	}
}

var oldCommentRE = regexp.MustCompile(`(?s)<!--.*?(?:-->|\z)`)

// oldSanitizeMarkdown is SanitizeMarkdown before security review r2 (N1): the comment step was
// the regexp `(?s)<!--.*?(?:-->|\z)`, which costs 450 ns per byte above 17 KB. It is the oracle of
// the linear version.
func oldSanitizeMarkdown(input string) (string, []string) {
	var warnings []string
	text := strings.ReplaceAll(input, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if n := len(oldCommentRE.FindAllStringIndex(text, -1)); n > 0 {
		text = oldCommentRE.ReplaceAllString(text, "")
		warnings = append(warnings, fmt.Sprintf("Removed %d HTML comment(s) (hidden text)", n))
	}
	remove := func(pred func(rune) bool, what string) {
		n := 0
		text = strings.Map(func(r rune) rune {
			if pred(r) {
				n++
				return -1
			}
			return r
		}, text)
		if n > 0 {
			warnings = append(warnings, fmt.Sprintf("Removed %d %s", n, what))
		}
	}
	remove(func(r rune) bool { return (r >= 0x200B && r <= 0x200D) || r == 0x2060 || r == 0xFEFF }, "zero-width character(s)")
	remove(func(r rune) bool {
		return r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
	}, "bidi control character(s)")
	return text, warnings
}

func TestSanitizeMarkdown_MatchesTheRegexpVersion(t *testing.T) {
	pieces := []string{"<!--", "-->", "<!-", "--", "-", ">", "<!-->", "<!--->", "<", "!", "a", " ", "\n", "\r\n", "\r",
		"\u200B", "\u202E", "\xff", "\U0001F600", "<!-- x -->", "--!>", "<!---->"}
	rng := rand.New(rand.NewPCG(3, 4))
	corpus := []string{"", "<!--", "<!-->", "<!--->", "<!---->", "a<!--b-->c<!--d", strings.Repeat("<!--", 300), strings.Repeat("<!--a-->", 300)}
	for range 20_000 {
		var sb strings.Builder
		for range rng.IntN(24) {
			sb.WriteString(pieces[rng.IntN(len(pieces))])
		}
		corpus = append(corpus, sb.String())
	}
	for _, in := range corpus {
		wantText, wantWarnings := oldSanitizeMarkdown(in)
		gotText, gotWarnings := domain.SanitizeMarkdown(in)
		if gotText != wantText {
			t.Fatalf("SanitizeMarkdown(%q) = %q, the regexp version gives %q", in, gotText, wantText)
		}
		if diff := cmp.Diff(wantWarnings, gotWarnings); diff != "" {
			t.Fatalf("SanitizeMarkdown(%q) warnings differ from the regexp version (-regexp +linear):\n%s", in, diff)
		}
	}
}
