// Package postgres persists the repo-intel index with GORM: the seven index tables and the repo
// lookup of the indexer. Every method gets its handle from platformpg.Conn, so a store called
// inside the indexer's transaction joins it.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// foreignKeyViolation is the SQLSTATE of a foreign key violation: the repo is gone.
const foreignKeyViolation = "23503"

// deleteChunk is the most values in one IN list of a delete or select; far below the 65,535 bind
// parameter limit of the protocol.
const deleteChunk = 10_000

var (
	_ app.IndexWriter = (*Store)(nil)
	_ app.IndexReader = (*Store)(nil)
)

// Store is the GORM-backed store of the repo-intel context. It implements app.IndexWriter and
// app.IndexReader.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

// LockRepo takes the transaction-scoped advisory lock of the repo (FR3028). Without a
// transaction in ctx the lock is released when the statement ends.
func (s *Store) LockRepo(ctx context.Context, repoID string) error {
	// A lock function has no row builder in GORM; the key is a bound parameter.
	err := s.conn(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "repo-intel:"+repoID).Error
	if err != nil {
		return fmt.Errorf("lock repo %s: %w", repoID, err)
	}
	return nil
}

// DeleteRepoRows deletes every symbols and "references" row of the repo.
func (s *Store) DeleteRepoRows(ctx context.Context, repoID string) error {
	conn := s.conn(ctx)
	if err := conn.Where("repo_id = ?", repoID).Delete(&symbolRow{}).Error; err != nil {
		return fmt.Errorf("delete symbols: %w", err)
	}
	if err := conn.Where("repo_id = ?", repoID).Delete(&referenceRow{}).Error; err != nil {
		return fmt.Errorf("delete references: %w", err)
	}
	return nil
}

// DeleteFileRows deletes the symbols with path in paths and the references with from_path in
// paths.
func (s *Store) DeleteFileRows(ctx context.Context, repoID string, paths []string) error {
	conn := s.conn(ctx)
	paths = storablePaths(paths)
	for part := range slices.Chunk(paths, deleteChunk) {
		if err := conn.Where("repo_id = ? AND path IN ?", repoID, part).Delete(&symbolRow{}).Error; err != nil {
			return fmt.Errorf("delete symbols of files: %w", err)
		}
		if err := conn.Where("repo_id = ? AND from_path IN ?", repoID, part).Delete(&referenceRow{}).Error; err != nil {
			return fmt.Errorf("delete references of files: %w", err)
		}
	}
	return nil
}

// InsertSymbols makes the text storable (domain.SanitizeText), clamps names to MaxNameUnits UTF-16
// units, drops rows that repeat (path, name, kind, line) after both (the first one wins; the
// unique key would reject the second) and inserts the rest in chunks.
func (s *Store) InsertSymbols(ctx context.Context, repoID string, rows []domain.SymbolRow) error {
	type key struct {
		path, name, kind string
		line             int
	}
	seen := make(map[key]struct{}, len(rows))
	out := make([]symbolRow, 0, len(rows))
	for _, r := range rows {
		path := domain.SanitizeText(r.Path)
		name := domain.ClampName(domain.SanitizeText(r.Name))
		k := key{path, name, r.Kind, r.Line}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		line, endLine, signature, hash := r.Line, r.EndLine, domain.SanitizeText(r.Signature), r.ContentHash
		out = append(out, symbolRow{
			RepoID: repoID, Path: path, Name: name, Kind: r.Kind, Line: &line, EndLine: &endLine,
			Exported: r.Exported, Signature: &signature, ContentHash: &hash,
		})
	}
	return insertChunks(s.conn(ctx), "symbols", out)
}

// InsertReferences sanitizes and clamps to_symbol like InsertSymbols and inserts in chunks.
func (s *Store) InsertReferences(ctx context.Context, repoID string, rows []domain.ReferenceRow) error {
	out := make([]referenceRow, len(rows))
	for i, r := range rows {
		hash := r.ContentHash
		out[i] = referenceRow{
			RepoID: repoID, FromPath: domain.SanitizeText(r.FromPath),
			ToSymbol: domain.ClampName(domain.SanitizeText(r.ToSymbol)), Line: r.Line,
			ContentHash: &hash,
		}
	}
	return insertChunks(s.conn(ctx), "references", out)
}

// ReplaceEdges deletes the repo's file_edges and inserts edges.
func (s *Store) ReplaceEdges(ctx context.Context, repoID string, edges []domain.Edge) error {
	conn := s.conn(ctx)
	if err := conn.Where("repo_id = ?", repoID).Delete(&edgeRow{}).Error; err != nil {
		return fmt.Errorf("delete file_edges: %w", err)
	}
	out := make([]edgeRow, len(edges))
	for i, e := range edges {
		out[i] = edgeRow{RepoID: repoID, FromFile: domain.SanitizeText(e.From), ToFile: domain.SanitizeText(e.To)}
	}
	return insertChunks(conn, "file_edges", out)
}

// ReplaceFilePackages deletes the repo's file_packages and inserts pkgs.
func (s *Store) ReplaceFilePackages(ctx context.Context, repoID string, pkgs []domain.FilePackage) error {
	conn := s.conn(ctx)
	if err := conn.Where("repo_id = ?", repoID).Delete(&filePackageRow{}).Error; err != nil {
		return fmt.Errorf("delete file_packages: %w", err)
	}
	out := make([]filePackageRow, len(pkgs))
	for i, p := range pkgs {
		out[i] = filePackageRow{RepoID: repoID, FilePath: domain.SanitizeText(p.Path), PackageKey: domain.SanitizeText(p.Key)}
	}
	return insertChunks(conn, "file_packages", out)
}

// ResolveReferences sets decl_file (FR3030): a reference gets file F when exactly one distinct
// file F has an import edge from the reference's file and an exported symbol with the
// reference's name; with none or several such files decl_file stays as it is (NULL after a
// reset or on a first run). A second step (FR20) then resolves the references still NULL within
// the package: file G gets the reference from F when G is the only other file with F's package
// key that has a symbol named like the reference, exported or not.
func (s *Store) ResolveReferences(ctx context.Context, repoID string, reset bool) error {
	conn := s.conn(ctx)
	if reset {
		err := conn.Model(&referenceRow{}).Where("repo_id = ? AND decl_file IS NOT NULL", repoID).
			Update("decl_file", nil).Error
		if err != nil {
			return fmt.Errorf("reset decl_file: %w", err)
		}
	}
	// UPDATE ... FROM over an aggregate sub-select: the builder has no form for it.
	// One aggregate per reference, no second pass over it: the rows were inserted in this very
	// transaction, so the planner has no statistics for them, and a self-join of the candidates
	// (WITH cand ... JOIN uniq) was re-run once per candidate row (67 s for 138k references).
	err := conn.Exec(`
UPDATE "references" r
SET decl_file = c.decl
FROM (
	SELECT r.id AS ref_id, min(e.to_file) AS decl
	FROM "references" r
	JOIN file_edges e ON e.repo_id = r.repo_id AND e.from_file = r.from_path
	JOIN symbols s ON s.repo_id = r.repo_id AND s.path = e.to_file
		AND s.name = r.to_symbol AND s.exported = true
	WHERE r.repo_id = ?
	GROUP BY r.id
	HAVING count(DISTINCT e.to_file) = 1
) c
WHERE r.id = c.ref_id`, repoID).Error
	if err != nil {
		return fmt.Errorf("resolve references: %w", err)
	}
	if err := resolveInPackage(conn, repoID); err != nil {
		return fmt.Errorf("resolve references in package: %w", err)
	}
	return nil
}

// resolveInPackage is the FR20 step of ResolveReferences. The symbols are first aggregated to one
// row per (package key, name) holding the number of distinct declaring files and the least and
// greatest of them; each NULL reference then joins its own file's package row and that one
// aggregate row. "Exactly one other file" is then: one declaring file that is not the
// reference's own, or two of which one is. Joining the references to the other files of the
// package and their symbols instead was cubic when the planner had no statistics (rows fresh in
// the transaction, or tables vacuumed while empty: 300 files × 300 references ran 9.5 min).
//
// Without statistics every estimate is one row, and the planner still picks nested loops over
// the aggregate (90,000 references × 300 package rows × the aggregate: 6 s, 100,000 × 5,000:
// 112 s), so nested loops are switched off for this one statement and the setting restored
// after it: the plan is then hash joins only. SET LOCAL lasts until the transaction ends and a
// failed statement rolls the transaction back, setting included; outside a transaction (tests)
// both settings are no-ops. ANALYZE is no option inside the transaction: it serialises
// concurrent index jobs.
func resolveInPackage(conn *gorm.DB, repoID string) error {
	// GUCs and UPDATE ... FROM over a materialized CTE: the builder has no form for them.
	var nestLoop string
	if err := conn.Raw(`SELECT current_setting('enable_nestloop')`).Scan(&nestLoop).Error; err != nil {
		return fmt.Errorf("read enable_nestloop: %w", err)
	}
	if err := conn.Exec(`SET LOCAL enable_nestloop = off`).Error; err != nil {
		return fmt.Errorf("switch nested loops off: %w", err)
	}
	err := conn.Exec(`
WITH decl AS MATERIALIZED (
	SELECT fp.package_key, s.name, count(DISTINCT s.path) AS n, min(s.path) AS mn, max(s.path) AS mx
	FROM symbols s
	JOIN file_packages fp ON fp.repo_id = s.repo_id AND fp.file_path = s.path
	WHERE s.repo_id = ?
	GROUP BY fp.package_key, s.name
)
UPDATE "references" r
SET decl_file = CASE WHEN d.n = 1 OR r.from_path = d.mx THEN d.mn ELSE d.mx END
FROM file_packages fp, decl d
WHERE r.repo_id = ? AND r.decl_file IS NULL
	AND fp.repo_id = r.repo_id AND fp.file_path = r.from_path
	AND d.package_key = fp.package_key AND d.name = r.to_symbol
	AND ((d.n = 1 AND d.mn <> r.from_path) OR (d.n = 2 AND r.from_path IN (d.mn, d.mx)))`, repoID, repoID).Error
	if err != nil {
		return err
	}
	if err := conn.Exec(`SELECT set_config('enable_nestloop', ?, true)`, nestLoop).Error; err != nil {
		return fmt.Errorf("restore enable_nestloop: %w", err)
	}
	return nil
}

// ReplaceFileRank deletes the repo's file_rank rows and inserts rows.
func (s *Store) ReplaceFileRank(ctx context.Context, repoID string, rows []domain.FileRankRow) error {
	conn := s.conn(ctx)
	if err := conn.Where("repo_id = ?", repoID).Delete(&rankRow{}).Error; err != nil {
		return fmt.Errorf("delete file_rank: %w", err)
	}
	out := make([]rankRow, len(rows))
	for i, r := range rows {
		out[i] = rankRow{
			RepoID: repoID, FilePath: domain.SanitizeText(r.Path), PageRank: r.PageRank, Hotness: r.Hotness, Rank: r.Rank,
			Percentile: r.Percentile,
		}
	}
	return insertChunks(conn, "file_rank", out)
}

// ReplaceFileFacts deletes the repo's file_facts rows and inserts rows that hold at least one
// endpoint or cron.
func (s *Store) ReplaceFileFacts(ctx context.Context, repoID string, rows []domain.FileFacts) error {
	conn := s.conn(ctx)
	if err := conn.Where("repo_id = ?", repoID).Delete(&factsRow{}).Error; err != nil {
		return fmt.Errorf("delete file_facts: %w", err)
	}
	return insertFacts(conn, repoID, rows)
}

// PatchFileFacts deletes the file_facts of paths and inserts rows that hold at least one endpoint
// or cron.
func (s *Store) PatchFileFacts(ctx context.Context, repoID string, paths []string, rows []domain.FileFacts) error {
	conn := s.conn(ctx)
	paths = storablePaths(paths)
	for part := range slices.Chunk(paths, deleteChunk) {
		if err := conn.Where("repo_id = ? AND file_path IN ?", repoID, part).Delete(&factsRow{}).Error; err != nil {
			return fmt.Errorf("delete file_facts of files: %w", err)
		}
	}
	return insertFacts(conn, repoID, rows)
}

func insertFacts(conn *gorm.DB, repoID string, rows []domain.FileFacts) error {
	out := make([]factsRow, 0, len(rows))
	for _, r := range rows {
		if len(r.Endpoints) == 0 && len(r.Crons) == 0 {
			continue
		}
		endpoints, err := marshalStrings(sanitizeAll(r.Endpoints))
		if err != nil {
			return err
		}
		crons, err := marshalStrings(sanitizeAll(r.Crons))
		if err != nil {
			return err
		}
		out = append(out, factsRow{RepoID: repoID, FilePath: domain.SanitizeText(r.Path), Endpoints: endpoints, Crons: crons})
	}
	return insertChunks(conn, "file_facts", out)
}

// storablePaths drops the paths that no row can hold (not valid UTF-8, or holding U+0000): the
// walk never indexes them, and Postgres would fail the statement on one of them. A changed path
// reported by git can be one.
func storablePaths(paths []string) []string {
	if !slices.ContainsFunc(paths, unstorable) {
		return paths
	}
	return slices.DeleteFunc(slices.Clone(paths), unstorable)
}

func unstorable(p string) bool { return !utf8.ValidString(p) || strings.IndexByte(p, 0) >= 0 }

// sanitizeAll applies domain.SanitizeText to every element (jsonb refuses \u0000 too).
func sanitizeAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = domain.SanitizeText(s)
	}
	return out
}

// DeleteRepoMaps deletes every repo_map_cache row of the repo.
func (s *Store) DeleteRepoMaps(ctx context.Context, repoID string) error {
	if err := s.conn(ctx).Where("repo_id = ?", repoID).Delete(&mapRow{}).Error; err != nil {
		return fmt.Errorf("delete repo_map_cache: %w", err)
	}
	return nil
}

// PutRepoMap inserts a repo_map_cache row; a row for the same (repo, sha, budget) is replaced and
// gets a fresh created_at.
func (s *Store) PutRepoMap(ctx context.Context, repoID string, m domain.RepoMap) error {
	row := mapRow{RepoID: repoID, CommitSHA: m.CommitSHA, TokenBudget: m.Budget, MapText: m.Text, TokenCount: m.Tokens}
	err := s.conn(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "repo_id"}, {Name: "commit_sha"}, {Name: "token_budget"}},
		DoUpdates: clause.Assignments(map[string]any{
			"map_text":    gorm.Expr("excluded.map_text"),
			"token_count": gorm.Expr("excluded.token_count"),
			"created_at":  gorm.Expr("now()"),
		}),
	}).Create(&row).Error
	if err != nil {
		return writeError("insert repo_map_cache", err)
	}
	return nil
}

// UpsertState inserts or replaces the repo's state row; st.UpdatedAt is written as given.
func (s *Store) UpsertState(ctx context.Context, st domain.IndexState) error {
	stats := st.Stats
	if stats == nil {
		stats = map[string]any{}
	}
	encoded, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("encode index stats: %w", err)
	}
	row := stateRow{
		RepoID: st.RepoID, LastIndexedSHA: st.LastIndexedSHA, IndexerVersion: st.IndexerVersion,
		Status: string(st.Status), FilesIndexed: st.FilesIndexed, FilesSkipped: st.FilesSkipped,
		Stats: encoded, UpdatedAt: st.UpdatedAt,
	}
	err = s.conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repo_id"}},
		UpdateAll: true,
	}).Create(&row).Error
	if err != nil {
		return writeError("upsert repo_index_state", err)
	}
	return nil
}

// TouchState sets updated_at to at and changes nothing else; no row, no effect.
func (s *Store) TouchState(ctx context.Context, repoID string, at time.Time) error {
	err := s.conn(ctx).Model(&stateRow{}).Where("repo_id = ?", repoID).Update("updated_at", at).Error
	if err != nil {
		return fmt.Errorf("touch repo_index_state: %w", err)
	}
	return nil
}

// AdvanceSHA sets last_indexed_sha to sha and updated_at to at; no row, no effect.
func (s *Store) AdvanceSHA(ctx context.Context, repoID, sha string, at time.Time) error {
	err := s.conn(ctx).Model(&stateRow{}).Where("repo_id = ?", repoID).
		Updates(map[string]any{"last_indexed_sha": sha, "updated_at": at}).Error
	if err != nil {
		return fmt.Errorf("advance repo_index_state: %w", err)
	}
	return nil
}

// insertChunks inserts rows with one statement per domain.InsertChunkRows rows; no rows, no
// statement.
func insertChunks[T any](conn *gorm.DB, table string, rows []T) error {
	for part := range slices.Chunk(rows, domain.InsertChunkRows) {
		if err := conn.Create(&part).Error; err != nil {
			return writeError("insert "+table, err)
		}
	}
	return nil
}

// writeError wraps a failed write; a foreign key violation means the repo was deleted meanwhile
// and wraps domain.ErrRepoNotFound.
func writeError(what string, err error) error {
	if sqlState(err) == foreignKeyViolation {
		return fmt.Errorf("%s: %w: %w", what, domain.ErrRepoNotFound, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// sqlState returns the SQLSTATE the error carries, without importing the driver.
func sqlState(err error) string {
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		return coded.SQLState()
	}
	return ""
}
