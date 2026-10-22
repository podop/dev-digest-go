package importgraph

import (
	"context"
	"path"

	"dev-digest-go/backend/internal/repointel/domain"
)

// SpecifierSource returns the import specifiers of one walked file in first-seen order. A file
// that cannot be read or parsed returns none.
type SpecifierSource func(ctx context.Context, file string) []string

// walkedTree is a Tree whose files are the walked source files and whose reads go to a SourceFS.
type walkedTree struct {
	fs     domain.SourceFS
	root   string
	walked map[string]struct{}
	// dirs holds every directory above a walked file ("." included).
	dirs map[string]struct{}
}

// ctxCheckEvery is the number of specifiers resolved between two looks at ctx in BuildEdges.
const ctxCheckEvery = 256

// NewTree returns the Tree of a clone: IsFile is true for the walked files (the only files an
// edge can end in, so a file that exists on disk but was not walked, such as a .mts file or one
// over the size limit, does not shadow the next candidate), ReadFile reads through fs, which
// confines reads to root and refuses symbolic links.
func NewTree(fs domain.SourceFS, root string, files []string) Tree {
	walked := make(map[string]struct{}, len(files))
	dirs := map[string]struct{}{}
	for _, f := range files {
		walked[f] = struct{}{}
		for d := path.Dir(f); ; d = path.Dir(d) {
			if _, ok := dirs[d]; ok {
				break
			}
			dirs[d] = struct{}{}
			if d == "." || d == "/" {
				break
			}
		}
	}
	return &walkedTree{fs: fs, root: root, walked: walked, dirs: dirs}
}

// HasFilesUnder reports whether a walked file lies somewhere below dir ("." is the clone root):
// package.json is read only in such directories, which bounds the Resolver's cache by the walk.
func (t *walkedTree) HasFilesUnder(dir string) bool {
	_, ok := t.dirs[dir]
	return ok
}

func (t *walkedTree) IsFile(rel string) bool {
	_, ok := t.walked[rel]
	return ok
}

func (t *walkedTree) ReadFile(rel string) ([]byte, error) { return t.fs.ReadFile(t.root, rel) }

// BuildEdges returns the import edges importer -> imported over files (FR3023): for each file in
// order and each of its specifiers in order, the resolved path when it is another walked file
// with a supported extension (step 5). Duplicates are dropped; edge paths are clone-relative. It
// returns ctx's error when ctx ends first.
func BuildEdges(ctx context.Context, files []string, r *Resolver, specs SpecifierSource) ([]domain.Edge, error) {
	walked := make(map[string]struct{}, len(files))
	for _, f := range files {
		walked[f] = struct{}{}
	}
	edges := []domain.Edge{}
	seen := make(map[domain.Edge]struct{})
	resolved := 0
	for _, from := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, spec := range specs(ctx, from) {
			if resolved++; resolved%ctxCheckEvery == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			to, ok := r.Resolve(from, spec)
			if !ok || to == from {
				continue
			}
			if _, inWalk := walked[to]; !inWalk {
				continue
			}
			if _, supported := domain.SyntaxForPath(to); !supported {
				continue
			}
			e := domain.Edge{From: from, To: to}
			if _, dup := seen[e]; dup {
				continue
			}
			seen[e] = struct{}{}
			edges = append(edges, e)
		}
	}
	return edges, nil
}

// Build loads the root tsconfig aliases and builds the edges of the walked files. aliasErr
// explains, in content-free words (categories and clone-relative config paths, see LoadAliases),
// why alias resolution is off or partial; the caller logs it, the edges are still built from relative imports. err is only ctx's error.
func Build(ctx context.Context, fs domain.SourceFS, root string, files []string, specs SpecifierSource) (edges []domain.Edge, aliasErr, err error) {
	tree := NewTree(fs, root, files)
	aliases, aliasErr := LoadAliases(ctx, tree)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	edges, err = BuildEdges(ctx, files, NewResolver(tree, aliases), specs)
	return edges, aliasErr, err
}
