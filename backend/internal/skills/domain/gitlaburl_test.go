package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/skills/domain"
)

func TestImportHostKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://gitlab.com/a/b/-/blob/main/a.md", "gitlab.com", true},
		{"  HTTPS://GitLab.COM:443/a  ", "gitlab.com", true},
		{"https://gitlab.com./a", "gitlab.com", true},
		{"https://git.corp.example:8443/x", "git.corp.example:8443", true},
		{"https://[::1]:8443/x", "[::1]:8443", true},
		{"https://[0:0:0:0:0:0:0:1]/x", "[::1]", true},
		{"https://0x7f.1/x", "127.0.0.1", true},
		{"https://u:p@gitlab.com/x", "gitlab.com", true},
		{"http://gitlab.com/x", "", false},
		{"https:///x", "", false},
		{"https://gitlab.com:0/x", "", false},
		{"https://gitlab.com:99999/x", "", false},
		{"https://a b/x", "", false},
		{"not a url", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := domain.ImportHostKey(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("ImportHostKey(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseGitLabFileURL_Forms(t *testing.T) {
	tests := []struct {
		name, raw, base string
		want            domain.GitLabFileLink
	}{
		{
			name: "blob with query",
			raw:  "https://gitlab.com/acme/skills/-/blob/main/a.md?ref_type=heads",
			base: "https://gitlab.com",
			want: domain.GitLabFileLink{Project: "acme/skills", Rest: []string{"main", "a.md"}},
		},
		{
			name: "raw",
			raw:  "https://gitlab.com/acme/skills/-/raw/main/dir/a.md",
			base: "https://gitlab.com",
			want: domain.GitLabFileLink{Project: "acme/skills", Rest: []string{"main", "dir", "a.md"}},
		},
		{
			name: "EC23 prefix, nested ref, port 443 spelled out",
			raw:  "https://git.corp.example:443/gitlab/acme/skills/-/blob/feature/x/skills/a.md",
			base: "https://git.corp.example/gitlab",
			want: domain.GitLabFileLink{Project: "acme/skills", Rest: []string{"feature", "x", "skills", "a.md"}},
		},
		{
			name: "non-443 port and uppercase host",
			raw:  "https://GIT.corp.example:8443/g/s/p/-/blob/v1/a.md#L1",
			base: "https://git.corp.example:8443",
			want: domain.GitLabFileLink{Project: "g/s/p", Rest: []string{"v1", "a.md"}},
		},
		{
			name: "encoded segments are decoded",
			raw:  "https://gitlab.com/a/b/-/blob/main/my%20skill%23.md",
			base: "https://gitlab.com",
			want: domain.GitLabFileLink{Project: "a/b", Rest: []string{"main", "my skill#.md"}},
		},
		{
			name: "21 project segments",
			raw:  "https://gitlab.com/" + strings.Repeat("g/", 20) + "p/-/blob/main/a.md",
			base: "https://gitlab.com",
			want: domain.GitLabFileLink{Project: strings.Repeat("g/", 20) + "p", Rest: []string{"main", "a.md"}},
		},
		{
			name: "prefix with the same name as a group",
			raw:  "https://h.example/gl/gl/p/-/blob/main/a.md",
			base: "https://h.example/gl",
			want: domain.GitLabFileLink{Project: "gl/p", Rest: []string{"main", "a.md"}},
		},
		{
			name: "IPv6 host",
			raw:  "https://[0:0:0:0:0:0:0:1]:8443/a/b/-/raw/main/a.md",
			base: "https://[::1]:8443",
			want: domain.GitLabFileLink{Project: "a/b", Rest: []string{"main", "a.md"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, matched, err := domain.ParseGitLabFileURL(tc.raw, tc.base)
			if err != nil || !matched {
				t.Fatalf("ParseGitLabFileURL(%q, %q) = (_, %v, %v), want matched, no error", tc.raw, tc.base, matched, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseGitLabFileURL(%q, %q) mismatch (-want +got):\n%s", tc.raw, tc.base, diff)
			}
		})
	}
}

// EC25 and the rest of FR30: any other path on the connection's host is refused with the fixed
// message, and nothing is matched for another host or another path prefix.
func TestParseGitLabFileURL_Refused(t *testing.T) {
	const base = "https://git.corp.example/gitlab"
	msg := "Only GitLab blob or raw file links can be imported from git.corp.example"
	tests := []struct {
		name, raw string
		wantMsg   string
	}{
		{"EC25 tree", "https://git.corp.example/gitlab/a/b/-/tree/main/skills", msg},
		{"EC25 snippets", "https://git.corp.example/gitlab/a/b/-/snippets/1", msg},
		{"project only", "https://git.corp.example/gitlab/a/b", msg},
		{"prefix only", "https://git.corp.example/gitlab", msg},
		{"prefix with slash", "https://git.corp.example/gitlab/", msg},
		{"one-segment project", "https://git.corp.example/gitlab/a/-/blob/main/a.md", msg},
		{"no project", "https://git.corp.example/gitlab/-/blob/main/a.md", msg},
		{"22 project segments", "https://git.corp.example/gitlab/" + strings.Repeat("g/", 21) + "p/-/blob/main/a.md", msg},
		{"blob without file", "https://git.corp.example/gitlab/a/b/-/blob/main", msg},
		{"blob without ref and file", "https://git.corp.example/gitlab/a/b/-/blob", msg},
		{"blob/ only", "https://git.corp.example/gitlab/a/b/-/blob/", msg},
		{"trailing slash", "https://git.corp.example/gitlab/a/b/-/blob/main/a.md/", msg},
		{"empty segment", "https://git.corp.example/gitlab/a/b/-/blob/main//a.md", msg},
		{"dot-dot in file", "https://git.corp.example/gitlab/a/b/-/blob/main/../a.md", msg},
		{"dot-dot ref", "https://git.corp.example/gitlab/a/b/-/blob/../a.md", msg},
		{"encoded slash in ref", "https://git.corp.example/gitlab/a/b/-/blob/feature%2Fx/a.md", msg},
		{"encoded control char", "https://git.corp.example/gitlab/a/b/-/blob/main/a%00.md", msg},
		{"project segment with odd char", "https://git.corp.example/gitlab/a/b%20c/-/blob/main/a.md", msg},
		{"project segment dot-dot", "https://git.corp.example/gitlab/a/../-/blob/main/a.md", msg},
		{"project segment .git", "https://git.corp.example/gitlab/a/.git/-/blob/main/a.md", msg},
		{"project segment leading dash", "https://git.corp.example/gitlab/a/-b/-/blob/main/a.md", msg},
		{"api route", "https://git.corp.example/gitlab/api/v4/projects/1/repository/files/a/raw", msg},
		{"credentials", "https://u:p@git.corp.example/gitlab/a/b/-/blob/main/a.md", "URLs with credentials are not allowed"},
		{"empty password", "https://u@git.corp.example/gitlab/a/b/-/blob/main/a.md", "URLs with credentials are not allowed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, matched, err := domain.ParseGitLabFileURL(tc.raw, base)
			if !matched || err == nil {
				t.Fatalf("ParseGitLabFileURL(%q) = (%+v, %v, %v), want matched and an error", tc.raw, got, matched, err)
			}
			var ie *domain.ImportError
			if !errors.As(err, &ie) {
				t.Fatalf("ParseGitLabFileURL(%q) error = %v (%T), want *domain.ImportError", tc.raw, err, err)
			}
			if ie.Message != tc.wantMsg {
				t.Errorf("ParseGitLabFileURL(%q) message = %q, want %q", tc.raw, ie.Message, tc.wantMsg)
			}
			if len(got.Rest) != 0 || got.Project != "" {
				t.Errorf("ParseGitLabFileURL(%q) link = %+v, want zero value on error", tc.raw, got)
			}
		})
	}
}

func TestParseGitLabFileURL_NotMatched(t *testing.T) {
	tests := []struct {
		name, raw, base string
	}{
		{"other host", "https://gitlab.com/a/b/-/blob/main/a.md", "https://git.corp.example"},
		{"suffix host", "https://evilgit.corp.example/a/b/-/blob/main/a.md", "https://git.corp.example"},
		{"subdomain host", "https://x.git.corp.example/a/b/-/blob/main/a.md", "https://git.corp.example"},
		{"other port", "https://git.corp.example:8443/a/b/-/blob/main/a.md", "https://git.corp.example"},
		{"explicit port vs none", "https://git.corp.example/a/b/-/blob/main/a.md", "https://git.corp.example:8443"},
		{"outside prefix", "https://git.corp.example/other/a/b/-/blob/main/a.md", "https://git.corp.example/gitlab"},
		{"prefix is a string prefix only", "https://git.corp.example/gitlabx/a/b/-/blob/main/a.md", "https://git.corp.example/gitlab"},
		{"prefix case differs", "https://git.corp.example/GitLab/a/b/-/blob/main/a.md", "https://git.corp.example/gitlab"},
		{"url shorter than prefix", "https://git.corp.example/gitlab", "https://git.corp.example/gitlab/sub"},
		{"http scheme", "http://git.corp.example/a/b/-/blob/main/a.md", "https://git.corp.example"},
		{"no host", "https:///a/b/-/blob/main/a.md", "https://git.corp.example"},
		{"not a url", "not a url", "https://git.corp.example"},
		{"empty", "", "https://git.corp.example"},
		{"invalid port", "https://git.corp.example:abc/a/b/-/blob/main/a.md", "https://git.corp.example"},
		{"bad base", "https://git.corp.example/a/b/-/blob/main/a.md", "::not a url"},
		{"empty base", "https://git.corp.example/a/b/-/blob/main/a.md", ""},
		{"credentials on another host", "https://u:p@gitlab.com/a/b/-/blob/main/a.md", "https://git.corp.example"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, matched, err := domain.ParseGitLabFileURL(tc.raw, tc.base)
			if matched || err != nil {
				t.Errorf("ParseGitLabFileURL(%q, %q) = (%+v, %v, %v), want not matched and no error", tc.raw, tc.base, got, matched, err)
			}
		})
	}
}

// A URL that net/url cannot parse is never matched, and the public rule refuses it too, so
// nothing is fetched for it.
func TestParseGitLabFileURL_Unparsable(t *testing.T) {
	const raw = "https://git.corp.example/gitlab/a/b/-/blob/main/a%zz.md"
	if _, matched, err := domain.ParseGitLabFileURL(raw, "https://git.corp.example/gitlab"); matched || err != nil {
		t.Errorf("ParseGitLabFileURL(%q) = (_, %v, %v), want not matched, no error", raw, matched, err)
	}
	u, err := domain.ParseImportURL(raw)
	if err == nil {
		t.Fatalf("ParseImportURL(%q) = %v, want an error", raw, u)
	}
	if got := importMessage(t, err); got != "Not a valid URL" {
		t.Errorf("ParseImportURL(%q) message = %q, want %q", raw, got, "Not a valid URL")
	}
}

// ParseGitLabFileURL accepts what the public rule would refuse (the connection's address rules
// apply instead); ParseImportURL keeps refusing the same URL when no connection matched.
func TestParseGitLabFileURL_NoNetworkRefusal(t *testing.T) {
	const raw = "https://10.0.0.5:8443/gitlab/acme/skills/-/blob/feature/x/skills/a.md"
	if _, matched, err := domain.ParseGitLabFileURL(raw, "https://10.0.0.5:8443/gitlab"); !matched || err != nil {
		t.Errorf("ParseGitLabFileURL(%q) = (_, %v, %v), want matched, no error", raw, matched, err)
	}
	if u, err := domain.ParseImportURL(raw); err == nil {
		t.Errorf("ParseImportURL(%q) = %v, want an error", raw, u)
	}
}

func TestGitLabFileLink_Splits(t *testing.T) {
	tests := []struct {
		name string
		rest []string
		want []domain.GitLabFileSplit
	}{
		{"ref and file", []string{"main", "a.md"}, []domain.GitLabFileSplit{{Ref: "main", File: "a.md"}}},
		{"EC23 two splits shortest first", []string{"feature", "x", "a.md"}, []domain.GitLabFileSplit{
			{Ref: "feature", File: "x/a.md"},
			{Ref: "feature/x", File: "a.md"},
		}},
		{"never all segments", []string{"a", "b", "c", "d"}, []domain.GitLabFileSplit{
			{Ref: "a", File: "b/c/d"}, {Ref: "a/b", File: "c/d"}, {Ref: "a/b/c", File: "d"},
		}},
		{"at most five", []string{"1", "2", "3", "4", "5", "6", "7", "8"}, []domain.GitLabFileSplit{
			{Ref: "1", File: "2/3/4/5/6/7/8"},
			{Ref: "1/2", File: "3/4/5/6/7/8"},
			{Ref: "1/2/3", File: "4/5/6/7/8"},
			{Ref: "1/2/3/4", File: "5/6/7/8"},
			{Ref: "1/2/3/4/5", File: "6/7/8"},
		}},
		{"zero value", nil, []domain.GitLabFileSplit{}},
		{"one segment", []string{"main"}, []domain.GitLabFileSplit{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.GitLabFileLink{Project: "a/b", Rest: tc.rest}.Splits()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Splits(%v) mismatch (-want +got):\n%s", tc.rest, diff)
			}
		})
	}
}

func FuzzParseGitLabFileURL(f *testing.F) {
	for _, s := range []string{
		"https://git.corp.example/gitlab/a/b/-/blob/main/a.md",
		"https://git.corp.example/gitlab/a/b/-/raw/m/a%2Fb.md?x#y",
		"https://u:p@git.corp.example/gitlab/a/b/-/blob/main/a.md",
		"https://git.corp.example/gitlab/a/b/-/blob/main//..",
		"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		link, matched, err := domain.ParseGitLabFileURL(raw, "https://git.corp.example/gitlab")
		if !matched && err != nil {
			t.Fatalf("ParseGitLabFileURL(%q) = unmatched with error %v", raw, err)
		}
		if err != nil {
			return
		}
		if !matched {
			return
		}
		if n := len(strings.Split(link.Project, "/")); n < 2 || n > 21 || len(link.Rest) < 2 {
			t.Fatalf("ParseGitLabFileURL(%q) = %+v, out of the accepted shape", raw, link)
		}
		for _, s := range link.Rest {
			if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\") {
				t.Fatalf("ParseGitLabFileURL(%q) rest segment %q is unsafe", raw, s)
			}
		}
	})
}
