package app

import (
	"context"
	"fmt"

	"dev-digest-go/backend/internal/autoreview/domain"
)

// Status derives the status line from the stored facts (FR12). The spend cap is read first and
// through Caps, which logs nothing: no cap means not paused (A9). Selected agents that no longer
// exist or are disabled do not count (EC9).
func (s *Service) Status(ctx context.Context, workspaceID string) (domain.Status, error) {
	d := s.deps
	capReached, err := d.Caps.CapReached(ctx, workspaceID)
	if err != nil {
		return domain.Status{}, fmt.Errorf("status: read spend cap: %w", err)
	}
	setting, err := d.Settings.AutomaticReviews(ctx, workspaceID)
	if err != nil {
		return domain.Status{}, fmt.Errorf("status: read setting: %w", err)
	}
	interval, err := d.Interval.Interval(ctx, workspaceID)
	if err != nil {
		return domain.Status{}, fmt.Errorf("status: read poll interval: %w", err)
	}
	facts, err := d.Store.StatusFacts(ctx, workspaceID)
	if err != nil {
		return domain.Status{}, fmt.Errorf("status: read poll facts: %w", err)
	}
	hasToken, err := d.Tokens.HasGitHubToken(ctx, workspaceID)
	if err != nil {
		return domain.Status{}, fmt.Errorf("status: read github token: %w", err)
	}
	agents := 0
	if len(setting.AgentIDs) > 0 {
		enabled, err := d.Store.EnabledAgents(ctx, workspaceID, setting.AgentIDs)
		if err != nil {
			return domain.Status{}, fmt.Errorf("status: read agents: %w", err)
		}
		agents = len(domain.EffectiveAgents(setting.AgentIDs, enabled))
	}
	state := domain.StateOf(domain.Facts{
		Enabled:         setting.Enabled,
		PollEnabled:     d.PollEnabled,
		HasGitHubToken:  hasToken,
		EffectiveAgents: agents,
		OnNewPR:         setting.OnNewPR,
		OnNewCommits:    setting.OnNewCommits,
		CapReached:      capReached,
	})
	return domain.NewStatus(state, facts.LastPollAt, facts.PRsTracked, interval), nil
}
