package importgraph

import (
	"context"
	"path"
	"strings"

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

// ctxCheckEvery is the number of specifiers (or extra targets) resolved between two looks at
// ctx in collectEdges.
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

// BuildEdges returns the import edges importer -> imported over files with the TS/JS resolver
// only (FR3023): for each file in order and each of its specifiers in order, the resolved path
// when it is another walked file with a supported extension (step 5). Duplicates are dropped;
// edge paths are clone-relative. It returns ctx's error when ctx ends first.
func BuildEdges(ctx context.Context, files []string, r *Resolver, specs SpecifierSource) ([]domain.Edge, error) {
	imports, err := readImports(ctx, files, func(ctx context.Context, file string) domain.FileImports {
		return domain.FileImports{Specs: specs(ctx, file)}
	})
	if err != nil {
		return nil, err
	}
	return collectEdges(ctx, files, tsResolve(r), imports)
}

// javaKeyPrefix is the prefix of the package key of a Java file (see domain.JavaPackageKey).
const javaKeyPrefix = "java:"

// SourceImports returns the imports of one walked file: its package key and its specifiers in
// first-seen order. A file that cannot be read or parsed returns the zero value.
type SourceImports func(ctx context.Context, file string) domain.FileImports

// Build builds the import graph of the walked files (FR14, FR15, FR17): it loads the root
// tsconfig aliases, reads every file's imports once, and resolves each specifier with the
// resolver of the importing file's syntax (TS/JS: Resolver, Go: GoResolver, Java: JavaResolver). Graph.Packages has
// one row per file whose imports name a package, in walk order; both slices are non-nil.
// aliasErr explains, in content-free words (categories and clone-relative config paths, see
// LoadAliases), why alias resolution is off or partial; the caller logs it, the edges are still
// built from relative imports. err is only ctx's error.
func Build(ctx context.Context, fs domain.SourceFS, root string, files []string, imports SourceImports) (g domain.Graph, aliasErr, err error) {
	tree := NewTree(fs, root, files)
	aliases, aliasErr := LoadAliases(ctx, tree)
	if err := ctx.Err(); err != nil {
		return domain.Graph{}, nil, err
	}
	byFile, err := readImports(ctx, files, imports)
	if err != nil {
		return domain.Graph{}, nil, err
	}
	packages := []domain.FilePackage{}
	hasGo := false
	javaFiles := map[string][]string{}
	for _, f := range files {
		key := byFile[f].Package
		if key != "" {
			packages = append(packages, domain.FilePackage{Path: f, Key: key})
		}
		switch syntax, _ := domain.SyntaxForPath(f); syntax {
		case domain.SyntaxGo:
			hasGo = true
		case domain.SyntaxJava:
			// The package comes from the declaration, not the path (Review Focus 2).
			if pkg, ok := strings.CutPrefix(key, javaKeyPrefix); ok {
				javaFiles[pkg] = append(javaFiles[pkg], f)
			}
		}
	}
	java := NewJavaResolver(javaFiles)
	ts := tsResolve(NewResolver(tree, aliases))
	var golang *GoResolver
	if hasGo {
		golang = NewGoResolver(tree, files)
	}
	resolve := func(from, spec string) []string {
		syntax, ok := domain.SyntaxForPath(from)
		switch {
		case !ok:
			return nil
		case syntax == domain.SyntaxGo:
			return golang.ResolveAll(spec)
		case syntax == domain.SyntaxJava:
			return java.ResolveAll(spec)
		case syntax == domain.SyntaxTypeScript, syntax == domain.SyntaxTSX, syntax == domain.SyntaxJavaScript:
			return ts(from, spec)
		default:
			return nil // an unsupported syntax resolves to nothing
		}
	}
	edges, err := collectEdges(ctx, files, resolve, byFile)
	if err != nil {
		return domain.Graph{}, nil, err
	}
	return domain.Graph{Edges: edges, Packages: packages}, aliasErr, nil
}

// readImports reads the imports of every file once, looking at ctx before each file.
func readImports(ctx context.Context, files []string, src SourceImports) (map[string]domain.FileImports, error) {
	out := make(map[string]domain.FileImports, len(files))
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[f] = src(ctx, f)
	}
	return out, nil
}

// tsResolve adapts the TS/JS Resolver to the many-target form of collectEdges.
func tsResolve(r *Resolver) func(from, spec string) []string {
	return func(from, spec string) []string {
		to, ok := r.Resolve(from, spec)
		if !ok {
			return nil
		}
		return []string{to}
	}
}

// collectEdges resolves the specifiers of every file in order and keeps the targets that are
// another walked file with a supported extension; duplicates are dropped. ctx is looked at
// before each file and every ctxCheckEvery specifiers or targets.
func collectEdges(ctx context.Context, files []string, resolve func(from, spec string) []string, imports map[string]domain.FileImports) ([]domain.Edge, error) {
	walked := make(map[string]struct{}, len(files))
	for _, f := range files {
		walked[f] = struct{}{}
	}
	edges := []domain.Edge{}
	seen := make(map[domain.Edge]struct{})
	work := 0
	tick := func() error {
		if work++; work%ctxCheckEvery == 0 {
			return ctx.Err()
		}
		return nil
	}
	for _, from := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, spec := range imports[from].Specs {
			if err := tick(); err != nil {
				return nil, err
			}
			for i, to := range resolve(from, spec) {
				if i > 0 {
					if err := tick(); err != nil {
						return nil, err
					}
				}
				if to == from {
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
	}
	return edges, nil
}
