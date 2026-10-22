// Package clonetree is the repo-intel view of a clone on disk: the source file walk of FR3013 and
// confined reads. Clone content is untrusted, so everything goes through platform/clonefs, which
// never follows a symbolic link, stays inside the clone root and only reads regular files; this
// package adds the walk rules and a read cap. It never writes and never runs anything.
package clonetree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"dev-digest-go/backend/internal/platform/clonefs"
	"dev-digest-go/backend/internal/repointel/domain"
)

// MaxReadBytes is the largest file ReadFile returns. Source files above it are not indexed in
// the first place (domain.MaxFileBytes); the same cap protects the reads of configuration files.
const MaxReadBytes = domain.MaxFileBytes

// FS reads clones from the local disk. The zero value is ready to use and safe for concurrent
// use.
type FS struct{}

var _ domain.SourceFS = FS{}

// New returns an FS.
func New() FS { return FS{} }

// Walk lists the source files under root as FR3013 defines them: excluded directories are not
// entered, symbolic links are neither followed nor listed, unreadable directories are skipped,
// and the domain rules pick the files (supported extension, size limit, byte order, bound).
// The listing itself is not capped (P19); the bound applies after the sort.
func (FS) Walk(ctx context.Context, root string) (domain.WalkResult, error) {
	entries, _, err := clonefs.Walk(ctx, root, clonefs.WalkOptions{
		MaxDepth: -1,
		SkipDir:  func(name, _ string) bool { return domain.IsExcludedDir(name) },
		Include:  domain.IsSupported,
	})
	if err != nil {
		if errors.Is(err, clonefs.ErrNotFound) {
			return domain.WalkResult{}, fmt.Errorf("walk clone: %w", domain.ErrCloneMissing)
		}
		return domain.WalkResult{}, fmt.Errorf("walk clone: %w", err)
	}
	files := make([]domain.FileEntry, len(entries))
	for i, e := range entries {
		files[i] = domain.FileEntry{Path: e.Path, Size: e.Size}
	}
	return domain.SelectFiles(files), nil
}

// ReadFile returns the bytes of the regular file rel (slash-separated, relative to root). It
// fails when rel is not a clean relative path, is missing, is a symbolic link or lies behind
// one, is not a regular file, or is larger than MaxReadBytes. The errors wrap clonefs.ErrNotFound
// (which here also satisfies errors.Is(err, fs.ErrNotExist), the contract of domain.SourceFS) or
// clonefs.ErrTooLarge and carry neither the root nor the file's content.
func (FS) ReadFile(root, rel string) ([]byte, error) {
	f, err := clonefs.Read(root, rel, MaxReadBytes)
	if errors.Is(err, clonefs.ErrNotFound) {
		return nil, fmt.Errorf("read clone file: %w: %w", err, fs.ErrNotExist)
	}
	if err != nil {
		return nil, fmt.Errorf("read clone file: %w", err)
	}
	return f.Data, nil
}
