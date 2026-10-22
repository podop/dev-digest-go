// Package clonefs reads a local repository clone without ever leaving it.
//
// Clone content is untrusted: a repository can contain symlinks to /etc, FIFOs and huge files,
// and the paths handed in come from HTTP requests or from a model. Every function here is
// read-only, treats a symlink anywhere on the path as "not found" (a symlinked file or a
// symlinked directory is never followed, even when it points back into the clone), reads only
// regular files and is confined to the clone root by os.Root as a second line of defence.
// Callers apply their own path-shape and glob rules before calling.
package clonefs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
)

var (
	// ErrNotFound is returned for every failure to reach a regular file inside the clone: a
	// missing clone or file, an invalid path, a symlink on the way, a directory, a device, a
	// FIFO, an unreadable file. Callers cannot tell these apart, on purpose.
	ErrNotFound = errors.New("clonefs: file not found")
	// ErrTooLarge is returned by Read when the file is larger than the cap. Stat still
	// reports the size of such a file.
	ErrTooLarge = errors.New("clonefs: file too large")
)

// Info describes a regular file of the clone.
type Info struct {
	// Size is the file size in bytes.
	Size int64
	// ModTime is the modification time.
	ModTime time.Time
}

// File is a regular file of the clone read in full.
type File struct {
	Info
	// Data is the whole content; len(Data) == Size.
	Data []byte
}

// Stat returns the size and modification time of the regular file at rel (a slash-separated path
// relative to root) without reading it. It returns ErrNotFound as described there.
func Stat(root, rel string) (Info, error) {
	r, err := openRoot(root)
	if err != nil {
		return Info{}, err
	}
	defer r.Close()
	return statIn(r, rel)
}

// Read returns the content of the regular file at rel (a slash-separated path relative to root).
// A file larger than maxBytes is not read: Read returns ErrTooLarge and the file's Info, so the
// caller can still report the size. A file that grows past maxBytes while being read also yields
// ErrTooLarge. Any other failure is ErrNotFound.
func Read(root, rel string, maxBytes int64) (File, error) {
	r, err := openRoot(root)
	if err != nil {
		return File{}, err
	}
	defer r.Close()

	info, err := statIn(r, rel)
	if err != nil {
		return File{}, err
	}
	if info.Size > maxBytes {
		return File{Info: info}, ErrTooLarge
	}
	f, err := r.Open(rel)
	if err != nil {
		return File{}, ErrNotFound
	}
	defer f.Close()
	// The file may have been swapped between the check and the open: check what was opened.
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return File{}, ErrNotFound
	}
	if st.Size() > maxBytes {
		return File{Info: Info{Size: st.Size(), ModTime: st.ModTime()}}, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return File{}, ErrNotFound
	}
	if int64(len(data)) > maxBytes {
		return File{Info: Info{Size: int64(len(data)), ModTime: st.ModTime()}}, ErrTooLarge
	}
	return File{Info: Info{Size: int64(len(data)), ModTime: st.ModTime()}, Data: data}, nil
}

// openRoot opens the clone root. The root itself is the operator-configured clone directory and
// may be reached through a link; everything below it may not.
func openRoot(root string) (*os.Root, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, ErrNotFound
	}
	return r, nil
}

// validRel reports whether rel is a clean, relative, slash-separated path without "." or ".."
// elements, backslashes or NUL.
func validRel(rel string) bool {
	return fs.ValidPath(rel) && rel != "." && !strings.ContainsAny(rel, "\x00\\")
}

// statIn lstat-checks every element of rel: the directories on the way must be real directories
// and the last element a regular file, none of them a symlink.
func statIn(r *os.Root, rel string) (Info, error) {
	if !validRel(rel) {
		return Info{}, ErrNotFound
	}
	for i := 0; i < len(rel); i++ {
		if rel[i] != '/' {
			continue
		}
		st, err := r.Lstat(rel[:i])
		if err != nil || !st.IsDir() { // IsDir is false for a symlink: Lstat does not follow
			return Info{}, ErrNotFound
		}
	}
	st, err := r.Lstat(rel)
	if err != nil || !st.Mode().IsRegular() {
		return Info{}, ErrNotFound
	}
	return Info{Size: st.Size(), ModTime: st.ModTime()}, nil
}
