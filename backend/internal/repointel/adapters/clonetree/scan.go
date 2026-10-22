package clonetree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"unicode/utf8"

	"dev-digest-go/backend/internal/platform/clonefs"
	"dev-digest-go/backend/internal/repointel/domain"
)

// ScanCallers lists the files the CallerSignatures scan reads (spec section 7.11, D4004): the
// regular files of the clone with one of the extensions .ts .tsx .js .jsx .mjs .cjs and a size
// below domain.MaxCallerFileBytes, outside the directories .git, node_modules, dist, build,
// .next and coverage, in path byte order. Symbolic links are neither followed nor listed (a
// symlinked directory is not entered), unreadable directories are skipped, a path that is not
// valid UTF-8 or holds U+0000 is left out, and the listing stops at domain.MaxCallerScanFiles
// files (breadth first). The walk is its own: it differs from Walk in the excluded directories
// and the size limit. It returns an error wrapping domain.ErrCloneMissing when root is not a
// readable directory, and ctx's error when ctx ends.
func (FS) ScanCallers(ctx context.Context, root string) ([]string, error) {
	entries, _, err := clonefs.Walk(ctx, root, clonefs.WalkOptions{
		MaxDepth: -1,
		MaxFiles: domain.MaxCallerScanFiles,
		SkipDir:  func(name, _ string) bool { return domain.IsCallerScanDir(name) },
		Include:  domain.IsCallerScanFile,
	})
	if err != nil {
		if errors.Is(err, clonefs.ErrNotFound) {
			return nil, fmt.Errorf("scan clone: %w", domain.ErrCloneMissing)
		}
		return nil, fmt.Errorf("scan clone: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Size >= domain.MaxCallerFileBytes || !utf8.ValidString(e.Path) || strings.IndexByte(e.Path, 0) >= 0 {
			continue
		}
		files = append(files, e.Path)
	}
	slices.Sort(files)
	return files, nil
}

// ReadCallerFile returns the bytes of the regular file rel as ReadFile does, with the larger
// read cap of the caller scan (files below domain.MaxCallerFileBytes).
func (FS) ReadCallerFile(root, rel string) ([]byte, error) {
	f, err := clonefs.Read(root, rel, domain.MaxCallerFileBytes-1)
	if errors.Is(err, clonefs.ErrNotFound) {
		return nil, fmt.Errorf("read clone file: %w: %w", err, fs.ErrNotExist)
	}
	if err != nil {
		return nil, fmt.Errorf("read clone file: %w", err)
	}
	return f.Data, nil
}
