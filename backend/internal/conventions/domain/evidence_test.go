package domain_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/conventions/domain"
)

func TestSafeRelativePath(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{name: "plain", in: "src/a.ts", want: "src/a.ts", wantOK: true},
		{name: "trimmed and dot-slash removed", in: "  ./././src/a.ts ", want: "src/a.ts", wantOK: true},
		{name: "backslashes", in: `src\a.ts`, want: "src/a.ts", wantOK: true},
		{name: "empty", in: "  ", wantOK: false},
		{name: "parent", in: "../etc/passwd", wantOK: false},
		{name: "inner parent", in: "src/../a.ts", wantOK: false},
		{name: "absolute", in: "/etc/passwd", wantOK: false},
		{name: "drive letter", in: `C:\Windows\x.ts`, wantOK: false},
		{name: "nul", in: "a\x00b.ts", wantOK: false},
		{name: "empty segment", in: "src//a.ts", wantOK: false},
		{name: "dot segment", in: "src/./a.ts", wantOK: false},
		{name: "trailing slash", in: "src/", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.SafeRelativePath(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("SafeRelativePath(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func fileOf(n int, set map[int]string) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "// filler line number " + strings.Repeat("x", i%3)
		if s, ok := set[i+1]; ok {
			lines[i] = s
		}
	}
	return lines
}

func TestVerifyEvidence(t *testing.T) {
	getUser := "  export function getUser(id: string) {"
	tests := []struct {
		name       string
		file       []string
		claimed    domain.ClaimedEvidence
		want       domain.Evidence
		wantReason domain.DropReason
	}{
		{
			name:    "relocated to the real line and dedented",
			file:    fileOf(20, map[int]string{10: getUser}),
			claimed: domain.ClaimedEvidence{Path: "a.ts", StartLine: 9, EndLine: 9, Snippet: "export function getUser(id: string) {"},
			want:    domain.Evidence{Path: "a.ts", StartLine: 10, EndLine: 10, Snippet: "export function getUser(id: string) {"},
		},
		{
			name:       "snippet mismatch inside the file",
			file:       fileOf(20, nil),
			claimed:    domain.ClaimedEvidence{Path: "a.ts", StartLine: 3, Snippet: "this text is nowhere"},
			wantReason: domain.ReasonSnippetMismatch,
		},
		{
			name:       "start line past the end",
			file:       fileOf(20, nil),
			claimed:    domain.ClaimedEvidence{Path: "a.ts", StartLine: 500, Snippet: "this text is nowhere"},
			wantReason: domain.ReasonLineOutOfRange,
		},
		{
			name:       "blank snippet is invalid",
			file:       fileOf(5, nil),
			claimed:    domain.ClaimedEvidence{Path: "a.ts", StartLine: 1, Snippet: " \r\n  12|  \n"},
			wantReason: domain.ReasonInvalid,
		},
		{
			name:    "line-number prefix and CRLF are ignored",
			file:    []string{"a", "const x = 1;", "const y = 2;", "b"},
			claimed: domain.ClaimedEvidence{Path: "p", StartLine: 2, Snippet: " 2| const x = 1;\r\n 3| const y = 2;"},
			want:    domain.Evidence{Path: "p", StartLine: 2, EndLine: 3, Snippet: "const x = 1;\nconst y = 2;"},
		},
		{
			name:    "whitespace is collapsed",
			file:    []string{"if   (a)\t{"},
			claimed: domain.ClaimedEvidence{Path: "p", StartLine: 1, Snippet: "if (a) {"},
			want:    domain.Evidence{Path: "p", StartLine: 1, EndLine: 1, Snippet: "if   (a)\t{"},
		},
		{
			name:    "a long snippet line matches as a substring",
			file:    []string{"x", "    return computeTotal(items, taxRate); // note"},
			claimed: domain.ClaimedEvidence{Path: "p", StartLine: 2, Snippet: "computeTotal(items, taxRate);"},
			want:    domain.Evidence{Path: "p", StartLine: 2, EndLine: 2, Snippet: "return computeTotal(items, taxRate); // note"},
		},
		{
			name:       "a short snippet line must match exactly",
			file:       []string{"foo(bar)"},
			claimed:    domain.ClaimedEvidence{Path: "p", StartLine: 1, Snippet: "foo(bar"},
			wantReason: domain.ReasonSnippetMismatch,
		},
		{
			name:    "nearest match wins",
			file:    []string{"same line here!", "x", "x", "same line here!", "x", "x", "x", "same line here!"},
			claimed: domain.ClaimedEvidence{Path: "p", StartLine: 6, Snippet: "same line here!"},
			want:    domain.Evidence{Path: "p", StartLine: 4, EndLine: 4, Snippet: "same line here!"},
		},
		{
			name:    "earlier match wins on a tie",
			file:    []string{"same line here!", "x", "same line here!"},
			claimed: domain.ClaimedEvidence{Path: "p", StartLine: 2, Snippet: "same line here!"},
			want:    domain.Evidence{Path: "p", StartLine: 1, EndLine: 1, Snippet: "same line here!"},
		},
		{
			name:    "blank lines between matched lines are kept in the stored text",
			file:    []string{"\tfirst line of it", "", "\t\tsecond line of it   ", "tail"},
			claimed: domain.ClaimedEvidence{Path: "p", StartLine: 1, Snippet: "first line of it\nsecond line of it"},
			want:    domain.Evidence{Path: "p", StartLine: 1, EndLine: 3, Snippet: "first line of it\n\n\tsecond line of it"},
		},
		{
			name: "stored lines are capped at 12",
			file: func() []string {
				var l []string
				for i := 1; i <= 20; i++ {
					l = append(l, "line number "+strings.Repeat("a", i))
				}
				return l
			}(),
			claimed: domain.ClaimedEvidence{Path: "p", StartLine: 1, Snippet: func() string {
				var l []string
				for i := 1; i <= 20; i++ {
					l = append(l, "line number "+strings.Repeat("a", i))
				}
				return strings.Join(l, "\n")
			}()},
			want: domain.Evidence{Path: "p", StartLine: 1, EndLine: 12, Snippet: func() string {
				var l []string
				for i := 1; i <= 12; i++ {
					l = append(l, "line number "+strings.Repeat("a", i))
				}
				return strings.Join(l, "\n")
			}()},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason, ok := domain.VerifyEvidence(tc.file, tc.claimed)
			if ok != (tc.wantReason == "") {
				t.Fatalf("VerifyEvidence ok = %v (reason %q), want reason %q", ok, reason, tc.wantReason)
			}
			if reason != tc.wantReason {
				t.Errorf("VerifyEvidence reason = %q, want %q", reason, tc.wantReason)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("VerifyEvidence evidence mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSplitLines(t *testing.T) {
	got := domain.SplitLines("a\r\nb\rc\n")
	want := []string{"a", "b", "c", ""}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SplitLines mismatch (-want +got):\n%s", diff)
	}
}

func TestRuleKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Use  camelCase, always!", "use camelcase always"},
		{"  --Wrap--errors--  ", "wrap errors"},
		{"!!!", ""},
		{"Ünï 2", "n 2"},
	}
	for _, tc := range tests {
		if got := domain.RuleKey(tc.in); got != tc.want {
			t.Errorf("RuleKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
