package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/spend/app"
	"dev-digest-go/backend/internal/spend/domain"
)

func TestCapReached(t *testing.T) {
	limit := domain.Micros(50_000_000)
	err := app.CapReached(domain.Status{
		MonthStart: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Spent:      50_120_000,
		Cap:        &limit,
	})

	var ae *apperr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("CapReached() = %T, want *apperr.Error", err)
	}
	if ae.Kind != apperr.KindPaymentRequired || ae.Code != "spend_cap_reached" {
		t.Errorf("CapReached() kind, code = %v, %q, want payment_required, spend_cap_reached", ae.Kind, ae.Code)
	}
	if want := "Monthly spend cap of $50.00 reached: $50.12 used since 2026-10-01 (UTC)."; ae.Message != want {
		t.Errorf("CapReached().Message = %q, want %q", ae.Message, want)
	}
	want := map[string]any{"cap_usd": 50.0, "spent_usd": 50.12, "month_start": "2026-10-01T00:00:00.000Z"}
	if diff := cmp.Diff(want, ae.Details); diff != "" {
		t.Errorf("CapReached().Details mismatch (-want +got):\n%s", diff)
	}
}
