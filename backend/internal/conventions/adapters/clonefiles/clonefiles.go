// Package clonefiles gives the conventions scan read-only access to a repository clone on
// disk. Clone content is untrusted; every access goes through platform/clonefs, which never
// follows a symlink and never leaves the clone root.
package clonefiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/platform/clonefs"
)

// Files reads clones. The zero value is ready to use.
type Files struct{}

// New returns a Files.
func New() Files { return Files{} }

// List returns the slash-separated paths of the regular files of the clone, found by a
// breadth-first walk of at most domain.WalkMaxDepth levels and domain.WalkMaxFiles files that
// skips symlinks, the build and vendor directories and every directory starting with a dot.
// A clone directory that cannot be read yields an error wrapping domain.ErrCloneMissing.
func (Files) List(ctx context.Context, root string) ([]string, error) {
	entries, _, err := clonefs.Walk(ctx, root, clonefs.WalkOptions{
		MaxDepth: domain.WalkMaxDepth,
		MaxFiles: domain.WalkMaxFiles,
		SkipDir:  func(name, _ string) bool { return domain.SkipDir(name) },
	})
	if errors.Is(err, clonefs.ErrNotFound) {
		return nil, fmt.Errorf("walk %s: %w", root, domain.ErrCloneMissing)
	}
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	return paths, nil
}

// Read returns the text of the regular file rel of the clone. ok is false when rel is not a
// safe repo-relative path or the file is missing, is a symlink (or sits behind one), is larger
// than domain.FileReadMaxBytes or contains a NUL byte. Invalid UTF-8 is replaced by U+FFFD.
func (Files) Read(root, rel string) (string, bool) {
	safe, ok := domain.SafeRelativePath(rel)
	if !ok {
		return "", false
	}
	f, err := clonefs.Read(root, safe, domain.FileReadMaxBytes)
	if err != nil || bytes.IndexByte(f.Data, 0) >= 0 {
		return "", false
	}
	return string([]rune(string(f.Data))), true
}
