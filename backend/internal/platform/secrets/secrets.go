// Package secrets stores provider API keys and the GitHub token in a JSON file
// under the data directory, with the process environment as fallback.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"dev-digest-go/backend/internal/platform/keynames"
)

// Secret names, as used in the secrets file and as environment variables (defined in keynames).
const (
	OpenAIKey     = keynames.OpenAIKey
	AnthropicKey  = keynames.AnthropicKey
	OpenRouterKey = keynames.OpenRouterKey
	GitHubToken   = keynames.GitHubToken

	// githubTokenFallback is the legacy environment variable read when
	// GITHUB_TOKEN is unset.
	githubTokenFallback = "GITHUB_PAT"
)

// FileName is the secrets file inside the data directory.
const FileName = "secrets.json"

// Store reads secrets from a JSON object file, falling back to the environment.
// The file is read on every call, so a key saved through Set is visible to
// every later Get without a restart.
type Store struct {
	path   string
	getenv func(string) string
	mu     sync.Mutex // serializes Set's read-modify-write
}

// New returns a Store over the file at path. getenv is os.Getenv in production.
func New(path string, getenv func(string) string) *Store {
	return &Store{path: path, getenv: getenv}
}

// Path returns the secrets file location.
func (s *Store) Path() string { return s.path }

// Get returns the value for name, or "" when it is not configured. The file
// value wins over the environment; GITHUB_TOKEN falls back to GITHUB_PAT. An
// empty string counts as unset, and a missing, unreadable or malformed file
// counts as empty.
func (s *Store) Get(name string) string {
	if v, ok := s.readFile()[name].(string); ok && v != "" {
		return v
	}
	if v := s.getenv(name); v != "" {
		return v
	}
	if name == GitHubToken {
		return s.getenv(githubTokenFallback)
	}
	return ""
}

// Set stores value under name. It creates the directory if missing and replaces
// the file atomically with mode 0600. A malformed existing file is replaced.
// Errors never contain the value.
func (s *Store) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data := s.readFile()
	data[name] = value
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	return writeAtomic(dir, s.path, out)
}

// readFile returns the parsed file, or an empty map when it is missing,
// unreadable or not a JSON object.
func (s *Store) readFile() map[string]any {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return map[string]any{}
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return map[string]any{}
	}
	return data
}

func writeAtomic(dir, path string, content []byte) (err error) {
	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp") // created with mode 0600
	if err != nil {
		return fmt.Errorf("create temp secrets file: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmp.Name()))
		}
	}()
	if _, err = tmp.Write(content); err != nil {
		return errors.Join(fmt.Errorf("write secrets file: %w", err), tmp.Close())
	}
	if err = tmp.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("chmod secrets file: %w", err), tmp.Close())
	}
	if err = tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync secrets file: %w", err), tmp.Close())
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close secrets file: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace secrets file: %w", err)
	}
	return nil
}
