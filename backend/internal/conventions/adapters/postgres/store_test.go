package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	pgstore "dev-digest-go/backend/internal/conventions/adapters/postgres"
	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

type env struct {
	db    *gorm.DB
	store *pgstore.Store
	ws    string
	repo  string
}

// newEnv returns a migrated database with a workspace and one repo without clone.
func newEnv(t *testing.T) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	e := &env{db: d.DB, store: pgstore.NewStore(d.DB)}
	if err := d.DB.Raw(`INSERT INTO workspaces (name) VALUES ('default') RETURNING id`).Scan(&e.ws).Error; err != nil {
		t.Fatal(err)
	}
	e.repo = e.addRepo(t, "o/r", "")
	return e
}

// addRepo inserts a repo; clonePath "" leaves clone_path NULL.
func (e *env) addRepo(t *testing.T, fullName, clonePath string) string {
	t.Helper()
	var clone *string
	if clonePath != "" {
		clone = &clonePath
	}
	var id string
	err := e.db.Raw(`INSERT INTO repos (workspace_id, owner, name, full_name, clone_path)
		VALUES (?, 'o', ?, ?, ?) RETURNING id`, e.ws, fullName, fullName, clone).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type convSpec struct {
	rule       string
	status     string
	confidence *float64
	edited     bool
	category   string
	createdAt  time.Time
}

func f(v float64) *float64 { return &v }

func (e *env) addConvention(t *testing.T, repo string, c convSpec) string {
	t.Helper()
	if c.status == "" {
		c.status = "pending"
	}
	if c.category == "" {
		c.category = "style"
	}
	if c.createdAt.IsZero() {
		c.createdAt = time.Now()
	}
	var id string
	err := e.db.Raw(`INSERT INTO conventions (workspace_id, repo_id, rule, confidence, category, status, edited, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		e.ws, repo, c.rule, c.confidence, c.category, c.status, c.edited, c.createdAt, c.createdAt).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// addScan inserts a scan row with the given status and start time.
func (e *env) addScan(t *testing.T, repo, status string, startedAt time.Time) string {
	t.Helper()
	var id string
	err := e.db.Raw(`INSERT INTO convention_scans (workspace_id, repo_id, status, started_at)
		VALUES (?, ?, ?, ?) RETURNING id`, e.ws, repo, status, startedAt).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// scanRec reads a convention_scans row for assertions, independently of the store under test.
type scanRec struct {
	ID, WorkspaceID, RepoID, Status string
	SampledFiles, Dropped           []byte
	Proposed, Kept                  int
	Model                           *string
	CostUSD                         *float64
	Error                           *string
	StartedAt                       time.Time
	FinishedAt                      *time.Time
}

func (e *env) scan(t *testing.T, id string) domain.Scan {
	t.Helper()
	var r scanRec
	if err := e.db.Raw(`SELECT id, workspace_id, repo_id, status, sampled_files, dropped, proposed, kept,
		model, cost_usd, error, started_at, finished_at FROM convention_scans WHERE id = ?`, id).Scan(&r).Error; err != nil {
		t.Fatalf("load scan %s: %v", id, err)
	}
	if r.ID == "" {
		t.Fatalf("scan %s does not exist", id)
	}
	s := domain.Scan{
		ID: r.ID, WorkspaceID: r.WorkspaceID, RepoID: r.RepoID, Status: domain.ScanStatus(r.Status),
		Proposed: r.Proposed, Kept: r.Kept, Model: r.Model, CostUSD: r.CostUSD, Error: r.Error,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	if err := json.Unmarshal(r.SampledFiles, &s.SampledFiles); err != nil {
		t.Fatalf("decode sampled_files: %v", err)
	}
	if err := json.Unmarshal(r.Dropped, &s.Dropped); err != nil {
		t.Fatalf("decode dropped: %v", err)
	}
	return s
}

func rules(cs []domain.Convention) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Rule
	}
	return out
}

func TestGet_RepoLookup(t *testing.T) {
	e := newEnv(t)
	cloned := e.addRepo(t, "o/cloned", "/clones/o/cloned")
	tests := []struct {
		name string
		ws   string
		id   string
		want app.Repo
		err  error
	}{
		{"not cloned", e.ws, e.repo, app.Repo{ID: e.repo, FullName: "o/r"}, nil},
		{"cloned", e.ws, cloned, app.Repo{ID: cloned, FullName: "o/cloned", ClonePath: "/clones/o/cloned"}, nil},
		{"other workspace", "00000000-0000-0000-0000-000000000001", cloned, app.Repo{}, domain.ErrNotFound},
		{"unknown", e.ws, "00000000-0000-0000-0000-000000000002", app.Repo{}, domain.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.store.Get(t.Context(), tc.ws, tc.id)
			if !errors.Is(err, tc.err) {
				t.Fatalf("Get() error = %v, want %v", err, tc.err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Get() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStartScan_OneRunningScanPerRepo(t *testing.T) {
	e := newEnv(t)
	other := e.addRepo(t, "o/other", "")
	now := time.Now()

	first, err := e.store.StartScan(t.Context(), e.ws, e.repo, now)
	if err != nil {
		t.Fatalf("StartScan() error = %v", err)
	}
	if first.Status != domain.ScanRunning || first.ID == "" || len(first.SampledFiles) != 0 || first.Proposed != 0 || first.FinishedAt != nil {
		t.Errorf("StartScan() = %+v, want an empty running scan", first)
	}
	if _, err := e.store.StartScan(t.Context(), e.ws, e.repo, now); !errors.Is(err, domain.ErrScanRunning) {
		t.Errorf("second StartScan() error = %v, want ErrScanRunning", err)
	}
	if _, err := e.store.StartScan(t.Context(), e.ws, other, now); err != nil {
		t.Errorf("StartScan(other repo) error = %v, want nil", err)
	}
	if err := e.store.FailScan(t.Context(), first.ID, "x", nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.StartScan(t.Context(), e.ws, e.repo, now); err != nil {
		t.Errorf("StartScan() after the first ended: error = %v, want nil", err)
	}
}

func TestStartScan_ConcurrentCallsYieldExactlyOneScan(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Go(func() { _, errs[i] = e.store.StartScan(t.Context(), e.ws, e.repo, time.Now()) })
	}
	wg.Wait()
	ok, running := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrScanRunning):
			running++
		default:
			t.Errorf("StartScan() error = %v", err)
		}
	}
	if ok != 1 || running != len(errs)-1 {
		t.Errorf("results: %d started, %d conflicts, want 1 and %d", ok, running, len(errs)-1)
	}
}

func TestFailStaleScans(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	old := e.addScan(t, e.repo, "running", now.Add(-11*time.Minute))
	otherRepo := e.addRepo(t, "o/other", "")
	otherOld := e.addScan(t, otherRepo, "running", now.Add(-11*time.Minute))
	if err := e.store.FailStaleScans(t.Context(), e.repo, now.Add(-domain.ScanStaleAfter), "The scan was interrupted", now); err != nil {
		t.Fatalf("FailStaleScans() error = %v", err)
	}
	s := e.scan(t, old)
	if s.Status != domain.ScanFailed || s.Error == nil || *s.Error != "The scan was interrupted" || s.FinishedAt == nil {
		t.Errorf("old scan = %+v, want failed, interrupted, finished", s)
	}
	if got := e.scan(t, otherOld); got.Status != domain.ScanRunning {
		t.Errorf("scan of another repo status = %s, want running", got.Status)
	}
	young := e.addScan(t, e.repo, "running", now.Add(-time.Minute))
	if err := e.store.FailStaleScans(t.Context(), e.repo, now.Add(-domain.ScanStaleAfter), "x", now); err != nil {
		t.Fatal(err)
	}
	if got := e.scan(t, young); got.Status != domain.ScanRunning {
		t.Errorf("1-minute-old scan status = %s, want running", got.Status)
	}
}

func TestLatestScan(t *testing.T) {
	e := newEnv(t)
	if _, err := e.store.LatestScan(t.Context(), e.repo); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("LatestScan() of an unscanned repo error = %v, want ErrNotFound", err)
	}
	now := time.Now()
	e.addScan(t, e.repo, "done", now.Add(-time.Hour))
	newer := e.addScan(t, e.repo, "failed", now)
	got, err := e.store.LatestScan(t.Context(), e.repo)
	if err != nil || got.ID != newer {
		t.Errorf("LatestScan() = %v, %v, want scan %s", got.ID, err, newer)
	}
}

func TestList_OrdersByStatusConfidenceThenCreation(t *testing.T) {
	e := newEnv(t)
	base := time.Now().Add(-time.Hour)
	e.addConvention(t, e.repo, convSpec{rule: "pending 0.9", status: "pending", confidence: f(0.9), createdAt: base})
	e.addConvention(t, e.repo, convSpec{rule: "accepted 0.5", status: "accepted", confidence: f(0.5), createdAt: base})
	e.addConvention(t, e.repo, convSpec{rule: "rejected 0.8", status: "rejected", confidence: f(0.8), createdAt: base})
	e.addConvention(t, e.repo, convSpec{rule: "pending null", status: "pending", createdAt: base})
	e.addConvention(t, e.repo, convSpec{rule: "pending 0.9 later", status: "pending", confidence: f(0.9), createdAt: base.Add(time.Minute)})
	e.addConvention(t, e.repo, convSpec{rule: "accepted 0.7", status: "accepted", confidence: f(0.7), createdAt: base})

	got, err := e.store.List(t.Context(), e.repo)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := []string{"accepted 0.7", "accepted 0.5", "pending 0.9", "pending 0.9 later", "pending null", "rejected 0.8"}
	if diff := cmp.Diff(want, rules(got)); diff != "" {
		t.Errorf("List() order mismatch (-want +got):\n%s", diff)
	}
	for _, c := range got {
		if c.Rule == "pending null" && c.Confidence != 0 {
			t.Errorf("NULL confidence read as %v, want 0", c.Confidence)
		}
	}
}

func completion(e *env, scanID string) (app.ScanRef, []domain.KeptConvention, app.Summary) {
	cost := 0.0123
	kept := []domain.KeptConvention{
		{Category: "naming", Rule: "First <rule> & more", Confidence: 0.9,
			Evidence: []domain.Evidence{{Path: "src/a.ts", StartLine: 3, EndLine: 4, Snippet: "  line <1>\nline 2"}}},
		{Category: "async", Rule: "Second rule", Confidence: 0.4,
			Evidence: []domain.Evidence{{Path: "src/b.ts", StartLine: 1, EndLine: 1, Snippet: "x"}}},
	}
	sum := app.Summary{
		SampledFiles: []string{"package.json", "src/a.ts"}, Proposed: 3, Kept: 2,
		Dropped: []domain.Dropped{{Rule: "Third", Path: "src/c.ts", Reason: domain.ReasonFileNotFound}},
		Model:   "gpt-5.4", TokensIn: 1000, TokensOut: 200, CostUSD: &cost,
	}
	return app.ScanRef{ID: scanID, WorkspaceID: e.ws, RepoID: e.repo}, kept, sum
}

func TestCompleteScan_ReplacesOnlyPendingUneditedRules(t *testing.T) {
	e := newEnv(t)
	e.addConvention(t, e.repo, convSpec{rule: "A accepted", status: "accepted", confidence: f(0.1)})
	e.addConvention(t, e.repo, convSpec{rule: "R rejected", status: "rejected", confidence: f(0.1)})
	e.addConvention(t, e.repo, convSpec{rule: "E edited", status: "pending", edited: true, confidence: f(0.1)})
	e.addConvention(t, e.repo, convSpec{rule: "P pending", status: "pending", confidence: f(0.1)})
	other := e.addRepo(t, "o/other", "")
	e.addConvention(t, other, convSpec{rule: "other repo pending", status: "pending"})
	scanID := e.addScan(t, e.repo, "running", time.Now())
	ref, kept, sum := completion(e, scanID)
	at := time.Now().Truncate(time.Millisecond)

	if err := e.store.CompleteScan(t.Context(), ref, kept, sum, at); err != nil {
		t.Fatalf("CompleteScan() error = %v", err)
	}

	got, err := e.store.List(t.Context(), e.repo)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"A accepted", "First <rule> & more", "Second rule", "E edited", "R rejected"}, rules(got)); diff != "" {
		t.Errorf("conventions after the scan mismatch (-want +got):\n%s", diff)
	}
	for _, c := range got {
		switch c.Rule {
		case "First <rule> & more":
			if c.Status != domain.StatusPending || c.Edited || c.ScanID == nil || *c.ScanID != scanID || c.Confidence != 0.9 || c.Category != "naming" {
				t.Errorf("new convention = %+v, want pending, unedited, scan %s, confidence 0.9, naming", c, scanID)
			}
			if diff := cmp.Diff(kept[0].Evidence, c.Evidence); diff != "" {
				t.Errorf("evidence mismatch (-want +got):\n%s", diff)
			}
		case "A accepted", "R rejected", "E edited":
			if c.ScanID != nil {
				t.Errorf("%s got scan %s, want it untouched", c.Rule, *c.ScanID)
			}
		}
	}
	if others, _ := e.store.List(t.Context(), other); len(others) != 1 {
		t.Errorf("other repo has %d conventions, want 1 (untouched)", len(others))
	}

	s := e.scan(t, scanID)
	model, cost := "gpt-5.4", 0.0123
	wantScan := domain.Scan{
		ID: scanID, WorkspaceID: e.ws, RepoID: e.repo, Status: domain.ScanDone,
		SampledFiles: []string{"package.json", "src/a.ts"}, Proposed: 3, Kept: 2, Dropped: sum.Dropped,
		Model: &model, CostUSD: &cost, FinishedAt: &at,
	}
	opts := []cmp.Option{cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })}
	s.StartedAt = time.Time{}
	if diff := cmp.Diff(wantScan, s, opts...); diff != "" {
		t.Errorf("scan mismatch (-want +got):\n%s", diff)
	}
	var tokens struct{ TokensIn, TokensOut int }
	if err := e.db.Raw(`SELECT tokens_in, tokens_out FROM convention_scans WHERE id = ?`, scanID).Scan(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if tokens.TokensIn != 1000 || tokens.TokensOut != 200 {
		t.Errorf("tokens = %+v, want 1000/200", tokens)
	}
}

func TestCompleteScan_IsAtomic(t *testing.T) {
	e := newEnv(t)
	e.addConvention(t, e.repo, convSpec{rule: "P pending", status: "pending"})
	scanID := e.addScan(t, e.repo, "running", time.Now())
	ref, kept, sum := completion(e, scanID)
	kept[1].Category = "not-a-category" // violates conventions_category_chk after the delete

	if err := e.store.CompleteScan(t.Context(), ref, kept, sum, time.Now()); err == nil {
		t.Fatal("CompleteScan() error = nil, want the check violation")
	}
	got, _ := e.store.List(t.Context(), e.repo)
	if diff := cmp.Diff([]string{"P pending"}, rules(got)); diff != "" {
		t.Errorf("conventions after a failed completion mismatch (-want +got):\n%s", diff)
	}
	if s := e.scan(t, scanID); s.Status != domain.ScanRunning {
		t.Errorf("scan status = %s, want running (rolled back)", s.Status)
	}
}

func TestCompleteScan_ScanNoLongerRunningChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.addConvention(t, e.repo, convSpec{rule: "P pending", status: "pending"})
	scanID := e.addScan(t, e.repo, "failed", time.Now())
	ref, kept, sum := completion(e, scanID)
	if err := e.store.CompleteScan(t.Context(), ref, kept, sum, time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("CompleteScan() error = %v, want ErrNotFound", err)
	}
	got, _ := e.store.List(t.Context(), e.repo)
	if diff := cmp.Diff([]string{"P pending"}, rules(got)); diff != "" {
		t.Errorf("conventions mismatch (-want +got):\n%s", diff)
	}
}

func TestCompleteScan_WithNothingKeptStillClearsPendingRules(t *testing.T) {
	e := newEnv(t)
	e.addConvention(t, e.repo, convSpec{rule: "A", status: "accepted"})
	e.addConvention(t, e.repo, convSpec{rule: "P", status: "pending"})
	scanID := e.addScan(t, e.repo, "running", time.Now())
	ref, _, sum := completion(e, scanID)
	sum.Kept = 0
	if err := e.store.CompleteScan(t.Context(), ref, nil, sum, time.Now()); err != nil {
		t.Fatalf("CompleteScan() error = %v", err)
	}
	got, _ := e.store.List(t.Context(), e.repo)
	if diff := cmp.Diff([]string{"A"}, rules(got)); diff != "" {
		t.Errorf("conventions mismatch (-want +got):\n%s", diff)
	}
}

func TestFailScan_OnlyFailsARunningScan(t *testing.T) {
	e := newEnv(t)
	running := e.addScan(t, e.repo, "running", time.Now())
	at := time.Now().Truncate(time.Millisecond)
	if err := e.store.FailScan(t.Context(), running, "boom", []string{"a", "b"}, at); err != nil {
		t.Fatalf("FailScan() error = %v", err)
	}
	s := e.scan(t, running)
	if s.Status != domain.ScanFailed || s.Error == nil || *s.Error != "boom" || !s.FinishedAt.Equal(at) ||
		!cmp.Equal(s.SampledFiles, []string{"a", "b"}) {
		t.Errorf("scan = %+v, want failed with error, finished_at and sampled files", s)
	}

	done := e.addScan(t, e.repo, "done", time.Now().Add(-time.Hour))
	if err := e.store.FailScan(t.Context(), done, "late", nil, at); err != nil {
		t.Fatal(err)
	}
	if got := e.scan(t, done); got.Status != domain.ScanDone || got.Error != nil {
		t.Errorf("done scan = %+v, want untouched", got)
	}
	if err := e.store.FailScan(t.Context(), "00000000-0000-0000-0000-000000000009", "x", nil, at); err != nil {
		t.Errorf("FailScan(unknown scan) error = %v, want nil", err)
	}
}

func TestFailRunningScans(t *testing.T) {
	e := newEnv(t)
	other := e.addRepo(t, "o/other", "")
	r1 := e.addScan(t, e.repo, "running", time.Now())
	r2 := e.addScan(t, other, "running", time.Now())
	d := e.addScan(t, other, "done", time.Now().Add(-time.Hour))
	at := time.Now().Truncate(time.Millisecond)
	n, err := e.store.FailRunningScans(t.Context(), "The scan was interrupted", at)
	if err != nil || n != 2 {
		t.Fatalf("FailRunningScans() = %d, %v, want 2, nil", n, err)
	}
	for _, id := range []string{r1, r2} {
		if s := e.scan(t, id); s.Status != domain.ScanFailed || s.Error == nil || *s.Error != "The scan was interrupted" || s.FinishedAt == nil {
			t.Errorf("scan %s = %+v, want failed, interrupted, finished", id, s)
		}
	}
	if s := e.scan(t, d); s.Status != domain.ScanDone {
		t.Errorf("done scan status = %s", s.Status)
	}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	id := e.addConvention(t, e.repo, convSpec{rule: "old", status: "pending", confidence: f(0.5)})
	at := time.Now().Truncate(time.Millisecond)

	got, err := e.store.Update(t.Context(), e.ws, id, func(c domain.Convention) (domain.Convention, error) {
		c.Status, c.Rule, c.Category, c.Edited, c.UpdatedAt = domain.StatusAccepted, "new", "naming", true, at
		return c, nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got.Status != domain.StatusAccepted || got.Rule != "new" || got.Category != "naming" || !got.Edited || got.Confidence != 0.5 || !got.UpdatedAt.Equal(at) {
		t.Errorf("Update() = %+v, want the changed fields and the old confidence", got)
	}
	list, _ := e.store.List(t.Context(), e.repo)
	if len(list) != 1 || list[0].Rule != "new" || !list[0].UpdatedAt.Equal(at) {
		t.Errorf("stored = %+v, want the update persisted", list)
	}

	boom := errors.New("boom")
	if _, err := e.store.Update(t.Context(), e.ws, id, func(c domain.Convention) (domain.Convention, error) {
		c.Rule = "never"
		return c, boom
	}); !errors.Is(err, boom) {
		t.Errorf("Update(fn error) error = %v, want boom", err)
	}
	if list, _ := e.store.List(t.Context(), e.repo); list[0].Rule != "new" {
		t.Errorf("rule after an aborted update = %q, want new", list[0].Rule)
	}
	if _, err := e.store.Update(t.Context(), "00000000-0000-0000-0000-000000000001", id, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update(other workspace) error = %v, want ErrNotFound", err)
	}
}

func (e *env) addSkill(t *testing.T, name string) string {
	t.Helper()
	var id string
	err := e.db.Raw(`INSERT INTO skills (workspace_id, name, description, type, source, body)
		VALUES (?, ?, 'd', 'convention', 'extracted', 'b') RETURNING id`, e.ws, name).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAcceptedIDsAndSetSkill(t *testing.T) {
	e := newEnv(t)
	other := e.addRepo(t, "o/other", "")
	a1 := e.addConvention(t, e.repo, convSpec{rule: "a1", status: "accepted"})
	a2 := e.addConvention(t, e.repo, convSpec{rule: "a2", status: "accepted"})
	p1 := e.addConvention(t, e.repo, convSpec{rule: "p1", status: "pending"})
	o1 := e.addConvention(t, other, convSpec{rule: "o1", status: "accepted"})

	got, err := e.store.AcceptedIDs(t.Context(), e.ws, e.repo, []string{a1, p1, o1})
	if err != nil {
		t.Fatalf("AcceptedIDs() error = %v", err)
	}
	if diff := cmp.Diff([]string{a1}, got); diff != "" {
		t.Errorf("AcceptedIDs() mismatch (-want +got):\n%s", diff)
	}

	skill := e.addSkill(t, "house-rules")
	at := time.Now()
	if err := e.store.SetSkill(t.Context(), e.ws, e.repo, []string{a1, a2, o1}, skill, at); err != nil {
		t.Fatalf("SetSkill() error = %v", err)
	}
	list, _ := e.store.List(t.Context(), e.repo)
	for _, c := range list {
		has := c.SkillID != nil && *c.SkillID == skill
		if want := c.ID == a1 || c.ID == a2; has != want {
			t.Errorf("convention %s has skill = %v, want %v", c.Rule, has, want)
		}
	}
	if otherList, _ := e.store.List(t.Context(), other); otherList[0].SkillID != nil {
		t.Errorf("convention of another repo got a skill: %+v", otherList[0])
	}
}

func TestSetSkill_JoinsTheCallersTransaction(t *testing.T) {
	e := newEnv(t)
	a1 := e.addConvention(t, e.repo, convSpec{rule: "a1", status: "accepted"})
	skill := e.addSkill(t, "house-rules")
	rollback := errors.New("rollback")

	err := platformpg.NewTx(e.db).InTx(t.Context(), func(ctx context.Context) error {
		if err := e.store.SetSkill(ctx, e.ws, e.repo, []string{a1}, skill, time.Now()); err != nil {
			return err
		}
		inside, _ := e.store.List(ctx, e.repo)
		if len(inside) != 1 || inside[0].SkillID == nil {
			t.Errorf("inside the transaction the skill is not visible: %+v", inside)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("InTx() error = %v, want rollback", err)
	}
	after, _ := e.store.List(t.Context(), e.repo)
	if after[0].SkillID != nil {
		t.Errorf("skill_id after rollback = %v, want nil", *after[0].SkillID)
	}
}
