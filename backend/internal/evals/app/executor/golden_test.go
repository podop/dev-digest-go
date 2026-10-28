package executor_test

// The executor against the Node goldens (internal/parity/testdata, scenarios `executor` of
// evals.json): the real review engine over a scripted LLM, the case store and the run store as
// fakes. It compares what the Node EvalRunExecutor did: the loads, the byte-exact prompts of
// every call, the recorded results, the final state and the log lines.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/app/executor"
	"dev-digest-go/backend/internal/evals/domain"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

var parityData = filepath.Join("..", "..", "..", "parity", "testdata")

func readJSON(t testing.TB, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parityData, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

type goldenJob struct {
	RunID        string  `json:"runId"`
	WorkspaceID  string  `json:"workspaceId"`
	OwnerKind    string  `json:"ownerKind"`
	SkillID      *string `json:"skillId"`
	SkillVersion *int    `json:"skillVersion"`
	AgentID      string  `json:"agentId"`
	AgentVersion int     `json:"agentVersion"`
	AgentName    string  `json:"agentName"`
	Config       struct {
		Provider     string `json:"provider"`
		Model        string `json:"model"`
		SystemPrompt string `json:"system_prompt"`
		Strategy     string `json:"strategy"`
		Skills       []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Body        string `json:"body"`
			Version     int    `json:"version"`
		} `json:"skills"`
	} `json:"config"`
	CaseIDs []string `json:"caseIds"`
}

type goldenCase struct {
	Expect string         `json:"expect"`
	Diff   string         `json:"diff"`
	Meta   map[string]any `json:"meta"`
}

// scriptEntry is one scripted model call of a case. Cost: absent = nothing reported, null =
// unpriced, a number = the cost of the response.
type scriptEntry struct {
	Cost        json.RawMessage  `json:"cost"`
	Findings    []map[string]any `json:"findings"`
	Error       *string          `json:"error"`
	CostOnError *float64         `json:"costOnError"`
	Hang        bool             `json:"hang"`
}

type goldenResult struct {
	Loads         []string         `json:"loads"`
	ResolverCalls []string         `json:"resolverCalls"`
	LLMRequests   []goldenRequest  `json:"llmRequests"`
	Records       []map[string]any `json:"records"`
	RecordAttempt []string         `json:"recordAttempts"`
	Dropped       int              `json:"dropped"`
	Finals        []map[string]any `json:"finals"`
	Shutdown      *bool            `json:"shutdown"`
	Logs          []goldenLog      `json:"logs"`
}

type goldenRequest struct {
	Case     string `json:"case"`
	Call     int    `json:"call"`
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type goldenLog struct {
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Obj   map[string]any `json:"obj"`
}

type goldenScenario struct {
	Name     string                   `json:"name"`
	Job      goldenJob                `json:"job"`
	Cases    map[string]*goldenCase   `json:"cases"`
	Script   map[string][]scriptEntry `json:"script"`
	Steps    []map[string]any         `json:"steps"`
	Result   goldenResult             `json:"result"`
	Variants []struct {
		Strategy string `json:"strategy"`
		goldenScenario
	} `json:"variants"`
}

// corpusScenario holds the failure switches the golden does not repeat.
type corpusScenario struct {
	Name       string            `json:"name"`
	RecordGone []string          `json:"recordGone"`
	RecordFail map[string]string `json:"recordFail"`
	LoadFail   map[string]string `json:"loadFail"`
	FinishFail string            `json:"finishFail"`
}

func TestGolden_Executor(t *testing.T) {
	var golden struct {
		Executor []goldenScenario `json:"executor"`
	}
	readJSON(t, "goldens/evals.json", &golden)
	var flows struct {
		Executor struct {
			Finding   map[string]any   `json:"finding"`
			Scenarios []corpusScenario `json:"scenarios"`
		} `json:"executor"`
	}
	readJSON(t, "corpus/evals/flows.json", &flows)
	var rawFlows struct {
		Executor struct {
			Expect map[string]struct {
				Kind      domain.ExpectationKind `json:"kind"`
				File      string                 `json:"file"`
				StartLine int                    `json:"start_line"`
				EndLine   int                    `json:"end_line"`
				Severity  *string                `json:"severity"`
				Category  *string                `json:"category"`
				Title     *string                `json:"title"`
			} `json:"expect"`
		} `json:"executor"`
	}
	readJSON(t, "corpus/evals/flows.json", &rawFlows)
	var content struct {
		Diffs map[string]json.RawMessage `json:"diffs"`
	}
	readJSON(t, "corpus/evals/content.json", &content)

	switches := map[string]corpusScenario{}
	for _, s := range flows.Executor.Scenarios {
		switches[s.Name] = s
	}
	expect := map[string]domain.Expectation{}
	for k, e := range rawFlows.Executor.Expect {
		expect[k] = domain.Expectation{Kind: e.Kind, File: e.File, StartLine: e.StartLine, EndLine: e.EndLine, Severity: e.Severity, Category: e.Category, Title: e.Title}
	}
	env := goldenEnv{expect: expect, finding: flows.Executor.Finding, diffs: content.Diffs}

	if len(golden.Executor) != 16 {
		t.Fatalf("the golden has %d executor scenarios, want 16", len(golden.Executor))
	}
	for _, sc := range golden.Executor {
		if len(sc.Variants) > 0 {
			for _, v := range sc.Variants {
				t.Run(fmt.Sprintf("%s/strategy=%q", sc.Name, v.Strategy), func(t *testing.T) {
					env.run(t, v.goldenScenario, switches[sc.Name])
				})
			}
			continue
		}
		t.Run(sc.Name, func(t *testing.T) { env.run(t, sc, switches[sc.Name]) })
	}
}

type goldenEnv struct {
	expect  map[string]domain.Expectation
	finding map[string]any
	diffs   map[string]json.RawMessage
}

// request is one LLM call as the fake saw it.
type request struct {
	caseID   string
	call     int
	model    string
	messages []reviewer.Message
}

func (g goldenEnv) run(t *testing.T, sc goldenScenario, sw corpusScenario) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore()
		for id, c := range sc.Cases {
			if c != nil {
				store.cases[id] = g.buildCase(t, id, *c)
			}
		}
		store.recordGone = map[string]bool{}
		for _, id := range sw.RecordGone {
			store.recordGone[id] = true
		}
		store.recordFail, store.loadFail, store.finishFail = sw.RecordFail, sw.LoadFail, sw.FinishFail

		var mu sync.Mutex
		var requests []request
		llm := func(ctx context.Context, b executor.LLMBinding, c reviewerapp.Call) (reviewerapp.CallResult, error) {
			id := caseOf(c)
			mu.Lock()
			requests = append(requests, request{caseID: id, call: c.Chunk.Index, model: b.Model, messages: c.Messages})
			mu.Unlock()
			entries := sc.Script[id]
			if len(entries) == 0 {
				return reviewerapp.CallResult{}, fmt.Errorf("no script for %s", id)
			}
			e := entries[min(c.Chunk.Index, len(entries)-1)]
			report := func(cost json.RawMessage) {
				var v *float64
				if len(cost) > 0 && string(cost) != "null" {
					v = new(float64)
					if err := json.Unmarshal(cost, v); err != nil {
						t.Errorf("script cost %s: %v", cost, err)
					}
				}
				if len(cost) > 0 {
					b.OnUsage(reviewer.Usage{TokensIn: 100, TokensOut: 50, CostUSD: v})
				}
			}
			// Node finishes the cases in start order; stagger them so the workers do too.
			if !e.Hang {
				time.Sleep(time.Duration(slices.Index(sc.Job.CaseIDs, id)) * time.Millisecond)
			}
			switch {
			case e.Error != nil:
				if e.CostOnError != nil {
					b.OnUsage(reviewer.Usage{TokensIn: 100, TokensOut: 10, CostUSD: e.CostOnError})
				}
				return reviewerapp.CallResult{}, fmt.Errorf("%s", *e.Error)
			case e.Hang:
				report(e.Cost)
				<-ctx.Done()
				return reviewerapp.CallResult{}, ctx.Err()
			}
			report(e.Cost)
			return reviewerapp.CallResult{Review: reviewer.Review{
				Verdict: reviewer.VerdictComment, Summary: "ok", Score: 80, Findings: g.findings(t, e.Findings),
			}}, nil
		}

		fixed := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
		// The Node scenarios time cases against Node's 120 s case budget; Go's domain.CaseBudget is
		// larger since 2026-10-28, so the replay runs with Node's.
		h := newHarness(t, store, llm, executor.Options{CaseBudget: 120 * time.Second}, func() time.Time { return fixed })
		run := g.buildRun(sc.Job)

		shutdown := (*bool)(nil)
		h.exec.Launch(t.Context(), run)
		steps := sc.Steps
		if steps == nil {
			steps = []map[string]any{{"waitFinish": true}}
		}
		for _, step := range steps {
			if step["settle"] != nil || step["waitFinish"] != nil {
				time.Sleep(10 * time.Second) // the staggered cases of the scenario run to their end
				synctest.Wait()
			}
			if ms, ok := step["fire"].(float64); ok {
				time.Sleep(time.Duration(ms) * time.Millisecond)
				synctest.Wait()
			}
			if _, ok := step["shutdown"]; ok {
				err := h.exec.Shutdown(t.Context())
				drained := err == nil
				shutdown = &drained
			}
			if step["waitFinish"] != nil {
				time.Sleep(10 * time.Second)
				synctest.Wait()
			}
		}
		if shutdown == nil {
			if err := h.exec.Shutdown(t.Context()); err != nil {
				t.Errorf("cleanup Shutdown() = %v, want nil", err)
			}
		}

		g.compare(t, sc, h, store, requests, shutdown)
	})
}

func (g goldenEnv) compare(t *testing.T, sc goldenScenario, h *harness, store *fakeStore, requests []request, shutdown *bool) {
	t.Helper()
	order := sc.Job.CaseIDs
	byCase := func(id string) int { return slices.Index(order, id) }

	store.mu.Lock()
	defer store.mu.Unlock()
	h.prov.mu.Lock()
	defer h.prov.mu.Unlock()
	want := sc.Result

	loads := slices.Clone(store.loads)
	slices.SortFunc(loads, func(a, b string) int { return byCase(a) - byCase(b) })
	diff(t, "loads", nilSlice(want.Loads), nilSlice(loads))
	diff(t, "resolverCalls", nilSlice(want.ResolverCalls), nilSlice(h.prov.resolved))
	attempts := slices.Clone(store.attempts)
	slices.SortFunc(attempts, func(a, b string) int { return byCase(a) - byCase(b) })
	diff(t, "recordAttempts", nilSlice(want.RecordAttempt), nilSlice(attempts))
	if store.dropped != want.Dropped {
		t.Errorf("dropped cases = %d, want %d", store.dropped, want.Dropped)
	}

	// The prompts, byte for byte.
	slices.SortFunc(requests, func(a, b request) int {
		if d := byCase(a.caseID) - byCase(b.caseID); d != 0 {
			return d
		}
		return a.call - b.call
	})
	var gotReq, wantReq []string
	for _, r := range requests {
		var parts []string
		for _, m := range r.messages {
			parts = append(parts, string(m.Role)+": "+m.Content)
		}
		gotReq = append(gotReq, fmt.Sprintf("%s#%d %s\n%s", r.caseID, r.call, r.model, strings.Join(parts, "\n---\n")))
	}
	for _, r := range want.LLMRequests {
		var parts []string
		for _, m := range r.Messages {
			parts = append(parts, m.Role+": "+m.Content)
		}
		wantReq = append(wantReq, fmt.Sprintf("%s#%d %s\n%s", r.Case, r.Call, r.Model, strings.Join(parts, "\n---\n")))
	}
	diff(t, "llmRequests", nilSlice(wantReq), nilSlice(gotReq))

	records := slices.Clone(store.records)
	slices.SortFunc(records, func(a, b domain.NewResult) int { return byCase(a.CaseID) - byCase(b.CaseID) })
	var gotRecords []map[string]any
	for _, r := range records {
		gotRecords = append(gotRecords, recordJSON(r))
	}
	diff(t, "records", normalize(t, nilSlice(want.Records)), normalize(t, nilSlice(gotRecords)))

	var gotFinals []map[string]any
	for _, f := range store.finals {
		gotFinals = append(gotFinals, finalJSON(f))
	}
	diff(t, "finals", normalize(t, nilSlice(want.Finals)), normalize(t, nilSlice(gotFinals)))

	if (want.Shutdown == nil) != (shutdown == nil) || (shutdown != nil && *shutdown != *want.Shutdown) {
		t.Errorf("shutdown drained = %v, want %v", ptrText(shutdown), ptrText(want.Shutdown))
	}

	var gotLogs, wantLogs []goldenLog
	for _, l := range h.logs.lines(t) {
		gotLogs = append(gotLogs, goldenLog{Level: l.Level, Msg: l.Msg, Obj: l.Obj})
	}
	for _, l := range want.Logs {
		wantLogs = append(wantLogs, l)
	}
	diff(t, "logs", normalize(t, wantLogs), normalize(t, gotLogs))
}

func ptrText(b *bool) string {
	if b == nil {
		return "unset"
	}
	return fmt.Sprint(*b)
}

func nilSlice[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}

// normalize round-trips v through JSON so numbers and maps compare as the golden decoded them.
func normalize(t testing.TB, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func diff(t testing.TB, what string, want, got any) {
	t.Helper()
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", what, d)
	}
}

// recordJSON is a recorded result in the shape of Node's CaseRecord.
func recordJSON(r domain.NewResult) map[string]any {
	findings := make([]map[string]any, 0, len(r.Findings))
	for _, f := range r.Findings {
		findings = append(findings, map[string]any{
			"file": f.File, "start_line": f.StartLine, "end_line": f.EndLine,
			"severity": ptrVal(f.Severity), "category": ptrVal(f.Category), "title": ptrVal(f.Title), "match": string(f.Match),
		})
	}
	return map[string]any{
		"caseId": r.CaseID, "status": string(r.Status), "findings": findings, "droppedCount": r.DroppedCount,
		"hitCount": r.HitCount, "violationCount": r.ViolationCount, "unlabeledCount": r.UnlabeledCount,
		"durationMs": r.DurationMS, "costUsd": ptrVal(r.CostUSD), "error": ptrVal(r.Error),
	}
}

// finalJSON is a final state in the shape of Node's RunFinal.
func finalJSON(f domain.RunFinish) map[string]any {
	return map[string]any{
		"status": string(f.Status), "error": ptrVal(f.Error), "durationMs": f.DurationMS,
		"metrics": map[string]any{
			"recall": ptrVal(f.Metrics.Recall), "precision": ptrVal(f.Metrics.Precision),
			"citation_accuracy": ptrVal(f.Metrics.CitationAccuracy), "cases_passed": f.CasesPassed,
			"cases_errored": f.CasesErrored, "unlabeled_findings": f.Metrics.UnlabeledFindings,
			"cost_usd": ptrVal(f.Metrics.CostUSD),
		},
	}
}

func ptrVal[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func (g goldenEnv) buildRun(j goldenJob) domain.Run {
	run := domain.Run{
		ID: j.RunID, WorkspaceID: j.WorkspaceID, OwnerKind: domain.OwnerKind(j.OwnerKind), SkillID: j.SkillID,
		SkillVersion: j.SkillVersion, AgentID: j.AgentID, AgentVersion: j.AgentVersion, AgentName: j.AgentName,
		CaseIDs: j.CaseIDs, Status: domain.RunRunning, CasesTotal: len(j.CaseIDs),
		Config: domain.Config{Provider: j.Config.Provider, Model: j.Config.Model, SystemPrompt: j.Config.SystemPrompt, Strategy: j.Config.Strategy},
	}
	for _, s := range j.Config.Skills {
		run.Config.Skills = append(run.Config.Skills, domain.SkillSnapshot{ID: s.ID, Name: s.Name, Description: s.Description, Body: s.Body, Version: s.Version})
	}
	return run
}

func (g goldenEnv) buildCase(t testing.TB, id string, c goldenCase) domain.Case {
	t.Helper()
	exp, ok := g.expect[c.Expect]
	if !ok {
		t.Fatalf("case %s: unknown expectation %q", id, c.Expect)
	}
	raw, ok := g.diffs[c.Diff]
	if !ok {
		t.Fatalf("case %s: unknown diff %q", id, c.Diff)
	}
	var spec textSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("case %s: diff %q: %v", id, c.Diff, err)
	}
	meta := domain.InputMeta{PRTitle: "Case " + id}
	for k, v := range c.Meta {
		switch k {
		case "pr_description":
			if s, ok := v.(string); ok {
				meta.PRDescription = &s
			}
		case "pr_number":
			if f, ok := v.(float64); ok {
				n := int(f)
				meta.PRNumber = &n
			}
		case "repo":
			s := v.(string)
			meta.Repo = &s
		case "head_sha":
			s := v.(string)
			meta.HeadSHA = &s
		default:
			t.Fatalf("case %s: unknown meta key %q", id, k)
		}
	}
	return domain.Case{
		ID: id, WorkspaceID: "ws-1", OwnerKind: domain.OwnerAgent, OwnerID: "agent-1", Name: id,
		Expectation: exp, InputDiff: spec.text(), InputMeta: meta,
	}
}

// findings builds the model's findings: the corpus finding with the scripted overrides.
func (g goldenEnv) findings(t testing.TB, overrides []map[string]any) []reviewer.Finding {
	t.Helper()
	out := make([]reviewer.Finding, 0, len(overrides))
	for i, o := range overrides {
		m := map[string]any{}
		for k, v := range g.finding {
			m[k] = v
		}
		m["id"] = fmt.Sprintf("f%d", i+1)
		for k, v := range o {
			m[k] = v
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			ID         string  `json:"id"`
			Severity   string  `json:"severity"`
			Category   string  `json:"category"`
			Title      string  `json:"title"`
			File       string  `json:"file"`
			StartLine  int     `json:"start_line"`
			EndLine    int     `json:"end_line"`
			Rationale  string  `json:"rationale"`
			Suggestion *string `json:"suggestion"`
			Confidence float64 `json:"confidence"`
			Kind       string  `json:"kind"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("finding %s: %v", b, err)
		}
		out = append(out, reviewer.Finding{
			ID: f.ID, Severity: reviewer.Severity(f.Severity), Category: reviewer.Category(f.Category), Title: f.Title,
			File: f.File, StartLine: f.StartLine, EndLine: f.EndLine, Rationale: f.Rationale, Suggestion: f.Suggestion,
			Confidence: f.Confidence, Kind: reviewer.Kind(f.Kind),
		})
	}
	return out
}

// textSpec is a corpus diff: lines, literal text or a synthetic diff (gen/lib.mts textOf).
type textSpec struct {
	Lines       []string `json:"lines"`
	EOL         *string  `json:"eol"`
	TrailingEOL bool     `json:"trailingEol"`
	Text        *string  `json:"text"`
	Synth       *struct {
		Files []struct {
			Path  string      `json:"path"`
			Hunks []synthHunk `json:"hunks"`
		} `json:"files"`
	} `json:"synth"`
}

type synthHunk struct {
	Start int     `json:"start"`
	Add   int     `json:"add"`
	Del   int     `json:"del"`
	Ctx   int     `json:"ctx"`
	Text  *string `json:"text"`
}

func (s textSpec) text() string {
	switch {
	case s.Synth != nil:
		var lines []string
		for _, f := range s.Synth.Files {
			lines = append(lines, "diff --git a/"+f.Path+" b/"+f.Path, "--- a/"+f.Path, "+++ b/"+f.Path)
			for _, h := range f.Hunks {
				text := "const value = compute();"
				if h.Text != nil {
					text = *h.Text
				}
				lines = append(lines, fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.Start, h.Ctx+h.Del, h.Start, h.Ctx+h.Add))
				for i := 1; i <= h.Ctx; i++ {
					lines = append(lines, fmt.Sprintf(" %s ctx%d", text, i))
				}
				for i := 1; i <= h.Del; i++ {
					lines = append(lines, fmt.Sprintf("-%s old%d", text, i))
				}
				for i := 1; i <= h.Add; i++ {
					lines = append(lines, fmt.Sprintf("+%s new%d", text, i))
				}
			}
		}
		return strings.Join(lines, "\n")
	case s.Text != nil:
		return *s.Text
	}
	eol := "\n"
	if s.EOL != nil {
		eol = *s.EOL
	}
	body := strings.Join(s.Lines, eol)
	if s.TrailingEOL {
		body += eol
	}
	return body
}
