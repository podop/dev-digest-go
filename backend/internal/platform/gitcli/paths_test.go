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
