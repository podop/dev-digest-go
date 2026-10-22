package domain

import (
	"encoding/json"
	"testing"
	"time"
)

var stateTime = time.Date(2026, 10, 22, 10, 0, 0, 123_456_789, time.UTC)

func marshal(t *testing.T, v StateView) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNewStateView_Full(t *testing.T) {
	// AC3001: bounded, softBudgetReached and every other stats key stay out.
	st := IndexState{
		RepoID: "r1", LastIndexedSHA: "abc", IndexerVersion: 2, Status: StatusFull,
		FilesIndexed: 10, FilesSkipped: 2, UpdatedAt: stateTime,
		Stats: map[string]any{"durationMs": float64(1234), "bounded": float64(7), "softBudgetReached": false},
	}
	want := `{"repoId":"r1","status":"full","filesIndexed":10,"filesSkipped":2,"durationMs":1234,` +
		`"lastIndexedSha":"abc","indexerVersion":2,"updatedAt":"2026-10-22T10:00:00.123Z"}`
	if got := marshal(t, NewStateView(st)); got != want {
		t.Errorf("NewStateView(full) = %s, want %s", got, want)
	}
}

func TestNewStateView_Degraded(t *testing.T) {
	// AC3002.
	tests := []struct {
		name  string
		state IndexState
		want  string
	}{
		{
			"degraded with reasons",
			IndexState{RepoID: "r", Status: StatusDegraded, IndexerVersion: 2, UpdatedAt: stateTime,
				Stats: map[string]any{"reason": "no_clone", "degradedReason": "no_data"}},
			`{"repoId":"r","status":"degraded","filesIndexed":0,"filesSkipped":0,"durationMs":0,"reason":"no_clone",` +
				`"lastIndexedSha":"","indexerVersion":2,"updatedAt":"2026-10-22T10:00:00.123Z","degraded":true,"degradedReason":"no_data"}`,
		},
		{
			"failed without degradedReason",
			IndexState{RepoID: "r", Status: StatusFailed, IndexerVersion: 2, UpdatedAt: stateTime,
				Stats: map[string]any{"reason": "index_failed"}},
			`{"repoId":"r","status":"failed","filesIndexed":0,"filesSkipped":0,"durationMs":0,"reason":"index_failed",` +
				`"lastIndexedSha":"","indexerVersion":2,"updatedAt":"2026-10-22T10:00:00.123Z","degraded":true,"degradedReason":"index_failed"}`,
		},
		{
			"unknown degradedReason falls back",
			IndexState{RepoID: "r", Status: StatusDegraded, IndexerVersion: 2, UpdatedAt: stateTime,
				Stats: map[string]any{"degradedReason": "weird"}},
			`{"repoId":"r","status":"degraded","filesIndexed":0,"filesSkipped":0,"durationMs":0,` +
				`"lastIndexedSha":"","indexerVersion":2,"updatedAt":"2026-10-22T10:00:00.123Z","degraded":true,"degradedReason":"index_failed"}`,
		},
		{
			"partial carries no degraded keys, even with a degradedReason in stats",
			IndexState{RepoID: "r", Status: StatusPartial, IndexerVersion: 2, UpdatedAt: stateTime,
				Stats: map[string]any{"degradedReason": "no_data", "reason": ""}},
			`{"repoId":"r","status":"partial","filesIndexed":0,"filesSkipped":0,"durationMs":0,"reason":"",` +
				`"lastIndexedSha":"","indexerVersion":2,"updatedAt":"2026-10-22T10:00:00.123Z"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := marshal(t, NewStateView(tc.state)); got != tc.want {
				t.Errorf("NewStateView() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNewStateView_DurationMs(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int
	}{
		{"float64 from JSON", float64(42), 42},
		{"int", 7, 7},
		{"int64", int64(9), 9},
		{"json.Number", json.Number("11"), 11},
		{"fraction truncated", 12.9, 12},
		{"string is not a number", "5", 0},
		{"nil", nil, 0},
		{"out of range", 1e18, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := NewStateView(IndexState{Stats: map[string]any{StatDurationMs: tc.v}})
			if v.DurationMs != tc.want {
				t.Errorf("NewStateView(durationMs=%v).DurationMs = %d, want %d", tc.v, v.DurationMs, tc.want)
			}
		})
	}
}

func TestSynthesised(t *testing.T) {
	// AC3003: the id is echoed as sent.
	want := `{"repoId":"5F0C-UPPER","status":"degraded","filesIndexed":0,"filesSkipped":0,"durationMs":0,"reason":"no_data",` +
		`"lastIndexedSha":"","indexerVersion":2,"updatedAt":"1970-01-01T00:00:00.000Z","degraded":true,"degradedReason":"no_data"}`
	if got := marshal(t, Synthesised("5F0C-UPPER")); got != want {
		t.Errorf("Synthesised() = %s, want %s", got, want)
	}
}

func TestStatusRules(t *testing.T) {
	if got := FullStatus(false, false, 0); got != StatusFull {
		t.Errorf("FullStatus(clean) = %q, want full", got)
	}
	for _, c := range []struct {
		soft, graph bool
		parse       int
	}{{true, false, 0}, {false, true, 0}, {false, false, 1}} {
		if got := FullStatus(c.soft, c.graph, c.parse); got != StatusPartial {
			t.Errorf("FullStatus(%v, %v, %d) = %q, want partial", c.soft, c.graph, c.parse, got)
		}
	}
	incr := []struct {
		prev  Status
		graph bool
		parse int
		want  Status
	}{
		{StatusFull, false, 0, StatusFull},
		{StatusPartial, false, 0, StatusPartial},
		{StatusDegraded, false, 0, StatusPartial},
		{StatusFull, true, 0, StatusPartial},
		{StatusFull, false, 2, StatusPartial},
	}
	for _, c := range incr {
		if got := IncrementalStatus(c.prev, c.graph, c.parse); got != c.want {
			t.Errorf("IncrementalStatus(%q, %v, %d) = %q, want %q", c.prev, c.graph, c.parse, got, c.want)
		}
	}
}

func TestShouldRecordFailure(t *testing.T) {
	tests := []struct {
		name string
		prev *IndexState
		want bool
	}{
		{"no row", nil, true},
		{"degraded", &IndexState{Status: StatusDegraded}, true},
		{"failed", &IndexState{Status: StatusFailed}, true},
		{"full", &IndexState{Status: StatusFull}, false},
		{"partial", &IndexState{Status: StatusPartial}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRecordFailure(tc.prev); got != tc.want {
				t.Errorf("ShouldRecordFailure(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
