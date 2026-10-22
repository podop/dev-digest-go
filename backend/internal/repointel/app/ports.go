// Package app holds the repo-intel use cases (indexer, jobs, service, read port) and the ports
// they need. Ports are exported so each adapter can assert `var _ app.X = (*Impl)(nil)` without
// the composition root.
package app

import (
	"context"
	"time"

	"dev-digest-go/backend/internal/repointel/domain"
)

// Tx is the transaction boundary of an index. Stores take part in it through the context
// (postgres.Conn); the use case decides where it starts and ends.
type Tx interface {
	// InTx runs fn in one transaction: commit on nil, rollback on an error or a panic. Joins a
	// transaction already carried by ctx.
	InTx(ctx context.Context, fn func(context.Context) error) error
	// Savepoint runs fn inside a SAVEPOINT of the transaction ctx carries: an error from fn rolls
	// back to the savepoint only and is returned. It returns an error when ctx carries no
	// transaction.
	Savepoint(ctx context.Context, fn func(context.Context) error) error
}

// IndexWriter writes the index tables. Every method uses the transaction ctx carries (and works
// without one as a single statement). Rows are written in chunks (domain.InsertChunkRows); an
// empty row set issues no statement. A write whose repo no longer exists (foreign-key
// violation) returns an error wrapping domain.ErrRepoNotFound.
type IndexWriter interface {
	// LockRepo takes the transaction-scoped advisory lock of the repo; the first statement of
	// every index transaction (FR3028). It blocks until other holders commit.
	LockRepo(ctx context.Context, repoID string) error
	// DeleteRepoRows deletes every symbols and "references" row of the repo.
	DeleteRepoRows(ctx context.Context, repoID string) error
	// DeleteFileRows deletes the symbols with path in paths and the references with from_path in
	// paths.
	DeleteFileRows(ctx context.Context, repoID string, paths []string) error
	// InsertSymbols clamps names (domain.ClampUTF16, MaxNameUnits) and drops rows that repeat
	// (path, name, kind, line) after the clamp before inserting.
	InsertSymbols(ctx context.Context, repoID string, rows []domain.SymbolRow) error
	// InsertReferences clamps to_symbol like InsertSymbols.
	InsertReferences(ctx context.Context, repoID string, rows []domain.ReferenceRow) error
	// ReplaceEdges deletes the repo's file_edges and inserts edges.
	ReplaceEdges(ctx context.Context, repoID string, edges []domain.Edge) error
	// ResolveReferences sets decl_file as FR3030 defines; reset first sets every decl_file of the
	// repo to NULL.
	ResolveReferences(ctx context.Context, repoID string, reset bool) error
	// ReplaceFileRank deletes the repo's file_rank rows and inserts rows.
	ReplaceFileRank(ctx context.Context, repoID string, rows []domain.FileRankRow) error
	// ReplaceFileFacts deletes the repo's file_facts rows and inserts rows.
	ReplaceFileFacts(ctx context.Context, repoID string, rows []domain.FileFacts) error
	// PatchFileFacts deletes the file_facts of paths and inserts rows (incremental slice).
	PatchFileFacts(ctx context.Context, repoID string, paths []string, rows []domain.FileFacts) error
	// DeleteRepoMaps deletes every repo_map_cache row of the repo.
	DeleteRepoMaps(ctx context.Context, repoID string) error
	// PutRepoMap inserts a repo_map_cache row (created_at now).
	PutRepoMap(ctx context.Context, repoID string, m domain.RepoMap) error
	// UpsertState inserts or replaces the repo's state row; st.UpdatedAt is written as given
	// (the caller sets it from its clock).
	UpsertState(ctx context.Context, st domain.IndexState) error
	// TouchState sets updated_at to at and changes nothing else; no row, no effect.
	TouchState(ctx context.Context, repoID string, at time.Time) error
	// AdvanceSHA sets last_indexed_sha to sha and updated_at to at; no row, no effect.
	AdvanceSHA(ctx context.Context, repoID, sha string, at time.Time) error
}

// IndexReader reads the repo and the index tables. A method called with a ctx that carries a
// transaction (MapCandidates inside an index) sees that transaction's writes.
type IndexReader interface {
	// GetRepo returns domain.ErrRepoNotFound for an unknown id.
	GetRepo(ctx context.Context, repoID string) (domain.Repo, error)
	// GetState returns ok=false when the repo has no state row.
	GetState(ctx context.Context, repoID string) (st domain.IndexState, ok bool, err error)
	// ListBackfill returns, ordered by repo id, the repos with a clone path that have no state
	// row or a row whose indexer_version differs from domain.IndexerVersion (FR3008).
	ListBackfill(ctx context.Context) ([]domain.Repo, error)
	// MapCandidates returns the candidates of section 7.7 in their order: rank DESC, exported
	// DESC, line ASC, name ASC, path ASC (byte order).
	MapCandidates(ctx context.Context, repoID string) ([]domain.MapCandidate, error)
	// CachedMap returns the repo_map_cache row for (repo, sha, budget); ok=false on a miss.
	CachedMap(ctx context.Context, repoID, sha string, budget int) (m domain.RepoMap, ok bool, err error)
	// FileRanks returns the percentile of each of paths that has a file_rank row (any order).
	FileRanks(ctx context.Context, repoID string, paths []string) ([]domain.PathPercentile, error)
	// SymbolsInFiles returns the symbols of paths ordered by path, line, name (byte order).
	SymbolsInFiles(ctx context.Context, repoID string, paths []string) ([]domain.SymbolInfo, error)
	// RankedPaths returns the top limit file_rank rows by rank DESC, then path ASC (byte order).
	RankedPaths(ctx context.Context, repoID string, limit int) ([]domain.RankedPath, error)
	// Edges returns every file_edges row of the repo ordered by from, then to (byte order).
	Edges(ctx context.Context, repoID string) ([]domain.Edge, error)
}

// Git is the part of *gitcli.Client the indexer and the resync job use. owner and name locate
// the clone under the git root.
type Git interface {
	// Head returns the clone's HEAD sha.
	Head(ctx context.Context, owner, name string) (string, error)
	// DiffNames lists the paths changed between base and head; it fails (gitcli.ErrOutputTooLarge
	// for output above the git output cap) when it cannot, e.g. base is missing from a shallow
	// clone.
	DiffNames(ctx context.Context, owner, name, base, head string) ([]string, error)
	// DefaultBranch asks the remote for its default branch.
	DefaultBranch(ctx context.Context, owner, name string) (string, error)
	// Sync fetches branch, resets the clone to it and returns the new HEAD sha.
	Sync(ctx context.Context, owner, name, branch string) (string, error)
}

// Analyzer extracts symbols, references and import specifiers from source text with the
// tree-sitter grammars. path is only used for its extension (it selects the grammar and the
// JavaScript or TypeScript import rules). text is valid UTF-8. Implementations are safe for
// concurrent use. A method returns an error wrapping ctx.Err() when ctx ends first; the caller
// bounds one file's parse time with a context deadline (domain.ParseTimeout).
type Analyzer interface {
	// Extract returns the symbols and references of section 7.4 (EC3005: a file with syntax
	// errors yields what the parsable parts hold).
	Extract(ctx context.Context, path, text string) (domain.Extraction, error)
	// Imports returns the import specifiers of section 7.5 in first-seen order, without
	// duplicates and empty strings.
	Imports(ctx context.Context, path, text string) ([]string, error)
}

// GraphBuilder builds the import graph over the walked files: edges importer → imported for the
// specifiers that resolve to another walked file (FR3023). A file that cannot be read or parsed
// contributes no specifier; an error means the builder itself failed (graphFailed, P11).
type GraphBuilder interface {
	BuildEdges(ctx context.Context, root string, files []string) ([]domain.Edge, error)
}

// Enqueuer is the job queue: it records the job and returns at once.
type Enqueuer interface {
	Enqueue(ctx context.Context, workspaceID, kind string, payload any) (jobID string, err error)
}

// Redactor scrubs provider keys out of a text (llm.Client.Redact).
type Redactor interface {
	Redact(s string) string
}
