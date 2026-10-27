package app

// The cache key of a derivation and the staleness check built on it (spec 7.4.1, 7.4.8). The key
// is computed before anything expensive happens: the feature model from the settings, the
// commits, and the pure link extraction.

import (
	"context"

	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
)

// derivation is a derivation's key and the material it was computed from.
type derivation struct {
	provider llm.Provider
	model    string
	commits  []domain.Commit
	tickets  domain.TicketLinks
	docs     domain.DocLinks
	hash     string
}

// computeKey computes the key of the pull request as it is now. Any failure (settings, commits, a
// malformed escape in a doc link) is returned as it is: the derivation is then unavailable with
// that message.
func (s *Service) computeKey(ctx context.Context, workspaceID string, pull domain.Pull, repo domain.Repo) (derivation, error) {
	bag, err := s.d.Settings.Get(ctx, workspaceID)
	if err != nil {
		return derivation{}, err
	}
	provider, model := llm.ResolveFeatureModel(bag, domain.FeatureModel)
	commits, err := s.d.Pulls.Commits(ctx, pull.ID)
	if err != nil {
		return derivation{}, err
	}
	texts := make([]string, 0, 2+len(commits))
	texts = append(texts, pull.Title, pull.Body)
	for _, c := range commits {
		texts = append(texts, c.Message)
	}
	// A GitLab repository has its own link forms and "same project" rule (spec B FR26, FR28); the
	// GitHub extraction never sees it.
	extractTickets, extractDocs := domain.ExtractTickets, domain.ExtractDocs
	if repo.IsGitLab() {
		extractTickets, extractDocs = domain.ExtractGitLabTickets, domain.ExtractGitLabDocs
	}
	tickets := extractTickets(texts, repo)
	docs, err := extractDocs([]string{pull.Title, pull.Body}, repo)
	if err != nil {
		return derivation{}, err
	}
	hash, err := domain.NewKeyInput(string(provider), model, pull, tickets.Refs, docs.Refs).Hash()
	if err != nil {
		return derivation{}, err
	}
	return derivation{provider: provider, model: model, commits: commits, tickets: tickets, docs: docs, hash: hash}, nil
}

// stale reports whether deriving now, with the current settings, pull request and commits, would
// give another key than the stored record's. A key that cannot be computed is not stale: the
// difference cannot be verified. Ticket and doc bodies are not part of the key.
func (s *Service) stale(ctx context.Context, workspaceID string, pull domain.Pull, repo domain.Repo, rec domain.Record) bool {
	key, err := s.computeKey(ctx, workspaceID, pull, repo)
	return err == nil && key.hash != rec.InputHash
}
