package notebody

import (
	"strings"
	"testing"
)

// residue lists what FR16 must have removed from out outside its code regions: an "@" without
// the U+200B after it, a "<" or ">" (except on the two allowed lines), "![" and a character
// reference that decodes to "@", "<", "!" or "[".
func residue(out string) []string {
	var found []string
	regions := codeRegions(out)
	lastStart := lastNonEmptyLineStart(out)
	lineStart, lineEnd, allowed, next := 0, 0, false, 0
	for i := 0; i < len(out); i++ {
		if i == 0 || out[i-1] == '\n' {
			lineStart = i
			lineEnd = i + strings.IndexByte(out[i:]+"\n", '\n')
			line := out[lineStart:lineEnd]
			allowed = isSubLine(line) || (lineStart == lastStart && isMarkerLine(line))
		}
		for next < len(regions) && regions[next].end <= i {
			next++
		}
		if next < len(regions) && regions[next].start <= i {
			continue
		}
		switch out[i] {
		case '@':
			if !strings.HasPrefix(out[i+1:], ZWSP) {
				found = append(found, "@ at "+itoa(i))
			}
		case '<', '>':
			if !allowed {
				found = append(found, string(out[i])+" at "+itoa(i))
			}
		case '!':
			if strings.HasPrefix(out[i+1:], "[") {
				found = append(found, "![ at "+itoa(i))
			}
		case '&':
			if specialEntity(out[i:]) {
				found = append(found, "entity at "+itoa(i))
			}
		}
	}
	return found
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{digits[i%10]}, b...)
	}
	return string(b)
}

// FuzzApplyLeavesNoResidueOutsideCodeRegions: whatever the input, the output holds no mention,
// raw HTML, image or special character reference outside the conservative code regions.
func FuzzApplyLeavesNoResidueOutsideCodeRegions(f *testing.F) {
	for _, seed := range []string{
		"", "@all", "`@a`", "```\n@a\n```", "\\`@a <b>\\`", "See `\n\n@all ![t](u)\n\n` end",
		"- ```\n@v ![p](u)\n```", "~~~\n`\n~~~\n@v <img>\n`", "    `\n\n@v `", "&#64;u &commat;u &#x40;u",
		"| `a | @b` |", "\\`` a ` @b `", "> ```\n> @a\n> ```", "- x\n  ```\n@a\n  ```", "```\n@a",
		"<sub>DevDigest · 5% confidence</sub>\n<!-- devdigest-finding:a -->",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := Apply(in)
		if found := residue(out); len(found) > 0 {
			t.Fatalf("Apply(%q) = %q leaves %v outside the code regions", in, out, found)
		}
	})
}

func TestCodeRegions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string // the text of every region
	}{
		{"inline span", "a `b` c", []string{"`b`"}},
		{"two spans", "`a` x ``b`c``", []string{"`a`", "``b`c``"}},
		{"span over two lines", "`a\nb`", nil},
		{"closed fence", "x\n```\na\n```\ny", []string{"```\na\n```"}},
		{"fence swallows backticks inside", "```\n`a`\n```", []string{"```\n`a`\n```"}},
		{"escaped opener", "\\`a`", nil},
		{"escaped double run opens with one", "\\``a`", []string{"`a`"}},
		{"line with pipe", "`a|b`", nil},
		{"unclosed fence", "```\na", nil},
		{"indented fence in a list", "- a\n  ```\n  b\n  ```", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, r := range codeRegions(tc.in) {
				got = append(got, tc.in[r.start:r.end])
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("codeRegions(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
