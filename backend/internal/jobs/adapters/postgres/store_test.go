package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/jobs"
	jobspg "dev-digest-go/backend/internal/jobs/adapters/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// jobRecord reads a jobs row with every column the store writes.
type jobRecord struct {
	WorkspaceID string
	Kind        string
	Payload     string
	Status      string
	Attempts    int
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Error       *string
}

func (jobRecord) TableName() string { return "jobs" }

// newWorkspace inserts a workspace (jobs.workspace_id is a foreign key) and returns its id.
func newWorkspace(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var id string
	if err := db.Raw(`INSERT INTO workspaces (name) VALUES ('jobs-test') RETURNING id`).Scan(&id).Error; err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	return id
}

func load(t *testing.T, db *gorm.DB, id string) jobRecord {
	t.Helper()
	var rec jobRecord
	if err := db.Where("id = ?", id).Take(&rec).Error; err != nil {
		t.Fatalf("load job %s: %v", id, err)
	}
	return rec
}

func TestStore_Lifecycle(t *testing.T) {
	d := pgtest.Migrated(t)
	ws := newWorkspace(t, d.DB)
	s := jobspg.NewStore(d.DB)
	ctx := t.Context()

	id, err := s.Insert(ctx, ws, "clone", json.RawMessage(`{"repo_id":"r1"}`))
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	rec := load(t, d.DB, id)
	if rec.Status != "queued" || rec.Kind != "clone" || rec.WorkspaceID != ws || rec.Attempts != 0 || rec.StartedAt != nil || rec.Error != nil {
		t.Errorf("row after Insert = %+v, want a fresh queued clone job", rec)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(rec.Payload), &payload); err != nil || payload["repo_id"] != "r1" {
		t.Errorf("stored payload = %q (%v), want JSON with repo_id r1", rec.Payload, err)
	}

	start := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.MarkRunning(ctx, id, start); err != nil {
		t.Fatalf("MarkRunning() error = %v", err)
	}
	if rec = load(t, d.DB, id); rec.Status != "running" || rec.StartedAt == nil || !rec.StartedAt.Equal(start) {
		t.Errorf("row after MarkRunning = %+v, want running with started_at %v", rec, start)
	}

	if err := s.SetAttempts(ctx, id, 2); err != nil {
		t.Fatalf("SetAttempts() error = %v", err)
	}
	if rec = load(t, d.DB, id); rec.Attempts != 2 || rec.Status != "running" {
		t.Errorf("row after SetAttempts = %+v, want attempts 2 and still running", rec)
	}

	end := start.Add(time.Second)
	if err := s.Finish(ctx, id, jobs.StatusFailed, 0, "git clone: not found", end); err != nil {
		t.Fatalf("Finish(failed) error = %v", err)
	}
	rec = load(t, d.DB, id)
	if rec.Status != "failed" || rec.Attempts != 0 || rec.FinishedAt == nil || !rec.FinishedAt.Equal(end) ||
		rec.Error == nil || *rec.Error != "git clone: not found" {
		t.Errorf("row after Finish(failed) = %+v, want failed, attempts 0, the error text", rec)
	}

	if err := s.Finish(ctx, id, jobs.StatusDone, 1, "", end); err != nil {
		t.Fatalf("Finish(done) error = %v", err)
	}
	if rec = load(t, d.DB, id); rec.Status != "done" || rec.Attempts != 1 || rec.Error != nil {
		t.Errorf("row after Finish(done) = %+v, want done, attempts 1, error NULL", rec)
	}
}

func TestStore_UnknownJob(t *testing.T) {
	d := pgtest.Migrated(t)
	s := jobspg.NewStore(d.DB)
	const missing = "00000000-0000-0000-0000-000000000000"
	for name, err := range map[string]error{
		"MarkRunning": s.MarkRunning(t.Context(), missing, time.Now()),
		"SetAttempts": s.SetAttempts(t.Context(), missing, 1),
		"Finish":      s.Finish(t.Context(), missing, jobs.StatusDone, 1, "", time.Now()),
	} {
		if !errors.Is(err, jobs.ErrNotFound) {
			t.Errorf("%s(unknown id) error = %v, want ErrNotFound", name, err)
		}
	}
}

func TestStore_InsertUnknownWorkspace(t *testing.T) {
	d := pgtest.Migrated(t)
	s := jobspg.NewStore(d.DB)
	if _, err := s.Insert(t.Context(), "00000000-0000-0000-0000-000000000000", "clone", json.RawMessage(`null`)); err == nil {
		t.Error("Insert(unknown workspace) error = nil, want a foreign key error")
	}
}

// The runner on the real table: a successful job, a failing one, and a queued row that
// Shutdown leaves untouched (AC63, AC94, AC101).
func TestRunnerOnPostgres(t *testing.T) {
	d := pgtest.Migrated(t)
	ws := newWorkspace(t, d.DB)
	r := jobs.New(jobspg.NewStore(d.DB), jobs.Options{Concurrency: 1})
	r.Register("ok", func(context.Context, json.RawMessage) error { return nil })
	r.Register("bad", func(context.Context, json.RawMessage) error { return errors.New("repository not found") })
	block := make(chan struct{})
	r.Register("block", func(ctx context.Context, _ json.RawMessage) error {
		close(block)
		<-ctx.Done()
		return ctx.Err()
	})

	okID, err := r.Enqueue(t.Context(), ws, "ok", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	badID, err := r.Enqueue(t.Context(), ws, "bad", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		Status   string
		Attempts int
		Error    string
	}
	summary := func(id string) outcome {
		rec := load(t, d.DB, id)
		o := outcome{Status: rec.Status, Attempts: rec.Attempts}
		if rec.Error != nil {
			o.Error = *rec.Error
		}
		return o
	}
	if diff := cmp.Diff(outcome{Status: "done", Attempts: 1}, summary(okID)); diff != "" {
		t.Errorf("ok job mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(outcome{Status: "failed", Error: "repository not found"}, summary(badID)); diff != "" {
		t.Errorf("failing job mismatch (-want +got):\n%s", diff)
	}

	runningID, err := r.Enqueue(t.Context(), ws, "block", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-block
	queuedID, err := r.Enqueue(t.Context(), ws, "ok", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Shutdown(jobs.DefaultShutdownTimeout); err != nil {
		t.Fatalf("Shutdown() = %v", err)
	}
	if diff := cmp.Diff(outcome{Status: "failed", Error: "aborted by shutdown: context canceled"}, summary(runningID)); diff != "" {
		t.Errorf("aborted job mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(outcome{Status: "queued"}, summary(queuedID)); diff != "" {
		t.Errorf("queued job after shutdown mismatch (-want +got):\n%s", diff)
	}
}
