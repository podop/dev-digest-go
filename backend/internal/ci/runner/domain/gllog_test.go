package domain_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"dev-digest-go/backend/internal/ci/runner/domain"
)

func TestGitLabLogText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is unchanged", "GitLab API error posting MR review (/projects/42/x): 500 boom", "GitLab API error posting MR review (/projects/42/x): 500 boom"},
		{"non-ASCII text is unchanged", "ошибка — 失敗 😀", "ошибка — 失敗 😀"},
		{"empty", "", ""},
		{"colour sequence", "\x1b[31mred\x1b[0m done", "red done"},
		{"erase-line sequence of a section line", "\x1b[0Ksection_start:1700000000:x\x1b[0K", "1700000000:x"},
		{"CSI with intermediates", "a\x1b[1;2 qb", "ab"},
		{"truncated CSI", "a\x1b[12", "a"},
		{"lone ESC", "a\x1bb", "ab"},
		{"ESC at the end", "a\x1b", "a"},
		{"OSC title keeps its text but not the ESC", "\x1b]0;title\x07x", "]0;titlex"},
		{"LF", "a\nb", "a↵b"},
		{"CR", "a\rb", "a↵b"},
		{"CR LF is one break", "a\r\nb", "a↵b"},
		{"LF CR is two", "a\n\rb", "a↵↵b"},
		{"NEL, LS and PS", "a\u0085b\u2028c\u2029d", "a↵b↵c↵d"},
		{"C0 controls and DEL", "a\x00b\x01c\x08d\te\x0bf\x0cg\x7fh", "abcdefgh"},
		{"C1 controls", "a\u0080b\u009bc\u009cd\u009fe", "abcde"},
		{"section_start", "x section_start:1:x y", "x 1:x y"},
		{"section_end", "x section_end:1:x y", "x 1:x y"},
		{"both from AC28", "section_start:1:x / section_end:1:x", "1:x / 1:x"},
		{"nested start", "section_section_start:start:1:x", "1:x"},
		{"nested end", "section_sectionsection_end:_end:z", "section_z"},
		{"mixed nesting", "section_section_end:start:q", "q"},
		{"marker split by a control", "section_\x01start:1", "1"},
		{"marker split by ESC", "section_\x1bstart:1", "1"},
		{"marker split by a CSI sequence", "section_\x1b[0Kend:1", "1"},
		{"marker split by a line break is not a marker", "section_\nstart:1", "section_↵start:1"},
		{"upper case is not a marker", "SECTION_START:1", "SECTION_START:1"},
		{"near miss", "section_start 1", "section_start 1"},
		{"invalid UTF-8", "a\xffb\xc3", "a�b�"},
		{"lone C1 byte", "a\x9bb", "a�b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.GitLabLogText(tc.in); got != tc.want {
				t.Errorf("GitLabLogText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestGitLabLogText_AC28 feeds the AC28 error body: ESC sequences, CR, C1 controls, line breaks
// and section markers, and checks that nothing a log renderer acts on is left.
func TestGitLabLogText_AC28(t *testing.T) {
	body := "line1\r\nline2\x1b[2K\rsection_start:1:x\u009b31m\nsection_end:1:x\x1b]8;;http://e\x07"
	got := domain.GitLabLogText(body)
	checkLogSafe(t, body, got)
}

func TestGitLabLogText_LinearOnNastyInput(t *testing.T) {
	// A long run of nested half markers must not take quadratic time (this finishes instantly if
	// the removal is one pass).
	in := strings.Repeat("section_", 200000) + strings.Repeat("start:", 200000)
	got := domain.GitLabLogText(in)
	if strings.Contains(got, "section_start:") {
		t.Errorf("GitLabLogText(nested markers) still contains a marker")
	}
}

func checkLogSafe(t *testing.T, in, got string) {
	t.Helper()
	if !utf8.ValidString(got) {
		t.Fatalf("GitLabLogText(%q) = %q is not valid UTF-8", in, got)
	}
	if strings.Contains(got, "section_start:") || strings.Contains(got, "section_end:") {
		t.Fatalf("GitLabLogText(%q) = %q holds a section marker", in, got)
	}
	for _, r := range got {
		switch {
		case r == '\n' || r == '\r' || r == 0x85 || r == 0x2028 || r == 0x2029,
			r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			t.Fatalf("GitLabLogText(%q) = %q holds %U", in, got, r)
		}
	}
}

func FuzzGitLabLogText(f *testing.F) {
	for _, s := range []string{
		"", "plain", "\x1b[31m", "a\r\nb", "section_start:1:x", "section_section_start:start:", "section_\x1b[0Kend:",
		"\x1b[0Ksection_end:1:x\r", "\u0085\u2028\xff\x9b", "sec\x00tion_start:", "\x1b",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := domain.GitLabLogText(in)
		checkLogSafe(t, in, got)
		if again := domain.GitLabLogText(got); again != got {
			t.Fatalf("GitLabLogText is not idempotent: %q -> %q -> %q", in, got, again)
		}
	})
}
