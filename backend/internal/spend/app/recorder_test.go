package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/spend/app"
	"dev-digest-go/backend/internal/spend/domain"
)

const (
	wsID  = "11111111-1111-4111-8111-111111111111"
	refID = "22222222-2222-4222-8222-222222222222"
)

// appendCall is one Append the fake saw, with what the entry's context said at that moment.
type appendCall struct {
	Entry       domain.Entry
	CtxErr      error
	HasDeadline bool
	Remaining   time.Duration
	Value       any
}

type ctxKey struct{}

// fakeEntries is an in-memory Entries: it dedupes by key like the real store.
type fakeEntries struct {
	mu    sync.Mutex
	calls []appendCall
	seen  map[string]bool
	err   error
	// block makes Append wait for its context, like a database that does not answer.
	block bool
}

func (f *fakeEntries) Append(ctx context.Context, e domain.Entry) (bool, error) {
	call := appendCall{Entry: e, CtxErr: ctx.Err(), Value: ctx.Value(ctxKey{})}
	if dl, ok := ctx.Deadline(); ok {
		call.HasDeadline, call.Remaining = true, time.Until(dl)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if f.err != nil {
		return false, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[e.DedupeKey] {
		return false, nil
	}
	f.seen[e.DedupeKey] = true
	return true, nil
}

func (f *fakeEntries) entries() []domain.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Entry, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Entry)
	}
	return out
}

func newRecorder(entries *fakeEntries) (*app.Recorder, *bytes.Buffer) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return app.NewRecorder(entries, func() time.Time { return october }, log), &buf
}

func usage(kind llm.UsageKind, cost *float64) llm.RecordedUsage {
	return llm.RecordedUsage{
		Charge:   llm.Charge{WorkspaceID: wsID, Kind: kind, RefID: refID},
		Provider: llm.Provider("anthropic"),
		Model:    "claude-x",
		Tokens:   llm.Usage{TokensIn: 1200, TokensOut: 340},
		CostUSD:  cost,
	}
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestRecorder_RecordUsage_MapsEveryKind(t *testing.T) {
	tests := []struct {
		kind llm.UsageKind
		want domain.Kind
	}{
		{llm.KindReviewRun, domain.KindReviewRun},
		{llm.KindReviewIntent, domain.KindReviewIntent},
		{llm.KindIntentRefresh, domain.KindIntentRefresh},
		{llm.KindBrief, domain.KindBrief},
		{llm.KindOnboarding, domain.KindOnboarding},
		{llm.KindConventionsScan, domain.KindConventionsScan},
		{llm.KindEvalCase, domain.KindEvalCase},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			entries := &fakeEntries{}
			rec, logs := newRecorder(entries)

			rec.RecordUsage(t.Context(), usage(tc.kind, f64(0.0123456)))

			got := entries.entries()
			if len(got) != 1 {
				t.Fatalf("RecordUsage() appended %d entries, want 1", len(got))
			}
			want := domain.Entry{
				WorkspaceID: wsID,
				OccurredAt:  october,
				Kind:        tc.want,
				Origin:      domain.OriginLive,
				Provider:    "anthropic",
				Model:       "claude-x",
				TokensIn:    i64(1200),
				TokensOut:   i64(340),
				Cost:        micros(12_346),
				RefID:       refID,
				DedupeKey:   got[0].DedupeKey,
			}
			if diff := cmp.Diff(want, got[0]); diff != "" {
				t.Errorf("RecordUsage() entry mismatch (-want +got):\n%s", diff)
			}
			if !strings.HasPrefix(got[0].DedupeKey, "live:") || len(got[0].DedupeKey) <= len("live:") {
				t.Errorf("RecordUsage() dedupe key = %q, want a generated live: key", got[0].DedupeKey)
			}
			if logs.Len() != 0 {
				t.Errorf("RecordUsage() logged %q, want nothing on success", logs.String())
			}
		})
	}
}

func TestRecorder_RecordUsage_CostConversion(t *testing.T) {
	tests := []struct {
		name string
		cost *float64
		want *domain.Micros
	}{
		{"known", f64(1.5), micros(1_500_000)},
		{"zero is known", f64(0), micros(0)},
		{"rounds half away from zero", f64(0.0000005), micros(1)},
		{"nil is unpriced", nil, nil},
		{"negative is unpriced", f64(-0.5), nil},
		{"NaN is unpriced", f64(math.NaN()), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := &fakeEntries{}
			rec, _ := newRecorder(entries)

			rec.RecordUsage(t.Context(), usage(llm.KindBrief, tc.cost))

			got := entries.entries()
			if len(got) != 1 {
				t.Fatalf("RecordUsage() appended %d entries, want 1 (an unpriced answer is still an entry)", len(got))
			}
			if diff := cmp.Diff(tc.want, got[0].Cost); diff != "" {
				t.Errorf("RecordUsage(cost %v) cost mismatch (-want +got):\n%s", tc.cost, diff)
			}
		})
	}
}

func TestRecorder_RecordUsage_EveryEntryGetsItsOwnDedupeKey(t *testing.T) {
	entries := &fakeEntries{}
	rec, _ := newRecorder(entries)

	// Identical answers, e.g. re-prompts: each is its own entry (D3).
	for range 50 {
		rec.RecordUsage(t.Context(), usage(llm.KindReviewRun, f64(0.01)))
	}

	seen := map[string]bool{}
	for _, e := range entries.entries() {
		if seen[e.DedupeKey] {
			t.Fatalf("dedupe key %q used twice, want a unique key per entry", e.DedupeKey)
		}
		seen[e.DedupeKey] = true
	}
	if len(seen) != 50 {
		t.Errorf("RecordUsage() x50 produced %d distinct keys, want 50", len(seen))
	}
}

func TestRecorder_RecordUsage_RefAndTokens(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		tokensIn   int
		wantRef    string
		wantTokens *int64
	}{
		{"uuid kept", refID, 7, refID, i64(7)},
		{"upper-case uuid kept", strings.ToUpper(refID), 7, strings.ToUpper(refID), i64(7)},
		{"empty ref", "", 7, "", i64(7)},
		{"ref that is no uuid is dropped, entry kept", "pr-42", 7, "", i64(7)},
		{"negative tokens are unknown", refID, -1, refID, nil},
		{"zero tokens are known", refID, 0, refID, i64(0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := &fakeEntries{}
			rec, _ := newRecorder(entries)
			u := usage(llm.KindOnboarding, f64(0.1))
			u.Charge.RefID = tc.ref
			u.Tokens.TokensIn = tc.tokensIn

			rec.RecordUsage(t.Context(), u)

			got := entries.entries()
			if len(got) != 1 {
				t.Fatalf("RecordUsage() appended %d entries, want 1", len(got))
			}
			if got[0].RefID != tc.wantRef {
				t.Errorf("RecordUsage(ref %q) RefID = %q, want %q", tc.ref, got[0].RefID, tc.wantRef)
			}
			if diff := cmp.Diff(tc.wantTokens, got[0].TokensIn); diff != "" {
				t.Errorf("RecordUsage(tokens %d) TokensIn mismatch (-want +got):\n%s", tc.tokensIn, diff)
			}
		})
	}
}

func TestRecorder_RecordUsage_UnknownKindIsLoggedNotRecorded(t *testing.T) {
	for _, kind := range []llm.UsageKind{"", "bogus", "ci_run"} {
		t.Run("kind "+string(kind), func(t *testing.T) {
			entries := &fakeEntries{}
			rec, logs := newRecorder(entries)

			rec.RecordUsage(t.Context(), usage(kind, f64(0.5)))

			if got := entries.entries(); len(got) != 0 {
				t.Errorf("RecordUsage(kind %q) appended %v, want nothing", kind, got)
			}
			lines := logLines(t, logs)
			if len(lines) != 1 || lines[0]["level"] != "ERROR" {
				t.Errorf("RecordUsage(kind %q) log = %v, want one ERROR line", kind, lines)
			}
		})
	}
}

func TestRecorder_RecordUsage_AppendFailureLogsOneErrorAndGoesOn(t *testing.T) {
	boom := errors.New("connection refused")
	entries := &fakeEntries{err: boom}
	rec, logs := newRecorder(entries)

	// Must neither panic nor return anything: the unit of work goes on (D8).
	rec.RecordUsage(t.Context(), usage(llm.KindReviewRun, f64(0.25)))

	lines := logLines(t, logs)
	if len(lines) != 1 {
		t.Fatalf("RecordUsage() wrote %d log lines %q, want exactly 1", len(lines), logs.String())
	}
	line := lines[0]
	if line["level"] != "ERROR" || line["kind"] != "review_run" || line["provider"] != "anthropic" ||
		line["model"] != "claude-x" || line["workspace_id"] != wsID || line["cost_usd"] != 0.25 ||
		line["unpriced"] != false || line["error"] != boom.Error() {
		t.Errorf("RecordUsage() log line = %v, want ERROR with kind, provider, model, workspace_id, cost_usd 0.25 and the error", line)
	}
	for _, field := range []string{"ref_id", "dedupe_key", "prompt", "output"} {
		if _, ok := line[field]; ok {
			t.Errorf("RecordUsage() log line has %q, want only kind, provider, model, cost and workspace (NFR4)", field)
		}
	}
}

func TestRecorder_RecordUsage_UnpricedFailureLogsNoCost(t *testing.T) {
	entries := &fakeEntries{err: errors.New("down")}
	rec, logs := newRecorder(entries)

	rec.RecordUsage(t.Context(), usage(llm.KindBrief, nil))

	lines := logLines(t, logs)
	if len(lines) != 1 || lines[0]["unpriced"] != true {
		t.Fatalf("RecordUsage() log = %v, want one line with unpriced true", lines)
	}
	if _, ok := lines[0]["cost_usd"]; ok {
		t.Errorf("RecordUsage() log line has cost_usd = %v for an unpriced answer, want none", lines[0]["cost_usd"])
	}
}

// A cancelled caller has still spent its tokens (FR5): the append is detached from the caller's
// cancellation but keeps its values (logging, tracing).
func TestRecorder_RecordUsage_CancelledCallerStillAppends(t *testing.T) {
	entries := &fakeEntries{}
	rec, logs := newRecorder(entries)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "trace-1"))
	cancel()

	rec.RecordUsage(ctx, usage(llm.KindReviewRun, f64(0.5)))

	entries.mu.Lock()
	defer entries.mu.Unlock()
	if len(entries.calls) != 1 {
		t.Fatalf("RecordUsage() on a cancelled ctx appended %d entries, want 1", len(entries.calls))
	}
	call := entries.calls[0]
	if call.CtxErr != nil {
		t.Errorf("Append ctx.Err() = %v, want nil: the caller's cancellation must not reach the append", call.CtxErr)
	}
	if call.Value != "trace-1" {
		t.Errorf("Append ctx value = %v, want the caller's values kept", call.Value)
	}
	if logs.Len() != 0 {
		t.Errorf("RecordUsage() logged %q, want nothing on success", logs.String())
	}
}

func TestRecorder_RecordUsage_AppendIsBoundedTo3Seconds(t *testing.T) {
	t.Run("deadline set", func(t *testing.T) {
		entries := &fakeEntries{}
		rec, _ := newRecorder(entries)

		rec.RecordUsage(t.Context(), usage(llm.KindBrief, f64(1)))

		call := entries.calls[0]
		if !call.HasDeadline || call.Remaining > 3*time.Second || call.Remaining < 2*time.Second {
			t.Errorf("Append deadline = (set %v, %v left), want one about 3s away", call.HasDeadline, call.Remaining)
		}
	})
	t.Run("a database that does not answer", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			entries := &fakeEntries{block: true}
			rec, logs := newRecorder(entries)

			start := time.Now()
			rec.RecordUsage(t.Context(), usage(llm.KindBrief, f64(1)))

			if elapsed := time.Since(start); elapsed != 3*time.Second {
				t.Errorf("RecordUsage() blocked %v, want 3s", elapsed)
			}
			lines := logLines(t, logs)
			if len(lines) != 1 || lines[0]["level"] != "ERROR" {
				t.Errorf("RecordUsage() log = %v, want one ERROR line for the timeout", lines)
			}
		})
	})
}

func TestRecorder_RecordUsage_TimestampIsTheReceiveTimeInUTC(t *testing.T) {
	entries := &fakeEntries{}
	local := time.FixedZone("far", 14*3600)
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, local) // 2026-10-31 11:00 UTC
	rec := app.NewRecorder(entries, func() time.Time { return at }, slog.New(slog.DiscardHandler))

	rec.RecordUsage(t.Context(), usage(llm.KindBrief, f64(1)))

	got := entries.entries()[0].OccurredAt
	if want := time.Date(2026, 10, 31, 11, 0, 0, 0, time.UTC); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("RecordUsage() OccurredAt = %v, want %v in UTC", got, want)
	}
}

func ciRun() app.CIRun {
	prov, model := "anthropic", "claude-y"
	return app.CIRun{
		WorkspaceID: wsID,
		AgentRunID:  refID,
		GithubRunID: 9001,
		Attempt:     2,
		OccurredAt:  time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC),
		Provider:    &prov,
		Model:       &model,
		CostUSD:     f64(0.42),
	}
}

func TestRecorder_RecordCIRun(t *testing.T) {
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*app.CIRun)
		want   domain.Entry
	}{
		{"priced", func(*app.CIRun) {}, domain.Entry{
			WorkspaceID: wsID, OccurredAt: at, Kind: domain.KindCIRun, Origin: domain.OriginCIIngest,
			Provider: "anthropic", Model: "claude-y", Cost: micros(420_000), RefID: refID, DedupeKey: "ci:9001:2",
		}},
		{"null cost is unpriced", func(r *app.CIRun) { r.CostUSD = nil }, domain.Entry{
			WorkspaceID: wsID, OccurredAt: at, Kind: domain.KindCIRun, Origin: domain.OriginCIIngest,
			Provider: "anthropic", Model: "claude-y", RefID: refID, DedupeKey: "ci:9001:2",
		}},
		{"no provider or model", func(r *app.CIRun) { r.Provider, r.Model = nil, nil }, domain.Entry{
			WorkspaceID: wsID, OccurredAt: at, Kind: domain.KindCIRun, Origin: domain.OriginCIIngest,
			Cost: micros(420_000), RefID: refID, DedupeKey: "ci:9001:2",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := &fakeEntries{}
			rec, logs := newRecorder(entries)
			run := ciRun()
			tc.mutate(&run)

			if err := rec.RecordCIRun(t.Context(), run); err != nil {
				t.Fatalf("RecordCIRun() = %v, want nil", err)
			}

			got := entries.entries()
			if len(got) != 1 {
				t.Fatalf("RecordCIRun() appended %d entries, want 1", len(got))
			}
			if diff := cmp.Diff(tc.want, got[0]); diff != "" {
				t.Errorf("RecordCIRun() entry mismatch (-want +got):\n%s", diff)
			}
			if logs.Len() != 0 {
				t.Errorf("RecordCIRun() logged %q, want nothing", logs.String())
			}
		})
	}
}

func TestRecorder_RecordCIRun_IsIdempotent(t *testing.T) {
	entries := &fakeEntries{}
	rec, _ := newRecorder(entries)

	for i := range 3 {
		if err := rec.RecordCIRun(t.Context(), ciRun()); err != nil {
			t.Fatalf("RecordCIRun() call %d = %v, want nil: a duplicate is not an error", i+1, err)
		}
	}
	if len(entries.seen) != 1 || !entries.seen["ci:9001:2"] {
		t.Errorf("RecordCIRun() x3 stored keys %v, want only ci:9001:2", entries.seen)
	}

	other := ciRun()
	other.Attempt = 3
	if err := rec.RecordCIRun(t.Context(), other); err != nil || len(entries.seen) != 2 {
		t.Errorf("RecordCIRun(attempt 3) = %v with %d stored, want nil with 2: another attempt is its own entry", err, len(entries.seen))
	}
}

// The CI append runs in the caller's transaction: its context is passed through untouched (no
// detaching, no timeout), and a failure is returned so the run is rolled back.
func TestRecorder_RecordCIRun_UsesTheCallersContextAndReturnsErrors(t *testing.T) {
	boom := errors.New("tx aborted")
	entries := &fakeEntries{err: boom}
	rec, logs := newRecorder(entries)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "tx-1"))
	cancel()

	err := rec.RecordCIRun(ctx, ciRun())

	if !errors.Is(err, boom) {
		t.Fatalf("RecordCIRun() = %v, want an error wrapping %v", err, boom)
	}
	call := entries.calls[0]
	if call.Value != "tx-1" || !errors.Is(call.CtxErr, context.Canceled) || call.HasDeadline {
		t.Errorf("Append ctx = (value %v, err %v, deadline %v), want the caller's own context", call.Value, call.CtxErr, call.HasDeadline)
	}
	if logs.Len() != 0 {
		t.Errorf("RecordCIRun() logged %q, want the error returned and not logged (handled once, by the caller)", logs.String())
	}
}

func TestRecorder_ReportBackfill(t *testing.T) {
	entries := &fakeEntries{}
	rec, logs := newRecorder(entries)

	rec.ReportBackfill(t.Context(), app.BackfillSummary{
		Created:  map[domain.Kind]int{domain.KindReviewRun: 120, domain.KindCIRun: 7, domain.KindBrief: 3},
		Skipped:  2,
		Duration: 1500 * time.Millisecond,
	})

	lines := logLines(t, logs)
	if len(lines) != 1 {
		t.Fatalf("ReportBackfill() wrote %d log lines %q, want exactly 1", len(lines), logs.String())
	}
	line := lines[0]
	wantCreated := map[string]any{"review_run": 120.0, "ci_run": 7.0, "brief": 3.0}
	if line["level"] != "INFO" || line["skipped_malformed"] != 2.0 || line["duration_ms"] != 1500.0 ||
		line["created_total"] != 130.0 {
		t.Errorf("ReportBackfill() log line = %v, want INFO with created_total 130, skipped_malformed 2, duration_ms 1500", line)
	}
	if diff := cmp.Diff(wantCreated, line["created"]); diff != "" {
		t.Errorf("ReportBackfill() created mismatch (-want +got):\n%s", diff)
	}
	if got := entries.entries(); len(got) != 0 {
		t.Errorf("ReportBackfill() appended %v, want nothing", got)
	}
}

func TestRecorder_ReportBackfill_NothingToCopy(t *testing.T) {
	rec, logs := newRecorder(&fakeEntries{})

	rec.ReportBackfill(t.Context(), app.BackfillSummary{})

	lines := logLines(t, logs)
	if len(lines) != 1 || lines[0]["created_total"] != 0.0 || lines[0]["skipped_malformed"] != 0.0 {
		t.Errorf("ReportBackfill(empty) log = %v, want one INFO line with zero counts", lines)
	}
}
