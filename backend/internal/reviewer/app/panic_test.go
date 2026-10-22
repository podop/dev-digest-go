package app_test

import (
	"context"
	"testing"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
)

// A panic in the LLM port runs on a goroutine of the engine's pool. It must surface on the caller's
// goroutine, where the executor recovers it and fails only that run; on the pool's own goroutine
// nothing could recover it and the whole process would die.
func TestRun_APanicInTheLLMPortSurfacesOnTheCallersGoroutine(t *testing.T) {
	l := &fakeLLM{fn: func(context.Context, reviewerapp.Call) (reviewerapp.CallResult, error) {
		panic("provider adapter bug")
	}}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Run() returned, want the panic of the LLM port re-raised on the caller's goroutine")
		}
		if got, _ := r.(string); got != "provider adapter bug" {
			t.Errorf("recovered %q, want the original panic value", got)
		}
	}()
	_, _ = reviewerapp.Run(t.Context(), reviewerapp.Input{Diff: diffOf(map[string]int{"a.ts": 1})}, l, nil, reviewerapp.Options{})
}
