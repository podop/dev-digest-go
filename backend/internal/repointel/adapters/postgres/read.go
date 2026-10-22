package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/repointel/domain"
)

// GetRepo returns the repo or domain.ErrRepoNotFound.
func (s *Store) GetRepo(ctx context.Context, repoID string) (domain.Repo, error) {
	var row repoRow
	err := s.conn(ctx).Where("id = ?", repoID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Repo{}, domain.ErrRepoNotFound
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("select repo %s: %w", repoID, err)
	}
	return row.toDomain(), nil
}

func (r repoRow) toDomain() domain.Repo {
	repo := domain.Repo{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Owner: r.Owner, Name: r.Name, DefaultBranch: r.DefaultBranch,
	}
	if r.ClonePath != nil {
		repo.ClonePath = *r.ClonePath
	}
	return repo
}

// GetState returns ok=false when the repo has no state row.
func (s *Store) GetState(ctx context.Context, repoID string) (domain.IndexState, bool, error) {
	var row stateRow
	err := s.conn(ctx).Where("repo_id = ?", repoID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.IndexState{}, false, nil
	}
	if err != nil {
		return domain.IndexState{}, false, fmt.Errorf("select repo_index_state %s: %w", repoID, err)
	}
	stats := map[string]any{}
	if len(row.Stats) > 0 {
		if err := json.Unmarshal(row.Stats, &stats); err != nil {
			return domain.IndexState{}, false, fmt.Errorf("decode stats of %s: %w", repoID, err)
		}
		if stats == nil { // a stored JSON null
			stats = map[string]any{}
		}
	}
	return domain.IndexState{
		RepoID: row.RepoID, LastIndexedSHA: row.LastIndexedSHA, IndexerVersion: row.IndexerVersion,
		Status: domain.Status(row.Status), FilesIndexed: row.FilesIndexed, FilesSkipped: row.FilesSkipped,
		Stats: stats, UpdatedAt: row.UpdatedAt,
	}, true, nil
}

// ListBackfill returns, ordered by repo id, the repos with a clone path that have no state row or
// a row of another indexer version (FR3008).
func (s *Store) ListBackfill(ctx context.Context) ([]domain.Repo, error) {
	var rows []repoRow
	// A LEFT JOIN with an anti-condition on the right side: the builder has no join-with-null form.
	err := s.conn(ctx).Raw(`
SELECT r.id, r.workspace_id, r.owner, r.name, r.default_branch, r.clone_path
FROM repos r
LEFT JOIN repo_index_state st ON st.repo_id = r.id
WHERE r.clone_path IS NOT NULL AND r.clone_path <> ''
	AND (st.repo_id IS NULL OR st.indexer_version <> ?)
ORDER BY r.id`, domain.IndexerVersion).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select repos to backfill: %w", err)
	}
	out := make([]domain.Repo, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// candidateRow is a scan target of MapCandidates; line is nullable in the table.
type candidateRow struct {
	Path      string  `gorm:"column:path"`
	Name      string  `gorm:"column:name"`
	Exported  bool    `gorm:"column:exported"`
	Line      *int    `gorm:"column:line"`
	Signature string  `gorm:"column:signature"`
	Rank      float64 `gorm:"column:rank"`
}

// MapCandidates returns the symbols with a signature joined with the rank of their file in the
// order of section 7.7: rank DESC, exported DESC, line ASC (NULL last), name, path (byte order);
// the kind breaks the remaining ties, which the unique key allows.
func (s *Store) MapCandidates(ctx context.Context, repoID string) ([]domain.MapCandidate, error) {
	var rows []candidateRow
	// The collation is set on the underlying columns, never on a select alias (invalid in ORDER BY).
	err := s.conn(ctx).Raw(`
SELECT s.path, s.name, s.exported, s.line, s.signature, f.rank
FROM symbols s
JOIN file_rank f ON f.repo_id = s.repo_id AND f.file_path = s.path
WHERE s.repo_id = ? AND s.signature IS NOT NULL
ORDER BY f.rank DESC, s.exported DESC, s.line ASC, s.name COLLATE "C" ASC, s.path COLLATE "C" ASC,
	s.kind COLLATE "C" ASC`, repoID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select repo map candidates: %w", err)
	}
	out := make([]domain.MapCandidate, len(rows))
	for i, r := range rows {
		out[i] = domain.MapCandidate{
			Path: r.Path, Name: r.Name, Exported: r.Exported, Line: deref(r.Line), Signature: r.Signature,
			Rank: r.Rank,
		}
	}
	return out, nil
}

// CachedMap returns the repo_map_cache row for (repo, sha, budget); ok=false on a miss.
func (s *Store) CachedMap(ctx context.Context, repoID, sha string, budget int) (domain.RepoMap, bool, error) {
	var row mapRow
	err := s.conn(ctx).Where("repo_id = ? AND commit_sha = ? AND token_budget = ?", repoID, sha, budget).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.RepoMap{}, false, nil
	}
	if err != nil {
		return domain.RepoMap{}, false, fmt.Errorf("select repo_map_cache: %w", err)
	}
	return domain.RepoMap{CommitSHA: row.CommitSHA, Budget: row.TokenBudget, Text: row.MapText, Tokens: row.TokenCount}, true, nil
}

// FileRanks returns the percentile of each of paths that has a file_rank row (any order).
func (s *Store) FileRanks(ctx context.Context, repoID string, paths []string) ([]domain.PathPercentile, error) {
	var out []domain.PathPercentile
	for part := range slices.Chunk(paths, deleteChunk) {
		var rows []rankRow
		err := s.conn(ctx).Where("repo_id = ? AND file_path IN ?", repoID, part).Find(&rows).Error
		if err != nil {
			return nil, fmt.Errorf("select file_rank: %w", err)
		}
		for _, r := range rows {
			out = append(out, domain.PathPercentile{Path: r.FilePath, Percentile: r.Percentile})
		}
	}
	return out, nil
}

// symbolInfoRow is a scan target of SymbolsInFiles.
type symbolInfoRow struct {
	Path      string  `gorm:"column:path"`
	Name      string  `gorm:"column:name"`
	Kind      string  `gorm:"column:kind"`
	Exported  bool    `gorm:"column:exported"`
	Line      *int    `gorm:"column:line"`
	EndLine   *int    `gorm:"column:end_line"`
	Signature *string `gorm:"column:signature"`
}

// SymbolsInFiles returns the symbols of paths ordered by path, line (NULL last), name, all in
// byte order. EndLine falls back to the line, then to 0.
func (s *Store) SymbolsInFiles(ctx context.Context, repoID string, paths []string) ([]domain.SymbolInfo, error) {
	var rows []symbolInfoRow
	for part := range slices.Chunk(paths, deleteChunk) {
		var chunk []symbolInfoRow
		err := s.conn(ctx).Raw(`
SELECT path, name, kind, exported, line, end_line, signature
FROM symbols
WHERE repo_id = ? AND path IN ?
ORDER BY path COLLATE "C" ASC, line ASC, name COLLATE "C" ASC, kind COLLATE "C" ASC`, repoID, part).
			Scan(&chunk).Error
		if err != nil {
			return nil, fmt.Errorf("select symbols of files: %w", err)
		}
		rows = append(rows, chunk...)
	}
	if len(paths) > deleteChunk { // chunks arrive one after the other: restore the global order
		slices.SortStableFunc(rows, compareSymbolInfo)
	}
	out := make([]domain.SymbolInfo, len(rows))
	for i, r := range rows {
		end := deref(r.EndLine)
		if r.EndLine == nil {
			end = deref(r.Line)
		}
		out[i] = domain.SymbolInfo{
			File: r.Path, Name: r.Name, Kind: r.Kind, Exported: r.Exported, StartLine: deref(r.Line),
			EndLine: end, Signature: r.Signature,
		}
	}
	return out, nil
}

// compareSymbolInfo is the Go form of the ORDER BY of SymbolsInFiles.
func compareSymbolInfo(a, b symbolInfoRow) int {
	if c := strings.Compare(a.Path, b.Path); c != 0 {
		return c
	}
	switch {
	case a.Line == nil && b.Line != nil:
		return 1
	case a.Line != nil && b.Line == nil:
		return -1
	case a.Line != nil && *a.Line != *b.Line:
		return *a.Line - *b.Line
	}
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	return strings.Compare(a.Kind, b.Kind)
}

// RankedPaths returns the top limit file_rank rows by rank DESC, then path ASC (byte order).
func (s *Store) RankedPaths(ctx context.Context, repoID string, limit int) ([]domain.RankedPath, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []rankRow
	err := s.conn(ctx).Where("repo_id = ?", repoID).
		Order(`rank DESC, file_path COLLATE "C" ASC`).Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select ranked paths: %w", err)
	}
	out := make([]domain.RankedPath, len(rows))
	for i, r := range rows {
		out[i] = domain.RankedPath{Path: r.FilePath, Rank: r.Rank}
	}
	return out, nil
}

// Edges returns every file_edges row of the repo ordered by from, then to (byte order).
func (s *Store) Edges(ctx context.Context, repoID string) ([]domain.Edge, error) {
	var rows []edgeRow
	err := s.conn(ctx).Where("repo_id = ?", repoID).
		Order(`from_file COLLATE "C" ASC, to_file COLLATE "C" ASC`).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select file_edges: %w", err)
	}
	out := make([]domain.Edge, len(rows))
	for i, r := range rows {
		out[i] = domain.Edge{From: r.FromFile, To: r.ToFile}
	}
	return out, nil
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
