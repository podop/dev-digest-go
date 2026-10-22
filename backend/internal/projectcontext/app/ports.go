package app

import (
	"context"
	"errors"
	"time"

	"dev-digest-go/backend/internal/projectcontext/domain"
)

// Sources of a document.
const (
	SourceRepo  = "repo"
	SourceStore = "store"
)

// Ways an agent uses a document.
const (
	ViaDirect = "direct"
	ViaSkill  = "skill"
)

// Values of List.CloneStatus.
const (
	CloneReady     = "ready"
	CloneNotCloned = "not_cloned"
)

// Sentinel errors of the store port; the service maps them to application errors.
var (
	// ErrPathExists means a store file already holds the (repo, path).
	ErrPathExists = errors.New("store file path exists")
	// ErrOwnerGone means the agent, skill or repo of an insert was deleted meanwhile.
	ErrOwnerGone = errors.New("owner or repository no longer exists")
	// ErrRepoGone means the repo of a store file insert was deleted meanwhile.
	ErrRepoGone = errors.New("repository no longer exists")
)

// Repo is the part of a repository row this context needs.
type Repo struct {
	ID string
	// ClonePath is the clone directory; nil until the repo has been cloned.
	ClonePath *string
}

// UsageRow is one fact "agent X uses document P in this repo", direct or through a linked skill.
type UsageRow struct {
	Path      string
	AgentID   string
	AgentName string
	Via       string // ViaDirect or ViaSkill
	SkillName string // set when Via is ViaSkill
}

// File is a store file as saved.
type File struct {
	Path      string
	Content   string
	SizeBytes int
	Version   int
	UpdatedAt time.Time
}

// FileInfo is a store file in a listing: no content, its length in characters instead.
type FileInfo struct {
	Path      string
	SizeBytes int
	Chars     int
	Version   int
	UpdatedAt time.Time
}

// store is what the use cases need from persistence. Every method joins the transaction of ctx.
type store interface {
	// FindRepo returns the repo of the workspace; ok is false when there is none.
	FindRepo(ctx context.Context, workspaceID, repoID string) (repo Repo, ok bool, err error)
	// LockRepo locks the repo row until the transaction ends; false when there is none.
	LockRepo(ctx context.Context, workspaceID, repoID string) (bool, error)
	AgentExists(ctx context.Context, workspaceID, agentID string) (bool, error)
	SkillExists(ctx context.Context, workspaceID, skillID string) (bool, error)

	// ListUsage returns every attachment of the repo joined to the agents that use it.
	ListUsage(ctx context.Context, repoID string) ([]UsageRow, error)
	// AgentPaths and SkillPaths return the attached paths of an owner in one repo by position.
	AgentPaths(ctx context.Context, agentID, repoID string) ([]string, error)
	SkillPaths(ctx context.Context, skillID, repoID string) ([]string, error)
	// ReplaceAgentPaths and ReplaceSkillPaths replace the list under a row lock on the owner;
	// ErrOwnerGone when the owner or repo vanished.
	ReplaceAgentPaths(ctx context.Context, agentID, repoID string, paths []string) error
	ReplaceSkillPaths(ctx context.Context, skillID, repoID string, paths []string) error

	// ListFiles returns the repo's store files by path in byte order, without content.
	ListFiles(ctx context.Context, repoID string) ([]FileInfo, error)
	FindFile(ctx context.Context, repoID, path string) (file File, ok bool, err error)
	// InsertFile inserts at version 1; ErrPathExists / ErrRepoGone.
	InsertFile(ctx context.Context, repoID, path, content string, sizeBytes int) (File, error)
	// SaveFile updates only when the version equals base and bumps it; ok is false otherwise.
	SaveFile(ctx context.Context, repoID, path, content string, sizeBytes, base int) (file File, ok bool, err error)
	// RenameFile moves only when the version equals base and bumps it; ok is false otherwise.
	// ErrPathExists when newPath is taken.
	RenameFile(ctx context.Context, repoID, path, newPath string, base int) (file File, ok bool, err error)
	// MoveAttachments repoints every agent and skill attachment of from to to; an owner that
	// already attaches to loses that row first.
	MoveAttachments(ctx context.Context, repoID, from, to string) error
	// DeleteFile reports whether a row was deleted. Attachments stay.
	DeleteFile(ctx context.Context, repoID, path string) (bool, error)
}

// ClonedDoc is a listable document of a clone.
type ClonedDoc struct {
	Path      string
	SizeBytes int64
	// Tokens is the token estimate (D218).
	Tokens    int
	UpdatedAt time.Time
}

// ClonedList is the documents of a clone.
type ClonedList struct {
	// Docs are the first docs by path (byte order), at most the limit.
	Docs []ClonedDoc
	// Truncated is true when more documents matched than the limit.
	Truncated bool
}

// ClonedStatus is the outcome of reading one clone document.
type ClonedStatus int

// Outcomes of CloneDocs.Read.
const (
	ClonedOK ClonedStatus = iota
	ClonedNotFound
	ClonedTooLarge
)

// ClonedRead is one clone document read.
type ClonedRead struct {
	Status ClonedStatus
	// Content, SizeBytes and Tokens are set when Status is ClonedOK.
	Content   string
	SizeBytes int64
	Tokens    int
}

// cloneDocs reads documents of a clone. It never leaves the clone root and never writes.
type cloneDocs interface {
	// List returns the clone's documents that globs accept; ok is false when root is not a
	// readable directory.
	List(ctx context.Context, root string, globs *domain.GlobSet, limit int) (list ClonedList, ok bool, err error)
	// Read reads one document; the caller has applied the path-shape and glob gates.
	Read(root, path string) ClonedRead
	// Exists reports whether a regular file (no symlink on the way) is at path.
	Exists(root, path string) bool
}

// txRunner runs fn in one database transaction.
type txRunner interface {
	InTx(ctx context.Context, fn func(context.Context) error) error
}

// gitReader reads documents of a clone at a commit; *gitcli.Client serves it. BlobAt reports a
// blob over max bytes as gitcli.ErrBlobTooLarge and a path that is not in the commit as
// gitcli.ErrBlobMissing.
type gitReader interface {
	// BaseCommit resolves the commit a pull request's base side is read at from the base
	// branch name and the head SHA; "" means none could be resolved.
	BaseCommit(ctx context.Context, owner, name, baseRef, headSHA string) (string, error)
	// BlobAt returns the file at path in commit, reading at most max bytes.
	BlobAt(ctx context.Context, owner, name, commit, path string, max int64) ([]byte, error)
}
