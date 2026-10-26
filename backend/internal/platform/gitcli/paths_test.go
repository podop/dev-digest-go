package gitcli_test

import (
	"errors"
	"path/filepath"
	"testing"

	"dev-digest-go/backend/internal/platform/gitcli"
)

func newClient(t *testing.T, opts gitcli.Options) *gitcli.Client {
	t.Helper()
	c, err := gitcli.New(opts)
	if err != nil {
		t.Fatalf("New(%+v) error: %v", opts, err)
	}
	return c
}

func TestNew_RequiresCloneRoot(t *testing.T) {
	if _, err := gitcli.New(gitcli.Options{}); err == nil {
		t.Error("New(Options{}) = nil error, want error for empty clone root")
	}
}

func TestClonePath(t *testing.T) {
	root := t.TempDir()
	c := newClient(t, gitcli.Options{CloneRoot: root})

	tests := []struct {
		name        string
		owner, repo string
		want        string
		wantErr     error
	}{
		{name: "plain", owner: "octo", repo: "hello", want: filepath.Join(root, "octo", "hello")},
		{name: "dots dashes underscores", owner: "a.b-c_d", repo: "x.y", want: filepath.Join(root, "a.b-c_d", "x.y")},
		{name: "name ending in .git", owner: "o", repo: "r.git", want: filepath.Join(root, "o", "r.git")},
		{name: "owner dot dot", owner: "..", repo: "r", wantErr: gitcli.ErrInvalidSegment},
		{name: "name dot dot", owner: "o", repo: "..", wantErr: gitcli.ErrInvalidSegment},
		{name: "owner dot", owner: ".", repo: "r", wantErr: gitcli.ErrInvalidSegment},
		{name: "name dot", owner: "o", repo: ".", wantErr: gitcli.ErrInvalidSegment},
		{name: "name .git", owner: "o", repo: ".git", wantErr: gitcli.ErrInvalidSegment},
		{name: "name .GIT any case", owner: "o", repo: ".GiT", wantErr: gitcli.ErrInvalidSegment},
		{name: "owner .git", owner: ".git", repo: "r", wantErr: gitcli.ErrInvalidSegment},
		{name: "leading dash owner", owner: "-o", repo: "r", wantErr: gitcli.ErrInvalidSegment},
		{name: "leading dash name", owner: "o", repo: "-r", wantErr: gitcli.ErrInvalidSegment},
		{name: "empty owner", owner: "", repo: "r", wantErr: gitcli.ErrInvalidSegment},
		{name: "empty name", owner: "o", repo: "", wantErr: gitcli.ErrInvalidSegment},
		{name: "slash in owner", owner: "a/b", repo: "r", wantErr: gitcli.ErrInvalidSegment},
		{name: "traversal in name", owner: "o", repo: "../x", wantErr: gitcli.ErrInvalidSegment},
		{name: "absolute name", owner: "o", repo: "/etc", wantErr: gitcli.ErrInvalidSegment},
		{name: "backslash", owner: "o", repo: `a\b`, wantErr: gitcli.ErrInvalidSegment},
		{name: "space", owner: "o", repo: "a b", wantErr: gitcli.ErrInvalidSegment},
		{name: "trailing newline", owner: "o", repo: "r\n", wantErr: gitcli.ErrInvalidSegment},
		{name: "nul byte", owner: "o", repo: "r\x00", wantErr: gitcli.ErrInvalidSegment},
		{name: "unicode", owner: "o", repo: "répo", wantErr: gitcli.ErrInvalidSegment},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.ClonePath(tc.owner, tc.repo)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ClonePath(%q, %q) error = %v, want %v", tc.owner, tc.repo, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("ClonePath(%q, %q) = %q, want %q", tc.owner, tc.repo, got, tc.want)
			}
		})
	}
}

func TestClonePath_GitLabKey(t *testing.T) {
	root := t.TempDir()
	c := newClient(t, gitcli.Options{CloneRoot: root})

	tests := []struct {
		name        string
		owner, repo string
		want        string
		wantErr     error
	}{
		{name: "dns host", owner: "@gitlab/gitlab.com", repo: "42", want: filepath.Join(root, "@gitlab", "gitlab.com", "42")},
		{name: "host with port", owner: "@gitlab/git.corp.example_8443", repo: "7", want: filepath.Join(root, "@gitlab", "git.corp.example_8443", "7")},
		{name: "ipv4 host with port", owner: "@gitlab/10.0.0.5_8443", repo: "1", want: filepath.Join(root, "@gitlab", "10.0.0.5_8443", "1")},
		{name: "ipv6 host", owner: "@gitlab/__1-", repo: "9", want: filepath.Join(root, "@gitlab", "__1-", "9")},
		{name: "ipv6 host with port", owner: "@gitlab/__1-_8443", repo: "9", want: filepath.Join(root, "@gitlab", "__1-_8443", "9")},
		{name: "big project id", owner: "@gitlab/gitlab.com", repo: "9223372036854775807", want: filepath.Join(root, "@gitlab", "gitlab.com", "9223372036854775807")},
		{name: "github owner named gitlab", owner: "gitlab", repo: "gitlab.com", want: filepath.Join(root, "gitlab", "gitlab.com")},
		{name: "github form unchanged", owner: "octo", repo: "hello", want: filepath.Join(root, "octo", "hello")},

		{name: "no host", owner: "@gitlab/", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "only the top level", owner: "@gitlab", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host dot dot", owner: "@gitlab/..", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host dot", owner: "@gitlab/.", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host .git", owner: "@gitlab/.git", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host leading dash", owner: "@gitlab/-h", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host with slash", owner: "@gitlab/a/b", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host traversal", owner: "@gitlab/../x", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host backslash", owner: `@gitlab/a\b`, repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host colon", owner: "@gitlab/h:8443", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host bracket", owner: "@gitlab/[::1]", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "host nul", owner: "@gitlab/h\x00", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "top level in other case", owner: "@GitLab/gitlab.com", repo: "42", wantErr: gitcli.ErrInvalidSegment},
		{name: "project path instead of id", owner: "@gitlab/gitlab.com", repo: "group", wantErr: gitcli.ErrInvalidSegment},
		{name: "project id empty", owner: "@gitlab/gitlab.com", repo: "", wantErr: gitcli.ErrInvalidSegment},
		{name: "project id dot dot", owner: "@gitlab/gitlab.com", repo: "..", wantErr: gitcli.ErrInvalidSegment},
		{name: "project id negative", owner: "@gitlab/gitlab.com", repo: "-1", wantErr: gitcli.ErrInvalidSegment},
		{name: "project id with slash", owner: "@gitlab/gitlab.com", repo: "1/2", wantErr: gitcli.ErrInvalidSegment},
		{name: "project id traversal", owner: "@gitlab/gitlab.com", repo: "../1", wantErr: gitcli.ErrInvalidSegment},
		{name: "project id too long", owner: "@gitlab/gitlab.com", repo: "12345678901234567890", wantErr: gitcli.ErrInvalidSegment},
		{name: "project id trailing newline", owner: "@gitlab/gitlab.com", repo: "42\n", wantErr: gitcli.ErrInvalidSegment},
		{name: "github owner with at sign", owner: "@gitlab", repo: "r", wantErr: gitcli.ErrInvalidSegment},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.ClonePath(tc.owner, tc.repo)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ClonePath(%q, %q) error = %v, want %v", tc.owner, tc.repo, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("ClonePath(%q, %q) = %q, want %q", tc.owner, tc.repo, got, tc.want)
			}
		})
	}
}
