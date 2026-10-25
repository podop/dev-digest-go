package postgres_test

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"dev-digest-go/backend/internal/autoreview/adapters/postgres"
	"dev-digest-go/backend/internal/autoreview/app"
	"dev-digest-go/backend/internal/autoreview/domain"
)

// autoReviewLoad switches on the NFR1/NFR2 load test (AC16, plan assumption A14). It is heavy
// (seeds 1,000 PRs and 10,000 records, runs 50 evaluations), so the normal suite skips it:
//
//	go test -race -count=1 -run TestLoad_AC16 ./internal/autoreview/adapters/postgres -autoreview.load -v
//
// Pass it with a single package: `go test` hands unknown flags to every package it runs.
var autoReviewLoad = flag.Bool("autoreview.load", false, "run the automatic-review load test (AC16, NFR1/NFR2)")

const (
	loadPRs         = 1000
	loadRecords     = 10000
	loadEvaluations = 50
	loadDBBudgetP95 = 750 * time.Millisecond // spec D16 (pgtest, fsync off)
)

// timedStore is the real postgres Store with a stopwatch around every call: the sum of the time
// spent inside its methods during one AfterCycle is that evaluation's database time. Time spent
// in the pure decision code, the fakes and the logger is not counted.
type timedStore struct {
	app.Store
	spent time.Duration
	calls int
	// by splits spent per store method, n counts the calls per method.
	by map[string]time.Duration
	n  map[string]int
}

func (s *timedStore) track(method string) func() {
	start := time.Now()
	return func() {
		d := time.Since(start)
		s.spent += d
		s.calls++
		s.by[method] += d
		s.n[method]++
	}
}

func (s *timedStore) reset() {
	s.spent, s.calls, s.by, s.n = 0, 0, map[string]time.Duration{}, map[string]int{}
}

func (s *timedStore) Candidates(ctx context.Context, ws string, enabledAt time.Time) ([]domain.Candidate, error) {
	defer s.track("Candidates")()
	return s.Store.Candidates(ctx, ws, enabledAt)
}

func (s *timedStore) EnabledAgents(ctx context.Context, ws string, ids []string) ([]string, error) {
	defer s.track("EnabledAgents")()
	return s.Store.EnabledAgents(ctx, ws, ids)
}

func (s *timedStore) StartedSince(ctx context.Context, ws string, since time.Time) (int, error) {
	defer s.track("StartedSince")()
	return s.Store.StartedSince(ctx, ws, since)
}

func (s *timedStore) Claim(ctx context.Context, rec domain.Record) (bool, error) {
	defer s.track("Claim")()
	return s.Store.Claim(ctx, rec)
}

func (s *timedStore) Finish(ctx context.Context, ws, prID, head string, o domain.Outcome, runs int) error {
	defer s.track("Finish")()
	return s.Store.Finish(ctx, ws, prID, head, o, runs)
}

func (s *timedStore) StatusFacts(ctx context.Context, ws string) (app.PollFacts, error) {
	defer s.track("StatusFacts")()
	return s.Store.StatusFacts(ctx, ws)
}

// loadStarter is the fake review start: it counts calls and creates nothing.
type loadStarter struct{ calls atomic.Int64 }

func (s *loadStarter) StartAuto(context.Context, string, string, []string) (int, error) {
	s.calls.Add(1)
	return 1, nil
}

// loadBudget, loadCaps and loadTokens are fakes that count their calls. Only the budget may be
// asked, once per evaluation (A2); the cap and token reads belong to the status line.
type loadBudget struct{ calls atomic.Int64 }

func (b *loadBudget) Admit(context.Context, string) error { b.calls.Add(1); return nil }

type loadCaps struct{ calls atomic.Int64 }

func (c *loadCaps) CapReached(context.Context, string) (bool, error) {
	c.calls.Add(1)
	return false, nil
}

type loadTokens struct{ calls atomic.Int64 }

func (t *loadTokens) HasGitHubToken(context.Context, string) (bool, error) {
	t.calls.Add(1)
	return true, nil
}

// outboundCounter stands in for GitHub and for every LLM provider: it replaces the process-wide
// default HTTP transport, the one an SDK or client built without its own transport would use, and
// counts requests (failing them, so nothing reaches the network). The Deps of the evaluation hold
// no GitHub or LLM client at all; this proves nothing on the evaluation's path reaches one anyway.
type outboundCounter struct{ calls atomic.Int64 }

func (o *outboundCounter) RoundTrip(*http.Request) (*http.Response, error) {
	o.calls.Add(1)
	return nil, http.ErrNotSupported
}

// percentile is the nearest-rank percentile of sorted values.
func percentile(sorted []time.Duration, p float64) time.Duration {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[max(rank, 1)-1]
}

// loadProfile shapes the 1,000 PRs: how many are still undecided when an evaluation runs. The
// 10,000 records are the same in every profile.
type loadProfile struct {
	name string
	// fresh PRs (numbers 1..fresh) were never evaluated: no record, event new_pr.
	fresh int
	// pending (SQL over g, the PR's 0-based index) picks the older PRs whose current head has no
	// record yet (event new_commits); every other older PR sits on a head that has its record.
	pending string
}

var (
	// loadSteady is a busy workspace between two poll cycles: 4% new PRs and 8% PRs with new
	// commits since the last evaluation, so about 120 candidates (a quarter of them forks that get
	// their one skipped_fork record, a tenth with an unknown head repository that stay deferred).
	loadSteady = loadProfile{name: "steady", fresh: 40, pending: "g % 12 = 0"}
	// loadBurst is the worst case: 700 of the 1,000 PRs undecided at once (all of them new since
	// the poll first saw them, as right after arming the setting on a busy workspace).
	loadBurst = loadProfile{name: "burst", fresh: 100, pending: "g % 3 <> 0"}
)

// seedLoad inserts loadPRs open PRs and loadRecords records for them, shaped like a busy
// workspace so the planner sees realistic data:
//   - the PRs after the fresh ones carry the 10,000 records on earlier heads (old-0 .. old-10/11),
//     mixed outcomes, decided 48 h ago; each has exactly one record per head;
//   - head repository: 70% same repository, 20% fork, 10% unknown (NULL);
//   - 5% were last reviewed at their head; 2% have a running review; every PR has a finished run;
//   - updated_at spread over the PRs, 4% NULL.
func seedLoad(t *testing.T, f *fixture, p loadProfile) {
	t.Helper()
	older := loadPRs - p.fresh
	f.exec(t, `INSERT INTO pull_requests
		(workspace_id, repo_id, number, title, author, branch, base, head_sha, status, draft, opened_at, updated_at, head_is_fork)
		SELECT ?::uuid, ?::uuid, g + 1, 't', 'dev', 'feat-' || g, 'main',
			CASE WHEN g >= ? AND NOT (`+p.pending+`) THEN 'old-5' ELSE 'head-current' END,
			'open', false, ?::timestamptz + g * interval '1 second',
			CASE WHEN g % 25 = 0 THEN NULL ELSE ?::timestamptz + g * interval '1 second' END,
			CASE g % 10 WHEN 7 THEN true WHEN 8 THEN true WHEN 9 THEN NULL ELSE false END
		FROM generate_series(0, ?) g`, f.ws, f.repo, p.fresh, after, after, loadPRs-1)
	f.exec(t, `UPDATE pull_requests SET last_reviewed_sha = head_sha WHERE workspace_id = ? AND number % 20 = 1`, f.ws)
	f.exec(t, `INSERT INTO auto_review_records
		(workspace_id, pr_id, head_sha, event, outcome, runs_started, decided_at)
		SELECT pr.workspace_id, pr.id, 'old-' || (r / ?),
			CASE WHEN r / ? = 0 THEN 'new_pr' ELSE 'new_commits' END,
			CASE r % 4 WHEN 0 THEN 'skipped_trigger_off' WHEN 1 THEN 'skipped_already_reviewed' WHEN 2 THEN 'started' ELSE 'failed' END,
			CASE WHEN r % 4 = 2 THEN 1 ELSE 0 END,
			?::timestamptz - r * interval '1 minute'
		FROM generate_series(0, ?) r
		JOIN pull_requests pr ON pr.workspace_id = ? AND pr.number = ? + 1 + r % ?`,
		older, older, decided.Add(-48*time.Hour), loadRecords-1, f.ws, p.fresh, older)
	f.exec(t, `INSERT INTO agent_runs (workspace_id, pr_id, ran_at, status, source)
		SELECT workspace_id, id, ?::timestamptz - number * interval '1 minute',
			CASE WHEN number % 50 = 3 THEN 'running' ELSE 'done' END, 'local'
		FROM pull_requests WHERE workspace_id = ?`, decided.Add(-72*time.Hour), f.ws)
	// A long-lived database has statistics; a freshly bulk-loaded one has none yet.
	f.exec(t, `ANALYZE pull_requests`)
	f.exec(t, `ANALYZE auto_review_records`)
	f.exec(t, `ANALYZE agent_runs`)

	var gotPRs, gotRecords int
	if err := f.db.Raw(`SELECT count(*) FROM pull_requests WHERE workspace_id = ?`, f.ws).Scan(&gotPRs).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw(`SELECT count(*) FROM auto_review_records WHERE workspace_id = ?`, f.ws).Scan(&gotRecords).Error; err != nil {
		t.Fatal(err)
	}
	if gotPRs != loadPRs || gotRecords != loadRecords {
		t.Fatalf("seeded %d PRs and %d records, want %d and %d", gotPRs, gotRecords, loadPRs, loadRecords)
	}
}

// runLoad seeds the profile and runs loadEvaluations evaluations of the real Service (AfterCycle,
// real postgres store, armed setting), the fakes standing in for everything else. Database time
// is the summed duration of the store calls of one AfterCycle (timedStore); the first
// evaluation is measured too. Every evaluation starts from the same data: the records and starts
// of the previous evaluation (all stamped with the evaluation clock) are deleted outside the
// stopwatch, so each reads the candidate set again, records the forks again and starts the five
// reviews of its per-evaluation limit again. It returns the sorted database times of all
// evaluations and of their Candidates call alone.
func runLoad(t *testing.T, p loadProfile) (db, candidates []time.Duration) {
	t.Helper()
	f := newFixture(t)
	agent := f.agent(t, f.ws, "reviewer", true)
	seedLoad(t, f, p)

	outbound := &outboundCounter{}
	prev := http.DefaultTransport
	http.DefaultTransport = outbound
	t.Cleanup(func() { http.DefaultTransport = prev })

	store := &timedStore{Store: postgres.NewStore(f.db)}
	store.reset()
	starter, budget, caps, tokens := &loadStarter{}, &loadBudget{}, &loadCaps{}, &loadTokens{}
	svc := app.NewService(app.Deps{
		Settings:    stubSettings{armedSetting(agent)},
		Interval:    stubInterval{},
		Store:       store,
		Starter:     starter,
		Budget:      budget,
		Caps:        caps,
		Tokens:      tokens,
		PollEnabled: true,
		Now:         func() time.Time { return decided },
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	wall := make([]time.Duration, 0, loadEvaluations)
	for i := range loadEvaluations {
		f.exec(t, `DELETE FROM auto_review_records WHERE workspace_id = ? AND decided_at = ?`, f.ws, decided)
		store.reset()
		startsBefore, budgetBefore, outboundBefore := starter.calls.Load(), budget.calls.Load(), outbound.calls.Load()

		start := time.Now()
		svc.AfterCycle(t.Context(), f.ws)
		wall = append(wall, time.Since(start))

		if got := starter.calls.Load() - startsBefore; got != domain.MaxStartsPerEvaluation {
			t.Fatalf("evaluation %d started %d reviews, want %d (the per-evaluation limit); the fixture is not exercising the start path", i+1, got, domain.MaxStartsPerEvaluation)
		}
		if got := budget.calls.Load() - budgetBefore; got != 1 {
			t.Errorf("evaluation %d asked the budget %d times, want once", i+1, got)
		}
		if got := outbound.calls.Load() - outboundBefore; got != 0 {
			t.Errorf("evaluation %d made %d outbound HTTP requests, want 0 (NFR2)", i+1, got)
		}
		db = append(db, store.spent)
		candidates = append(candidates, store.by["Candidates"])
		if i == 0 {
			var forks, total int
			if err := f.db.Raw(`SELECT count(*) FILTER (WHERE outcome = 'skipped_fork'), count(*) FROM auto_review_records WHERE workspace_id = ? AND decided_at = ?`, f.ws, decided).Row().Scan(&forks, &total); err != nil {
				t.Fatal(err)
			}
			if forks == 0 {
				t.Errorf("evaluation wrote no skipped_fork record; the fixture lacks undecided forks")
			}
			t.Logf("[%s] first evaluation: %d store calls, %d records written (%d skipped_fork, %d started)",
				p.name, store.calls, total, forks, domain.MaxStartsPerEvaluation)
			for _, m := range []string{"Candidates", "EnabledAgents", "StartedSince", "Claim", "Finish"} {
				t.Logf("[%s]   %-13s %4d calls %v", p.name, m, store.n[m], store.by[m])
			}
		}
	}
	if got := caps.calls.Load() + tokens.calls.Load(); got != 0 {
		t.Errorf("cap and token reads during evaluations = %d, want 0 (status line only)", got)
	}
	if got := outbound.calls.Load(); got != 0 {
		t.Errorf("outbound HTTP requests during the load test = %d, want 0 (NFR2)", got)
	}
	slices.Sort(db)
	slices.Sort(candidates)
	slices.Sort(wall)
	t.Logf("[%s] database time of %d evaluations over %d PRs / %d records: p50 %v  p95 %v  max %v",
		p.name, loadEvaluations, loadPRs, loadRecords, percentile(db, 50), percentile(db, 95), db[len(db)-1])
	t.Logf("[%s]   of which Candidates: p50 %v  p95 %v  max %v; AfterCycle wall time: p50 %v  p95 %v",
		p.name, percentile(candidates, 50), percentile(candidates, 95), candidates[len(candidates)-1],
		percentile(wall, 50), percentile(wall, 95))
	return db, candidates
}

// TestLoad_AC16 (NFR1, NFR2, plan assumption A14): over 1,000 open PRs and 10,000 records, 50
// evaluations spend at most 750 ms (spec D16) of database time at the 95th percentile, and none of them
// calls GitHub or an LLM. The profile is a steady workspace (see loadSteady).
func TestLoad_AC16(t *testing.T) {
	if !*autoReviewLoad {
		t.Skip("load test: pass -autoreview.load to run it (AC16)")
	}
	db, _ := runLoad(t, loadSteady)
	if p95 := percentile(db, 95); p95 > loadDBBudgetP95 {
		t.Errorf("database time p95 = %v, want <= %v (NFR1)", p95, loadDBBudgetP95)
	}
}

// TestLoad_AC16_Burst is the worst case for the same data (see loadBurst): 700 undecided PRs in
// one evaluation, about 180 sequential record claims. The spec bounds the evaluation's database
// time without saying how many PRs are undecided; this test pins only the set-based read (one
// Candidates statement, S9) and reports the total, which is dominated by the per-record claims.
func TestLoad_AC16_Burst(t *testing.T) {
	if !*autoReviewLoad {
		t.Skip("load test: pass -autoreview.load to run it (AC16)")
	}
	_, candidates := runLoad(t, loadBurst)
	if p95 := percentile(candidates, 95); p95 > loadDBBudgetP95 {
		t.Errorf("Candidates() p95 = %v, want <= %v (NFR1)", p95, loadDBBudgetP95)
	}
}
