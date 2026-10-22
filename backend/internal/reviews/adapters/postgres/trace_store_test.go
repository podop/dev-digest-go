package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviews/domain"
)

func TestSaveTrace_UpsertsAndRoundTrips(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)

	if err := f.store.SaveTrace(t.Context(), run, endedTrace()); err != nil {
		t.Fatalf("SaveTrace() error = %v", err)
	}
	want := completedTrace()
	want.RawOutput = "raw with NUL \x00 and \xff"
	if err := f.store.SaveTrace(t.Context(), run, want); err != nil {
		t.Fatalf("SaveTrace(again) error = %v, want an upsert", err)
	}
	if n := f.count(t, `SELECT count(*) FROM run_traces WHERE run_id = ?`, run); n != 1 {
		t.Fatalf("run_traces rows = %d, want 1", n)
	}
	got, err := f.store.FindTrace(t.Context(), f.ws, run)
	if err != nil {
		t.Fatalf("FindTrace() error = %v", err)
	}
	want.RawOutput = "raw with NUL  and �"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindTrace() mismatch (-want +got):\n%s", diff)
	}
}

func TestSaveTrace_RunDeletedMeanwhile(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SaveTrace(t.Context(), missingID, endedTrace()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SaveTrace(deleted run) error = %v, want ErrNotFound", err)
	}
}

func TestFindTrace_NotFound(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	if _, err := f.store.FindTrace(t.Context(), f.ws, run); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindTrace(run without trace) error = %v, want ErrNotFound", err)
	}
	if err := f.store.SaveTrace(t.Context(), run, endedTrace()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.FindTrace(t.Context(), f.other, run); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindTrace(other workspace) error = %v, want ErrNotFound", err)
	}
	if _, err := f.store.FindTrace(t.Context(), f.ws, missingID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FindTrace(unknown run) error = %v, want ErrNotFound", err)
	}
}

// TestFindTrace_InvalidStoredDocument is AC4012's 500 case at the store: a stored document with
// stats.duration_ms "x" is ErrTraceInvalid and the error does not quote the document.
func TestFindTrace_InvalidStoredDocument(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	f.exec(t, `INSERT INTO run_traces (run_id, trace) VALUES (?, ?::jsonb)`, run,
		strings.Replace(minimalDoc, `"duration_ms":1`, `"duration_ms":"x"`, 1))
	_, err := f.store.FindTrace(t.Context(), f.ws, run)
	if !errors.Is(err, domain.ErrTraceInvalid) {
		t.Fatalf("FindTrace() error = %v, want ErrTraceInvalid", err)
	}
	if strings.Contains(err.Error(), `"x"`) {
		t.Errorf("error %q quotes the document", err)
	}
}
