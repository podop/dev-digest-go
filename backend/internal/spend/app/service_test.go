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

type totalsCall struct {
	WorkspaceID string
	Month       time.Time
}

type fakeTotals struct {
	total domain.MonthTotal
	err   error
	calls []totalsCall
}

func (f *fakeTotals) MonthTotal(_ context.Context, ws string, month time.Time) (domain.MonthTotal, error) {
	f.calls = append(f.calls, totalsCall{ws, month})
	return f.total, f.err
}

// forbiddenTotals fails the test when the month total is read: Admit must not read it without a cap (D24).
type forbiddenTotals struct{ t *testing.T }

func (f forbiddenTotals) MonthTotal(context.Context, string, time.Time) (domain.MonthTotal, error) {
	f.t.Error("MonthTotal was called, want no total read when no cap is set")
	return domain.MonthTotal{}, errors.New("unexpected MonthTotal call")
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
)

// newService builds a Service whose log lines go to the returned buffer, as JSON.
func newService(totals *fakeTotals, caps fakeCaps) (*app.Service, *bytes.Buffer) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return app.NewService(totals, caps, func() time.Time { return october }, log), &buf
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
			totals := &fakeTotals{total: domain.MonthTotal{Spent: tc.spent}}
			svc, _ := newService(totals, fakeCaps{limit: tc.limit})

			got, err := svc.Status(t.Context(), "ws-1")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Status() mismatch (-want +got):\n%s", diff)
			}
			wantCalls := []totalsCall{{"ws-1", monthFrom}}
			if diff := cmp.Diff(wantCalls, totals.calls); diff != "" {
				t.Errorf("MonthTotal calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestService_Status_MonthBoundsUseInjectedClock(t *testing.T) {
	totals := &fakeTotals{}
	now := time.Date(2026, 12, 31, 23, 59, 59, 999_000_000, time.UTC)
	svc := app.NewService(totals, fakeCaps{}, func() time.Time { return now }, slog.New(slog.DiscardHandler))

	got, err := svc.Status(t.Context(), "ws-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	wantFrom := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if !got.MonthStart.Equal(wantFrom) {
		t.Errorf("Status().MonthStart = %v, want %v", got.MonthStart, wantFrom)
	}
	if diff := cmp.Diff([]totalsCall{{"ws-1", wantFrom}}, totals.calls); diff != "" {
		t.Errorf("MonthTotal calls mismatch (-want +got):\n%s", diff)
	}
}

func TestService_Status_ReadErrorsAreWrapped(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name   string
		totals *fakeTotals
		caps   fakeCaps
	}{
		{"totals fail", &fakeTotals{err: boom}, fakeCaps{limit: micros(1)}},
		{"cap fails", &fakeTotals{}, fakeCaps{err: boom}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(tc.totals, tc.caps)
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
			svc, logs := newService(&fakeTotals{total: domain.MonthTotal{Spent: tc.spent}}, fakeCaps{limit: tc.limit})
			if err := svc.Admit(t.Context(), "ws-1", app.OpBriefGenerate); err != nil {
				t.Errorf("Admit() = %v, want nil", err)
			}
			if logs.Len() != 0 {
				t.Errorf("Admit() logged %q, want no log line when admitted", logs.String())
			}
		})
	}
}

func TestService_Admit_NoCapSkipsTheSpendRead(t *testing.T) {
	var buf bytes.Buffer
	svc := app.NewService(forbiddenTotals{t}, fakeCaps{}, func() time.Time { return october }, slog.New(slog.NewJSONHandler(&buf, nil)))

	if err := svc.Admit(t.Context(), "ws-1", app.OpReviewStart); err != nil {
		t.Errorf("Admit() without a cap = %v, want nil", err)
	}
	if buf.Len() != 0 {
		t.Errorf("Admit() logged %q, want no log line when admitted", buf.String())
	}
}

func TestService_Status_NoCapStillReadsTheSpend(t *testing.T) {
	totals := &fakeTotals{total: domain.MonthTotal{Spent: 12_340_000}}
	svc, _ := newService(totals, fakeCaps{})

	got, err := svc.Status(t.Context(), "ws-1")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Spent != 12_340_000 || len(totals.calls) != 1 {
		t.Errorf("Status() spent = %d after %d total reads, want 12340000 after 1 (the Settings screen shows it)", got.Spent, len(totals.calls))
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
			svc, logs := newService(&fakeTotals{total: domain.MonthTotal{Spent: tc.spent}}, fakeCaps{limit: &tc.limit})

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
		totals *fakeTotals
		caps   fakeCaps
	}{
		{"totals fail", &fakeTotals{err: boom}, fakeCaps{limit: micros(50_000_000)}},
		{"cap fails", &fakeTotals{}, fakeCaps{err: boom}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, logs := newService(tc.totals, tc.caps)

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

func TestService_Status_ReportsTheUnpricedCount(t *testing.T) {
	tests := []struct {
		name  string
		total domain.MonthTotal
		want  domain.Status
	}{
		{"spend and unpriced", domain.MonthTotal{Spent: 12_340_000, Unpriced: 3},
			domain.Status{MonthStart: monthFrom, Spent: 12_340_000, Unpriced: 3, Cap: micros(50_000_000)}},
		{"only unpriced entries", domain.MonthTotal{Unpriced: 2},
			domain.Status{MonthStart: monthFrom, Unpriced: 2, Cap: micros(50_000_000)}},
		{"no entry this month (EC19)", domain.MonthTotal{},
			domain.Status{MonthStart: monthFrom, Cap: micros(50_000_000)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(&fakeTotals{total: tc.total}, fakeCaps{limit: micros(50_000_000)})

			got, err := svc.Status(t.Context(), "ws-1")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Status() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Unknown-cost entries never block (FR8): with a huge unpriced count and a spend under the cap,
// Admit lets the work through.
func TestService_Admit_UnpricedEntriesNeverBlock(t *testing.T) {
	svc, logs := newService(&fakeTotals{total: domain.MonthTotal{Spent: 1_000_000, Unpriced: 100_000}}, fakeCaps{limit: micros(50_000_000)})

	if err := svc.Admit(t.Context(), "ws-1", app.OpReviewStart); err != nil {
		t.Errorf("Admit() = %v, want nil", err)
	}
	if logs.Len() != 0 {
		t.Errorf("Admit() logged %q, want no log line when admitted", logs.String())
	}
}

// Admit reads the stored total once, for the workspace asked about and the month of the clock.
func TestService_Admit_ReadsTheStoredTotalOnce(t *testing.T) {
	totals := &fakeTotals{total: domain.MonthTotal{Spent: 1}}
	svc, _ := newService(totals, fakeCaps{limit: micros(50_000_000)})

	if err := svc.Admit(t.Context(), "ws-9", app.OpReviewStart); err != nil {
		t.Fatalf("Admit() = %v, want nil", err)
	}
	if diff := cmp.Diff([]totalsCall{{"ws-9", monthFrom}}, totals.calls); diff != "" {
		t.Errorf("MonthTotal calls mismatch (-want +got):\n%s", diff)
	}
}
