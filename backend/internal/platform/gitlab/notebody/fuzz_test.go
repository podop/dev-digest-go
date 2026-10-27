package notebody_test

import (
	"strings"
	"testing"

	"dev-digest-go/backend/internal/platform/gitlab/notebody"
)

// FuzzApply checks FR17 on arbitrary input: Apply never panics, is idempotent, leaves no CR and
// only ever adds bytes besides the CR and the HTML it escapes.
func FuzzApply(f *testing.F) {
	for _, tc := range loadCorpus(f) {
		f.Add(tc.In)
		f.Add(tc.Want)
	}
	for _, seed := range []string{
		"", "`", "``", "` `` ``` ` ``", "@\u200b@", "!\u200b[", "\r\r\n\n\r",
		"\u200b/", ">\u200b/", "- 1) 2. > /x", "<sub>DevDigest · 5% confidence</sub>\n<!-- devdigest-finding:a -->\n \t",
		"\xff\xfe\xe2\x80/", "\ufeff\u00a0\t/x", "`@`\n/a`<`", "![", "```\n<!-- devdigest-finding:a -->",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := notebody.Apply(in)
		if again := notebody.Apply(out); again != out {
			t.Fatalf("Apply is not idempotent: Apply(%q) = %q, Apply of that = %q", in, out, again)
		}
		if strings.Contains(out, "\r") {
			t.Fatalf("Apply(%q) = %q still contains a CR", in, out)
		}
		// Removing what the rules add must give back the input apart from newline normalisation
		// and the escaped angle brackets.
		stripped := strings.ReplaceAll(out, notebody.ZWSP, "")
		stripped = strings.ReplaceAll(stripped, "&lt;", "<")
		stripped = strings.ReplaceAll(stripped, "&gt;", ">")
		base := strings.ReplaceAll(strings.ReplaceAll(in, "\r\n", "\n"), "\r", "\n")
		base = strings.ReplaceAll(base, notebody.ZWSP, "")
		base = strings.ReplaceAll(base, "&lt;", "<")
		base = strings.ReplaceAll(base, "&gt;", ">")
		if stripped != base {
			t.Fatalf("Apply(%q) = %q changes more than FR14-FR16 allow", in, out)
		}
	})
}
