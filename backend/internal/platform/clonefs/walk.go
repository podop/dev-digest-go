package clonefs

import (
	"context"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"
)

// WalkOptions bounds and filters a Walk. Note that the zero MaxDepth lists the root only; pass a
// negative value for no depth limit.
type WalkOptions struct {
	// MaxDepth is the deepest directory level entered; files in the root have depth 0 and
	// files in a root subdirectory depth 1. A negative value means no limit.
	MaxDepth int
	// MaxFiles caps the number of entries returned (after Include). Zero or negative means no
	// limit.
	MaxFiles int
	// SkipDir reports a directory that must not be entered, from its base name and its
	// slash-separated path relative to the root. Nil enters every directory.
	SkipDir func(name, rel string) bool
	// Include reports a regular file to return, from its slash-separated path relative to the
	// root. Nil returns every regular file.
	Include func(rel string) bool
}

// Entry is one regular file found by Walk.
type Entry struct {
	// Path is relative to the root, slash-separated.
	Path string
	// Depth is the number of directories above the file: 0 for a root file.
	Depth int
	Size  int64
	// ModTime is the file's modification time at listing.
	ModTime time.Time
}

// Walk lists the regular files under root breadth-first, one directory level after the other,
// names in byte order inside a directory. It never follows or returns symlinks and never returns
// directories, devices, sockets or FIFOs; a directory that cannot be read is skipped. The
// second result is true when MaxFiles cut the listing. It returns ErrNotFound when root is not a
// readable directory, and ctx's error when ctx ends.
func Walk(ctx context.Context, root string, opts WalkOptions) ([]Entry, bool, error) {
	r, err := openRoot(root)
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	if st, err := r.Stat("."); err != nil || !st.IsDir() {
		return nil, false, ErrNotFound
	}

	var out []Entry
	level := []string{"."} // directories of the current depth, in discovery order
	for depth := 0; len(level) > 0 && (opts.MaxDepth < 0 || depth <= opts.MaxDepth); depth++ {
		var next []string
		for _, dir := range level {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			entries, ok := readDir(r, dir)
			if !ok {
				continue
			}
			for _, e := range entries {
				rel := e.Name()
				if dir != "." {
					rel = dir + "/" + rel
				}
				switch typ := e.Type(); {
				case typ&fs.ModeSymlink != 0:
					// never followed, never listed
				case typ.IsDir():
					if opts.SkipDir == nil || !opts.SkipDir(e.Name(), rel) {
						next = append(next, rel)
					}
				case typ.IsRegular():
					if opts.Include != nil && !opts.Include(rel) {
						continue
					}
					if opts.MaxFiles > 0 && len(out) >= opts.MaxFiles {
						return out, true, nil
					}
					info, err := e.Info()
					if err != nil || !info.Mode().IsRegular() {
						continue // vanished or swapped since the listing
					}
					out = append(out, Entry{Path: rel, Depth: depth, Size: info.Size(), ModTime: info.ModTime()})
				}
			}
		}
		level = next
	}
	return out, false, nil
}

// readDir lists a directory of the root sorted by name; ok is false when it cannot be read.
func readDir(r *os.Root, dir string) ([]fs.DirEntry, bool) {
	d, err := r.Open(dir)
	if err != nil {
		return nil, false
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	if err != nil {
		return nil, false
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, true
}
