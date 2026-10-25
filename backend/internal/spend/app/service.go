package app

import (
	"context"
	"fmt"
	"log/slog"

	"dev-digest-go/backend/internal/spend/domain"
)

// Service implements API over the stored month totals and the cap provider.
type Service struct {
	totals Totals
	caps   Caps
	now    Clock
	log    *slog.Logger
}

var _ API = (*Service)(nil)

// NewService builds the service. now is the clock that decides which month is current.
func NewService(totals Totals, caps Caps, now Clock, log *slog.Logger) *Service {
	return &Service{totals: totals, caps: caps, now: now, log: log}
}

// Status reads the cap and the current UTC month's stored total (spend and unknown-cost count). Both reads must succeed: a status
// computed from half of the data could let work through (D16).
func (s *Service) Status(ctx context.Context, workspaceID string) (domain.Status, error) {
	limit, err := s.caps.Cap(ctx, workspaceID)
	if err != nil {
		return domain.Status{}, fmt.Errorf("read spend cap: %w", err)
	}
	return s.status(ctx, workspaceID, limit)
}

// status adds the current month's stored total to an already read cap.
func (s *Service) status(ctx context.Context, workspaceID string, limit *domain.Micros) (domain.Status, error) {
	now := s.now()
	month := domain.MonthStart(now)
	total, err := s.totals.MonthTotal(ctx, workspaceID, month)
	if err != nil {
		return domain.Status{}, fmt.Errorf("read month total: %w", err)
	}
	return domain.Status{MonthStart: month, Spent: total.Spent, Cap: limit, Unpriced: total.Unpriced}, nil
}

// Admit refuses new work once the cap is reached: it logs one INFO line (operation, workspace,
// cap, spent; no secrets) and returns the 402 error. A read error fails closed. It reads the cap
// first and, when none is set, admits without reading the total: a missing cap can never be
// reached (D24). Unknown-cost entries never block (FR8).
func (s *Service) Admit(ctx context.Context, workspaceID, op string) error {
	limit, err := s.caps.Cap(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("admit %s: read spend cap: %w", op, err)
	}
	if limit == nil {
		return nil
	}
	st, err := s.status(ctx, workspaceID, limit)
	if err != nil {
		return fmt.Errorf("admit %s: %w", op, err)
	}
	if !st.Reached() {
		return nil
	}
	s.log.InfoContext(ctx, "spend cap reached: request refused",
		"op", op,
		"workspace_id", workspaceID,
		"cap_usd", st.Cap.USD(),
		"spent_usd", st.Spent.USD(),
		"month_start", st.MonthStart.Format(monthStartLayout),
	)
	return CapReached(st)
}
