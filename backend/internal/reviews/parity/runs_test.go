package parity_test

// S25 (AC4041, NFR4005): the review run executor against runs.json (the Node ReviewRunExecutor over
// fake ports, 23 scenarios: run events, port calls and recorded statements, final run rows, saved
// traces, provider requests, prompt-log lines) and sse.json (the raw GET /runs/:id/events bodies of
// the same runs, live and replayed). The harness is in runs_fakes_test.go.
//
// What is compared, and what is not (README, "runs.json scenarios"):
//   - events: seq, kind, message (with "(<n>ms)" normalised) and data (byte for byte as compact JSON,
//     key order included); `t` is ignored;
//   - calls: the port calls in order with the recorded statements, durations ignored. `git.diff` is
//     dropped from the golden: Node always failed it (no clone) and read pr_files, as Go does (D4003,
//     the executor has no git port at all);
//   - finalRuns, traces (minus stats.duration_ms, tool_calls[].ms, log[].t), llmCalls, promptLog and
//     the HTTP bodies the provider received.
// Agents run one after another in Node; the ordered comparison therefore uses AgentConcurrency 1, and
// a second pass runs the multi-agent scenarios at the default concurrency and compares per run.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/llm"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	reviewspg "dev-digest-go/backend/internal/reviews/adapters/postgres"
	"dev-digest-go/backend/internal/reviews/app/executor"
	"dev-digest-go/backend/internal/reviews/domain"
)

// ---- normalisation -------------------------------------------------------------------------------

// jsonNorm turns v into the generic value its JSON encoding decodes to (numbers float64, structs maps).
func jsonNorm(t testing.TB, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return out
}

// scrubKeys deletes the named keys at every depth.
func scrubKeys(v any, keys ...string) any {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range keys {
			delete(x, k)
		}
		for _, c := range x {
			scrubKeys(c, keys...)
		}
	case []any:
		for _, c := range x {
			scrubKeys(c, keys...)
		}
	}
	return v
}

func compactJSON(t testing.TB, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buf.String()
}

type eventView struct {
	Seq  int
	Kind string
	Msg  string
	Data string
}

func goldenEventViews(t testing.TB, in []runEvent) []eventView {
	out := make([]eventView, len(in))
	for i, e := range in {
		out[i] = eventView{Seq: e.Seq, Kind: e.Kind, Msg: normMS(e.Msg), Data: compactJSON(t, e.Data)}
	}
	return out
}

func (h *runHarness) eventViews(runID string) []eventView {
	events := h.bus.Buffer(runID)
	out := make([]eventView, len(events))
	for i, e := range events {
		out[i] = eventView{Seq: e.Seq, Kind: string(e.Kind), Msg: normMS(e.Msg), Data: compactJSON(h.t, e.Data)}
	}
	return out
}

// scrubTrace drops what the logical clock decides from an encoded trace document.
func scrubTrace(t testing.TB, v any) any {
	t.Helper()
	doc, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("trace document is %T, want an object", v)
	}
	if stats, ok := doc["stats"].(map[string]any); ok {
		delete(stats, "duration_ms")
	}
	for _, c := range asSlice(doc["tool_calls"]) {
		if m, ok := c.(map[string]any); ok {
			delete(m, "ms")
		}
	}
	for _, l := range asSlice(doc["log"]) {
		if m, ok := l.(map[string]any); ok {
			delete(m, "t")
			if s, ok := m["msg"].(string); ok {
				m["msg"] = normMS(s)
			}
		}
	}
	return doc
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// goTrace is the trace the run saved, as the document the store would write.
func (h *runHarness) goTrace(runID string) any {
	h.t.Helper()
	tr, ok := h.store.trace(runID)
	if !ok {
		h.t.Fatalf("run %s saved no trace", runID)
	}
	doc, err := reviewspg.EncodeTrace(tr)
	if err != nil {
		h.t.Fatalf("EncodeTrace(%s) = %v", runID, err)
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		h.t.Fatalf("trace document of %s: %v", runID, err)
	}
	return scrubTrace(h.t, v)
}

// wantCalls is the golden call list as Go makes it: no git.diff, no timings, no Node error texts.
func wantCalls(in []map[string]any) []any {
	var out []any
	for _, c := range in {
		if c["port"] == "git.diff" {
			continue
		}
		if c["port"] == "tx.rollback" {
			delete(c, "error")
		}
		out = append(out, scrubKeys(c, "durationMs"))
	}
	return out
}

func (h *runHarness) gotCalls() []any {
	var out []any
	for _, c := range h.recordedCalls() {
		out = append(out, scrubKeys(jsonNorm(h.t, c), "durationMs"))
	}
	return out
}

// llmView is a structured request in a form both sides can build.
type llmView struct {
	Chunk       int
	Schema      string
	Model       string
	Temperature *float64
	Reprompts   int
	Session     string
	Messages    [][2]string
}

func wantLLMCalls(t testing.TB, in []runLLMCall) []llmView {
	out := make([]llmView, len(in))
	for i, c := range in {
		v := llmView{Chunk: c.ChunkIndex, Schema: c.SchemaName, Model: c.Model, Temperature: c.Temperature, Reprompts: c.MaxRetries}
		if c.SessionID != nil {
			v.Session = *c.SessionID
		}
		for _, m := range c.Messages {
			if m.Content.b != nil {
				t.Fatalf("llm call %d: a blob message needs a synth spec", i)
			}
			v.Messages = append(v.Messages, [2]string{m.Role, m.Content.s})
		}
		out[i] = v
	}
	return out
}

func (h *runHarness) gotLLMCalls() []llmView {
	var out []llmView
	for _, c := range h.client.recordedCalls() {
		v := llmView{Chunk: c.Chunk, Schema: c.SchemaName, Model: c.Model, Temperature: c.Temperature, Reprompts: c.Reprompts, Session: c.SessionID}
		for _, m := range c.Messages {
			v.Messages = append(v.Messages, [2]string{string(m.Role), m.Content})
		}
		out = append(out, v)
	}
	return out
}

// gotPromptLog is the prompt_assembled lines of the real client, reduced to the fields the Node
// prompt log carries.
func (h *runHarness) gotPromptLog() []any {
	var out []any
	for _, line := range h.clientLogs.lines() {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			h.t.Fatalf("client log line %q: %v", line, err)
		}
		if rec["evt"] != "prompt_assembled" {
			continue
		}
		entry := map[string]any{}
		for _, k := range []string{"feature", "correlationId", "provider", "model", "chunk", "sections"} {
			if v, ok := rec[k]; ok {
				entry[k] = v
			}
		}
		out = append(out, entry)
	}
	return out
}

func asJSONStrings(v []any) []string {
	out := make([]string, len(v))
	for i, x := range v {
		b, _ := json.Marshal(x)
		out[i] = string(b)
	}
	return out
}

func sortedJSON(v []any) []string {
	out := asJSONStrings(v)
	slices.Sort(out)
	return out
}

// ---- the executor comparison -------------------------------------------------------------------

// compareRun diffs everything a scenario recorded. ordered=false compares the lists that interleave
// across concurrently running agents as multisets.
func compareRun(t *testing.T, h *runHarness, sc *runScenario, ordered bool) {
	t.Helper()
	for _, j := range sc.Input.Jobs {
		id := j.RunID
		if diff := cmp.Diff(goldenEventViews(t, sc.Events[id]), h.eventViews(id)); diff != "" {
			t.Errorf("run %s: events mismatch (-node +go):\n%s", id, diff)
		}
		if !h.busCompleted(id, time.Second) {
			t.Errorf("run %s: the bus is not completed", id)
		}
		if !sc.BusComplete[id] {
			t.Errorf("run %s: the golden bus is not completed (corrupt golden)", id)
		}
		if diff := cmp.Diff(scrubKeys(sc.FinalRuns[id], "durationMs"), scrubKeys(jsonNorm(t, h.store.finalRun(id)), "durationMs")); diff != "" {
			t.Errorf("run %s: final run row mismatch (-node +go):\n%s", id, diff)
		}
		if diff := cmp.Diff(scrubTrace(t, sc.Traces[id]), h.goTrace(id)); diff != "" {
			t.Errorf("run %s: trace mismatch (-node +go):\n%s", id, diff)
		}
	}

	wantC, gotC := wantCalls(sc.Calls), h.gotCalls()
	wantL, gotL := wantLLMCalls(t, sc.LLMCalls), h.gotLLMCalls()
	var wantP []any
	for _, p := range sc.PromptLog {
		// D4009: the Node prompt log's `verbose` field (the chunk's file label, PROMPT_LOG_VERBOSE) is
		// not ported; the Go line stays content-free.
		delete(p, "verbose")
		wantP = append(wantP, p)
	}
	gotP := h.gotPromptLog()
	if ordered {
		if diff := cmp.Diff(wantC, gotC); diff != "" {
			t.Errorf("port calls mismatch (-node +go):\n%s", diff)
		}
		if diff := cmp.Diff(wantL, gotL, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("provider requests (llmCalls) mismatch (-node +go):\n%s", diff)
		}
		if diff := cmp.Diff(wantP, gotP); diff != "" {
			t.Errorf("prompt log mismatch (-node +go):\n%s", diff)
		}
	} else {
		if diff := cmp.Diff(sortedJSON(wantC), sortedJSON(gotC)); diff != "" {
			t.Errorf("port calls (as a set) mismatch (-node +go):\n%s", diff)
		}
		wantLS, gotLS := make([]any, len(wantL)), make([]any, len(gotL))
		for i, v := range wantL {
			wantLS[i] = v
		}
		for i, v := range gotL {
			gotLS[i] = v
		}
		if diff := cmp.Diff(sortedJSON(wantLS), sortedJSON(gotLS)); diff != "" {
			t.Errorf("provider requests (as a set) mismatch (-node +go):\n%s", diff)
		}
		if diff := cmp.Diff(sortedJSON(wantP), sortedJSON(gotP)); diff != "" {
			t.Errorf("prompt log (as a set) mismatch (-node +go):\n%s", diff)
		}
	}

	if sc.Input.LLM.Mode == "http" {
		compareHTTPRequests(t, h, sc)
	}
}

// compareHTTPRequests checks the bodies the provider received (request path, key, body with the strict
// Review response_format abbreviated as the golden shows it).
func compareHTTPRequests(t *testing.T, h *runHarness, sc *runScenario) {
	t.Helper()
	raw, err := llm.StrictSchema(reviewer.ReviewSchemaJSON)
	if err != nil {
		t.Fatalf("StrictSchema: %v", err)
	}
	var strict any
	if err := json.Unmarshal(raw, &strict); err != nil {
		t.Fatalf("strict schema: %v", err)
	}
	norm := func(reqs []map[string]any) []any {
		out := make([]any, len(reqs))
		for i, r := range reqs {
			c := map[string]any{}
			for k, v := range r {
				c[k] = v
			}
			if body, ok := r["body"].(map[string]any); ok {
				c["body"] = normBody(t, body, strict)
			}
			out[i] = jsonNorm(t, c)
		}
		return out
	}
	if diff := cmp.Diff(norm(sc.LLMHTTPRequests), norm(h.client.fake.recorded())); diff != "" {
		t.Errorf("provider HTTP requests mismatch (-node +go):\n%s", diff)
	}
}

func loadRuns(t testing.TB) (*runsGolden, map[string]string) {
	t.Helper()
	var g runsGolden
	loadGolden(t, "runs", &g)
	if len(g.Scenarios) != 23 {
		t.Fatalf("runs.json has %d scenarios, want 23", len(g.Scenarios))
	}
	return &g, loadRunRefs(t)
}

// TestRunParity_Executor runs every scenario of runs.json through the real executor, engine, bus and
// LLM path (agents one after another, as Node) and compares all that the scenario recorded.
func TestRunParity_Executor(t *testing.T) {
	g, refs := loadRuns(t)
	for i := range g.Scenarios {
		sc := &g.Scenarios[i]
		t.Run(sc.Name, func(t *testing.T) {
			h := newRunHarness(t, sc, refs, harnessOptions{agentConcurrency: 1})
			h.start()
			h.waitDone()
			compareRun(t, h, sc, true)
		})
	}
}

// TestRunParity_ExecutorAgentsInParallel runs the scenarios with several agents at the executor's default
// concurrency. The interleaving of the agents is not parity data, so what is compared is each run's
// events, row and trace, and the other lists as sets.
func TestRunParity_ExecutorAgentsInParallel(t *testing.T) {
	g, refs := loadRuns(t)
	n := 0
	for i := range g.Scenarios {
		sc := &g.Scenarios[i]
		if len(sc.Input.Jobs) < 2 {
			continue
		}
		n++
		t.Run(sc.Name, func(t *testing.T) {
			h := newRunHarness(t, sc, refs, harnessOptions{agentConcurrency: executor.DefaultAgentConcurrency})
			h.start()
			h.waitDone()
			compareRun(t, h, sc, false)
		})
	}
	if n == 0 {
		t.Fatal("no multi-agent scenario in runs.json")
	}
}

// TestRunParity_ErrorEventsAreCutAtTheFirstNewlineInLogsOnly pins D4017: a schema-failure note carries a
// snippet of the model's output after its first line; the Live Log event keeps the whole note (as Node),
// the server log keeps its first line only.
func TestRunParity_ErrorEventsAreCutAtTheFirstNewlineInLogsOnly(t *testing.T) {
	g, refs := loadRuns(t)
	var sc *runScenario
	for i := range g.Scenarios {
		if g.Scenarios[i].Name == "every_attempt_fails_the_schema_over_http" {
			sc = &g.Scenarios[i]
		}
	}
	if sc == nil {
		t.Fatal("scenario every_attempt_fails_the_schema_over_http not found")
	}
	h := newRunHarness(t, sc, refs, harnessOptions{agentConcurrency: 1})
	h.start()
	h.waitDone()

	const snippet = "Last raw output"
	runID := sc.Input.Jobs[0].RunID
	events := h.bus.Buffer(runID)
	last := events[len(events)-1]
	if last.Kind != reviewer.EventError || !strings.Contains(last.Msg, "\n") || !strings.Contains(last.Msg, snippet) {
		t.Fatalf("last event = %s %q, want the whole multi-line failure note", last.Kind, last.Msg)
	}
	firstLine, _, _ := strings.Cut(last.Msg, "\n")

	logged := false
	for _, line := range h.execLogs.lines() {
		if bytes.Contains(line, []byte(snippet)) {
			t.Errorf("an executor log line carries more than the first line of the note: %s", line)
		}
		if bytes.Contains(line, []byte(jsonQuoted(t, firstLine))) {
			logged = true
		}
	}
	if !logged {
		t.Errorf("no executor log line carries the first line %q of the failure note", firstLine)
	}
}

func jsonQuoted(t testing.TB, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	// The value without its quotes: a log line holds it inside a longer string or as the whole value.
	return string(b[1 : len(b)-1])
}

// ---- SSE bodies ---------------------------------------------------------------------------------

var sseStamp = regexp.MustCompile(`"t":"\d\d:\d\d:\d\d"`)

// normSSE blanks the clock stamps and the elapsed times: they come from a logical clock.
func normSSE(s string) string { return normMS(sseStamp.ReplaceAllString(s, `"t":"T"`)) }

type sseStream struct {
	Scenario string `json:"scenario"`
	RunID    string `json:"runId"`
	Mode     string `json:"mode"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"response"`
}

// TestRunParity_SSE serves GET /runs/:id/events of the real service and routes over the real bus and
// compares the raw body of the runs of runs.json with the Node recording: replayed after the run ended,
// and followed live by a client that connected before the run started.
func TestRunParity_SSE(t *testing.T) {
	g, refs := loadRuns(t)
	byName := map[string]*runScenario{}
	for i := range g.Scenarios {
		byName[g.Scenarios[i].Name] = &g.Scenarios[i]
	}
	var sse struct {
		Streams []sseStream `json:"streams"`
	}
	loadGolden(t, "sse", &sse)
	if len(sse.Streams) != 11 {
		t.Fatalf("sse.json has %d scenario streams, want 11", len(sse.Streams))
	}
	live := 0
	for _, st := range sse.Streams {
		sc := byName[st.Scenario]
		if sc == nil {
			t.Fatalf("stream of unknown scenario %s", st.Scenario)
		}
		t.Run(st.Scenario+"/"+st.Mode, func(t *testing.T) {
			h := newRunHarness(t, sc, refs, harnessOptions{agentConcurrency: 1})
			url := h.server().URL + "/runs/" + st.RunID + "/events"
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			var resp *http.Response
			switch st.Mode {
			case "live":
				// The response headers are flushed once the stream is subscribed: the client is
				// connected before the first event exists.
				resp = getSSE(t, ctx, h, url)
				h.start()
			case "replay":
				h.start()
				h.waitDone()
				resp = getSSE(t, ctx, h, url)
			default:
				t.Fatalf("unknown stream mode %q", st.Mode)
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Errorf("close body: %v", err)
				}
			}()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read the stream: %v", err)
			}
			if st.Mode == "live" {
				h.waitDone()
			}

			if resp.StatusCode != st.Response.Status {
				t.Errorf("status = %d, want %d", resp.StatusCode, st.Response.Status)
			}
			for _, k := range []string{"content-type", "connection", "cache-control", "x-no-compression"} {
				if got, want := resp.Header.Get(k), st.Response.Headers[k]; got != want {
					t.Errorf("header %s = %q, want %q", k, got, want)
				}
			}
			if diff := cmp.Diff(normSSE(st.Response.Body), normSSE(string(body))); diff != "" {
				t.Errorf("%s body mismatch (-node +go):\n%s", st.Mode, diff)
			}
		})
		if st.Mode == "live" {
			live++
		}
	}
	if live == 0 {
		t.Fatal("sse.json has no live stream")
	}
}

func getSSE(t *testing.T, ctx context.Context, h *runHarness, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := h.server().Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// ---- the store double follows the hand-written transition table ----------------------------------

// TestRunParity_StoreDoubleFollowsTheStatusTransitions checks the fake store that produces `finalRuns`
// against handwritten/run_status_transitions.json: the executor scenarios are only as good as that
// double (the real SQL is tested against a database in reviews/adapters/postgres).
func TestRunParity_StoreDoubleFollowsTheStatusTransitions(t *testing.T) {
	var table struct {
		FailAgentRun struct {
			Cases []struct {
				From struct {
					Status string  `json:"status"`
					Error  *string `json:"error"`
				} `json:"from"`
				Call struct {
					Status string  `json:"status"`
					Error  *string `json:"error"`
				} `json:"call"`
				After struct {
					Status       string  `json:"status"`
					Error        *string `json:"error"`
					StatsWritten bool    `json:"statsWritten"`
				} `json:"after"`
			} `json:"cases"`
		} `json:"failAgentRun"`
		CompleteAgentRunIfRunning struct {
			Cases []struct {
				From    string `json:"from"`
				Returns bool   `json:"returns"`
				After   string `json:"after"`
			} `json:"cases"`
		} `json:"completeAgentRunIfRunning"`
		CancelRunIfRunning struct {
			Cases []struct {
				From    string `json:"from"`
				Returns bool   `json:"returns"`
				After   struct {
					Status string `json:"status"`
				} `json:"after"`
			} `json:"cases"`
		} `json:"cancelRunIfRunning"`
	}
	loadHandwritten(t, "run_status_transitions", &table)
	if len(table.FailAgentRun.Cases) == 0 || len(table.CompleteAgentRunIfRunning.Cases) == 0 || len(table.CancelRunIfRunning.Cases) == 0 {
		t.Fatal("run_status_transitions.json has an empty case list")
	}
	g, refs := loadRuns(t)
	sc := &g.Scenarios[0]
	newStore := func(t *testing.T, status string, errText *string) (*fakeRunStore, string) {
		h := newRunHarness(t, sc, refs, harnessOptions{agentConcurrency: 1})
		id := sc.Input.Jobs[0].RunID
		h.store.runs[id].status, h.store.runs[id].err = status, errText
		return h.store, id
	}

	for i, c := range table.FailAgentRun.Cases {
		t.Run(fmt.Sprintf("failAgentRun/%d_%s_to_%s", i, c.From.Status, c.Call.Status), func(t *testing.T) {
			s, id := newStore(t, c.From.Status, c.From.Error)
			if err := s.EndRun(t.Context(), id, domainStatus(c.Call.Status), runCompletionWithTokens(7, c.Call.Error)); err != nil {
				t.Fatalf("EndRun() = %v", err)
			}
			row := s.finalRun(id)
			if row["status"] != c.After.Status {
				t.Errorf("status after = %v, want %s", row["status"], c.After.Status)
			}
			if diff := cmp.Diff(c.After.Error, row["error"]); diff != "" {
				t.Errorf("error after mismatch (-want +got):\n%s", diff)
			}
			if written := s.runs[id].statsWritten; written != c.After.StatsWritten {
				t.Errorf("stats written = %v, want %v", written, c.After.StatsWritten)
			}
		})
	}
	for i, c := range table.CompleteAgentRunIfRunning.Cases {
		t.Run(fmt.Sprintf("completeAgentRunIfRunning/%d_%s", i, c.From), func(t *testing.T) {
			s, id := newStore(t, c.From, nil)
			tx := &fakeRunTx{h: s.h}
			var got bool
			err := tx.InTx(t.Context(), func(ctx context.Context) error {
				var err error
				got, err = s.CompleteRunIfRunning(ctx, id, runCompletionWithTokens(7, nil))
				return err
			})
			if err != nil {
				t.Fatalf("CompleteRunIfRunning() = %v", err)
			}
			if got != c.Returns {
				t.Errorf("CompleteRunIfRunning() = %v, want %v", got, c.Returns)
			}
			if status := s.finalRun(id)["status"]; status != c.After {
				t.Errorf("status after = %v, want %s", status, c.After)
			}
		})
	}
	for i, c := range table.CancelRunIfRunning.Cases {
		t.Run(fmt.Sprintf("cancelRunIfRunning/%d_%s", i, c.From), func(t *testing.T) {
			s, id := newStore(t, c.From, nil)
			got, err := s.CancelIfRunning(t.Context(), "ws", id, "Cancelled by user")
			if err != nil {
				t.Fatalf("CancelIfRunning() = %v", err)
			}
			if got != c.Returns {
				t.Errorf("CancelIfRunning() = %v, want %v", got, c.Returns)
			}
			if status := s.finalRun(id)["status"]; status != c.After.Status {
				t.Errorf("status after = %v, want %s", status, c.After.Status)
			}
		})
	}
}

func domainStatus(s string) domain.RunStatus { return domain.RunStatus(s) }

// runCompletionWithTokens is a completion record that is recognisable when the stats were written.
func runCompletionWithTokens(tokens int, errText *string) domain.RunCompletion {
	return domain.RunCompletion{TokensIn: tokens, Grounding: domain.NoGrounding, Error: errText}
}
