package importgraph_test

import (
	"fmt"
	"io/fs"
	"sort"
)

// fakeTree is an in-memory clone: every entry is a file for IsFile, and ReadFile returns its text.
type fakeTree map[string]string

func (f fakeTree) IsFile(rel string) bool { _, ok := f[rel]; return ok }

func (f fakeTree) ReadFile(rel string) ([]byte, error) {
	if s, ok := f[rel]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("read %s: %w", rel, fs.ErrNotExist)
}

func (f fakeTree) paths() []string {
	out := make([]string, 0, len(f))
	for p := range f {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
