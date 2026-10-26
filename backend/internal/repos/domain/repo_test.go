package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/repos/domain"
)

func gh(path, owner, name string, ssh bool) domain.Parsed {
	return domain.Parsed{Provider: codehost.GitHub, Host: "github.com", SSH: ssh, Path: path, Owner: owner, Name: name}
}

func gl(host string, ssh bool, path, owner, name string) domain.Parsed {
	return domain.Parsed{Provider: codehost.GitLab, Host: host, SSH: ssh, Path: path, Owner: owner, Name: name}
}

func TestParseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want domain.Parsed
	}{
		{name: "https", in: "https://github.com/o/r", want: gh("o/r", "o", "r", false)},
		{name: "https .git", in: "https://github.com/o/r.git", want: gh("o/r", "o", "r", false)},
		{name: "https trailing slash", in: "https://github.com/o/r/", want: gh("o/r", "o", "r", false)},
		{name: "https .git and slash", in: "https://github.com/o/r.git/", want: gh("o/r", "o", "r", false)},
		{name: "trimmed", in: " \thttps://github.com/o/r.git \n", want: gh("o/r", "o", "r", false)},
		{name: "ssh", in: "git@github.com:o/r", want: gh("o/r", "o", "r", true)},
		{name: "ssh .git", in: "git@github.com:o/r.git", want: gh("o/r", "o", "r", true)},
		{name: "case kept", in: "https://github.com/Acme/Pay-API_v2.js", want: gh("Acme/Pay-API_v2.js", "Acme", "Pay-API_v2.js", false)},
		{name: "dots inside", in: "https://github.com/a.b/c..d", want: gh("a.b/c..d", "a.b", "c..d", false)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ParseURL(tc.in)
			if err != nil {
				t.Fatalf("ParseURL(%q) error = %v", tc.in, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseURL(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestParseURL_GitLab(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want domain.Parsed
	}{
		{name: "https", in: "https://gitlab.com/acme/web", want: gl("gitlab.com", false, "acme/web", "acme", "web")},
		{name: "https .git", in: "https://gitlab.com/acme/web.git", want: gl("gitlab.com", false, "acme/web", "acme", "web")},
		{name: "https slash", in: "https://gitlab.com/acme/web/", want: gl("gitlab.com", false, "acme/web", "acme", "web")},
		{name: "https .git and slash", in: "https://gitlab.com/acme/web.git/", want: gl("gitlab.com", false, "acme/web", "acme", "web")},
		{name: "trimmed", in: "  https://gitlab.com/acme/web\n", want: gl("gitlab.com", false, "acme/web", "acme", "web")},
		{name: "host lower-cased, path case kept", in: "https://GitLab.COM/Acme/Web", want: gl("gitlab.com", false, "Acme/Web", "Acme", "Web")},
		{name: "nested groups", in: "https://gitlab.com/group/sub/deep/project", want: gl("gitlab.com", false, "group/sub/deep/project", "group/sub/deep", "project")},
		{name: "port", in: "https://git.corp.example:8443/g/p", want: gl("git.corp.example:8443", false, "g/p", "g", "p")},
		{name: "port 443 is the default", in: "https://git.corp.example:443/g/p", want: gl("git.corp.example", false, "g/p", "g", "p")},
		{name: "path prefix stays in Path", in: "https://git.corp.example/gitlab/g/p", want: gl("git.corp.example", false, "gitlab/g/p", "gitlab/g", "p")},
		{name: "ipv4 host", in: "https://10.0.0.5:8080/g/p.git", want: gl("10.0.0.5:8080", false, "g/p", "g", "p")},
		{name: "trailing dot dropped from the host key", in: "https://GitLab.Example.com.:8443/g/p", want: gl("gitlab.example.com:8443", false, "g/p", "g", "p")},
		{name: "ssh trailing dot", in: "git@gitlab.example.com.:g/p", want: gl("gitlab.example.com", true, "g/p", "g", "p")},
		{name: "ipv6 host canonical", in: "https://[0:0:0:0:0:0:0:1]:8443/g/p", want: gl("[::1]:8443", false, "g/p", "g", "p")},
		{name: "ipv6 host", in: "https://[::1]:8443/g/p", want: gl("[::1]:8443", false, "g/p", "g", "p")},
		{name: "ssh", in: "git@gitlab.com:acme/web", want: gl("gitlab.com", true, "acme/web", "acme", "web")},
		{name: "ssh .git nested", in: "git@Git.Corp.Example:group/sub/proj.git", want: gl("git.corp.example", true, "group/sub/proj", "group/sub", "proj")},
		{name: "look-alike host is another host", in: "https://github.com.evil.com/o/r", want: gl("github.com.evil.com", false, "o/r", "o", "r")},
		{name: "dots inside", in: "https://gitlab.com/a.b/c..d", want: gl("gitlab.com", false, "a.b/c..d", "a.b", "c..d")},
		{name: "21 segments", in: "https://gitlab.com/" + strings.Repeat("a/", 20) + "a", want: gl("gitlab.com", false, strings.Repeat("a/", 20)+"a", strings.Repeat("a/", 19)+"a", "a")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ParseURL(tc.in)
			if err != nil {
				t.Fatalf("ParseURL(%q) error = %v", tc.in, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseURL(%q) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestParseURL_Rejects(t *testing.T) {
	for _, in := range []string{
		"", "   ",
		"http://github.com/o/r",
		"ssh://git@github.com/o/r",
		"https://github.com/o/r/tree/main",
		"https://github.com.evil.com/o",
		"https://github.com/o",
		"https://github.com//r",
		"https://github.com/../r",
		"https://github.com/o/..",
		"https://github.com/./r",
		"https://github.com/o/.git",
		"https://github.com/.GIT/r",
		"https://github.com/-x/r",
		"https://github.com/o/-r",
		"git@github.com:o/r/extra",
		"git@github.com:-x/r",
		"https://github.com/o/r?x=1",
		"prefix https://github.com/o/r",
		"https://GitHub.com/o/r",
		"https://www.github.com/o/r",
		"https://api.github.com/o/r",
		"https://github.com:8443/o/r",
		"git@www.github.com:o/r",
		// GitLab forms
		"http://gitlab.com/o/r",
		"ssh://git@gitlab.com/o/r",
		"ssh://git@gitlab.com:2222/o/r",
		"https://gitlab.com",
		"https://gitlab.com/",
		"https://gitlab.com/o",
		"https://gitlab.com/o/",
		"https://gitlab.com//o/r",
		"https://gitlab.com/o//r",
		"https://gitlab.com/o/r//",
		"https://gitlab.com/o/r?x=1",
		"https://gitlab.com/o/r#frag",
		"https://user@gitlab.com/o/r",
		"https://user:pw@gitlab.com/o/r",
		"https://gitlab.com/o/r/-/merge_requests/1",
		"https://gitlab.com/o/-/r",
		"https://gitlab.com/-/o/r",
		"https://gitlab.com/o/./r",
		"https://gitlab.com/o/../r",
		"https://gitlab.com/o/.git/r",
		"https://gitlab.com/o/%2e%2e/r",
		"https://gitlab.com/o/r%2Fx",
		"https://gitlab.com/o/r s",
		"https://gitlab.com/o\\r",
		"https://gitlab.com/\u00e4/r",
		"https://gitlab.com:/o/r",
		"https://gitlab.com:0/o/r",
		"https://gitlab.com:65536/o/r",
		"https://gitlab.com:08443/o/r",
		"https://gitlab.com:80a/o/r",
		"https://gitlab.com:1:2/o/r",
		"https://-gitlab.com/o/r",
		"https://gitlab..com/o/r",
		"https://github.com./o/r",
		"https://www.github.com./o/r",
		"https://gitlab.com../o/r",
		"https:///o/r",
		"https://[::1/o/r",
		"https://[fe80::1%25eth0]/o/r",
		"https://[gitlab]/o/r",
		"https://[::1]x/o/r",
		"HTTPS://gitlab.com/o/r",
		"git@gitlab.com:/o/r",
		"git@gitlab.com:o",
		"git@gitlab.com:o/r/",
		"git@gitlab.com:o/r/-/x",
		"git@gitlab.com:2222:o/r",
		"git@[::1]:o/r",
		"git@:o/r",
		"git@gitlab.com",
		"user@gitlab.com:o/r",
		"git@user@gitlab.com:o/r",
		"https://gitlab.com/" + strings.Repeat("a/", 40) + "a",
		"https://gitlab.com/" + strings.Repeat("a", 3000),
	} {
		t.Run(in, func(t *testing.T) {
			if got, err := domain.ParseURL(in); !errors.Is(err, domain.ErrInvalidURL) {
				t.Errorf("ParseURL(%q) = %+v, %v, want ErrInvalidURL", in, got, err)
			}
		})
	}
}

func TestParsed_WithoutPrefix(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		prefix  string
		want    domain.Parsed
		wantErr error
	}{
		{name: "no prefix", in: "https://gitlab.com/a/b/c", want: gl("gitlab.com", false, "a/b/c", "a/b", "c")},
		{name: "prefix removed", in: "https://git.corp.example/gitlab/g/p", prefix: "gitlab", want: gl("git.corp.example", false, "g/p", "g", "p")},
		{name: "slashes around prefix", in: "https://git.corp.example/gitlab/g/s/p", prefix: "/gitlab/", want: gl("git.corp.example", false, "g/s/p", "g/s", "p")},
		{name: "nested prefix", in: "https://h/a/b/g/p", prefix: "a/b", want: gl("h", false, "g/p", "g", "p")},
		{name: "outside prefix (EC11)", in: "https://git.corp.example/other/group/proj", prefix: "gitlab", wantErr: domain.ErrOutsidePrefix},
		{name: "prefix is only a string prefix", in: "https://h/gitlabx/g/p", prefix: "gitlab", wantErr: domain.ErrOutsidePrefix},
		{name: "prefix without a project", in: "https://h/gitlab/p", prefix: "gitlab", wantErr: domain.ErrInvalidURL},
		{name: "ssh ignores prefix", in: "git@h:g/p.git", prefix: "gitlab", want: gl("h", true, "g/p", "g", "p")},
		{name: "21 segments after prefix", in: "https://h/pre/" + strings.Repeat("a/", 20) + "a", prefix: "pre", want: gl("h", false, strings.Repeat("a/", 20)+"a", strings.Repeat("a/", 19)+"a", "a")},
		{name: "22 segments (EC12)", in: "https://gitlab.com/" + strings.Repeat("a/", 21) + "a", wantErr: domain.ErrInvalidURL},
		{name: "22 segments over ssh", in: "git@gitlab.com:" + strings.Repeat("a/", 21) + "a", wantErr: domain.ErrInvalidURL},
		{name: "github untouched", in: "https://github.com/o/r", prefix: "x", want: gh("o/r", "o", "r", false)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := domain.ParseURL(tc.in)
			if err != nil {
				t.Fatalf("ParseURL(%q) error = %v", tc.in, err)
			}
			got, err := p.WithoutPrefix(tc.prefix)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("WithoutPrefix(%q) error = %v, want %v", tc.prefix, err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("WithoutPrefix(%q) mismatch (-want +got):\n%s", tc.prefix, diff)
				}
			}
		})
	}
}

func TestParsed_Ref(t *testing.T) {
	p, err := domain.ParseURL("https://gitlab.com/g/s/p")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(domain.Ref{Owner: "g/s", Name: "p"}, p.Ref()); diff != "" {
		t.Errorf("Ref() mismatch (-want +got):\n%s", diff)
	}
}

func FuzzParseURL(f *testing.F) {
	for _, seed := range []string{
		"https://github.com/o/r", "git@github.com:o/r.git", "https://gitlab.com/g/s/p/",
		"git@git.corp.example:g/p.git", "https://git.corp.example:8443/gitlab/g/p",
		"https://[::1]:8443/g/p", "https://gitlab.com/o/r/-/merge_requests/1",
		"https://user@gitlab.com/o/r", "https://gitlab.com/o/%2e%2e/r", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		p, err := domain.ParseURL(in)
		if err != nil {
			if !errors.Is(err, domain.ErrInvalidURL) {
				t.Fatalf("ParseURL(%q) error = %v, want ErrInvalidURL", in, err)
			}
			return
		}
		if p.Provider != codehost.GitHub && p.Provider != codehost.GitLab {
			t.Fatalf("ParseURL(%q).Provider = %q", in, p.Provider)
		}
		if p.Host == "" || p.Host != strings.ToLower(p.Host) || strings.HasSuffix(p.Host, ".") || strings.ContainsAny(p.Host, "/@?#\\ \t\r\n%") {
			t.Fatalf("ParseURL(%q).Host = %q", in, p.Host)
		}
		if p.Path != p.Owner+"/"+p.Name {
			t.Fatalf("ParseURL(%q): Path %q != Owner %q + / + Name %q", in, p.Path, p.Owner, p.Name)
		}
		segs := strings.Split(p.Path, "/")
		if len(segs) < 2 {
			t.Fatalf("ParseURL(%q).Path = %q has fewer than 2 segments", in, p.Path)
		}
		for _, seg := range segs {
			if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, "-") || strings.ToLower(seg) == ".git" ||
				strings.Trim(seg, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-") != "" {
				t.Fatalf("ParseURL(%q).Path = %q has unsafe segment %q", in, p.Path, seg)
			}
		}
		if p.Provider == codehost.GitHub && (len(segs) != 2 || p.Host != codehost.GitHubHost) {
			t.Fatalf("ParseURL(%q) = %+v, bad GitHub result", in, p)
		}
		if p.Provider == codehost.GitLab && (p.Host == "github.com" || strings.HasPrefix(p.Host, "github.com:")) {
			t.Fatalf("ParseURL(%q) = %+v, GitLab result on a GitHub host", in, p)
		}
		if p.SSH && strings.Contains(p.Host, ":") && !strings.HasPrefix(p.Host, "[") {
			t.Fatalf("ParseURL(%q).Host = %q carries a port over SSH", in, p.Host)
		}
	})
}

func TestRef(t *testing.T) {
	r := domain.Ref{Owner: "o", Name: "r"}
	if got, want := r.FullName(), "o/r"; got != want {
		t.Errorf("FullName() = %q, want %q", got, want)
	}
	if got, want := r.CloneURL("https://github.com/"), "https://github.com/o/r.git"; got != want {
		t.Errorf("CloneURL() = %q, want %q", got, want)
	}
}

func TestRepo_Cloned(t *testing.T) {
	path := "/data/o/r"
	if (domain.Repo{}).Cloned() {
		t.Error("Cloned() = true for a repo without clone path")
	}
	if !(domain.Repo{ClonePath: &path}).Cloned() {
		t.Error("Cloned() = false for a repo with clone path")
	}
}
