package peers_test

import (
	"os"
	"path/filepath"
	"testing"

	"dev-digest-go/backend/internal/ci/adapters/peers"
)

func TestCloneFiles_Exists(t *testing.T) {
	clone := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	for path, content := range map[string]string{
		filepath.Join(clone, ".gitlab-ci.yml"):                "stages: []\n",
		filepath.Join(clone, ".gitlab", "devdigest", "a.yml"): "x: 1\n",
		outside: "outside",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(clone, "link.yml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(clone, "linkdir")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		clonePath string
		rel       string
		want      bool
	}{
		{"file at the root", clone, ".gitlab-ci.yml", true},
		{"nested file", clone, ".gitlab/devdigest/a.yml", true},
		{"missing file", clone, "nope.yml", false},
		{"directory", clone, ".gitlab", false},
		{"symlinked file", clone, "link.yml", false},
		{"through a symlinked directory", clone, "linkdir/secret", false},
		{"dot dot", clone, "../secret", false},
		{"absolute", clone, outside, false},
		{"no clone yet", "", ".gitlab-ci.yml", false},
		{"clone directory missing", filepath.Join(clone, "gone"), ".gitlab-ci.yml", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := peers.NewCloneFiles().Exists(t.Context(), tc.clonePath, tc.rel)
			if err != nil || got != tc.want {
				t.Errorf("Exists(%q, %q) = %v, %v, want %v, nil", tc.clonePath, tc.rel, got, err, tc.want)
			}
		})
	}
}
