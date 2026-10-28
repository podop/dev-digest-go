package importgraph

import (
	"context"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// MaxGoModBytes is the largest go.mod the GoResolver reads; a larger one contributes no module.
const MaxGoModBytes = 64 << 10

// maxGoModDepth is the deepest directory, in "/"-separated segments, in which NewGoResolver
// looks for a go.mod. Real module roots are shallow; without the bound one file a thousand
// directories deep cost a thousand probes, each O(depth) in the clone file system (S-M1: depth
// 500 took 46 s).
const maxGoModDepth = 64

// maxSpecBytes is the longest import specifier the Go and Java resolvers look up; a longer one
// is no import path and resolves to nothing. Each step of their prefix walk hashes the rest of
// the specifier, so a 400 KB one cost about 2 s (S-M3).
const maxSpecBytes = 1024

// GoResolver maps Go import paths to the walked .go files of the package they name (FR14,
// FR15). It holds only what NewGoResolver read and is safe for concurrent use.
type GoResolver struct {
	// modules maps a module path to the clone-relative directory of its go.mod.
	modules map[string]string
	// filesByDir holds the walked .go files that are not tests, per directory, in walk order.
	filesByDir map[string][]string
}

// NewGoResolver reads go.mod in every directory above a walked file, the clone root included
// (never elsewhere, so the reads are bounded by the walk; a nested module's directory may hold
// only subdirectories), and indexes the walked .go files that
// do not end in _test.go by directory. A go.mod that is missing, over MaxGoModBytes, without a
// module directive or more than 64 directories deep (maxGoModDepth: it is not read) contributes
// no module. When two go.mod files declare the same module path, the one in the directory first
// in byte order wins. The Tree has no size query, so a go.mod is read in full before the
// MaxGoModBytes check; the clone's Tree refuses files over the source size limit
// (domain.MaxFileBytes), so that is at most one 400 KiB read per probed directory. ctx is
// looked at before every read; its error is returned, with no resolver, when it ends.
func NewGoResolver(ctx context.Context, tree Tree, files []string) (*GoResolver, error) {
	r := &GoResolver{modules: map[string]string{}, filesByDir: map[string][]string{}}
	dirs := map[string]struct{}{".": {}}
	for _, f := range files {
		dir := path.Dir(f)
		if strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") {
			r.filesByDir[dir] = append(r.filesByDir[dir], f)
		}
		// Every directory above f within maxGoModDepth, up to the first one already seen (as
		// NewTree does).
		for d := shallowDir(dir); ; d = path.Dir(d) {
			if _, ok := dirs[d]; ok {
				break
			}
			dirs[d] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Strings(ordered)
	for _, dir := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := tree.ReadFile(path.Join(dir, "go.mod"))
		if err != nil || len(data) > MaxGoModBytes {
			continue
		}
		mod, ok := modulePath(data)
		if !ok {
			continue
		}
		if _, dup := r.modules[mod]; !dup {
			r.modules[mod] = dir
		}
	}
	return r, nil
}

// shallowDir returns dir cut to its first maxGoModDepth segments ("." stays ".").
func shallowDir(dir string) string {
	slashes := 0
	for i := 0; i < len(dir); i++ {
		if dir[i] == '/' {
			if slashes++; slashes == maxGoModDepth {
				return dir[:i]
			}
		}
	}
	return dir
}

// ResolveAll returns the walked non-test .go files directly in the package directory of the
// import path spec: the module with the longest path that equals spec or is a prefix of it
// followed by "/", joined with the rest of spec. It returns nil for an import of no in-repo
// module (standard library, external modules), for a directory without such files and for a
// spec over maxSpecBytes. The returned slice must not be modified.
func (r *GoResolver) ResolveAll(spec string) []string {
	if len(spec) > maxSpecBytes {
		return nil
	}
	// Walk spec's prefixes from the longest down: the first module found is the longest match,
	// and the lookups are bounded by the number of segments of spec.
	for mod := spec; mod != ""; {
		if dir, ok := r.modules[mod]; ok {
			rest := strings.TrimPrefix(spec[len(mod):], "/")
			if rest != "" && (rest == "." || !fs.ValidPath(rest)) {
				return nil // empty, "." or ".." segments: not an import path
			}
			files := r.filesByDir[path.Join(dir, rest)]
			if len(files) == 0 {
				return nil
			}
			return files[:len(files):len(files)]
		}
		i := strings.LastIndexByte(mod, '/')
		if i < 0 {
			break
		}
		mod = mod[:i]
	}
	return nil
}

// modulePath returns the path of the first module directive of a go.mod, bare or quoted
// (interpreted or raw string); ok is false when there is none or it has no path.
func modulePath(data []byte) (mod string, ok bool) {
	for line := range strings.Lines(string(data)) {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		rest, found := strings.CutPrefix(strings.TrimSpace(line), "module")
		if !found || (rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '"' && rest[0] != '`') {
			continue
		}
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "`") {
			unquoted, err := strconv.Unquote(rest)
			if err != nil {
				return "", false
			}
			rest = unquoted
		}
		if rest == "" || strings.ContainsAny(rest, " \t") {
			return "", false
		}
		return rest, true
	}
	return "", false
}
