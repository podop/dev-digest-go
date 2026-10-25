package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var pruneNow = time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)

// seedTrace inserts an agent run started at ran and a trace for it.
func (e *historyEnv) seedTrace(ws string, ran time.Time) string {
	e.t.Helper()
	id := e.scalar(`INSERT INTO agent_runs (workspace_id, status, ran_at) VALUES (?, 'done', ?) RETURNING id`, ws, ran)
	e.exec(`INSERT INTO run_traces (run_id, trace) VALUES (?, '{}')`, id)
	return id
}

func (e *historyEnv) traceRuns() []string {
	e.t.Helper()
	var ids []string
	if err := e.db.Raw(`SELECT run_id FROM run_traces ORDER BY run_id`).Scan(&ids).Error; err != nil {
		e.t.Fatalf("list traces: %v", err)
	}
	return ids
}

// Only traces strictly older than the cutoff go; nothing else changes (AC7).
func TestPruneTraces_DeletesStrictlyOlderOnly(t *testing.T) {
	e := newHistoryEnv(t)
	a, b := e.seedTenant("a"), e.seedTenant("b")
	cutoff := pruneNow.Add(-90 * 24 * time.Hour)
	older := e.seedTrace(a.ws, cutoff.Add(-time.Second))
	exact := e.seedTrace(a.ws, cutoff)
	newer := e.seedTrace(a.ws, cutoff.Add(time.Second))
	otherWS := e.seedTrace(b.ws, cutoff.Add(-24*time.Hour))
	before := e.tableCounts()

	n, err := e.store.PruneTraces(t.Context(), a.ws, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("PruneTraces(a, %v) = %d, %v; want 1, nil", cutoff, n, err)
	}
	left := map[string]bool{}
	for _, id := range e.traceRuns() {
		left[id] = true
	}
	if left[older] || !left[exact] || !left[newer] || !left[otherWS] {
		t.Errorf("traces left: older %v exact %v newer %v other workspace %v; want false true true true", left[older], left[exact], left[newer], left[otherWS])
	}
	want := before
	want["run_traces"]--
	if diff := cmp.Diff(want, e.tableCounts()); diff != "" {
		t.Errorf("row counts after PruneTraces (-want +got):\n%s", diff)
	}
}

// More than one batch of traces is deleted in full (EC11).
func TestPruneTraces_DeletesMoreThanOneBatch(t *testing.T) {
	e := newHistoryEnv(t)
	ws := e.scalar(`INSERT INTO workspaces (name) VALUES ('a') RETURNING id`)
	e.exec(`INSERT INTO agent_runs (workspace_id, status, ran_at)
		SELECT ?, 'done', ?::timestamptz - make_interval(secs => g) FROM generate_series(1, 5001) g`, ws, pruneNow)
	e.exec(`INSERT INTO run_traces (run_id, trace) SELECT id, '{}' FROM agent_runs WHERE workspace_id = ?`, ws)

	n, err := e.store.PruneTraces(t.Context(), ws, pruneNow)
	if err != nil || n != 5001 {
		t.Fatalf("PruneTraces(5001 expired) = %d, %v; want 5001, nil", n, err)
	}
	if left := e.count(`SELECT count(*) FROM run_traces`); left != 0 {
		t.Errorf("%d traces left, want 0", left)
	}
	if runs := e.count(`SELECT count(*) FROM agent_runs`); runs != 5001 {
		t.Errorf("%d agent runs left, want 5001 (only traces are pruned)", runs)
	}
}

// A cancelled context stops the prune before it deletes.
func TestPruneTraces_CancelledContext(t *testing.T) {
	e := newHistoryEnv(t)
	a := e.seedTenant("a")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	n, err := e.store.PruneTraces(ctx, a.ws, pruneNow)
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Errorf("PruneTraces(cancelled) = %d, %v; want 0, context.Canceled", n, err)
	}
	if got := e.count(`SELECT count(*) FROM run_traces`); got != 2 {
		t.Errorf("%d traces left, want 2", got)
	}
}
