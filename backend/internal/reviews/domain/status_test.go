package domain_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"dev-digest-go/backend/internal/reviews/domain"
)

func TestEndingFor(t *testing.T) {
	boom := errors.New("provider exploded")
	tests := []struct {
		name      string
		err       error
		cancelled bool
		want      domain.Ending
	}{
		{"plain failure", boom, false, domain.Ending{Status: domain.StatusFailed, Note: "provider exploded"}},
		{"failure after a cancel is a cancel (EC4023)", boom, true, domain.Ending{Status: domain.StatusCancelled, Note: domain.CancelledNote}},
		{"aborted call while cancelled", context.Canceled, true, domain.Ending{Status: domain.StatusCancelled, Note: domain.CancelledNote}},
		{"run cancelled sentinel", domain.ErrRunCancelled, false, domain.Ending{Status: domain.StatusCancelled, Note: domain.CancelledNote}},
		{"wrapped sentinel", fmt.Errorf("chunk 2: %w", domain.ErrRunCancelled), false, domain.Ending{Status: domain.StatusCancelled, Note: domain.CancelledNote}},
		{"context error without a cancel flag fails", context.Canceled, false, domain.Ending{Status: domain.StatusFailed, Note: "context canceled"}},
		{"nil error", nil, false, domain.Ending{Status: domain.StatusFailed, Note: "unknown error"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.EndingFor(tc.err, tc.cancelled); got != tc.want {
				t.Errorf("EndingFor(%v, %v) = %+v, want %+v", tc.err, tc.cancelled, got, tc.want)
			}
		})
	}
}

func TestEndMsg(t *testing.T) {
	if got, want := domain.EndMsg(domain.Ending{Status: domain.StatusCancelled, Note: domain.CancelledNote}), "Run cancelled by user"; got != want {
		t.Errorf("EndMsg(cancelled) = %q, want %q", got, want)
	}
	if got, want := domain.EndMsg(domain.Ending{Status: domain.StatusFailed, Note: "bad key"}), "Run failed: bad key"; got != want {
		t.Errorf("EndMsg(failed) = %q, want %q", got, want)
	}
}

func TestRunStatus(t *testing.T) {
	for s, want := range map[domain.RunStatus]bool{
		domain.StatusRunning: false, domain.StatusDone: true, domain.StatusFailed: true,
		domain.StatusCancelled: true, "": false,
	} {
		if got := s.IsTerminal(); got != want {
			t.Errorf("RunStatus(%q).IsTerminal() = %v, want %v", s, got, want)
		}
	}
	for in, want := range map[domain.RunStatus]domain.RunStatus{
		"": domain.StatusFailed, "weird": domain.StatusFailed, domain.StatusRunning: domain.StatusRunning,
		domain.StatusDone: domain.StatusDone, domain.StatusCancelled: domain.StatusCancelled,
	} {
		if got := domain.ColumnStatus(in); got != want {
			t.Errorf("ColumnStatus(%q) = %q, want %q", in, got, want)
		}
	}
	custom := "Cancelled by someone"
	for _, tc := range []struct {
		in   *string
		want string
	}{{nil, "Cancelled by user"}, {ptr(""), "Cancelled by user"}, {&custom, custom}} {
		if got := domain.EndedNote(tc.in); got != tc.want {
			t.Errorf("EndedNote(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
