package app

import (
	"context"
	"fmt"
	"log/slog"

	"dev-digest-go/backend/internal/spend/domain"
)

// Service implements API over the ledger and the cap provider.
type Service struct {
	ledger Ledger
	caps   Caps
	now    Clock
	log    *slog.Logger
}

var _ API = (*Service)(nil)

// NewService builds the service. now is the clock that decides which month is current.
func NewService(ledger Ledger, caps Caps, now Clock, log *slog.Logger) *Service {
	return &Service{ledger: ledger, caps: caps, now: now, log: log}
}

// Status reads the cap and the current UTC month's spend. Both reads must succeed: a status
// computed from half of the data could let work through (D16).
func (s *Service) Status(ctx context.Context, workspaceID string) (domain.Status, error) {
	limit, err := s.caps.Cap(ctx, workspaceID)
	if err != nil {
		return domain.Status{}, fmt.Errorf("read spend cap: %w", err)
	}
	return s.status(ctx, workspaceID, limit)
}

// status adds the current month's spend to an already read cap.
func (s *Service) status(ctx context.Context, workspaceID string, limit *domain.Micros) (domain.Status, error) {
	now := s.now()
	spent, err := s.ledger.MonthSpend(ctx, workspaceID, domain.MonthStart(now), domain.NextMonthStart(now))
	if err != nil {
		return domain.Status{}, fmt.Errorf("read month spend: %w", err)
	}
	return domain.Status{MonthStart: domain.MonthStart(now), Spent: spent, Cap: limit}, nil
}

// Admit refuses new work once the cap is reached: it logs one INFO line (operation, workspace,
// cap, spent; no secrets) and returns the 402 error. A read error fails closed. It reads the cap
// first and, when none is set, admits without reading the spend: a missing cap can never be
// reached, and the month-spend query is the expensive read (D24).
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
