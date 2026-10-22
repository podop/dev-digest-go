package domain_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func TestLen16(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"é", 1},
		{"日本", 2},
		{"😀", 2},
		{"a😀b", 4},
		{"\xff", 1}, // an invalid byte counts one unit
	}
	for _, tc := range tests {
		if got := domain.Len16(tc.in); got != tc.want {
			t.Errorf("Len16(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0}, {"a", 1}, {"abcd", 1}, {"abcde", 2}, {strings.Repeat("😀", 4), 2},
	}
	for _, tc := range tests {
		if got := domain.EstimateTokens(tc.in); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestJSTrim(t *testing.T) {
	// U+0085 is whitespace for Go's unicode.IsSpace but not for JavaScript; U+FEFF is the reverse.
	tests := []struct{ in, want string }{
		{"  a  ", "a"},
		{"  a　\ufeff", "a"},
		{"\t\n\v\f\ra  ", "a"},
		{"\u0085a\u0085", "\u0085a\u0085"},
		{"​a​", "​a​"},
		{"   ", ""},
	}
	for _, tc := range tests {
		if got := domain.JSTrim(tc.in); got != tc.want {
			t.Errorf("JSTrim(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

const testHeader = "diff --git a/big.ts b/big.ts\n--- a/big.ts\n+++ b/big.ts"

// bigHunk is a hunk of exactly n UTF-16 units made of 100-unit lines.
func bigHunk(start, n int) string {
	head := fmt.Sprintf("@@ -%d,1 +%d,1 @@", start, start)
	var b strings.Builder
	b.WriteString(head)
	for b.Len() < n {
		line := "\n+" + strings.Repeat("x", 98)
		if b.Len()+len(line) > n {
			line = "\n+" + strings.Repeat("x", n-b.Len()-2)
		}
		b.WriteString(line)
	}
	return b.String()
}

func TestSplitOversizeDiff_WithinTheCapIsOnePart(t *testing.T) {
	raw := testHeader + "\n@@ -1 +1 @@\n+x"
	got := domain.SplitOversizeDiff(raw, domain.MaxChunkUnits)
	if len(got.Parts) != 1 || got.Parts[0] != raw || got.Truncated {
		t.Errorf("SplitOversizeDiff(small) = %+v, want the text as one untruncated part", got)
	}
	if got := domain.SplitOversizeDiff(raw, domain.Len16(raw)); len(got.Parts) != 1 || got.Truncated {
		t.Errorf("SplitOversizeDiff at exactly the cap = %+v, want one untruncated part", got)
	}
}

// AC4043, first half: one file of 450,000 units in 3 hunks of 150,000 splits into 3 parts.
func TestSplitOversizeDiff_ThreeHunks(t *testing.T) {
	raw := testHeader + "\n" + bigHunk(1, 150_000) + "\n" + bigHunk(1000, 150_000) + "\n" + bigHunk(2000, 150_000)
	got := domain.SplitOversizeDiff(raw, domain.MaxChunkUnits)
	if len(got.Parts) != 3 || got.Truncated {
		t.Fatalf("SplitOversizeDiff(3 x 150,000) = %d parts truncated=%v, want 3 untruncated", len(got.Parts), got.Truncated)
	}
	for i, p := range got.Parts {
		if n := domain.Len16(p); n > domain.MaxChunkUnits {
			t.Errorf("part %d has %d units, want at most %d", i, n, domain.MaxChunkUnits)
		}
		if !strings.HasPrefix(p, testHeader+"\n@@ ") {
			t.Errorf("part %d does not start with the file header: %.80q", i, p)
		}
	}
}

func TestSplitOversizeDiff_SmallHunksFillPartsInOrder(t *testing.T) {
	// Hunks of 58 units and a header of 18 under a 140-unit cap: two hunks per part, then a new part.
	h := func(i int) string { return fmt.Sprintf("@@ -%d +%d @@\n+%s", i, i, strings.Repeat("y", 45)) }
	raw := "diff --git a/f b/f\n" + strings.Join([]string{h(1), h(2), h(3), h(4)}, "\n")
	got := domain.SplitOversizeDiff(raw, 140)
	want := []string{
		"diff --git a/f b/f\n" + h(1) + "\n" + h(2),
		"diff --git a/f b/f\n" + h(3) + "\n" + h(4),
	}
	if len(got.Parts) != 2 || got.Parts[0] != want[0] || got.Parts[1] != want[1] || got.Truncated {
		t.Errorf("SplitOversizeDiff(...) = %q truncated=%v, want %q", got.Parts, got.Truncated, want)
	}
}

// AC4043, second half: a single hunk of 250,000 units is truncated with the note.
func TestSplitOversizeDiff_OversizeHunkIsTruncated(t *testing.T) {
	raw := testHeader + "\n" + bigHunk(1, 250_000)
	got := domain.SplitOversizeDiff(raw, domain.MaxChunkUnits)
	if len(got.Parts) != 1 || !got.Truncated {
		t.Fatalf("SplitOversizeDiff(1 x 250,000) = %d parts truncated=%v, want 1 truncated part", len(got.Parts), got.Truncated)
	}
	part := got.Parts[0]
	re := regexp.MustCompile(`\n\[diff truncated by reviewer-core: (\d+) chars omitted \(maxDiffChars=200000\)\]$`)
	m := re.FindStringSubmatch(part)
	if m == nil {
		t.Fatalf("part does not end with the truncation note: ...%q", part[len(part)-120:])
	}
	body := part[:len(part)-len(m[0])]
	var omitted int
	fmt.Sscan(m[1], &omitted)
	if total := domain.Len16(raw); domain.Len16(body)+omitted != total {
		t.Errorf("kept %d + omitted %d units = %d, want the original %d", domain.Len16(body), omitted, domain.Len16(body)+omitted, total)
	}
	if n := domain.Len16(part); n > domain.MaxChunkUnits+10 {
		t.Errorf("truncated part has %d units, want about the cap %d", n, domain.MaxChunkUnits)
	}
	if !strings.HasPrefix(part, testHeader+"\n@@ -1,1 +1,1 @@\n+xxx") {
		t.Errorf("truncated part does not keep the start of the unit: %.100q", part)
	}
	if !strings.HasPrefix(raw, body) || raw[len(body)] != '\n' {
		t.Errorf("cut is not at a line boundary of the original: kept %d bytes", len(body))
	}
}

func TestSplitOversizeDiff_TextWithoutHunksIsTruncatedWhole(t *testing.T) {
	raw := strings.Repeat("no hunk here\n", 50)
	got := domain.SplitOversizeDiff(raw, 200)
	if len(got.Parts) != 1 || !got.Truncated || !strings.Contains(got.Parts[0], "[diff truncated by reviewer-core: ") {
		t.Errorf("SplitOversizeDiff(no hunks) = %+v, want one truncated part with the note", got)
	}
}

func TestSplitOversizeDiff_CountsUTF16Units(t *testing.T) {
	// 40 emoji are 80 units but 160 bytes: a 100-unit cap keeps it whole, a 79-unit cap does not.
	raw := "diff --git a/f b/f\n@@ -1 +1 @@\n+" + strings.Repeat("😀", 40)
	if n := domain.Len16(raw); n != 112 {
		t.Fatalf("Len16(raw) = %d, test setup is off", n)
	}
	if got := domain.SplitOversizeDiff(raw, domain.Len16(raw)); len(got.Parts) != 1 || got.Truncated {
		t.Errorf("SplitOversizeDiff at the exact unit length = %+v, want one part", got)
	}
	if got := domain.SplitOversizeDiff(raw, domain.Len16(raw)-1); !got.Truncated {
		t.Errorf("SplitOversizeDiff one unit under = %+v, want truncated", got)
	}
}

func TestSplitOversizeDiff_NeverCutsASurrogatePair(t *testing.T) {
	// Cut positions sweep across the emoji run; every part must stay valid UTF-8.
	raw := "diff --git a/f b/f\n@@ -1 +1 @@\n+" + strings.Repeat("😀", 60)
	for max := 60; max < 140; max++ {
		for i, p := range domain.SplitOversizeDiff(raw, max).Parts {
			if strings.ContainsRune(p, '�') || !isValidUTF8(p) {
				t.Fatalf("SplitOversizeDiff(max=%d) part %d is not valid text: %q", max, i, p)
			}
		}
	}
}

func isValidUTF8(s string) bool { return strings.ToValidUTF8(s, "") == s }
