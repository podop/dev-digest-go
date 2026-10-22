// Package importgraph resolves the import specifiers of a clone's source files to the files they
// name and builds the file-level import graph (spec section 7.5). It is pure: the clone is
// reached through the Tree interface and nothing is executed or written.
package importgraph

import (
	"encoding/json"
	"path"
	"strings"
	"sync"
)

// resolveExtensions are the extensions tried after a path, in Node's (enhanced-resolve) order.
var resolveExtensions = [...]string{".js", ".cjs", ".mjs", ".jsx", ".ts", ".tsx", ".d.ts", ".cts", ".d.cts", ".mts", ".d.mts"}

// scriptExtensions are the specifier extensions that may stand for a TypeScript source file.
var scriptExtensions = [...]string{".js", ".jsx", ".mjs", ".cjs"}

// maxPackageHops bounds package.json `main` chains (a main that names a directory with its own
// package.json).
const maxPackageHops = 3

// Tree is what resolution needs from the clone. Implementations never follow symbolic links.
type Tree interface {
	// IsFile reports whether rel (slash-separated, relative to the clone root) is a regular file
	// that resolution may land on.
	IsFile(rel string) bool
	// ReadFile returns the bytes of rel (tsconfig.json, package.json). A missing file yields an
	// error for which errors.Is(err, fs.ErrNotExist) holds.
	ReadFile(rel string) ([]byte, error)
}

// DirLister is implemented by a Tree that knows which directories hold walked files. The
// Resolver then reads package.json only there, so its cache cannot outgrow the walk.
type DirLister interface {
	// HasFilesUnder reports whether a file that resolution may land on lies below dir.
	HasFilesUnder(dir string) bool
}

// maxPackageCache bounds the package.json cache of a Tree that is not a DirLister; past it a
// directory is treated as having no package.json.
const maxPackageCache = 1 << 16

// Resolver maps import specifiers to clone files. It is safe for concurrent use.
type Resolver struct {
	tree    Tree
	aliases *Aliases

	mu    sync.Mutex
	mains map[string]string // package.json `main` per directory, "" when none
}

// NewResolver returns a Resolver over tree. aliases may be nil (no tsconfig paths).
func NewResolver(tree Tree, aliases *Aliases) *Resolver {
	return &Resolver{tree: tree, aliases: aliases, mains: map[string]string{}}
}

// Resolve resolves spec, imported by the file importer (clone-relative), to a clone-relative file
// path under the rules of spec section 7.5 steps 1-4. ok is false for builtins, bare packages
// that no alias maps, and anything that does not resolve. The result is not checked against the
// walked files (step 5): see BuildEdges.
func (r *Resolver) Resolve(importer, spec string) (resolved string, ok bool) {
	if i := strings.IndexByte(spec, '?'); i >= 0 {
		spec = spec[:i]
	}
	if spec == "" || IsBuiltin(spec) {
		return "", false
	}
	if isRelativeSpec(spec) {
		base := path.Join(path.Dir(importer), spec)
		if escapes(base) {
			return "", false
		}
		return r.attempt(base, directoryOnly(spec))
	}
	for _, cand := range r.aliases.Candidates(spec) {
		if got, ok := r.attempt(cand, directoryOnly(cand)); ok {
			return got, true
		}
	}
	return "", false
}

// directoryOnly reports whether p names a directory by its spelling ("." or a trailing slash or
// dot segment), so that no file named like it is tried.
func directoryOnly(p string) bool {
	return p == "." || p == ".." || strings.HasSuffix(p, "/") || strings.HasSuffix(p, "/.") || strings.HasSuffix(p, "/..")
}

func escapes(p string) bool { return p == ".." || strings.HasPrefix(p, "../") }

// attempt is step 2 for base, then step 3 (the .js -> .ts retry) when step 2 finds nothing.
func (r *Resolver) attempt(base string, dirOnly bool) (string, bool) {
	if got, ok := r.resolveBase(base, dirOnly); ok {
		return got, true
	}
	if dirOnly {
		return "", false
	}
	for _, ext := range scriptExtensions {
		stripped, found := strings.CutSuffix(base, ext)
		if !found || stripped == "" {
			continue
		}
		if got, ok := r.resolveBase(stripped, false); ok && isTypeScriptFile(got) {
			return got, true
		}
		return "", false
	}
	return "", false
}

func isTypeScriptFile(p string) bool {
	for _, ext := range [...]string{".ts", ".tsx", ".cts", ".mts"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

// resolveBase is step 2: base as a file, base plus an extension, then base as a directory.
func (r *Resolver) resolveBase(base string, dirOnly bool) (string, bool) {
	if !dirOnly {
		if got, ok := r.resolveFile(base); ok {
			return got, true
		}
	}
	return r.resolveDir(base, 0)
}

func (r *Resolver) resolveFile(base string) (string, bool) {
	if r.tree.IsFile(base) {
		return base, true
	}
	for _, ext := range resolveExtensions {
		if r.tree.IsFile(base + ext) {
			return base + ext, true
		}
	}
	return "", false
}

// resolveDir resolves dir through its package.json `main` (as a file, then as a directory), then
// through dir/index plus an extension.
func (r *Resolver) resolveDir(dir string, hops int) (string, bool) {
	if main := r.packageMain(dir); main != "" && hops < maxPackageHops {
		target := path.Join(dir, main)
		if !escapes(target) {
			if !directoryOnly(main) {
				if got, ok := r.resolveFile(target); ok {
					return got, true
				}
			}
			if got, ok := r.resolveDir(target, hops+1); ok {
				return got, true
			}
		}
	}
	for _, ext := range resolveExtensions {
		if p := path.Join(dir, "index"+ext); r.tree.IsFile(p) {
			return p, true
		}
	}
	return "", false
}

// packageMain returns the `main` string of dir/package.json, or "" when it has none or the file
// is missing or malformed.
func (r *Resolver) packageMain(dir string) string {
	r.mu.Lock()
	main, cached := r.mains[dir]
	full := len(r.mains) >= maxPackageCache
	r.mu.Unlock()
	if cached {
		return main
	}
	if full {
		return ""
	}
	if dl, ok := r.tree.(DirLister); ok && !dl.HasFilesUnder(dir) {
		return ""
	}
	if data, err := r.tree.ReadFile(path.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Main any `json:"main"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			main, _ = pkg.Main.(string)
		}
	}
	r.mu.Lock()
	r.mains[dir] = main
	r.mu.Unlock()
	return main
}
