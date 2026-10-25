package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/ci/adapters/postgres"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func ptr[T any](v T) *T { return &v }

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// fixture is a migrated database with two workspaces and an agent in each.
type fixture struct {
	db     *gorm.DB
	store  *postgres.Store
	ws     string
	other  string
	agentA string
	agentB string
	agentO string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB)}
	f.ws = f.scan(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = f.scan(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.agentA = f.agent(t, f.ws, "alpha")
	f.agentB = f.agent(t, f.ws, "beta")
	f.agentO = f.agent(t, f.other, "outsider")
	return f
}

func (f fixture) scan(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := f.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (f fixture) agent(t *testing.T, ws, name string) string {
	t.Helper()
	return f.scan(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
		VALUES (?, ?, 'openrouter', 'm', 'p') RETURNING id`, ws, name)
}

// install exports agent to repo with slug and returns the installation id.
func (f fixture) install(t *testing.T, agentID, repo, slug string, version int) string {
	t.Helper()
	err := f.store.UpsertInstallation(t.Context(), domain.NewInstallation{AgentID: agentID, Repo: repo, Slug: slug, ManifestVersion: version})
	if err != nil {
		t.Fatalf("UpsertInstallation(%s, %s) = %v, want nil", agentID, repo, err)
	}
	return f.scan(t, `SELECT id FROM ci_installations WHERE agent_id = ? AND repo = ?`, agentID, repo)
}

func (f fixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := f.db.Raw(`SELECT count(*) FROM ` + table).Scan(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func newRun(f fixture, inst, agent string, runID int64, attempt int, at time.Time) domain.NewRun {
	return domain.NewRun{
		WorkspaceID: f.ws, InstallationID: inst, AgentID: agent,
		GithubRunID: runID, RunAttempt: attempt,
		HeadSHA: "abc123", GithubURL: fmt.Sprintf("https://github.com/acme/api/actions/runs/%d", runID),
		RanAt: at, PRNumber: ptr(7), ManifestVersion: ptr(3),
		Status: domain.RunSucceeded, Model: ptr("openai/gpt-4o"), CostUSD: ptr(0.123456), DurationMS: ptr(4321),
		FindingsCount: 2, Blockers: 1,
	}
}

func TestUpsertInstallation(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	id := f.install(t, f.agentA, "acme/api", "alpha", 1)
	var first time.Time
	if err := f.db.Raw(`SELECT installed_at FROM ci_installations WHERE id = ?`, id).Scan(&first).Error; err != nil {
		t.Fatal(err)
	}

	// A second export of the same (agent, repo) updates slug and version in place.
	if err := f.store.UpsertInstallation(ctx, domain.NewInstallation{AgentID: f.agentA, Repo: "acme/api", Slug: "alpha-2", ManifestVersion: 2}); err != nil {
		t.Fatalf("UpsertInstallation(update) = %v, want nil", err)
	}
	got, ok, err := f.store.FindInstallation(ctx, f.ws, f.agentA, "acme/api")
	if err != nil || !ok {
		t.Fatalf("FindInstallation() = _, %v, %v, want found", ok, err)
	}
	want := domain.Installation{ID: id, AgentID: f.agentA, Repo: "acme/api", TargetType: "gha", InstalledAt: first.UTC(), Slug: ptr("alpha-2"), ManifestVersion: ptr(2)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindInstallation() mismatch (-want +got):\n%s", diff)
	}
	if n := f.count(t, "ci_installations"); n != 1 {
		t.Errorf("ci_installations rows = %d, want 1", n)
	}

	owner, found, err := f.store.SlugOwner(ctx, "acme/api", "alpha-2")
	if err != nil || !found || owner != f.agentA {
		t.Errorf("SlugOwner(alpha-2) = %q, %v, %v, want %q, true, nil", owner, found, err, f.agentA)
	}
	if _, found, err := f.store.SlugOwner(ctx, "acme/api", "nobody"); err != nil || found {
		t.Errorf("SlugOwner(nobody) = _, %v, %v, want false, nil", found, err)
	}
	if _, found, err := f.store.SlugOwner(ctx, "acme/other", "alpha-2"); err != nil || found {
		t.Errorf("SlugOwner(other repo) = _, %v, %v, want false, nil", found, err)
	}
}

func TestUpsertInstallation_SlugTaken(t *testing.T) {
	f := newFixture(t)
	f.install(t, f.agentA, "acme/api", "shared", 1)
	in := domain.NewInstallation{AgentID: f.agentB, Repo: "acme/api", Slug: "shared", ManifestVersion: 1}

	if err := f.store.UpsertInstallation(t.Context(), in); !errors.Is(err, app.ErrSlugTaken) {
		t.Fatalf("UpsertInstallation(taken slug) = %v, want ErrSlugTaken", err)
	}

	// Inside a transaction the lost race does not abort it.
	tx := platformpg.NewTx(f.db)
	err := tx.InTx(t.Context(), func(ctx context.Context) error {
		if err := f.store.UpsertInstallation(ctx, in); !errors.Is(err, app.ErrSlugTaken) {
			t.Errorf("UpsertInstallation(taken slug in tx) = %v, want ErrSlugTaken", err)
		}
		in.Slug = "free"
		return f.store.UpsertInstallation(ctx, in)
	})
	if err != nil {
		t.Fatalf("transaction after ErrSlugTaken = %v, want nil", err)
	}
	if n := f.count(t, "ci_installations"); n != 2 {
		t.Errorf("ci_installations rows = %d, want 2", n)
	}
}

func TestRecordRun(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	inst := f.install(t, f.agentA, "acme/api", "alpha", 3)

	first, err := f.store.RecordRun(ctx, newRun(f, inst, f.agentA, 100, 1, t0))
	if err != nil || !first.Stored {
		t.Fatalf("RecordRun() = %+v, %v, want Stored, nil", first, err)
	}
	// The returned id is the agent_runs row the ci_runs row links to.
	var linked string
	if err := f.db.Raw(`SELECT agent_run_id::text FROM ci_runs WHERE github_run_id = 100 AND run_attempt = 1`).Scan(&linked).Error; err != nil {
		t.Fatal(err)
	}
	if first.AgentRunID == "" || first.AgentRunID != linked {
		t.Errorf("RecordRun().AgentRunID = %q, want the linked agent run %q", first.AgentRunID, linked)
	}
	failed := newRun(f, inst, f.agentA, 101, 1, t0.Add(time.Hour))
	failed.Status, failed.Model, failed.CostUSD, failed.DurationMS, failed.FindingsCount, failed.Blockers = domain.RunFailed, nil, nil, nil, 0, 0
	failed.Error = ptr(domain.FailedRunError)
	if rec, err := f.store.RecordRun(ctx, failed); err != nil || !rec.Stored || rec.AgentRunID == "" {
		t.Fatalf("RecordRun(failed) = %+v, %v, want Stored with an agent run id, nil", rec, err)
	}

	// The same attempt again stores nothing; a re-run attempt is a separate run.
	if rec, err := f.store.RecordRun(ctx, newRun(f, inst, f.agentA, 100, 1, t0)); err != nil || rec != (app.Recorded{}) {
		t.Errorf("RecordRun(duplicate) = %+v, %v, want the zero Recorded, nil", rec, err)
	}
	if rec, err := f.store.RecordRun(ctx, newRun(f, inst, f.agentA, 100, 2, t0)); err != nil || !rec.Stored || rec.AgentRunID == first.AgentRunID {
		t.Errorf("RecordRun(attempt 2) = %+v, %v, want Stored with its own agent run id, nil", rec, err)
	}
	if ci, ag := f.count(t, "ci_runs"), f.count(t, "agent_runs"); ci != 3 || ag != 3 {
		t.Errorf("rows ci_runs, agent_runs = %d, %d, want 3, 3 (a duplicate leaves no agent_runs row)", ci, ag)
	}

	type agentRun struct {
		Provider, Source, Status string
		Model                    *string
		DurationMS, Blockers     *int
		FindingsCount            *int
		Error                    *string
		PRID                     *string
		CostUSD                  *float64
		Linked                   bool
	}
	read := func(runID int64, attempt int) agentRun {
		var r struct {
			Provider, Source, Status string
			Model                    *string
			DurationMS, Blockers     *int
			FindingsCount            *int
			Error                    *string
			PRID                     *string
			CostUSD                  *float64
			RanOK                    bool
		}
		err := f.db.Raw(`SELECT a.provider, a.source, a.status, a.model, a.duration_ms, a.blockers, a.findings_count,
			a.error, a.pr_id, a.cost_usd, a.ran_at = c.ran_at AS ran_ok
			FROM ci_runs c JOIN agent_runs a ON a.id = c.agent_run_id WHERE c.github_run_id = ? AND c.run_attempt = ?`, runID, attempt).Scan(&r).Error
		if err != nil {
			t.Fatalf("read agent run of %d/%d: %v", runID, attempt, err)
		}
		return agentRun{r.Provider, r.Source, r.Status, r.Model, r.DurationMS, r.Blockers, r.FindingsCount, r.Error, r.PRID, r.CostUSD, r.RanOK}
	}
	wantOK := agentRun{Provider: "openrouter", Source: "ci", Status: "done", Model: ptr("openai/gpt-4o"), DurationMS: ptr(4321),
		Blockers: ptr(1), FindingsCount: ptr(2), CostUSD: ptr(0.123456), Linked: true}
	if diff := cmp.Diff(wantOK, read(100, 1)); diff != "" {
		t.Errorf("agent run of 100/1 mismatch (-want +got):\n%s", diff)
	}
	wantFailed := agentRun{Provider: "openrouter", Source: "ci", Status: "failed", Blockers: ptr(0), FindingsCount: ptr(0),
		Error: ptr(domain.FailedRunError), Linked: true}
	if diff := cmp.Diff(wantFailed, read(101, 1)); diff != "" {
		t.Errorf("agent run of 101/1 mismatch (-want +got):\n%s", diff)
	}

	keys := []domain.RunKey{{GithubRunID: 100, Attempt: 1}, {GithubRunID: 100, Attempt: 2}, {GithubRunID: 100, Attempt: 3}, {GithubRunID: 999, Attempt: 1}}
	got, err := f.store.StoredRunKeys(ctx, keys)
	if err != nil {
		t.Fatalf("StoredRunKeys() = _, %v", err)
	}
	want := map[domain.RunKey]bool{{GithubRunID: 100, Attempt: 1}: true, {GithubRunID: 100, Attempt: 2}: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("StoredRunKeys() mismatch (-want +got):\n%s", diff)
	}
	if got, err := f.store.StoredRunKeys(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("StoredRunKeys(nil) = %v, %v, want empty, nil", got, err)
	}
}

func TestRecordRun_JoinsCallerTransaction(t *testing.T) {
	f := newFixture(t)
	inst := f.install(t, f.agentA, "acme/api", "alpha", 3)
	sentinel := errors.New("roll back")
	var id string
	err := platformpg.NewTx(f.db).InTx(t.Context(), func(ctx context.Context) error {
		rec, err := f.store.RecordRun(ctx, newRun(f, inst, f.agentA, 100, 1, t0))
		if err != nil || !rec.Stored || rec.AgentRunID == "" {
			t.Errorf("RecordRun() = %+v, %v, want Stored with an agent run id, nil", rec, err)
		}
		id = rec.AgentRunID
		// The caller's own writes see the agent run (the ledger entry of the run is written here).
		var n int64
		if err := platformpg.Conn(ctx, f.db).Table("agent_runs").Where("id = ?", id).Count(&n).Error; err != nil || n != 1 {
			t.Errorf("agent run %q visible in the caller tx = %d, %v, want 1, nil", id, n, err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx() = %v, want the sentinel", err)
	}
	if ci, ag := f.count(t, "ci_runs"), f.count(t, "agent_runs"); ci != 0 || ag != 0 {
		t.Errorf("rows after rollback ci_runs, agent_runs = %d, %d, want 0, 0", ci, ag)
	}
}

// A caller that commits keeps the run and the id it was handed (the ledger entry is written in
// the same transaction).
func TestRecordRun_JoinedTransactionCommits(t *testing.T) {
	f := newFixture(t)
	inst := f.install(t, f.agentA, "acme/api", "alpha", 3)
	var id string
	err := platformpg.NewTx(f.db).InTx(t.Context(), func(ctx context.Context) error {
		rec, err := f.store.RecordRun(ctx, newRun(f, inst, f.agentA, 100, 1, t0))
		id = rec.AgentRunID
		return err
	})
	if err != nil {
		t.Fatalf("InTx() = %v, want nil", err)
	}
	var linked string
	if err := f.db.Raw(`SELECT agent_run_id::text FROM ci_runs WHERE github_run_id = 100`).Scan(&linked).Error; err != nil {
		t.Fatal(err)
	}
	if id == "" || id != linked {
		t.Errorf("agent run id handed to the caller = %q, want the committed link %q", id, linked)
	}
}

// TestRecordRun_NotStorable: values a column cannot hold are refused before any write, with the
// typed error the ingest turns into artifact_invalid (a database error would repeat on every
// refresh).
func TestRecordRun_NotStorable(t *testing.T) {
	f := newFixture(t)
	inst := f.install(t, f.agentA, "acme/api", "alpha", 3)
	tests := []struct {
		name string
		edit func(*domain.NewRun)
	}{
		{"pr number above int32", func(r *domain.NewRun) { r.PRNumber = ptr(math.MaxInt32 + 1) }},
		{"manifest version below int32", func(r *domain.NewRun) { r.ManifestVersion = ptr(math.MinInt32 - 1) }},
		{"duration above int32", func(r *domain.NewRun) { r.DurationMS = ptr(1 << 40) }},
		{"findings above int32", func(r *domain.NewRun) { r.FindingsCount = math.MaxInt32 + 1 }},
		{"blockers above int32", func(r *domain.NewRun) { r.Blockers = 1 << 53 }},
		{"attempt above int32", func(r *domain.NewRun) { r.RunAttempt = math.MaxInt32 + 1 }},
		{"cost overflows numeric(12,6)", func(r *domain.NewRun) { r.CostUSD = ptr(1e6) }},
		{"cost rounds up to overflow", func(r *domain.NewRun) { r.CostUSD = ptr(999999.9999996) }},
		{"cost negative overflow", func(r *domain.NewRun) { r.CostUSD = ptr(-1e9) }},
		{"cost NaN", func(r *domain.NewRun) { r.CostUSD = ptr(math.NaN()) }},
		{"cost infinite", func(r *domain.NewRun) { r.CostUSD = ptr(math.Inf(1)) }},
		{"model with NUL", func(r *domain.NewRun) { r.Model = ptr("gpt\x00") }},
		{"model invalid UTF-8", func(r *domain.NewRun) { r.Model = ptr("gpt\xff") }},
		{"error with NUL", func(r *domain.NewRun) { r.Error = ptr("a\x00b") }},
		{"head sha with NUL", func(r *domain.NewRun) { r.HeadSHA = "a\x00" }},
		{"url invalid UTF-8", func(r *domain.NewRun) { r.GithubURL = "https://x/\xc3\x28" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := newRun(f, inst, f.agentA, 500, 1, t0)
			tc.edit(&run)
			rec, err := f.store.RecordRun(t.Context(), run)
			if !errors.Is(err, app.ErrNotStorable) || rec != (app.Recorded{}) {
				t.Errorf("RecordRun() = %+v, %v, want the zero Recorded, ErrNotStorable", rec, err)
			}
		})
	}
	if ci, ag := f.count(t, "ci_runs"), f.count(t, "agent_runs"); ci != 0 || ag != 0 {
		t.Errorf("rows ci_runs, agent_runs = %d, %d, want 0, 0", ci, ag)
	}

	// Edge values the columns do hold are stored.
	edge := newRun(f, inst, f.agentA, 501, 1, t0)
	edge.PRNumber, edge.DurationMS, edge.FindingsCount = ptr(math.MaxInt32), ptr(math.MinInt32), math.MaxInt32
	edge.CostUSD, edge.Model = ptr(999999.999999), ptr("ünï code")
	if rec, err := f.store.RecordRun(t.Context(), edge); err != nil || !rec.Stored {
		t.Errorf("RecordRun(edge values) = %+v, %v, want Stored, nil", rec, err)
	}
}

func TestUpsertInstallation_NotStorable(t *testing.T) {
	f := newFixture(t)
	for name, in := range map[string]domain.NewInstallation{
		"version above int32": {AgentID: f.agentA, Repo: "acme/api", Slug: "a", ManifestVersion: math.MaxInt32 + 1},
		"slug with NUL":       {AgentID: f.agentA, Repo: "acme/api", Slug: "a\x00", ManifestVersion: 1},
		"repo invalid UTF-8":  {AgentID: f.agentA, Repo: "acme/\xff", Slug: "a", ManifestVersion: 1},
	} {
		if err := f.store.UpsertInstallation(t.Context(), in); !errors.Is(err, app.ErrNotStorable) {
			t.Errorf("UpsertInstallation(%s) = %v, want ErrNotStorable", name, err)
		}
	}
}

func TestListRuns(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	instA := f.install(t, f.agentA, "acme/api", "alpha", 3)
	instB := f.install(t, f.agentB, "acme/web", "beta", 1)
	mustRecord := func(r domain.NewRun) {
		t.Helper()
		if rec, err := f.store.RecordRun(ctx, r); err != nil || !rec.Stored {
			t.Fatalf("RecordRun(%d) = %+v, %v, want Stored, nil", r.GithubRunID, rec, err)
		}
	}
	mustRecord(newRun(f, instA, f.agentA, 1, 1, t0))
	mustRecord(newRun(f, instA, f.agentA, 2, 1, t0.Add(2*time.Hour)))
	b := newRun(f, instB, f.agentB, 3, 1, t0.Add(time.Hour))
	b.PRNumber, b.CostUSD, b.DurationMS, b.Model = nil, nil, nil, nil
	mustRecord(b)
	// A run of another workspace never shows.
	foreign := newRun(f, instA, f.agentO, 4, 1, t0.Add(3*time.Hour))
	foreign.WorkspaceID = f.other
	mustRecord(foreign)

	runs, err := f.store.ListRuns(ctx, f.ws, app.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns() = _, %v", err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, *r.GithubURL)
	}
	wantIDs := []string{
		"https://github.com/acme/api/actions/runs/2",
		"https://github.com/acme/api/actions/runs/3",
		"https://github.com/acme/api/actions/runs/1",
	}
	if diff := cmp.Diff(wantIDs, ids); diff != "" {
		t.Fatalf("ListRuns() order mismatch (-want +got):\n%s", diff)
	}
	want := domain.Run{
		InstallationID: &instA, PRNumber: ptr(7), RanAt: ptr(t0.Add(2 * time.Hour)), Status: ptr("succeeded"), FindingsCount: ptr(2),
		CostUSD: ptr(0.123456), GithubURL: ptr("https://github.com/acme/api/actions/runs/2"), Source: ptr("gha"),
		AgentName: ptr("alpha"), DurationMS: ptr(4321), AgentID: &f.agentA, Repo: ptr("acme/api"), Blockers: ptr(1),
		HeadSHA: ptr("abc123"), ManifestVersion: ptr(3),
	}
	if diff := cmp.Diff(want, runs[0], cmpopts.IgnoreFields(domain.Run{}, "ID")); diff != "" {
		t.Errorf("ListRuns()[0] mismatch (-want +got):\n%s", diff)
	}
	if runs[0].ID == "" {
		t.Error("ListRuns()[0].ID is empty")
	}
	if got := runs[1]; got.PRNumber != nil || got.CostUSD != nil || got.DurationMS != nil {
		t.Errorf("ListRuns()[1] = %+v, want null pr_number, cost_usd, duration_ms", got)
	}

	for name, tc := range map[string]struct {
		f    app.RunFilter
		want int
	}{
		"limit":        {app.RunFilter{Limit: 2}, 2},
		"agent filter": {app.RunFilter{AgentID: f.agentB, Limit: 100}, 1},
		"other agent":  {app.RunFilter{AgentID: f.agentO, Limit: 100}, 0},
	} {
		got, err := f.store.ListRuns(ctx, f.ws, tc.f)
		if err != nil || len(got) != tc.want {
			t.Errorf("ListRuns(%s) = %d runs, %v, want %d", name, len(got), err, tc.want)
		}
	}
	if got, err := f.store.ListRuns(ctx, f.other, app.RunFilter{Limit: 100}); err != nil || len(got) != 1 {
		t.Errorf("ListRuns(other workspace) = %d runs, %v, want 1", len(got), err)
	}
}

// TestListRuns_DeletedAgent: deleting the agent removes its installations; its runs stay with a
// null agent, installation and repo (EC6043).
func TestListRuns_DeletedAgent(t *testing.T) {
	f := newFixture(t)
	inst := f.install(t, f.agentA, "acme/api", "alpha", 3)
	if rec, err := f.store.RecordRun(t.Context(), newRun(f, inst, f.agentA, 1, 1, t0)); err != nil || !rec.Stored {
		t.Fatalf("RecordRun() = %+v, %v", rec, err)
	}
	if err := f.db.Exec(`DELETE FROM agents WHERE id = ?`, f.agentA).Error; err != nil {
		t.Fatal(err)
	}
	runs, err := f.store.ListRuns(t.Context(), f.ws, app.RunFilter{Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns() = %d runs, %v, want 1", len(runs), err)
	}
	r := runs[0]
	if r.AgentID != nil || r.AgentName != nil || r.InstallationID != nil || r.Repo != nil || r.GithubURL == nil {
		t.Errorf("ListRuns()[0] = %+v, want null agent, installation and repo, url kept", r)
	}
	if got, err := f.store.ListInstallations(t.Context(), f.ws, app.InstallationFilter{}); err != nil || len(got) != 0 {
		t.Errorf("ListInstallations() = %d, %v, want 0", len(got), err)
	}
}

func TestListInstallations(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	older := f.install(t, f.agentA, "acme/api", "alpha", 3)
	newer := f.install(t, f.agentA, "acme/web", "alpha-web", 3)
	other := f.install(t, f.agentB, "acme/api", "beta", 1)
	f.install(t, f.agentO, "acme/api", "outsider", 1)
	// Order by installed_at, newest first.
	for id, d := range map[string]time.Duration{older: 0, newer: time.Hour, other: 2 * time.Hour} {
		if err := f.db.Exec(`UPDATE ci_installations SET installed_at = ? WHERE id = ?`, t0.Add(d), id).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A non-gha installation never shows.
	f.scan(t, `INSERT INTO ci_installations (agent_id, repo, target_type) VALUES (?, 'acme/api', 'cli') RETURNING id`, f.agentA)

	for _, r := range []domain.NewRun{
		newRun(f, older, f.agentA, 1, 1, t0),
		newRun(f, older, f.agentA, 2, 1, t0.Add(time.Hour)),
		newRun(f, older, f.agentA, 3, 1, t0.Add(30*time.Minute)),
	} {
		if rec, err := f.store.RecordRun(ctx, r); err != nil || !rec.Stored {
			t.Fatalf("RecordRun(%d) = %+v, %v", r.GithubRunID, rec, err)
		}
	}

	got, err := f.store.ListInstallations(ctx, f.ws, app.InstallationFilter{})
	if err != nil {
		t.Fatalf("ListInstallations() = _, %v", err)
	}
	var order []string
	for _, i := range got {
		order = append(order, i.ID)
	}
	if diff := cmp.Diff([]string{other, newer, older}, order); diff != "" {
		t.Fatalf("ListInstallations() order mismatch (-want +got):\n%s", diff)
	}
	if got[0].LastRun != nil || got[1].LastRun != nil {
		t.Errorf("installations without runs have last_run %v, %v, want nil", got[0].LastRun, got[1].LastRun)
	}
	last := got[2].LastRun
	if last == nil || *last.GithubURL != "https://github.com/acme/api/actions/runs/2" || *last.Repo != "acme/api" || *last.AgentName != "alpha" {
		t.Errorf("last run of the older installation = %+v, want run 2", last)
	}

	byAgent, err := f.store.ListInstallations(ctx, f.ws, app.InstallationFilter{AgentID: f.agentA})
	if err != nil || len(byAgent) != 2 {
		t.Errorf("ListInstallations(agent A) = %d, %v, want 2", len(byAgent), err)
	}
	if _, ok, err := f.store.FindInstallation(ctx, f.ws, f.agentO, "acme/api"); err != nil || ok {
		t.Errorf("FindInstallation(other workspace's agent) = %v, %v, want not found", ok, err)
	}
	if inst, ok, err := f.store.FindInstallation(ctx, f.ws, f.agentA, "acme/api"); err != nil || !ok || inst.LastRun == nil {
		t.Errorf("FindInstallation(A, api) = %+v, %v, %v, want found with last run", inst, ok, err)
	}
	// Runs of another workspace are not the last run.
	foreign := newRun(f, older, f.agentO, 9, 1, t0.Add(10*time.Hour))
	foreign.WorkspaceID = f.other
	if rec, err := f.store.RecordRun(ctx, foreign); err != nil || !rec.Stored {
		t.Fatalf("RecordRun(foreign) = %+v, %v", rec, err)
	}
	inst, _, _ := f.store.FindInstallation(ctx, f.ws, f.agentA, "acme/api")
	if inst.LastRun == nil || *inst.LastRun.GithubURL != "https://github.com/acme/api/actions/runs/2" {
		t.Errorf("last run after a foreign run = %+v, want run 2", inst.LastRun)
	}
}

// TestRecordRun_ConcurrentRefreshes (AC6045): two writers of the same run, both held inside the
// insert, store it once. The held transaction is the first writer's; the second waits on its
// uncommitted unique key and then inserts nothing.
func TestRecordRun_ConcurrentRefreshes(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	inst := f.install(t, f.agentA, "acme/api", "alpha", 3)

	const key = "ci-record-run"
	mustExec(t, f.db, `CREATE FUNCTION test_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		PERFORM pg_advisory_xact_lock(hashtextextended('`+key+`', 0)); RETURN NULL; END $$`)
	mustExec(t, f.db, `CREATE TRIGGER test_pause AFTER INSERT ON ci_runs FOR EACH STATEMENT EXECUTE FUNCTION test_pause()`)

	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, key); err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if _, err := conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key); err != nil {
				t.Errorf("release lock: %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Errorf("close lock connection: %v", err)
			}
		})
	}
	t.Cleanup(release)

	results := make(chan bool, 2)
	errs := make(chan error, 2)
	record := func() {
		rec, err := f.store.RecordRun(ctx, newRun(f, inst, f.agentA, 77, 1, t0))
		results <- rec.Stored
		errs <- err
	}
	go record()
	waitFor(t, f.db, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`, 1)
	go record()
	// The second insert waits for the first one's transaction (its unique key is uncommitted).
	waitFor(t, f.db, `SELECT count(*) FROM pg_locks WHERE locktype = 'transactionid' AND NOT granted`, 1)
	release()

	stored := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("RecordRun() = %v, want nil", err)
		}
		if <-results {
			stored++
		}
	}
	if stored != 1 {
		t.Errorf("runs stored by two concurrent refreshes = %d, want 1", stored)
	}
	if ci, ag := f.count(t, "ci_runs"), f.count(t, "agent_runs"); ci != 1 || ag != 1 {
		t.Errorf("rows ci_runs, agent_runs = %d, %d, want 1, 1", ci, ag)
	}
}

func mustExec(t *testing.T, db *gorm.DB, query string) {
	t.Helper()
	if err := db.Exec(query).Error; err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// waitFor polls a count query until it reaches n: Postgres has no event for a session starting to
// wait on a lock.
func waitFor(t *testing.T, db *gorm.DB, query string, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var got int
		if err := db.Raw(query).Scan(&got).Error; err != nil {
			t.Fatalf("poll %q: %v", query, err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q = %d, want >= %d", query, got, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
