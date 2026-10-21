// Package domain holds the repository entity and the GitHub URL rules of the repos context.
// It depends on the standard library only.
package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Sentinel errors of the repos context.
var (
	// ErrNotFound means no repo matches the id (or it belongs to another workspace).
	ErrNotFound = errors.New("repo not found")
	// ErrInvalidURL means the text is not an accepted GitHub repository URL.
	ErrInvalidURL = errors.New("not a GitHub repository URL")
)

// InvalidURLMessage is the validation message the API shows for a rejected URL.
const InvalidURLMessage = "Must be a GitHub repository URL, e.g. https://github.com/owner/repo"

// DefaultBranch is the branch a repo starts with, until a refresh learns the remote's.
const DefaultBranch = "main"

// Repo is a GitHub repository tracked in a workspace.
type Repo struct {
	ID            string
	WorkspaceID   string
	Owner         string
	Name          string
	FullName      string
	DefaultBranch string
	// ClonePath is the absolute clone directory, nil until a clone job succeeded.
	ClonePath    *string
	LastPolledAt *time.Time
	CreatedBy    *string
}

// Cloned reports whether the repo has a clone on disk.
func (r Repo) Cloned() bool { return r.ClonePath != nil }

// Ref identifies a GitHub repository by owner and name.
type Ref struct {
	Owner string
	Name  string
}

// FullName returns "owner/name".
func (r Ref) FullName() string { return r.Owner + "/" + r.Name }

// CloneURL returns the HTTPS clone URL on base (e.g. "https://github.com"); every accepted
// URL form, SSH included, is cloned over HTTPS.
func (r Ref) CloneURL(base string) string {
	return strings.TrimRight(base, "/") + "/" + r.Owner + "/" + r.Name + ".git"
}

// repoURLPattern accepts https://github.com/<owner>/<repo>[.git][/] and
// git@github.com:<owner>/<repo>[.git]; the segment charset matches the clone-path rule.
var repoURLPattern = regexp.MustCompile(
	`^(?:https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?|` +
		`git@github\.com:([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?)$`)

// ParseURL extracts owner and name from a GitHub repository URL after trimming whitespace.
// It wraps ErrInvalidURL for anything else, including segments that are unsafe as directory
// names ("." , "..", ".git" in any case, a leading "-").
func ParseURL(raw string) (Ref, error) {
	m := repoURLPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return Ref{}, ErrInvalidURL
	}
	owner, name := m[1], m[2]
	if owner == "" {
		owner, name = m[3], m[4]
	}
	if !safeSegment(owner) || !safeSegment(name) {
		return Ref{}, ErrInvalidURL
	}
	return Ref{Owner: owner, Name: name}, nil
}

func safeSegment(s string) bool {
	switch strings.ToLower(s) {
	case "", ".", "..", ".git":
		return false
	}
	return !strings.HasPrefix(s, "-")
}
