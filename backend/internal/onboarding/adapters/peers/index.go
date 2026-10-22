package peers

import (
	"context"

	"dev-digest-go/backend/internal/onboarding/app"
	"dev-digest-go/backend/internal/onboarding/domain"
	repointel "dev-digest-go/backend/internal/repointel/domain"
)

// StateReader is the part of *repointel/app.Service the adapter uses.
type StateReader interface {
	// IndexState never fails: a missing or unreadable state is the synthesised degraded one.
	IndexState(ctx context.Context, repoID string) repointel.StateView
}

// IndexStates implements onboarding/app.IndexStates over the repo-intel service.
type IndexStates struct{ s StateReader }

var _ app.IndexStates = (*IndexStates)(nil)

// NewIndexStates wraps the repo-intel service.
func NewIndexStates(s StateReader) *IndexStates { return &IndexStates{s: s} }

// IndexState returns the files indexed and the last indexed commit.
func (a *IndexStates) IndexState(ctx context.Context, repoID string) domain.IndexState {
	v := a.s.IndexState(ctx, repoID)
	return domain.IndexState{FilesIndexed: v.FilesIndexed, LastIndexedSHA: v.LastIndexedSha}
}

// IndexReader is the part of *repointel/app.Reader the adapter uses.
type IndexReader interface {
	RepoMap(ctx context.Context, repoID string, budget int) (repointel.RepoMapResult, error)
	TopFilesByRank(ctx context.Context, repoID string, n int, exclude []string) ([]string, error)
	CriticalPaths(ctx context.Context, repoID string) ([][]string, error)
	ListIndexedFiles(ctx context.Context, repoID string) ([]string, error)
}

// Index implements onboarding/app.Index over the repo-intel read port.
type Index struct{ r IndexReader }

var _ app.Index = (*Index)(nil)

// NewIndex wraps the repo-intel reader.
func NewIndex(r IndexReader) *Index { return &Index{r: r} }

// RepoMap returns the cached map text; "" when the map is degraded (flag off, no data).
func (a *Index) RepoMap(ctx context.Context, repoID string, budget int) (string, error) {
	m, err := a.r.RepoMap(ctx, repoID, budget)
	if err != nil {
		return "", err
	}
	if m.Degraded {
		return "", nil
	}
	return m.Text, nil
}

// TopFilesByRank returns the n best-ranked paths with the reader's default exclusions only.
func (a *Index) TopFilesByRank(ctx context.Context, repoID string, n int) ([]string, error) {
	return a.r.TopFilesByRank(ctx, repoID, n, nil)
}

// CriticalPaths returns the dependency chains from the most central files.
func (a *Index) CriticalPaths(ctx context.Context, repoID string) ([][]string, error) {
	return a.r.CriticalPaths(ctx, repoID)
}

// ListIndexedFiles returns every indexed path.
func (a *Index) ListIndexedFiles(ctx context.Context, repoID string) ([]string, error) {
	return a.r.ListIndexedFiles(ctx, repoID)
}
