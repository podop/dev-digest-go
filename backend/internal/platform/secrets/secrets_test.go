package secrets_test

import (
	"encoding/json"
	"errors"
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

func customSecret() secrets.CustomSecret {
	return secrets.CustomSecret{
		Key:     "sk-custom",
		Headers: []secrets.CustomHeader{{Name: "X-Org", Value: "org-1"}, {Name: "X-Team", Value: "team-1"}},
	}
}

func TestStore_Custom_roundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	s := secrets.New(path, envOf(nil))
	want := customSecret()

	prev, err := s.SetCustom("ws1", "custom-gw", want)
	if err != nil {
		t.Fatalf("SetCustom error: %v", err)
	}
	if !prev.IsZero() {
		t.Errorf("SetCustom previous = %+v, want zero", prev)
	}
	if diff := cmp.Diff(want, s.Custom("ws1", "custom-gw")); diff != "" {
		t.Errorf("Custom(ws1, custom-gw) mismatch (-want +got):\n%s", diff)
	}
	// A second Store over the same file sees it (read per call, no cache).
	if diff := cmp.Diff(want, secrets.New(path, envOf(nil)).Custom("ws1", "custom-gw")); diff != "" {
		t.Errorf("Custom via a second store mismatch (-want +got):\n%s", diff)
	}
	if _, ok := readJSON(t, path)["custom_providers.ws1/custom-gw"]; !ok {
		t.Errorf("secrets file has no key custom_providers.ws1/custom-gw: %v", readJSON(t, path))
	}
}

func TestStore_Custom_replaceReturnsPrevious(t *testing.T) {
	t.Parallel()
	s := secrets.New(filepath.Join(t.TempDir(), secrets.FileName), envOf(nil))
	first := customSecret()
	if _, err := s.SetCustom("ws", "custom-a", first); err != nil {
		t.Fatal(err)
	}
	prev, err := s.SetCustom("ws", "custom-a", secrets.CustomSecret{Key: "sk-2"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first, prev); diff != "" {
		t.Errorf("SetCustom previous mismatch (-want +got):\n%s", diff)
	}
	if got := s.Custom("ws", "custom-a"); got.Key != "sk-2" || len(got.Headers) != 0 {
		t.Errorf("Custom after replace = %+v, want key sk-2 and no headers", got)
	}
}

func TestStore_Custom_delete(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	s := secrets.New(path, envOf(nil))
	if err := s.Set(secrets.OpenAIKey, "sk-builtin"); err != nil {
		t.Fatal(err)
	}
	want := customSecret()
	if _, err := s.SetCustom("ws", "custom-a", want); err != nil {
		t.Fatal(err)
	}
	prev, err := s.DeleteCustom("ws", "custom-a")
	if err != nil {
		t.Fatalf("DeleteCustom error: %v", err)
	}
	if diff := cmp.Diff(want, prev); diff != "" {
		t.Errorf("DeleteCustom previous mismatch (-want +got):\n%s", diff)
	}
	if got := s.Custom("ws", "custom-a"); !got.IsZero() {
		t.Errorf("Custom after delete = %+v, want zero", got)
	}
	if diff := cmp.Diff(map[string]any{"OPENAI_API_KEY": "sk-builtin"}, readJSON(t, path)); diff != "" {
		t.Errorf("secrets file after delete mismatch (-want +got):\n%s", diff)
	}
	// Deleting what is not there is not an error and returns zero.
	prev, err = s.DeleteCustom("ws", "custom-a")
	if err != nil || !prev.IsZero() {
		t.Errorf("DeleteCustom of a missing entry = (%+v, %v), want (zero, nil)", prev, err)
	}
}

func TestStore_Custom_restore(t *testing.T) {
	t.Parallel()
	s := secrets.New(filepath.Join(t.TempDir(), secrets.FileName), envOf(nil))
	orig := customSecret()
	if _, err := s.SetCustom("ws", "custom-a", orig); err != nil {
		t.Fatal(err)
	}

	// A write that must be undone: replace, then restore the previous value.
	prev, err := s.SetCustom("ws", "custom-a", secrets.CustomSecret{Key: "sk-new"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreCustom("ws", "custom-a", prev); err != nil {
		t.Fatalf("RestoreCustom error: %v", err)
	}
	if diff := cmp.Diff(orig, s.Custom("ws", "custom-a")); diff != "" {
		t.Errorf("Custom after restoring a replace mismatch (-want +got):\n%s", diff)
	}

	// A delete that must be undone.
	prev, err = s.DeleteCustom("ws", "custom-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreCustom("ws", "custom-a", prev); err != nil {
		t.Fatalf("RestoreCustom error: %v", err)
	}
	if diff := cmp.Diff(orig, s.Custom("ws", "custom-a")); diff != "" {
		t.Errorf("Custom after restoring a delete mismatch (-want +got):\n%s", diff)
	}

	// A create that must be undone: the previous value was zero, so restore removes it.
	prev, err = s.SetCustom("ws", "custom-b", secrets.CustomSecret{Key: "sk-b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreCustom("ws", "custom-b", prev); err != nil {
		t.Fatalf("RestoreCustom error: %v", err)
	}
	if got := s.Custom("ws", "custom-b"); !got.IsZero() {
		t.Errorf("Custom after restoring a create = %+v, want zero", got)
	}
}

func TestStore_Custom_isolation(t *testing.T) {
	t.Parallel()
	s := secrets.New(filepath.Join(t.TempDir(), secrets.FileName), envOf(nil))
	if _, err := s.SetCustom("ws1", "custom-a", secrets.CustomSecret{Key: "k-ws1-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCustom("ws2", "custom-a", secrets.CustomSecret{Key: "k-ws2-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCustom("ws1", "custom-b", secrets.CustomSecret{Key: "k-ws1-b"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ws, id, want string }{
		{"ws1", "custom-a", "k-ws1-a"},
		{"ws2", "custom-a", "k-ws2-a"},
		{"ws1", "custom-b", "k-ws1-b"},
		{"ws2", "custom-b", ""},
		{"ws3", "custom-a", ""},
	} {
		if got := s.Custom(tc.ws, tc.id).Key; got != tc.want {
			t.Errorf("Custom(%q, %q).Key = %q, want %q", tc.ws, tc.id, got, tc.want)
		}
	}
	if _, err := s.DeleteCustom("ws1", "custom-a"); err != nil {
		t.Fatal(err)
	}
	if got := s.Custom("ws2", "custom-a").Key; got != "k-ws2-a" {
		t.Errorf("Custom(ws2, custom-a).Key after deleting ws1's = %q, want k-ws2-a", got)
	}
}

func TestStore_Custom_noEnvFallback(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"custom_providers.ws/custom-a": "env-key",
		"CUSTOM_A_API_KEY":             "env-key",
		"OPENAI_API_KEY":               "env-openai",
		"GITHUB_TOKEN":                 "env-gh",
	}
	s := secrets.New(filepath.Join(t.TempDir(), secrets.FileName), envOf(env))
	if got := s.Custom("ws", "custom-a"); !got.IsZero() {
		t.Errorf("Custom with only environment values = %+v, want zero", got)
	}
	if got := s.CustomValues(); len(got) != 0 {
		t.Errorf("CustomValues() = %v, want none", got)
	}
}

func TestStore_Custom_builtinsUntouched(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	s := secrets.New(path, envOf(map[string]string{"GITHUB_PAT": "pat"}))
	if err := s.Set(secrets.OpenAIKey, "sk-openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCustom("ws", "custom-a", customSecret()); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(secrets.OpenAIKey); got != "sk-openai" {
		t.Errorf("Get(%q) after SetCustom = %q, want sk-openai", secrets.OpenAIKey, got)
	}
	if got := s.Get("custom_providers.ws/custom-a"); got != "" {
		t.Errorf("Get of a custom file key = %q, want empty (never a string value)", got)
	}
	if _, err := s.DeleteCustom("ws", "custom-a"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(secrets.OpenAIKey); got != "sk-openai" {
		t.Errorf("Get(%q) after DeleteCustom = %q, want sk-openai", secrets.OpenAIKey, got)
	}
	if got := s.Get(secrets.GitHubToken); got != "pat" {
		t.Errorf("Get(%q) = %q, want the GITHUB_PAT fallback", secrets.GitHubToken, got)
	}
}

func TestStore_Custom_fileModeAndNoLeftovers(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", secrets.FileName)
	s := secrets.New(path, envOf(nil))
	if _, err := s.SetCustom("ws", "custom-a", customSecret()); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("secrets file mode %s = %o, want 600", when, mode)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("secrets dir %s has %d entries, want only the secrets file", when, len(entries))
		}
	}
	check("after SetCustom")
	if _, err := s.DeleteCustom("ws", "custom-a"); err != nil {
		t.Fatal(err)
	}
	check("after DeleteCustom")

	// An existing loose file is tightened by a custom write too.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCustom("ws", "custom-a", customSecret()); err != nil {
		t.Fatal(err)
	}
	check("after a write over a 0644 file")
}

func TestStore_CustomValues(t *testing.T) {
	t.Parallel()
	s := secrets.New(filepath.Join(t.TempDir(), secrets.FileName), envOf(nil))
	if err := s.Set(secrets.OpenAIKey, "sk-builtin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCustom("ws1", "custom-a", secrets.CustomSecret{
		Key:     "k1",
		Headers: []secrets.CustomHeader{{Name: "X-A", Value: "h1"}, {Name: "X-B", Value: ""}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCustom("ws2", "custom-a", secrets.CustomSecret{Key: "k2", Headers: []secrets.CustomHeader{{Name: "X-A", Value: "h1"}}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"h1", "k1", "k2"} // sorted, deduplicated, no empty value, no built-in key
	if diff := cmp.Diff(want, s.CustomValues()); diff != "" {
		t.Errorf("CustomValues() mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_Custom_malformedEntryIsZero(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	writeFile(t, path, `{"custom_providers.ws/a":"plain","custom_providers.ws/b":{"key":5},"custom_providers.ws/c":["x"]}`)
	s := secrets.New(path, envOf(nil))
	for _, id := range []string{"a", "b", "c"} {
		if got := s.Custom("ws", id); !got.IsZero() {
			t.Errorf("Custom(ws, %q) on a malformed entry = %+v, want zero", id, got)
		}
	}
	if got := s.CustomValues(); len(got) != 0 {
		t.Errorf("CustomValues() on malformed entries = %v, want none", got)
	}
}

func TestStore_SetCustom_invalidRef(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	s := secrets.New(path, envOf(nil))
	for _, tc := range []struct{ name, ws, id string }{
		{"empty ws", "", "custom-a"},
		{"empty id", "ws", ""},
		{"slash in ws", "a/b", "c"},
		{"slash in id", "a", "b/c"},
	} {
		if _, err := s.SetCustom(tc.ws, tc.id, customSecret()); !errors.Is(err, secrets.ErrInvalidCustomRef) {
			t.Errorf("%s: SetCustom(%q, %q) error = %v, want ErrInvalidCustomRef", tc.name, tc.ws, tc.id, err)
		}
		if got := s.Custom(tc.ws, tc.id); !got.IsZero() {
			t.Errorf("%s: Custom(%q, %q) = %+v, want zero", tc.name, tc.ws, tc.id, got)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("secrets file exists after only invalid writes (stat error %v)", err)
	}
}

func TestStore_SetCustom_errorDoesNotLeakValue(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, blocker, "x")
	s := secrets.New(filepath.Join(blocker, "sub", secrets.FileName), envOf(nil))
	_, err := s.SetCustom("ws", "custom-a", secrets.CustomSecret{Key: "sk-super-secret", Headers: []secrets.CustomHeader{{Name: "X", Value: "hv-secret"}}})
	if err == nil {
		t.Fatal("SetCustom() error = nil, want an error")
	}
	if msg := err.Error(); strings.Contains(msg, "sk-super-secret") || strings.Contains(msg, "hv-secret") {
		t.Errorf("SetCustom() error leaks a secret value: %v", err)
	}
}

func TestStore_Custom_concurrentWithSet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), secrets.FileName)
	s := secrets.New(path, envOf(nil))
	errs := make(chan error, 6)
	go func() { errs <- s.Set(secrets.OpenAIKey, "sk") }()
	go func() { errs <- s.Set(secrets.GitHubToken, "gh") }()
	for _, id := range []string{"custom-a", "custom-b", "custom-c", "custom-d"} {
		go func() { _, err := s.SetCustom("ws", id, secrets.CustomSecret{Key: "k-" + id}); errs <- err }()
	}
	for range 6 {
		if err := <-errs; err != nil {
			t.Errorf("write error: %v", err)
		}
	}
	if got := readJSON(t, path); len(got) != 6 {
		t.Errorf("secrets file has %d keys, want 6: %v", len(got), got)
	}
}
