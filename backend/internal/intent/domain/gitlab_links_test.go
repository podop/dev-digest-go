package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

var payments = domain.Repo{
	Owner: "acme/platform", Name: "payments", Provider: "gitlab", Host: "git.corp.example",
	WebURL: "https://git.corp.example/gitlab/acme/platform/payments",
}

const projectURL = "https://git.corp.example/gitlab/acme/platform/payments"

// ec20Corpus is the ticket corpus of spec EC20, one reference per line.
var ec20Corpus = strings.Join([]string{
	"Closes " + projectURL + "/-/issues/7",
	"acme/platform/payments#8",
	"payments#9",
	"other#3",
	"acme/other#4",
	"https://gitlab.com/acme/platform/payments/-/issues/5",
	projectURL + "/-/work_items/10",
	"#11",
	"!12",
	"JIRA-1",
}, "\n")

func TestExtractGitLabTickets_EC20Golden(t *testing.T) {
	got := domain.ExtractGitLabTickets([]string{ec20Corpus}, payments)
	want := domain.TicketLinks{
		Refs: []domain.TicketRef{{Number: 7, Closing: true}, {Number: 8}, {Number: 9}},
		Skipped: []domain.Skipped{
			{Ref: "other#3", Reason: "issue reference from a different project (other)"},
			{Ref: "acme/other#4", Reason: "issue reference from a different project (acme/other)"},
			{Ref: "https://gitlab.com/acme/platform/payments/-/issues/5", Reason: "issue reference from another host (gitlab.com)"},
			{Ref: "JIRA-1", Reason: "Jira/Linear-style keys are not supported (server/specs/05-intent-layer.md)"},
			{Ref: "#10", Reason: "ticket limit reached (max 3)"},
			{Ref: "#11", Reason: "ticket limit reached (max 3)"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ExtractGitLabTickets(EC20) mismatch (-want +got):\n%s", diff)
	}
}

// The same corpus for a GitHub repository goes through the unchanged GitHub rules.
func TestExtractTickets_EC20CorpusGitHubGolden(t *testing.T) {
	got := domain.ExtractTickets([]string{ec20Corpus}, domain.Repo{Owner: "acme", Name: "payments"})
	want := domain.TicketLinks{
		Refs: []domain.TicketRef{{Number: 11}},
		Skipped: []domain.Skipped{
			{Ref: "platform/payments#8", Reason: "issue reference from a different repository (platform/payments)"},
			{Ref: "acme/other#4", Reason: "issue reference from a different repository (acme/other)"},
			{Ref: "JIRA-1", Reason: "Jira/Linear-style keys are not supported (server/specs/05-intent-layer.md)"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ExtractTickets(EC20, github) mismatch (-want +got):\n%s", diff)
	}
}

func TestExtractGitLabTickets(t *testing.T) {
	const other = "issue reference from a different project ("
	tests := []struct {
		name        string
		texts       []string
		repo        domain.Repo
		wantRefs    []domain.TicketRef
		wantSkipped []domain.Skipped
	}{
		{name: "tickets follow their position in the text, not their form",
			texts:    []string{"#3 " + projectURL + "/-/issues/1 payments#2"},
			repo:     payments,
			wantRefs: []domain.TicketRef{{Number: 3}, {Number: 1}, {Number: 2}}},
		{name: "closing keyword wins over position",
			texts:    []string{"#1 #2 fixes payments#3"},
			repo:     payments,
			wantRefs: []domain.TicketRef{{Number: 3, Closing: true}, {Number: 1}, {Number: 2}}},
		{name: "text order: title before body, a repeat ORs the closing flag",
			texts:    []string{"#5", "see #6, resolves #5"},
			repo:     payments,
			wantRefs: []domain.TicketRef{{Number: 5, Closing: true}, {Number: 6}}},
		{name: "legacy URL and work item, any case, trailing slash in the repo path",
			texts: []string{"https://git.corp.example/gitlab/ACME/Platform/Payments/issues/4 and " + projectURL + "/-/work_items/5"},
			repo:  payments, wantRefs: []domain.TicketRef{{Number: 4}, {Number: 5}}},
		{name: "host key: default port and case",
			texts: []string{"https://GIT.corp.example:443/gitlab/acme/platform/payments/-/issues/4"},
			repo:  payments, wantRefs: []domain.TicketRef{{Number: 4}}},
		{name: "another port is another host",
			texts: []string{"https://git.corp.example:8443/gitlab/acme/platform/payments/-/issues/4"},
			repo:  payments,
			wantSkipped: []domain.Skipped{{Ref: "https://git.corp.example:8443/gitlab/acme/platform/payments/-/issues/4",
				Reason: "issue reference from another host (git.corp.example:8443)"}}},
		{name: "missing connection prefix is another project",
			texts: []string{"https://git.corp.example/acme/platform/payments/-/issues/4"},
			repo:  payments,
			wantSkipped: []domain.Skipped{{Ref: "https://git.corp.example/acme/platform/payments/-/issues/4",
				Reason: other + "acme/platform/payments)"}}},
		{name: "http is no issue URL", texts: []string{"http://git.corp.example/gitlab/acme/platform/payments/-/issues/4"}, repo: payments},
		{name: "GitHub issue URL keeps today's reason",
			texts: []string{"https://github.com/x/y/issues/2"}, repo: payments,
			wantSkipped: []domain.Skipped{{Ref: "https://github.com/x/y/issues/2", Reason: "issue reference from a different repository (x/y)"}}},
		{name: "one segment is relative to the namespace, case-insensitive",
			texts: []string{"PAYMENTS#9 platform/payments#10 Acme/Platform/Payments#11"}, repo: payments,
			wantRefs:    []domain.TicketRef{{Number: 9}, {Number: 11}},
			wantSkipped: []domain.Skipped{{Ref: "platform/payments#10", Reason: other + "platform/payments)"}}},
		{name: "a project reference starts the text or follows a non-word character",
			texts: []string{"a_b#1 (c.d#2) .#3"}, repo: payments,
			wantSkipped: []domain.Skipped{
				{Ref: "a_b#1", Reason: other + "a_b)"},
				{Ref: "c.d#2", Reason: other + "c.d)"},
				{Ref: ".#3", Reason: other + ".)"},
			}},
		{name: "an empty last segment is no project reference", texts: []string{"x/#2 /#3"}, repo: payments},
		{name: "leftmost start that fits, as a lookbehind regex: after a dash, not inside a word",
			texts: []string{"/x-payments#4 /acme/payments#5"}, repo: payments,
			wantRefs: []domain.TicketRef{{Number: 4}}},
		{name: "22 segments do not fit and every shorter start follows a slash",
			texts: []string{strings.Repeat("a/", 21) + "b#1"}, repo: payments},
		{name: "bare # needs no word or slash before it", texts: []string{"ab/#1 /#2 (#3) x #4"}, repo: payments,
			wantRefs: []domain.TicketRef{{Number: 3}, {Number: 4}}},
		{name: "issue URL inside is not read twice, a fragment is no reference",
			texts: []string{projectURL + "/-/issues/7#note_3"}, repo: payments, wantRefs: []domain.TicketRef{{Number: 7}}},
		{name: "merge request references are ignored", texts: []string{"!12 !payments"}, repo: payments},
		{name: "Jira keys and standard names", texts: []string{"ABC-123 UTF-8"}, repo: payments,
			wantSkipped: []domain.Skipped{{Ref: "ABC-123", Reason: "Jira/Linear-style keys are not supported (server/specs/05-intent-layer.md)"}}},
		{name: "no web address matches no URL (fail closed)",
			texts: []string{projectURL + "/-/issues/7"},
			repo:  domain.Repo{Owner: "acme/platform", Name: "payments", Provider: "gitlab", Host: "git.corp.example"},
			wantSkipped: []domain.Skipped{{Ref: projectURL + "/-/issues/7",
				Reason: other + "gitlab/acme/platform/payments)"}}},
		{name: "a web address of another host is not trusted",
			texts: []string{projectURL + "/-/issues/7"},
			repo:  domain.Repo{Owner: "acme", Name: "p", Provider: "gitlab", Host: "git.corp.example", WebURL: "https://evil.example/gitlab/acme/platform/payments"},
			wantSkipped: []domain.Skipped{{Ref: projectURL + "/-/issues/7",
				Reason: other + "gitlab/acme/platform/payments)"}}},
		{name: "empty texts", texts: []string{"", ""}, repo: payments},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ExtractGitLabTickets(tc.texts, tc.repo)
			if diff := cmp.Diff(append([]domain.TicketRef{}, tc.wantRefs...), got.Refs); diff != "" {
				t.Errorf("ExtractGitLabTickets(%q).Refs mismatch (-want +got):\n%s", tc.texts, diff)
			}
			if diff := cmp.Diff(append([]domain.Skipped{}, tc.wantSkipped...), got.Skipped); diff != "" {
				t.Errorf("ExtractGitLabTickets(%q).Skipped mismatch (-want +got):\n%s", tc.texts, diff)
			}
		})
	}
}

func TestExtractGitLabDocs(t *testing.T) {
	const (
		notBlob = "not a blob or tree URL of this GitLab project (no arbitrary URL fetch)"
		outside = "doc link points outside this project ("
	)
	tests := []struct {
		name        string
		texts       []string
		wantRefs    []string
		wantSkipped []domain.Skipped
	}{
		{name: "blob and tree URLs of this project, decoded, query and fragment cut",
			texts:    []string{projectURL + "/-/blob/main/docs/My%20Plan.md?ref_type=heads and " + projectURL + "/-/tree/v1/specs/a.md#L3"},
			wantRefs: []string{"specs/a.md", "docs/My Plan.md"}},
		{name: "markdown link to a blob",
			texts:    []string{"[plan](" + projectURL + "/-/blob/main/docs/plans/p.md)"},
			wantRefs: []string{"docs/plans/p.md"}},
		{name: "another project on the same host",
			texts: []string{"[x](https://git.corp.example/gitlab/acme/other/-/blob/main/a.md) https://git.corp.example/gitlab/acme/other/-/blob/main/b.md"},
			wantSkipped: []domain.Skipped{
				{Ref: "https://git.corp.example/gitlab/acme/other/-/blob/main/a.md", Reason: outside + "gitlab/acme/other)"},
				{Ref: "https://git.corp.example/gitlab/acme/other/-/blob/main/b.md", Reason: outside + "gitlab/acme/other)"},
			}},
		{name: "another host, a plain URL and http are not fetched",
			texts: []string{"[a](https://gitlab.com/acme/platform/payments/-/blob/main/a.md) [b](https://example.com/readme.md) [c](" + strings.Replace(projectURL, "https", "http", 1) + "/-/blob/main/c.md)"},
			wantSkipped: []domain.Skipped{
				{Ref: "https://gitlab.com/acme/platform/payments/-/blob/main/a.md", Reason: notBlob},
				{Ref: "https://example.com/readme.md", Reason: notBlob},
				{Ref: strings.Replace(projectURL, "https", "http", 1) + "/-/blob/main/c.md", Reason: notBlob},
			}},
		{name: "a blob URL inside another URL's query is not this project's",
			texts: []string{"[a](https://evil.example/r?u=" + projectURL + "/-/blob/main/a.md)"},
			wantSkipped: []domain.Skipped{
				{Ref: "https://evil.example/r?u=" + projectURL + "/-/blob/main/a.md", Reason: notBlob},
			}},
		{name: "prose paths, relative links, unsafe paths",
			texts:       []string{"see docs/a.md and [b](./b.md) [c](../c.md) and https://git.corp.example/x/y.md"},
			wantRefs:    []string{"docs/a.md", "b.md"},
			wantSkipped: []domain.Skipped{{Ref: "../c.md", Reason: "unsafe path"}}},
		{name: "cap of five",
			texts:       []string{"a.md b.md c.md d.md e.md f.md"},
			wantRefs:    []string{"a.md", "b.md", "c.md", "d.md", "e.md"},
			wantSkipped: []domain.Skipped{{Ref: "f.md", Reason: "doc limit reached (max 5)"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ExtractGitLabDocs(tc.texts, payments)
			if err != nil {
				t.Fatalf("ExtractGitLabDocs(%q) error = %v", tc.texts, err)
			}
			var refs []string
			for _, r := range got.Refs {
				refs = append(refs, r.Path)
			}
			if diff := cmp.Diff(tc.wantRefs, refs); diff != "" {
				t.Errorf("ExtractGitLabDocs(%q).Refs mismatch (-want +got):\n%s", tc.texts, diff)
			}
			if diff := cmp.Diff(append([]domain.Skipped{}, tc.wantSkipped...), got.Skipped); diff != "" {
				t.Errorf("ExtractGitLabDocs(%q).Skipped mismatch (-want +got):\n%s", tc.texts, diff)
			}
		})
	}
}

func TestExtractGitLabDocs_MalformedEscape(t *testing.T) {
	_, err := domain.ExtractGitLabDocs([]string{projectURL + "/-/blob/main/a%zz.md"}, payments)
	if !errors.Is(err, reviewer.ErrURIMalformed) {
		t.Errorf("ExtractGitLabDocs(malformed escape) error = %v, want %v", err, reviewer.ErrURIMalformed)
	}
}

func FuzzExtractGitLab(f *testing.F) {
	f.Add(ec20Corpus)
	f.Add("[a](" + projectURL + "/-/blob/main/a.md) x/y#1 //#2 " + projectURL + "/issues/9")
	f.Fuzz(func(t *testing.T, text string) {
		links := domain.ExtractGitLabTickets([]string{text, text}, payments)
		if len(links.Refs) > domain.TicketMax {
			t.Errorf("ExtractGitLabTickets(%q) returned %d refs, want at most %d", text, len(links.Refs), domain.TicketMax)
		}
		if docs, err := domain.ExtractGitLabDocs([]string{text}, payments); err == nil && len(docs.Refs) > domain.DocMax {
			t.Errorf("ExtractGitLabDocs(%q) returned %d refs, want at most %d", text, len(docs.Refs), domain.DocMax)
		}
	})
}

// Security review r1 MEDIUM-1: the "<p>#<n>" scan of a 1 MB text (a description or a commit
// message of a merge request) must not be quadratic; it took 20 s before it was bounded.
func TestExtractGitLabTickets_AdversarialTextIsLinear(t *testing.T) {
	size := 1 << 20
	if raceEnabled {
		size = 1 << 18 // the race detector slows the extractor ~10x; normal builds cover 1 MB
	}
	inputs := map[string]string{
		"dashes then slash then hash": strings.Repeat("-a", size/2) + "/#1",
		"dashes then double slash":    strings.Repeat("-a", size/2) + "//x#1",
		"dashes, 22 slashes at end":   strings.Repeat("-a", size/2) + strings.Repeat("/a", 22) + "#1",
		"slashes only":                strings.Repeat("a/", size/2) + "#1",
		"many hashes after a run":     strings.Repeat("-a", size/4) + "/" + strings.Repeat("#1 ", size/6),
		"hash heavy":                  strings.Repeat("a-b/c#1 ", size/8),
		"issue urls and hashes":       strings.Repeat(projectURL+"/-/issues/1 #2 ", size/(len(projectURL)+16)),
	}
	// The quadratic scan took 20 s on the first input; a linear pass takes well under a second
	// (the reference-heavy inputs, 170k references, up to ~1.5 s on a loaded host).
	bound := 5 * time.Second
	if raceEnabled {
		bound = 30 * time.Second
	}
	for name, text := range inputs {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			links := domain.ExtractGitLabTickets([]string{text}, payments)
			if took := time.Since(start); took > bound {
				t.Errorf("ExtractGitLabTickets(%s, %d bytes) took %v, want <= %v", name, len(text), took, bound)
			}
			if len(links.Refs) > domain.TicketMax {
				t.Errorf("ExtractGitLabTickets(%s) returned %d refs, want at most %d", name, len(links.Refs), domain.TicketMax)
			}
		})
	}
}
