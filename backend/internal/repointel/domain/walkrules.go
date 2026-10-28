package domain

import (
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// IsExcludedDir reports a directory name the walk never enters, at any depth (FR3013; target is
// Maven's output, FR2).
func IsExcludedDir(name string) bool {
	switch name {
	case "node_modules", "dist", "build", "coverage", ".next", "out", "vendor", ".git", "target":
		return true
	}
	return false
}

// IsSupported reports whether p has one of the supported extensions, compared
// case-insensitively. Like Node's path.extname, a name whose only dot is the first character
// (".ts") has no extension.
func IsSupported(p string) bool {
	if strings.LastIndexByte(path.Base(p), '.') == 0 {
		return false
	}
	_, ok := SyntaxForPath(p)
	return ok
}

// FileEntry is a regular file found by a walk: its slash-separated path relative to the clone
// root and its size in bytes.
type FileEntry struct {
	Path string
	Size int64
}

// SelectFiles applies the walk rules of FR3013 to the regular files of a clone and returns the
// source files to index. A file qualifies when its extension is supported and none of its
// directories is excluded; each qualifying file counts in TotalCandidates. One larger than
// MaxFileBytes, or whose path is not valid UTF-8 (Postgres cannot store it; Node reads such a
// name as U+FFFD and cannot open it), counts in SkippedTooLarge and is left out. The paths are
// sorted in byte order and only the first MaxIndexedFiles are kept, Bounded counting the rest.
// The input is not modified.
func SelectFiles(entries []FileEntry) WalkResult {
	var res WalkResult
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !IsSupported(e.Path) || isGeneratedGoPath(e.Path) || inExcludedDir(e.Path) {
			continue
		}
		res.Stats.TotalCandidates++
		if e.Size > MaxFileBytes || !storablePath(e.Path) {
			res.Stats.SkippedTooLarge++
			continue
		}
		files = append(files, e.Path)
	}
	slices.Sort(files)
	if len(files) > MaxIndexedFiles {
		res.Stats.Bounded = len(files) - MaxIndexedFiles
		files = files[:MaxIndexedFiles]
	}
	res.Files = files
	return res
}

// storablePath reports whether p can be stored: valid UTF-8 without U+0000.
func storablePath(p string) bool { return utf8.ValidString(p) && strings.IndexByte(p, 0) < 0 }

// inExcludedDir reports whether any directory element of the slash-separated path is excluded.
func inExcludedDir(p string) bool {
	dir, _ := path.Split(p)
	for _, seg := range strings.Split(dir, "/") {
		if IsExcludedDir(seg) {
			return true
		}
	}
	return false
}

// isGeneratedGoPath reports whether p is protobuf-generated Go (.pb.go, .pb.gw.go); such files
// are neither candidates nor indexed.
func isGeneratedGoPath(p string) bool {
	return strings.HasSuffix(p, ".pb.go") || strings.HasSuffix(p, ".pb.gw.go")
}
