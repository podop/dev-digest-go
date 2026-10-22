package app

import (
	"context"
	"fmt"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/rank"
)

// readerSource is what the read port needs from persistence.
type readerSource interface {
	stateReader
	blastSource
	CachedMap(ctx context.Context, repoID, sha string, budget int) (m domain.RepoMap, ok bool, err error)
	FileRanks(ctx context.Context, repoID string, paths []string) ([]domain.PathPercentile, error)
	SymbolsInFiles(ctx context.Context, repoID string, paths []string) ([]domain.SymbolInfo, error)
	RankedPaths(ctx context.Context, repoID string, limit int) ([]domain.RankedPath, error)
	Edges(ctx context.Context, repoID string) ([]domain.Edge, error)
}

// Reader is the in-process read port over the index (spec section 7.3). Later features (the
// conventions sampler, reviews, blast, onboarding) read the index through it and never parse a
// clone at request time. With the feature flag off every operation answers empty; a missing or
// degraded index is never an error, a database error is returned.
type Reader struct {
	src     readerSource
	enabled bool
	scan    *CallerScan
}

// NewReader builds the read port; enabled is REPO_INTEL_ENABLED. Options add the operations that
// read the clone (WithCallerScan).
func NewReader(src readerSource, enabled bool, opts ...ReaderOption) *Reader {
	r := &Reader{src: src, enabled: enabled}
	for _, o := range opts {
		o(r)
	}
	return r
}

// RepoMap returns the cached repo map of the last indexed commit for the token budget (zero or
// less: domain.DefaultTokenBudget). With the flag off it is empty with reason flag_off; without
// a state, a sha or a cached map for that budget it is empty with reason no_data.
func (r *Reader) RepoMap(ctx context.Context, repoID string, budget int) (domain.RepoMapResult, error) {
	if !r.enabled {
		return domain.RepoMapResult{Degraded: true, Reason: domain.ReasonFlagOff}, nil
	}
	noData := domain.RepoMapResult{Degraded: true, Reason: domain.ReasonNoData}
	st, ok, err := r.src.GetState(ctx, repoID)
	if err != nil {
		return domain.RepoMapResult{}, fmt.Errorf("read index state: %w", err)
	}
	if !ok || st.LastIndexedSHA == "" {
		return noData, nil
	}
	if budget <= 0 {
		budget = domain.DefaultTokenBudget
	}
	m, ok, err := r.src.CachedMap(ctx, repoID, st.LastIndexedSHA, budget)
	if err != nil {
		return domain.RepoMapResult{}, fmt.Errorf("read repo map: %w", err)
	}
	if !ok {
		return noData, nil
	}
	return domain.RepoMapResult{Text: m.Text, Tokens: m.Tokens, Cached: true}, nil
}

// FileRank returns the percentile of each of paths that has a file_rank row.
func (r *Reader) FileRank(ctx context.Context, repoID string, paths []string) ([]domain.PathPercentile, error) {
	if !r.enabled || len(paths) == 0 {
		return []domain.PathPercentile{}, nil
	}
	rows, err := r.src.FileRanks(ctx, repoID, paths)
	if err != nil {
		return nil, fmt.Errorf("read file ranks: %w", err)
	}
	return nonNil(rows), nil
}

// SymbolsInFiles returns the indexed symbols of paths.
func (r *Reader) SymbolsInFiles(ctx context.Context, repoID string, paths []string) ([]domain.SymbolInfo, error) {
	if !r.enabled || len(paths) == 0 {
		return []domain.SymbolInfo{}, nil
	}
	rows, err := r.src.SymbolsInFiles(ctx, repoID, paths)
	if err != nil {
		return nil, fmt.Errorf("read symbols: %w", err)
	}
	return nonNil(rows), nil
}

// TopFilesByRank returns the n best-ranked source paths without junk (tests, configs,
// declarations, migrations, fixtures) and without a path containing any exclude substring.
func (r *Reader) TopFilesByRank(ctx context.Context, repoID string, n int, exclude []string) ([]string, error) {
	if !r.enabled || n <= 0 {
		return []string{}, nil
	}
	ranked, err := r.src.RankedPaths(ctx, repoID, rank.TopFilesScan(n))
	if err != nil {
		return nil, fmt.Errorf("read ranked paths: %w", err)
	}
	return rank.TopFiles(ranked, n, exclude), nil
}

// ConventionSamples is TopFilesByRank(repoID, n, nil): the conventions sampler's ranked picks.
// Without an index it returns an empty slice and no error, and the sampler falls back to its
// own walk.
func (r *Reader) ConventionSamples(ctx context.Context, repoID string, n int) ([]string, error) {
	return r.TopFilesByRank(ctx, repoID, n, nil)
}

// CriticalPaths returns dependency chains from the top-ranked files, for onboarding.
func (r *Reader) CriticalPaths(ctx context.Context, repoID string) ([][]string, error) {
	if !r.enabled {
		return [][]string{}, nil
	}
	edges, err := r.src.Edges(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("read edges: %w", err)
	}
	if len(edges) == 0 {
		return [][]string{}, nil
	}
	ranked, err := r.src.RankedPaths(ctx, repoID, domain.MaxListedFiles)
	if err != nil {
		return nil, fmt.Errorf("read ranked paths: %w", err)
	}
	return rank.CriticalPaths(edges, ranked), nil
}

// ListIndexedFiles returns every indexed path, best-ranked first, at most domain.MaxListedFiles.
func (r *Reader) ListIndexedFiles(ctx context.Context, repoID string) ([]string, error) {
	if !r.enabled {
		return []string{}, nil
	}
	ranked, err := r.src.RankedPaths(ctx, repoID, domain.MaxListedFiles)
	if err != nil {
		return nil, fmt.Errorf("read ranked paths: %w", err)
	}
	out := make([]string, len(ranked))
	for i, p := range ranked {
		out[i] = p.Path
	}
	return out, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
