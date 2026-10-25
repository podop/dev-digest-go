package postgres_test

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/autoreview/adapters/postgres"
	"dev-digest-go/backend/internal/autoreview/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func ptr[T any](v T) *T { return &v }

var (
	enabledAt = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	before    = enabledAt.Add(-time.Hour)
	after     = enabledAt.Add(time.Hour)
	decided   = enabledAt.Add(2 * time.Hour)
)

// fixture is a migrated database with two workspaces and one repository in each.
type fixture struct {
	db        *gorm.DB
	store     *postgres.Store
	ws, other string
	repo      string
	otherRepo string
	next      int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := &fixture{db: d.DB, store: postgres.NewStore(d.DB)}
	f.ws = f.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = f.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.repo = f.addRepo(t, f.ws, "acme", "api")
	f.otherRepo = f.addRepo(t, f.other, "acme", "api")
	return f
}

func (f *fixture) insert(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := f.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (f *fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := f.db.Exec(query, args...).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

func (f *fixture) addRepo(t *testing.T, ws, owner, name string) string {
	t.Helper()
	return f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, ?, ?, ?) RETURNING id`,
		ws, owner, name, owner+"/"+name)
}

// prSpec describes a pull_requests row; the zero value is an open, non-draft PR opened after
// enabledAt with head "h1".
type prSpec struct {
	ws, repo     string // default: the fixture's workspace and repository
	id           string // default: generated
	status       string // default: open
	draft        bool
	head         string
	lastReviewed string
	openedAt     *time.Time // default: after; use noTime for NULL
	updatedAt    *time.Time
}

var noTime = new(time.Time) // marks a NULL time in prSpec.openedAt

func (f *fixture) pr(t *testing.T, s prSpec) string {
	t.Helper()
	if s.ws == "" {
		s.ws, s.repo = f.ws, f.repo
	}
	if s.status == "" {
		s.status = "open"
	}
	if s.head == "" {
		s.head = "h1"
	}
	switch s.openedAt {
	case nil:
		s.openedAt = ptr(after)
	case noTime:
		s.openedAt = nil
	}
	var last *string
	if s.lastReviewed != "" {
		last = &s.lastReviewed
	}
	f.next++
	return f.insert(t, `INSERT INTO pull_requests
		(id, workspace_id, repo_id, number, title, author, branch, base, head_sha, last_reviewed_sha, status, draft, opened_at, updated_at)
		VALUES (COALESCE(NULLIF(?, '')::uuid, gen_random_uuid()), ?, ?, ?, 't', 'dev', 'feat', 'main', ?, ?, ?, ?, ?, ?) RETURNING id`,
		s.id, s.ws, s.repo, f.next, s.head, last, s.status, s.draft, s.openedAt, s.updatedAt)
}

func (f *fixture) record(t *testing.T, ws, pr, head string, ev domain.Event, o domain.Outcome, runs int, at time.Time) {
	t.Helper()
	f.exec(t, `INSERT INTO auto_review_records (workspace_id, pr_id, head_sha, event, outcome, runs_started, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, ws, pr, head, string(ev), string(o), runs, at)
}

func (f *fixture) agent(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return f.insert(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled)
		VALUES (?, ?, 'openai', 'gpt', 'p', ?) RETURNING id`, ws, name, enabled)
}

func (f *fixture) candidates(t *testing.T) []domain.Candidate {
	t.Helper()
	got, err := f.store.Candidates(t.Context(), f.ws, enabledAt)
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	return got
}

func ids(cs []domain.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.PRID
	}
	return out
}

func TestCandidates_FR4Rules(t *testing.T) {
	f := newFixture(t)
	want := f.pr(t, prSpec{})
	// Each of these breaks one rule of FR4 and must not be a candidate.
	f.pr(t, prSpec{status: "closed"})
	f.pr(t, prSpec{status: "merged"})
	f.pr(t, prSpec{draft: true})
	f.pr(t, prSpec{openedAt: ptr(before)})
	f.pr(t, prSpec{openedAt: noTime})
	headRecorded := f.pr(t, prSpec{head: "h2"})
	f.record(t, f.ws, headRecorded, "h2", domain.EventNewPR, domain.OutcomeSkippedTriggerOff, 0, decided)

	got := f.candidates(t)
	if diff := cmp.Diff([]string{want}, ids(got)); diff != "" {
		t.Fatalf("Candidates() ids mismatch (-want +got):\n%s", diff)
	}
	c := got[0]
	if c.Repo != "acme/api" || c.HeadSHA != "h1" || c.Number == 0 || c.HasAnyRecord || c.Running || c.LastReviewedSHA != "" {
		t.Errorf("Candidates()[0] = %+v, want acme/api at h1, no record, not running, never reviewed", c)
	}
}

func TestCandidates_OpenedAtBoundaryIsInclusive(t *testing.T) {
	f := newFixture(t)
	exact := f.pr(t, prSpec{openedAt: ptr(enabledAt)})
	f.pr(t, prSpec{openedAt: ptr(enabledAt.Add(-time.Microsecond))})
	if diff := cmp.Diff([]string{exact}, ids(f.candidates(t))); diff != "" {
		t.Errorf("Candidates() ids mismatch (-want +got):\n%s", diff)
	}
}

func TestCandidates_NewCommitsAfterARecord(t *testing.T) {
	f := newFixture(t)
	// Recorded at the old head, then the head moved: a candidate again, with an earlier record (D6).
	moved := f.pr(t, prSpec{head: "h2"})
	f.record(t, f.ws, moved, "h1", domain.EventNewPR, domain.OutcomeStarted, 1, decided)
	// Opened before enabling: new commits never make it a candidate (EC1).
	f.pr(t, prSpec{head: "h2", openedAt: ptr(before)})

	got := f.candidates(t)
	if diff := cmp.Diff([]string{moved}, ids(got)); diff != "" {
		t.Fatalf("Candidates() ids mismatch (-want +got):\n%s", diff)
	}
	if !got[0].HasAnyRecord || domain.EventFor(got[0]) != domain.EventNewCommits {
		t.Errorf("Candidates()[0] = %+v, want HasAnyRecord and event new_commits", got[0])
	}
}

func TestCandidates_RunningAndLastReviewed(t *testing.T) {
	f := newFixture(t)
	running := f.pr(t, prSpec{updatedAt: ptr(before)})
	finished := f.pr(t, prSpec{updatedAt: ptr(after)})
	reviewed := f.pr(t, prSpec{updatedAt: ptr(after.Add(time.Hour)), lastReviewed: "h1"})
	for _, run := range []struct{ pr, status, source string }{
		{running, "running", "local"}, // a manual run in progress counts, whatever its source
		{finished, "done", "local"},
		{reviewed, "failed", "auto"},
	} {
		f.exec(t, `INSERT INTO agent_runs (workspace_id, pr_id, status, source) VALUES (?, ?, ?, ?)`, f.ws, run.pr, run.status, run.source)
	}
	// A running run of another source on the first PR changes nothing.
	f.exec(t, `INSERT INTO agent_runs (workspace_id, pr_id, status, source) VALUES (?, ?, 'running', 'auto')`, f.ws, running)

	got := f.candidates(t)
	if diff := cmp.Diff([]string{running, finished, reviewed}, ids(got)); diff != "" {
		t.Fatalf("Candidates() ids mismatch (-want +got):\n%s", diff)
	}
	if !got[0].Running || got[1].Running || got[2].Running {
		t.Errorf("Candidates() Running = %v %v %v, want true false false", got[0].Running, got[1].Running, got[2].Running)
	}
	if got[2].LastReviewedSHA != "h1" || got[0].LastReviewedSHA != "" {
		t.Errorf("Candidates() LastReviewedSHA = %q, %q, want \"\", h1 for the third", got[0].LastReviewedSHA, got[2].LastReviewedSHA)
	}
}

func TestCandidates_Order(t *testing.T) {
	f := newFixture(t)
	const (
		idA = "00000000-0000-0000-0000-00000000000a"
		idB = "00000000-0000-0000-0000-00000000000b"
		idC = "00000000-0000-0000-0000-00000000000c"
		idD = "00000000-0000-0000-0000-00000000000d"
		idE = "00000000-0000-0000-0000-00000000000e"
	)
	// Inserted in a scrambled order on purpose.
	f.pr(t, prSpec{id: idE})                         // no update time: last, after idD by id
	f.pr(t, prSpec{id: idC, updatedAt: ptr(after)})  // newest update
	f.pr(t, prSpec{id: idD})                         // no update time
	f.pr(t, prSpec{id: idB, updatedAt: ptr(before)}) // oldest update, tie with idA...
	f.pr(t, prSpec{id: idA, updatedAt: ptr(before)}) // ...broken by id

	got := f.candidates(t)
	want := []string{idA, idB, idC, idD, idE}
	if diff := cmp.Diff(want, ids(got)); diff != "" {
		t.Fatalf("Candidates() order mismatch (-want +got):\n%s", diff)
	}
	sorted := slices.Clone(got)
	slices.Reverse(sorted)
	domain.SortCandidates(sorted)
	if diff := cmp.Diff(want, ids(sorted)); diff != "" {
		t.Errorf("domain.SortCandidates differs from the store order (-store +domain):\n%s", diff)
	}
}

// TestCandidates_MatchesDomainEligible runs the SQL rule against domain.Eligible over every
// combination of the stored facts.
func TestCandidates_MatchesDomainEligible(t *testing.T) {
	f := newFixture(t)
	setting := domain.Setting{Enabled: true, OnNewPR: true, OnNewCommits: true, EnabledAt: ptr(enabledAt)}
	opened := map[string]*time.Time{"before": ptr(before), "equal": ptr(enabledAt), "after": ptr(after), "null": noTime}
	wantIDs := map[string]bool{}
	allIDs := map[string]bool{}
	for _, status := range []string{"open", "needs_review", "stale", "closed", "merged"} {
		for _, draft := range []bool{false, true} {
			for _, at := range opened {
				for _, hasHead := range []bool{false, true} {
					id := f.pr(t, prSpec{status: status, draft: draft, openedAt: at})
					allIDs[id] = true
					if hasHead {
						f.record(t, f.ws, id, "h1", domain.EventNewPR, domain.OutcomeStarted, 1, decided)
					}
					var openedAt *time.Time
					if at != noTime {
						openedAt = at
					}
					p := domain.Pull{
						Candidate:     domain.Candidate{PRID: id, HeadSHA: "h1"},
						Open:          status != "closed" && status != "merged",
						Draft:         draft,
						OpenedAt:      openedAt,
						HasHeadRecord: hasHead,
					}
					if domain.Eligible(p, setting) {
						wantIDs[id] = true
					}
				}
			}
		}
	}
	gotIDs := map[string]bool{}
	for _, c := range f.candidates(t) {
		gotIDs[c.PRID] = true
	}
	if len(allIDs) != 5*2*4*2 || len(wantIDs) == 0 {
		t.Fatalf("matrix has %d pulls, %d eligible; want 80 and some eligible", len(allIDs), len(wantIDs))
	}
	if diff := cmp.Diff(wantIDs, gotIDs); diff != "" {
		t.Errorf("Candidates() differs from domain.Eligible (-domain +sql):\n%s", diff)
	}
}

func TestCandidates_WorkspaceIsolation(t *testing.T) {
	f := newFixture(t)
	mine := f.pr(t, prSpec{})
	f.pr(t, prSpec{ws: f.other, repo: f.otherRepo})
	if diff := cmp.Diff([]string{mine}, ids(f.candidates(t))); diff != "" {
		t.Errorf("Candidates(ws) ids mismatch (-want +got):\n%s", diff)
	}
	got, err := f.store.Candidates(t.Context(), "00000000-0000-0000-0000-000000000000", enabledAt)
	if err != nil || len(got) != 0 {
		t.Errorf("Candidates(unknown workspace) = %v, %v, want none", got, err)
	}
}

func TestEnabledAgents(t *testing.T) {
	f := newFixture(t)
	on1 := f.agent(t, f.ws, "one", true)
	on2 := f.agent(t, f.ws, "two", true)
	off := f.agent(t, f.ws, "off", false)
	foreign := f.agent(t, f.other, "foreign", true)
	ctx := t.Context()

	got, err := f.store.EnabledAgents(ctx, f.ws, []string{on1, off, foreign, on2, "not-a-uuid", "00000000-0000-0000-0000-000000000000"})
	if err != nil {
		t.Fatalf("EnabledAgents() error = %v", err)
	}
	slices.Sort(got)
	want := []string{on1, on2}
	slices.Sort(want)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("EnabledAgents() mismatch (-want +got):\n%s", diff)
	}
	if got, err := f.store.EnabledAgents(ctx, f.ws, nil); err != nil || len(got) != 0 {
		t.Errorf("EnabledAgents(nil) = %v, %v, want none", got, err)
	}
}

func rec(f *fixture, pr, head string, o domain.Outcome, runs int, at time.Time) domain.Record {
	return domain.Record{WorkspaceID: f.ws, PRID: pr, HeadSHA: head, Event: domain.EventNewPR, Outcome: o, RunsStarted: runs, DecidedAt: at}
}

func TestClaim_InsertsOnceAndConflictKeepsFirst(t *testing.T) {
	f := newFixture(t)
	pr := f.pr(t, prSpec{})
	ctx := t.Context()

	first, err := f.store.Claim(ctx, rec(f, pr, "h1", domain.OutcomeStarted, 0, decided))
	if err != nil || !first {
		t.Fatalf("Claim(first) = %v, %v, want true, nil", first, err)
	}
	second, err := f.store.Claim(ctx, rec(f, pr, "h1", domain.OutcomeSkippedNoAgents, 0, decided.Add(time.Minute)))
	if err != nil || second {
		t.Fatalf("Claim(same PR and head) = %v, %v, want false, nil", second, err)
	}
	other, err := f.store.Claim(ctx, rec(f, pr, "h2", domain.OutcomeSkippedNoAgents, 0, decided))
	if err != nil || !other {
		t.Fatalf("Claim(new head) = %v, %v, want true, nil", other, err)
	}

	var rows []struct {
		HeadSHA, Outcome string
		DecidedAt        time.Time
	}
	if err := f.db.Raw(`SELECT head_sha, outcome, decided_at FROM auto_review_records WHERE pr_id = ? ORDER BY head_sha`, pr).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Outcome != "started" || !rows[0].DecidedAt.Equal(decided) || rows[1].Outcome != "skipped_no_agents" {
		t.Errorf("stored records = %+v, want h1 started at the first decision and h2 skipped_no_agents", rows)
	}
}

func TestClaim_ConcurrentClaimsHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	pr := f.pr(t, prSpec{})
	const n = 12
	var wg sync.WaitGroup
	results := make([]bool, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			results[i], errs[i] = f.store.Claim(context.WithoutCancel(t.Context()), rec(f, pr, "h1", domain.OutcomeStarted, 0, decided))
		})
	}
	wg.Wait()
	winners := 0
	for i := range n {
		if errs[i] != nil {
			t.Errorf("Claim #%d error = %v", i, errs[i])
		}
		if results[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("claims won = %d of %d, want exactly 1", winners, n)
	}
}

func TestClaim_RejectsInvalidAndOrphanRecords(t *testing.T) {
	f := newFixture(t)
	pr := f.pr(t, prSpec{})
	ctx := t.Context()
	bad := rec(f, pr, "h1", domain.Outcome("bogus"), 0, decided)
	if ok, err := f.store.Claim(ctx, bad); err == nil || ok {
		t.Errorf("Claim(bogus outcome) = %v, %v, want an error", ok, err)
	}
	orphan := rec(f, "00000000-0000-0000-0000-000000000000", "h1", domain.OutcomeStarted, 0, decided)
	if ok, err := f.store.Claim(ctx, orphan); err == nil || ok {
		t.Errorf("Claim(unknown PR) = %v, %v, want a foreign-key error", ok, err)
	}
}

func TestFinish(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	read := func(pr, head string) (string, int) {
		t.Helper()
		var r struct {
			Outcome     string
			RunsStarted int
		}
		if err := f.db.Raw(`SELECT outcome, runs_started FROM auto_review_records WHERE pr_id = ? AND head_sha = ?`, pr, head).Scan(&r).Error; err != nil {
			t.Fatal(err)
		}
		return r.Outcome, r.RunsStarted
	}
	claim := func(pr, head string, o domain.Outcome) {
		t.Helper()
		if ok, err := f.store.Claim(ctx, rec(f, pr, head, o, 0, decided)); err != nil || !ok {
			t.Fatalf("Claim() = %v, %v", ok, err)
		}
	}

	t.Run("started with runs", func(t *testing.T) {
		pr := f.pr(t, prSpec{})
		claim(pr, "h1", domain.OutcomeStarted)
		if err := f.store.Finish(ctx, f.ws, pr, "h1", domain.OutcomeStarted, 3); err != nil {
			t.Fatalf("Finish() error = %v", err)
		}
		if o, n := read(pr, "h1"); o != "started" || n != 3 {
			t.Errorf("record = %s/%d, want started/3", o, n)
		}
	})
	t.Run("failed stores zero runs", func(t *testing.T) {
		pr := f.pr(t, prSpec{})
		claim(pr, "h1", domain.OutcomeStarted)
		if err := f.store.Finish(ctx, f.ws, pr, "h1", domain.OutcomeFailed, 2); err != nil {
			t.Fatalf("Finish() error = %v", err)
		}
		if o, n := read(pr, "h1"); o != "failed" || n != 0 {
			t.Errorf("record = %s/%d, want failed/0", o, n)
		}
	})
	t.Run("a skip record is never changed", func(t *testing.T) {
		pr := f.pr(t, prSpec{})
		claim(pr, "h1", domain.OutcomeSkippedSpendCap)
		if err := f.store.Finish(ctx, f.ws, pr, "h1", domain.OutcomeStarted, 1); err == nil {
			t.Error("Finish(skip record) error = nil, want an error")
		}
		if o, n := read(pr, "h1"); o != "skipped_spend_cap" || n != 0 {
			t.Errorf("record = %s/%d, want skipped_spend_cap/0", o, n)
		}
	})
	t.Run("another workspace's record is not touched", func(t *testing.T) {
		pr := f.pr(t, prSpec{})
		claim(pr, "h1", domain.OutcomeStarted)
		if err := f.store.Finish(ctx, f.other, pr, "h1", domain.OutcomeStarted, 4); err == nil {
			t.Error("Finish(other workspace) error = nil, want an error")
		}
		if o, n := read(pr, "h1"); o != "started" || n != 0 {
			t.Errorf("record = %s/%d, want started/0", o, n)
		}
	})
	t.Run("invalid arguments", func(t *testing.T) {
		pr := f.pr(t, prSpec{})
		claim(pr, "h1", domain.OutcomeStarted)
		if err := f.store.Finish(ctx, f.ws, pr, "h1", domain.OutcomeSkippedNoAgents, 0); err == nil {
			t.Error("Finish(skip outcome) error = nil, want an error")
		}
		if err := f.store.Finish(ctx, f.ws, pr, "h1", domain.OutcomeStarted, -1); err == nil {
			t.Error("Finish(negative runs) error = nil, want an error")
		}
		if err := f.store.Finish(ctx, f.ws, pr, "missing", domain.OutcomeStarted, 1); err == nil {
			t.Error("Finish(no such head) error = nil, want an error")
		}
	})
}

func TestStartedSince(t *testing.T) {
	f := newFixture(t)
	since := decided
	mk := func(ws string, o domain.Outcome, runs int, at time.Time, repo string) {
		pr := f.pr(t, prSpec{ws: ws, repo: repo})
		f.record(t, ws, pr, "h1", domain.EventNewPR, o, runs, at)
	}
	mk(f.ws, domain.OutcomeStarted, 2, since, f.repo)                          // at the bound: counted
	mk(f.ws, domain.OutcomeStarted, 0, since.Add(time.Minute), f.repo)         // claimed, not settled: counted
	mk(f.ws, domain.OutcomeStarted, 1, since.Add(-time.Second), f.repo)        // before: not counted
	mk(f.ws, domain.OutcomeFailed, 0, since.Add(time.Minute), f.repo)          // failed: no
	mk(f.ws, domain.OutcomeSkippedSpendCap, 0, since.Add(time.Hour), f.repo)   // skip: no
	mk(f.other, domain.OutcomeStarted, 1, since.Add(time.Minute), f.otherRepo) // other workspace: no

	got, err := f.store.StartedSince(t.Context(), f.ws, since)
	if err != nil || got != 2 {
		t.Errorf("StartedSince(ws) = %d, %v, want 2, nil", got, err)
	}
	got, err = f.store.StartedSince(t.Context(), f.other, since)
	if err != nil || got != 1 {
		t.Errorf("StartedSince(other) = %d, %v, want 1, nil", got, err)
	}
}

func TestStatusFacts(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	empty, err := f.store.StatusFacts(ctx, f.ws)
	if err != nil || empty.LastPollAt != nil || empty.PRsTracked != 0 {
		t.Fatalf("StatusFacts(no polls, no PRs) = %+v, %v, want nil poll time and 0 PRs", empty, err)
	}

	f.pr(t, prSpec{})
	f.pr(t, prSpec{draft: true})                    // drafts are tracked (D12)
	f.pr(t, prSpec{openedAt: noTime})               // so are PRs the trigger would never pick
	f.pr(t, prSpec{status: "closed"})               // not open
	f.pr(t, prSpec{status: "merged"})               // not open
	f.pr(t, prSpec{ws: f.other, repo: f.otherRepo}) // other workspace
	older, newer := before, after
	repo2 := f.addRepo(t, f.ws, "acme", "web")
	f.addRepo(t, f.ws, "acme", "never-polled")
	f.exec(t, `UPDATE repos SET last_polled_at = ? WHERE id = ?`, older, f.repo)
	f.exec(t, `UPDATE repos SET last_polled_at = ? WHERE id = ?`, newer, repo2)
	f.exec(t, `UPDATE repos SET last_polled_at = ? WHERE id = ?`, after.Add(24*time.Hour), f.otherRepo)

	got, err := f.store.StatusFacts(ctx, f.ws)
	if err != nil {
		t.Fatalf("StatusFacts() error = %v", err)
	}
	if got.PRsTracked != 3 {
		t.Errorf("StatusFacts().PRsTracked = %d, want 3 (open PRs, drafts included)", got.PRsTracked)
	}
	if got.LastPollAt == nil || !got.LastPollAt.Equal(newer) {
		t.Errorf("StatusFacts().LastPollAt = %v, want the newest repository poll %v", got.LastPollAt, newer)
	}
}

// TestCandidates_StaysSetBased seeds 1,000 open PRs with 10 records each (NFR1) and checks one
// evaluation reads them in a single fast statement.
func TestCandidates_StaysSetBased(t *testing.T) {
	f := newFixture(t)
	f.exec(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, status, opened_at, updated_at)
		SELECT ?::uuid, ?::uuid, g, 't', 'dev', 'feat', 'main', 'head-10', 'open', ?::timestamptz, ?::timestamptz + g * interval '1 second'
		FROM generate_series(1, 1000) g`, f.ws, f.repo, after, after)
	f.exec(t, `INSERT INTO auto_review_records (workspace_id, pr_id, head_sha, event, outcome, decided_at)
		SELECT p.workspace_id, p.id, 'head-' || h, 'new_pr', 'skipped_trigger_off', ?::timestamptz
		FROM pull_requests p, generate_series(1, 9) h WHERE p.workspace_id = ?`, decided, f.ws)

	best := time.Hour
	var n int
	for range 3 {
		start := time.Now()
		got, err := f.store.Candidates(t.Context(), f.ws, enabledAt)
		if err != nil {
			t.Fatalf("Candidates() error = %v", err)
		}
		best, n = min(best, time.Since(start)), len(got)
	}
	if n != 1000 {
		t.Fatalf("Candidates() = %d rows, want 1000", n)
	}
	if best > 500*time.Millisecond {
		t.Errorf("Candidates() best of 3 = %v, want <= 500ms (NFR1)", best)
	}
}
