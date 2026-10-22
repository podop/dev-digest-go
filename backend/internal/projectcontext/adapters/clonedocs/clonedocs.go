// Package clonedocs reads the documents of a local repository clone for the project-context
// use cases. It is read-only and confined to the clone root by internal/platform/clonefs: a
// symlink anywhere on a path is "not found", nothing outside the root is read and nothing is
// ever written (NFR205).
package clonedocs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"dev-digest-go/backend/internal/platform/clonefs"
	"dev-digest-go/backend/internal/projectcontext/app"
	"dev-digest-go/backend/internal/projectcontext/domain"
)

// Docs reads clone documents. The zero value is ready to use.
type Docs struct{}

// List walks the clone (never entering excluded directories, never listing symlinks), keeps the
// files the globs accept, orders them by path in byte order and describes the first limit of
// them. Matches that cannot be read any more are dropped after the cut. ok is false when root
// is not a readable directory.
func (Docs) List(ctx context.Context, root string, globs *domain.GlobSet, limit int) (app.ClonedList, bool, error) {
	entries, _, err := clonefs.Walk(ctx, root, clonefs.WalkOptions{
		MaxDepth: -1,
		SkipDir:  func(name, _ string) bool { return domain.ExcludedDir(name) },
		Include:  func(rel string) bool { return domain.IsListablePath(rel, globs) },
	})
	if err != nil {
		if errors.Is(err, clonefs.ErrNotFound) {
			return app.ClonedList{}, false, nil
		}
		return app.ClonedList{}, false, fmt.Errorf("walk clone: %w", err)
	}
	slices.SortFunc(entries, func(a, b clonefs.Entry) int { return strings.Compare(a.Path, b.Path) })
	list := app.ClonedList{Truncated: len(entries) > limit}
	if list.Truncated {
		entries = entries[:limit]
	}
	list.Docs = make([]app.ClonedDoc, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return app.ClonedList{}, false, err
		}
		file, err := clonefs.Read(root, e.Path, domain.MaxDocBytes)
		switch {
		case err == nil:
			list.Docs = append(list.Docs, app.ClonedDoc{
				Path: e.Path, SizeBytes: file.Size, Tokens: domain.CloneTextTokens(file.Data), UpdatedAt: file.ModTime,
			})
		case errors.Is(err, clonefs.ErrTooLarge):
			list.Docs = append(list.Docs, app.ClonedDoc{
				Path: e.Path, SizeBytes: file.Size, Tokens: domain.CloneSizeTokens(file.Size), UpdatedAt: file.ModTime,
			})
		}
	}
	return list, true, nil
}

// Read reads one document of the clone. The caller has applied the path-shape and glob gates.
func (Docs) Read(root, path string) app.ClonedRead {
	file, err := clonefs.Read(root, path, domain.MaxDocBytes)
	switch {
	case err == nil:
		return app.ClonedRead{
			Status: app.ClonedOK, Content: domain.DecodeUTF8(file.Data), SizeBytes: file.Size,
			Tokens: domain.CloneTextTokens(file.Data),
		}
	case errors.Is(err, clonefs.ErrTooLarge):
		return app.ClonedRead{Status: app.ClonedTooLarge, SizeBytes: file.Size}
	default:
		return app.ClonedRead{Status: app.ClonedNotFound}
	}
}

// Exists reports whether a regular file is at path inside the clone, reached without a symlink.
func (Docs) Exists(root, path string) bool {
	_, err := clonefs.Stat(root, path)
	return err == nil
}
