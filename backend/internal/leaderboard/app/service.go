package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/leaderboard/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Service implements API over a Reader and a clock.
type Service struct {
	reader Reader
	now    Clock
}

var _ API = (*Service)(nil)

// NewService builds the leaderboard use case.
func NewService(reader Reader, now Clock) *Service {
	return &Service{reader: reader, now: now}
}

// Leaderboard implements API. It only reads (NFR2): the repositories of the filter and one fact
// read over [ReadFrom, To) that serves both windows; then the pure folds of the domain. A scope
// without repositories skips the fact read and folds empty facts, which yields zeroed KPIs, no
// rows and the window's activity points with zero counts. The previous summary is nil for a
// window without a previous one (30d) and for a previous window without rows (FR11).
func (s *Service) Leaderboard(ctx context.Context, workspaceID string, q domain.Query) (domain.Leaderboard, error) {
	now := s.now()
	win := domain.ResolveWindow(q.Period, q.Location, now)

	repos, err := s.reader.RepoScope(ctx, workspaceID, q.Repo)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Leaderboard{}, apperr.NotFound("Repo not found")
	}
	if err != nil {
		return domain.Leaderboard{}, fmt.Errorf("read repository scope: %w", err)
	}

	var facts domain.Facts
	if len(repos) > 0 {
		ids := make([]string, len(repos))
		for i := range repos {
			ids[i] = repos[i].ID
		}
		if facts, err = s.reader.WindowFacts(ctx, workspaceID, ids, win); err != nil {
			return domain.Leaderboard{}, fmt.Errorf("read window facts: %w", err)
		}
	}

	cur, prev := domain.BuildRows(win, facts, now)
	domain.Rank(cur)
	domain.Rank(prev)
	var previous *domain.KpiSet
	if len(prev) > 0 {
		k := domain.Kpis(win.Prev(), prev, facts)
		previous = &k
	}
	total, items := domain.Attention(win.Cur(), cur, facts.Pulls, now)
	return domain.Leaderboard{
		Window:          win,
		Repos:           repos,
		Summary:         domain.Kpis(win.Cur(), cur, facts),
		PreviousSummary: previous,
		Awards:          domain.Awards(win.Cur(), cur),
		Rows:            cur,
		NeedsAttention:  domain.AttentionList{Total: total, Items: items},
		HotFiles:        domain.HotFiles(facts.HotFiles),
		HealthBySize:    domain.HealthBySize(cur),
		Activity:        domain.Activity(win, cur, facts.Pulls),
	}, nil
}
