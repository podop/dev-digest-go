package executor

import (
	"context"
	"errors"
	"testing"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// TestExecutor_BatchSourceReachesEveryTrace covers FR7/FR11: the batch's run source is the
// trace's config.source for a completed run, a failed run and a run whose diff failed to load; an
// empty source stays "local".
func TestExecutor_BatchSourceReachesEveryTrace(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
		setup  func(h *harness)
		ended  bool
	}{
		{"auto completed", domain.SourceAuto, "auto", func(*harness) {}, false},
		{"auto failed", domain.SourceAuto, "auto", func(h *harness) {
			h.engine.script = func(context.Context, reviewerapp.Input, reviewerapp.LLM, reviewerapp.Events, reviewerapp.Options) (reviewer.Outcome, error) {
				return reviewer.Outcome{}, errors.New("model unavailable")
			}
		}, true},
		{"auto diff failure", domain.SourceAuto, "auto", func(h *harness) { h.store.filesErr = errors.New("db down") }, true},
		{"local default", "", "local", func(*harness) {}, false},
		{"local explicit", domain.SourceLocal, "local", func(*harness) {}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Options{}, fixedClock)
			tc.setup(h)
			b := h.batch(1)
			b.Source = tc.source
			h.exec.Start(t.Context(), b)
			h.wait()

			tr := h.store.run("run-1").trace
			if tr == nil {
				t.Fatal("no trace recorded")
			}
			if tr.Ended != tc.ended {
				t.Errorf("trace.Ended = %v, want %v", tr.Ended, tc.ended)
			}
			if tr.Config.Source != tc.want {
				t.Errorf("trace.Config.Source = %q, want %q", tr.Config.Source, tc.want)
			}
		})
	}
}
