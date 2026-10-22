package parity_test

// S32 (AC5030, AC5032, spec 7.5, D5017): the review run executor with the intent hook ON against
// intent.json, the Node ReviewRunExecutor with the real IntentService behind the IntentResolver port.
//
// The intent service is not under test here (S23/S30 test it); the golden records what crossed the
// port, request by request (`resolve.calls`). The fake resolver below asserts the request the REAL Go
// executor hands over (pull, repo, changed files in diff order, the stored diff by length and SHA-256,
// not forced, a cancellable context), emits the recorded events, blocks until its context ends when
// the golden says every run of the request was cancelled, and returns the recorded result. Everything
// else is the Phase 4 harness (runs_fakes_test.go): the real executor, engine, run bus and LLM path
// over in-memory fakes, compared by compareRun with the same rules as runs.json. What this pins, per
// scenario, from the golden: the events (intent events after "Diff ready" and before "Starting review
// with agent", the scope line after "Citation grounding"), the provider requests (the "## PR intent"
// section in every chunk), the prompt log (intent_rule and intent after pr_description), the traces
// (intent and prompt_assembly.intent), the stored out_of_scope flags (the findings INSERT of
// tx.commit) and the agent_runs stats (no intent tokens or cost).
//
// The Phase 4 goldens (runs.json, sse.json) are the switch-off case and run unchanged in
// runs_test.go with no resolver at all (AC5032); TestIntentParity_SwitchOff below also pins that
// the harness without a resolver makes no intent request.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/app/executor"
	"dev-digest-go/backend/internal/reviews/domain"

	"github.com/google/go-cmp/cmp"
)

// ---- golden shapes -----------------------------------------------------------------------------

type intentGolden struct {
	Scenarios []intentScenario `json:"scenarios"`
}

type intentScenario struct {
	runScenario
	Resolve struct {
		Calls []resolveCall `json:"calls"`
	} `json:"resolve"`
}

type wireIntentSource struct {
	Kind   string  `json:"kind"`
	Ref    string  `json:"ref"`
	Status string  `json:"status"`
	Detail *string `json:"detail"`
}

func wireSources(in []wireIntentSource) []reviewer.IntentSource {
	var out []reviewer.IntentSource
	for _, s := range in {
		out = append(out, reviewer.IntentSource{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail})
	}
	return out
}

type resolveCall struct {
	Input struct {
		WorkspaceID string `json:"workspaceId"`
		Pull        struct {
			ID          string  `json:"id"`
			WorkspaceID string  `json:"workspaceId"`
			RepoID      string  `json:"repoId"`
			Number      int     `json:"number"`
			Title       string  `json:"title"`
			Author      string  `json:"author"`
			Branch      string  `json:"branch"`
			Base        string  `json:"base"`
			HeadSHA     string  `json:"headSha"`
			Body        *string `json:"body"`
		} `json:"pull"`
		Repo struct {
			ID    string `json:"id"`
			Owner string `json:"owner"`
			Name  string `json:"name"`
		} `json:"repo"`
		ChangedFiles      []string `json:"changedFiles"`
		DiffExcerptLen    int      `json:"diffExcerptLen"`
		DiffExcerptSHA256 string   `json:"diffExcerptSha256"`
		Force             bool     `json:"force"`
		HasSignal         bool     `json:"hasSignal"`
	} `json:"input"`
	Events []struct {
		Kind string          `json:"kind"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	} `json:"events"`
	Result struct {
		Status  string `json:"status"`
		Warning string `json:"warning"`
		Intent  *struct {
			Intent      string             `json:"intent"`
			InScope     []string           `json:"in_scope"`
			OutOfScope  []string           `json:"out_of_scope"`
			ChangeType  *string            `json:"change_type"`
			Confidence  *string            `json:"confidence"`
			DerivedFrom string             `json:"derived_from"`
			Sources     []wireIntentSource `json:"sources"`
		} `json:"intent"`
		Trace *struct {
			CacheHit    bool               `json:"cache_hit"`
			Confidence  *string            `json:"confidence"`
			DerivedFrom *string            `json:"derived_from"`
			ChangeType  *string            `json:"change_type"`
			Provider    string             `json:"provider"`
			Model       string             `json:"model"`
			TokensIn    int                `json:"tokens_in"`
			TokensOut   int                `json:"tokens_out"`
			CostUSD     *float64           `json:"cost_usd"`
			Sources     []wireIntentSource `json:"sources"`
		} `json:"trace"`
	} `json:"result"`
	SignalAbortedAtReturn bool `json:"signalAbortedAtReturn"`
}

// result is the IntentResult the recorded outcome stands for.
func (c *resolveCall) result() (reviewsapp.IntentResult, error) {
	r := c.Result
	switch r.Status {
	case domain.IntentStatusUnavailable:
		return reviewsapp.IntentResult{Trace: domain.UnavailableIntentTrace(r.Warning)}, nil
	case domain.IntentStatusUsed:
		if r.Intent == nil || r.Trace == nil {
			return reviewsapp.IntentResult{}, fmt.Errorf("a used result needs an intent and a trace")
		}
		return reviewsapp.IntentResult{
			Intent: &reviewer.Intent{
				Text: r.Intent.Intent, InScope: r.Intent.InScope, OutOfScope: r.Intent.OutOfScope,
				ChangeType: r.Intent.ChangeType, Confidence: r.Intent.Confidence,
				DerivedFrom: r.Intent.DerivedFrom, Sources: wireSources(r.Intent.Sources),
			},
			Trace: &domain.IntentTrace{
				Status: domain.IntentStatusUsed, CacheHit: r.Trace.CacheHit, Confidence: r.Trace.Confidence,
				DerivedFrom: r.Trace.DerivedFrom, ChangeType: r.Trace.ChangeType, Provider: r.Trace.Provider,
				Model: r.Trace.Model, TokensIn: r.Trace.TokensIn, TokensOut: r.Trace.TokensOut,
				CostUSD: r.Trace.CostUSD, Sources: wireSources(r.Trace.Sources),
			},
		}, nil
	}
	return reviewsapp.IntentResult{}, fmt.Errorf("unknown result status %q", r.Status)
}

func loadIntent(t testing.TB) (*intentGolden, map[string]string) {
	t.Helper()
	var g intentGolden
	loadGolden(t, "intent", &g)
	if len(g.Scenarios) != 16 {
		t.Fatalf("intent.json has %d scenarios, want 16", len(g.Scenarios))
	}
	return &g, loadCorpusRefs(t, "intent")
}

// ---- the replaying resolver --------------------------------------------------------------------

// replayResolver plays the recorded `resolve.calls` of a scenario, in order, one per request.
type replayResolver struct {
	h     *runHarness
	calls []resolveCall

	mu   sync.Mutex
	next int
}

var _ reviewsapp.IntentResolver = (*replayResolver)(nil)

func (r *replayResolver) used() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.next
}

func (r *replayResolver) Resolve(ctx context.Context, req reviewsapp.IntentRequest, emit reviewsapp.IntentEmit) reviewsapp.IntentResult {
	t := r.h.t
	r.mu.Lock()
	i := r.next
	r.next++
	r.mu.Unlock()
	if i >= len(r.calls) {
		t.Errorf("Resolve call #%d: the golden has %d call(s) (the hook ran when it should not have)", i, len(r.calls))
		return reviewsapp.IntentResult{Trace: domain.UnavailableIntentTrace("unexpected call")}
	}
	c := &r.calls[i]
	r.checkRequest(i, c, ctx, req)

	// The cancel route runs while the derivation works (Node: `intentCall:<n>`), before it reports.
	r.h.fire(context.WithoutCancel(ctx), fmt.Sprintf("intentCall:%d", i))
	for _, e := range c.Events {
		var data json.RawMessage
		if len(e.Data) > 0 && string(e.Data) != "null" {
			data = e.Data
		}
		emit(e.Kind, e.Msg, data)
	}
	if c.SignalAbortedAtReturn {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			t.Errorf("Resolve call #%d: the request context did not end although every run was cancelled", i)
		}
	} else if err := ctx.Err(); err != nil {
		t.Errorf("Resolve call #%d: the request context ended (%v) although a run is still waiting for the intent", i, err)
	}
	res, err := c.result()
	if err != nil {
		t.Errorf("Resolve call #%d: %v", i, err)
	}
	return res
}

// checkRequest compares the request the executor built with what crossed the port in Node.
func (r *replayResolver) checkRequest(i int, c *resolveCall, ctx context.Context, req reviewsapp.IntentRequest) {
	t := r.h.t
	t.Helper()
	in := c.Input
	want := reviewsapp.IntentRequest{
		WorkspaceID: in.WorkspaceID,
		Pull: domain.ReviewPull{
			ID: in.Pull.ID, WorkspaceID: in.Pull.WorkspaceID, RepoID: in.Pull.RepoID, Number: in.Pull.Number,
			Title: in.Pull.Title, Author: in.Pull.Author, Base: in.Pull.Base, Branch: in.Pull.Branch,
			HeadSHA: in.Pull.HeadSHA, Body: in.Pull.Body,
		},
		Repo:         domain.ReviewRepo{ID: in.Repo.ID, Owner: in.Repo.Owner, Name: in.Repo.Name},
		ChangedFiles: in.ChangedFiles,
	}
	got := req
	got.DiffExcerpt = ""
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Resolve call #%d: request mismatch (-node +go):\n%s", i, diff)
	}

	// The excerpt is the raw stored diff, rebuilt here from the scenario's pr_files.
	var files []reviewer.PatchFile
	for _, f := range r.h.sc.Input.PRFiles {
		files = append(files, reviewer.PatchFile{Path: f.Path, Patch: f.Patch.s})
	}
	rebuilt := reviewer.RawDiff(files)
	for what, excerpt := range map[string]string{"request": req.DiffExcerpt, "rebuilt from prFiles": rebuilt} {
		sum := sha256.Sum256([]byte(excerpt))
		if n, h := utf16Len(excerpt), hex.EncodeToString(sum[:]); n != in.DiffExcerptLen || h != in.DiffExcerptSHA256 {
			t.Errorf("Resolve call #%d: diff excerpt (%s) = %d units sha256 %s, want %d units sha256 %s",
				i, what, n, h, in.DiffExcerptLen, in.DiffExcerptSHA256)
		}
	}
	if in.Force {
		t.Errorf("Resolve call #%d: the golden request is forced (corrupt golden)", i)
	}
	if hasSignal := ctx.Done() != nil; hasSignal != in.HasSignal {
		t.Errorf("Resolve call #%d: request context cancellable = %v, want %v", i, hasSignal, in.HasSignal)
	}
}

// newIntentHarness builds the Phase 4 harness with the replaying resolver as Deps.Intent.
func newIntentHarness(t *testing.T, sc *intentScenario, refs map[string]string, agentConcurrency int) (*runHarness, *replayResolver) {
	t.Helper()
	var rr *replayResolver
	h := newRunHarness(t, &sc.runScenario, refs, harnessOptions{
		agentConcurrency: agentConcurrency,
		intent: func(h *runHarness) reviewsapp.IntentResolver {
			rr = &replayResolver{h: h, calls: sc.Resolve.Calls}
			return rr
		},
	})
	return h, rr
}

func checkResolveCalls(t *testing.T, sc *intentScenario, rr *replayResolver) {
	t.Helper()
	if got, want := rr.used(), len(sc.Resolve.Calls); got != want {
		t.Errorf("the executor called the IntentResolver %d time(s), want %d", got, want)
	}
}

// ---- the tests ---------------------------------------------------------------------------------

// TestIntentParity_Executor runs every scenario of intent.json through the real executor (agents one
// after another, as Node) with the replaying resolver and compares everything the scenario recorded.
func TestIntentParity_Executor(t *testing.T) {
	g, refs := loadIntent(t)
	for i := range g.Scenarios {
		sc := &g.Scenarios[i]
		t.Run(sc.Name, func(t *testing.T) {
			h, rr := newIntentHarness(t, sc, refs, 1)
			h.start()
			h.waitDone()
			compareRun(t, h, &sc.runScenario, true)
			checkResolveCalls(t, sc, rr)
		})
	}
}

// TestIntentParity_ExecutorAgentsInParallel runs the multi-agent scenarios at the executor's default
// concurrency: each run's events, row and trace, and the other lists as sets.
func TestIntentParity_ExecutorAgentsInParallel(t *testing.T) {
	g, refs := loadIntent(t)
	n := 0
	for i := range g.Scenarios {
		sc := &g.Scenarios[i]
		if len(sc.Input.Jobs) < 2 {
			continue
		}
		n++
		t.Run(sc.Name, func(t *testing.T) {
			h, rr := newIntentHarness(t, sc, refs, executor.DefaultAgentConcurrency)
			h.start()
			h.waitDone()
			compareRun(t, h, &sc.runScenario, false)
			checkResolveCalls(t, sc, rr)
		})
	}
	if n == 0 {
		t.Fatal("no multi-agent scenario in intent.json")
	}
}

// TestIntentParity_GoldenCoversTheSwitchOnContract guards the replay against a vacuous golden: the
// scenarios must contain what AC5030 and spec 7.5 name, so a regenerated golden that lost it fails
// here and not silently.
func TestIntentParity_GoldenCoversTheSwitchOnContract(t *testing.T) {
	g, _ := loadIntent(t)
	var scopeEvents, intentSections, ruleLogs, noHookScenarios, unavailable, oosStored int
	for i := range g.Scenarios {
		sc := &g.Scenarios[i]
		if len(sc.Resolve.Calls) == 0 {
			noHookScenarios++
		}
		for _, c := range sc.Resolve.Calls {
			if c.Result.Status == domain.IntentStatusUnavailable {
				unavailable++
			}
		}
		for _, evs := range sc.Events {
			for k, e := range evs {
				if !strings.HasPrefix(e.Msg, "scope: ") {
					continue
				}
				scopeEvents++
				if k == 0 || !strings.HasPrefix(evs[k-1].Msg, "Citation grounding: ") {
					t.Errorf("%s: scope event %q does not follow the grounding result", sc.Name, e.Msg)
				}
			}
		}
		for _, c := range sc.LLMCalls {
			for _, m := range c.Messages {
				if m.Role == "user" && strings.Contains(m.Content.s, "## PR intent\n") {
					intentSections++
				}
			}
		}
		for _, p := range sc.PromptLog {
			if strings.Contains(fmt.Sprint(p["sections"]), "intent_rule") {
				ruleLogs++
			}
		}
		for _, c := range sc.Calls {
			if c["port"] != "tx.commit" {
				continue
			}
			if strings.Contains(string(mustJSON(t, c)), `"outOfScope":true`) {
				oosStored++
			}
		}
	}
	for what, n := range map[string]int{
		"scope events": scopeEvents, "user messages with a PR intent section": intentSections,
		"prompt-log entries with intent_rule": ruleLogs, "scenarios without a hook call": noHookScenarios,
		"unavailable results": unavailable, "commits storing an out-of-scope finding": oosStored,
	} {
		if n == 0 {
			t.Errorf("intent.json has no %s", what)
		}
	}
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

// TestIntentParity_SwitchOff pins AC5032 at the harness level: the same scenarios without a resolver
// (Deps.Intent nil, REVIEW_INTENT_ENABLED=false) emit no intent event, send no "## PR intent" section,
// save a trace whose intent is null and store no out-of-scope flag, whatever the model answered.
func TestIntentParity_SwitchOff(t *testing.T) {
	g, refs := loadIntent(t)
	sc := &g.Scenarios[0] // intent_derived_single_pass_scope_events: the model flags findings out of scope
	h := newRunHarness(t, &sc.runScenario, refs, harnessOptions{agentConcurrency: 1})
	h.start()
	h.waitDone()

	for _, j := range sc.Input.Jobs {
		for _, e := range h.bus.Buffer(j.RunID) {
			if strings.Contains(e.Msg, "intent") || strings.HasPrefix(e.Msg, "scope: ") {
				t.Errorf("run %s: event %q with the switch off", j.RunID, e.Msg)
			}
		}
		doc, ok := h.goTrace(j.RunID).(map[string]any)
		if !ok {
			t.Fatalf("trace of %s is not an object", j.RunID)
		}
		if v, present := doc["intent"]; !present || v != nil {
			t.Errorf("run %s: trace.intent = %v (present %v), want null", j.RunID, v, present)
		}
	}
	for _, c := range h.gotLLMCalls() {
		for _, m := range c.Messages {
			if strings.Contains(m[1], "## PR intent") {
				t.Errorf("a request carries a PR intent section with the switch off: %q", m[1])
			}
		}
	}
	if b := mustJSON(t, h.gotCalls()); strings.Contains(string(b), `"outOfScope":true`) {
		t.Errorf("a finding was stored out of scope with the switch off")
	}
}
