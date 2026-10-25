package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	ciapp "dev-digest-go/backend/internal/ci/app"
	evalspeers "dev-digest-go/backend/internal/evals/adapters/peers"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	"dev-digest-go/backend/internal/reviews/adapters/peers"
	spendapp "dev-digest-go/backend/internal/spend/app"
	spenddomain "dev-digest-go/backend/internal/spend/domain"
)

// entriesFake keeps what the recorder appends.
type entriesFake struct{ got []spenddomain.Entry }

func (f *entriesFake) Append(_ context.Context, e spenddomain.Entry) (bool, error) {
	f.got = append(f.got, e)
	return true, nil
}

func TestCILedger_MapsTheRunFieldByField(t *testing.T) { // FR6
	entries := &entriesFake{}
	ledger := ciLedger{recorder: spendapp.NewRecorder(entries, time.Now, slog.New(slog.DiscardHandler))}
	model := "anthropic/claude-sonnet-4.5"
	cost := 0.1234565
	ranAt := time.Date(2031, 1, 15, 12, 0, 0, 0, time.UTC)

	err := ledger.RecordCIRun(t.Context(), ciapp.CIRun{
		WorkspaceID: "11111111-1111-1111-1111-111111111111",
		AgentRunID:  "22222222-2222-2222-2222-222222222222",
		GithubRunID: 987,
		Attempt:     2,
		OccurredAt:  ranAt,
		Provider:    "openrouter",
		Model:       &model,
		CostUSD:     &cost,
	})
	if err != nil {
		t.Fatalf("RecordCIRun() error = %v, want nil", err)
	}

	want := []spenddomain.Entry{{
		WorkspaceID: "11111111-1111-1111-1111-111111111111",
		OccurredAt:  ranAt,
		Kind:        spenddomain.KindCIRun,
		Origin:      spenddomain.OriginCIIngest,
		Provider:    "openrouter",
		Model:       model,
		Cost:        spenddomain.CostFromUSD(&cost),
		RefID:       "22222222-2222-2222-2222-222222222222",
		DedupeKey:   spenddomain.CIDedupeKey(987, 2),
	}}
	if diff := cmp.Diff(want, entries.got); diff != "" {
		t.Errorf("RecordCIRun() appended entries mismatch (-want +got):\n%s", diff)
	}
}

func TestCILedger_UnknownCostAndModelAreKept(t *testing.T) { // FR6, EC
	entries := &entriesFake{}
	ledger := ciLedger{recorder: spendapp.NewRecorder(entries, time.Now, slog.New(slog.DiscardHandler))}

	err := ledger.RecordCIRun(t.Context(), ciapp.CIRun{WorkspaceID: "w", GithubRunID: 1, Attempt: 1, Provider: "openrouter"})
	if err != nil {
		t.Fatalf("RecordCIRun() error = %v, want nil", err)
	}
	if len(entries.got) != 1 || entries.got[0].Cost != nil || entries.got[0].Model != "" {
		t.Errorf("RecordCIRun() without cost or model appended %+v, want one entry with Cost nil and Model \"\"", entries.got)
	}
}

// chargeClient captures the request of the one call it answers.
type chargeClient struct{ req llm.Request }

func (c *chargeClient) Structured(_ context.Context, req llm.Request) (llm.Result, error) {
	c.req = req
	return llm.Result{}, context.Canceled
}

func TestCallerFactories_CopyTheChargeIntoTheRequest(t *testing.T) { // FR1
	charge := llm.Charge{WorkspaceID: "ws", Kind: llm.KindReviewRun, RefID: "run"}
	evalCharge := llm.Charge{WorkspaceID: "ws", Kind: llm.KindEvalCase, RefID: "case"}
	tests := []struct {
		name   string
		caller func(c llm.Charge, client *chargeClient) reviewerapp.LLM
		want   llm.Charge
	}{
		{"reviews", func(c llm.Charge, client *chargeClient) reviewerapp.LLM {
			return newLLMCaller(client)(peers.CallerConfig{Charge: &c})
		}, charge},
		{"evals", func(c llm.Charge, client *chargeClient) reviewerapp.LLM {
			return newEvalLLMCaller(client)(evalspeers.CallerConfig{Charge: &c})
		}, evalCharge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &chargeClient{}
			_, _ = tc.caller(tc.want, client).Review(t.Context(), reviewerapp.Call{})
			if client.req.Charge == nil {
				t.Fatalf("%s caller sent Charge nil, want %+v", tc.name, tc.want)
			}
			if diff := cmp.Diff(tc.want, *client.req.Charge); diff != "" {
				t.Errorf("%s caller request Charge mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func setBackfillRow(t *testing.T, db *pgtest.Database, created string, skipped, durationMs int) {
	t.Helper()
	err := db.DB.Exec(`UPDATE public.cost_ledger_backfill
		SET created = CAST(? AS jsonb), skipped = ?, duration_ms = ?, logged_at = NULL WHERE id = 1`,
		created, skipped, durationMs).Error
	if err != nil {
		t.Fatalf("set backfill row: %v", err)
	}
}

func TestReportLedgerBackfill_LogsOnceAndStampsTheRow(t *testing.T) { // FR15
	db := pgtest.Migrated(t)
	setBackfillRow(t, db, `{"review_run": 3, "ci_run": 2, "brief": 0, "future_kind": 9}`, 4, 1500)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))

	for i := range 2 {
		if err := reportLedgerBackfill(t.Context(), db.DB, log); err != nil {
			t.Fatalf("reportLedgerBackfill() call %d error = %v, want nil", i+1, err)
		}
	}

	lines := logLines(t, logs, "cost ledger: backfill finished")
	if len(lines) != 1 {
		t.Fatalf("backfill summary log lines after two calls = %d, want 1: %s", len(lines), logs.String())
	}
	line := lines[0]
	created, _ := line["created"].(map[string]any)
	if line["created_total"] != float64(5) || line["skipped_malformed"] != float64(4) || line["duration_ms"] != float64(1500) ||
		created["review_run"] != float64(3) || created["ci_run"] != float64(2) {
		t.Errorf("backfill summary = %v, want created_total 5 (review_run 3, ci_run 2), skipped_malformed 4, duration_ms 1500", line)
	}
	if got := scalar[bool](t, db.DB, `SELECT logged_at IS NOT NULL FROM cost_ledger_backfill WHERE id = 1`); !got {
		t.Error("cost_ledger_backfill.logged_at after the report = NULL, want a time")
	}
}

func TestStartup_LogsTheLedgerBackfillOnce(t *testing.T) { // FR15
	db := pgtest.Empty(t)
	first := startStack(t, spec{db: db})
	if n := len(logLines(t, first.logs, "cost ledger: backfill finished")); n != 1 {
		t.Errorf("first start logged the backfill summary %d times, want 1", n)
	}
	if err := first.stop(); err != nil {
		t.Fatalf("stop first server: %v", err)
	}
	second := startStack(t, spec{db: db})
	if n := len(logLines(t, second.logs, "cost ledger: backfill finished")); n != 0 {
		t.Errorf("second start logged the backfill summary %d times, want 0", n)
	}
}
