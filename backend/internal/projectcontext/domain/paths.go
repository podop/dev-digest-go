// Package domain holds the pure rules of the project-context context: which
// repository paths are listable documents, the store-file path shape, the
// attachment checks, the glob matcher and the token estimates. It does no I/O.
package domain

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

// Limits and fixed names of the project-context contract.
const (
	// MaxDocBytes is the size above which a clone document is not read.
	MaxDocBytes = 262_144
	// MaxDocs is the number of clone documents a listing returns at most.
	MaxDocs = 500
	// MaxPaths is the number of paths one attachment set holds at most.
	MaxPaths = 50
	// PathMax is the longest document path in UTF-16 code units.
	PathMax = 512
	// StoreRoot is the prefix of every store file path.
	StoreRoot = ".devdigest/specs/"
	// StoreMaxFiles is the number of store files a repository holds at most.
	StoreMaxFiles = 500
	// StoreMaxDepth is the number of folder levels below StoreRoot.
	StoreMaxDepth = 5
	// DocExtension is the required (case-sensitive) extension of a document path.
	DocExtension = ".md"
)

// DocType classifies a document by its directory.
type DocType string

// Document types.
const (
	DocTypeSpecs    DocType = "specs"
	DocTypeDocs     DocType = "docs"
	DocTypeInsights DocType = "insights"
	// DefaultDocType applies when no specs/docs/insights directory is in the path.
	DefaultDocType = DocTypeDocs
)

// DefaultGlobs is the glob list used when PROJECT_CONTEXT_GLOBS is unset or empty.
var DefaultGlobs = []string{"**/{specs,docs,insights}/**/*.md"}

// excludedDirs are directory names no document path may contain.
var excludedDirs = []string{"node_modules", ".git", "dist", "build", "coverage", ".next", "out", "vendor"}

// Attachment validation errors (FR255); the transport maps them to the codes
// too_many_paths, invalid_path and duplicate_path.
var (
	ErrTooManyPaths  = errors.New("too many paths")
	ErrInvalidPath   = errors.New("a path is not a listable document path")
	ErrDuplicatePath = errors.New("a path is attached more than once")
)

// utf16Len is the length of s in UTF-16 code units (JavaScript's String.length).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// CheckPath is the shape rule of a repository document path: 1 to 512
// characters, no NUL, no backslash, no leading "/", ending in ".md", and no
// empty, "." or ".." segment. It is checked before any file system access.
func CheckPath(path string) bool {
	if path == "" || utf16Len(path) > PathMax {
		return false
	}
	if strings.ContainsRune(path, 0) || strings.Contains(path, `\`) || strings.HasPrefix(path, "/") {
		return false
	}
	if !strings.HasSuffix(path, DocExtension) {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// HasExcludedDir reports whether a directory segment of the path is an excluded directory.
func HasExcludedDir(path string) bool {
	segs := strings.Split(path, "/")
	return slices.ContainsFunc(segs[:len(segs)-1], func(d string) bool { return slices.Contains(excludedDirs, d) })
}

// ExcludedDir reports whether a directory name is excluded from the listing walk.
func ExcludedDir(name string) bool { return slices.Contains(excludedDirs, name) }

// IsListablePath reports whether the list would show the path: right shape, no
// excluded directory, and a match with one of the globs.
func IsListablePath(path string, globs *GlobSet) bool {
	return CheckPath(path) && !HasExcludedDir(path) && globs.Match(path)
}

// DocName is the file name of a document path.
func DocName(path string) string { return path[strings.LastIndexByte(path, '/')+1:] }

// DocTypeOf is the last directory segment equal to specs, docs or insights,
// else DefaultDocType.
func DocTypeOf(path string) DocType {
	segs := strings.Split(path, "/")
	for i := len(segs) - 2; i >= 0; i-- {
		switch DocType(segs[i]) {
		case DocTypeSpecs, DocTypeDocs, DocTypeInsights:
			return DocType(segs[i])
		}
	}
	return DefaultDocType
}

func isStoreSegmentChar(r rune) bool {
	return r == '.' || r == '_' || r == '-' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// CheckStorePath is the shape rule of a store file path: it starts with
// ".devdigest/specs/", ends in ".md", has at most 512 characters and at most
// 5 folder levels below the root, and every segment is non-empty, not "." or
// ".." and made of letters, digits, ".", "_" and "-". It never touches a file system.
func CheckStorePath(path string) bool {
	if utf16Len(path) > PathMax {
		return false
	}
	if !strings.HasPrefix(path, StoreRoot) || !strings.HasSuffix(path, DocExtension) {
		return false
	}
	segs := strings.Split(path[len(StoreRoot):], "/")
	if len(segs)-1 > StoreMaxDepth {
		return false
	}
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." || strings.IndexFunc(seg, func(r rune) bool { return !isStoreSegmentChar(r) }) >= 0 {
			return false
		}
	}
	return true
}

// WithSuffix turns "a/b.md" and 2 into "a/b-2.md". The caller re-checks the
// result with CheckStorePath.
func WithSuffix(path string, n int) string {
	return strings.TrimSuffix(path, DocExtension) + "-" + strconv.Itoa(n) + DocExtension
}

// HasNul reports whether the text holds a NUL character (never valid markdown).
func HasNul(text string) bool { return strings.ContainsRune(text, 0) }

// ValidateAttachmentPaths checks the path list of a PUT body: at most 50 paths
// (ErrTooManyPaths), then in order each path is listable or store-shaped
// (ErrInvalidPath) and unique (ErrDuplicatePath). Existence is not checked.
func ValidateAttachmentPaths(paths []string, globs *GlobSet) error {
	if len(paths) > MaxPaths {
		return ErrTooManyPaths
	}
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if !IsListablePath(p, globs) && !CheckStorePath(p) {
			return ErrInvalidPath
		}
		if _, dup := seen[p]; dup {
			return ErrDuplicatePath
		}
		seen[p] = struct{}{}
	}
	return nil
}
