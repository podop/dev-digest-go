package peers

import (
	"context"

	repointel "dev-digest-go/backend/internal/repointel/domain"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// IntelReader is the part of *repointel/app.Reader the adapter uses.
type IntelReader interface {
	CallerSignatures(ctx context.Context, repoID string, changed []string, limit int) ([]repointel.CallerSignature, error)
	RepoMap(ctx context.Context, repoID string, budget int) (repointel.RepoMapResult, error)
	FileRank(ctx context.Context, repoID string, paths []string) ([]repointel.PathPercentile, error)
}

// RepoIntel implements reviews/app.RepoIntel over the repo-intel read port.
type RepoIntel struct{ r IntelReader }

var _ reviewsapp.RepoIntel = (*RepoIntel)(nil)

// NewRepoIntel wraps the repo-intel reader.
func NewRepoIntel(r IntelReader) *RepoIntel { return &RepoIntel{r: r} }

// CallerSignatures returns the caller rows of the changed files (the rank is dropped: the reader
// already ordered by it).
func (a *RepoIntel) CallerSignatures(ctx context.Context, repoID string, changed []string, limit int) ([]domain.CallerSignature, error) {
	rows, err := a.r.CallerSignatures(ctx, repoID, changed, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.CallerSignature, len(rows))
	for i, r := range rows {
		out[i] = domain.CallerSignature{File: r.File, Symbol: r.Symbol, Signature: r.Signature}
	}
	return out, nil
}

// RepoMap returns the cached repo map for the token budget.
func (a *RepoIntel) RepoMap(ctx context.Context, repoID string, budget int) (domain.RepoMap, error) {
	m, err := a.r.RepoMap(ctx, repoID, budget)
	if err != nil {
		return domain.RepoMap{}, err
	}
	return domain.RepoMap{Text: m.Text, Tokens: m.Tokens, Cached: m.Cached, Degraded: m.Degraded}, nil
}

// FileRank returns the percentile of each path that has a rank.
func (a *RepoIntel) FileRank(ctx context.Context, repoID string, paths []string) ([]domain.PathRank, error) {
	rows, err := a.r.FileRank(ctx, repoID, paths)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PathRank, len(rows))
	for i, r := range rows {
		out[i] = domain.PathRank{Path: r.Path, Percentile: r.Percentile}
	}
	return out, nil
}
