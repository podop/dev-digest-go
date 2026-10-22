// Package peers adapts what other contexts and the platform answer to the ports of the blast
// context: the repo-intel BlastRadius read and the GitHub merged-pull-request read. Their
// vocabularies are mapped one to one, so the domain never imports either.
package peers

import (
	"context"

	"dev-digest-go/backend/internal/blast/app"
	"dev-digest-go/backend/internal/blast/domain"
	repointel "dev-digest-go/backend/internal/repointel/domain"
)

// IntelReader is the part of *repointel/app.Reader the adapter uses.
type IntelReader interface {
	BlastRadius(ctx context.Context, repoID string, changed []string) (repointel.BlastResult, error)
}

// RepoIntel implements app.RepoIntel over the repo-intel read port.
type RepoIntel struct{ r IntelReader }

var _ app.RepoIntel = (*RepoIntel)(nil)

// NewRepoIntel wraps the repo-intel reader.
func NewRepoIntel(r IntelReader) *RepoIntel { return &RepoIntel{r: r} }

// BlastRadius maps the reader's result to the blast vocabulary.
func (a *RepoIntel) BlastRadius(ctx context.Context, repoID string, changed []string) (domain.PortResult, error) {
	res, err := a.r.BlastRadius(ctx, repoID, changed)
	if err != nil {
		return domain.PortResult{}, err
	}
	out := domain.PortResult{
		ChangedSymbols: make([]domain.ChangedSymbol, len(res.ChangedSymbols)),
		Callers:        make([]domain.Caller, len(res.Callers)),
		Degraded:       res.Degraded,
		Reason:         domain.Reason(res.Reason),
	}
	for i, s := range res.ChangedSymbols {
		out.ChangedSymbols[i] = domain.ChangedSymbol{Name: s.Name, File: s.File, Kind: s.Kind}
	}
	for i, c := range res.Callers {
		out.Callers[i] = domain.Caller{File: c.File, Symbol: c.Symbol, ViaSymbol: c.ViaSymbol, Line: c.Line, Rank: c.Rank}
	}
	if res.FactsByFile != nil {
		out.FactsByFile = make(map[string]domain.Facts, len(res.FactsByFile))
		for file, f := range res.FactsByFile {
			out.FactsByFile[file] = domain.Facts{Endpoints: f.Endpoints, Crons: f.Crons}
		}
	}
	return out, nil
}
