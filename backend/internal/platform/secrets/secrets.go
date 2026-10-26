// Package secrets stores provider API keys and the GitHub token in a JSON file
// under the data directory, with the process environment as fallback.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// customPrefix starts the file key of a custom provider's secrets:
// custom_providers.<workspace>/<id>. The value is an object, not a string, so
// Get never returns it.
const customPrefix = "custom_providers."

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
	_, err := s.update(name, func(map[string]any) any { return value })
	return err
}

// update replaces the file entry key with fn's result (nil deletes it) and
// returns the entry it replaced (nil when there was none). The read-modify-write
// is serialized and the file is replaced atomically.
func (s *Store) update(key string, fn func(data map[string]any) any) (prev any, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data := s.readFile()
	prev = data[key]
	if v := fn(data); v == nil {
		delete(data, key)
	} else {
		data[key] = v
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode secrets: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create secrets dir: %w", err)
	}
	if err := writeAtomic(dir, s.path, out); err != nil {
		return nil, err
	}
	return prev, nil
}

// CustomHeader is one extra request header of a custom provider; Value is the
// secret.
type CustomHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CustomSecret is everything secret about one custom provider: its API key and
// its extra-header values. The zero value means "nothing stored".
type CustomSecret struct {
	Key     string         `json:"key,omitempty"`
	Headers []CustomHeader `json:"headers,omitempty"`
}

// IsZero reports whether nothing is stored: no key and no header.
func (c CustomSecret) IsZero() bool { return c.Key == "" && len(c.Headers) == 0 }

// ErrInvalidCustomRef is returned for an empty workspace or provider id, or one
// holding "/" (the separator of the file key).
var ErrInvalidCustomRef = errors.New("invalid custom provider secret reference")

func customKey(ws, id string) (string, bool) {
	if ws == "" || id == "" || strings.Contains(ws, "/") || strings.Contains(id, "/") {
		return "", false
	}
	return customPrefix + ws + "/" + id, true
}

// Custom returns the stored secrets of custom provider id in workspace ws, or
// the zero CustomSecret when none are stored, the reference is invalid or the
// entry is malformed. There is no environment fallback (spec D22).
func (s *Store) Custom(ws, id string) CustomSecret {
	key, ok := customKey(ws, id)
	if !ok {
		return CustomSecret{}
	}
	return decodeCustom(s.readFile()[key])
}

// SetCustom stores sec for custom provider id in workspace ws, replacing what
// was stored, and returns the previous value (zero when none) so a caller whose
// database commit fails can put it back with RestoreCustom. A zero sec removes
// the entry. Built-in names are untouched. Errors never contain a value.
func (s *Store) SetCustom(ws, id string, sec CustomSecret) (prev CustomSecret, err error) {
	key, ok := customKey(ws, id)
	if !ok {
		return CustomSecret{}, ErrInvalidCustomRef
	}
	old, err := s.update(key, func(map[string]any) any {
		if sec.IsZero() {
			return nil
		}
		return sec
	})
	if err != nil {
		return CustomSecret{}, err
	}
	return decodeCustom(old), nil
}

// DeleteCustom removes the secrets of custom provider id in workspace ws and
// returns what was stored (zero when none), for RestoreCustom.
func (s *Store) DeleteCustom(ws, id string) (prev CustomSecret, err error) {
	return s.SetCustom(ws, id, CustomSecret{})
}

// RestoreCustom puts prev, as returned by SetCustom or DeleteCustom, back; a
// zero prev removes the entry again. It undoes a write whose database
// transaction did not commit.
func (s *Store) RestoreCustom(ws, id string, prev CustomSecret) error {
	_, err := s.SetCustom(ws, id, prev)
	return err
}

// CustomValues returns every non-empty custom-provider secret value (keys and
// header values) of every workspace, sorted and without duplicates, for
// redaction of logs, errors and responses.
func (s *Store) CustomValues() []string {
	var out []string
	for name, raw := range s.readFile() {
		if !strings.HasPrefix(name, customPrefix) {
			continue
		}
		sec := decodeCustom(raw)
		if sec.Key != "" {
			out = append(out, sec.Key)
		}
		for _, h := range sec.Headers {
			if h.Value != "" {
				out = append(out, h.Value)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// decodeCustom reads a file entry (decoded JSON or a CustomSecret); anything
// else is the zero value.
func decodeCustom(raw any) CustomSecret {
	if raw == nil {
		return CustomSecret{}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return CustomSecret{}
	}
	var sec CustomSecret
	if err := json.Unmarshal(b, &sec); err != nil {
		return CustomSecret{}
	}
	return sec
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
