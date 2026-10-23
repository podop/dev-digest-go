package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// Phase 4 component tests, part 1: the 15 review routes through serve over a real database, the
// run lifecycle on the deterministic mock and the contract goldens (testdata/contracts/review_*).
// Part 2 (phase4_runs_test.go) holds the run, SSE and shutdown tests with scripted providers.

// ---- scripted providers ----

// scripted is an llm.Adapter whose answers the test decides. It records every attempt it gets.
type scripted struct {
	needsKey bool
	complete func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error)

	mu       sync.Mutex
	attempts []llm.Attempt
	inFlight int
	maxIn    int
}

var _ llm.Adapter = (*scripted)(nil)

func (a *scripted) RequiresKey() bool { return a.needsKey }
func (a *scripted) Policy() llm.Policy {
	return llm.Policy{RequestTimeout: 120 * time.Second}
}
func (a *scripted) ListModels(context.Context, string) ([]llm.ModelInfo, error) { return nil, nil }

func (a *scripted) Complete(ctx context.Context, _ string, at llm.Attempt) (llm.AttemptResult, error) {
	a.mu.Lock()
	a.attempts = append(a.attempts, at)
	a.inFlight++
	a.maxIn = max(a.maxIn, a.inFlight)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.inFlight--
		a.mu.Unlock()
	}()
	return a.complete(ctx, at)
}

// calls is the number of attempts received so far.
func (a *scripted) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.attempts)
}

// attempt returns the i-th attempt received (0-based).
func (a *scripted) attempt(i int) llm.Attempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.attempts[i]
}

// maxInFlight is the highest number of attempts that were inside Complete at once.
func (a *scripted) maxInFlight() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maxIn
}

// forAll serves every provider with a.
func (a *scripted) forAll() map[llm.Provider]llm.Adapter {
	return map[llm.Provider]llm.Adapter{llm.OpenAI: a, llm.Anthropic: a, llm.OpenRouter: a}
}

// mockAnswer is the deterministic mock's answer to at.
func mockAnswer(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
	return mock.New(llm.OpenRouter, mock.Options{}).Complete(ctx, "", at)
}

// gate answers like the mock, but only once it is opened; until then a call waits (and gives up
// when its context ends). It makes "a run that is still running" deterministic.
type gate struct {
	*scripted
	open chan struct{}
	once sync.Once
}

func newGate() *gate {
	g := &gate{open: make(chan struct{})}
	g.scripted = &scripted{complete: func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
		select {
		case <-g.open:
			return mockAnswer(ctx, at)
		case <-ctx.Done():
			return llm.AttemptResult{}, ctx.Err()
		}
	}}
	return g
}

// Open lets every waiting and later call through.
func (g *gate) Open() { g.once.Do(func() { close(g.open) }) }

// mockScript is a scripted provider that answers like the mock at once.
func mockScript() *scripted {
	return &scripted{complete: mockAnswer}
}

// ---- stack helpers ----

// pr482 is the id of the seeded pull request #482.
func pr482(t testing.TB, s *stack) string {
	t.Helper()
	return scalar[string](t, s.db.DB, `SELECT id::text FROM pull_requests WHERE number = 482`)
}

// agentByName is the id of the agent with that name.
func agentByName(t testing.TB, s *stack, name string) string {
	t.Helper()
	id := scalar[string](t, s.db.DB, `SELECT id::text FROM agents WHERE name = ?`, name)
	if id == "" {
		t.Fatalf("no agent named %q", name)
	}
	return id
}

const (
	agentGeneral     = "General Reviewer"
	agentPerformance = "Performance Reviewer"
	agentSecurity    = "Security Reviewer"
	agentTestQuality = "Test Quality Reviewer"
)

// startReview posts a review request and returns the answer.
func startReview(t testing.TB, s *stack, prID string, body any, headers ...string) response {
	t.Helper()
	return s.do(http.MethodPost, "/pulls/"+prID+"/review", body, headers...)
}

// startOK posts a review request that must be answered 200 and returns the run ids in order.
func startOK(t testing.TB, s *stack, prID string, body any) (runs []string, multi any) {
	t.Helper()
	resp := startReview(t, s, prID, body)
	if resp.Status != http.StatusOK {
		t.Fatalf("POST /pulls/%s/review %v = %d %s, want 200", prID, body, resp.Status, resp.Body)
	}
	obj := resp.Obj(t)
	for _, r := range obj["runs"].([]any) {
		runs = append(runs, r.(map[string]any)["run_id"].(string))
	}
	return runs, obj["multi_agent_run_id"]
}

// runStatus is the stored status of a run ("" when the run does not exist).
func runStatus(t testing.TB, s *stack, runID string) string {
	t.Helper()
	return scalar[string](t, s.db.DB, `SELECT coalesce((SELECT status FROM agent_runs WHERE id = ?), '')`, runID)
}

// waitStatus waits until the run has one of the statuses and returns it.
func waitStatus(t testing.TB, s *stack, runID string, want ...string) string {
	t.Helper()
	var got string
	eventually(t, 60*time.Second, fmt.Sprintf("run %s to be %v", runID, want), func() bool {
		got = runStatus(t, s, runID)
		return slices.Contains(want, got)
	})
	return got
}

// waitTrace waits until the run has a stored trace and returns the GET /runs/:id/trace answer.
func waitTrace(t testing.TB, s *stack, runID string) response {
	t.Helper()
	var resp response
	eventually(t, 60*time.Second, "the trace of run "+runID, func() bool {
		resp = s.get("/runs/" + runID + "/trace")
		return resp.Status == http.StatusOK
	})
	return resp
}

// count runs SELECT count(*).
func count(t testing.TB, db *gorm.DB, sql string, args ...any) int {
	t.Helper()
	return scalar[int](t, db, sql, args...)
}

// message is the error message of an envelope.
func message(t testing.TB, r response) string {
	t.Helper()
	m, _ := r.errorMessage(t)
	return m
}

// arrayKeys returns the keys, in wire order, of every object of a JSON array.
func arrayKeys(t testing.TB, body []byte) [][]string {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("body is not an array: %q: %v", body, err)
	}
	out := make([][]string, len(items))
	for i, it := range items {
		out[i] = topLevelKeys(t, it)
	}
	return out
}

// keysOf returns the keys, in wire order, of the object at path (dot separated, "" = the root).
func keysAt(t testing.TB, body []byte, path ...string) []string {
	t.Helper()
	raw := json.RawMessage(body)
	for _, p := range path {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
		raw = m[p]
	}
	return topLevelKeys(t, raw)
}

// ---- volatile values of the goldens ----

var volatile = []struct {
	re *regexp.Regexp
	to string
}{
	{regexp.MustCompile(`"((?:total_|avg_)?duration_ms)":\d+(?:\.\d+)?`), `"$1":1`},
	{regexp.MustCompile(`"ms":\d+`), `"ms":1`},
	{regexp.MustCompile(`"t":"\d\d:\d\d:\d\d"`), `"t":"00:00:00"`},
	{regexp.MustCompile(`\(\d+ms\)`), `(1ms)`},
}

// masked replaces what depends on the clock (durations, wall-clock stamps) so a golden is stable.
func masked(r response) response {
	body := r.Body
	for _, v := range volatile {
		body = v.re.ReplaceAll(body, []byte(v.to))
	}
	r.Body = body
	return r
}

// ---- SSE client ----

// sseFrame is one parsed frame of an event stream.
type sseFrame struct {
	ID, Event, Data string
}

// sseEvent is the data of a run-event frame.
type sseEvent struct {
	RunID string          `json:"runId"`
	Seq   int             `json:"seq"`
	Kind  string          `json:"kind"`
	Msg   string          `json:"msg"`
	T     string          `json:"t"`
	Data  json.RawMessage `json:"data"`
}

// capture follows one event stream in the background.
type capture struct {
	Status int
	Header http.Header

	mu   sync.Mutex
	body bytes.Buffer
	done chan struct{}
	err  error
	stop context.CancelFunc
}

// follow opens GET path and returns once the response headers are in; the body is collected in
// the background until the stream ends or Close is called.
func follow(t testing.TB, s *stack, path string, headers ...string) *capture {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", path, err)
	}
	c := &capture{Status: resp.StatusCode, Header: resp.Header, done: make(chan struct{}), stop: cancel}
	go func() {
		defer close(c.done)
		defer func() { _ = resp.Body.Close() }()
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				c.mu.Lock()
				c.body.Write(buf[:n])
				c.mu.Unlock()
			}
			if err != nil {
				if err != io.EOF {
					c.mu.Lock()
					c.err = err
					c.mu.Unlock()
				}
				return
			}
		}
	}()
	t.Cleanup(cancel)
	return c
}

// Body is what has arrived so far.
func (c *capture) Body() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body.String()
}

// Close drops the connection, as a client that went away.
func (c *capture) Close() { c.stop() }

// Wait waits for the stream to end and returns its body; a read error other than EOF fails.
func (c *capture) Wait(t testing.TB, d time.Duration) string {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(d):
		t.Fatalf("the event stream did not end within %s; body so far:\n%s", d, c.Body())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		t.Fatalf("event stream read: %v", c.err)
	}
	return c.body.String()
}

// Ended reports whether the stream has ended.
func (c *capture) Ended() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// frames parses a stream body: the preamble must be "retry: 3000" and every other frame has id,
// event and data fields.
func frames(t testing.TB, body string) []sseFrame {
	t.Helper()
	const preamble = "retry: 3000\n\n"
	if !strings.HasPrefix(body, preamble) {
		t.Fatalf("stream does not start with %q: %q", preamble, body)
	}
	rest := strings.TrimPrefix(body, preamble)
	if rest != "" && !strings.HasSuffix(rest, "\n\n") {
		t.Fatalf("stream ends in the middle of a frame: %q", rest)
	}
	var out []sseFrame
	for _, raw := range strings.Split(strings.TrimSuffix(rest, "\n\n"), "\n\n") {
		if raw == "" {
			continue
		}
		var f sseFrame
		sc := bufio.NewScanner(strings.NewReader(raw))
		sc.Buffer(nil, 1<<22)
		var order []string
		for sc.Scan() {
			name, val, ok := strings.Cut(sc.Text(), ": ")
			if !ok {
				t.Fatalf("frame line %q has no field separator in %q", sc.Text(), raw)
			}
			order = append(order, name)
			switch name {
			case "id":
				f.ID = val
			case "event":
				f.Event = val
			case "data":
				f.Data = val
			default:
				t.Fatalf("unexpected field %q in frame %q", name, raw)
			}
		}
		if diff := cmp.Diff([]string{"id", "event", "data"}, order); diff != "" {
			t.Fatalf("frame field order mismatch (-want +got):\n%s\nframe: %q", diff, raw)
		}
		out = append(out, f)
	}
	return out
}

// events parses the frames of a stream body into run events and checks each frame against its
// event: id = seq, event = kind, data keys runId, seq, kind, msg, t (+ data).
func events(t testing.TB, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	for _, f := range frames(t, body) {
		var ev sseEvent
		if err := json.Unmarshal([]byte(f.Data), &ev); err != nil {
			t.Fatalf("frame data %q is not an event: %v", f.Data, err)
		}
		if f.ID != fmt.Sprint(ev.Seq) || f.Event != ev.Kind {
			t.Errorf("frame id/event = %q/%q, want %d/%s", f.ID, f.Event, ev.Seq, ev.Kind)
		}
		want := []string{"runId", "seq", "kind", "msg", "t"}
		if len(ev.Data) > 0 {
			want = append(want, "data")
		}
		if diff := cmp.Diff(want, topLevelKeys(t, []byte(f.Data))); diff != "" {
			t.Errorf("event data keys mismatch (-want +got):\n%s", diff)
		}
		out = append(out, ev)
	}
	return out
}

// msgs lists the messages of events.
func msgs(evs []sseEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Msg
	}
	return out
}

// ---- row fixtures ----

// fx inserts rows the review routes read; ids come back as strings.
type fx struct {
	t  testing.TB
	db *gorm.DB
	ws string
}

func newFx(t testing.TB, s *stack) fx {
	t.Helper()
	return fx{t: t, db: s.db.DB, ws: scalar[string](t, s.db.DB, `SELECT id::text FROM workspaces ORDER BY created_at LIMIT 1`)}
}

func (f fx) agent(name string) string {
	f.t.Helper()
	return scalar[string](f.t, f.db,
		`INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, ?, 'openrouter', 'm', 'p') RETURNING id::text`, f.ws, name)
}

// pr inserts a pull request of repo with the given files (path -> patch).
func (f fx) pr(repo string, number int, files map[string]string) string {
	f.t.Helper()
	id := scalar[string](f.t, f.db, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, ?, ?, 'octo', 'feat', 'main', ?) RETURNING id::text`, f.ws, repo, number, fmt.Sprintf("Pull request %d", number), fmt.Sprintf("%040x", number))
	for path, patch := range files {
		if err := f.db.Exec(`INSERT INTO pr_files (pr_id, path, patch) VALUES (?, ?, ?)`, id, path, patch).Error; err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

// runRow describes an agent_runs row; zero fields are NULL.
type runRow struct {
	pr, agent string
	ranAt     string
	status    string
	duration  *int
	tokensIn  *int
	cost      *float64
	multi     *string
	errText   *string
	score     *int
}

func (f fx) run(r runRow) string {
	f.t.Helper()
	if r.ranAt == "" {
		r.ranAt = "2026-01-01T10:00:00Z"
	}
	var agent any
	if r.agent != "" {
		agent = r.agent
	}
	return scalar[string](f.t, f.db, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, ran_at, provider, model, status, duration_ms,
		tokens_in, tokens_out, findings_count, grounding, error, score, blockers, cost_usd, multi_agent_run_id)
		VALUES (?, ?, ?, ?, 'openrouter', 'm', ?, ?, ?, 10, 0, '0/0 passed', ?, ?, 0, ?, ?) RETURNING id::text`,
		f.ws, agent, r.pr, r.ranAt, r.status, r.duration, r.tokensIn, r.errText, r.score, r.cost, r.multi)
}

func (f fx) multi(pr, ranAt string) string {
	f.t.Helper()
	return scalar[string](f.t, f.db, `INSERT INTO multi_agent_runs (workspace_id, pr_id, ran_at) VALUES (?, ?, ?) RETURNING id::text`, f.ws, pr, ranAt)
}

// review inserts a review (kind review); run and agent may be "".
func (f fx) review(pr, agent, run, createdAt string, score int) string {
	f.t.Helper()
	var a, r any
	if agent != "" {
		a = agent
	}
	if run != "" {
		r = run
	}
	return scalar[string](f.t, f.db, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, verdict, summary, score, model, created_at, run_id)
		VALUES (?, ?, ?, 'review', 'comment', 'sum', ?, 'm', ?, ?) RETURNING id::text`, f.ws, pr, a, score, createdAt, r)
}

// finding inserts a finding.
func (f fx) finding(review, file string, start, end int, severity, title string) string {
	f.t.Helper()
	return scalar[string](f.t, f.db, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, suggestion, confidence)
		VALUES (?, ?, ?, ?, ?, 'bug', ?, 'because', 'fix it', 0.8) RETURNING id::text`, review, file, start, end, severity, title)
}

func ptr[T any](v T) *T { return &v }

// ---- POST /pulls/:id/review ----

const unknownID = "7b2f6c1e-3d2a-4c5b-9a41-0f6e2d8c9b10"

// AC4003, AC4004, AC4005 (EC4002, EC4003, EC4006, EC4007): a request the route refuses writes
// nothing, and the order of the checks is body, target, pull request.
func TestReviewStart_RefusesBadRequestsWithoutWriting(t *testing.T) {
	g := newGate()
	s := startStack(t, spec{llmAdapters: g.forAll()})
	pr, agent := pr482(t, s), agentByName(t, s, agentGeneral)
	rows := func() [3]int {
		return [3]int{
			count(t, s.db.DB, `SELECT count(*) FROM agent_runs`),
			count(t, s.db.DB, `SELECT count(*) FROM multi_agent_runs`),
			count(t, s.db.DB, `SELECT count(*) FROM run_traces`),
		}
	}
	before := rows()

	const (
		noTarget   = "Provide agentId, all:true or agentIds"
		exactlyOne = "Provide exactly one of agentId, all or agentIds"
		idCount    = "agentIds must contain 1–20 ids" // an en dash
	)
	twentyOne := slices.Repeat([]string{agent}, 21)
	tests := []struct {
		name    string
		body    any
		headers []string
		status  int
		code    string
		msg     string
	}{
		{name: "no body", status: 400, code: "invalid_run_request", msg: noTarget},
		{name: "null", body: "null", status: 400, code: "invalid_run_request", msg: noTarget},
		{name: "empty object", body: map[string]any{}, status: 400, code: "invalid_run_request", msg: noTarget},
		{name: "all false alone", body: map[string]any{"all": false}, status: 400, code: "invalid_run_request", msg: noTarget},
		{name: "empty agentIds", body: map[string]any{"agentIds": []string{}}, status: 400, code: "invalid_run_request", msg: idCount},
		{name: "21 copies of one id", body: map[string]any{"agentIds": twentyOne}, status: 400, code: "invalid_run_request", msg: idCount},
		{name: "agentIds and all", body: map[string]any{"agentIds": []string{agent}, "all": true}, status: 400, code: "invalid_run_request", msg: exactlyOne},
		{name: "agentIds and agentId", body: map[string]any{"agentIds": []string{agent}, "agentId": agent}, status: 400, code: "invalid_run_request", msg: exactlyOne},
		{name: "agentIds item is not a uuid", body: map[string]any{"agentIds": []string{"x"}}, status: 422, code: "validation_error"},
		{name: "all is not a boolean", body: map[string]any{"all": "yes"}, status: 422, code: "validation_error"},
		{name: "text/plain", body: "agentId=" + agent, headers: []string{"Content-Type", "text/plain"}, status: 422, code: "validation_error"},
		{name: "agentId is not a uuid", body: map[string]any{"agentId": "abc"}, status: 404, code: "not_found", msg: "Agent not found"},
		{name: "unknown agent beats unknown pull request", body: map[string]any{"agentId": unknownID}, status: 404, code: "not_found", msg: "Agent not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := pr
			if tc.name == "unknown agent beats unknown pull request" {
				target = unknownID
			}
			resp := startReview(t, s, target, tc.body, tc.headers...)
			if resp.Status != tc.status || resp.errorCode(t) != tc.code {
				t.Fatalf("POST review %s = %d %s, want %d %s", tc.name, resp.Status, resp.Body, tc.status, tc.code)
			}
			if tc.msg != "" {
				if got := message(t, resp); got != tc.msg {
					t.Errorf("message = %q, want %q", got, tc.msg)
				}
			}
		})
	}

	if resp := startReview(t, s, unknownID, map[string]any{"agentId": agent}); resp.Status != 404 || message(t, resp) != "Pull request not found" {
		t.Errorf("known agent, unknown pull request = %d %s, want 404 Pull request not found", resp.Status, resp.Body)
	}
	if diff := cmp.Diff(before, rows()); diff != "" {
		t.Errorf("rows (agent_runs, multi_agent_runs, run_traces) changed by refused requests (-before +after):\n%s", diff)
	}
	if n := g.calls(); n != 0 {
		t.Errorf("LLM calls after refused requests = %d, want 0", n)
	}
}

// AC4001, AC4002, AC4006 (EC4001, EC4004, EC4005): the rows a start writes before it answers.
func TestReviewStart_CreatesRunsAndParent(t *testing.T) {
	g := newGate() // every run stays running
	s := startStack(t, spec{llmAdapters: g.forAll()})
	pr := pr482(t, s)
	general, performance, security := agentByName(t, s, agentGeneral), agentByName(t, s, agentPerformance), agentByName(t, s, agentSecurity)
	if err := s.db.DB.Exec(`UPDATE agents SET enabled = false WHERE id = ?`, performance).Error; err != nil {
		t.Fatal(err)
	}

	type runDB struct {
		ID              string
		AgentID         string
		Status          string
		Source          string
		MultiAgentRunID *string
		Provider        string
		Model           string
	}
	runRows := func(ids ...string) []runDB {
		var out []runDB
		if err := s.db.DB.Raw(`SELECT id::text AS id, agent_id::text AS agent_id, status, source, multi_agent_run_id::text AS multi_agent_run_id, provider, model
			FROM agent_runs WHERE id::text IN ?`, ids).Scan(&out).Error; err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("two distinct agents, one disabled, a duplicate", func(t *testing.T) { // AC4001
		resp := startReview(t, s, pr, map[string]any{"agentIds": []string{general, performance, general}})
		if resp.Status != http.StatusOK {
			t.Fatalf("POST review = %d %s, want 200", resp.Status, resp.Body)
		}
		if diff := cmp.Diff([]string{"pr_id", "multi_agent_run_id", "runs", "reviews"}, topLevelKeys(t, resp.Body)); diff != "" {
			t.Errorf("response keys mismatch (-want +got):\n%s", diff)
		}
		body := resp.Obj(t)
		if body["pr_id"] != pr || len(body["reviews"].([]any)) != 0 {
			t.Errorf("response = %s, want pr_id %s and reviews []", resp.Body, pr)
		}
		multi, _ := body["multi_agent_run_id"].(string)
		if !uuidPattern.MatchString(multi) {
			t.Fatalf("multi_agent_run_id = %v, want a uuid", body["multi_agent_run_id"])
		}
		runs := body["runs"].([]any)
		if len(runs) != 2 {
			t.Fatalf("runs = %v, want 2", runs)
		}
		var ids []string
		for i, want := range []struct{ agent, name string }{{general, agentGeneral}, {performance, agentPerformance}} {
			r := runs[i].(map[string]any)
			ids = append(ids, r["run_id"].(string))
			if r["agent_id"] != want.agent || r["agent_name"] != want.name {
				t.Errorf("runs[%d] = %v, want agent %s (%s)", i, r, want.agent, want.name)
			}
		}
		var shape struct {
			Runs []json.RawMessage `json:"runs"`
		}
		if err := json.Unmarshal(resp.Body, &shape); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"run_id", "agent_id", "agent_name"}, topLevelKeys(t, shape.Runs[0])); diff != "" {
			t.Errorf("run target keys mismatch (-want +got):\n%s", diff)
		}
		for _, row := range runRows(ids...) {
			if row.Status != "running" || row.Source != "local" || row.MultiAgentRunID == nil || *row.MultiAgentRunID != multi ||
				row.Provider != "openrouter" || row.Model != "deepseek/deepseek-v4-flash" {
				t.Errorf("agent_runs row = %+v, want running/local/openrouter model with parent %s", row, multi)
			}
		}
		if n := count(t, s.db.DB, `SELECT count(*) FROM multi_agent_runs WHERE id = ? AND pr_id = ?`, multi, pr); n != 1 {
			t.Errorf("multi_agent_runs rows of the parent = %d, want 1", n)
		}
	})

	t.Run("duplicates collapse, single targets have no parent", func(t *testing.T) { // AC4002, EC4001
		parents := count(t, s.db.DB, `SELECT count(*) FROM multi_agent_runs`)
		runs, multi := startOK(t, s, pr, map[string]any{"agentIds": []string{general, general}})
		if len(runs) != 1 || multi != nil {
			t.Errorf("agentIds [a, a] = runs %v, multi %v; want one run and multi_agent_run_id null", runs, multi)
		}
		if runs, multi := startOK(t, s, pr, map[string]any{"agentId": general}); len(runs) != 1 || multi != nil {
			t.Errorf("agentId = runs %v, multi %v; want one run and null", runs, multi)
		}
		if runs, multi := startOK(t, s, pr, map[string]any{"all": true}); len(runs) != 3 || multi != nil {
			t.Errorf("all:true = runs %v, multi %v; want the 3 enabled agents and null", runs, multi)
		}
		if got := count(t, s.db.DB, `SELECT count(*) FROM multi_agent_runs`); got != parents {
			t.Errorf("multi_agent_runs rows = %d after single-target requests, want %d", got, parents)
		}
	})

	t.Run("agentIds keep their order", func(t *testing.T) {
		resp := startReview(t, s, pr, map[string]any{"agentIds": []string{security, general}})
		var names []string
		for _, r := range resp.Obj(t)["runs"].([]any) {
			names = append(names, r.(map[string]any)["agent_name"].(string))
		}
		if diff := cmp.Diff([]string{agentSecurity, agentGeneral}, names); diff != "" {
			t.Errorf("run order mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("agentId with all:true runs every enabled agent by name", func(t *testing.T) { // AC4006
		resp := startReview(t, s, pr, map[string]any{"agentId": general, "all": true})
		var names []string
		for _, r := range resp.Obj(t)["runs"].([]any) {
			names = append(names, r.(map[string]any)["agent_name"].(string))
		}
		want := []string{agentGeneral, agentSecurity, agentTestQuality} // Performance is disabled
		if diff := cmp.Diff(want, names); diff != "" {
			t.Errorf("runs of {agentId, all:true} mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("a disabled agent named by agentId still runs", func(t *testing.T) { // EC4004
		runs, _ := startOK(t, s, pr, map[string]any{"agentId": performance})
		if len(runs) != 1 {
			t.Errorf("runs = %v, want the disabled agent's run", runs)
		}
	})

	t.Run("no enabled agent: no run, no row", func(t *testing.T) { // EC4005
		if err := s.db.DB.Exec(`UPDATE agents SET enabled = false`).Error; err != nil {
			t.Fatal(err)
		}
		before := count(t, s.db.DB, `SELECT count(*) FROM agent_runs`)
		resp := startReview(t, s, pr, map[string]any{"all": true})
		if resp.Status != http.StatusOK {
			t.Fatalf("POST review all:true = %d %s, want 200", resp.Status, resp.Body)
		}
		body := resp.Obj(t)
		if runs := body["runs"].([]any); len(runs) != 0 || body["multi_agent_run_id"] != nil {
			t.Errorf("response = %s, want runs [] and multi_agent_run_id null", resp.Body)
		}
		if got := count(t, s.db.DB, `SELECT count(*) FROM agent_runs`); got != before {
			t.Errorf("agent_runs rows = %d, want %d (nothing written)", got, before)
		}
	})
}

// AC4007 (FR4045, NFR4009): the review start has a bucket of 10 per minute; the event stream
// counts against no limit at all.
func TestReviewRoutes_RateLimits(t *testing.T) {
	t.Run("the 11th review request is limited", func(t *testing.T) {
		s := startStack(t, spec{rateLimit: true, llmAdapters: mockScript().forAll()})
		pr := pr482(t, s)
		// Without an enabled agent a request starts nothing, so only the bucket is exercised.
		if err := s.db.DB.Exec(`UPDATE agents SET enabled = false`).Error; err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 10; i++ {
			if resp := startReview(t, s, pr, map[string]any{"all": true}); resp.Status != http.StatusOK {
				t.Fatalf("review request %d = %d %s, want 200", i, resp.Status, resp.Body)
			}
		}
		resp := startReview(t, s, pr, map[string]any{"all": true})
		if resp.Status != http.StatusTooManyRequests || resp.errorCode(t) != "internal_error" {
			t.Fatalf("review request 11 = %d %s, want 429 internal_error", resp.Status, resp.Body)
		}
		if resp := s.get("/settings"); resp.Status != http.StatusOK {
			t.Errorf("GET /settings after the bucket ran out = %d, want 200 (the global limit is 120)", resp.Status)
		}
	})

	t.Run("130 event streams in a minute are never limited", func(t *testing.T) {
		s := startStack(t, spec{rateLimit: true, llmAdapters: mockScript().forAll()})
		run := scalar[string](t, s.db.DB, `SELECT id::text FROM agent_runs LIMIT 1`) // the seeded demo run
		for i := 1; i <= 130; i++ {
			resp := s.get("/runs/" + run + "/events")
			if resp.Status != http.StatusOK {
				t.Fatalf("event stream request %d = %d %s, want 200", i, resp.Status, resp.Body)
			}
		}
		for i := 1; i <= 100; i++ { // the streams used none of the 120 global requests
			if resp := s.get("/settings"); resp.Status != http.StatusOK {
				t.Fatalf("GET /settings request %d = %d after the streams, want 200", i, resp.Status)
			}
		}
	})
}

// AC4008 (NFR4009): a foreign Origin is refused on every state-changing review route, and nothing
// changes.
func TestReviewRoutes_ForeignOriginIsForbidden(t *testing.T) {
	g := newGate()
	s := startStack(t, spec{llmAdapters: g.forAll()})
	pr, agent := pr482(t, s), agentByName(t, s, agentGeneral)
	run := scalar[string](t, s.db.DB, `SELECT id::text FROM agent_runs LIMIT 1`)
	review := scalar[string](t, s.db.DB, `SELECT id::text FROM reviews LIMIT 1`)
	finding := scalar[string](t, s.db.DB, `SELECT id::text FROM findings LIMIT 1`)
	snapshot := func() []int {
		var out []int
		for _, q := range []string{
			`SELECT count(*) FROM agent_runs`, `SELECT count(*) FROM multi_agent_runs`, `SELECT count(*) FROM reviews`,
			`SELECT count(*) FROM findings`, `SELECT count(*) FROM memory`,
			`SELECT count(*) FROM findings WHERE accepted_at IS NOT NULL OR dismissed_at IS NOT NULL`,
			`SELECT count(*) FROM agent_runs WHERE status <> 'done'`,
		} {
			out = append(out, count(t, s.db.DB, q))
		}
		return out
	}
	before := snapshot()
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/pulls/" + pr + "/review", map[string]any{"agentIds": []string{agent}}},
		{http.MethodPost, "/runs/" + run + "/cancel", nil},
		{http.MethodDelete, "/runs/" + run, nil},
		{http.MethodDelete, "/reviews/" + review, nil},
		{http.MethodPost, "/findings/" + finding + "/accept", nil},
		{http.MethodPost, "/findings/" + finding + "/dismiss", nil},
		{http.MethodPost, "/findings/" + finding + "/learn", nil},
	} {
		resp := s.do(tc.method, tc.path, tc.body, "Origin", "http://evil.example")
		if resp.Status != http.StatusForbidden || resp.errorCode(t) != "forbidden" {
			t.Errorf("%s %s with a foreign Origin = %d %s, want 403 forbidden", tc.method, tc.path, resp.Status, resp.Body)
		}
	}
	if diff := cmp.Diff(before, snapshot()); diff != "" {
		t.Errorf("rows changed by refused requests (-before +after):\n%s", diff)
	}
	if n := g.calls(); n != 0 {
		t.Errorf("LLM calls = %d, want 0", n)
	}
}

// AC4010: run history newest first with every key, active runs only the running ones.
func TestReviewReads_RunsAndActiveRuns(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	agent := f.agent("Reader")
	pr := f.pr(repo, 900, nil)
	t1 := f.run(runRow{pr: pr, agent: agent, ranAt: "2026-01-01T10:00:00Z", status: "done", duration: ptr(1500), tokensIn: ptr(100), cost: ptr(0.25), score: ptr(88)})
	t2 := f.run(runRow{pr: pr, agent: agent, ranAt: "2026-01-02T10:00:00Z", status: "running"})
	t3 := f.run(runRow{pr: pr, agent: agent, ranAt: "2026-01-03T10:00:00Z", status: "failed", errText: ptr("boom")})
	f.run(runRow{pr: pr, ranAt: "2026-01-02T10:00:00Z", status: "running"}) // a run whose agent is gone, same time as t2

	runs := s.get("/pulls/" + pr + "/runs")
	if runs.Status != http.StatusOK {
		t.Fatalf("GET runs = %d %s, want 200", runs.Status, runs.Body)
	}
	items := list(t, runs)
	var got []string
	for _, it := range items {
		got = append(got, it["run_id"].(string))
	}
	if len(got) != 4 || got[0] != t3 || got[3] != t1 || !slices.Contains(got[1:3], t2) {
		t.Errorf("run order = %v, want t3 %s first, t1 %s last and t2 %s between", got, t3, t1, t2)
	}
	wantKeys := []string{"run_id", "agent_id", "agent_name", "provider", "model", "status", "error", "duration_ms", "tokens_in",
		"tokens_out", "cost_usd", "findings_count", "grounding", "ran_at", "score", "blockers"}
	for i, keys := range arrayKeys(t, runs.Body) {
		if diff := cmp.Diff(wantKeys, keys); diff != "" {
			t.Errorf("runs[%d] keys mismatch (-want +got):\n%s", i, diff)
		}
	}
	if first := items[3]; first["agent_name"] != "Reader" || first["cost_usd"] != 0.25 || first["duration_ms"] != float64(1500) || first["score"] != float64(88) {
		t.Errorf("t1 summary = %v, want agent Reader, cost 0.25, duration 1500, score 88", first)
	}

	active := s.get("/pulls/" + pr + "/runs/active")
	if active.Status != http.StatusOK {
		t.Fatalf("GET active = %d %s", active.Status, active.Body)
	}
	var ids []string
	for _, it := range list(t, active) {
		ids = append(ids, it["run_id"].(string))
	}
	if len(ids) != 2 || !slices.Contains(ids, t2) {
		t.Errorf("active runs = %v, want t2 %s and the run without agent", ids, t2)
	}
	for i, keys := range arrayKeys(t, active.Body) {
		if diff := cmp.Diff([]string{"run_id", "agent_id", "agent_name", "ran_at"}, keys); diff != "" {
			t.Errorf("active[%d] keys mismatch (-want +got):\n%s", i, diff)
		}
	}

	for _, path := range []string{"/pulls/" + unknownID + "/runs", "/pulls/" + unknownID + "/runs/active"} {
		if resp := s.get(path); resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != "[]" {
			t.Errorf("GET %s = %d %s, want 200 []", path, resp.Status, resp.Body)
		}
	}
}

// AC4011, AC4038-adjacent: deleting a run takes its trace, review and findings with it.
func TestReviewDeleteRun_CascadesAndIsIdempotent(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	agent := f.agent("Deleter")
	pr := f.pr(repo, 901, nil)
	run := f.run(runRow{pr: pr, agent: agent, status: "done"})
	review := f.review(pr, agent, run, "2026-01-01T10:00:00Z", 80)
	f.finding(review, "a.ts", 1, 2, "WARNING", "one")
	f.finding(review, "a.ts", 3, 4, "SUGGESTION", "two")
	if err := s.db.DB.Exec(`INSERT INTO run_traces (run_id, trace) VALUES (?, '{}'::jsonb)`, run).Error; err != nil {
		t.Fatal(err)
	}

	resp := s.do(http.MethodDelete, "/runs/"+run, nil)
	if resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != `{"ok":true}` {
		t.Fatalf("DELETE run = %d %s, want 200 {\"ok\":true}", resp.Status, resp.Body)
	}
	for _, q := range []string{
		`SELECT count(*) FROM agent_runs WHERE id = '` + run + `'`,
		`SELECT count(*) FROM run_traces WHERE run_id = '` + run + `'`,
		`SELECT count(*) FROM reviews WHERE id = '` + review + `'`,
		`SELECT count(*) FROM findings WHERE review_id = '` + review + `'`,
	} {
		if n := count(t, s.db.DB, q); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
	resp = s.do(http.MethodDelete, "/runs/"+run, nil)
	if resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != `{"ok":false}` {
		t.Errorf("second DELETE run = %d %s, want 200 {\"ok\":false}", resp.Status, resp.Body)
	}
}

// AC4012: the stored trace is served validated, in contract key order, with source defaulting to
// local; a missing trace is a 404 and a stored document that breaks the contract a 500 whose log
// line does not carry the document.
func TestReviewTrace_ServedStrictlyAndInContractOrder(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	agent := f.agent("Tracer")
	pr := f.pr(repo, 902, nil)
	put := func(run, doc string) {
		t.Helper()
		if err := s.db.DB.Exec(`INSERT INTO run_traces (run_id, trace) VALUES (?, ?::jsonb)`, run, doc).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Keys stored out of contract order (jsonb reorders them anyway), no config.source.
	good := f.run(runRow{pr: pr, agent: agent, status: "done"})
	put(good, `{"log":[{"t":"10:00:00","kind":"info","msg":"hello"}],"specs_read":[],"memory_pulled":[],"raw_output":"raw",
		"tool_calls":[{"tool":"review_file","args":"all files","meta":"single-pass","ms":5}],
		"prompt_assembly":{"user":"u","system":"s","skills":null,"memory":null,"specs":null,"callers":null,"repo_map":null,"pr_description":null,"intent":null},
		"stats":{"grounding":"1/1 passed","findings":1,"cost_usd":0.5,"tokens_out":2,"tokens_in":3,"duration_ms":7},
		"config":{"pr":902,"model":"m","provider":"openrouter","version":"3","agent":"Tracer"},
		"skills_used":[],"intent":null}`)
	resp := s.get("/runs/" + good + "/trace")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET trace = %d %s, want 200", resp.Status, resp.Body)
	}
	if diff := cmp.Diff([]string{"config", "stats", "prompt_assembly", "tool_calls", "raw_output", "memory_pulled", "specs_read", "log", "skills_used", "intent"},
		topLevelKeys(t, resp.Body)); diff != "" {
		t.Errorf("trace keys mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"agent", "version", "provider", "model", "pr", "source"}, keysAt(t, resp.Body, "config")); diff != "" {
		t.Errorf("config keys mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"duration_ms", "tokens_in", "tokens_out", "cost_usd", "findings", "grounding"}, keysAt(t, resp.Body, "stats")); diff != "" {
		t.Errorf("stats keys mismatch (-want +got):\n%s", diff)
	}
	cfg := resp.Obj(t)["config"].(map[string]any)
	if cfg["source"] != "local" || cfg["agent"] != "Tracer" || cfg["version"] != "3" || cfg["pr"] != float64(902) {
		t.Errorf("config = %v, want source local and the stored values", cfg)
	}

	empty := f.run(runRow{pr: pr, agent: agent, status: "done"})
	if resp := s.get("/runs/" + empty + "/trace"); resp.Status != http.StatusNotFound || message(t, resp) != "Run trace not found" {
		t.Errorf("GET trace of a run without one = %d %s, want 404 Run trace not found", resp.Status, resp.Body)
	}

	const secret = "RAW-OUTPUT-OF-A-BROKEN-TRACE-3c1d"
	bad := f.run(runRow{pr: pr, agent: agent, status: "done"})
	put(bad, `{"config":{"agent":"A","model":"m"},"stats":{"duration_ms":"x","tokens_in":0,"tokens_out":0,"findings":0,"grounding":"g"},
		"prompt_assembly":{"system":"s","user":"u"},"tool_calls":[],"raw_output":"`+secret+`","memory_pulled":[],"specs_read":[],"log":[]}`)
	resp = s.get("/runs/" + bad + "/trace")
	if resp.Status != http.StatusInternalServerError || resp.errorCode(t) != "internal_error" || message(t, resp) != "Internal error" {
		t.Fatalf("GET trace of a broken document = %d %s, want 500 internal_error Internal error", resp.Status, resp.Body)
	}
	var errLines []map[string]any
	for _, e := range s.logs.entries(t) {
		if e["level"] == "ERROR" && strings.Contains(fmt.Sprint(e["msg"], e["error"], e["err"]), "trace") {
			errLines = append(errLines, e)
		}
	}
	if len(errLines) != 1 {
		t.Errorf("error log lines about the trace = %v, want exactly one", errLines)
	}
	if strings.Contains(s.logs.String(), secret) || strings.Contains(string(resp.Body), secret) {
		t.Error("the broken trace document leaked into the log or the response")
	}
}

// AC4013, AC4014, AC4015: reviews newest first with usage only when a run exists, findings in
// insertion order and carrying the eval case; accept and dismiss swap the two timestamps;
// delete takes the findings along.
func TestReviewReads_ReviewsFindingsAndActions(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	agent := f.agent("Triager")
	pr := f.pr(repo, 903, nil)
	run := f.run(runRow{pr: pr, agent: agent, status: "done", duration: ptr(900), tokensIn: ptr(321), cost: ptr(0.125)})
	older := f.review(pr, agent, "", "2026-01-01T10:00:00Z", 70) // seeded without a run
	newer := f.review(pr, agent, run, "2026-01-02T10:00:00Z", 90)
	first := f.finding(newer, "a.ts", 1, 2, "CRITICAL", "first")
	second := f.finding(newer, "a.ts", 5, 6, "WARNING", "second")
	third := f.finding(newer, "b.ts", 1, 1, "SUGGESTION", "third")
	oldFinding := f.finding(older, "c.ts", 1, 1, "SUGGESTION", "old")
	evalCase := scalar[string](t, s.db.DB, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, source_finding_id)
		VALUES (?, 'agent', ?, 'case', ?) RETURNING id::text`, f.ws, agent, second)

	read := func() (response, []map[string]any) {
		t.Helper()
		resp := s.get("/pulls/" + pr + "/reviews")
		if resp.Status != http.StatusOK {
			t.Fatalf("GET reviews = %d %s, want 200", resp.Status, resp.Body)
		}
		return resp, list(t, resp)
	}
	titles := func(r map[string]any) []string {
		var out []string
		for _, fd := range r["findings"].([]any) {
			out = append(out, fd.(map[string]any)["title"].(string))
		}
		return out
	}

	resp, reviews := read()
	if len(reviews) != 2 || reviews[0]["id"] != newer || reviews[1]["id"] != older {
		t.Fatalf("reviews = %s, want the newer review first", resp.Body)
	}
	keys := arrayKeys(t, resp.Body)
	wantNewer := []string{"id", "pr_id", "agent_id", "run_id", "agent_name", "kind", "verdict", "summary", "score", "model", "created_at",
		"cost_usd", "tokens_in", "tokens_out", "findings"}
	wantOlder := slices.DeleteFunc(slices.Clone(wantNewer), func(k string) bool { return strings.HasSuffix(k, "_usd") || strings.HasPrefix(k, "tokens_") })
	if diff := cmp.Diff(wantNewer, keys[0]); diff != "" {
		t.Errorf("review with a run: keys mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantOlder, keys[1]); diff != "" {
		t.Errorf("review without a run: keys mismatch (-want +got):\n%s", diff)
	}
	if reviews[0]["cost_usd"] != 0.125 || reviews[0]["tokens_in"] != float64(321) || reviews[0]["agent_name"] != "Triager" {
		t.Errorf("newer review = %v, want cost 0.125, tokens_in 321, agent Triager", reviews[0])
	}
	if diff := cmp.Diff([]string{"first", "second", "third"}, titles(reviews[0])); diff != "" {
		t.Errorf("findings are not in insertion order (-want +got):\n%s", diff)
	}
	wantFinding := []string{"id", "severity", "category", "title", "file", "start_line", "end_line", "rationale", "suggestion", "confidence",
		"kind", "trifecta_components", "evidence", "skill", "out_of_scope", "review_id", "accepted_at", "dismissed_at", "eval_case_id"}
	var shape []struct {
		Findings []json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(resp.Body, &shape); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(wantFinding, topLevelKeys(t, shape[0].Findings[0])); diff != "" {
		t.Errorf("finding keys mismatch (-want +got):\n%s", diff)
	}
	fd := reviews[0]["findings"].([]any)[1].(map[string]any)
	if fd["eval_case_id"] != evalCase || fd["evidence"] != nil || fd["out_of_scope"] != false {
		t.Errorf("second finding = %v, want eval_case_id %s, evidence null, out_of_scope false", fd, evalCase)
	}
	if fd0 := reviews[0]["findings"].([]any)[0].(map[string]any); fd0["eval_case_id"] != nil {
		t.Errorf("first finding eval_case_id = %v, want null", fd0["eval_case_id"])
	}
	if resp := s.get("/pulls/" + unknownID + "/reviews"); resp.Status != http.StatusNotFound || message(t, resp) != "Pull request not found" {
		t.Errorf("GET reviews of an unknown pull request = %d %s, want 404 Pull request not found", resp.Status, resp.Body)
	}

	// AC4015: accept and dismiss.
	accept := s.post("/findings/"+first+"/accept", nil)
	if accept.Status != http.StatusOK {
		t.Fatalf("accept = %d %s, want 200", accept.Status, accept.Body)
	}
	if diff := cmp.Diff([]string{"finding"}, topLevelKeys(t, accept.Body)); diff != "" {
		t.Errorf("accept keys mismatch (-want +got):\n%s", diff)
	}
	af := accept.Obj(t)["finding"].(map[string]any)
	if at, _ := af["accepted_at"].(string); !wireTime.MatchString(at) || af["dismissed_at"] != nil {
		t.Errorf("accepted finding = %v, want accepted_at in wire format and dismissed_at null", af)
	}
	if _, has := af["memory_id"]; has {
		t.Errorf("accept answer has memory_id: %v", af)
	}
	dismiss := s.do(http.MethodPost, "/findings/"+first+"/dismiss", map[string]any{"reply": "x"}) // the body is ignored
	df := dismiss.Obj(t)["finding"].(map[string]any)
	if at, _ := df["dismissed_at"].(string); dismiss.Status != http.StatusOK || !wireTime.MatchString(at) || df["accepted_at"] != nil {
		t.Errorf("dismiss = %d %s, want dismissed_at set and accepted_at null", dismiss.Status, dismiss.Body)
	}
	if resp := s.post("/findings/"+unknownID+"/accept", nil); resp.Status != http.StatusNotFound || message(t, resp) != "Finding not found" {
		t.Errorf("accept of an unknown finding = %d %s, want 404 Finding not found", resp.Status, resp.Body)
	}
	// AC4014: delete a review.
	del := s.do(http.MethodDelete, "/reviews/"+older, nil)
	if del.Status != http.StatusOK || strings.TrimSpace(string(del.Body)) != `{"ok":true}` {
		t.Errorf("DELETE review = %d %s, want {\"ok\":true}", del.Status, del.Body)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM findings WHERE id = ?`, oldFinding); n != 0 {
		t.Errorf("findings of the deleted review = %d, want 0", n)
	}
	if resp := s.do(http.MethodDelete, "/reviews/"+older, nil); resp.Status != http.StatusNotFound || message(t, resp) != "Review not found" {
		t.Errorf("second DELETE review = %d %s, want 404 Review not found", resp.Status, resp.Body)
	}
	_ = third
}

// AC4028 (EC4008): a pull request without stored files is reviewed on an empty diff: the mock's
// findings are all dropped by grounding.
func TestReview_PullRequestWithoutFilesEndsDoneWithNoFindings(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	pr := f.pr(repo, 904, nil)
	runs, _ := startOK(t, s, pr, map[string]any{"agentId": agentByName(t, s, agentGeneral)})
	if got := waitStatus(t, s, runs[0], "done", "failed"); got != "done" {
		t.Fatalf("run ended %s: %s", got, scalar[string](t, s.db.DB, `SELECT coalesce(error, '') FROM agent_runs WHERE id = ?`, runs[0]))
	}

	row := struct {
		FindingsCount int
		Grounding     string
		Score         int
	}{}
	if err := s.db.DB.Raw(`SELECT findings_count, grounding, score FROM agent_runs WHERE id = ?`, runs[0]).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.FindingsCount != 0 || row.Grounding != "0/2 passed" || row.Score != 100 {
		t.Errorf("run = %+v, want 0 findings, grounding 0/2 passed, score 100", row)
	}
	var logged []string
	for _, l := range waitTrace(t, s, runs[0]).Obj(t)["log"].([]any) {
		logged = append(logged, l.(map[string]any)["msg"].(string))
	}
	if !slices.Contains(logged, "Reviewing 0 changed file(s) in one pass") {
		t.Errorf("trace log = %q, want it to hold %q", logged, "Reviewing 0 changed file(s) in one pass")
	}
}

// reviewOutcome is the part of a finished mock review that must repeat (AC4075).
type reviewOutcome struct {
	Run       map[string]any
	Verdict   any
	Summary   any
	Score     any
	Findings  []map[string]any
	TraceStat any
}

// outcomeOf reads the review of a finished run through the API, minus ids and timestamps.
func outcomeOf(t testing.TB, s *stack, pr, run string) reviewOutcome {
	t.Helper()
	var out reviewOutcome
	for _, r := range list(t, s.get("/pulls/"+pr+"/runs")) {
		if r["run_id"] == run {
			out.Run = r
			delete(out.Run, "run_id")
			delete(out.Run, "ran_at")
			delete(out.Run, "duration_ms")
		}
	}
	for _, r := range list(t, s.get("/pulls/"+pr+"/reviews")) {
		if r["run_id"] != run {
			continue
		}
		out.Verdict, out.Summary, out.Score = r["verdict"], r["summary"], r["score"]
		for _, fd := range r["findings"].([]any) {
			m := fd.(map[string]any)
			for _, k := range []string{"id", "review_id"} {
				delete(m, k)
			}
			out.Findings = append(out.Findings, m)
		}
	}
	trace := waitTrace(t, s, run).Obj(t)
	out.TraceStat = trace["stats"]
	delete(out.TraceStat.(map[string]any), "duration_ms")
	return out
}

// AC4075 (NFR4014): two mock reviews of the same pull request are identical but for ids and times;
// AC4025 (the numbers of the mock review).
func TestReview_TwoMockReviewsAreIdentical(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	pr, agent := pr482(t, s), agentByName(t, s, agentGeneral)
	var outcomes []reviewOutcome
	var runs []string
	for range 2 {
		ids, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
		waitStatus(t, s, ids[0], "done")
		runs = append(runs, ids[0])
		outcomes = append(outcomes, outcomeOf(t, s, pr, ids[0]))
	}
	if diff := cmp.Diff(outcomes[0], outcomes[1]); diff != "" {
		t.Errorf("the second mock review differs from the first (-first +second):\n%s", diff)
	}
	run := outcomes[0].Run
	if run["score"] != float64(85) || run["grounding"] != "2/2 passed" || run["tokens_in"] != float64(1200) ||
		run["tokens_out"] != float64(180) || run["cost_usd"] != 0.00042 || run["findings_count"] != float64(2) || run["blockers"] != float64(0) {
		t.Errorf("run summary = %v, want score 85 (100-12-3), grounding 2/2 passed, 1200/180 tokens, cost 0.00042, 2 findings, 0 blockers", run)
	}
	if sha := scalar[string](t, s.db.DB, `SELECT last_reviewed_sha FROM pull_requests WHERE id = ?`, pr); sha != "a1b2c3d4e5f6" {
		t.Errorf("last_reviewed_sha = %q, want the head sha a1b2c3d4e5f6", sha)
	}
	if len(outcomes[0].Findings) != 2 {
		t.Errorf("findings = %v, want 2", outcomes[0].Findings)
	}
	_ = runs
}

// ---- contract goldens (AC4016) ----

// AC4016, NFR4001: every JSON response shape of the review routes, recorded after a real flow on
// the mock: a single run held in its LLM call (active runs), cancelled (ended trace), run again to
// the end (completed trace, history, reviews, accept, dismiss, learn, estimates) and a 3-agent
// request (start, multi-agent run, list). Durations and wall-clock stamps are masked. The
// frontend contract test parses each recording with the vendored Zod contracts.
func TestContracts_Phase4GoldenResponses(t *testing.T) {
	g := newGate()
	s := startStack(t, spec{llmAdapters: g.forAll()})
	gold := &goldens{t: t, cloneDir: s.cfg.CloneDir, ids: map[string]string{}}
	check := func(name string, resp response, status int) {
		t.Helper()
		assertWireTimes(t, name, resp.Body)
		gold.check(name, masked(resp), status)
	}
	pr, repo := pr482(t, s), repoByName(t, s, "acme/payments-api")
	general := agentByName(t, s, agentGeneral)

	// A single run, still inside its LLM call.
	start := startReview(t, s, pr, map[string]any{"agentIds": []string{general}})
	check("review_start_single", start, http.StatusOK)
	held := start.Obj(t)["runs"].([]any)[0].(map[string]any)["run_id"].(string)
	eventually(t, 30*time.Second, "the run to reach its LLM call", func() bool { return g.calls() == 1 })
	check("review_active_runs", s.get("/pulls/"+pr+"/runs/active"), http.StatusOK)

	// Cancelled: the route answers ok, the run records an ended trace.
	check("review_ok", s.post("/runs/"+held+"/cancel", nil), http.StatusOK)
	if got := waitStatus(t, s, held, "cancelled"); got != "cancelled" {
		t.Fatalf("held run = %s, want cancelled", got)
	}
	check("review_trace_ended", waitTrace(t, s, held), http.StatusOK)

	// The same request again, let through: a completed review.
	g.Open()
	done := startReview(t, s, pr, map[string]any{"agentIds": []string{general}})
	run := done.Obj(t)["runs"].([]any)[0].(map[string]any)["run_id"].(string)
	waitStatus(t, s, run, "done")
	check("review_trace_completed", waitTrace(t, s, run), http.StatusOK)
	check("review_runs", s.get("/pulls/"+pr+"/runs"), http.StatusOK)
	reviews := s.get("/pulls/" + pr + "/reviews")
	check("review_reviews", reviews, http.StatusOK)

	var findings []string
	for _, fd := range list(t, reviews)[0]["findings"].([]any) {
		findings = append(findings, fd.(map[string]any)["id"].(string))
	}
	if len(findings) != 2 {
		t.Fatalf("findings of the mock review = %v, want 2", findings)
	}
	check("review_accept", s.post("/findings/"+findings[0]+"/accept", nil), http.StatusOK)
	check("review_dismiss", s.post("/findings/"+findings[0]+"/dismiss", nil), http.StatusOK)
	check("review_learn", s.post("/findings/"+findings[1]+"/learn", nil), http.StatusOK)
	// Only General Reviewer has finished runs so far (the seeded one and the mock one), so the
	// estimates have a single row and no id order to depend on: drop the run of the eval demo
	// PR #483 (a Security Reviewer run) first.
	if err := s.db.DB.Exec(`DELETE FROM agent_runs WHERE pr_id = (SELECT id FROM pull_requests WHERE number = 483)`).Error; err != nil {
		t.Fatal(err)
	}
	check("review_estimates", s.get("/runs/estimates"), http.StatusOK)

	// Three agents: a parent run with three columns; one finding is raised to CRITICAL so the
	// location group of line 7 is a conflict.
	multi := startReview(t, s, pr, map[string]any{"agentIds": []string{
		agentByName(t, s, agentSecurity), agentByName(t, s, agentPerformance), agentByName(t, s, agentTestQuality)}})
	check("review_start_multi", multi, http.StatusOK)
	mbody := multi.Obj(t)
	parent := mbody["multi_agent_run_id"].(string)
	for _, r := range mbody["runs"].([]any) {
		waitStatus(t, s, r.(map[string]any)["run_id"].(string), "done")
	}
	if err := s.db.DB.Exec(`UPDATE findings SET severity = 'CRITICAL'
		WHERE title LIKE 'Rate-limit key%' AND review_id IN (SELECT r.id FROM reviews r JOIN agent_runs a ON a.id = r.run_id
			JOIN agents ag ON ag.id = a.agent_id WHERE a.multi_agent_run_id = ? AND ag.name = ?)`, parent, agentPerformance).Error; err != nil {
		t.Fatal(err)
	}
	check("review_multi_run", s.get("/multi-agent-runs/"+parent), http.StatusOK)
	check("review_multi_list", s.get("/repos/"+repo+"/multi-agent-runs"), http.StatusOK)
}

// ---- multi-agent read model, estimates, Learn ----

// AC4062 (EC4029, EC4030): the columns of a multi-agent run in English collation order, a deleted
// agent's column labelled "", totals, location groups over the finished columns only, memory ids.
func TestMultiAgentRun_ColumnsTotalsAndGroups(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	pr := f.pr(repo, 910, nil)
	beta, alpha, gamma := f.agent("beta-x"), f.agent("Alpha"), f.agent("gamma")
	m := f.multi(pr, "2026-01-05T10:00:00Z")
	betaRun := f.run(runRow{pr: pr, agent: beta, status: "done", duration: ptr(5000), cost: ptr(0.5), score: ptr(60), multi: &m})
	alphaRun := f.run(runRow{pr: pr, agent: alpha, status: "done", duration: ptr(9000), score: ptr(70), multi: &m}) // unpriced
	gammaRun := f.run(runRow{pr: pr, agent: gamma, status: "failed", duration: ptr(1000), cost: ptr(0.25), errText: ptr("boom"), multi: &m})
	f.run(runRow{pr: pr, agent: alpha, status: "done", multi: nil}) // not linked: not a column
	alphaReview := f.review(pr, alpha, alphaRun, "2026-01-05T10:01:00Z", 71)
	betaReview := f.review(pr, beta, betaRun, "2026-01-05T10:02:00Z", 61)
	a1 := f.finding(alphaReview, "x.ts", 10, 12, "WARNING", "alpha one")
	f.finding(alphaReview, "z.ts", 1, 1, "SUGGESTION", "alpha two")
	b1 := f.finding(betaReview, "x.ts", 11, 15, "CRITICAL", "beta one")
	if err := s.db.DB.Exec(`DELETE FROM agents WHERE id = ?`, gamma).Error; err != nil {
		t.Fatal(err)
	}
	_ = gammaRun
	if resp := s.post("/findings/"+b1+"/learn", nil); resp.Status != http.StatusOK {
		t.Fatalf("learn = %d %s", resp.Status, resp.Body)
	}

	resp := s.get("/multi-agent-runs/" + m)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET multi-agent run = %d %s, want 200", resp.Status, resp.Body)
	}
	if diff := cmp.Diff([]string{"id", "pr_id", "repo_id", "pr_number", "pr_title", "ran_at", "agent_count", "total_duration_ms",
		"total_cost_usd", "columns", "conflicts"}, topLevelKeys(t, resp.Body)); diff != "" {
		t.Errorf("multi-agent run keys mismatch (-want +got):\n%s", diff)
	}
	var shape struct {
		Columns   []json.RawMessage `json:"columns"`
		Conflicts []json.RawMessage `json:"conflicts"`
	}
	if err := json.Unmarshal(resp.Body, &shape); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"run_id", "agent_id", "agent_name", "provider", "model", "status", "error", "verdict", "score",
		"summary", "duration_ms", "cost_usd", "findings"}, topLevelKeys(t, shape.Columns[0])); diff != "" {
		t.Errorf("column keys mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"file", "line", "end_line", "title", "is_conflict", "takes"}, topLevelKeys(t, shape.Conflicts[0])); diff != "" {
		t.Errorf("conflict keys mismatch (-want +got):\n%s", diff)
	}
	body := resp.Obj(t)
	if body["agent_count"] != float64(3) || body["total_duration_ms"] != float64(9000) || body["total_cost_usd"] != 0.75 ||
		body["pr_number"] != float64(910) || body["repo_id"] != repo {
		t.Errorf("header = %v, want agent_count 3, total_duration_ms 9000, total_cost_usd 0.75, pr 910", body)
	}
	cols := body["columns"].([]any)
	var names []string
	for _, c := range cols {
		names = append(names, c.(map[string]any)["agent_name"].(string))
	}
	if diff := cmp.Diff([]string{"", "Alpha", "beta-x"}, names); diff != "" {
		t.Errorf("column order mismatch (-want +got):\n%s", diff)
	}
	gone := cols[0].(map[string]any)
	if gone["agent_id"] != "" || gone["status"] != "failed" || gone["error"] != "boom" || gone["verdict"] != nil || gone["run_id"] != gammaRun {
		t.Errorf("the deleted agent's column = %v, want agent_id and agent_name \"\", failed, error boom, no verdict", gone)
	}
	if got := cols[1].(map[string]any); got["score"] != float64(71) || got["verdict"] != "comment" || got["cost_usd"] != nil {
		t.Errorf("Alpha's column = %v, want the review's score 71, verdict comment, no cost", got)
	}
	betaFindings := cols[2].(map[string]any)["findings"].([]any)
	if mem, _ := betaFindings[0].(map[string]any)["memory_id"].(string); mem == "" {
		t.Errorf("beta's finding has memory_id %v after Learn, want an id", betaFindings[0].(map[string]any)["memory_id"])
	}
	if mem, ok := cols[1].(map[string]any)["findings"].([]any)[0].(map[string]any)["memory_id"]; !ok || mem != nil {
		t.Errorf("Alpha's finding memory_id = %v (present %v), want present and null", mem, ok)
	}

	groups := body["conflicts"].([]any)
	if len(groups) != 2 {
		t.Fatalf("conflicts = %v, want 2 groups (x.ts 10-15 and z.ts)", groups)
	}
	g0 := groups[0].(map[string]any)
	if g0["file"] != "x.ts" || g0["line"] != float64(10) || g0["end_line"] != float64(15) || g0["title"] != "beta one" || g0["is_conflict"] != true {
		t.Errorf("first group = %v, want x.ts 10-15 titled beta one, a conflict", g0)
	}
	takes := g0["takes"].([]any)
	if len(takes) != 2 { // the failed column has no take
		t.Fatalf("takes = %v, want one per finished column", takes)
	}
	t0, t1 := takes[0].(map[string]any), takes[1].(map[string]any)
	if t0["persona"] != "Alpha" || t0["verdict"] != "WARNING" || t1["persona"] != "beta-x" || t1["verdict"] != "CRITICAL" ||
		cmp.Diff([]any{a1}, t0["finding_ids"]) != "" || cmp.Diff([]any{b1}, t1["finding_ids"]) != "" {
		t.Errorf("takes = %v, want Alpha WARNING [%s] and beta-x CRITICAL [%s]", takes, a1, b1)
	}
	g1 := groups[1].(map[string]any)
	if g1["file"] != "z.ts" || g1["is_conflict"] != true || g1["takes"].([]any)[1].(map[string]any)["verdict"] != "ignored" {
		t.Errorf("second group = %v, want z.ts flagged by Alpha only, beta-x ignored, a conflict", g1)
	}

	if resp := s.get("/multi-agent-runs/" + unknownID); resp.Status != http.StatusNotFound || resp.errorCode(t) != "multi_agent_run_not_found" {
		t.Errorf("GET unknown multi-agent run = %d %s, want 404 multi_agent_run_not_found", resp.Status, resp.Body)
	}

	// English collation, not byte order: "alpha" sorts before "Beta".
	m2 := f.multi(pr, "2026-01-06T10:00:00Z")
	for _, a := range []string{f.agent("Beta"), f.agent("alpha")} {
		f.run(runRow{pr: pr, agent: a, status: "done", multi: &m2})
	}
	var order []string
	for _, c := range s.get("/multi-agent-runs/" + m2).Obj(t)["columns"].([]any) {
		order = append(order, c.(map[string]any)["agent_name"].(string))
	}
	if diff := cmp.Diff([]string{"alpha", "Beta"}, order); diff != "" {
		t.Errorf("collation order mismatch (-want +got):\n%s", diff)
	}
}

// AC4064 (EC4031): the repo's multi-agent runs, newest first, limited to 20.
func TestMultiAgentRuns_ListAndLimit(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	agent := f.agent("Lister")
	pr := f.pr(repo, 911, nil)
	var ids []string
	for i := range 25 {
		m := f.multi(pr, fmt.Sprintf("2026-02-%02dT10:00:00Z", i+1))
		ids = append(ids, m)
		status := "done"
		if i == 24 {
			status = "running"
		}
		cost := ptr(0.5)
		if i == 23 {
			cost = nil
		}
		f.run(runRow{pr: pr, agent: agent, status: status, cost: cost, multi: &m})
		f.run(runRow{pr: pr, agent: agent, status: "done", cost: cost, multi: &m})
	}
	// ids[i] is the (i+1)th day, so the newest run set is ids[24].
	idsOf := func(resp response) []string {
		var out []string
		for _, it := range list(t, resp) {
			out = append(out, it["id"].(string))
		}
		return out
	}
	all := s.get("/repos/" + repo + "/multi-agent-runs")
	if all.Status != http.StatusOK {
		t.Fatalf("GET list = %d %s", all.Status, all.Body)
	}
	got := idsOf(all)
	if len(got) != 20 || got[0] != ids[24] || got[19] != ids[5] {
		t.Errorf("default list = %d items from %v to %v, want the 20 newest (%s … %s)", len(got), got[0], got[len(got)-1], ids[24], ids[5])
	}
	for _, keys := range arrayKeys(t, all.Body)[:1] {
		if diff := cmp.Diff([]string{"id", "pr_id", "pr_number", "pr_title", "ran_at", "agent_count", "running_count", "total_cost_usd"}, keys); diff != "" {
			t.Errorf("summary keys mismatch (-want +got):\n%s", diff)
		}
	}
	newest := list(t, all)[0]
	if newest["running_count"] != float64(1) || newest["agent_count"] != float64(2) || newest["total_cost_usd"] != 1.0 || newest["pr_number"] != float64(911) {
		t.Errorf("newest summary = %v, want 1 running of 2 agents, cost 1.0, PR 911", newest)
	}
	if second := list(t, all)[1]; second["total_cost_usd"] != nil {
		t.Errorf("a run set without cost has total_cost_usd %v, want null", second["total_cost_usd"])
	}
	if five := idsOf(s.get("/repos/" + repo + "/multi-agent-runs?limit=5")); len(five) != 5 || five[0] != ids[24] {
		t.Errorf("limit=5 = %v, want the 5 newest", five)
	}
	for _, tc := range []struct {
		query  string
		status int
		code   string
	}{
		{"limit=0", 400, "invalid_limit"}, {"limit=21", 400, "invalid_limit"}, {"limit=abc", 422, "validation_error"}, {"limit=2.5", 422, "validation_error"},
	} {
		resp := s.get("/repos/" + repo + "/multi-agent-runs?" + tc.query)
		if resp.Status != tc.status || resp.errorCode(t) != tc.code {
			t.Errorf("?%s = %d %s, want %d %s", tc.query, resp.Status, resp.Body, tc.status, tc.code)
		}
		if tc.code == "invalid_limit" && message(t, resp) != "limit must be between 1 and 20" {
			t.Errorf("?%s message = %q, want %q", tc.query, message(t, resp), "limit must be between 1 and 20")
		}
	}
	other := scalar[string](t, s.db.DB, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	foreign := scalar[string](t, s.db.DB, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'x', 'y', 'x/y') RETURNING id::text`, other)
	if resp := s.get("/repos/" + foreign + "/multi-agent-runs"); resp.Status != http.StatusNotFound || message(t, resp) != "Repo not found" {
		t.Errorf("GET list of another workspace's repo = %d %s, want 404 Repo not found", resp.Status, resp.Body)
	}
}

// AC4065: estimates average the newest 10 done runs of each agent that still exists.
func TestRunEstimates_AverageTheNewestTenDoneRuns(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	agent, gone := f.agent("Estimated"), f.agent("Gone")
	pr := f.pr(repo, 912, nil)
	for i := 1; i <= 12; i++ {
		cost := ptr(float64(i) / 100)
		if i == 5 {
			cost = nil
		}
		f.run(runRow{pr: pr, agent: agent, status: "done", duration: ptr(i * 1000), cost: cost, ranAt: fmt.Sprintf("2026-03-%02dT10:00:00Z", i)})
	}
	f.run(runRow{pr: pr, agent: agent, status: "failed", duration: ptr(99000), cost: ptr(9.0), ranAt: "2026-03-20T10:00:00Z"})
	f.run(runRow{pr: pr, agent: gone, status: "done", duration: ptr(1000), cost: ptr(1.0)})
	if err := s.db.DB.Exec(`DELETE FROM agents WHERE id = ?`, gone).Error; err != nil {
		t.Fatal(err)
	}

	resp := s.get("/runs/estimates")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET estimates = %d %s", resp.Status, resp.Body)
	}
	rows := list(t, resp)
	var ids []string
	var mine map[string]any
	for _, r := range rows {
		ids = append(ids, r["agent_id"].(string))
		if r["agent_id"] == agent {
			mine = r
		}
		if r["agent_id"] == gone {
			t.Errorf("the deleted agent has an estimate row: %v", r)
		}
	}
	if !slices.IsSorted(ids) {
		t.Errorf("rows are not ordered by agent id: %v", ids)
	}
	if mine == nil {
		t.Fatalf("no row for the estimated agent in %s", resp.Body)
	}
	if diff := cmp.Diff([]string{"agent_id", "sample_size", "avg_duration_ms", "avg_cost_usd"}, arrayKeys(t, resp.Body)[0]); diff != "" {
		t.Errorf("estimate keys mismatch (-want +got):\n%s", diff)
	}
	// Newest ten: i = 3..12 → durations 3000..12000 (mean 7500); costs of all but i = 5.
	wantCost := (3 + 4 + 6 + 7 + 8 + 9 + 10 + 11 + 12) / 100.0 / 9
	if mine["sample_size"] != float64(10) || mine["avg_duration_ms"] != 7500.0 || cmp.Diff(wantCost, mine["avg_cost_usd"], cmpFloat) != "" {
		t.Errorf("estimate = %v, want sample 10, avg duration 7500, avg cost %v", mine, wantCost)
	}
}

// AC4066 (EC4032): Learn twice at once stores one memory entry; both answers carry its id.
func TestLearn_ConcurrentCallsShareOneMemoryEntry(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	pr, repo := pr482(t, s), repoByName(t, s, "acme/payments-api")
	agent := f.agent("Learner")
	review := f.review(pr, agent, "", "2026-01-01T10:00:00Z", 50)
	finding := f.finding(review, "a.ts", 1, 2, "WARNING", "title")

	answers := make([]response, 2)
	var wg sync.WaitGroup
	for i := range answers {
		wg.Go(func() {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, s.base+"/findings/"+finding+"/learn", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			answers[i] = response{Status: resp.StatusCode, Header: resp.Header, Body: body}
		})
	}
	wg.Wait()

	var memoryIDs []string
	for i, a := range answers {
		if a.Status != http.StatusOK {
			t.Fatalf("learn %d = %d %s, want 200", i, a.Status, a.Body)
		}
		obj := a.Obj(t)
		id, _ := obj["memoryId"].(string)
		fm, _ := obj["finding"].(map[string]any)["memory_id"].(string)
		if id == "" || fm != id {
			t.Errorf("learn %d = %s, want memoryId equal to finding.memory_id", i, a.Body)
		}
		memoryIDs = append(memoryIDs, id)
	}
	if memoryIDs[0] != memoryIDs[1] {
		t.Errorf("memory ids = %v, want the same entry twice", memoryIDs)
	}
	type memoryRow struct {
		Scope, Kind, Content string
		Sources              string
		Embedded             bool
		RepoID               string
	}
	var rows []memoryRow
	if err := s.db.DB.Raw(`SELECT scope, kind, content, sources::text AS sources, embedding IS NOT NULL AS embedded, repo_id::text AS repo_id
		FROM memory WHERE kind = 'learning'`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("learning memory rows = %+v, want exactly one", rows)
	}
	r := rows[0]
	if r.Scope != "repo" || r.Content != "title\n\nbecause\n\nSuggested fix: fix it" || r.Embedded || r.RepoID != repo {
		t.Errorf("memory row = %+v, want scope repo, content \"title\\n\\nbecause\\n\\nSuggested fix: fix it\", no embedding, repo %s", r, repo)
	}
	var sources []map[string]any
	if err := json.Unmarshal([]byte(r.Sources), &sources); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]map[string]any{{"pr": float64(482), "context": "finding:" + finding}}, sources); diff != "" {
		t.Errorf("sources mismatch (-want +got):\n%s", diff)
	}
	if resp := s.post("/findings/"+unknownID+"/learn", nil); resp.Status != http.StatusNotFound || message(t, resp) != "Finding not found" {
		t.Errorf("learn of an unknown finding = %d %s, want 404 Finding not found", resp.Status, resp.Body)
	}
}

// cmpFloat compares floats that went through a numeric average.
var cmpFloat = cmpopts.EquateApprox(0, 1e-9)

// ---- seed ----

// AC4071 (EC4035): the demo review of PR #482, written once; deleting only the review keeps it
// gone, deleting the demo run brings both back at the next start.
func TestSeed_DemoReviewAcrossRestarts(t *testing.T) {
	db := pgtest.Migrated(t)
	restart(t, db)
	pr := scalar[string](t, db.DB, `SELECT id::text FROM pull_requests WHERE number = 482`)
	counts := func() [3]int {
		return [3]int{
			count(t, db.DB, `SELECT count(*) FROM reviews WHERE pr_id = ?`, pr),
			count(t, db.DB, `SELECT count(*) FROM findings WHERE review_id IN (SELECT id FROM reviews WHERE pr_id = ?)`, pr),
			count(t, db.DB, `SELECT count(*) FROM agent_runs WHERE pr_id = ?`, pr),
		}
	}
	if got := counts(); got != [3]int{1, 2, 1} {
		t.Fatalf("after the first start: reviews/findings/runs = %v, want 1/2/1", got)
	}

	type demo struct {
		Verdict, Summary, Model, RunStatus, Grounding, Provider, AgentName string
		Score, Blockers, FindingsCount, TokensIn, TokensOut, DurationMS    int
		Cost                                                               float64
		RunLinked                                                          bool
	}
	var d demo
	if err := db.DB.Raw(`SELECT r.verdict, r.summary, r.model, a.status AS run_status, a.grounding, a.provider, ag.name AS agent_name,
			r.score, a.blockers, a.findings_count, a.tokens_in, a.tokens_out, a.duration_ms, a.cost_usd::float8 AS cost, (r.run_id = a.id) AS run_linked
		FROM reviews r JOIN agent_runs a ON a.pr_id = r.pr_id JOIN agents ag ON ag.id = a.agent_id WHERE r.pr_id = ?`, pr).Scan(&d).Error; err != nil {
		t.Fatal(err)
	}
	want := demo{
		Verdict: "request_changes", Model: "seed", RunStatus: "done", Grounding: "2/2 passed", Provider: "openrouter", AgentName: "General Reviewer",
		Summary: "Solid middleware approach, but a Stripe secret key is committed in plaintext and the user-list endpoint introduces an N+1 query under the new limiter.",
		Score:   61, Blockers: 1, FindingsCount: 2, TokensIn: 6214, TokensOut: 842, DurationMS: 18420, Cost: 0.002135, RunLinked: true,
	}
	if diff := cmp.Diff(want, d); diff != "" {
		t.Errorf("demo review mismatch (-want +got):\n%s", diff)
	}

	restart(t, db)
	if got := counts(); got != [3]int{1, 2, 1} {
		t.Errorf("after a second start: %v, want nothing new (1/2/1)", got)
	}

	// The review alone is deleted: the run is still there, so nothing is re-created.
	if err := db.DB.Exec(`DELETE FROM reviews WHERE pr_id = ?`, pr).Error; err != nil {
		t.Fatal(err)
	}
	restart(t, db)
	if got := counts(); got != [3]int{0, 0, 1} {
		t.Errorf("after deleting the review and restarting: %v, want 0/0/1 (not re-created)", got)
	}

	// The demo run is deleted (the way DELETE /runs/:id does): both come back.
	if err := db.DB.Exec(`DELETE FROM agent_runs WHERE pr_id = ?`, pr).Error; err != nil {
		t.Fatal(err)
	}
	restart(t, db)
	if got := counts(); got != [3]int{1, 2, 1} {
		t.Errorf("after deleting the demo run and restarting: %v, want the demo review and run written again (1/2/1)", got)
	}
}

// ---- prompt context: repo intel and project documents ----

// patchOf is a patch that adds n lines.
func patchOf(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@", n)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "\n+line %d", i)
	}
	return b.String()
}

// userPrompt is the user message of a run, from its trace.
func userPrompt(t testing.TB, trace map[string]any) string {
	t.Helper()
	u, _ := trace["prompt_assembly"].(map[string]any)["user"].(string)
	return u
}

// AC4051: an indexed repo with a cached map and 2 of 4 changed files in the top 5%: the task line
// carries the hot-file note and the prompt has the repo skeleton.
func TestReview_RepoIntelAddsTheSkeletonAndTheHotFileNote(t *testing.T) {
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", intelFixture)
	s := startStack(t, spec{remoteDir: remote, config: intelOn, llmAdapters: mockScript().forAll()})
	repo := addClonedRepo(t, s, "o", "r")
	s.waitJobs("repo-intel-index", 1)
	if st := s.get("/repos/" + repo + "/index-state").Obj(t); st["status"] != "full" {
		t.Fatalf("index state = %v, want full", st)
	}
	// Two of the four changed files are in the top 5%; the index left the repo map in its cache.
	if err := s.db.DB.Exec(`UPDATE file_rank SET percentile = CASE WHEN file_path IN ('src/util.ts', 'src/core.ts') THEN 99 ELSE 40 END WHERE repo_id = ?`, repo).Error; err != nil {
		t.Fatal(err)
	}
	f := newFx(t, s)
	pr := f.pr(repo, 920, map[string]string{
		"src/util.ts": patchOf(3), "src/core.ts": patchOf(3), "src/a.ts": patchOf(3), "src/b.ts": patchOf(3),
	})
	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	waitStatus(t, s, runs[0], "done")
	trace := waitTrace(t, s, runs[0]).Obj(t)

	user := userPrompt(t, trace)
	const note = "\n\n2 of 4 changed file(s) are in the top 5% most-depended-on (high blast risk) — prioritise their correctness."
	if !strings.Contains(user, note) {
		t.Errorf("the user message lacks the hot-file note %q:\n%s", note, user)
	}
	if !strings.Contains(user, "## Repo skeleton\n") {
		t.Errorf("the user message has no ## Repo skeleton section:\n%s", user)
	}
	var logged []string
	for _, l := range trace["log"].([]any) {
		logged = append(logged, l.(map[string]any)["msg"].(string))
	}
	if !slices.Contains(logged, "file rank: 2/4 changed file(s) in top 5%") {
		t.Errorf("log = %q, want the file rank line", logged)
	}
	mapLine := regexp.MustCompile(`^repo map: \d+ token\(s\) attached \(cached=true\)$`)
	if !slices.ContainsFunc(logged, mapLine.MatchString) {
		t.Errorf("log = %q, want a repo map line", logged)
	}
}

// projectContextFixture is the repo o/r with a document of 300,000 bytes in its base commit, a
// pull request over it and an agent and a skill with attached paths.
type projectContextFixture struct {
	s                   *stack
	repo, pr            string
	agent, skill        string
	store, big, missing string
}

func newProjectContextFixture(t *testing.T, sp spec) projectContextFixture {
	t.Helper()
	files := map[string]string{"docs/d2.md": "# Big\n" + strings.Repeat("x", 300_000) + "\n", "docs/other.md": "# Other\n"}
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", files)
	sp.remoteDir = remote
	sp.llmAdapters = mockScript().forAll()
	s := startStack(t, sp)
	repo := addClonedRepo(t, s, "o", "r")
	fx := projectContextFixture{s: s, repo: repo, store: ".devdigest/specs/d1.md", big: "docs/d2.md", missing: "docs/d3.md"}

	if resp := s.post("/repos/"+repo+"/context/files", map[string]any{"path": fx.store, "content": "# Spec\n\nUse the rate limiter.\n"}); resp.Status != http.StatusCreated {
		t.Fatalf("create store file = %d %s", resp.Status, resp.Body)
	}
	fx.agent = idOf(t, s.post("/agents", map[string]any{"name": "Context Agent", "provider": "openrouter", "model": "deepseek/deepseek-v4-flash", "system_prompt": "Review."}))
	fx.skill = idOf(t, s.post("/skills", map[string]any{"name": "ctx-skill", "type": "custom", "body": "Check the contract.", "description": "Applies to contracts"}))
	if resp := s.post("/agents/"+fx.agent+"/skills", map[string]any{"skill_id": fx.skill}); resp.Status != http.StatusOK {
		t.Fatalf("link skill = %d %s", resp.Status, resp.Body)
	}
	if resp := s.do(http.MethodPut, "/agents/"+fx.agent+"/context", map[string]any{"repo_id": repo, "paths": []string{fx.store, fx.big}}); resp.Status != http.StatusOK {
		t.Fatalf("attach to the agent = %d %s", resp.Status, resp.Body)
	}
	if resp := s.do(http.MethodPut, "/skills/"+fx.skill+"/context", map[string]any{"repo_id": repo, "paths": []string{fx.big, fx.missing}}); resp.Status != http.StatusOK {
		t.Fatalf("attach to the skill = %d %s", resp.Status, resp.Body)
	}
	fx.pr = newFx(t, s).pr(repo, 921, map[string]string{"src/middleware/ratelimit.ts": patchOf(15)})
	return fx
}

// run starts the fixture agent and returns the finished run's trace.
func (p projectContextFixture) run(t *testing.T) map[string]any {
	t.Helper()
	runs, _ := startOK(t, p.s, p.pr, map[string]any{"agentIds": []string{p.agent}})
	waitStatus(t, p.s, runs[0], "done")
	return waitTrace(t, p.s, runs[0]).Obj(t)
}

// AC4053 (EC4037): the documents of a run: the agent's paths, then the skill's, deduplicated; a
// store file from the database, a repo document at the base commit.
func TestReview_ProjectContextDocumentsAndTheirStatuses(t *testing.T) {
	p := newProjectContextFixture(t, spec{})
	trace := p.run(t)

	pc, _ := trace["project_context"].(map[string]any)
	if pc == nil {
		t.Fatalf("trace has no project_context: %v", trace)
	}
	if pc["budget_tokens"] != float64(16000) {
		t.Errorf("budget_tokens = %v, want 16000", pc["budget_tokens"])
	}
	docs := pc["docs"].([]any)
	if len(docs) != 3 {
		t.Fatalf("docs = %v, want 3 (d1, d2, d3)", docs)
	}
	type origin = map[string]any
	for i, want := range []struct {
		path, status, source string
		origin               origin
		text                 bool
	}{
		{p.store, "included", "store", origin{"kind": "agent"}, true},
		{p.big, "too_large", "repo", origin{"kind": "agent"}, false},
		{p.missing, "missing", "repo", origin{"kind": "skill", "skill_id": p.skill, "skill_name": "ctx-skill"}, false},
	} {
		d := docs[i].(map[string]any)
		_, hasText := d["text"]
		if d["path"] != want.path || d["status"] != want.status || d["source"] != want.source || hasText != want.text ||
			cmp.Diff(want.origin, d["origin"]) != "" {
			t.Errorf("docs[%d] = %v, want %s %s (%s) origin %v text=%v", i, d, want.path, want.status, want.source, want.origin, want.text)
		}
	}
	tokens := int(pc["tokens_total"].(float64))
	wantLine := fmt.Sprintf("project context: 1 included, 2 skipped · ~%d tokens", tokens)
	var logged []string
	for _, l := range trace["log"].([]any) {
		logged = append(logged, l.(map[string]any)["msg"].(string))
	}
	if !slices.Contains(logged, wantLine) {
		t.Errorf("log = %q, want it to hold %q", logged, wantLine)
	}
	if diff := cmp.Diff([]any{p.store}, trace["specs_read"]); diff != "" {
		t.Errorf("specs_read mismatch (-want +got):\n%s", diff)
	}
	if user := userPrompt(t, trace); !strings.Contains(user, "## Project context\n") || !strings.Contains(user, "Use the rate limiter.") {
		t.Errorf("the user message lacks the project context section with the store file:\n%s", user)
	}
}
