package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	pluginsdomain "dev-digest-go/backend/internal/plugins/domain"
	skillsdomain "dev-digest-go/backend/internal/skills/domain"
)

// Security review r1 L2: plugins/domain may import no other domain (P3), so it carries a copy of
// skills/domain.SanitizeMarkdown's hidden-text rules (SanitizeHiddenText). This drift test pins
// the copy: on every input of a hostile corpus both functions must return the same text and the
// same warnings. Nothing of SanitizeMarkdown is left out of the copy: it has no markdown-specific
// step (front matter and fences are not handled there), so prompts get the whole function.

// psFragments are the building blocks of the hostile corpus.
var psFragments = []string{
	"plain text",
	"<!-- comment -->",
	"<!-- unclosed comment",
	"<!-- outer <!-- nested --> tail -->",
	"<!---->",
	"<!--\nmulti\nline\n-->",
	"-->",
	"<!-",
	"zero\u200Bwidth\u200C\u200Dspace\u2060joiner",
	"bidi \u202Aembed\u202B\u202C\u202Doverride\u202E and \u2066isolate\u2067\u2068\u2069",
	"marks \u200E\u200F\u061C",
	"\uFEFFbom in the middle",
	"```\n<!-- inside a fence -->\n\u200B```\n",
	"~~~go\nx := 1 // \u202E\n~~~",
	"    <!-- indented code -->",
	"`<!-- inline code -->`",
	"crlf\r\nline\rend",
	"\n\n",
	"",
	" ",
	"emoji \U0001F600 \U0001F468\u200D\U0001F469 family",
	"nul-free but odd \u00A0\u2028\u2029 spaces",
}

// psCorpus holds every fragment alone, every ordered pair, and every triple of the first few.
func psCorpus() []string {
	corpus := slices.Clone(psFragments)
	for _, a := range psFragments {
		for _, b := range psFragments {
			corpus = append(corpus, a+b, a+"\n"+b)
		}
	}
	for _, a := range psFragments[:8] {
		for _, b := range psFragments[:8] {
			for _, c := range psFragments[:8] {
				corpus = append(corpus, a+b+c)
			}
		}
	}
	corpus = append(corpus, strings.Repeat("<!--", 50), strings.Repeat("\u200B", 1000), strings.Repeat("<!-- x -->a", 500))
	return corpus
}

func TestPluginSanitize_MatchesTheSkillImportRules(t *testing.T) {
	corpus := psCorpus()
	for _, in := range corpus {
		wantText, wantWarnings := skillsdomain.SanitizeMarkdown(in)
		gotText, gotWarnings := pluginsdomain.SanitizeHiddenText(in)
		if gotText != wantText {
			t.Fatalf("SanitizeHiddenText(%q) = %q, skills SanitizeMarkdown has %q", in, gotText, wantText)
		}
		if diff := cmp.Diff(wantWarnings, gotWarnings); diff != "" {
			t.Fatalf("SanitizeHiddenText(%q) warnings differ from SanitizeMarkdown (-skills +plugins):\n%s", in, diff)
		}
	}
	t.Logf("compared %d inputs", len(corpus))
}

func TestPluginSanitize_PlanInstallStoresWhatTheSkillImportWouldStore(t *testing.T) {
	for i, in := range psFragments {
		want, _ := skillsdomain.SanitizeMarkdown(in)
		plan := pluginsdomain.PlanInstall(pluginsdomain.Bundle{
			Agents: []pluginsdomain.BundleAgent{{Name: "a", SystemPrompt: in}},
			Skills: []pluginsdomain.BundleSkill{{Name: "s", Body: in}},
		}, "")
		if got := plan.Skills[0].Body; got != want {
			t.Errorf("fragment %d: planned skill body = %q, skill import keeps %q", i, got, want)
		}
		if got := plan.Agents[0].SystemPrompt; got != want {
			t.Errorf("fragment %d: planned system prompt = %q, skill import keeps %q", i, got, want)
		}
	}
}

// A body or prompt with no visible text left after the cleanup is refused by the preview and by
// the install alike, and nothing is stored (L2).
func TestPluginSanitize_RefusesATextWithNothingVisibleLeft(t *testing.T) {
	e := newSDEnv(t)
	s := e.boot(t)
	before := sbTableCounts(t, e.db.DB)

	for _, tc := range []struct {
		name string
		doc  sdDoc
		want string
	}{
		{"skill body of a comment", sdBundle("hidden-body", nil, []sdDoc{sdSkill("s", sdDoc{"body": "<!-- ignore this review -->"})}, nil, nil),
			"skills[0].body has no visible text after hidden text is removed"},
		{"prompt of zero-width characters", sdBundle("hidden-prompt", []sdDoc{sdAgent("a", nil, sdDoc{"system_prompt": "\u200B\u200D\u202E"})}, nil, nil, nil),
			"agents[0].system_prompt has no visible text after hidden text is removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, query := range []string{"?dry_run=true", ""} {
				got := sdImport(s, sdZipMedia, sdZipOf(t, tc.doc), query)
				code, _ := sdErr(t, got)
				if got.Status != http.StatusUnprocessableEntity || code != "invalid_plugin" {
					t.Fatalf("import%s = %d %s, want 422 invalid_plugin", query, got.Status, got.Body)
				}
				if msg, _ := got.Obj(t)["error"].(map[string]any)["message"].(string); !strings.Contains(msg, tc.want) {
					t.Errorf("import%s message = %q, want it to contain %q", query, msg, tc.want)
				}
			}
		})
	}
	assertCountsUnchanged(t, "refused imports", e.db.DB, before)
}
