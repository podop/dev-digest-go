package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/spend/app"
	"dev-digest-go/backend/internal/spend/domain"
)

type ledgerCall struct {
	WorkspaceID string
	From, To    time.Time
}

type fakeLedger struct {
	spent domain.Micros
	err   error
	calls []ledgerCall
}

func (f *fakeLedger) MonthSpend(_ context.Context, ws string, from, to time.Time) (domain.Micros, error) {
	f.calls = append(f.calls, ledgerCall{ws, from, to})
	return f.spent, f.err
}

type fakeCaps struct {
	limit *domain.Micros
	err   error
}

func (f fakeCaps) Cap(context.Context, string) (*domain.Micros, error) { return f.limit, f.err }

func micros(v domain.Micros) *domain.Micros { return &v }

var (
	october   = time.Date(2026, 10, 24, 13, 5, 0, 0, time.UTC)
	monthFrom = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	monthTo   = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
)

// newService builds a Service whose log lines go to the returned buffer, as JSON.
func newService(ledger *fakeLedger, caps fakeCaps) (*app.Service, *bytes.Buffer) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return app.NewService(ledger, caps, func() time.Time { return october }, log), &buf
}

func TestService_Status(t *testing.T) {
	tests := []struct {
		name  string
		spent domain.Micros
		limit *domain.Micros
		want  domain.Status
	}{
		{"no cap", 12_340_000, nil, domain.Status{MonthStart: monthFrom, Spent: 12_340_000}},
		{"under the cap", 12_340_000, micros(50_000_000), domain.Status{MonthStart: monthFrom, Spent: 12_340_000, Cap: micros(50_000_000)}},
		{"at the cap", 50_000_000, micros(50_000_000), domain.Status{MonthStart: monthFrom, Spent: 50_000_000, Cap: micros(50_000_000)}},
		{"over the cap", 60_000_000, micros(50_000_000), domain.Status{MonthStart: monthFrom, Spent: 60_000_000, Cap: micros(50_000_000)}},
		{"cap 0", 0, micros(0), domain.Status{MonthStart: monthFrom, Cap: micros(0)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &fakeLedger{spent: tc.spent}
			svc, _ := newService(ledger, fakeCaps{limit: tc.limit})

			got, err := svc.Status(t.Context(), "ws-1")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Status() mismatch (-want +got):\n%s", diff)
			}
			wantCalls := []ledgerCall{{"ws-1", monthFrom, monthTo}}
			if diff := cmp.Diff(wantCalls, ledger.calls); diff != "" {
				t.Errorf("MonthSpend calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestService_Status_MonthBoundsUseInjectedClock(t *testing.T) {
	ledger := &fakeLedger{}
	now := time.Date(2026, 12, 31, 23, 59, 59, 999_000_000, time.UTC)
	svc := app.NewService(ledger, fakeCaps{}, func() time.Time { return now }, slog.New(slog.DiscardHandler))

	got, err := svc.Status(t.Context(), "ws-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	wantFrom := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.MonthStart.Equal(wantFrom) {
		t.Errorf("Status().MonthStart = %v, want %v", got.MonthStart, wantFrom)
	}
	if diff := cmp.Diff([]ledgerCall{{"ws-1", wantFrom, wantTo}}, ledger.calls); diff != "" {
		t.Errorf("MonthSpend calls mismatch (-want +got):\n%s", diff)
	}
}

func TestService_Status_ReadErrorsAreWrapped(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name   string
		ledger *fakeLedger
		caps   fakeCaps
	}{
		{"ledger fails", &fakeLedger{err: boom}, fakeCaps{limit: micros(1)}},
		{"cap fails", &fakeLedger{}, fakeCaps{err: boom}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(tc.ledger, tc.caps)
			_, err := svc.Status(t.Context(), "ws-1")
			if !errors.Is(err, boom) {
				t.Fatalf("Status() error = %v, want it to wrap %v", err, boom)
			}
			var ae *apperr.Error
			if errors.As(err, &ae) {
				t.Errorf("Status() error is an *apperr.Error (%v); a read failure must map to 500", ae)
			}
		})
	}
}

func TestService_Admit_Allowed(t *testing.T) {
	tests := []struct {
		name  string
		spent domain.Micros
		limit *domain.Micros
	}{
		{"no cap", 999_000_000, nil},
		{"under the cap", 49_990_000, micros(50_000_000)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, logs := newService(&fakeLedger{spent: tc.spent}, fakeCaps{limit: tc.limit})
			if err := svc.Admit(t.Context(), "ws-1", app.OpBriefGenerate); err != nil {
				t.Errorf("Admit() = %v, want nil", err)
			}
			if logs.Len() != 0 {
				t.Errorf("Admit() logged %q, want no log line when admitted", logs.String())
			}
		})
	}
}

func TestService_Admit_Refused(t *testing.T) {
	tests := []struct {
		name  string
		spent domain.Micros
		limit domain.Micros
	}{
		{"at the cap", 50_000_000, 50_000_000},
		{"over the cap", 50_120_000, 50_000_000},
		{"cap 0", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, logs := newService(&fakeLedger{spent: tc.spent}, fakeCaps{limit: &tc.limit})

			err := svc.Admit(t.Context(), "ws-1", app.OpReviewStart)

			var ae *apperr.Error
			if !errors.As(err, &ae) {
				t.Fatalf("Admit() = %v, want *apperr.Error", err)
			}
			if ae.Kind != apperr.KindPaymentRequired || ae.Code != app.CodeCapReached {
				t.Errorf("Admit() kind, code = %v, %q, want payment_required, %q", ae.Kind, ae.Code, app.CodeCapReached)
			}
			wantDetails := map[string]any{
				"cap_usd":     tc.limit.USD(),
				"spent_usd":   tc.spent.USD(),
				"month_start": "2026-10-01T00:00:00.000Z",
			}
			if diff := cmp.Diff(wantDetails, ae.Details); diff != "" {
				t.Errorf("Admit() details mismatch (-want +got):\n%s", diff)
			}

			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("Admit() wrote %d log lines %q, want exactly 1", len(lines), logs.String())
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
				t.Fatalf("decode log line %q: %v", lines[0], err)
			}
			if rec["level"] != "INFO" || rec["op"] != app.OpReviewStart || rec["workspace_id"] != "ws-1" ||
				rec["cap_usd"] != tc.limit.USD() || rec["spent_usd"] != tc.spent.USD() {
				t.Errorf("log line = %v, want INFO with op %q, workspace ws-1, cap %v, spent %v",
					rec, app.OpReviewStart, tc.limit.USD(), tc.spent.USD())
			}
		})
	}
}

func TestService_Admit_FailsClosedOnReadError(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name   string
		ledger *fakeLedger
		caps   fakeCaps
	}{
		{"ledger fails", &fakeLedger{err: boom}, fakeCaps{limit: micros(50_000_000)}},
		{"ledger fails without a cap", &fakeLedger{err: boom}, fakeCaps{}},
		{"cap fails", &fakeLedger{}, fakeCaps{err: boom}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, logs := newService(tc.ledger, tc.caps)

			err := svc.Admit(t.Context(), "ws-1", app.OpBriefGenerate)

			if !errors.Is(err, boom) {
				t.Fatalf("Admit() = %v, want an error wrapping %v", err, boom)
			}
			var ae *apperr.Error
			if errors.As(err, &ae) {
				t.Errorf("Admit() error is an *apperr.Error (%v); a read failure must map to 500", ae)
			}
			if logs.Len() != 0 {
				t.Errorf("Admit() logged %q on a read error, want no 402 log line", logs.String())
			}
		})
	}
}
