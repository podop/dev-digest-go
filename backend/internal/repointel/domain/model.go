// Package domain holds the repo-intel rows, constants and pure rules. It imports nothing outside
// the standard library and platform/codehost (clone-key naming, itself standard-library only); the parser, the database and the clone on disk sit behind the ports of
// package app (and SourceFS and TokenCounter below).
package domain

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/codehost"
)

// Limits and versions of the indexer (spec section 7.1).
const (
	// IndexerVersion is written to every state row; a different stored value forces a full index.
	IndexerVersion = 3
	// MaxIndexedFiles bounds the walk: the first files in byte order are kept.
	MaxIndexedFiles = 5000
	// MaxFileBytes is the largest source file that is parsed (400 KiB).
	MaxFileBytes = 409_600
	// ParseTimeout is the parse time allowed per file.
	ParseTimeout = 2000 * time.Millisecond
	// SoftBudget is the full index's time budget, after which no new file is started.
	SoftBudget = 110 * time.Second
	// MaxIncrementalFiles is the most changed source paths an incremental index handles.
	MaxIncrementalFiles = 300
	// MaxSignatureUnits is the signature length limit in UTF-16 units.
	MaxSignatureUnits = 120
	// MaxNameUnits is the length limit of symbols.name and references.to_symbol (FR3031).
	MaxNameUnits = 255
	// DefaultTokenBudget is the repo-map budget in cl100k_base tokens.
	DefaultTokenBudget = 1500
	// MaxParseDegraded bounds the parseDegraded list kept in the state's stats.
	MaxParseDegraded = 50
	// MaxErrorChars bounds error texts kept in state rows and logs.
	MaxErrorChars = 500
	// InsertChunkRows is the row count of one bulk insert statement (P14).
	InsertChunkRows = 500
	// MaxListedFiles bounds ListIndexedFiles.
	MaxListedFiles = 100_000
	// JobTimeout is the per-attempt limit of the three job kinds.
	JobTimeout = 120 * time.Second
)

// Job kinds (rows of the jobs table). Payloads: index {repoId, owner, name}; refresh and resync
// {repoId}.
const (
	KindIndex   = "repo-intel-index"
	KindRefresh = "repo-intel-refresh"
	KindResync  = "repo-intel-resync"
)

// Status is the value of repo_index_state.status.
type Status string

const (
	StatusFull     Status = "full"
	StatusPartial  Status = "partial"
	StatusDegraded Status = "degraded"
	StatusFailed   Status = "failed"
)

// DegradedReason is the machine reason of a degraded or failed state and of a degraded port
// result (the five values of the RepoIndexState contract).
type DegradedReason string

const (
	ReasonFlagOff      DegradedReason = "flag_off"
	ReasonIndexFailed  DegradedReason = "index_failed"
	ReasonIndexPartial DegradedReason = "index_partial"
	ReasonRepoTooLarge DegradedReason = "repo_too_large"
	ReasonNoData       DegradedReason = "no_data"
)

// Keys of the state's stats document that the HTTP view reads (section 7.2) or that more than
// one component writes. Other keys are the indexer's own.
const (
	StatDurationMs     = "durationMs"
	StatReason         = "reason"
	StatDegradedReason = "degradedReason"
)

// Sentinel errors.
var (
	// ErrRepoNotFound: the repo does not exist (or was deleted while a write was running).
	ErrRepoNotFound = errors.New("repo not found")
	// ErrCloneMissing: the clone directory does not exist or is not a directory.
	ErrCloneMissing = errors.New("clone missing")
)

// Syntax selects the tree-sitter grammar of a file.
type Syntax int

const (
	// SyntaxTypeScript is the grammar of .ts files.
	SyntaxTypeScript Syntax = iota + 1
	// SyntaxTSX is the grammar of .tsx and .jsx files (JSX elements are references).
	SyntaxTSX
	// SyntaxJavaScript is the grammar of .js, .mjs and .cjs files.
	SyntaxJavaScript
	// SyntaxGo is the grammar of .go files.
	SyntaxGo
)

// String names the grammar.
func (s Syntax) String() string {
	switch s {
	case SyntaxTypeScript:
		return "typescript"
	case SyntaxTSX:
		return "tsx"
	case SyntaxJavaScript:
		return "javascript"
	case SyntaxGo:
		return "go"
	}
	return "unknown"
}

// SyntaxForPath returns the grammar for a file path by its extension, compared
// case-insensitively; ok is false for every other extension. The supported extensions are
// .ts, .tsx, .js, .jsx, .mjs, .cjs and .go.
func SyntaxForPath(p string) (Syntax, bool) {
	switch strings.ToLower(path.Ext(p)) {
	case ".ts":
		return SyntaxTypeScript, true
	case ".tsx", ".jsx":
		return SyntaxTSX, true
	case ".js", ".mjs", ".cjs":
		return SyntaxJavaScript, true
	case ".go":
		return SyntaxGo, true
	}
	return 0, false
}

// FileImports is what the parser reads from one file's import statements: the file's declared
// package (Go and Java; "" for TypeScript and JavaScript) and its raw import specifiers.
type FileImports struct {
	Package string
	Specs   []string
}

// FilePackage maps a source file to the key of its package (see GoPackageKey).
type FilePackage struct {
	Path string
	Key  string
}

// Graph is the result of building the import graph: file-to-file edges and the package key of
// each file that has one. Both slices are non-nil.
type Graph struct {
	Edges    []Edge
	Packages []FilePackage
}

// GoPackageKey returns the package key of a Go file: "go:" plus its repo-relative directory
// ("go:." for the repository root).
func GoPackageKey(file string) string { return "go:" + path.Dir(file) }

// Repo is the part of a repository row the indexer needs.
type Repo struct {
	ID          string
	WorkspaceID string
	Owner       string
	Name        string
	// Provider is "github" or "gitlab" ("" reads as github); Host is the host key and ProjectID
	// the numeric GitLab project id (0 = none). They locate the clone (CloneKey).
	Provider  string
	Host      string
	ProjectID int64
	// DefaultBranch is the stored branch name.
	DefaultBranch string
	// ClonePath is the clone directory; "" when the repo is not cloned.
	ClonePath string
}

// CloneKey returns the (owner, name) pair the git client locates the clone under: (Owner, Name)
// for GitHub, ("@gitlab/<host dir>", "<project id>") for GitLab (codehost.CloneKey).
func (r Repo) CloneKey() (owner, name string) {
	return codehost.CloneKey(r.Provider, r.Host, r.ProjectID, r.Owner, r.Name)
}

// Symbol is a top-level declaration extracted from one file (section 7.4).
type Symbol struct {
	Name     string
	Kind     string // function, class, method, interface, type, enum
	Line     int    // 1-based
	EndLine  int    // 1-based
	Exported bool
	// Signature is the declaration head, at most MaxSignatureUnits UTF-16 units.
	Signature string
}

// Reference is a use of a name (call, new, JSX element) extracted from one file.
type Reference struct {
	Name string
	Line int // 1-based
}

// Extraction is what the parser returns for one file.
type Extraction struct {
	Symbols    []Symbol
	References []Reference
}

// SymbolRow is a symbols row (repo id left out: a write names the repo once).
type SymbolRow struct {
	Path        string
	Name        string
	Kind        string
	Line        int
	EndLine     int
	Exported    bool
	Signature   string
	ContentHash string
}

// ReferenceRow is a "references" row; decl_file is set later by reference resolution.
type ReferenceRow struct {
	FromPath    string
	ToSymbol    string
	Line        int
	ContentHash string
}

// Edge is an import edge: importer From → imported To, repo-relative paths.
type Edge struct {
	From string
	To   string
}

// FileRankRow is a file_rank row.
type FileRankRow struct {
	Path       string
	PageRank   float64
	Hotness    float64
	Rank       float64
	Percentile int // 1..100
}

// FileFacts is a file_facts row: the endpoints ("GET /x") and cron/job expressions of a file.
type FileFacts struct {
	Path      string
	Endpoints []string
	Crons     []string
}

// MapCandidate is one row of the repo-map candidate query (section 7.7): a symbol with a
// non-null signature joined with the rank of its file.
type MapCandidate struct {
	Path      string
	Name      string
	Exported  bool
	Line      int
	Signature string
	Rank      float64
}

// RepoMap is a repo_map_cache row.
type RepoMap struct {
	CommitSHA string
	Budget    int
	Text      string
	Tokens    int
}

// IndexState is a repo_index_state row.
type IndexState struct {
	RepoID         string
	LastIndexedSHA string
	IndexerVersion int
	Status         Status
	FilesIndexed   int
	FilesSkipped   int
	// Stats is the JSON stats document; keys as in the spec (durationMs, reason, ...).
	Stats     map[string]any
	UpdatedAt time.Time
}

// WalkStats are the walk counters recorded in the state's stats.
type WalkStats struct {
	// TotalCandidates counts files with a supported extension before the size and bound filters.
	TotalCandidates int
	// SkippedTooLarge counts candidates left out because they are larger than MaxFileBytes or have
	// a path that is not valid UTF-8 (the stats key keeps the Node name).
	SkippedTooLarge int
	// Bounded is the number of files dropped by the MaxIndexedFiles bound.
	Bounded int
}

// WalkResult is the list of source files of a clone.
type WalkResult struct {
	// Files are slash-separated paths relative to the clone root, sorted by byte order, at most
	// MaxIndexedFiles.
	Files []string
	Stats WalkStats
}

// PathPercentile is a FileRank port result.
type PathPercentile struct {
	Path       string
	Percentile int
}

// RankedPath is a file_rank row reduced to path and rank.
type RankedPath struct {
	Path string
	Rank float64
}

// SymbolInfo is a SymbolsInFiles port result. Lines are 0 when the row has none.
type SymbolInfo struct {
	File      string
	Name      string
	Kind      string
	Exported  bool
	StartLine int
	EndLine   int
	// Signature is nil when the row has none.
	Signature *string
}

// RepoMapResult is the RepoMap port result.
type RepoMapResult struct {
	Text   string
	Tokens int
	Cached bool
	// Degraded is true when Reason explains an empty map (flag_off, no_data).
	Degraded bool
	Reason   DegradedReason
}

// SourceFS is read-only access to a clone on disk. Clone content is untrusted: implementations
// stay inside root, never follow a symbolic link and never write.
type SourceFS interface {
	// Walk lists the clone's source files as the walk rules of FR3013 define them. It wraps
	// ErrCloneMissing when root is not a readable directory and returns ctx's error when ctx ends.
	Walk(ctx context.Context, root string) (WalkResult, error)
	// ReadFile returns the raw bytes of the regular file rel (any file, e.g. tsconfig.json), or an
	// error when it is missing, a symlink, behind a symlink, not repo-relative or larger than the
	// read cap; a file that cannot be reached satisfies errors.Is(err, fs.ErrNotExist). Decoding to
	// text is the caller's job (see the text rules of this package).
	ReadFile(root, rel string) ([]byte, error)
}

// TokenCounter counts cl100k_base tokens. Implementations are safe for concurrent use and never
// fail: special-token literals count as ordinary text (FR3027).
type TokenCounter interface {
	Count(text string) int
}
