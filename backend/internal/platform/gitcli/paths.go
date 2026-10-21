package gitcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Sentinel errors for rejected input. They never carry the rejected value, so
// they are safe to surface in job errors.
var (
	// ErrInvalidSegment reports an owner or name that is not a safe clone-path segment.
	ErrInvalidSegment = errors.New("invalid repo path segment")
	// ErrUnsafePath reports a clone path that would leave <root>/<owner>/<name>.
	ErrUnsafePath = errors.New("clone path is outside the clone root")
	// ErrInvalidRef reports a branch name that is not a plain ref.
	ErrInvalidRef = errors.New("invalid branch name")
)

var (
	segmentRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	refRE     = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// validSegment applies the FR35 segment rule.
func validSegment(s string) bool {
	if !segmentRE.MatchString(s) || strings.HasPrefix(s, "-") {
		return false
	}
	switch strings.ToLower(s) {
	case ".", "..", ".git":
		return false
	}
	return true
}

// validRef accepts plain branch names only: never an option, a range or a
// revision expression.
func validRef(s string) bool {
	return refRE.MatchString(s) &&
		!strings.HasPrefix(s, "-") &&
		!strings.HasPrefix(s, "/") &&
		!strings.HasSuffix(s, "/") &&
		!strings.Contains(s, "..") &&
		!strings.Contains(s, "//") &&
		!strings.HasSuffix(s, ".lock")
}

// ClonePath returns <root>/<owner>/<name>, guaranteed to be exactly two levels
// under the clone root. It does not touch the filesystem.
func (c *Client) ClonePath(owner, name string) (string, error) {
	for _, seg := range []string{owner, name} {
		if !validSegment(seg) {
			return "", ErrInvalidSegment
		}
	}
	dest := filepath.Join(c.root, owner, name)
	rel, err := filepath.Rel(c.root, dest)
	if err != nil || rel == "." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, "..") || len(strings.Split(rel, string(filepath.Separator))) != 2 {
		return "", ErrUnsafePath
	}
	return dest, nil
}

// prepareDest makes sure <root>/<owner> exists as a real directory and that the
// destination is not a symlink, so that creating or removing it cannot reach
// outside the clone root.
func (c *Client) prepareDest(dest string) error {
	ownerDir := filepath.Dir(dest)
	if err := os.MkdirAll(c.root, 0o750); err != nil {
		return fmt.Errorf("create clone root: %w", err)
	}
	if err := rejectSymlink(ownerDir); err != nil {
		return err
	}
	if err := os.MkdirAll(ownerDir, 0o750); err != nil {
		return fmt.Errorf("create owner directory: %w", err)
	}
	// Re-check after creation: the entry must now be a plain directory.
	if err := rejectSymlink(ownerDir); err != nil {
		return err
	}
	return rejectSymlink(dest)
}

// rejectSymlink fails with ErrUnsafePath when p exists and is a symlink.
func rejectSymlink(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect clone path: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}
