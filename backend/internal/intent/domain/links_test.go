package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

var acme = domain.Repo{Owner: "acme", Name: "api"}

func TestExtractTickets(t *testing.T) {
	tests := []struct {
		name        string
		texts       []string
		wantRefs    []domain.TicketRef
		wantSkipped []domain.Skipped
	}{
		{name: "bare number", texts: []string{"see #12"}, wantRefs: []domain.TicketRef{{Number: 12}}},
		{name: "closing keyword within 24 units", texts: []string{"closes #5"}, wantRefs: []domain.TicketRef{{Number: 5, Closing: true}}},
		{name: "closing keyword out of the window", texts: []string{"closes " + strings.Repeat("x ", 12) + "#5"}, wantRefs: []domain.TicketRef{{Number: 5}}},
		{name: "keyword inside a longer word does not close", texts: []string{"prefix #5"}, wantRefs: []domain.TicketRef{{Number: 5}}},
		{name: "upper case keyword", texts: []string{"RESOLVES #5"}, wantRefs: []domain.TicketRef{{Number: 5, Closing: true}}},
		{name: "number seen again ORs the flag, keeps its place", texts: []string{"#1 #2", "fixes #1"},
			wantRefs: []domain.TicketRef{{Number: 1, Closing: true}, {Number: 2}}},
		{name: "closing first, then first seen", texts: []string{"#1 #2 fixes #3"},
			wantRefs: []domain.TicketRef{{Number: 3, Closing: true}, {Number: 1}, {Number: 2}}},
		{name: "leading zeros are a number", texts: []string{"#007"}, wantRefs: []domain.TicketRef{{Number: 7}}},
		{name: "same repo URL and owner/repo form, any case", texts: []string{"https://github.com/ACME/api/issues/21 and Acme/API#22"},
			wantRefs: []domain.TicketRef{{Number: 21}, {Number: 22}}},
		{name: "other repo is skipped", texts: []string{"other/repo#4 https://github.com/x/y/issues/2"},
			wantSkipped: []domain.Skipped{
				{Ref: "https://github.com/x/y/issues/2", Reason: "issue reference from a different repository (x/y)"},
				{Ref: "other/repo#4", Reason: "issue reference from a different repository (other/repo)"},
			}},
		{name: "a hash glued to a word or a slash is no ticket", texts: []string{"a#6 /#7 _#8"}},
		{name: "Jira key skipped once", texts: []string{"ABC-123 and ABC-123"},
			wantSkipped: []domain.Skipped{{Ref: "ABC-123", Reason: "Jira/Linear-style keys are not supported (server/specs/05-intent-layer.md)"}}},
		{name: "standard names are ignored", texts: []string{"UTF-8 SHA-256 ES-2024 X-1"}},
		{name: "limit", texts: []string{"#1 #2 #3 #4 #5"},
			wantRefs: []domain.TicketRef{{Number: 1}, {Number: 2}, {Number: 3}},
			wantSkipped: []domain.Skipped{
				{Ref: "#4", Reason: "ticket limit reached (max 3)"}, {Ref: "#5", Reason: "ticket limit reached (max 3)"},
			}},
		{name: "empty texts are skipped", texts: []string{"", ""}},
		{name: "surrogate pair inside the window", texts: []string{"close 😀😀😀😀😀😀😀😀#9"}, wantRefs: []domain.TicketRef{{Number: 9, Closing: true}}},
		{name: "surrogate pairs count two units", texts: []string{"close 😀😀😀😀😀😀😀😀😀😀#9"}, wantRefs: []domain.TicketRef{{Number: 9}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ExtractTickets(tc.texts, acme)
			if diff := cmp.Diff(append([]domain.TicketRef{}, tc.wantRefs...), got.Refs); diff != "" {
				t.Errorf("ExtractTickets(%q).Refs mismatch (-want +got):\n%s", tc.texts, diff)
			}
			if diff := cmp.Diff(append([]domain.Skipped{}, tc.wantSkipped...), got.Skipped); diff != "" {
				t.Errorf("ExtractTickets(%q).Skipped mismatch (-want +got):\n%s", tc.texts, diff)
			}
		})
	}
}

// A run of digits longer than a double is Infinity in JavaScript; its ref string is "Infinity".
func TestExtractTickets_HugeNumberIsInfinity(t *testing.T) {
	got := domain.ExtractTickets([]string{"#" + strings.Repeat("9", 400)}, acme)
	if len(got.Refs) != 1 || got.Refs[0].Ref() != "Infinity" {
		t.Errorf("ExtractTickets(400 digits) = %+v, want one ref \"Infinity\"", got.Refs)
	}
}

func TestTicketRef_Ref(t *testing.T) {
	for _, tc := range []struct {
		n    float64
		want string
	}{{12, "12"}, {9007199254740993, "9007199254740992"}, {1e21, "1e+21"}} {
		if got := (domain.TicketRef{Number: tc.n}).Ref(); got != tc.want {
			t.Errorf("TicketRef{%v}.Ref() = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestExtractDocs(t *testing.T) {
	tests := []struct {
		name        string
		texts       []string
		wantRefs    []string
		wantSkipped []domain.Skipped
	}{
		{name: "markdown link and prose", texts: []string{"see [plan](docs/plans/a.md) and README.md."}, wantRefs: []string{"docs/plans/a.md", "README.md"}},
		{name: "leading ./ and trailing punctuation", texts: []string{"[x](./specs/b.md)), (notes.txt)"}, wantRefs: []string{"specs/b.md", "notes.txt"}},
		{name: "non-doc extension is ignored silently", texts: []string{"[x](a.pdf) a.md5 x.mdxx"}},
		{name: "same-repo blob URL decodes the path", texts: []string{"[l](https://github.com/acme/api/blob/main/docs/%E2%9C%93.md)"}, wantRefs: []string{"docs/✓.md"}},
		{name: "foreign blob URL in a link", texts: []string{"[l](https://github.com/other/r/blob/main/d.md)"},
			wantSkipped: []domain.Skipped{{Ref: "https://github.com/other/r/blob/main/d.md", Reason: "doc link points outside this repo (other/r)"}}},
		{name: "non-GitHub URL in a link", texts: []string{"[l](https://example.com/x.md)"},
			wantSkipped: []domain.Skipped{{Ref: "https://example.com/x.md", Reason: "non-GitHub or non-blob/tree URL (no arbitrary URL fetch)"}}},
		{name: "scheme is matched case-insensitively", texts: []string{"[l](HTTPS://github.com/acme/api/blob/main/z.md)"},
			wantSkipped: []domain.Skipped{{Ref: "HTTPS://github.com/acme/api/blob/main/z.md", Reason: "non-GitHub or non-blob/tree URL (no arbitrary URL fetch)"}}},
		{name: "bare blob URL, other repo", texts: []string{"https://github.com/other/r/blob/main/docs/f.md"},
			wantSkipped: []domain.Skipped{{Ref: "https://github.com/other/r/blob/main/docs/f.md", Reason: "doc link points outside this repo (other/r)"}}},
		{name: "URL text is not scanned for bare paths", texts: []string{"https://example.com/docs/h.md"}},
		{name: "unsafe paths", texts: []string{"[x](../up.md) [y](/abs.md) [z](docs\\w.md)"},
			wantSkipped: []domain.Skipped{{Ref: "../up.md", Reason: "unsafe path"}, {Ref: "/abs.md", Reason: "unsafe path"}, {Ref: "docs\\w.md", Reason: "unsafe path"}}},
		{name: "priority then English collation", texts: []string{"z.md docs/other.md a/specs/mid.md docs/plans/p.md Z.md a.md"},
			wantRefs:    []string{"docs/plans/p.md", "a/specs/mid.md", "docs/other.md", "a.md", "z.md"},
			wantSkipped: []domain.Skipped{{Ref: "Z.md", Reason: "doc limit reached (max 5)"}}},
		{name: "extension case-insensitive", texts: []string{"docs/x.MD README.MarkDown"}, wantRefs: []string{"docs/x.MD", "README.MarkDown"}},
		{name: "Kelvin sign is no k (RE2 (?i) would fold it)", texts: []string{"x.mar" + string(rune(0x212A)) + "down"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ExtractDocs(tc.texts, acme)
			if err != nil {
				t.Fatalf("ExtractDocs(%q) error = %v", tc.texts, err)
			}
			paths := []string{}
			for _, r := range got.Refs {
				paths = append(paths, r.Path)
			}
			if diff := cmp.Diff(append([]string{}, tc.wantRefs...), paths); diff != "" {
				t.Errorf("ExtractDocs(%q) refs mismatch (-want +got):\n%s", tc.texts, diff)
			}
			if diff := cmp.Diff(append([]domain.Skipped{}, tc.wantSkipped...), got.Skipped); diff != "" {
				t.Errorf("ExtractDocs(%q) skipped mismatch (-want +got):\n%s", tc.texts, diff)
			}
		})
	}
}

func TestExtractDocs_MalformedEscapeFails(t *testing.T) {
	for _, text := range []string{
		"[bad](https://github.com/acme/api/blob/main/docs/%E2%9C.md)",
		"https://github.com/acme/api/blob/main/docs/%.md",
	} {
		_, err := domain.ExtractDocs([]string{text}, acme)
		if !errors.Is(err, reviewer.ErrURIMalformed) || err.Error() != "URI malformed" {
			t.Errorf("ExtractDocs(%q) error = %v, want URI malformed", text, err)
		}
	}
}

// A malformed escape in a foreign link is never decoded, so it is no failure.
func TestExtractDocs_ForeignLinkIsNotDecoded(t *testing.T) {
	if _, err := domain.ExtractDocs([]string{"[l](https://github.com/other/r/blob/main/%.md)"}, acme); err != nil {
		t.Errorf("ExtractDocs(foreign link with %%) error = %v, want none", err)
	}
}
