// Package domain is the vocabulary of the smart-diff context (Phase 5, spec section 7.6): the
// role a changed file plays in a pull request and the grouped read model the Files-changed tab
// shows. The pure rules (glob classification, grouping, ordering) are added next to this file; it
// fixes only the shared types. It imports only the standard library and carries no JSON or
// database tags: the wire shape lives in transport/http.
package domain

import "errors"

// ErrNotFound is returned by the store for a pull request that does not exist in the workspace.
var ErrNotFound = errors.New("smartdiff: not found")

// Role is the part a changed file plays in a pull request.
type Role string

// The roles. Classification tries boilerplate, tests, wiring and docs in that order; a path that
// matches none is core.
const (
	RoleCore        Role = "core"
	RoleTests       Role = "tests"
	RoleWiring      Role = "wiring"
	RoleDocs        Role = "docs"
	RoleBoilerplate Role = "boilerplate"
)

// Roles lists every role in the fixed order of the response's five groups.
func Roles() []Role {
	return []Role{RoleCore, RoleTests, RoleWiring, RoleDocs, RoleBoilerplate}
}

// PullFile is one stored file of a pull request (pr_files).
type PullFile struct {
	Path                 string
	Additions, Deletions int
}

// FindingRef is the place of one finding: its file and first line.
type FindingRef struct {
	File      string
	StartLine int
}

// FileEntry is a changed file in a group.
type FileEntry struct {
	Path                 string
	Additions, Deletions int
	// FindingLines are the distinct start lines of the findings on this file, ascending; never
	// nil.
	FindingLines []int
}

// Group is the files of one role, sorted by path in English collation; never nil.
type Group struct {
	Role  Role
	Files []FileEntry
}

// SmartDiff is the read model of a pull request: always five groups in Roles() order. TotalLines
// is the sum of additions and deletions over all files. The wire response adds the constant
// split suggestion (too_big false, proposed_splits []); the context never proposes splits.
type SmartDiff struct {
	Groups     []Group
	TotalLines int
}
