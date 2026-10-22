package catalog_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/skills/adapters/catalog"
	"dev-digest-go/backend/internal/skills/domain"
)

// wantEntries mirrors the Node catalog (community-catalog.ts:16-268); the body byte lengths were
// measured on the Node strings, so a changed byte in catalog.json fails here.
var wantEntries = []struct {
	id, lang string
	typ      domain.SkillType
	tags     []string
	desc     string
	bodyLen  int
	descLen  int
}{
	{"owasp-top-10-review", "any", domain.TypeSecurity, []string{"security", "owasp", "web"}, "Checks changed server code against the OWASP Top 10 web risks.", 1544, 173},
	{"sql-injection-gate", "sql", domain.TypeSecurity, []string{"security", "sql", "database"}, "Blocks SQL built from untrusted input instead of bound parameters.", 967, 137},
	{"react-hooks-rules", "typescript", domain.TypeConvention, []string{"react", "frontend", "hooks"}, "Enforces the Rules of Hooks and effect-dependency hygiene.", 996, 143},
	{"a11y-jsx-audit", "typescript", domain.TypeRubric, []string{"accessibility", "a11y", "react", "frontend"}, "Catches accessibility regressions in changed JSX.", 964, 143},
	{"error-handling-discipline", "typescript", domain.TypeConvention, []string{"reliability", "errors", "node"}, "Finds swallowed errors, floating promises and lost causes.", 906, 149},
	{"input-validation-boundaries", "typescript", domain.TypeSecurity, []string{"security", "validation", "api"}, "Requires schema validation where untrusted data enters the system.", 794, 153},
	{"test-assertion-quality", "any", domain.TypeRubric, []string{"testing", "quality"}, "Flags tests that cannot fail or assert nothing meaningful.", 886, 146},
	{"async-test-hygiene", "typescript", domain.TypeRubric, []string{"testing", "flaky", "async"}, "Catches timing-dependent async tests before they turn flaky.", 690, 141},
}

func newCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.New()
	if err != nil {
		t.Fatalf("catalog.New() error = %v", err)
	}
	return c
}

func TestCatalog_ShippedEntries(t *testing.T) {
	entries := newCatalog(t).List()
	if len(entries) != 8 {
		t.Fatalf("len(List()) = %d, want 8", len(entries))
	}
	for i, want := range wantEntries {
		got := entries[i]
		t.Run(want.id, func(t *testing.T) {
			if got.ID != want.id || got.Name != want.id {
				t.Errorf("entry %d id/name = %q/%q, want %q", i, got.ID, got.Name, want.id)
			}
			if got.Repo != "devdigest/community-skills" || got.Stars != 0 {
				t.Errorf("%s repo/stars = %q/%d, want devdigest/community-skills/0", want.id, got.Repo, got.Stars)
			}
			if got.Lang != want.lang || got.Type != want.typ || got.Desc != want.desc {
				t.Errorf("%s lang/type/desc = %q/%q/%q, want %q/%q/%q", want.id, got.Lang, got.Type, got.Desc, want.lang, want.typ, want.desc)
			}
			if diff := cmp.Diff(want.tags, got.Tags); diff != "" {
				t.Errorf("%s tags mismatch (-want +got):\n%s", want.id, diff)
			}
			if len(got.Body) != want.bodyLen {
				t.Errorf("len(%s body) = %d bytes, want %d", want.id, len(got.Body), want.bodyLen)
			}
			if len(got.Description) != want.descLen {
				t.Errorf("len(%s description) = %d bytes, want %d", want.id, len(got.Description), want.descLen)
			}
		})
	}
}

func TestCatalog_BodiesKeepNodeEscapes(t *testing.T) {
	sql, ok := newCatalog(t).Get("sql-injection-gate")
	if !ok {
		t.Fatal("Get(sql-injection-gate) not found")
	}
	if !strings.Contains(sql.Body, "${req.params.id}") {
		t.Errorf("sql-injection-gate body lost the literal ${req.params.id}")
	}
	if strings.Contains(sql.Body, "\\`") || strings.Contains(sql.Body, "\\${") {
		t.Errorf("sql-injection-gate body keeps a template-literal escape")
	}
}

func TestCatalog_Get(t *testing.T) {
	c := newCatalog(t)
	if _, ok := c.Get("nope"); ok {
		t.Error("Get(nope) found an entry")
	}
	e, ok := c.Get("react-hooks-rules")
	if !ok || e.Type != domain.TypeConvention {
		t.Errorf("Get(react-hooks-rules) = %+v, %v; want a convention entry", e, ok)
	}
}

func TestCatalog_ListIsACopy(t *testing.T) {
	c := newCatalog(t)
	first := c.List()
	first[0].Tags[0] = "mutated"
	first[0].Name = "mutated"
	if got := c.List()[0]; got.Name != "owasp-top-10-review" || got.Tags[0] != "security" {
		t.Errorf("List()[0] = %q %v after mutating a previous result, want it unchanged", got.Name, got.Tags)
	}
}

func TestCatalog_Filter(t *testing.T) {
	entries := newCatalog(t).List()
	ids := func(f domain.CatalogFilter) []string {
		out := []string{}
		for _, e := range domain.FilterCatalog(entries, f) {
			out = append(out, e.ID)
		}
		return out
	}
	tests := []struct {
		name   string
		filter domain.CatalogFilter
		want   []string
	}{
		{"tag is case-insensitive", domain.CatalogFilter{Tag: "REACT"}, []string{"react-hooks-rules", "a11y-jsx-audit"}},
		{"q matches a name", domain.CatalogFilter{Q: "owasp"}, []string{"owasp-top-10-review"}},
		{"filters combine with AND", domain.CatalogFilter{Lang: "sql", Q: "xyz"}, []string{}},
		{"lang trims and ignores case", domain.CatalogFilter{Lang: " SQL "}, []string{"sql-injection-gate"}},
		{"q matches a tag", domain.CatalogFilter{Q: "a11y"}, []string{"a11y-jsx-audit"}},
		{"tag is exact, not a substring", domain.CatalogFilter{Tag: "reac"}, []string{}},
		{"blank filters keep everything", domain.CatalogFilter{Q: "  "}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(tc.filter)
			if tc.want == nil {
				if len(got) != 8 {
					t.Errorf("FilterCatalog(%+v) kept %d entries, want 8", tc.filter, len(got))
				}
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FilterCatalog(%+v) mismatch (-want +got):\n%s", tc.filter, diff)
			}
		})
	}
}

func TestCatalog_SkillFromEntry(t *testing.T) {
	// AC249: a catalog body is a valid skill body of its own.
	for _, e := range newCatalog(t).List() {
		c, err := domain.SkillFromMarkdown(e.Body, e.Name)
		if err != nil {
			t.Errorf("SkillFromMarkdown(%s body) error = %v", e.ID, err)
			continue
		}
		if c.Name != e.Name {
			t.Errorf("SkillFromMarkdown(%s body).Name = %q, want %q", e.ID, c.Name, e.Name)
		}
	}
}
