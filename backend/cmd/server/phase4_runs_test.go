package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/platform/runbus"
	"dev-digest-go/backend/internal/reviews/adapters/peers"
	"dev-digest-go/backend/internal/reviews/app"
	reviewshttp "dev-digest-go/backend/internal/reviews/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

// Phase 4 component tests, part 2: run events over SSE, the run lifecycle with scripted providers
// (failures, cancels, a deleted run), restart and shutdown. The routes and goldens are in
// phase4_test.go.

// ---- helpers ----

// answerJSON is a valid Review answer without findings.
const answerJSON = `{"verdict":"comment","summary":"s","score":80,"findings":[]}`

// answer is an attempt result with usage and, when cost >= 0, a provider-reported cost.
func answer(raw string, in, out int, cost float64) llm.AttemptResult {
	r := llm.AttemptResult{Raw: raw, Usage: &llm.Usage{TokensIn: in, TokensOut: out}}
	if cost >= 0 {
		r.CostUSD = &cost
	}
	return r
}

// blockUntilDone is a scripted call that waits for its context to end and fails with its error.
func blockUntilDone(ctx context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
	<-ctx.Done()
	return llm.AttemptResult{}, ctx.Err()
}

// openAIKey is the stored OpenAI key of the privacy test.
const openAIKey = "sk-test-AAAA"

// eventMsgs returns the messages of the events of a finished run's replay.
func eventMsgs(t testing.TB, s *stack, run string) []sseEvent {
	t.Helper()
	c := follow(t, s, "/runs/"+run+"/events")
	return events(t, c.Wait(t, 30*time.Second))
}

// newAgent creates an agent through the API and returns its id.
func newAgent(t testing.TB, s *stack, body map[string]any) string {
	t.Helper()
	b := map[string]any{"name": "Agent", "provider": "openrouter", "model": "deepseek/deepseek-v4-flash", "system_prompt": "Review the diff."}
	for k, v := range body {
		b[k] = v
	}
	resp := s.post("/agents", b)
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /agents %v = %d %s, want 201", body, resp.Status, resp.Body)
	}
	return idOf(t, resp)
}

// newSkill creates a skill through the API and returns its id.
func newSkill(t testing.TB, s *stack, name, body string) string {
	t.Helper()
	resp := s.post("/skills", map[string]any{"name": name, "type": "custom", "body": body, "description": "Applies to " + name})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /skills %s = %d %s, want 201", name, resp.Status, resp.Body)
	}
	return idOf(t, resp)
}

func linkSkill(t testing.TB, s *stack, agent, skill string) {
	t.Helper()
	if resp := s.post("/agents/"+agent+"/skills", map[string]any{"skill_id": skill}); resp.Status != http.StatusOK {
		t.Fatalf("link skill = %d %s, want 200", resp.Status, resp.Body)
	}
}

// ---- SSE ----

// AC4017 (EC4026, NFR4002, AC4076): the stream of a running run, followed by a client that joined
// at once and one that joined after the first events: both get events 1..n exactly once, framed
// as fastify-sse-v2 frames them, and both end with the run.
func TestEvents_StreamOfARunningRun(t *testing.T) {
	g := newGate()
	s := startStack(t, spec{llmAdapters: g.forAll()})
	pr := pr482(t, s)
	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	run := runs[0]

	early := follow(t, s, "/runs/"+run+"/events", "Origin", "http://localhost:3100")
	eventually(t, 30*time.Second, "the run to reach its LLM call", func() bool { return g.calls() == 1 })
	eventually(t, 10*time.Second, "10 events to be buffered", func() bool {
		return strings.Count(early.Body(), "\nevent: ") >= 9
	})
	late := follow(t, s, "/runs/"+run+"/events")
	if late.Ended() || early.Ended() {
		t.Fatal("a stream ended while the run still waits in its LLM call")
	}
	g.Open()
	earlyBody, lateBody := early.Wait(t, 30*time.Second), late.Wait(t, 30*time.Second)

	for name, c := range map[string]*capture{"early": early, "late": late} {
		if c.Status != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", name, c.Status)
		}
		for h, want := range map[string]string{
			"Content-Type": "text/event-stream; charset=utf-8", "Connection": "keep-alive",
			"Cache-Control": "no-cache,no-transform", "X-No-Compression": "1",
			"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer",
		} {
			if got := c.Header.Get(h); got != want {
				t.Errorf("%s: header %s = %q, want %q", name, h, got, want)
			}
		}
		if cl := c.Header.Get("Content-Length"); cl != "" {
			t.Errorf("%s: Content-Length = %q, want none", name, cl)
		}
	}
	if got := early.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3100" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the allowed origin", got)
	}
	if lateBody != earlyBody {
		t.Errorf("the late client's stream differs from the early one's:\n%s\n---\n%s", lateBody, earlyBody)
	}

	evs := events(t, earlyBody)
	if len(evs) < 12 {
		t.Fatalf("events = %q, want the whole run", msgs(evs))
	}
	for i, ev := range evs {
		if ev.Seq != i+1 || ev.RunID != run || !regexp.MustCompile(`^\d\d:\d\d:\d\d$`).MatchString(ev.T) {
			t.Errorf("event %d = %+v, want seq %d, the run id and an HH:MM:SS stamp", i, ev, i+1)
		}
		if bytes.Contains(ev.Data, []byte(`"status"`)) {
			t.Errorf("a live event carries a status: %s", ev.Data)
		}
	}
	tail := msgs(evs[len(evs)-2:])
	if !strings.HasPrefix(tail[0], "Persisted review ") || tail[1] != "Run complete; trace persisted" {
		t.Errorf("last two events = %q, want Persisted review … and Run complete; trace persisted", tail)
	}

	// Every data line is what JSON.stringify makes of the event: compact, no HTML escaping.
	for _, f := range frames(t, earlyBody) {
		var ev sseEvent
		if err := json.Unmarshal([]byte(f.Data), &ev); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		type wire struct {
			RunID string          `json:"runId"`
			Seq   int             `json:"seq"`
			Kind  string          `json:"kind"`
			Msg   string          `json:"msg"`
			T     string          `json:"t"`
			Data  json.RawMessage `json:"data,omitempty"`
		}
		if err := enc.Encode(wire(ev)); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSuffix(buf.String(), "\n"); got != f.Data {
			t.Errorf("event %d data = %s, want the JSON.stringify form %s", ev.Seq, f.Data, got)
		}
	}

	// The headers and the framing are those of the Node recording (fastify-sse-v2 4.2.2).
	raw, err := os.ReadFile("../../internal/reviews/parity/testdata/goldens/sse.json")
	if err != nil {
		t.Fatal(err)
	}
	var node struct {
		Streams []struct {
			Response struct {
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"response"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	nodeStream := node.Streams[0].Response
	for _, h := range []string{"content-type", "connection", "cache-control", "x-no-compression"} {
		if got, want := early.Header.Get(h), nodeStream.Headers[h]; got != want {
			t.Errorf("header %s = %q, Node sent %q", h, got, want)
		}
	}
	if !strings.HasPrefix(nodeStream.Body, "retry: 3000\n\nid: 1\nevent: tool\ndata: {\"runId\":") || !strings.HasPrefix(earlyBody, "retry: 3000\n\nid: 1\nevent: tool\ndata: {\"runId\":") {
		t.Errorf("the first frames differ from the Node recording's shape:\nnode: %.120q\ngo:   %.120q", nodeStream.Body, earlyBody)
	}
}

// AC4018: a finished run the bus does not hold yields one synthetic event.
func TestEvents_FinishedRunsGetOneSyntheticEvent(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	agent := f.agent("Finished")
	pr := f.pr(repo, 930, nil)
	done := f.run(runRow{pr: pr, agent: agent, status: "done"})
	boom := f.run(runRow{pr: pr, agent: agent, status: "failed", errText: ptr("boom")})
	reaped := f.run(runRow{pr: pr, agent: agent, status: "failed"})
	cancelled := f.run(runRow{pr: pr, agent: agent, status: "cancelled", errText: ptr("Cancelled by user")})

	for _, tc := range []struct {
		name, run, kind, msg, data string
	}{
		{"done", done, "result", "Run finished", `{"status":"done","error":null}`},
		{"failed with an error", boom, "info", "Run failed: boom", `{"status":"failed","error":"boom"}`},
		{"failed by the reap, no error", reaped, "info", "Run failed", `{"status":"failed","error":null}`},
		{"cancelled", cancelled, "info", "Run cancelled: Cancelled by user", `{"status":"cancelled","error":"Cancelled by user"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := follow(t, s, "/runs/"+tc.run+"/events")
			evs := events(t, c.Wait(t, 10*time.Second))
			if len(evs) != 1 {
				t.Fatalf("events = %+v, want exactly one", evs)
			}
			ev := evs[0]
			if ev.Seq != 1 || ev.Kind != tc.kind || ev.Msg != tc.msg || string(ev.Data) != tc.data || ev.RunID != tc.run {
				t.Errorf("event = %+v (data %s), want seq 1 %s %q with data %s", ev, ev.Data, tc.kind, tc.msg, tc.data)
			}
		})
	}
}

// AC4019: an unknown run, a run of another workspace and a malformed id are refused before any
// stream is opened.
func TestEvents_RefusalsAreJSONBeforeTheStream(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	other := scalar[string](t, s.db.DB, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	foreign := scalar[string](t, s.db.DB, `INSERT INTO agent_runs (workspace_id, status) VALUES (?, 'running') RETURNING id::text`, other)

	for _, id := range []string{unknownID, foreign} {
		resp := s.get("/runs/" + id + "/events")
		if resp.Status != http.StatusNotFound || message(t, resp) != "Run not found" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("GET events of %s = %d %q (%s), want 404 Run not found as JSON", id, resp.Status, resp.Body, resp.Header.Get("Content-Type"))
		}
	}
	resp := s.get("/runs/not-a-uuid/events")
	if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "validation_error" {
		t.Errorf("GET events of a malformed id = %d %s, want 422 validation_error", resp.Status, resp.Body)
	}
}

// AC4022 (EC4028): a run that is running in the database but has published nothing yet: the stream
// opens, waits for the first event and ends with the run. (Only the cancel route can publish for
// a run no executor owns, so the one event is its.)
func TestEvents_RunNotYetOnTheBusWaitsForItsFirstEvent(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := newFx(t, s)
	repo := repoByName(t, s, "acme/payments-api")
	pr := f.pr(repo, 931, nil)
	run := f.run(runRow{pr: pr, agent: f.agent("Waiting"), status: "running"})

	c := follow(t, s, "/runs/"+run+"/events")
	if c.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", c.Status)
	}
	eventually(t, 5*time.Second, "the preamble", func() bool { return c.Body() == "retry: 3000\n\n" })
	time.Sleep(300 * time.Millisecond) // nothing may arrive, and the stream stays open
	if c.Ended() || c.Body() != "retry: 3000\n\n" {
		t.Fatalf("stream after 300 ms = %q (ended %v), want the preamble only and still open", c.Body(), c.Ended())
	}
	if resp := s.post("/runs/"+run+"/cancel", nil); resp.Status != http.StatusOK {
		t.Fatalf("cancel = %d %s", resp.Status, resp.Body)
	}
	evs := events(t, c.Wait(t, 10*time.Second))
	if diff := cmp.Diff([]string{"Cancellation requested — stopping…"}, msgs(evs)); diff != "" {
		t.Errorf("events mismatch (-want +got):\n%s", diff)
	}
	if got := runStatus(t, s, run); got != "cancelled" {
		t.Errorf("run status = %s, want cancelled", got)
	}
}

// busAPI serves the event stream of a real run bus; every other method is absent.
type busAPI struct {
	app.API
	bus *peers.Bus
}

func (b busAPI) Events(_ context.Context, _, runID string) (app.Subscription, error) {
	return b.bus.Subscribe(runID), nil
}

type oneWorkspace struct{}

func (oneWorkspace) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: "11111111-2222-4333-8444-555555555555"}, nil
}

// stallListener wraps the connections the server accepts. arm makes the next accepted connection
// a stalled one: after budget written bytes its Write blocks until release, which is what a client
// that stopped reading looks like to the server once the kernel buffers are full. The kernel
// buffers themselves are left alone: their size and TCP's window probing vary with machine load,
// which made the cut-off of a real stalled socket depend on timing.
type stallListener struct {
	net.Listener
	mu   sync.Mutex
	next *stall
}

func (l *stallListener) arm(budget int) *stall {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next = &stall{budget: budget, release: make(chan struct{})}
	return l.next
}

func (l *stallListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.next == nil {
		return c, nil
	}
	st := l.next
	l.next = nil
	return &stalledConn{Conn: c, st: st}, nil
}

type stall struct {
	budget  int
	release chan struct{}
	once    sync.Once
}

// Release lets the blocked and all later writes through; it is safe to call more than once.
func (s *stall) Release() { s.once.Do(func() { close(s.release) }) }

type stalledConn struct {
	net.Conn
	st      *stall
	mu      sync.Mutex
	written int
}

// Write passes writes through until the budget is used up, then blocks until Release or Close.
func (c *stalledConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	over := c.written >= c.st.budget
	c.written += len(p)
	c.mu.Unlock()
	if over {
		select {
		case <-c.st.release:
		case <-time.After(time.Minute): // a test that never released must not hang the server
			return 0, os.ErrDeadlineExceeded
		}
	}
	return c.Conn.Write(p)
}

// SetWriteDeadline is a no-op: the stream arms a per-frame deadline that a long stall in the test
// would otherwise turn into a failed write instead of a cut-off subscriber.
func (c *stalledConn) SetWriteDeadline(time.Time) error { return nil }

// AC4021 (EC4027, NFR4011): over a real socket and a real bus, a disconnected client releases its
// subscription within a second, and a client that reads nothing is cut off after 1,024 queued
// events while publishing never waits for it. The stalled client is simulated at the server's end
// of the socket (stallListener), so the handler really blocks in a write and the pending count
// provably backs up, whatever the kernel's buffers or the machine's load.
func TestEvents_DisconnectAndSlowSubscribers(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	e := httpx.New(httpx.Options{})
	reviewshttp.AddRoutes(e, busAPI{bus: peers.NewBus(bus)}, oneWorkspace{})
	srv := httptest.NewUnstartedServer(e)
	stalls := &stallListener{Listener: srv.Listener}
	srv.Listener = stalls
	srv.Start()
	t.Cleanup(srv.Close)

	connect := func(run string) net.Conn {
		t.Helper()
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		fmt.Fprintf(conn, "GET /runs/%s/events HTTP/1.1\r\nHost: localhost\r\n\r\n", run)
		return conn
	}

	t.Run("a disconnected client", func(t *testing.T) {
		const run = "7e1d0000-0000-4000-8000-0000000000a1"
		conn := connect(run)
		eventually(t, 5*time.Second, "one subscriber", func() bool { return bus.Subscribers(run) == 1 })
		bus.Publish(run, "info", "first", nil)
		_ = conn.Close()
		start := time.Now()
		eventually(t, time.Second, "the subscription to be released", func() bool {
			bus.Publish(run, "info", "keeps coming", nil) // a closed socket is noticed on a write
			return bus.Subscribers(run) == 0
		})
		t.Logf("released after %s", time.Since(start))
		started := time.Now()
		for range 100 {
			bus.Publish(run, "info", "no one listens", nil)
		}
		if took := time.Since(started); took > time.Second {
			t.Errorf("100 publishes with no subscriber took %s", took)
		}
	})

	t.Run("a subscriber that reads nothing", func(t *testing.T) {
		const (
			run       = "7e1d0000-0000-4000-8000-0000000000a2"
			published = 1100
			backlog   = 1024
			budget    = 16 << 10 // the bytes the server writes before the client "stops reading"
		)
		st := stalls.arm(budget)
		t.Cleanup(st.Release)
		conn := connect(run)
		eventually(t, 5*time.Second, "one subscriber", func() bool { return bus.Subscribers(run) == 1 })
		msg := strings.Repeat("x", 1024)
		// Publishing never waits for the stalled client: the loop ends although nothing reads. The
		// guard only turns a hang into a failure; no latency is asserted.
		publishedAll := make(chan struct{})
		go func() {
			defer close(publishedAll)
			for i := range published {
				bus.Publish(run, "info", fmt.Sprintf("%d %s", i, msg), nil)
			}
		}()
		select {
		case <-publishedAll:
		case <-time.After(time.Minute):
			t.Fatal("Publish did not return while the client was not reading")
		}
		// The handler is blocked in a write with at most budget bytes of frames delivered, so more
		// than the backlog is pending: the cut happens no matter how the machine is loaded.
		eventually(t, 10*time.Second, "the slow subscriber to be dropped", func() bool { return bus.Subscribers(run) == 0 })

		// Reading now drains what was queued and ends the response (chunked, so the end is the
		// terminating chunk; the connection itself stays open).
		st.Release()
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("read the response: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("the stream did not end after %d bytes: %v", len(body), err)
		}
		// The queued 1,024 events, plus the few the server wrote before it blocked (the budget at
		// about 1.1 KB a frame, a flush of up to 4 KB past it, the frame blocked in flight); the rest of the 1,100 was cut.
		const sentBeforeBlocking = budget/1024 + 8
		if n := strings.Count(string(body), "\nevent: info\n"); n < backlog || n > backlog+sentBeforeBlocking {
			t.Errorf("frames delivered = %d, want %d-%d of the %d published", n, backlog, backlog+sentBeforeBlocking, published)
		}
	})
}

// ---- the run lifecycle ----

// AC4025 (FR4017, FR4019, FR4020, FR4029, FR4039): the events of a mock run on PR #482 with one
// enabled and one disabled skill, exactly as the spec lists them, and the rows they leave.
func TestRun_EventsAndRowsOfAMockRun(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	pr := pr482(t, s)
	agent := newAgent(t, s, map[string]any{"name": "Sequence Agent", "strategy": "single-pass", "ci_fail_on": "critical"})
	on, off := newSkill(t, s, "skill-on", "Body of the enabled skill."), newSkill(t, s, "skill-off", "Body of the disabled skill.")
	linkSkill(t, s, agent, on)
	linkSkill(t, s, agent, off)
	if err := s.db.DB.Exec(`UPDATE skills SET enabled = false WHERE id = ?`, off).Error; err != nil {
		t.Fatal(err)
	}

	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	waitStatus(t, s, runs[0], "done")
	evs := eventMsgs(t, s, runs[0])

	type want struct{ kind, msg, data string }
	ms := `\(\d+ms\)`
	wants := []want{
		{"tool", "Loading PR diff…", ""},
		{"tool", "^Loading PR diff done " + ms + "$", ""},
		{"info", "Diff ready — 4 changed file(s); starting 1 agent run(s)", ""},
		{"info", `Starting review with agent "Sequence Agent" (openrouter/deepseek/deepseek-v4-flash)`, ""},
		{"info", `^skills: 1 attached \(skill-on v1\) · ~\d+ tokens$`, `^\{"skills":\[\{"id":"` + on + `","name":"skill-on","version":1\}\]\}$`},
		{"tool", "Resolving openrouter provider…", ""},
		{"tool", "^Resolving openrouter provider done " + ms + "$", ""},
		{"info", "Reviewing 4 changed file(s) in one pass", ""},
		{"tool", "Reviewing all files in one pass", `^\{"file":"all files"\}$`},
		{"result", "all files: 2 candidate finding(s)", ""},
		{"result", "Reduced to 2 finding(s); verdict=comment, score=70", ""},
		{"result", "Citation grounding: 2/2 passed", ""},
		{"result", `^Persisted review [0-9a-f-]{36} with 2 finding\(s\)$`, ""},
		{"info", "Run complete; trace persisted", ""},
	}
	if len(evs) != len(wants) {
		t.Fatalf("events = %q, want %d events", msgs(evs), len(wants))
	}
	for i, w := range wants {
		ev := evs[i]
		msgOK := ev.Msg == w.msg || (strings.HasPrefix(w.msg, "^") && regexp.MustCompile(w.msg).MatchString(ev.Msg))
		dataOK := (w.data == "" && len(ev.Data) == 0) || (w.data != "" && regexp.MustCompile(w.data).Match(ev.Data))
		if ev.Kind != w.kind || !msgOK || !dataOK || ev.Seq != i+1 {
			t.Errorf("event %d = %s %q data %s, want %s %q data %q", i+1, ev.Kind, ev.Msg, ev.Data, w.kind, w.msg, w.data)
		}
	}

	type row struct {
		Status, Grounding, Source            string
		Score, Blockers, TokensIn, TokensOut int
		FindingsCount                        int
		Cost                                 float64
		SkillRows                            int
		SkillOrder                           int
		SkillVersion                         int
		LastReviewedSHA, HeadSHA             string
	}
	var r row
	if err := s.db.DB.Raw(`SELECT a.status, a.grounding, a.source, a.score, a.blockers, a.tokens_in, a.tokens_out, a.findings_count, a.cost_usd::float8 AS cost,
			(SELECT count(*) FROM agent_run_skills WHERE run_id = a.id) AS skill_rows,
			(SELECT "order" FROM agent_run_skills WHERE run_id = a.id) AS skill_order,
			(SELECT skill_version FROM agent_run_skills WHERE run_id = a.id) AS skill_version,
			p.last_reviewed_sha, p.head_sha
		FROM agent_runs a JOIN pull_requests p ON p.id = a.pr_id WHERE a.id = ?`, runs[0]).Scan(&r).Error; err != nil {
		t.Fatal(err)
	}
	wantRow := row{Status: "done", Grounding: "2/2 passed", Source: "local", Score: 85, Blockers: 0, TokensIn: 1200, TokensOut: 180,
		FindingsCount: 2, Cost: 0.00042, SkillRows: 1, SkillOrder: 0, SkillVersion: 1, LastReviewedSHA: "a1b2c3d4e5f6", HeadSHA: "a1b2c3d4e5f6"}
	if diff := cmp.Diff(wantRow, r); diff != "" {
		t.Errorf("run row mismatch (-want +got):\n%s", diff)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM reviews WHERE run_id = ?`, runs[0]); n != 1 {
		t.Errorf("reviews of the run = %d, want 1", n)
	}
	var skillRow string
	if err := s.db.DB.Raw(`SELECT skill_id::text FROM agent_run_skills WHERE run_id = ?`, runs[0]).Scan(&skillRow).Error; err != nil || skillRow != on {
		t.Errorf("agent_run_skills skill = %q (%v), want the enabled skill %s", skillRow, err, on)
	}
}

// AC4026 (FR4018, FR4017, EC4017): 6 agents, 2 at a time; every run has the shared pre-work
// events; the run whose provider has no key fails and the others go on.
func TestRun_AgentConcurrencyIsBoundedAndAFailureDoesNotStopTheOthers(t *testing.T) {
	delayed := &scripted{complete: func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
		select {
		case <-time.After(300 * time.Millisecond):
			return mockAnswer(ctx, at)
		case <-ctx.Done():
			return llm.AttemptResult{}, ctx.Err()
		}
	}}
	keyed := &scripted{needsKey: true, complete: delayed.complete} // the openai provider, whose key is missing
	adapters := map[llm.Provider]llm.Adapter{llm.OpenRouter: delayed, llm.OpenAI: keyed, llm.Anthropic: delayed}
	s := startStack(t, spec{llmAdapters: adapters, config: func(c *config.Config) { c.ReviewAgentConcurrency = 2 }})
	pr := pr482(t, s)
	newAgent(t, s, map[string]any{"name": "Extra Reviewer"})
	broken := newAgent(t, s, map[string]any{"name": "Keyless Reviewer", "provider": "openai", "model": "gpt-5.4"})

	runs, multi := startOK(t, s, pr, map[string]any{"all": true})
	if len(runs) != 6 || multi != nil {
		t.Fatalf("all:true = %d runs, multi %v, want 6 runs and no parent", len(runs), multi)
	}
	for _, r := range runs {
		waitStatus(t, s, r, "done", "failed")
	}
	if got := delayed.maxInFlight(); got != 2 {
		t.Errorf("most runs inside an LLM call at once = %d, want 2 (REVIEW_AGENT_CONCURRENCY)", got)
	}
	if got := keyed.calls(); got != 0 {
		t.Errorf("the keyless provider received %d calls, want 0", got)
	}
	var failed []string
	for _, r := range runs {
		if st := runStatus(t, s, r); st == "failed" {
			failed = append(failed, r)
		} else if st != "done" {
			t.Errorf("run %s = %s, want done", r, st)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("failed runs = %v, want the one of the keyless agent (%s)", failed, broken)
	}
	if e := scalar[string](t, s.db.DB, `SELECT error FROM agent_runs WHERE id = ?`, failed[0]); e != "OPENAI_API_KEY is not configured" {
		t.Errorf("failed run error = %q, want OPENAI_API_KEY is not configured", e)
	}
	for _, r := range runs {
		evs := eventMsgs(t, s, r)
		got := msgs(evs[:3])
		want := []string{"Loading PR diff…", "", "Diff ready — 4 changed file(s); starting 6 agent run(s)"}
		if got[0] != want[0] || !strings.HasPrefix(got[1], "Loading PR diff done (") || got[2] != want[2] {
			t.Errorf("run %s starts with %q, want the shared pre-work events", r, got)
		}
	}
}

// AC4027 (FR4017, FR4047): when the stored files cannot be read, every queued run fails with the
// diff note, zero usage, an ended trace and a finished stream.
func TestRun_DiffLoadFailureFailsEveryRun(t *testing.T) {
	g := mockScript()
	s := startStack(t, spec{llmAdapters: g.forAll()})
	pr := pr482(t, s)
	if err := s.db.DB.Exec(`ALTER TABLE pr_files RENAME TO pr_files_gone`).Error; err != nil {
		t.Fatal(err)
	}
	runs, multi := startOK(t, s, pr, map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral), agentByName(t, s, agentSecurity)}})
	if len(runs) != 2 || multi == nil {
		t.Fatalf("runs = %v, multi = %v, want 2 runs and a parent", runs, multi)
	}
	for _, r := range runs {
		waitStatus(t, s, r, "failed")
		row := struct {
			Error             string
			TokensIn, Tokens2 int
			Cost              float64
			Findings          int
			Grounding         string
		}{}
		if err := s.db.DB.Raw(`SELECT error, tokens_in, tokens_out AS tokens2, cost_usd::float8 AS cost, findings_count AS findings, grounding FROM agent_runs WHERE id = ?`, r).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(row.Error, "Failed to load PR diff: ") || !strings.Contains(row.Error, "pr_files") || row.TokensIn != 0 || row.Tokens2 != 0 || row.Cost != 0 || row.Findings != 0 || row.Grounding != "0/0 passed" {
			t.Errorf("run row = %+v, want Failed to load PR diff: …pr_files…, zero usage, 0/0 passed", row)
		}
		trace := waitTrace(t, s, r).Obj(t)
		stats := trace["stats"].(map[string]any)
		if stats["tokens_in"] != float64(0) || stats["cost_usd"] != float64(0) || stats["grounding"] != "0/0 passed" || stats["duration_ms"] != float64(0) {
			t.Errorf("ended trace stats = %v, want zero usage and duration 0", stats)
		}
		evs := eventMsgs(t, s, r) // the stream ends: Wait inside returns
		last := evs[len(evs)-1]
		if last.Kind != "error" || !strings.HasPrefix(last.Msg, "Failed to load PR diff: ") {
			t.Errorf("last event = %s %q, want the error event with the diff note", last.Kind, last.Msg)
		}
		if len(evs) < 3 || evs[1].Kind != "error" || !strings.HasPrefix(evs[1].Msg, "Loading PR diff failed (") {
			t.Errorf("events = %q, want the step failure before the note", msgs(evs))
		}
	}
	if g.calls() != 0 {
		t.Errorf("LLM calls = %d, want 0", g.calls())
	}
}

// AC4029 (FR4038, EC4017): without the provider's key (and no mock) the run fails at provider
// resolution; no request reaches any provider.
func TestRun_MissingKeyFailsBeforeAnyProviderRequest(t *testing.T) {
	stub := newLLMStub(t, openAIError(500, "must not be called"))
	s := startStack(t, spec{llmURLs: llmURLs{openAI: stub.URL(), anthropic: stub.URL(), openRouter: stub.URL()}, llm: fastLLM()})
	pr := pr482(t, s)
	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	waitStatus(t, s, runs[0], "failed")

	evs := eventMsgs(t, s, runs[0])
	var sawStep, sawRun bool
	for _, ev := range evs {
		if ev.Kind == "error" && regexp.MustCompile(`^Resolving openrouter provider failed \(\d+ms\): OPENROUTER_API_KEY is not configured$`).MatchString(ev.Msg) {
			sawStep = true
		}
		if ev.Kind == "error" && ev.Msg == "Run failed: OPENROUTER_API_KEY is not configured" {
			sawRun = true
		}
	}
	if !sawStep || !sawRun {
		t.Errorf("events = %q, want the step failure and Run failed: OPENROUTER_API_KEY is not configured", msgs(evs))
	}
	row := struct {
		Error    string
		Cost     float64
		TokensIn int
	}{}
	if err := s.db.DB.Raw(`SELECT error, cost_usd::float8 AS cost, tokens_in FROM agent_runs WHERE id = ?`, runs[0]).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Error != "OPENROUTER_API_KEY is not configured" || row.Cost != 0 || row.TokensIn != 0 {
		t.Errorf("run row = %+v, want the key note and zero usage", row)
	}
	if n := stub.total(); n != 0 {
		t.Errorf("requests that reached a provider = %d, want 0", n)
	}
}

// openAIAgent makes the General Reviewer an OpenAI agent on a model the price book does not know.
func openAIAgent(t testing.TB, s *stack) string {
	t.Helper()
	id := agentByName(t, s, agentGeneral)
	if err := s.db.DB.Exec(`UPDATE agents SET provider = 'openai', model = 'scripted/unpriced-model' WHERE id = ?`, id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// AC4030 (FR4021, FR4036, EC4018): three schema-invalid answers fail the run; the usage of all
// three attempts is recorded, and an unpriced attempt makes the cost unknown.
func TestRun_SchemaFailureRecordsEveryAttempt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		costs    []float64 // per attempt, -1 = unpriced
		wantCost any
	}{
		{"every attempt priced", []float64{0.001, 0.001, 0.001}, 0.003},
		{"one attempt unpriced", []float64{0.001, -1, 0.001}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			ad := &scripted{needsKey: true, complete: func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
				i := int(n.Add(1)) - 1
				return answer(`{"verdict":"nope"}`, 100, 10, tc.costs[i%3]), nil
			}}
			s := startStack(t, spec{llmAdapters: ad.forAll(), llm: fastLLM(), env: map[string]string{"OPENAI_API_KEY": openAIKey}})
			agent := openAIAgent(t, s)
			runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentIds": []string{agent}})
			waitStatus(t, s, runs[0], "failed")

			var sum map[string]any
			for _, r := range list(t, s.get("/pulls/"+pr482(t, s)+"/runs")) {
				if r["run_id"] == runs[0] {
					sum = r
				}
			}
			errText, _ := sum["error"].(string)
			if !strings.HasPrefix(errText, "OpenAI structured output failed schema validation for Review after 3 attempt(s).") {
				t.Errorf("error = %q, want the schema failure after 3 attempts", errText)
			}
			if sum["tokens_in"] != float64(300) || sum["tokens_out"] != float64(30) || sum["cost_usd"] != tc.wantCost {
				t.Errorf("usage = %v/%v cost %v, want 300/30 cost %v", sum["tokens_in"], sum["tokens_out"], sum["cost_usd"], tc.wantCost)
			}
			if ad.calls() != 3 {
				t.Errorf("provider calls = %d, want 3", ad.calls())
			}
		})
	}
}

// threeFiles is a pull request of three files that map-reduce reviews one call each.
func threeFiles(t *testing.T, s *stack, number int) string {
	t.Helper()
	repo := repoByName(t, s, "acme/payments-api")
	return newFx(t, s).pr(repo, number, map[string]string{"src/a.ts": patchOf(3), "src/b.ts": patchOf(3), "src/c.ts": patchOf(3)})
}

// mapReduceAgent creates an agent with the map-reduce strategy.
func mapReduceAgent(t *testing.T, s *stack) string {
	t.Helper()
	return newAgent(t, s, map[string]any{"name": "Map Reducer", "strategy": "map-reduce"})
}

// AC4031 (FR4025, EC4019): the second of three chunks fails with concurrency 1: the third is never
// requested, the run fails with the second's error and keeps the first call's usage.
func TestRun_MapReduceStopsAfterTheFirstFailure(t *testing.T) {
	var n atomic.Int32
	ad := &scripted{complete: func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
		if n.Add(1) == 1 {
			return answer(answerJSON, 100, 10, 0.001), nil
		}
		return llm.AttemptResult{}, errors.New("provider exploded on the second file")
	}}
	s := startStack(t, spec{llmAdapters: ad.forAll(), llm: fastLLM(), config: func(c *config.Config) { c.ReviewMapConcurrency = 1 }})
	pr := threeFiles(t, s, 940)
	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{mapReduceAgent(t, s)}})
	waitStatus(t, s, runs[0], "failed")

	if ad.calls() != 2 {
		t.Fatalf("provider calls = %d, want 2 (the third file is never requested)", ad.calls())
	}
	lastPrompt := ad.attempt(1).Messages[len(ad.attempt(1).Messages)-1].Content
	if !strings.Contains(lastPrompt, "src/b.ts") || strings.Contains(lastPrompt, "src/a.ts") || strings.Contains(lastPrompt, "src/c.ts") {
		t.Errorf("the failing call's prompt is not file 2's:\n%s", lastPrompt)
	}
	row := struct {
		Error             string
		TokensIn, TokensO int
		Cost              float64
	}{}
	if err := s.db.DB.Raw(`SELECT error, tokens_in, tokens_out AS tokens_o, cost_usd::float8 AS cost FROM agent_runs WHERE id = ?`, runs[0]).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(row.Error, "provider exploded on the second file") || row.TokensIn != 100 || row.TokensO != 10 || row.Cost != 0.001 {
		t.Errorf("run row = %+v, want the second file's error and file 1's usage (100/10, 0.001)", row)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM reviews WHERE run_id = ?`, runs[0]); n != 0 {
		t.Errorf("reviews of the failed run = %d, want 0", n)
	}
}

// cancelFromOutside posts the cancel route from a goroutine that is not the test's; it never fails
// the test itself.
func cancelFromOutside(base, run string) error {
	req, err := http.NewRequest(http.MethodPost, base+"/runs/"+run+"/cancel", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cancel answered %d", resp.StatusCode)
	}
	return nil
}

// AC4032 (FR4006, FR4022, FR4021, FR4047, EC4023): cancelling a run inside an LLM call ends the
// call's context at once; the run is cancelled with the user note, even though the aborted call
// returned an error, and leaves an ended trace.
func TestRun_CancelAbortsTheCallAndKeepsTheCancelledNote(t *testing.T) {
	cancelledAt := make(chan time.Time, 1)
	ad := &scripted{complete: func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
		<-ctx.Done()
		cancelledAt <- time.Now()
		return llm.AttemptResult{}, errors.New("aborted: " + ctx.Err().Error())
	}}
	s := startStack(t, spec{llmAdapters: ad.forAll(), llm: fastLLM()})
	pr := pr482(t, s)
	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	eventually(t, 30*time.Second, "the run to reach its LLM call", func() bool { return ad.calls() == 1 })

	sent := time.Now()
	resp := s.post("/runs/"+runs[0]+"/cancel", nil)
	if resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != `{"ok":true}` {
		t.Fatalf("cancel = %d %s, want {\"ok\":true}", resp.Status, resp.Body)
	}
	select {
	case at := <-cancelledAt:
		if d := at.Sub(sent); d > 100*time.Millisecond {
			t.Errorf("the call's context ended %s after the cancel request, want within 100 ms", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call's context did not end")
	}

	trace := waitTrace(t, s, runs[0]).Obj(t)
	row := struct{ Status, Error string }{}
	if err := s.db.DB.Raw(`SELECT status, error FROM agent_runs WHERE id = ?`, runs[0]).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "cancelled" || row.Error != "Cancelled by user" {
		t.Errorf("run = %+v, want cancelled with Cancelled by user", row)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM reviews WHERE run_id = ?`, runs[0]); n != 0 {
		t.Errorf("reviews of the cancelled run = %d, want 0", n)
	}
	var logged []string
	for _, l := range trace["log"].([]any) {
		logged = append(logged, l.(map[string]any)["msg"].(string))
	}
	for _, want := range []string{"Cancellation requested — stopping…", "Run cancelled by user"} {
		if !slices.Contains(logged, want) {
			t.Errorf("trace log = %q, want it to hold %q", logged, want)
		}
	}
	if stats := trace["stats"].(map[string]any); stats["findings"] != float64(0) || stats["grounding"] != "0/0 passed" {
		t.Errorf("ended trace stats = %v, want 0 findings and 0/0 passed", stats)
	}
}

// AC4033 (FR4022): a cancel keeps later chunks from starting, and wins over a late answer.
func TestRun_CancelStopsLaterChunksAndBeatsALateAnswer(t *testing.T) {
	t.Run("map-reduce: no chunk starts after the cancel", func(t *testing.T) {
		release := make(chan struct{})
		ad := &scripted{complete: func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
			<-release // ignores the cancellation, then answers
			return answer(answerJSON, 100, 10, 0.001), nil
		}}
		s := startStack(t, spec{llmAdapters: ad.forAll(), llm: fastLLM(), config: func(c *config.Config) { c.ReviewMapConcurrency = 1 }})
		runs, _ := startOK(t, s, threeFiles(t, s, 941), map[string]any{"agentIds": []string{mapReduceAgent(t, s)}})
		eventually(t, 30*time.Second, "chunk 1 to be in its call", func() bool { return ad.calls() == 1 })
		if err := cancelFromOutside(s.base, runs[0]); err != nil {
			t.Fatal(err)
		}
		close(release)
		waitTrace(t, s, runs[0])
		if got := runStatus(t, s, runs[0]); got != "cancelled" {
			t.Errorf("run = %s, want cancelled", got)
		}
		if ad.calls() != 1 {
			t.Errorf("provider calls = %d, want 1: chunk 2 must never be requested", ad.calls())
		}
	})

	t.Run("single pass: the answer of the last call does not undo the cancel", func(t *testing.T) {
		release := make(chan struct{})
		ad := &scripted{complete: func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
			<-release
			return mockAnswer(ctx, at)
		}}
		s := startStack(t, spec{llmAdapters: ad.forAll()})
		runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
		eventually(t, 30*time.Second, "the call", func() bool { return ad.calls() == 1 })
		if err := cancelFromOutside(s.base, runs[0]); err != nil {
			t.Fatal(err)
		}
		close(release)
		waitTrace(t, s, runs[0])
		if got := runStatus(t, s, runs[0]); got != "cancelled" {
			t.Errorf("run = %s, want cancelled", got)
		}
		if n := count(t, s.db.DB, `SELECT count(*) FROM reviews WHERE run_id = ?`, runs[0]); n != 0 {
			t.Errorf("reviews = %d, want 0", n)
		}
		if sha := scalar[*string](t, s.db.DB, `SELECT last_reviewed_sha FROM pull_requests WHERE number = 482`); sha != nil {
			t.Errorf("last_reviewed_sha = %q, want it untouched (NULL)", *sha)
		}
	})
}

// holdAdvisoryLock takes the transaction-scoped advisory lock of key on its own transaction;
// release commits it.
func holdAdvisoryLock(t testing.TB, s *stack, key string) (release func()) {
	t.Helper()
	tx := s.db.DB.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, key).Error; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	rel := func() { once.Do(func() { _ = tx.Commit().Error }) }
	t.Cleanup(rel)
	return rel
}

// AC4034 (FR4020, EC4022): a cancel that lands while the persist transaction is blocked makes it
// roll back: no review, no finding, the pull request is not marked reviewed.
func TestRun_CancelDuringPersistRollsTheReviewBack(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	pr := pr482(t, s)
	// The review insert blocks on an advisory lock another connection holds (backend/INSIGHTS.md).
	for _, q := range []string{
		`CREATE FUNCTION p4_freeze() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended('p4-freeze', 0)); RETURN NULL; END $$`,
		`CREATE TRIGGER p4_freeze AFTER INSERT ON reviews FOR EACH STATEMENT EXECUTE FUNCTION p4_freeze()`,
	} {
		if err := s.db.DB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	release := holdAdvisoryLock(t, s, "p4-freeze")
	reviews := count(t, s.db.DB, `SELECT count(*) FROM reviews`)

	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	eventually(t, 30*time.Second, "the persist transaction to block", func() bool {
		return count(t, s.db.DB, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`) > 0
	})
	if resp := s.post("/runs/"+runs[0]+"/cancel", nil); resp.Status != http.StatusOK {
		t.Fatalf("cancel = %d %s", resp.Status, resp.Body)
	}
	release()
	waitTrace(t, s, runs[0])

	if got := runStatus(t, s, runs[0]); got != "cancelled" {
		t.Errorf("run = %s, want cancelled", got)
	}
	if got := count(t, s.db.DB, `SELECT count(*) FROM reviews`); got != reviews {
		t.Errorf("reviews = %d, want %d (the persist rolled back)", got, reviews)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM findings f JOIN reviews r ON r.id = f.review_id WHERE r.run_id = ?`, runs[0]); n != 0 {
		t.Errorf("findings of the run = %d, want 0", n)
	}
	if sha := scalar[*string](t, s.db.DB, `SELECT last_reviewed_sha FROM pull_requests WHERE id = ?`, pr); sha != nil {
		t.Errorf("last_reviewed_sha = %q, want it unchanged (NULL)", *sha)
	}
	if e := scalar[string](t, s.db.DB, `SELECT error FROM agent_runs WHERE id = ?`, runs[0]); e != "Cancelled by user" {
		t.Errorf("run error = %q, want Cancelled by user", e)
	}
}

// AC4036 (EC4021): a cancel while the diff loads: when the batch gets to the run, no LLM request
// is made, the row stays cancelled and the agent's skills are recorded.
func TestRun_CancelWhileTheDiffLoadsMakesNoCall(t *testing.T) {
	ad := mockScript()
	s := startStack(t, spec{llmAdapters: ad.forAll()})
	pr, agent := pr482(t, s), agentByName(t, s, agentGeneral)
	tx := s.db.DB.Begin()
	if err := tx.Exec(`LOCK TABLE pr_files IN ACCESS EXCLUSIVE MODE`).Error; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unlock := func() { once.Do(func() { _ = tx.Rollback().Error }) }
	t.Cleanup(unlock)

	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	eventually(t, 30*time.Second, "the diff load to block", func() bool {
		return count(t, s.db.DB, `SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation WHERE c.relname = 'pr_files' AND NOT l.granted`) > 0
	})
	if resp := s.post("/runs/"+runs[0]+"/cancel", nil); resp.Status != http.StatusOK {
		t.Fatalf("cancel = %d %s", resp.Status, resp.Body)
	}
	unlock()
	waitTrace(t, s, runs[0])

	if got := runStatus(t, s, runs[0]); got != "cancelled" {
		t.Errorf("run = %s, want cancelled", got)
	}
	if ad.calls() != 0 {
		t.Errorf("LLM calls = %d, want 0", ad.calls())
	}
	want := count(t, s.db.DB, `SELECT count(*) FROM agent_skills a JOIN skills s ON s.id = a.skill_id WHERE a.agent_id = ? AND s.enabled`, agent)
	if got := count(t, s.db.DB, `SELECT count(*) FROM agent_run_skills WHERE run_id = ?`, runs[0]); got != want || want == 0 {
		t.Errorf("agent_run_skills of the run = %d, want the agent's %d enabled skills", got, want)
	}
}

// AC4037 (EC4024): cancelling a finished run changes nothing; its stream replays the buffer with
// the cancel note last and no status event. After the bus forgot the run (a restart) the same
// calls produce a stream of that one event.
func TestRun_CancelOfAFinishedRun(t *testing.T) {
	db := pgtest.Migrated(t)
	s := startStack(t, spec{db: db, llmAdapters: mockScript().forAll()})
	runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	waitStatus(t, s, runs[0], "done")
	waitTrace(t, s, runs[0])

	if resp := s.post("/runs/"+runs[0]+"/cancel", nil); resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != `{"ok":true}` {
		t.Fatalf("cancel of a finished run = %d %s, want {\"ok\":true}", resp.Status, resp.Body)
	}
	if got := runStatus(t, s, runs[0]); got != "done" {
		t.Errorf("status after the cancel = %s, want done", got)
	}
	evs := eventMsgs(t, s, runs[0])
	if last := evs[len(evs)-1]; last.Msg != "Cancellation requested — stopping…" || last.Kind != "info" {
		t.Errorf("last replayed event = %s %q, want the cancel note", last.Kind, last.Msg)
	}
	for _, ev := range evs {
		if bytes.Contains(ev.Data, []byte(`"status"`)) {
			t.Errorf("the replay carries a status event: %+v", ev)
		}
	}
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}

	again := startStack(t, spec{db: db, llmAdapters: mockScript().forAll()}) // a new process: the bus is empty
	if resp := again.post("/runs/"+runs[0]+"/cancel", nil); resp.Status != http.StatusOK {
		t.Fatalf("cancel after a restart = %d %s", resp.Status, resp.Body)
	}
	if got := runStatus(t, again, runs[0]); got != "done" {
		t.Errorf("status after the restart and cancel = %s, want done", got)
	}
	evs = eventMsgs(t, again, runs[0])
	if diff := cmp.Diff([]string{"Cancellation requested — stopping…"}, msgs(evs)); diff != "" {
		t.Errorf("replay after the restart mismatch (-want +got):\n%s", diff)
	}
}

// AC4038 (EC4025): deleting a running run: the answer is ok, the run goes on until its call ends,
// and its later writes fail quietly: no row is re-created, nothing panics.
func TestRun_DeletedWhileRunningFinishesWithoutRecreatingRows(t *testing.T) {
	g := newGate()
	s := startStack(t, spec{llmAdapters: g.forAll()})
	runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	eventually(t, 30*time.Second, "the call", func() bool { return g.calls() == 1 })

	resp := s.do(http.MethodDelete, "/runs/"+runs[0], nil)
	if resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != `{"ok":true}` {
		t.Fatalf("DELETE running run = %d %s, want {\"ok\":true}", resp.Status, resp.Body)
	}
	reviews := count(t, s.db.DB, `SELECT count(*) FROM reviews`)
	g.Open() // the call ends; persisting now fails on the missing run
	eventually(t, 30*time.Second, "the run to end", func() bool {
		return len(logLines(t, s.logs, `review: agent "General Reviewer" failed`)) == 1
	})

	for _, q := range []string{
		`SELECT count(*) FROM agent_runs WHERE id = '` + runs[0] + `'`,
		`SELECT count(*) FROM run_traces WHERE run_id = '` + runs[0] + `'`,
		`SELECT count(*) FROM reviews WHERE run_id = '` + runs[0] + `'`,
		`SELECT count(*) FROM agent_run_skills WHERE run_id = '` + runs[0] + `'`,
	} {
		if n := count(t, s.db.DB, q); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
	if got := count(t, s.db.DB, `SELECT count(*) FROM reviews`); got != reviews {
		t.Errorf("reviews = %d, want %d", got, reviews)
	}
	if strings.Contains(s.logs.String(), "panic") || strings.Contains(s.logs.String(), "crashed") {
		t.Errorf("the log mentions a panic:\n%s", s.logs.String())
	}
	if resp := s.get("/health"); resp.Status != http.StatusOK {
		t.Errorf("GET /health = %d, want 200", resp.Status)
	}
}

// ---- repo intel and project documents in a run ----

// AC4050 (FR4031): an agent with repo intel off calls none of its parts; for the others each part
// degrades on its own: a failing caller scan is an info event, an uncached map and a failing rank
// lookup leave no line, and none of the three sections reaches the prompt.
func TestRun_RepoIntelDegradesAndCanBeSwitchedOff(t *testing.T) {
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", intelFixture)
	s := startStack(t, spec{remoteDir: remote, config: intelOn, llmAdapters: mockScript().forAll()})
	repo := addClonedRepo(t, s, "o", "r")
	s.waitJobs("repo-intel-index", 1)
	pr := newFx(t, s).pr(repo, 950, map[string]string{"src/util.ts": patchOf(3), "src/core.ts": patchOf(3)})
	agent := agentByName(t, s, agentGeneral)
	logOf := func(run string) (log []string, user string) {
		trace := waitTrace(t, s, run).Obj(t)
		for _, l := range trace["log"].([]any) {
			log = append(log, l.(map[string]any)["msg"].(string))
		}
		return log, userPrompt(t, trace)
	}

	// An agent that switched repo intel off: the one info line and no enrichment line at all.
	if err := s.db.DB.Exec(`UPDATE agents SET repo_intel = false WHERE id = ?`, agent).Error; err != nil {
		t.Fatal(err)
	}
	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	waitStatus(t, s, runs[0], "done")
	logged, _ := logOf(runs[0])
	if !slices.Contains(logged, "Repo intel disabled for this agent — skipping context enrichment") {
		t.Errorf("log = %q, want the repo-intel-disabled line", logged)
	}
	for _, m := range logged {
		if strings.HasPrefix(m, "callers digest:") || strings.HasPrefix(m, "repo map:") || strings.HasPrefix(m, "file rank:") {
			t.Errorf("log has %q although repo intel is off for the agent", m)
		}
	}

	// Repo intel on, every part failing or empty. The run is held before its repo-intel step (its
	// skills lookup waits on a table lock) while the repository row becomes unreadable for the
	// caller scan.
	if err := s.db.DB.Exec(`UPDATE agents SET repo_intel = true WHERE id = ?`, agent).Error; err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM repo_map_cache`, `ALTER TABLE file_rank RENAME TO file_rank_gone`} {
		if err := s.db.DB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	tx := s.db.DB.Begin()
	if err := tx.Exec(`LOCK TABLE agent_skills IN ACCESS EXCLUSIVE MODE`).Error; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unlock := func() { once.Do(func() { _ = tx.Commit().Error }) }
	t.Cleanup(unlock)
	runs, _ = startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	eventually(t, 30*time.Second, "the run to wait on its skills", func() bool {
		return count(t, s.db.DB, `SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation WHERE c.relname = 'agent_skills' AND NOT l.granted`) > 0
	})
	if err := s.db.DB.Exec(`ALTER TABLE repos RENAME COLUMN clone_path TO clone_path_gone`).Error; err != nil {
		t.Fatal(err)
	}
	unlock()
	waitStatus(t, s, runs[0], "done")
	logged, user := logOf(runs[0])
	if !slices.ContainsFunc(logged, regexp.MustCompile(`^callers digest: repoIntel failed — .+`).MatchString) {
		t.Errorf("log = %q, want a callers digest failure event", logged)
	}
	for _, m := range logged {
		if strings.HasPrefix(m, "repo map:") || strings.HasPrefix(m, "file rank:") {
			t.Errorf("log has %q, want no repo map and no rank line", m)
		}
	}
	for _, banned := range []string{"## Callers of changed symbols", "## Repo skeleton", "most-depended-on"} {
		if strings.Contains(user, banned) {
			t.Errorf("the prompt has %q, want it absent", banned)
		}
	}
}

// AC4040 (FR4047, FR4033), AC4055 (EC4037): the completed trace of a mock run with one context
// document and one skill; a repo that was never cloned has every repo document missing while
// store files are read from the database; a resolver failure is a warning event and no section.
func TestTrace_CompletedShapeAndProjectContextWithoutAClone(t *testing.T) {
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	repo := repoByName(t, s, "acme/payments-api") // never cloned
	pr := pr482(t, s)
	const store = ".devdigest/specs/notes.md"
	if resp := s.post("/repos/"+repo+"/context/files", map[string]any{"path": store, "content": "# Notes\n\nStore text.\n"}); resp.Status != http.StatusCreated {
		t.Fatalf("create store file = %d %s", resp.Status, resp.Body)
	}
	agent := newAgent(t, s, map[string]any{"name": "Context Agent", "strategy": "single-pass"})
	skill := newSkill(t, s, "ctx-skill", "Check the notes.")
	linkSkill(t, s, agent, skill)
	if resp := s.do(http.MethodPut, "/agents/"+agent+"/context", map[string]any{"repo_id": repo, "paths": []string{store, "docs/readme.md"}}); resp.Status != http.StatusOK {
		t.Fatalf("attach = %d %s", resp.Status, resp.Body)
	}

	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	waitStatus(t, s, runs[0], "done")
	trace := waitTrace(t, s, runs[0])
	obj := trace.Obj(t)

	if diff := cmp.Diff([]string{"config", "stats", "prompt_assembly", "tool_calls", "raw_output", "memory_pulled", "specs_read", "log", "skills_used", "intent", "project_context"},
		topLevelKeys(t, trace.Body)); diff != "" {
		t.Errorf("trace keys mismatch (-want +got):\n%s", diff)
	}
	cfg := obj["config"].(map[string]any)
	version := scalar[string](t, s.db.DB, `SELECT version::text FROM agents WHERE id = ?`, agent)
	if cfg["version"] != version || cfg["source"] != "local" || cfg["agent"] != "Context Agent" || cfg["pr"] != float64(482) {
		t.Errorf("config = %v, want the agent version as a string (%s), source local, agent Context Agent, pr 482", cfg, version)
	}
	if diff := cmp.Diff([]any{map[string]any{"tool": "review_file", "args": "all files", "meta": "single-pass", "ms": obj["tool_calls"].([]any)[0].(map[string]any)["ms"]}}, obj["tool_calls"]); diff != "" {
		t.Errorf("tool_calls mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{}, obj["memory_pulled"]); diff != "" {
		t.Errorf("memory_pulled mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{store}, obj["specs_read"]); diff != "" {
		t.Errorf("specs_read mismatch (-want +got):\n%s", diff)
	}
	if obj["intent"] != nil {
		t.Errorf("intent = %v, want null", obj["intent"])
	}
	pc := obj["project_context"].(map[string]any)
	docs := pc["docs"].([]any)
	if pc["budget_tokens"] != float64(16000) || len(docs) != 2 ||
		docs[0].(map[string]any)["status"] != "included" || docs[0].(map[string]any)["source"] != "store" ||
		docs[1].(map[string]any)["status"] != "missing" || docs[1].(map[string]any)["source"] != "repo" {
		t.Errorf("project_context = %v, want budget 16000, the store file included and the repo document missing (no clone)", pc)
	}
	log := obj["log"].([]any)
	if last := log[len(log)-1].(map[string]any)["msg"].(string); !strings.HasPrefix(last, "Persisted review ") {
		t.Errorf("last log line = %q, want Persisted review … (the completion line is published after the log was taken)", last)
	}
	if user := userPrompt(t, obj); !strings.Contains(user, "## Project context\n") || !strings.Contains(user, "Store text.") {
		t.Errorf("the prompt lacks the project context section:\n%s", user)
	}

	// A resolver failure: the store table is gone.
	if err := s.db.DB.Exec(`ALTER TABLE context_files RENAME TO context_files_gone`).Error; err != nil {
		t.Fatal(err)
	}
	runs, _ = startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	waitStatus(t, s, runs[0], "done")
	failed := waitTrace(t, s, runs[0])
	var warning *sseEvent
	for _, ev := range eventMsgs(t, s, runs[0]) {
		if strings.HasPrefix(ev.Msg, "warning: project context unavailable — ") {
			warning = &ev
		}
	}
	if warning == nil || warning.Kind != "info" || string(warning.Data) != `{"warning":"project_context_unavailable"}` {
		t.Errorf("warning event = %+v, want an info event with data {\"warning\":\"project_context_unavailable\"}", warning)
	}
	fo := failed.Obj(t)
	if _, has := fo["project_context"]; has {
		t.Errorf("trace after a resolver failure has project_context: %v", fo["project_context"])
	}
	if user := userPrompt(t, fo); strings.Contains(user, "## Project context") {
		t.Errorf("the prompt has a project context section after a resolver failure:\n%s", user)
	}
}

// ---- startup and shutdown ----

// AC4067 (FR4041, EC4034): runs left running are failed (error untouched) before the port
// accepts connections, with one info line; the finished run is untouched; with the migrations off
// on an empty database the failure is logged and /health answers.
func TestStartup_InterruptedRunsFailBeforeListening(t *testing.T) {
	db := pgtest.Migrated(t)
	restart(t, db) // migrate is done; this seeds
	f := fxOn(t, db)
	repo := scalar[string](t, db.DB, `SELECT id::text FROM repos LIMIT 1`)
	pr := f.pr(repo, 960, nil)
	agent := f.agent("Interrupted")
	r1 := f.run(runRow{pr: pr, agent: agent, status: "running", errText: ptr("kept")})
	r2 := f.run(runRow{pr: pr, agent: agent, status: "running"})
	r3 := f.run(runRow{pr: pr, agent: agent, status: "done"})

	type snap struct{ Status, Error string }
	read := func(id string) snap {
		var s snap
		if err := db.DB.Raw(`SELECT status, coalesce(error, '<null>') AS error FROM agent_runs WHERE id = ?`, id).Scan(&s).Error; err != nil {
			t.Fatal(err)
		}
		return s
	}
	var atListen map[string]snap
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- serve(ctx, newConfigWith(t, db), withLogOutput(logs), func(o *options) {
			o.onListen = func(net.Addr) {
				atListen = map[string]snap{r1: read(r1), r2: read(r2), r3: read(r3)}
				close(ready)
			}
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("serve returned before listening: %v\n%s", err, logs.String())
	case <-time.After(60 * time.Second):
		t.Fatal("serve did not start listening")
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve() = %v after the stop signal, want nil", err)
	}
	want := map[string]snap{r1: {"failed", "kept"}, r2: {"failed", "<null>"}, r3: {"done", "<null>"}}
	if diff := cmp.Diff(want, atListen); diff != "" {
		t.Errorf("run rows when the port opened (-want +got):\n%s", diff)
	}
	lines := logLines(t, logs, "reviews: interrupted runs failed")
	if len(lines) != 1 || lines[0]["level"] != "INFO" || lines[0]["count"] != float64(2) {
		t.Errorf("reap log lines = %v, want one info line with count 2", lines)
	}

	t.Run("migrations off on an empty database", func(t *testing.T) {
		s := startStack(t, spec{db: pgtest.Empty(t), noMigrate: true, noSeed: true})
		if resp := s.get("/health"); resp.Status != http.StatusOK {
			t.Errorf("GET /health = %d, want 200", resp.Status)
		}
		if lines := logLines(t, s.logs, "interrupted review runs not failed"); len(lines) != 1 || lines[0]["level"] != "ERROR" {
			t.Errorf("reap failure log lines = %v, want exactly one error line", lines)
		}
	})
}

// fxOn is newFx on a database without a running stack.
func fxOn(t testing.TB, db *pgtest.Database) fx {
	t.Helper()
	return fx{t: t, db: db.DB, ws: scalar[string](t, db.DB, `SELECT id::text FROM workspaces ORDER BY created_at LIMIT 1`)}
}

// AC4068 (FR4042, NFR4006, EC4033): on SIGTERM with 4 mock runs waiting in their calls, every run
// is cancelled with the user note, every stream has ended and the process is done within 20 s.
func TestShutdown_CancelsLiveRunsAndEndsTheStreams(t *testing.T) {
	s := startStack(t, spec{config: func(c *config.Config) {
		c.LLMProviderOverride = config.LLMProviderMock
		c.LLMMockDelay = 60 * time.Second
	}})
	pr := pr482(t, s)
	runs, _ := startOK(t, s, pr, map[string]any{"all": true})
	if len(runs) != 4 {
		t.Fatalf("runs = %v, want 4", runs)
	}
	var streams []*capture
	for _, r := range runs {
		streams = append(streams, follow(t, s, "/runs/"+r+"/events"))
	}
	for i, c := range streams {
		eventually(t, 30*time.Second, fmt.Sprintf("stream %d to reach its LLM call", i), func() bool {
			return strings.Contains(c.Body(), "Reviewing all files in one pass")
		})
	}

	start := time.Now()
	s.cancel() // SIGTERM
	if err := s.stop(); err != nil {
		t.Errorf("serve() = %v after the stop signal, want nil", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("shutdown took %s, want within 20 s", took)
	}
	for i, c := range streams {
		c.Wait(t, 5*time.Second)
		if !c.Ended() {
			t.Errorf("stream %d did not end", i)
		}
	}
	for _, r := range runs {
		row := struct{ Status, Error string }{}
		if err := s.db.DB.Raw(`SELECT status, coalesce(error, '') AS error FROM agent_runs WHERE id = ?`, r).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Status != "cancelled" || row.Error != "Cancelled by user" {
			t.Errorf("run %s = %+v, want cancelled with Cancelled by user", r, row)
		}
	}
}

// AC4069 (FR4042, EC4033): a call that ignores its cancellation for 30 s: after the 5 s drain the
// bus is completed (the stream ends), the process is done within 20 s with the row still running,
// and the next start reaps it to failed.
func TestShutdown_ACallThatIgnoresTheCancelIsLeftToTheReap(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) }) // the adapter outlives serve
	ad := &scripted{complete: func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
		select {
		case <-release:
		case <-time.After(30 * time.Second):
		}
		return mockAnswer(ctx, at)
	}}
	db := pgtest.Migrated(t)
	s := startStack(t, spec{db: db, llmAdapters: ad.forAll()})
	runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentIds": []string{agentByName(t, s, agentGeneral)}})
	stream := follow(t, s, "/runs/"+runs[0]+"/events")
	eventually(t, 30*time.Second, "the call", func() bool { return ad.calls() == 1 })

	start := time.Now()
	s.cancel()
	stream.Wait(t, 15*time.Second)
	if took := time.Since(start); took < 4*time.Second || took > 8*time.Second {
		t.Errorf("the stream ended %s after the stop signal, want about the 5 s drain", took)
	}
	if err := s.stop(); err != nil {
		t.Errorf("serve() = %v after the stop signal, want nil", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("shutdown took %s, want within 20 s", took)
	}
	if got := runStatus(t, s, runs[0]); got != "running" {
		t.Errorf("run = %s after the shutdown, want it still running", got)
	}
	if lines := logLines(t, s.logs, "review runs did not finish recording; the next start reaps them"); len(lines) != 1 {
		t.Errorf("not-drained log lines = %v, want one warning", lines)
	}

	next := startStack(t, spec{db: db, llmAdapters: mockScript().forAll()})
	if got := runStatus(t, next, runs[0]); got != "failed" {
		t.Errorf("run = %s after the next start, want failed (reaped)", got)
	}
	if e := scalar[*string](t, db.DB, `SELECT error FROM agent_runs WHERE id = ?`, runs[0]); e != nil {
		t.Errorf("reaped run error = %q, want it untouched (NULL)", *e)
	}
}

// ---- secrets and log privacy ----

// AC4072 (NFR4007), AC4073 (NFR4008): a stored OpenAI key and GitHub token never reach a response,
// event, trace, run error or log line, whether a run succeeds, fails with a provider error that
// echoes the key or fails the schema; the mirrored error log line holds only the first line of the
// note and no log line carries the diff, a skill body or the raw output.
func TestRuns_KeysAndContentNeverReachResponsesOrLogs(t *testing.T) {
	const (
		ghToken   = "ghp_secretTokenForTheTest0123456789"
		skillBody = "SKILL-BODY-SENTINEL-7f3a2c"
		rawOutput = "RAW-OUTPUT-SENTINEL-91bd04"
		diffText  = "sk_live_REDACTED_FOR_DEMO" // a line of PR #482's diff
	)
	var mode atomic.Value
	mode.Store("ok")
	ad := &scripted{needsKey: true, complete: func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
		switch mode.Load() {
		case "error":
			return llm.AttemptResult{}, errors.New("401 Unauthorized: invalid api key " + openAIKey + " for your account")
		case "schema":
			return answer(`{"verdict":"nope","note":"`+rawOutput+`"}`, 10, 5, 0.001), nil
		}
		return mockAnswer(ctx, at)
	}}
	secrets := fmt.Sprintf(`{"OPENAI_API_KEY":%q,"GITHUB_TOKEN":%q}`, openAIKey, ghToken)
	s := startStack(t, spec{llmAdapters: ad.forAll(), llm: fastLLM(), secretFile: secrets})
	agent := openAIAgent(t, s)
	skill := newSkill(t, s, "sentinel-skill", skillBody)
	linkSkill(t, s, agent, skill)
	pr := pr482(t, s)

	var runIDs []string
	var bodies []string
	for _, m := range []string{"ok", "error", "schema"} {
		mode.Store(m)
		runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
		want := "done"
		if m != "ok" {
			want = "failed"
		}
		waitStatus(t, s, runs[0], want)
		waitTrace(t, s, runs[0])
		runIDs = append(runIDs, runs[0])
	}
	read := func(path string) {
		bodies = append(bodies, string(s.get(path).Body))
	}
	read("/pulls/" + pr + "/runs")
	read("/pulls/" + pr + "/reviews")
	for _, r := range runIDs {
		read("/runs/" + r + "/trace")
		bodies = append(bodies, strings.Join(msgs(eventMsgs(t, s, r)), "\n"))
	}
	bodies = append(bodies, scalar[string](t, s.db.DB, `SELECT string_agg(coalesce(error, ''), ' ') FROM agent_runs`))
	for i, b := range bodies {
		for _, secret := range []string{openAIKey, ghToken} {
			if strings.Contains(b, secret) {
				t.Errorf("response/event/trace/error #%d contains the secret %q", i, secret)
			}
		}
	}
	logs := s.logs.String()
	for _, secret := range []string{openAIKey, ghToken} {
		if strings.Contains(logs, secret) {
			t.Errorf("a log line contains the secret %q", secret)
		}
	}
	for _, content := range []string{skillBody, rawOutput, diffText} {
		if strings.Contains(logs, content) {
			t.Errorf("a log line contains run content %q", content)
		}
	}
	// The mirrored error of the schema failure: one line, the first line of the note only.
	var mirrored []string
	for _, e := range s.logs.entries(t) {
		if e["level"] == "ERROR" && strings.Contains(fmt.Sprint(e["msg"]), "structured output failed schema validation") {
			mirrored = append(mirrored, fmt.Sprint(e["msg"]))
		}
	}
	if len(mirrored) == 0 {
		t.Fatalf("no error log line mirrors the schema failure; log:\n%s", logs)
	}
	for _, m := range mirrored {
		if strings.Contains(m, "\n") || strings.Contains(m, "Last issues:\n") || strings.Contains(m, "Raw output") {
			t.Errorf("mirrored error line = %q, want the first line of the note only", m)
		}
	}
}

// A panic in a provider adapter runs on a goroutine of the engine's pool: it must fail only that
// run, with the generic note and nothing of the panic value, and leave the process serving.
func TestRun_APanicInAProviderFailsOnlyThatRun(t *testing.T) {
	var n atomic.Int32
	ad := &scripted{complete: func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
		if n.Add(1) == 1 {
			panic("adapter bug with a secret sk-panic-value")
		}
		return mockAnswer(ctx, at)
	}}
	s := startStack(t, spec{llmAdapters: ad.forAll()})
	pr, agent := pr482(t, s), agentByName(t, s, agentGeneral)

	first, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	waitStatus(t, s, first[0], "failed")
	if e := scalar[string](t, s.db.DB, `SELECT error FROM agent_runs WHERE id = ?`, first[0]); e != "internal error" {
		t.Errorf("failed run error = %q, want the generic internal error", e)
	}
	if lines := logLines(t, s.logs, "review: run panicked"); len(lines) != 1 {
		t.Errorf("panic log lines = %v, want exactly one", lines)
	}
	second, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	waitStatus(t, s, second[0], "done")
	if resp := s.get("/health"); resp.Status != http.StatusOK {
		t.Errorf("GET /health = %d after the panic, want 200", resp.Status)
	}
	for _, r := range []string{first[0], second[0]} {
		for _, ev := range eventMsgs(t, s, r) {
			if strings.Contains(ev.Msg, "sk-panic-value") {
				t.Errorf("run %s event %q carries the panic value", r, ev.Msg)
			}
		}
	}
}
