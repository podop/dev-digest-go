package app_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
)

// hostileFiles are a README and a compose file of the blob cap (512 KiB) made of `a:${`: before
// the F2 fix of the Phase 5 security review, redacting one took a minute and ignored the context.
func hostileFiles() map[string]string {
	hostile := strings.Repeat("a:${", 512*1024/4)
	return map[string]string{"README.md": hostile, "docker-compose.yml": hostile}
}

// A generation whose budget ends while the README is redacted stops there: the compose file is not
// redacted and no model request is made (F2).
func TestGenerate_AnExpiredBudgetStopsTheRedactions(t *testing.T) {
	r := newRig(t)
	r.blobs.files = hostileFiles()
	r.budget = 20 * time.Millisecond // the in-memory reads take microseconds, one redaction far longer
	r.build(readyState(), nil, fixedNow)

	start := time.Now()
	_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
	took := time.Since(start)

	ae, ok := apperr.As(err)
	if !ok || ae.Code != "generation_failed" || ae.Message != "Generation timed out" {
		t.Fatalf("Generate error = %v, want 502 generation_failed \"Generation timed out\"", err)
	}
	if len(r.llm.requests) != 0 {
		t.Errorf("model requests = %d, want 0 after the budget ended during the redactions", len(r.llm.requests))
	}
	if len(r.store.saved) != 0 {
		t.Errorf("saved tours = %d, want 0", len(r.store.saved))
	}
	if took > 30*time.Second {
		t.Errorf("Generate took %v over two hostile files, want linear redaction", took)
	}
}

// Shutdown while a generation reads and redacts hostile files ends it within the grace period (F2:
// it used to wait the full redaction out and report ErrNotDrained).
func TestShutdown_EndsAGenerationThatRedactsHostileFiles(t *testing.T) {
	r := newRig(t)
	r.blobs.files = hostileFiles()
	started := make(chan struct{})
	r.blobs.onRead = func(string) {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	r.build(readyState(), nil, fixedNow)

	done := make(chan error, 1)
	go func() {
		_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
		done <- err
	}()
	<-started

	sctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	begin := time.Now()
	if err := r.svc.Shutdown(sctx); err != nil {
		t.Fatalf("Shutdown = %v after %v, want nil: the generation must stop", err, time.Since(begin))
	}
	err := <-done
	if ae, ok := apperr.As(err); !ok || ae.Code != "generation_failed" {
		t.Fatalf("Generate error = %v, want 502 generation_failed", err)
	}
	if len(r.llm.requests) != 0 {
		t.Errorf("model requests = %d, want 0 for a generation shut down while redacting", len(r.llm.requests))
	}
}

// F3: a failed model call's error is logged by the HTTP error handler with its cause. Whatever
// the provider says (here a schema failure that quotes the model's output, built from README
// text), neither the returned error chain, nor the response message, nor a log line may carry it.
func TestGenerate_AFailedModelCallCarriesNoModelOutput(t *testing.T) {
	const marker = "MODEL-OUTPUT-MARKER-db.corp:5432"
	tests := []struct {
		name      string
		do        func(context.Context, llm.Request) (llm.Result, error)
		wantClass string
	}{
		{"schema failure", func(context.Context, llm.Request) (llm.Result, error) {
			return llm.Result{}, apperr.ExternalService("OpenRouter structured output failed schema validation for OnboardingTour after 2 attempt(s). Last issues:\n- candidates: Required\nLast raw output (42 chars): README says: " + marker)
		}, "class=failed"},
		{"provider error", func(context.Context, llm.Request) (llm.Result, error) {
			return llm.Result{}, errors.New("provider 400: echo " + marker)
		}, "class=failed"},
		{"output that does not parse", func(context.Context, llm.Request) (llm.Result, error) {
			return llm.Result{Data: []byte(`{"architecture": "` + marker + `"}`), Attempts: 1}, nil
		}, "class=invalid_output"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.llm.do = tc.do
			r.build(readyState(), nil, fixedNow)
			_, err := r.svc.Generate(t.Context(), r.ws, r.repo.ID)
			ae, ok := apperr.As(err)
			if !ok || ae.Code != "generation_failed" || ae.Message != "The model call failed" {
				t.Fatalf("Generate error = %v, want 502 generation_failed \"The model call failed\"", err)
			}
			// err.Error() is what the HTTP error handler logs (message and cause chain).
			for e := error(err); e != nil; e = errors.Unwrap(e) {
				if strings.Contains(e.Error(), marker) {
					t.Errorf("the error chain carries model output: %q", e.Error())
				}
			}
			if !strings.Contains(err.Error(), tc.wantClass) {
				t.Errorf("error = %q, want the classification %q", err.Error(), tc.wantClass)
			}
			if bytes.Contains(r.logs.Bytes(), []byte(marker)) {
				t.Errorf("a log line carries model output: %s", r.logs.String())
			}
		})
	}
}
