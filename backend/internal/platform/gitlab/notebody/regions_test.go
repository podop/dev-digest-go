package notebody

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// residue lists what FR16 must have removed from out anywhere, code or not (decision D-new-3):
// an "@" without the U+200B after it, a "<" without the U+200B after it (except on the two allowed
// lines; Apply writes &lt; for the others), "![" and a character reference that decodes to "@",
// "<", "!" or "[".
func residue(out string) []string {
	var found []string
	lastStart := lastNonEmptyLineStart(out)
	lineStart, lineEnd, allowed := 0, 0, false
	for i := 0; i < len(out); i++ {
		if i == 0 || out[i-1] == '\n' {
			lineStart = i
			lineEnd = i + strings.IndexByte(out[i:]+"\n", '\n')
			line := out[lineStart:lineEnd]
			allowed = isSubLine(line) || (lineStart == lastStart && isMarkerLine(line))
		}
		switch out[i] {
		case '@':
			if !strings.HasPrefix(out[i+1:], ZWSP) {
				found = append(found, "@ at "+strconv.Itoa(i))
			}
		case '<':
			if !allowed && !strings.HasPrefix(out[i+1:], ZWSP) {
				found = append(found, "< at "+strconv.Itoa(i))
			}
		case '!':
			if strings.HasPrefix(out[i+1:], "[") {
				found = append(found, "![ at "+strconv.Itoa(i))
			}
		case '&':
			if specialEntity(out[i:]) {
				found = append(found, "entity at "+strconv.Itoa(i))
			}
		}
	}
	return found
}

// residueSeeds are the inputs the residue property starts from (testdata/residue_seeds.json): the
// security r2 bypasses (a)-(d), the FR17 input, close variants, and fences left open before the
// trailing lines.
func residueSeeds(tb testing.TB) []string {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "residue_seeds.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var seeds []string
	if err := json.Unmarshal(raw, &seeds); err != nil {
		tb.Fatal(err)
	}
	return seeds
}

// FuzzApplyLeavesNoResidue: whatever the input, the output holds no live mention, raw HTML,
// image or special character reference anywhere, code regions included, so a wrong guess about
// where GitLab renders code never matters.
func FuzzApplyLeavesNoResidue(f *testing.F) {
	for _, seed := range residueSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := Apply(in)
		if found := residue(out); len(found) > 0 {
			t.Fatalf("Apply(%q) = %q leaves %v", in, out, found)
		}
	})
}

// The residue property also holds for every seed and corpus input under plain `go test`.
func TestApplyLeavesNoResidue(t *testing.T) {
	inputs := residueSeeds(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "ec15.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ In string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		inputs = append(inputs, c.In)
	}
	for _, in := range inputs {
		if found := residue(Apply(in)); len(found) > 0 {
			t.Errorf("Apply(%q) = %q leaves %v", in, Apply(in), found)
		}
	}
}

// An unclosed fence in the finding text must not swallow the trailing "<sub>" and marker lines: Apply
// closes it on its own line before them, once.
func TestApply_ClosesAnOpenFenceBeforeTheTrailingLines(t *testing.T) {
	const sub, marker = "<sub>DevDigest · 85% confidence</sub>", "<!-- devdigest-finding:f1 -->"
	tests := []struct {
		name, in, want string
	}{
		{"backtick fence", "x\n\n```go\ncode\n\n" + sub + "\n\n" + marker, "x\n\n```go\ncode\n\n```\n" + sub + "\n\n" + marker},
		{"tilde fence of its own length", "~~~~\ncode\n" + sub + "\n" + marker, "~~~~\ncode\n~~~~\n" + sub + "\n" + marker},
		{"marker without a sub line", "```\ncode\n\n" + marker, "```\ncode\n\n```\n" + marker},
		{"closed fence", "```\ncode\n```\n\n" + sub + "\n\n" + marker, "```\ncode\n```\n\n" + sub + "\n\n" + marker},
		{"no fence", "code\n\n" + sub + "\n\n" + marker, "code\n\n" + sub + "\n\n" + marker},
		{"no trailing lines", "```\ncode", "```\ncode"},
		{"fence in a list item is not guessed", "- a\n  ```\n  c\n\n" + sub + "\n\n" + marker, "- a\n  ```\n  c\n\n" + sub + "\n\n" + marker},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.in)
			if got != tc.want {
				t.Errorf("Apply(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if again := Apply(got); again != got {
				t.Errorf("Apply(Apply(%q)) = %q, want %q (FR17)", tc.in, again, got)
			}
		})
	}
}

// The trailing lines are never changed, even when a region guess puts them inside code.
func TestApply_KeepsTheTrailingLinesWhateverTheRegions(t *testing.T) {
	const tail = "<sub>DevDigest · 85% confidence</sub>\n\n<!-- devdigest-finding:f1 -->"
	for _, in := range []string{
		"```\na\n```\n```\n```\n" + tail, "- x\n  ```\n```\n```\n" + tail, "> q\n  ```\n```\n" + tail,
		"a ` b\n`\n\n" + tail, "`\n\n" + tail, "```\n```\n```\n\n" + tail,
	} {
		out := Apply(in)
		if !strings.HasSuffix(out, tail) {
			t.Errorf("Apply(%q) = %q, want it to end with the unchanged trailing lines", in, out)
		}
	}
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
