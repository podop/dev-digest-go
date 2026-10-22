package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

func TestService_IndexState(t *testing.T) {
	const upper = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
	at := time.Date(2026, 10, 22, 10, 0, 0, 123_000_000, time.UTC)
	reasonR := "r"
	tests := []struct {
		name    string
		id      string
		setup   func(s *fakeStore)
		want    domain.StateView
		wantLog bool
	}{
		{
			name: "persisted full state",
			id:   repoID,
			setup: func(s *fakeStore) {
				s.states[repoID] = domain.IndexState{RepoID: repoID, LastIndexedSHA: "abc", IndexerVersion: 2, Status: domain.StatusFull,
					FilesIndexed: 7, FilesSkipped: 1, UpdatedAt: at, Stats: map[string]any{"durationMs": 42, "bounded": 9, "reason": "r"}}
			},
			want: domain.StateView{RepoID: repoID, Status: domain.StatusFull, FilesIndexed: 7, FilesSkipped: 1, DurationMs: 42,
				Reason: &reasonR, LastIndexedSha: "abc", IndexerVersion: 2, UpdatedAt: "2026-10-22T10:00:00.123Z"},
		},
		{
			name: "persisted failed state",
			id:   repoID,
			setup: func(s *fakeStore) {
				s.states[repoID] = domain.IndexState{RepoID: repoID, IndexerVersion: 2, Status: domain.StatusFailed, UpdatedAt: at,
					Stats: map[string]any{"degradedReason": "index_failed"}}
			},
			want: domain.StateView{RepoID: repoID, Status: domain.StatusFailed, IndexerVersion: 2,
				UpdatedAt: "2026-10-22T10:00:00.123Z", Degraded: true, DegradedReason: domain.ReasonIndexFailed},
		},
		{
			name: "no row keeps the id as sent",
			id:   upper,
			want: domain.Synthesised(upper),
		},
		{
			name: "read failure is synthesised and logged",
			id:   repoID,
			setup: func(s *fakeStore) {
				s.hooks["GetState"] = func(context.Context) error { return errors.New("connection refused") }
			},
			want:    domain.Synthesised(repoID),
			wantLog: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			if tc.setup != nil {
				tc.setup(store)
			}
			var logs bytes.Buffer
			svc := app.NewService(store, &fakeQueue{}, slog.New(slog.NewTextHandler(&logs, nil)))
			if diff := cmp.Diff(tc.want, svc.IndexState(t.Context(), tc.id)); diff != "" {
				t.Errorf("IndexState(%q) mismatch (-want +got):\n%s", tc.id, diff)
			}
			if warned := strings.Contains(logs.String(), "level=WARN"); warned != tc.wantLog {
				t.Errorf("warning logged = %v, want %v:\n%s", warned, tc.wantLog, logs.String())
			}
		})
	}
}

func TestService_Resync(t *testing.T) {
	q := &fakeQueue{}
	svc := app.NewService(newFakeStore(), q, nil)
	id, err := svc.Resync(t.Context(), "ws-1", "repo-7")
	if err != nil || id != "job-1" {
		t.Fatalf("Resync() = %q, %v, want job-1", id, err)
	}
	if diff := cmp.Diff([]queuedJob{{"ws-1", "repo-intel-resync", `{"repoId":"repo-7"}`}}, q.jobs); diff != "" {
		t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
	}
}

func TestService_ResyncEnqueueFailureIsReturnedAndLogged(t *testing.T) {
	var logs bytes.Buffer
	q := &fakeQueue{err: errors.New("no handler registered")}
	svc := app.NewService(newFakeStore(), q, slog.New(slog.NewTextHandler(&logs, nil)))
	id, err := svc.Resync(t.Context(), "ws-1", "repo-7")
	if err == nil || id != "" {
		t.Errorf("Resync() = %q, %v, want the error", id, err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "no handler registered") {
		t.Errorf("log = %q, want a warning with the cause", logs.String())
	}
}
