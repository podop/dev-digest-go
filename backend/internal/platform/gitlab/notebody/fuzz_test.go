package notebody_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/platform/gitlab/notebody"
)

// FuzzApply checks FR17 on arbitrary input: Apply never panics, is idempotent, leaves no CR and
// only ever adds bytes besides the CR: the HTML it escapes, the "&amp;" of a neutralised entity and
// at most one fence line closing a fence the body left open before its trailing lines.
func FuzzApply(f *testing.F) {
	for _, tc := range loadCorpus(f) {
		f.Add(tc.In)
		f.Add(tc.Want)
	}
	for _, seed := range []string{
		"", "`", "``", "` `` ``` ` ``", "@\u200b@", "!\u200b[", "\r\r\n\n\r",
		"\u200b/", ">\u200b/", "- 1) 2. > /x", "<sub>DevDigest · 5% confidence</sub>\n<!-- devdigest-finding:a -->\n \t",
		"\xff\xfe\xe2\x80/", "\ufeff\u00a0\t/x", "`@`\n/a`<`", "![", "```\n<!-- devdigest-finding:a -->",
		"&#64;", "&#x0040;&commat;&LT;&lt;", "\\`` a ` @b `", "- x\n  ```\n@a\n  ```", "~~~\n`\n~~~\n@a\n`", "| `a | @b` |",
		"`<\u200bb>` <\u200bi>", "`&\u200b#64;` &\u200b#64;", "&\u200bcommat;",
	} {
		f.Add(seed)
	}
	for _, seed := range residueSeeds(f) {
		f.Add(seed)
	}
	plain := strings.NewReplacer(notebody.ZWSP, "", "&lt;", "<", "&gt;", ">", "&amp;", "&")
	f.Fuzz(func(t *testing.T, in string) {
		out := notebody.Apply(in)
		if again := notebody.Apply(out); again != out {
			t.Fatalf("Apply is not idempotent: Apply(%q) = %q, Apply of that = %q", in, out, again)
		}
		if strings.Contains(out, "\r") {
			t.Fatalf("Apply(%q) = %q still contains a CR", in, out)
		}
		// Removing what the rules add must give back the input apart from newline normalisation,
		// the escaped angle brackets and the "&amp;" of neutralised entities.
		base := strings.ReplaceAll(strings.ReplaceAll(in, "\r\n", "\n"), "\r", "\n")
		if stripped, want := plain.Replace(out), plain.Replace(base); stripped != want && !withFenceLineRemoved(stripped, want) {
			t.Fatalf("Apply(%q) = %q changes more than FR14-FR16 allow", in, out)
		}
	})
}

// withFenceLineRemoved reports whether dropping one line made only of 3+ backticks or tildes turns
// got into want: the closing line Apply adds to a fence the body left open.
func withFenceLineRemoved(got, want string) bool {
	lines := strings.Split(got, "\n")
	for i, line := range lines {
		if len(line) < 3 || strings.Trim(line, line[:1]) != "" || (line[0] != '`' && line[0] != '~') {
			continue
		}
		if strings.Join(append(slices.Clone(lines[:i]), lines[i+1:]...), "\n") == want {
			return true
		}
	}
	return false
}

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
