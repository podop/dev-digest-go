package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var stateTime = time.Date(2026, 10, 22, 10, 0, 0, 123_456_789, time.UTC)

func ptr(s string) *string { return &s }

func TestNewStateView_Full(t *testing.T) {
	// AC3001: bounded, softBudgetReached and every other stats key stay out.
	st := IndexState{
		RepoID: "r1", LastIndexedSHA: "abc", IndexerVersion: 2, Status: StatusFull,
		FilesIndexed: 10, FilesSkipped: 2, UpdatedAt: stateTime,
		Stats: map[string]any{"durationMs": float64(1234), "bounded": float64(7), "softBudgetReached": false},
	}
	want := StateView{
		RepoID: "r1", Status: StatusFull, FilesIndexed: 10, FilesSkipped: 2, DurationMs: 1234,
		LastIndexedSha: "abc", IndexerVersion: 2, UpdatedAt: "2026-10-22T10:00:00.123Z",
	}
	if diff := cmp.Diff(want, NewStateView(st)); diff != "" {
		t.Errorf("NewStateView(full) mismatch (-want +got):\n%s", diff)
	}
}

func TestNewStateView_Degraded(t *testing.T) {
	// AC3002.
	const at = "2026-10-22T10:00:00.123Z"
	tests := []struct {
		name  string
		state IndexState
		want  StateView
	}{
		{
			"degraded with reasons",
			IndexState{RepoID: "r", Status: StatusDegraded, IndexerVersion: 2, UpdatedAt: stateTime,
				Stats: map[string]any{"reason": "no_clone", "degradedReason": "no_data"}},
			StateView{RepoID: "r", Status: StatusDegraded, Reason: ptr("no_clone"), IndexerVersion: 2,
				UpdatedAt: at, Degraded: true, DegradedReason: ReasonNoData},
		},
		{
			"failed without degradedReason",
			IndexState{RepoID: "r", Status: StatusFailed, IndexerVersion: 2, UpdatedAt: stateTime,
				Stats: map[string]any{"reason": "index_failed"}},
			StateView{RepoID: "r", Status: StatusFailed, Reason: ptr("index_failed"), IndexerVersion: 2,
				UpdatedAt: at, Degraded: true, DegradedReason: ReasonIndexFailed},
		},
		{
			"unknown degradedReason falls back",
			IndexState{RepoID: "r", Status: StatusDegraded, IndexerVersion: 2, UpdatedAt: stateTime,
				Stats: map[string]any{"degradedReason": "weird"}},
			StateView{RepoID: "r", Status: StatusDegraded, IndexerVersion: 2, UpdatedAt: at,
				Degraded: true, DegradedReason: ReasonIndexFailed},
		},
		{
			"partial carries no degraded keys, even with a degradedReason in stats",
			IndexState{RepoID: "r", Status: StatusPartial, IndexerVersion: 2, UpdatedAt: stateTime,
				Stats: map[string]any{"degradedReason": "no_data", "reason": ""}},
			StateView{RepoID: "r", Status: StatusPartial, Reason: ptr(""), IndexerVersion: 2, UpdatedAt: at},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, NewStateView(tc.state)); diff != "" {
				t.Errorf("NewStateView() mismatch (-want +got):\n%s", diff)
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
	want := StateView{
		RepoID: "5F0C-UPPER", Status: StatusDegraded, Reason: ptr("no_data"), IndexerVersion: 2,
		UpdatedAt: "1970-01-01T00:00:00.000Z", Degraded: true, DegradedReason: ReasonNoData,
	}
	if diff := cmp.Diff(want, Synthesised("5F0C-UPPER")); diff != "" {
		t.Errorf("Synthesised() mismatch (-want +got):\n%s", diff)
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
