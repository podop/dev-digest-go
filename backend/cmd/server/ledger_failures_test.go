package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/llm"
)

// Failure-path component tests of the cost ledger through the real server (plan S20): an answered
// call is recorded whatever happens to its unit of work (AC4, FR5), a call that never answered is
// not (AC5), the month of an entry is the month of its answer (AC7), a failed append never stops
// the work (AC12, FR15) and the spend gate fails closed (AC11). Helpers are prefixed lf.

// lfEntry is a cost_ledger row as the tests read it.
type lfEntry struct {
	Kind       string
	Provider   string
	Model      string
	RefID      *string
	CostMicros *int64
	OccurredAt time.Time
}

// lfEntries lists the entries of one kind ("" = every kind).
func lfEntries(t testing.TB, s *stack, kind string) []lfEntry {
	t.Helper()
	var rows []lfEntry
	err := s.db.DB.Raw(`SELECT kind, provider, model, ref_id::text AS ref_id, cost_micros, occurred_at
		FROM cost_ledger WHERE (? = '' OR kind = ?) ORDER BY occurred_at, cost_micros`, kind, kind).Scan(&rows).Error
	if err != nil {
		t.Fatalf("read cost_ledger kind %q: %v", kind, err)
	}
	return rows
}

// lfCosts is the cost of each entry in micro-USD, -1 for an unknown cost, in the entries' order.
func lfCosts(entries []lfEntry) []int64 {
	out := make([]int64, len(entries))
	for i, e := range entries {
		out[i] = -1
		if e.CostMicros != nil {
			out[i] = *e.CostMicros
		}
	}
	slices.Sort(out)
	return out
}

// lfSpent is spent_usd of GET /settings/spend.
func lfSpent(t testing.TB, s *stack) float64 {
	t.Helper()
	got, ok := s.get("/settings/spend").Obj(t)["spent_usd"].(float64)
	if !ok {
		t.Fatalf("GET /settings/spend has no numeric spent_usd")
	}
	return got
}

// lfMonth is the stored month total of the seeded workspace in one month ("2026-10"): the spent
// micro-USD and the number of entries.
type lfMonth struct{ Spent, Entries int64 }

func lfMonths(t testing.TB, s *stack) map[string]lfMonth {
	t.Helper()
	var rows []struct {
		Month   string
		Spent   int64
		Entries int64
	}
	err := s.db.DB.Raw(`SELECT to_char(month, 'YYYY-MM') AS month, sum(spent_micros)::bigint AS spent, sum(entry_count)::bigint AS entries
		FROM spend_month_totals GROUP BY month`).Scan(&rows).Error
	if err != nil {
		t.Fatalf("read spend_month_totals: %v", err)
	}
	out := map[string]lfMonth{}
	for _, r := range rows {
		out[r.Month] = lfMonth{Spent: r.Spent, Entries: r.Entries}
	}
	return out
}

// lfNth answers call n (1-based) of a scripted provider with the n-th function, and every later
// call with the last one.
func lfNth(fns ...callFn) callFn {
	var n atomic.Int32
	return func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
		i := int(n.Add(1)) - 1
		return fns[min(i, len(fns)-1)](ctx, at)
	}
}

// lfAnswer is a call that answers with a valid review and the cost (USD), without any other effect.
func lfAnswer(usd float64) callFn {
	return func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return answer(answerJSON, 100, 10, usd), nil
	}
}

// lfReviewStack starts a stack whose provider is ad and whose map-reduce runs one chunk at a time
// (so the call order, and with it the scripted behaviour, is deterministic).
func lfReviewStack(t *testing.T, ad *scripted, sp spec) *stack {
	t.Helper()
	sp.llmAdapters = ad.forAll()
	sp.llm = fastLLM()
	sp.now = lfOr(sp.now, capClock)
	sp.config = chain(func(c *config.Config) { c.ReviewMapConcurrency = 1 }, sp.config)
	return startStack(t, sp)
}

// lfOr returns f, or def when f is nil.
func lfOr(f, def func() time.Time) func() time.Time {
	if f != nil {
		return f
	}
	return def
}

// ---- AC4: an answered call is recorded whatever happens next ----

// AC4, EC4: a review cancelled after 2 of its 3 chunks answered keeps 2 entries, and the month
// spend counts them. The third call, still waiting for its answer, records nothing.
func TestLF_CancelledReviewKeepsItsAnsweredChunks(t *testing.T) {
	ad := &scripted{complete: lfNth(lfAnswer(0.001), lfAnswer(0.002), blockUntilDone)}
	s := lfReviewStack(t, ad, spec{})
	requireNoSpend(t, s)
	runs, _ := startOK(t, s, threeFiles(t, s, 950), map[string]any{"agentIds": []string{mapReduceAgent(t, s)}})
	eventually(t, 30*time.Second, "the third chunk to be in its call", func() bool { return ad.calls() == 3 })

	if err := cancelFromOutside(s.base, runs[0]); err != nil {
		t.Fatal(err)
	}
	waitTrace(t, s, runs[0])
	if got := runStatus(t, s, runs[0]); got != "cancelled" {
		t.Fatalf("run status = %s, want cancelled", got)
	}

	entries := lfEntries(t, s, "")
	if diff := cmp.Diff([]int64{1000, 2000}, lfCosts(entries)); diff != "" {
		t.Errorf("costs of the entries after a cancel with 2 of 3 chunks answered mismatch (-want +got):\n%s", diff)
	}
	for _, e := range entries {
		if e.Kind != "review_run" || e.RefID == nil || *e.RefID != runs[0] {
			t.Errorf("entry = %+v, want kind review_run referencing run %s", e, runs[0])
		}
	}
	if got := lfSpent(t, s); got != 0.003 {
		t.Errorf("GET /settings/spend spent_usd = %v, want 0.003 (the two answered chunks)", got)
	}
}

// AC4, EC6: a first invalid answer and a valid re-prompt are two answers, so two entries.
func TestLF_RepromptedReviewRecordsBothAnswers(t *testing.T) {
	invalid := func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return answer("this is not json", 50, 5, 0.001), nil
	}
	ad := &scripted{complete: lfNth(invalid, lfAnswer(0.002))}
	s := lfReviewStack(t, ad, spec{})
	requireNoSpend(t, s)
	runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	waitStatus(t, s, runs[0], "done")

	if ad.calls() != 2 {
		t.Fatalf("provider calls = %d, want 2 (the invalid answer and the re-prompt)", ad.calls())
	}
	entries := lfEntries(t, s, "review_run")
	if diff := cmp.Diff([]int64{1000, 2000}, lfCosts(entries)); diff != "" {
		t.Errorf("costs of the entries after a re-prompt mismatch (-want +got):\n%s", diff)
	}
	if got := lfSpent(t, s); got != 0.003 {
		t.Errorf("GET /settings/spend spent_usd = %v, want 0.003", got)
	}
}

// AC4, EC7: a brief, a tour and a conventions scan whose answers fail output validation fail as
// they do today and keep one entry per answer. Each of the three re-prompts once, so a generation
// that never gets a valid answer has two answers, and so two entries.
func TestLF_FailedGenerationsKeepTheirAnswers(t *testing.T) {
	invalid := func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return answer("this is not json", 100, 10, 0.001), nil
	}
	sc := routed(map[string]callFn{"PrBriefLlm": invalid, "OnboardingTour": invalid, "ConventionExtraction": invalid})
	s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll(), llm: fastLLM(), now: capClock})
	requireNoSpend(t, s)
	pr := pr482(t, s)

	// check runs one failing generation and verifies its entries.
	check := func(t *testing.T, kind, schema, ref string, generate func() string) {
		t.Helper()
		before := lfSpent(t, s)
		answered := countOf(sc, schema)
		if outcome := generate(); outcome != "failed" {
			t.Fatalf("%s generation outcome = %q, want failed", kind, outcome)
		}
		answers := countOf(sc, schema) - answered
		if answers != 2 {
			t.Fatalf("%s: provider answers = %d, want 2 (first attempt and one re-prompt)", kind, answers)
		}
		var costs []int64
		for _, e := range lfEntries(t, s, kind) {
			costs = append(costs, *e.CostMicros)
			if ref != "" && (e.RefID == nil || *e.RefID != ref) {
				t.Errorf("%s entry ref_id = %v, want %s", kind, e.RefID, ref)
			}
		}
		if diff := cmp.Diff([]int64{1000, 1000}, costs); diff != "" {
			t.Errorf("%s entries mismatch (-want +got):\n%s", kind, diff)
		}
		if got, want := lfSpent(t, s), before+0.002; got != want {
			t.Errorf("spent_usd after the failed %s = %v, want %v", kind, got, want)
		}
	}
	t.Run("brief", func(t *testing.T) {
		check(t, "brief", "PrBriefLlm", pr, func() string {
			if r := s.post("/pulls/"+pr+"/brief", nil); r.Status != http.StatusBadGateway || r.errorCode(t) != "generation_failed" {
				t.Errorf("POST brief with invalid answers = %d %s, want 502 generation_failed", r.Status, r.Body)
			}
			return "failed"
		})
	})
	t.Run("tour", func(t *testing.T) {
		check(t, "onboarding", "OnboardingTour", repo, func() string {
			if r := s.post("/repos/"+repo+"/onboarding", nil); r.Status != http.StatusBadGateway || r.errorCode(t) != "generation_failed" {
				t.Errorf("POST onboarding with invalid answers = %d %s, want 502 generation_failed", r.Status, r.Body)
			}
			return "failed"
		})
	})
	t.Run("conventions scan", func(t *testing.T) {
		check(t, "conventions_scan", "ConventionExtraction", "", func() string {
			scan := runScan(t, s, repo)
			if scan["status"] != "failed" {
				t.Errorf("scan = %v, want failed", scan)
			}
			return scan["status"].(string)
		})
	})
}

// AC4, FR5: an eval case whose call is answered at the moment the server is told to shut down
// (the answer arrives, then the process goes) keeps its entry. The entry is written after the
// shutdown began, on a context that no longer carries the cancel, and stays after the server is gone.
func TestLF_EvalCaseAbortedByShutdownAfterItsAnswerKeepsItsEntry(t *testing.T) {
	var shutdown atomic.Pointer[context.CancelFunc]
	ad := &scripted{complete: func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		if stop := shutdown.Load(); stop != nil {
			(*stop)()
		}
		return answer(answerJSON, 100, 10, 0.002), nil
	}}
	s := startStack(t, spec{llmAdapters: ad.forAll(), llm: fastLLM(), now: capClock})
	ws := workspaceID(t, s)
	agent := evAgent(t, s, "Shutdown Agent")
	cases := evSuiteCases(t, s, "agent", agent, 1)
	cancel := s.cancel
	shutdown.Store(&cancel)

	evStart(s, agent)
	eventually(t, 30*time.Second, "the eval case to reach its call", func() bool { return ad.calls() >= 1 })
	if err := s.stop(); err != nil {
		t.Errorf("serve() = %v after the stop signal, want nil", err)
	}

	entries := lfEntries(t, s, "eval_case")
	if len(entries) != 1 {
		t.Fatalf("eval_case entries after the shutdown = %d, want 1 (the case answered)", len(entries))
	}
	if e := entries[0]; e.CostMicros == nil || *e.CostMicros != 2000 || e.RefID == nil || *e.RefID != cases[0] {
		t.Errorf("entry = %+v, want cost 2000 micro-USD referencing case %s", e, cases[0])
	}
	if got := lfMonths(t, s)["2031-01"]; got != (lfMonth{Spent: 2000, Entries: 1}) {
		t.Errorf("month total of workspace %s after the shutdown = %+v, want 2000 micro-USD from 1 entry", ws, got)
	}
}

// ---- AC5: a call that never answered records nothing ----

// AC5, EC5: a cancel before the answer and a timeout leave no entry and the month spend unchanged.
func TestLF_UnansweredCallsRecordNothing(t *testing.T) {
	t.Run("cancel before the answer", func(t *testing.T) {
		ad := &scripted{complete: blockUntilDone}
		s := lfReviewStack(t, ad, spec{})
		requireNoSpend(t, s)
		runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
		eventually(t, 30*time.Second, "the call to be waiting", func() bool { return ad.calls() == 1 })
		if err := cancelFromOutside(s.base, runs[0]); err != nil {
			t.Fatal(err)
		}
		waitTrace(t, s, runs[0])

		if n := len(lfEntries(t, s, "")); n != 0 {
			t.Errorf("entries after a cancel before the answer = %d, want 0", n)
		}
		if got := lfSpent(t, s); got != 0 {
			t.Errorf("spent_usd = %v, want 0", got)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		sc := routed(map[string]callFn{"ConventionExtraction": blockUntilDone})
		s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll(), llm: fastLLM(), now: capClock, extractTimeout: time.Second})
		requireNoSpend(t, s)
		scan := runScan(t, s, repo)

		if scan["status"] != "failed" {
			t.Fatalf("scan = %v, want failed at its timeout", scan)
		}
		if countOf(sc, "ConventionExtraction") == 0 {
			t.Fatal("the scan never reached the provider")
		}
		if n := len(lfEntries(t, s, "")); n != 0 {
			t.Errorf("entries after a timeout without an answer = %d, want 0", n)
		}
		if got := lfSpent(t, s); got != 0 {
			t.Errorf("spent_usd = %v, want 0", got)
		}
	})
}

// ---- AC7: the month is the month of the answer ----

// AC7, EC11: two chunks of one run answer at 2026-10-31T23:59:59.999Z and 2026-11-01T00:00:00.000Z.
// October's and November's totals hold one entry each, and the spend report of each month shows
// only its own.
func TestLF_RunCrossingMidnightAtMonthEndSplitsAcrossTwoMonths(t *testing.T) {
	lastOfOctober := time.Date(2026, 10, 31, 23, 59, 59, 999_000_000, time.UTC)
	firstOfNovember := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	at := func(ts time.Time, usd float64) callFn {
		return func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
			clock.Store(ts.UnixNano())
			return answer(answerJSON, 100, 10, usd), nil
		}
	}

	ad := &scripted{complete: lfNth(at(lastOfOctober, 0.001), at(firstOfNovember, 0.002))}
	s := lfReviewStack(t, ad, spec{now: now})
	repo := repoByName(t, s, "acme/payments-api")
	pr := newFx(t, s).pr(repo, 951, map[string]string{"src/a.ts": patchOf(3), "src/b.ts": patchOf(3)})
	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{mapReduceAgent(t, s)}})
	waitStatus(t, s, runs[0], "done")

	wantMonths := map[string]lfMonth{"2026-10": {Spent: 1000, Entries: 1}, "2026-11": {Spent: 2000, Entries: 1}}
	if diff := cmp.Diff(wantMonths, lfMonths(t, s)); diff != "" {
		t.Errorf("stored month totals mismatch (-want +got):\n%s", diff)
	}
	entries := lfEntries(t, s, "review_run")
	if len(entries) != 2 || !entries[0].OccurredAt.Equal(lastOfOctober) || !entries[1].OccurredAt.Equal(firstOfNovember) {
		t.Errorf("entry times = %+v, want %s and %s", entries, lastOfOctober, firstOfNovember)
	}

	// On 2026-11-01 only the second answer counts.
	st := s.get("/settings/spend").Obj(t)
	if st["month_start"] != "2026-11-01T00:00:00.000Z" || st["spent_usd"] != 0.002 {
		t.Errorf("GET /settings/spend on 2026-11-01 = %v, want month_start 2026-11-01T00:00:00.000Z and spent_usd 0.002", st)
	}
	// One millisecond earlier it is October, with the first answer only.
	clock.Store(lastOfOctober.UnixNano())
	st = s.get("/settings/spend").Obj(t)
	if st["month_start"] != "2026-10-01T00:00:00.000Z" || st["spent_usd"] != 0.001 {
		t.Errorf("GET /settings/spend on 2026-10-31T23:59:59.999Z = %v, want month_start 2026-10-01T00:00:00.000Z and spent_usd 0.001", st)
	}
}

// ---- AC12: a failed append never stops the work ----

// lfFailSecondInsert makes the database refuse the one entry that costs 2000 micro-USD (the
// second answer of the append-failure test), and no other.
func lfFailSecondInsert(t testing.TB, s *stack) {
	t.Helper()
	execSQL(t, s, `CREATE FUNCTION lf_fail_second_entry() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.cost_micros = 2000 THEN
				RAISE EXCEPTION 'lf injected append failure';
			END IF;
			RETURN NEW;
		END $$`)
	execSQL(t, s, `CREATE TRIGGER lf_fail_second_entry BEFORE INSERT ON cost_ledger
		FOR EACH ROW EXECUTE FUNCTION lf_fail_second_entry()`)
}

// AC12, EC16, FR15, NFR4: when the append of the second of three chunk answers fails, the run
// still completes as done, exactly one ERROR line names kind, provider, model, cost and workspace,
// the line holds neither prompt text nor the key, and that answer's cost is missing from the total.
func TestLF_FailedAppendLogsOneErrorAndTheRunCompletes(t *testing.T) {
	const (
		apiKey = "sk-or-v1-LFLEDGERKEY0123456789"
		marker = "LF-PROMPT-MARKER-DO-NOT-LOG"
		msg    = "cost ledger: entry not recorded; its cost is missing from the month total"
	)
	ad := &scripted{needsKey: true, complete: lfNth(lfAnswer(0.001), lfAnswer(0.002), lfAnswer(0.004))}
	s := lfReviewStack(t, ad, spec{env: map[string]string{"OPENROUTER_API_KEY": apiKey}})
	ws := workspaceID(t, s)
	requireNoSpend(t, s)
	lfFailSecondInsert(t, s)
	agent := newAgent(t, s, map[string]any{"name": "Marked Reducer", "strategy": "map-reduce", "model": "lf/model", "system_prompt": "Review the diff. " + marker})
	runs, _ := startOK(t, s, threeFiles(t, s, 952), map[string]any{"agentIds": []string{agent}})
	waitStatus(t, s, runs[0], "done")

	if ad.calls() != 3 {
		t.Fatalf("provider calls = %d, want 3 (the failed append must not stop the run)", ad.calls())
	}
	lines := logLines(t, s.logs, msg)
	if len(lines) != 1 {
		t.Fatalf("ERROR lines for the failed append = %d, want 1: %v", len(lines), lines)
	}
	line := lines[0]
	want := map[string]any{"level": "ERROR", "kind": "review_run", "provider": "openrouter", "model": "lf/model", "workspace_id": ws, "cost_usd": 0.002}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("ERROR line field %s = %v, want %v (line %v)", k, line[k], v, line)
		}
	}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "the ERROR line", string(raw), apiKey, marker)
	assertNoSecret(t, "the log", s.logs.String(), apiKey, marker)

	if diff := cmp.Diff([]int64{1000, 4000}, lfCosts(lfEntries(t, s, ""))); diff != "" {
		t.Errorf("stored entries mismatch (-want +got):\n%s", diff)
	}
	if got := lfSpent(t, s); got != 0.005 {
		t.Errorf("spent_usd = %v, want 0.005 (the failed append's 0.002 is missing)", got)
	}
}

// ---- AC11: the spend gate fails closed ----

// AC11, EC20: a cap equal to the month spend refuses a review start with 402; with a cap set and
// the stored totals unreadable all eight entry routes answer 500 and create nothing; without a cap
// the start does not read the totals at all.
func TestLF_SpendGateFailsClosedWhenTheTotalsCannotBeRead(t *testing.T) {
	f := newCapFixture(t, nil)
	renameTotals := func() { execSQL(t, f.s, `ALTER TABLE spend_month_totals RENAME TO lf_totals_gone`) }
	restoreTotals := func() { execSQL(t, f.s, `ALTER TABLE lf_totals_gone RENAME TO spend_month_totals`) }

	t.Run("cap equal to the spend", func(t *testing.T) {
		f.seedSpend(1)
		setCap(t, f.s, 1)
		before := f.work()
		r := startReview(t, f.s, f.pr, map[string]any{"all": true})
		if r.Status != http.StatusPaymentRequired || r.errorCode(t) != "spend_cap_reached" {
			t.Fatalf("POST review with the cap equal to the spend = %d %s, want 402 spend_cap_reached", r.Status, r.Body)
		}
		details := obj(t, r.JSON(t), "error", "details")
		if details["cap_usd"] != 1.0 || details["spent_usd"] != 1.0 {
			t.Errorf("402 details = %v, want cap_usd 1 and spent_usd 1", details)
		}
		if diff := cmp.Diff(before, f.work()); diff != "" {
			t.Errorf("the refused start did work (-before +after):\n%s", diff)
		}
	})

	t.Run("totals unreadable with a cap set", func(t *testing.T) {
		setCap(t, f.s, 1000)
		for _, route := range f.routes() {
			t.Run(route.name, func(t *testing.T) {
				before := f.work()
				renameTotals()
				r := route.call(f)
				restoreTotals()
				if r.Status != http.StatusInternalServerError || r.errorCode(t) != "internal_error" {
					t.Fatalf("%s with unreadable totals and a cap = %d %s, want 500 internal_error", route.name, r.Status, r.Body)
				}
				if diff := cmp.Diff(before, f.work()); diff != "" {
					t.Errorf("%s did work although the totals could not be read (-before +after):\n%s", route.name, diff)
				}
			})
		}
	})

	t.Run("no cap does not read the totals", func(t *testing.T) {
		setCap(t, f.s, nil)
		renameTotals()
		defer restoreTotals()
		r := startReview(t, f.s, f.pr, map[string]any{"all": true})
		if r.Status != http.StatusOK {
			t.Fatalf("POST review without a cap and with unreadable totals = %d %s, want 200", r.Status, r.Body)
		}
		for _, run := range r.Obj(t)["runs"].([]any) {
			if got := waitStatus(t, f.s, run.(map[string]any)["run_id"].(string), "done", "failed"); got != "done" {
				t.Errorf("run status = %s, want done (a failed append does not fail the run)", got)
			}
		}
	})
}
