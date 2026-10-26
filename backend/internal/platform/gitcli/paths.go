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

// gitlabDir is the top-level clone directory of GitLab projects. '@' is outside the segment
// rule, so no GitHub owner can be named like it (spec FR18, EC22).
const gitlabDir = "@gitlab"

var projectIDRE = regexp.MustCompile(`^[0-9]{1,19}$`)

// gitlabHostSegment returns the host-key directory of a GitLab clone key's owner
// ("@gitlab/<host>[_<port>]") and whether owner has that shape (the segment is not validated).
func gitlabHostSegment(owner string) (string, bool) {
	return strings.CutPrefix(owner, gitlabDir+"/")
}

// ClonePath returns the clone directory of a repository, guaranteed to be exactly two levels
// under the clone root (<root>/<owner>/<name>, GitHub), or three for the GitLab clone key:
// owner "@gitlab/<host>[_<port>]" and name "<project id>" give
// <root>/@gitlab/<host>[_<port>]/<project id>. It does not touch the filesystem.
func (c *Client) ClonePath(owner, name string) (string, error) {
	levels := 2
	if host, ok := gitlabHostSegment(owner); ok {
		if !validSegment(host) || !projectIDRE.MatchString(name) {
			return "", ErrInvalidSegment
		}
		levels = 3
	} else {
		for _, seg := range []string{owner, name} {
			if !validSegment(seg) {
				return "", ErrInvalidSegment
			}
		}
	}
	dest := filepath.Join(c.root, filepath.FromSlash(owner), name)
	rel, err := filepath.Rel(c.root, dest)
	if err != nil || rel == "." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, "..") || len(strings.Split(rel, string(filepath.Separator))) != levels {
		return "", ErrUnsafePath
	}
	return dest, nil
}

// prepareDest makes sure every directory above the destination exists as a real directory and
// that no level down to the destination is a symlink, so that creating or removing it cannot
// reach outside the clone root.
func (c *Client) prepareDest(dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(c.root, 0o750); err != nil {
		return fmt.Errorf("create clone root: %w", err)
	}
	if err := c.rejectSymlinks(parent); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("create owner directory: %w", err)
	}
	// Re-check after creation: every entry must now be a plain directory.
	return c.rejectSymlinks(dest)
}

// rejectSymlinks fails with ErrUnsafePath when p, or any directory between the clone root and p,
// exists and is a symlink.
func (c *Client) rejectSymlinks(p string) error {
	for ; p != c.root && len(p) > len(c.root); p = filepath.Dir(p) {
		if err := rejectSymlink(p); err != nil {
			return err
		}
	}
	return nil
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
