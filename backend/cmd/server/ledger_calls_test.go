package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
)

// Component tests of the cost ledger at the LLM call sites, through the real server (cost ledger
// plan S19): one entry per answered call of every kind (AC1), regenerations add (AC2), a cost the
// provider reports badly is an unknown-cost entry (AC3), the run, eval and brief figures stay as
// they were (AC14) and no prompt, output, title or key reaches an entry or a log line (AC18). The
// helpers carry the prefix lc.

// lcNow is the fixed clock of these tests: a month far from the seeded demo data, so a fresh
// server's month spend is exactly 0 and every amount below is hand-computed.
var lcNow = time.Date(2032, 3, 10, 12, 0, 0, 0, time.UTC)

func lcClock() time.Time { return lcNow }

// lcMockMicros is the cost of one answer of the mock provider, $0.00042, in micro-USD.
const lcMockMicros = 420

// lcEntry is one cost_ledger row.
type lcEntry struct {
	ID          string
	WorkspaceID string
	OccurredAt  time.Time
	Kind        string
	Origin      string
	Provider    string
	Model       string
	TokensIn    *int64
	TokensOut   *int64
	CostMicros  *int64
	RefID       *string
}

// lcEntries lists the ledger rows, filtered by kind when kind is not "".
func lcEntries(t testing.TB, s *stack, kind string) []lcEntry {
	t.Helper()
	var out []lcEntry
	err := s.db.DB.Raw(`SELECT id::text AS id, workspace_id::text AS workspace_id, occurred_at, kind, origin,
			provider, model, tokens_in, tokens_out, cost_micros, ref_id::text AS ref_id
		FROM cost_ledger WHERE ? = '' OR kind = ? ORDER BY occurred_at, id`, kind, kind).Scan(&out).Error
	if err != nil {
		t.Fatalf("read cost_ledger: %v", err)
	}
	return out
}

// lcSpend reads GET /settings/spend: the month's known cost in micro-USD and the unpriced count.
func lcSpend(t testing.TB, s *stack) (micros int64, unpriced int) {
	t.Helper()
	r := s.get("/settings/spend")
	if r.Status != http.StatusOK {
		t.Fatalf("GET /settings/spend = %d %s, want 200", r.Status, r.Body)
	}
	o := r.Obj(t)
	usd, ok := o["spent_usd"].(float64)
	if !ok {
		t.Fatalf("GET /settings/spend spent_usd = %v, want a number", o["spent_usd"])
	}
	n, _ := o["unpriced_count"].(float64)
	return int64(math.Round(usd * 1e6)), int(n)
}

// lcCostSum adds the known costs of entries and counts the unknown ones.
func lcCostSum(entries []lcEntry) (sum int64, unknown int) {
	for _, e := range entries {
		if e.CostMicros == nil {
			unknown++
			continue
		}
		sum += *e.CostMicros
	}
	return sum, unknown
}

// lcKinds counts entries per kind.
func lcKinds(entries []lcEntry) map[string]int {
	out := map[string]int{}
	for _, e := range entries {
		out[e.Kind]++
	}
	return out
}

// lcNew is the entries of after whose id is not in before.
func lcNew(before, after []lcEntry) []lcEntry {
	seen := map[string]bool{}
	for _, e := range before {
		seen[e.ID] = true
	}
	var out []lcEntry
	for _, e := range after {
		if !seen[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// lcRefs returns the sorted ref ids of entries ("" for none).
func lcRefs(entries []lcEntry) []string {
	var out []string
	for _, e := range entries {
		r := ""
		if e.RefID != nil {
			r = *e.RefID
		}
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// lcCheckRefs fails unless the entries reference exactly want (in any order).
func lcCheckRefs(t testing.TB, what string, entries []lcEntry, want ...string) {
	t.Helper()
	want = slices.Clone(want)
	sort.Strings(want)
	if diff := cmp.Diff(want, lcRefs(entries)); diff != "" {
		t.Errorf("%s: entry refs mismatch (-want +got):\n%s", what, diff)
	}
}

// lcFixture is a server over the cloned and indexed repository o/r with scripted models: the tour
// is answered with tourAnswer at the mock's price, every other schema like the mock.
type lcFixture struct {
	t    *testing.T
	s    *stack
	sc   *scripted
	repo string
	pr   string
	ws   string
}

// newLCFixture starts the stack; extra replaces the scripted answer of a schema.
func newLCFixture(t *testing.T, extra map[string]callFn) *lcFixture {
	t.Helper()
	handlers := map[string]callFn{"OnboardingTour": func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return answer(tourAnswer, 900, 300, mock.CostUSD), nil
	}}
	for schema, fn := range extra {
		handlers[schema] = fn
	}
	sc := routed(handlers)
	s, _, repo := tourStack(t, spec{llmAdapters: sc.forAll(), now: lcClock, llm: fastLLM(), config: intentOn})
	f := &lcFixture{t: t, s: s, sc: sc, repo: repo, pr: pr482(t, s), ws: workspaceID(t, s)}
	if got := lcEntries(t, s, ""); len(got) != 0 {
		t.Fatalf("a fresh server has %d ledger entries, want 0 (D18)", len(got))
	}
	return f
}

// lcPost sends a POST that must answer 200.
func (f *lcFixture) post200(path string) {
	f.t.Helper()
	if r := f.s.post(path, nil); r.Status != http.StatusOK {
		f.t.Fatalf("POST %s = %d %s, want 200", path, r.Status, r.Body)
	}
}

// ---- AC1 ----

// AC1 (FR1, FR2, FR3, FR7): each answered attempt of every kind has exactly one entry with the
// matching kind, workspace, provider, model and reference, and the month spend rises by the cost of
// the answers.
func TestLedgerCalls_EveryKindRecordsOneEntryPerAnsweredCall(t *testing.T) {
	f := newLCFixture(t, nil)
	s := f.s

	// step runs one unit of work and returns the entries it added by kind. Every answered call of
	// the unit made exactly one entry, all live, at the fixed clock, of the seeded workspace.
	step := func(name string, work func()) map[string][]lcEntry {
		t.Helper()
		callsBefore, before := f.sc.calls(), lcEntries(t, s, "")
		work()
		added := lcNew(before, lcEntries(t, s, ""))
		if calls := f.sc.calls() - callsBefore; len(added) != calls {
			t.Errorf("%s: %d entries for %d answered calls, want one each", name, len(added), calls)
		}
		out := map[string][]lcEntry{}
		for _, e := range added {
			out[e.Kind] = append(out[e.Kind], e)
			if e.WorkspaceID != f.ws || e.Origin != "live" || !e.OccurredAt.Equal(lcNow) {
				t.Errorf("%s: entry = workspace %s origin %s at %s, want %s live at %s", name, e.WorkspaceID, e.Origin, e.OccurredAt, f.ws, lcNow)
			}
			if e.Provider == "" || e.Model == "" || e.TokensIn == nil || e.TokensOut == nil {
				t.Errorf("%s: %s entry = %+v, want provider, model and tokens", name, e.Kind, e)
			}
		}
		return out
	}
	wantKinds := func(name string, got map[string][]lcEntry, want map[string]int) {
		t.Helper()
		counts := map[string]int{}
		for k, es := range got {
			counts[k] = len(es)
		}
		if diff := cmp.Diff(want, counts); diff != "" {
			t.Errorf("%s: entries per kind mismatch (-want +got):\n%s", name, diff)
		}
	}
	// model is the model the scripted provider saw in its latest attempt for schema.
	modelOf := func(schema string) string {
		t.Helper()
		at := attemptsOf(f.sc, schema)
		if len(at) == 0 {
			t.Fatalf("the provider got no %s attempt", schema)
		}
		return at[len(at)-1].Model
	}

	t.Run("single-agent review and its intent pre-work", func(t *testing.T) {
		var run string
		got := step("review", func() { run = reviewDone(t, s, f.pr, agentByName(t, s, agentGeneral))[0] })
		wantKinds("review", got, map[string]int{"review_run": 1, "review_intent": 1})
		lcCheckRefs(t, "review_run", got["review_run"], run)
		lcCheckRefs(t, "review_intent", got["review_intent"], f.pr)
		var row struct{ Provider, Model string }
		if err := s.db.DB.Raw(`SELECT provider, model FROM agent_runs WHERE id = ?`, run).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if e := got["review_run"][0]; e.Provider != row.Provider || e.Model != row.Model {
			t.Errorf("review_run entry provider/model = %s/%s, want the run's %s/%s", e.Provider, e.Model, row.Provider, row.Model)
		}
	})
	t.Run("map-reduce review of three chunks", func(t *testing.T) {
		pr := threeFiles(t, s, 940)
		var run string
		got := step("map-reduce", func() { run = reviewDone(t, s, pr, mapReduceAgent(t, s))[0] })
		wantKinds("map-reduce", got, map[string]int{"review_run": 3, "review_intent": 1})
		lcCheckRefs(t, "review_run", got["review_run"], run, run, run)
		lcCheckRefs(t, "review_intent", got["review_intent"], pr)
	})
	t.Run("intent refresh", func(t *testing.T) {
		got := step("intent refresh", func() { f.post200("/pulls/" + f.pr + "/intent/refresh") })
		wantKinds("intent refresh", got, map[string]int{"intent_refresh": 1})
		lcCheckRefs(t, "intent_refresh", got["intent_refresh"], f.pr)
		if e := got["intent_refresh"][0]; e.Model != modelOf("IntentClassification") {
			t.Errorf("intent_refresh entry model = %s, want the model of the call %s", e.Model, modelOf("IntentClassification"))
		}
	})
	t.Run("brief", func(t *testing.T) {
		got := step("brief", func() { f.post200("/pulls/" + f.pr + "/brief") })
		wantKinds("brief", got, map[string]int{"brief": 1})
		lcCheckRefs(t, "brief", got["brief"], f.pr)
		if e := got["brief"][0]; e.Model != modelOf("PrBriefLlm") {
			t.Errorf("brief entry model = %s, want the model of the call %s", e.Model, modelOf("PrBriefLlm"))
		}
	})
	t.Run("onboarding tour on the scripted adapter", func(t *testing.T) { // D17: the mock has no tour fixture
		got := step("onboarding", func() { f.post200("/repos/" + f.repo + "/onboarding") })
		wantKinds("onboarding", got, map[string]int{"onboarding": 1})
		lcCheckRefs(t, "onboarding", got["onboarding"], f.repo)
		e := got["onboarding"][0]
		if e.Model != modelOf("OnboardingTour") || *e.TokensIn != 900 || *e.TokensOut != 300 {
			t.Errorf("onboarding entry = model %s tokens %d/%d, want %s 900/300", e.Model, *e.TokensIn, *e.TokensOut, modelOf("OnboardingTour"))
		}
	})
	t.Run("conventions scan", func(t *testing.T) {
		var scan map[string]any
		callsBefore := countOf(f.sc, mock.ConventionExtraction)
		got := step("conventions", func() {
			if r := s.post("/repos/"+f.repo+"/conventions/extract", nil); r.Status != http.StatusAccepted {
				t.Fatalf("POST conventions/extract = %d %s, want 202", r.Status, r.Body)
			}
			scan = waitScan(t, s, f.repo)
		})
		answers := countOf(f.sc, mock.ConventionExtraction) - callsBefore
		if answers < 1 {
			t.Fatalf("the scan made %d extraction calls, want at least 1", answers)
		}
		wantKinds("conventions", got, map[string]int{"conventions_scan": answers})
		scanID, _ := scan["id"].(string)
		refs := slices.Repeat([]string{scanID}, answers)
		lcCheckRefs(t, "conventions_scan", got["conventions_scan"], refs...)
	})
	t.Run("eval case", func(t *testing.T) {
		agent := evAgent(t, s, "Lc Eval")
		cases := evSuiteCases(t, s, "agent", agent, 2)
		got := step("eval", func() {
			r := s.post("/agents/"+agent+"/eval-runs", nil)
			if r.Status != http.StatusAccepted {
				t.Fatalf("POST eval-runs = %d %s, want 202", r.Status, r.Body)
			}
			evAwait(t, s, idOf(t, r))
		})
		wantKinds("eval", got, map[string]int{"eval_case": 2})
		lcCheckRefs(t, "eval_case", got["eval_case"], cases...)
		for _, e := range got["eval_case"] {
			if e.Provider != "openrouter" || e.Model != "model-lc-eval" {
				t.Errorf("eval_case entry provider/model = %s/%s, want openrouter/model-lc-eval", e.Provider, e.Model)
			}
		}
	})

	// Every answer cost $0.00042: the entries add up to that times the calls, and so does the month.
	all := lcEntries(t, s, "")
	calls := f.sc.calls()
	if len(all) != calls {
		t.Errorf("%d entries for %d answered calls in all, want one each", len(all), calls)
	}
	if sum, unknown := lcCostSum(all); sum != int64(calls)*lcMockMicros || unknown != 0 {
		t.Errorf("entries cost %d micro-USD (%d unknown), want %d (0 unknown)", sum, unknown, int64(calls)*lcMockMicros)
	}
	if spent, unpriced := lcSpend(t, s); spent != int64(calls)*lcMockMicros || unpriced != 0 {
		t.Errorf("GET /settings/spend = %d micro-USD, %d unpriced, want %d, 0", spent, unpriced, int64(calls)*lcMockMicros)
	}
	// The llm package logs a call without a usable Charge and records nothing for it (security L3).
	if logs := s.logs.String(); strings.Contains(logs, "no usable spend charge") {
		t.Errorf("the server log holds the missing-charge warning, want every call to carry a Charge:\n%s", logs)
	}
	wantKindsSeen := []string{"brief", "conventions_scan", "eval_case", "intent_refresh", "onboarding", "review_intent", "review_run"}
	var seen []string
	for k := range lcKinds(all) {
		seen = append(seen, k)
	}
	sort.Strings(seen)
	if diff := cmp.Diff(wantKindsSeen, seen); diff != "" {
		t.Errorf("kinds recorded mismatch (-want +got):\n%s", diff)
	}
}

// ---- AC2 ----

// AC2 (FR1, FR4, FR8, EC1): regenerating adds. Two intent refreshes, two briefs and two tours rise
// the month spend by 6 x $0.00042, and a cap 1 micro-USD above the old total refuses the next brief.
func TestLedgerCalls_RegenerateAddsAndTheCapSeesIt(t *testing.T) {
	f := newLCFixture(t, nil)
	s := f.s
	// The cap has at most 2 decimals (PUT /settings), so "1 micro-USD above the old total" is a
	// total of $0.009999 and a cap of $0.01.
	const oldMicros = 9_999
	seedLedgerSpend(t, s, f.ws, lcNow, 0.009999)
	if spent, _ := lcSpend(t, s); spent != oldMicros {
		t.Fatalf("seeded month spend = %d micro-USD, want %d", spent, oldMicros)
	}

	for range 2 {
		f.post200("/pulls/" + f.pr + "/intent/refresh")
		f.post200("/pulls/" + f.pr + "/brief")
		f.post200("/repos/" + f.repo + "/onboarding")
	}
	if diff := cmp.Diff(map[string]int{"brief": 2, "intent_refresh": 2, "onboarding": 2, "review_run": 1}, lcKinds(lcEntries(t, s, ""))); diff != "" {
		t.Errorf("entries per kind mismatch (-want +got; review_run is the seed):\n%s", diff)
	}
	if spent, _ := lcSpend(t, s); spent != oldMicros+6*lcMockMicros {
		t.Errorf("month spend after 6 regenerations = %d micro-USD, want %d", spent, oldMicros+6*lcMockMicros)
	}

	setCap(t, s, 0.01) // 1 micro-USD above the old total
	calls, entries := f.sc.calls(), len(lcEntries(t, s, ""))
	r := s.post("/pulls/"+f.pr+"/brief", nil)
	if r.Status != http.StatusPaymentRequired || r.errorCode(t) != "spend_cap_reached" {
		t.Errorf("POST brief with the cap 1 micro-USD above the old total = %d %s, want 402 spend_cap_reached", r.Status, r.Body)
	}
	if f.sc.calls() != calls || len(lcEntries(t, s, "")) != entries {
		t.Errorf("a refused brief made %d model calls and %d entries, want none", f.sc.calls()-calls, len(lcEntries(t, s, ""))-entries)
	}
}

// EC2: the stand's finding. An intent refresh whose answer costs $0.00375 used to change the month
// total by $0.000000 (the refresh replaced the earlier cost); now each one adds.
func TestLedgerCalls_IntentRefreshAtTheStandPriceAdds(t *testing.T) {
	const stand = 0.00375
	f := newLCFixture(t, map[string]callFn{"IntentClassification": func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
		res, err := mockAnswer(ctx, at)
		cost := stand
		res.CostUSD = &cost
		return res, err
	}})
	s := f.s
	f.post200("/pulls/" + f.pr + "/intent/refresh")
	if spent, _ := lcSpend(t, s); spent != 3750 {
		t.Errorf("month spend after 1 refresh = %d micro-USD, want 3750", spent)
	}
	f.post200("/pulls/" + f.pr + "/intent/refresh")
	if spent, _ := lcSpend(t, s); spent != 7500 {
		t.Errorf("month spend after 2 refreshes = %d micro-USD, want 7500 (2x, not 1x)", spent)
	}
	if got := lcKinds(lcEntries(t, s, "")); got["intent_refresh"] != 2 {
		t.Errorf("entries per kind = %v, want 2 intent_refresh", got)
	}
}

// ---- AC3 ----

// lcAnswer is a valid Review answer with the given cost; the cost can be a value answer() refuses.
func lcAnswer(cost *float64) llm.AttemptResult {
	return llm.AttemptResult{Raw: answerJSON, Usage: &llm.Usage{TokensIn: 100, TokensOut: 10}, CostUSD: cost}
}

// lcTwoFilePR is a pull request of two files, which a map-reduce agent reviews with 2 calls.
func lcTwoFilePR(t *testing.T, s *stack, number int) string {
	t.Helper()
	repo := repoByName(t, s, "acme/payments-api")
	return newFx(t, s).pr(repo, number, map[string]string{"src/a.ts": patchOf(3), "src/b.ts": patchOf(3)})
}

// AC3 (FR3, FR8, FR13, EC3): a model with no price: 2 answers are 2 unknown-cost entries, the month
// spend is unchanged, unpriced_count is 2 and a cap above the spend still admits the next review.
func TestLedgerCalls_UnpricedModelMakesUnknownCostEntries(t *testing.T) {
	sc := routed(map[string]callFn{mock.Review: func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return lcAnswer(nil), nil // no reported cost, and the price book has no such model
	}})
	s := startStack(t, spec{llmAdapters: sc.forAll(), llm: fastLLM(), now: lcClock})
	ws := workspaceID(t, s)
	pr := lcTwoFilePR(t, s, 941)
	agent := newAgent(t, s, map[string]any{"name": "Unpriced", "model": "lc-vendor/no-such-model-7", "strategy": "map-reduce"})

	run := reviewDone(t, s, pr, agent)[0]
	entries := lcEntries(t, s, "")
	if len(entries) != 2 {
		t.Fatalf("a review with 2 answers made %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Kind != "review_run" || e.CostMicros != nil || e.Model != "lc-vendor/no-such-model-7" || e.WorkspaceID != ws || e.TokensIn == nil || *e.TokensIn != 100 {
			t.Errorf("entry = %+v, want a review_run with unknown cost, the agent's model and 100 tokens in", e)
		}
	}
	lcCheckRefs(t, "review_run", entries, run, run)
	if spent, unpriced := lcSpend(t, s); spent != 0 || unpriced != 2 {
		t.Errorf("GET /settings/spend = %d micro-USD, %d unpriced, want 0, 2", spent, unpriced)
	}

	setCap(t, s, 1.0) // well above the spend
	r := startReview(t, s, pr, map[string]any{"agentIds": []string{agent}})
	if r.Status != http.StatusOK {
		t.Fatalf("POST review with a cap above the spend = %d %s, want 200 (unpriced usage does not reach the cap)", r.Status, r.Body)
	}
	for _, run := range r.Obj(t)["runs"].([]any) {
		waitStatus(t, s, run.(map[string]any)["run_id"].(string), "done")
	}
	if _, unpriced := lcSpend(t, s); unpriced != 4 {
		t.Errorf("unpriced_count after a second review = %d, want 4", unpriced)
	}
}

// AC3 (FR3, EC21): an answer reporting a cost of -1 or NaN stores an unknown cost and the review
// goes on to finish; a good cost beside it is kept.
func TestLedgerCalls_BadReportedCostsAreUnknownNeverACrash(t *testing.T) {
	nan, negative, good := math.NaN(), -1.0, 0.002
	for _, tc := range []struct {
		name string
		cost *float64
		want *int64
	}{
		{name: "negative", cost: &negative},
		{name: "NaN", cost: &nan},
		{name: "control: a good cost", cost: &good, want: ptr(int64(2000))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := routed(map[string]callFn{mock.Review: func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
				return lcAnswer(tc.cost), nil
			}})
			s := startStack(t, spec{llmAdapters: sc.forAll(), llm: fastLLM(), now: lcClock})
			// A priced model: a bad reported cost must not fall back to the price book's estimate.
			agent := newAgent(t, s, map[string]any{"name": "Bad cost"})
			reviewDone(t, s, pr482(t, s), agent)

			entries := lcEntries(t, s, "review_run")
			if len(entries) != 1 {
				t.Fatalf("a review with 1 answer made %d review_run entries, want 1", len(entries))
			}
			if diff := cmp.Diff(tc.want, entries[0].CostMicros); diff != "" {
				t.Errorf("entry cost for a reported %v mismatch (-want +got):\n%s", *tc.cost, diff)
			}
			wantSpent, wantUnpriced := int64(0), 1
			if tc.want != nil {
				wantSpent, wantUnpriced = *tc.want, 0
			}
			if spent, unpriced := lcSpend(t, s); spent != wantSpent || unpriced != wantUnpriced {
				t.Errorf("GET /settings/spend = %d micro-USD, %d unpriced, want %d, %d", spent, unpriced, wantSpent, wantUnpriced)
			}
			if lines := logLines(t, s.logs, "cost ledger: entry not recorded; its cost is missing from the month total"); len(lines) != 0 {
				t.Errorf("a bad cost logged %d ledger failure line(s), want none (it is an unknown cost, not a failure): %v", len(lines), lines)
			}
		})
	}
}

// AC3 (FR3, EC21): OpenRouter reporting usage.cost as a string ("0.1") is not a number: with an
// unpriced model that is an unknown cost. A numeric usage.cost beside it is kept.
func TestLedgerCalls_StringCostFromOpenRouterIsUnknown(t *testing.T) {
	stub := newLLMStub(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		var cost any = "0.1"
		if n == 2 {
			cost = 0.25
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": answerJSON}}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "cost": cost},
		})
	})
	closed := "http://127.0.0.1:1"
	s := startStack(t, spec{
		llmURLs: llmURLs{openAI: closed, anthropic: closed, openRouter: stub.URL()}, llm: fastLLM(), now: lcClock,
		env: map[string]string{"OPENROUTER_API_KEY": "sk-or-lc-string-cost"},
	})
	agent := newAgent(t, s, map[string]any{"name": "String cost", "model": "lc-vendor/no-such-model-8"})
	pr := pr482(t, s)

	reviewDone(t, s, pr, agent)
	first := lcEntries(t, s, "review_run")
	if len(first) != 1 || first[0].CostMicros != nil {
		t.Fatalf(`entries after an answer with usage.cost "0.1" = %+v, want 1 entry with unknown cost`, first)
	}
	if spent, unpriced := lcSpend(t, s); spent != 0 || unpriced != 1 {
		t.Errorf(`after cost "0.1": spend = %d micro-USD, %d unpriced, want 0, 1`, spent, unpriced)
	}

	reviewDone(t, s, pr, agent)
	second := lcNew(first, lcEntries(t, s, "review_run"))
	if len(second) != 1 || second[0].CostMicros == nil || *second[0].CostMicros != 250_000 {
		t.Fatalf("entries after an answer with usage.cost 0.25 = %+v, want 1 entry of 250000 micro-USD", second)
	}
	if spent, unpriced := lcSpend(t, s); spent != 250_000 || unpriced != 1 {
		t.Errorf("after cost 0.25: spend = %d micro-USD, %d unpriced, want 250000, 1", spent, unpriced)
	}
}

// ---- AC14 ----

// AC14 (FR11, NFR5): the figures of a completed review run, eval run and brief on the mock provider
// are what they were before the ledger, and the entries of each add up to them within 1 micro-USD
// per entry.
func TestLedgerCalls_OwnCostFieldsAreUnchangedAndMatchTheEntries(t *testing.T) {
	sc := routed(nil) // the mock's answers
	s := startStack(t, spec{llmAdapters: sc.forAll(), llm: fastLLM(), now: lcClock})
	near := func(what string, gotMicros, wantMicros int64, entries int) {
		t.Helper()
		if d := gotMicros - wantMicros; d < -int64(entries) || d > int64(entries) {
			t.Errorf("%s = %d micro-USD, entries sum to %d: differ by more than %d", what, gotMicros, wantMicros, entries)
		}
	}

	t.Run("review run", func(t *testing.T) {
		pr := threeFiles(t, s, 942)
		run := reviewDone(t, s, pr, mapReduceAgent(t, s))[0]
		var row struct {
			CostMicros         int64
			TokensIn, TokensOu int64
		}
		if err := s.db.DB.Raw(`SELECT round(cost_usd * 1000000)::bigint AS cost_micros, tokens_in, tokens_out AS tokens_ou FROM agent_runs WHERE id = ?`, run).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.CostMicros != 3*lcMockMicros || row.TokensIn != 3*mock.TokensIn || row.TokensOu != 3*mock.TokensOut {
			t.Errorf("run cost/tokens = %d micro-USD, %d in, %d out, want %d, %d, %d (3 mock answers)",
				row.CostMicros, row.TokensIn, row.TokensOu, 3*lcMockMicros, 3*mock.TokensIn, 3*mock.TokensOut)
		}
		var mine []lcEntry
		for _, e := range lcEntries(t, s, "review_run") {
			if e.RefID != nil && *e.RefID == run {
				mine = append(mine, e)
			}
		}
		sum, _ := lcCostSum(mine)
		near("run cost", row.CostMicros, sum, len(mine))
		if len(mine) != 3 {
			t.Errorf("the run has %d entries, want 3", len(mine))
		}
	})
	t.Run("eval run", func(t *testing.T) {
		agent := evAgent(t, s, "Lc Own Fields")
		cases := evSuiteCases(t, s, "agent", agent, 2)
		r := s.post("/agents/"+agent+"/eval-runs", nil)
		if r.Status != http.StatusAccepted {
			t.Fatalf("POST eval-runs = %d %s, want 202", r.Status, r.Body)
		}
		run := idOf(t, r)
		evAwait(t, s, run)
		var rows []struct {
			CaseID     string
			CostMicros int64
		}
		if err := s.db.DB.Raw(`SELECT case_id::text AS case_id, round(cost_usd * 1000000)::bigint AS cost_micros FROM eval_runs WHERE run_id = ? ORDER BY case_id`, run).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(cases) {
			t.Fatalf("the eval run stored %d case results, want %d", len(rows), len(cases))
		}
		var suiteMicros int64
		if err := s.db.DB.Raw(`SELECT round(cost_usd * 1000000)::bigint FROM eval_suite_runs WHERE id = ?`, run).Scan(&suiteMicros).Error; err != nil {
			t.Fatal(err)
		}
		byCase := map[string]int64{}
		for _, e := range lcEntries(t, s, "eval_case") {
			if e.RefID != nil && e.CostMicros != nil {
				byCase[*e.RefID] += *e.CostMicros
			}
		}
		for _, row := range rows {
			if row.CostMicros != lcMockMicros {
				t.Errorf("case %s own cost = %d micro-USD, want %d (1 mock answer)", row.CaseID, row.CostMicros, lcMockMicros)
			}
			near("case "+row.CaseID+" cost", row.CostMicros, byCase[row.CaseID], 1)
		}
		near("eval run cost", suiteMicros, byCase[cases[0]]+byCase[cases[1]], len(cases))
		if suiteMicros != int64(len(cases))*lcMockMicros {
			t.Errorf("eval run own cost = %d micro-USD, want %d", suiteMicros, int64(len(cases))*lcMockMicros)
		}
	})
	t.Run("brief", func(t *testing.T) {
		pr := pr482(t, s)
		if r := s.post("/pulls/"+pr+"/brief", nil); r.Status != http.StatusOK {
			t.Fatalf("POST brief = %d %s, want 200", r.Status, r.Body)
		}
		var row struct {
			CostMicros         int64
			TokensIn, TokensOu int64
		}
		if err := s.db.DB.Raw(`SELECT round((json->>'cost_usd')::numeric * 1000000)::bigint AS cost_micros,
			(json->>'tokens_in')::bigint AS tokens_in, (json->>'tokens_out')::bigint AS tokens_ou FROM pr_brief WHERE pr_id = ?`, pr).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.CostMicros != lcMockMicros || row.TokensIn != mock.TokensIn || row.TokensOu != mock.TokensOut {
			t.Errorf("brief cost/tokens = %d micro-USD, %d in, %d out, want %d, %d, %d", row.CostMicros, row.TokensIn, row.TokensOu, lcMockMicros, mock.TokensIn, mock.TokensOut)
		}
		entries := lcEntries(t, s, "brief")
		if len(entries) != 1 {
			t.Fatalf("the brief has %d entries, want 1", len(entries))
		}
		near("brief cost", row.CostMicros, *entries[0].CostMicros, 1)
		if *entries[0].TokensIn != row.TokensIn || *entries[0].TokensOut != row.TokensOu {
			t.Errorf("brief entry tokens = %d/%d, want the brief's %d/%d", *entries[0].TokensIn, *entries[0].TokensOut, row.TokensIn, row.TokensOu)
		}
	})
}

// ---- AC18 ----

const (
	lcCanary    = "CANARY-7f3a"      // in the PR title and diff
	lcOutCanary = "CANARY-OUT-5c21"  // in the model's answers
	lcKey       = "sk-or-LCKEY-9d84" // the stored provider key
)

// lcLedgerFailed is the one ERROR line an entry that could not be recorded writes.
const lcLedgerFailed = "cost ledger: entry not recorded; its cost is missing from the month total"

// AC18 (FR2, FR15, NFR4): with a canary in the PR title and diff (and in the model's answer, and a
// provider key set) no ledger row and no log line of the feature holds a prompt, output, title or
// key. It holds as well when the entries cannot be recorded and the FR15 ERROR lines are written.
func TestLedgerCalls_PrivacyCanaryReachesNoEntryAndNoLogLine(t *testing.T) {
	sc := routed(map[string]callFn{mock.Review: func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return answer(fmt.Sprintf(`{"verdict":"comment","summary":%q,"score":80,"findings":[]}`, lcOutCanary), 100, 10, mock.CostUSD), nil
	}})
	s := startStack(t, spec{
		llmAdapters: sc.forAll(), llm: fastLLM(), now: lcClock, config: intentOn,
		env: map[string]string{"OPENROUTER_API_KEY": lcKey},
	})
	repo := repoByName(t, s, "acme/payments-api")
	pr := scalar[string](t, s.db.DB, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, 943, ?, 'octo', 'feat', 'main', ?) RETURNING id::text`, workspaceID(t, s), repo, "Fix "+lcCanary+" in billing", fmt.Sprintf("%040x", 943))
	execSQL(t, s, `INSERT INTO pr_files (pr_id, path, patch) VALUES (?, 'src/billing.ts', ?)`, pr,
		"@@ -0,0 +1,3 @@\n+const a = 1\n+// "+lcCanary+" secret diff text\n+const b = 2")

	// leaks lists every place the canaries, the key or the prompt text show up.
	leaks := func(what, text string) {
		t.Helper()
		for _, bad := range []string{lcCanary, lcOutCanary, lcKey, "secret diff text", "Bearer ", "system_prompt"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s holds %q, want no prompt, output, title, key or header", what, bad)
			}
		}
	}
	audit := func(phase string) {
		t.Helper()
		var rows []string
		for _, table := range []string{"cost_ledger", "spend_month_totals", "cost_ledger_backfill"} {
			var dump []string
			if err := s.db.DB.Raw(fmt.Sprintf(`SELECT to_jsonb(x)::text FROM %s x`, table)).Scan(&dump).Error; err != nil {
				t.Fatalf("dump %s: %v", table, err)
			}
			rows = append(rows, dump...)
		}
		leaks(phase+": the ledger tables", strings.Join(rows, "\n"))
		for _, e := range s.logs.entries(t) {
			if msg, _ := e["msg"].(string); strings.HasPrefix(msg, "cost ledger") {
				raw, _ := json.Marshal(e)
				leaks(phase+": the log line "+msg, string(raw))
			}
		}
		leaks(phase+": the server log", s.logs.String())
	}

	// The ledger's columns are a closed set of facts: no column could hold free text from a call.
	var columns []string
	if err := s.db.DB.Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'cost_ledger' ORDER BY column_name`).Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	wantColumns := []string{"cost_micros", "dedupe_key", "id", "kind", "model", "occurred_at", "origin", "provider", "ref_id", "tokens_in", "tokens_out", "workspace_id"}
	if diff := cmp.Diff(wantColumns, columns); diff != "" {
		t.Errorf("cost_ledger columns mismatch (-want +got):\n%s", diff)
	}

	agent := agentByName(t, s, agentGeneral)
	reviewDone(t, s, pr, agent)
	if r := s.post("/pulls/"+pr+"/intent/refresh", nil); r.Status != http.StatusOK {
		t.Fatalf("POST intent/refresh = %d %s, want 200", r.Status, r.Body)
	}
	if r := s.post("/pulls/"+pr+"/brief", nil); r.Status != http.StatusOK {
		t.Fatalf("POST brief = %d %s, want 200", r.Status, r.Body)
	}
	kinds := lcKinds(lcEntries(t, s, ""))
	if diff := cmp.Diff(map[string]int{"review_run": 1, "review_intent": 1, "intent_refresh": 1, "brief": 1}, kinds); diff != "" {
		t.Fatalf("entries per kind mismatch (-want +got), the audit would check nothing:\n%s", diff)
	}
	audit("recorded entries")

	// Now every append fails: each answered call writes the FR15 ERROR line, still without content.
	execSQL(t, s, `ALTER TABLE cost_ledger ADD CONSTRAINT lc_block_appends CHECK (false) NOT VALID`)
	reviewDone(t, s, pr, agent)
	for _, path := range []string{"/pulls/" + pr + "/intent/refresh", "/pulls/" + pr + "/brief"} {
		if r := s.post(path, nil); r.Status != http.StatusOK {
			t.Fatalf("POST %s with the ledger failing = %d %s, want 200 (the work goes on)", path, r.Status, r.Body)
		}
	}
	failed := logLines(t, s.logs, lcLedgerFailed)
	var failedKinds []string // the intent of the PR is cached by now, so the second review derives none
	for _, l := range failed {
		failedKinds = append(failedKinds, fmt.Sprint(l["kind"]))
	}
	sort.Strings(failedKinds)
	if diff := cmp.Diff([]string{"brief", "intent_refresh", "review_run"}, failedKinds); diff != "" {
		t.Errorf("kinds of the ledger failure lines (one per answered call) mismatch (-want +got):\n%s", diff)
	}
	audit("failed appends")
}
