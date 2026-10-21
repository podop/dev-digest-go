package secrets_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/secrets"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestStore_Get(t *testing.T) {
	tests := []struct {
		name string
		file string // "" = no file
		env  map[string]string
		key  string
		want string
	}{
		{name: "env only", env: map[string]string{"OPENAI_API_KEY": "sk-a"}, key: secrets.OpenAIKey, want: "sk-a"},
		{name: "unset", key: secrets.AnthropicKey, want: ""},
		{name: "empty env is unset", env: map[string]string{"ANTHROPIC_API_KEY": ""}, key: secrets.AnthropicKey, want: ""},
		{name: "github pat fallback", env: map[string]string{"GITHUB_PAT": "ghp-b"}, key: secrets.GitHubToken, want: "ghp-b"},
		{name: "github token beats pat", env: map[string]string{"GITHUB_TOKEN": "t", "GITHUB_PAT": "p"}, key: secrets.GitHubToken, want: "t"},
		{name: "pat only for github", env: map[string]string{"GITHUB_PAT": "p"}, key: secrets.OpenAIKey, want: ""},
		{
			name: "file wins over env",
			file: `{"GITHUB_TOKEN":"file-tok"}`,
			env:  map[string]string{"GITHUB_TOKEN": "env-tok"},
			key:  secrets.GitHubToken,
			want: "file-tok",
		},
		{
			name: "empty file value falls back to env",
			file: `{"GITHUB_TOKEN":""}`,
			env:  map[string]string{"GITHUB_TOKEN": "env-tok"},
			key:  secrets.GitHubToken,
			want: "env-tok",
		},
		{
			name: "file value beats pat",
			file: `{"GITHUB_TOKEN":"file-tok"}`,
			env:  map[string]string{"GITHUB_PAT": "pat"},
			key:  secrets.GitHubToken,
			want: "file-tok",
		},
		{name: "malformed file is empty", file: `{not json`, env: map[string]string{"OPENAI_API_KEY": "e"}, key: secrets.OpenAIKey, want: "e"},
		{name: "non-object file is empty", file: `["x"]`, key: secrets.OpenAIKey, want: ""},
		{name: "null file is empty", file: `null`, key: secrets.OpenAIKey, want: ""},
		{name: "non-string value is ignored", file: `{"OPENAI_API_KEY":5}`, key: secrets.OpenAIKey, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), secrets.FileName)
			if tc.file != "" {
				writeFile(t, path, tc.file)
			}
			s := secrets.New(path, envOf(tc.env))
			if got := s.Get(tc.key); got != tc.want {
				t.Errorf("Get(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestStore_Get_unreadableFileCountsAsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // a directory where a file is expected: ReadFile fails
	s := secrets.New(dir, envOf(map[string]string{"OPENAI_API_KEY": "env"}))
	if got := s.Get(secrets.OpenAIKey); got != "env" {
		t.Errorf("Get(%q) = %q, want %q", secrets.OpenAIKey, got, "env")
	}
}

func TestStore_Get_readsPerCall(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	s := secrets.New(path, envOf(nil))
	if got := s.Get(secrets.GitHubToken); got != "" {
		t.Fatalf("Get before write = %q, want empty", got)
	}
	writeFile(t, path, `{"GITHUB_TOKEN":"one"}`)
	if got := s.Get(secrets.GitHubToken); got != "one" {
		t.Errorf("Get after external write = %q, want %q", got, "one")
	}
	writeFile(t, path, `{"GITHUB_TOKEN":"two"}`)
	if got := s.Get(secrets.GitHubToken); got != "two" {
		t.Errorf("Get after rewrite = %q, want %q", got, "two")
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("secrets file is not valid JSON: %v\n%s", err, raw)
	}
	return got
}

func TestStore_Set(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "dir", secrets.FileName)
	s := secrets.New(path, envOf(nil))

	if err := s.Set(secrets.GitHubToken, "ghp_test"); err != nil {
		t.Fatalf("Set(%q) error: %v", secrets.GitHubToken, err)
	}
	if err := s.Set(secrets.OpenAIKey, "sk-1"); err != nil {
		t.Fatalf("Set(%q) error: %v", secrets.OpenAIKey, err)
	}

	want := map[string]any{"GITHUB_TOKEN": "ghp_test", "OPENAI_API_KEY": "sk-1"}
	if diff := cmp.Diff(want, readJSON(t, path)); diff != "" {
		t.Errorf("secrets file mismatch (-want +got):\n%s", diff)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("secrets file mode = %o, want 600", mode)
	}
	if got := s.Get(secrets.GitHubToken); got != "ghp_test" {
		t.Errorf("Get after Set = %q, want %q", got, "ghp_test")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("secrets dir has %d entries, want only the secrets file (no temp leftovers)", len(entries))
	}
}

func TestStore_Set_replacesMalformedFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	writeFile(t, path, `{not json`)
	s := secrets.New(path, envOf(nil))

	if got := s.Get(secrets.OpenAIKey); got != "" {
		t.Fatalf("Get on malformed file = %q, want empty", got)
	}
	if err := s.Set(secrets.OpenAIKey, "sk-new"); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	want := map[string]any{"OPENAI_API_KEY": "sk-new"}
	if diff := cmp.Diff(want, readJSON(t, path)); diff != "" {
		t.Errorf("secrets file mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_Set_tightensExistingFileMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := secrets.New(path, envOf(nil))
	if err := s.Set(secrets.OpenAIKey, "sk"); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("secrets file mode after Set = %o, want 600", mode)
	}
}

func TestStore_Set_errorDoesNotLeakValue(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, blocker, "x")
	s := secrets.New(filepath.Join(blocker, "sub", secrets.FileName), envOf(nil)) // dir cannot be created
	err := s.Set(secrets.OpenAIKey, "sk-super-secret")
	if err == nil {
		t.Fatal("Set() error = nil, want an error")
	}
	if strings.Contains(err.Error(), "sk-super-secret") {
		t.Errorf("Set() error leaks the secret value: %v", err)
	}
}

func TestStore_Set_concurrent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	s := secrets.New(path, envOf(nil))
	names := []string{secrets.OpenAIKey, secrets.AnthropicKey, secrets.OpenRouterKey, secrets.GitHubToken}
	errs := make(chan error, len(names))
	for _, n := range names {
		go func() { errs <- s.Set(n, "v-"+n) }()
	}
	for range names {
		if err := <-errs; err != nil {
			t.Errorf("Set error: %v", err)
		}
	}
	got := readJSON(t, path)
	if len(got) != len(names) {
		t.Errorf("secrets file has %d keys, want %d: %v", len(got), len(names), got)
	}
}
