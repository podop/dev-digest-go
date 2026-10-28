package importgraph

import (
	"path"
	"strings"
)

// staticPrefix marks a static import in the specifiers the Java analyzer reports.
const staticPrefix = "static:"

// JavaResolver maps Java import specifiers to the walked .java files of the package they name
// (FR16). It holds only what NewJavaResolver indexed and is safe for concurrent use.
type JavaResolver struct {
	// byPackage holds the files of each package (name without the "java:" key prefix).
	byPackage map[string][]string
	// byClass holds, per package, the files by class name (the file name without ".java").
	byClass map[string]map[string][]string
}

// NewJavaResolver indexes pkgFiles, the walked .java files by the package of their package
// declaration (not their path, which source roots and misplaced files make unreliable), in the
// order given. The input is read once and not retained.
func NewJavaResolver(pkgFiles map[string][]string) *JavaResolver {
	r := &JavaResolver{
		byPackage: make(map[string][]string, len(pkgFiles)),
		byClass:   make(map[string]map[string][]string, len(pkgFiles)),
	}
	for pkg, files := range pkgFiles {
		r.byPackage[pkg] = files
		classes := make(map[string][]string, len(files))
		for _, f := range files {
			name, ok := strings.CutSuffix(path.Base(f), ".java")
			if !ok || name == "" {
				continue
			}
			classes[name] = append(classes[name], f)
		}
		r.byClass[pkg] = classes
	}
	return r
}

// ResolveAll returns the walked files a specifier names: "a.b.C" the files named C.java in
// package a.b; when there are none, the last segment is dropped and the lookup repeated
// ("a.b.C.Inner" reaches C.java), until fewer than two segments remain. "a.b.*" is every file of
// package a.b. "static:a.b.C.m" and "static:a.b.C.*" resolve as the class a.b.C. It returns nil
// for the JDK, libraries, unknown classes and a spec over maxSpecBytes. Several files of one name
// (a class duplicated across source roots) are all returned. The returned slice must not be
// modified.
func (r *JavaResolver) ResolveAll(spec string) []string {
	if len(spec) > maxSpecBytes {
		return nil
	}
	spec, static := strings.CutPrefix(spec, staticPrefix)
	if !static {
		if pkg, ok := strings.CutSuffix(spec, ".*"); ok {
			return clip(r.byPackage[pkg], pkg != "")
		}
	} else {
		spec = strings.TrimSuffix(spec, ".*")
	}
	// Each step drops one trailing segment, so the lookups are bounded by the segments of spec.
	for {
		i := strings.LastIndexByte(spec, '.')
		if i < 0 {
			return nil
		}
		pkg, name := spec[:i], spec[i+1:]
		if files := r.byClass[pkg][name]; len(files) > 0 {
			return clip(files, true)
		}
		spec = pkg
	}
}

// clip returns files with its capacity limited to its length, or nil when !ok or it is empty.
func clip(files []string, ok bool) []string {
	if !ok || len(files) == 0 {
		return nil
	}
	return files[:len(files):len(files)]
}
