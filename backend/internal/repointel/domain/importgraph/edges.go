package importgraph

import (
	"context"

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
}

// NewTree returns the Tree of a clone: IsFile is true for the walked files (the only files an
// edge can end in, so a file that exists on disk but was not walked, such as a .mts file or one
// over the size limit, does not shadow the next candidate), ReadFile reads through fs, which
// confines reads to root and refuses symbolic links.
func NewTree(fs domain.SourceFS, root string, files []string) Tree {
	walked := make(map[string]struct{}, len(files))
	for _, f := range files {
		walked[f] = struct{}{}
	}
	return &walkedTree{fs: fs, root: root, walked: walked}
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
	for _, from := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, spec := range specs(ctx, from) {
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
// explains why alias resolution is off (a malformed or unreadable tsconfig or extends chain);
// the caller logs it, the edges are still built from relative imports. err is only ctx's error.
func Build(ctx context.Context, fs domain.SourceFS, root string, files []string, specs SpecifierSource) (edges []domain.Edge, aliasErr, err error) {
	tree := NewTree(fs, root, files)
	aliases, aliasErr := LoadAliases(tree)
	edges, err = BuildEdges(ctx, files, NewResolver(tree, aliases), specs)
	return edges, aliasErr, err
}
