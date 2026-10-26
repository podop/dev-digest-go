package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	cpdomain "dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/secrets"
)

// Component tests of the calls through custom model providers (docs/plans/2026-10-26-custom-model-
// providers.md, S28): a stub gateway (OpenAI-compatible chat completions and model list, and an
// Anthropic-compatible messages endpoint) is defined as a custom provider through the real routes,
// then real features run on it: a PR review, a brief, an intent refresh, an eval run. The tests see
// what the gateway received (auth and extra headers, the body per structured-output mode), what
// was never sent (a redirect target, a forbidden address), what the ledger stored, what the log
// said and what the bundles hold. Helpers are prefixed cc.

// Recognisable secrets of at least eight characters (NFR1). None starts with "sk-": the gateway
// adapters also scrub anything key-shaped, and these must be removed by an exact match.
const (
	ccKey     = "SYNAPSE-KEY-9f3a7c21e5"
	ccTenant  = "TENANT-VALUE-4b8d1e07aa"
	ccTeam    = "TEAM-VALUE-71c0d9e2b3"
	ccPrompt  = "PROMPT-CANARY-5d2e8f10c4"
	ccSummary = "ANSWER-CANARY-b83a6e5f21"
)

// ccNow is the fixed clock of these tests: a month far from the seeded demo data, so a fresh
// server's month spend is exactly 0 and every amount is hand-computed (backend/INSIGHTS.md).
var ccNow = time.Date(2033, 5, 12, 12, 0, 0, 0, time.UTC)

func ccClock() time.Time { return ccNow }

// ---- the stub gateway ----

// ccReq is one request the gateway received.
type ccReq struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// obj decodes the JSON body.
func (r ccReq) obj(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("%s %s body is not a JSON object: %q: %v", r.Method, r.Path, r.Body, err)
	}
	return m
}

// completion reports whether the request asks for a model answer.
func (r ccReq) completion() bool {
	return r.Method == http.MethodPost && (strings.HasSuffix(r.Path, "/chat/completions") || strings.HasSuffix(r.Path, "/messages"))
}

// system is the system text of the request: the system messages of an OpenAI body, the system
// field of an Anthropic one.
func (r ccReq) system(t testing.TB) string {
	t.Helper()
	body := r.obj(t)
	if s, ok := body["system"].(string); ok {
		return s
	}
	var parts []string
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm["role"] == "system" {
			parts = append(parts, fmt.Sprint(mm["content"]))
		}
	}
	return strings.Join(parts, "\n\n")
}

// lastUser is the content of the last user message.
func (r ccReq) lastUser(t testing.TB) string {
	t.Helper()
	msgs, _ := r.obj(t)["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		if mm, _ := msgs[i].(map[string]any); mm["role"] == "user" {
			return fmt.Sprint(mm["content"])
		}
	}
	return ""
}

// messages are the request's messages as the mock provider reads them.
func (r ccReq) messages() []llm.Message {
	var body struct {
		System   string `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(r.Body, &body)
	var out []llm.Message
	if body.System != "" {
		out = append(out, llm.Message{Role: llm.System, Content: body.System})
	}
	for _, m := range body.Messages {
		out = append(out, llm.Message{Role: llm.Role(m.Role), Content: m.Content})
	}
	return out
}

// ccUsage is the usage block of an answer; Cost is raw JSON ("" = no cost field).
type ccUsage struct {
	In, Out int
	Cost    string
}

// ccReply is what the gateway answers instead of its default.
type ccReply struct {
	Status   int
	Location string // a 3xx target
	Body     string
	Hang     bool
}

// ccGateway is a stub gateway serving both protocols under /v1: GET /v1/models, POST
// /v1/chat/completions (OpenAI) and POST /v1/messages (Anthropic). By default it answers a
// completion with the mock provider's fixture of schema, in the shape of the request.
type ccGateway struct {
	t   testing.TB
	srv *httptest.Server

	mu    sync.Mutex
	reqs  []ccReq
	nComp int

	// schema is the mock fixture the default answer carries (mock.Review, mock.PrBriefLlm, ...).
	schema string
	// usage is the usage block of a default answer; nil = none at all.
	usage *ccUsage
	// models is the id list GET /v1/models answers.
	models []string
	// handle, when set, may answer a request itself: n is the 1-based count of completion requests
	// so far (0 for others); ok=false falls through to the default.
	handle func(r ccReq, n int) (reply ccReply, ok bool)
}

func newCCGateway(t testing.TB, schema string) *ccGateway {
	t.Helper()
	g := &ccGateway{t: t, schema: schema, usage: &ccUsage{In: 1200, Out: 180}, models: []string{"gw-model"}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

// base is the API root a provider is defined with.
func (g *ccGateway) base() string { return g.srv.URL + "/v1" }

// host is host:port of the gateway.
func (g *ccGateway) host() string { return strings.TrimPrefix(g.srv.URL, "http://") }

// requests returns a copy of what was received so far.
func (g *ccGateway) requests() []ccReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ccReq(nil), g.reqs...)
}

// completions returns the completion requests so far.
func (g *ccGateway) completions() []ccReq {
	var out []ccReq
	for _, r := range g.requests() {
		if r.completion() {
			out = append(out, r)
		}
	}
	return out
}

func (g *ccGateway) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	req := ccReq{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	g.mu.Lock()
	g.reqs = append(g.reqs, req)
	n := 0
	if req.completion() {
		g.nComp++
		n = g.nComp
	}
	g.mu.Unlock()

	if g.handle != nil {
		if rep, ok := g.handle(req, n); ok {
			g.write(w, r, rep)
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
		var data []map[string]string
		for _, id := range g.models {
			data = append(data, map[string]string{"id": id})
		}
		ccJSON(w, http.StatusOK, map[string]any{"data": data})
	case req.completion():
		g.write(w, r, ccReply{Body: ccAnswerBody(g.t, req, g.fixture(req), g.usage)})
	default:
		http.NotFound(w, r)
	}
}

func (g *ccGateway) write(w http.ResponseWriter, r *http.Request, rep ccReply) {
	switch {
	case rep.Hang:
		<-r.Context().Done()
	case rep.Location != "":
		w.Header().Set("Location", rep.Location)
		w.WriteHeader(rep.Status)
	default:
		if rep.Status == 0 {
			rep.Status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.Status)
		_, _ = io.WriteString(w, rep.Body)
	}
}

func ccJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fixture is the valid model text for the request: the mock provider's answer of g.schema.
func (g *ccGateway) fixture(req ccReq) string {
	g.t.Helper()
	res, err := mock.New(llm.OpenRouter, mock.Options{}).Complete(g.t.Context(), "", llm.Attempt{SchemaName: g.schema, Messages: req.messages()})
	if err != nil {
		g.t.Fatalf("mock fixture of %s: %v", g.schema, err)
	}
	return res.Raw
}

// ccAnswerBody wraps text in the answer shape of the request's protocol: chat completions for an
// OpenAI path; for Anthropic a forced tool call when the request has tools, else text blocks.
func ccAnswerBody(t testing.TB, req ccReq, text string, u *ccUsage) string {
	t.Helper()
	var usage map[string]any
	if u != nil {
		if strings.HasSuffix(req.Path, "/messages") {
			usage = map[string]any{"input_tokens": u.In, "output_tokens": u.Out}
		} else {
			usage = map[string]any{"prompt_tokens": u.In, "completion_tokens": u.Out}
		}
		if u.Cost != "" {
			usage["cost"] = json.RawMessage(u.Cost)
		}
	}
	var body map[string]any
	switch {
	case strings.HasSuffix(req.Path, "/chat/completions"):
		body = map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}}}}
	case req.obj(t)["tools"] != nil:
		body = map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "x", "input": json.RawMessage(text)}}}
	default:
		body = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
	}
	if usage != nil {
		body["usage"] = usage
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ccTextBlocks is an Anthropic text answer split over the given texts.
func ccTextBlocks(t testing.TB, u *ccUsage, texts ...string) string {
	t.Helper()
	var blocks []any
	for _, tx := range texts {
		blocks = append(blocks, map[string]any{"type": "text", "text": tx})
	}
	body := map[string]any{"content": blocks}
	if u != nil {
		body["usage"] = map[string]any{"input_tokens": u.In, "output_tokens": u.Out}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ccReviewText is a valid Review answer text with summary as its summary.
func ccReviewText(t testing.TB, summary string) string {
	t.Helper()
	res, err := mock.New(llm.OpenRouter, mock.Options{}).Complete(t.Context(), "", llm.Attempt{SchemaName: mock.Review})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Raw), &m); err != nil {
		t.Fatal(err)
	}
	m["summary"] = summary
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ccWithout is a Review answer text with the named top-level field removed (a schema violation).
func ccWithout(t testing.TB, text, field string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, field)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ---- a fake name server ----

// ccFakeDNS makes the given host names resolve to the given addresses for every lookup of the
// process (net.DefaultResolver) until the test ends; any other name that is not in /etc/hosts
// does not exist. It is how a host NAME that resolves to 169.254.169.254 is tested: the egress
// guard judges the address at connect time. The tests that call it do not run in parallel.
func ccFakeDNS(t *testing.T, hosts map[string]string) {
	t.Helper()
	prev := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go ccServeDNS(server, hosts)
			return client, nil
		},
	}
	t.Cleanup(func() { net.DefaultResolver = prev })
}

// ccServeDNS answers length-prefixed DNS queries (the stream framing) on c.
func ccServeDNS(c net.Conn, hosts map[string]string) {
	defer func() { _ = c.Close() }()
	for {
		var size [2]byte
		if _, err := io.ReadFull(c, size[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(c, query); err != nil {
			return
		}
		answer := ccDNSAnswer(query, hosts)
		framed := binary.BigEndian.AppendUint16(nil, uint16(len(answer)))
		if _, err := c.Write(append(framed, answer...)); err != nil {
			return
		}
	}
}

// ccDNSAnswer builds the response to a one-question query: an A record for a known name, an empty
// answer for its AAAA, NXDOMAIN for anything else.
func ccDNSAnswer(query []byte, hosts map[string]string) []byte {
	if len(query) < 12 {
		return nil
	}
	pos := 12
	var labels []string
	for pos < len(query) && query[pos] != 0 {
		n := int(query[pos])
		if pos+1+n > len(query) {
			return nil
		}
		labels = append(labels, string(query[pos+1:pos+1+n]))
		pos += 1 + n
	}
	end := pos + 1 + 4 // the root label, QTYPE and QCLASS
	if end > len(query) {
		return nil
	}
	name := strings.ToLower(strings.Join(labels, "."))
	qtype := binary.BigEndian.Uint16(query[pos+1:])
	resp := append([]byte(nil), query[:end]...)
	resp[2], resp[3] = 0x81, 0x80 // response, recursion desired and available
	resp[6], resp[7], resp[8], resp[9], resp[10], resp[11] = 0, 0, 0, 0, 0, 0
	ip, known := hosts[name]
	if !known {
		resp[3] |= 3 // NXDOMAIN
		return resp
	}
	if qtype != 1 {
		return resp
	}
	resp[7] = 1
	a4 := netip.MustParseAddr(ip).As4()
	resp = append(resp, 0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4)
	return append(resp, a4[:]...)
}

// ---- stacks and definitions ----

// ccStack starts a server on the fixed clock with the fast retry tuning of the stub tests.
func ccStack(t *testing.T, sp spec) *stack {
	t.Helper()
	if sp.now == nil {
		sp.now = ccClock
	}
	if sp.llm.Prices == nil {
		sp.llm = fastLLM()
	}
	return startStack(t, sp)
}

// ccInput is a custom provider body for POST /settings/custom-providers; over replaces or adds
// fields.
func ccInput(slug, name, protocol, base string, over map[string]any) map[string]any {
	in := map[string]any{"id": "custom:" + slug, "name": name, "protocol": protocol, "base_url": base}
	for k, v := range over {
		in[k] = v
	}
	return in
}

// ccDefine creates the provider through the real route and requires a 201.
func ccDefine(t testing.TB, s *stack, in map[string]any) map[string]any {
	t.Helper()
	r := s.post("/settings/custom-providers", in)
	if r.Status != http.StatusCreated {
		t.Fatalf("POST /settings/custom-providers %v = %d %s, want 201", in["id"], r.Status, r.Body)
	}
	return r.Obj(t)
}

// ccRedefine replaces the provider (PUT) and requires a 200.
func ccRedefine(t testing.TB, s *stack, in map[string]any) {
	t.Helper()
	id, _ := in["id"].(string)
	body := map[string]any{}
	for k, v := range in {
		if k != "id" {
			body[k] = v
		}
	}
	r := s.do(http.MethodPut, "/settings/custom-providers/"+url.PathEscape(id), body)
	if r.Status != http.StatusOK {
		t.Fatalf("PUT /settings/custom-providers/%s = %d %s, want 200", id, r.Status, r.Body)
	}
}

// ccAgent creates an agent on provider/model with the prompt canary in its system prompt.
func ccAgent(t testing.TB, s *stack, name, provider, model string) string {
	t.Helper()
	return newAgent(t, s, map[string]any{"name": name, "provider": provider, "model": model, "system_prompt": "Review the diff. " + ccPrompt})
}

// ccReview reviews pr with the agent and returns the run id and its final status (done or failed).
func ccReview(t testing.TB, s *stack, pr, agent string) (run, status string) {
	t.Helper()
	runs, _ := startOK(t, s, pr, map[string]any{"agentIds": []string{agent}})
	return runs[0], waitStatus(t, s, runs[0], "done", "failed")
}

// ccRunError is the stored error of a run.
func ccRunError(t testing.TB, s *stack, run string) string {
	t.Helper()
	return scalar[string](t, s.db.DB, `SELECT coalesce(error, '') FROM agent_runs WHERE id = ?`, run)
}

// ccFindings is the stored findings count of a run.
func ccFindings(t testing.TB, s *stack, run string) int {
	t.Helper()
	return count(t, s.db.DB, `SELECT findings_count FROM agent_runs WHERE id = ?`, run)
}

// ccAbsent fails when any of the texts holds a needle.
func ccAbsent(t testing.TB, what string, needles []string, texts ...string) {
	t.Helper()
	for _, text := range texts {
		for _, n := range needles {
			if n != "" && strings.Contains(text, n) {
				t.Errorf("%s holds %q, want it absent", what, n)
			}
		}
	}
}

// ccSecrets are the values that must appear nowhere but the secrets file.
var ccSecrets = []string{ccKey, ccTenant, ccTeam}

// ---- AC4: auth headers, extra headers and the strict body ----

// AC4 (FR4, FR8): a review on an OpenAI-protocol provider in bearer mode and on an Anthropic-
// protocol provider in header mode with two extra headers (both created without structured_output,
// so strict) reaches the gateway with the documented headers and bodies; no key, no auth header;
// an extra anthropic-version wins.
func TestCustomCalls_AuthHeadersAndStrictBodies(t *testing.T) {
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	n := 0
	review := func(g *ccGateway, provider string) ccReq {
		t.Helper()
		n++
		agent := ccAgent(t, s, fmt.Sprintf("Auth agent %d", n), provider, "gw-model")
		run, status := ccReview(t, s, pr, agent)
		if status != "done" {
			t.Fatalf("review on %s = %s (%s), want done", provider, status, ccRunError(t, s, run))
		}
		reqs := g.completions()
		if len(reqs) != 1 {
			t.Fatalf("gateway of %s received %d completions, want 1", provider, len(reqs))
		}
		if reqs[0].obj(t)["stream"] == true {
			t.Errorf("request to %s asked for streaming", provider)
		}
		if got := reqs[0].obj(t)["model"]; got != "gw-model" {
			t.Errorf("request to %s model = %v, want gw-model verbatim", provider, got)
		}
		return reqs[0]
	}

	t.Run("openai bearer with key and extra headers", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		ccDefine(t, s, ccInput("oa", "OA", "openai", g.base(), map[string]any{
			"api_key": ccKey, "extra_headers": []any{map[string]any{"name": "X-Tenant", "value": ccTenant}},
		}))
		r := review(g, "custom:oa")
		if r.Method != http.MethodPost || r.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s, want POST /v1/chat/completions", r.Method, r.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+ccKey {
			t.Errorf("Authorization = %q, want Bearer <key>", got)
		}
		if got := r.Header.Get("X-Tenant"); got != ccTenant {
			t.Errorf("X-Tenant = %q, want the stored value", got)
		}
		rf, _ := r.obj(t)["response_format"].(map[string]any)
		js, _ := rf["json_schema"].(map[string]any)
		if rf["type"] != "json_schema" || js["strict"] != true || js["schema"] == nil {
			t.Errorf("response_format = %v, want json_schema strict with a schema", rf)
		}
	})

	t.Run("anthropic header mode with two extra headers", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		ccDefine(t, s, ccInput("an", "AN", "anthropic", g.base(), map[string]any{
			"auth_mode": "header", "auth_header": "api-key", "api_key": ccKey,
			"extra_headers": []any{map[string]any{"name": "X-Tenant", "value": ccTenant}, map[string]any{"name": "X-Team", "value": ccTeam}},
		}))
		r := review(g, "custom:an")
		if r.Path != "/v1/messages" {
			t.Errorf("request path = %s, want /v1/messages", r.Path)
		}
		for name, want := range map[string]string{"Api-Key": ccKey, "Anthropic-Version": "2023-06-01", "X-Tenant": ccTenant, "X-Team": ccTeam} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("header %s = %q, want %q", name, got, want)
			}
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Errorf("request carries Authorization %q / x-api-key %q, want only the configured auth header", r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
		}
		body := r.obj(t)
		tools, _ := body["tools"].([]any)
		choice, _ := body["tool_choice"].(map[string]any)
		if len(tools) != 1 || choice["type"] != "tool" {
			t.Errorf("tools %v / tool_choice %v, want one forced tool", body["tools"], body["tool_choice"])
		}
	})

	t.Run("no key sends no auth header", func(t *testing.T) {
		for _, protocol := range []string{"openai", "anthropic"} {
			g := newCCGateway(t, mock.Review)
			slug := "nokey-" + protocol[:2]
			over := map[string]any{}
			if protocol == "anthropic" {
				over["auth_mode"], over["auth_header"] = "header", "x-api-key"
			}
			ccDefine(t, s, ccInput(slug, "NoKey "+protocol, protocol, g.base(), over))
			r := review(g, "custom:"+slug)
			for _, h := range []string{"Authorization", "X-Api-Key", "Api-Key"} {
				if got := r.Header.Get(h); got != "" {
					t.Errorf("%s provider without a key sent %s = %q, want none", protocol, h, got)
				}
			}
		}
	})

	t.Run("an extra anthropic-version wins", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		ccDefine(t, s, ccInput("ver", "Ver", "anthropic", g.base(), map[string]any{
			"auth_mode": "header", "api_key": ccKey,
			"extra_headers": []any{map[string]any{"name": "anthropic-version", "value": "2024-01-01"}},
		}))
		r := review(g, "custom:ver")
		if got := r.Header.Get("Anthropic-Version"); got != "2024-01-01" {
			t.Errorf("anthropic-version = %q, want the extra header's 2024-01-01", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != ccKey {
			t.Errorf("x-api-key = %q, want the key in the default auth header", got)
		}
	})
}

// ---- AC5: redirects and forbidden addresses (EC3, EC4, NFR2) ----

// ccTestConn posts a test-connection request and requires a 200.
func ccTestConn(t testing.TB, s *stack, provider string) map[string]any {
	t.Helper()
	r := s.post("/settings/test-connection", map[string]any{"provider": provider})
	if r.Status != http.StatusOK {
		t.Fatalf("POST /settings/test-connection %s = %d %s, want 200", provider, r.Status, r.Body)
	}
	return r.Obj(t)
}

// ccBrief generates the brief of pr on provider/model (the risk_brief feature model) and returns
// the answer.
func ccBrief(t testing.TB, s *stack, pr, provider, model string) response {
	t.Helper()
	setFeatureModel(t, s, "risk_brief", provider, model)
	return s.post("/pulls/"+pr+"/brief", nil)
}

// AC5 (FR3, FR8, EC3, EC4, EC15, NFR1, NFR2): a gateway that answers 302 to a second stub is not
// followed (the second stub receives nothing; the test answers ok:false, the brief 502 and the
// review run fails); a host NAME that resolves to 169.254.169.254, and one that resolves to 0.0.0.0
// where a listener waits, are refused at connect time without a request and with the
// forbidden-address message; no response, log line or stored error holds the key or a header value.
// The brief keeps its Phase 5 answer for any model failure, 502 generation_failed (spec AC5 says
// external_service; reported as a deviation), so the message is read from the review run.
func TestCustomCalls_RedirectAndForbiddenAddressesAreNeverFollowed(t *testing.T) {
	elsewhere := newCCGateway(t, mock.Review)
	redirecting := newCCGateway(t, mock.Review)
	redirecting.handle = func(r ccReq, _ int) (ccReply, bool) {
		return ccReply{Status: http.StatusFound, Location: elsewhere.srv.URL + r.Path}, true
	}
	// The unspecified address reaches this machine on Linux: the listener below would accept it.
	waiting := newCCGateway(t, mock.Review)
	_, port, _ := strings.Cut(waiting.host(), ":")
	ccFakeDNS(t, map[string]string{"link-local.test": "169.254.169.254", "unspecified.test": "0.0.0.0"})

	s := ccStack(t, spec{})
	pr := pr482(t, s)
	secretFields := map[string]any{"api_key": ccKey, "extra_headers": []any{map[string]any{"name": "X-Tenant", "value": ccTenant}}}
	ccDefine(t, s, ccInput("redir", "Redir", "openai", redirecting.base(), secretFields))
	ccDefine(t, s, ccInput("linklocal", "LinkLocal", "openai", "http://link-local.test/v1", secretFields))
	ccDefine(t, s, ccInput("unspec", "Unspec", "anthropic", "http://unspecified.test:"+port+"/v1", secretFields))

	var texts []string
	for _, id := range []string{"custom:redir", "custom:linklocal", "custom:unspec"} {
		forbidden := id != "custom:redir"
		res := ccTestConn(t, s, id)
		texts = append(texts, fmt.Sprint(res))
		if res["ok"] != false {
			t.Errorf("test-connection %s = %v, want ok:false", id, res)
		}
		if forbidden && res["message"] != "Base URL resolves to a forbidden address" {
			t.Errorf("test-connection %s message = %v, want the forbidden-address message", id, res["message"])
		}
		br := ccBrief(t, s, pr, id, "gw-model")
		texts = append(texts, string(br.Body))
		if br.Status != http.StatusBadGateway || br.errorCode(t) != "generation_failed" {
			t.Errorf("brief on %s = %d %s, want 502 generation_failed", id, br.Status, br.Body)
		}
		run, status := ccReview(t, s, pr, ccAgent(t, s, "Agent of "+id, id, "gw-model"))
		runErr := ccRunError(t, s, run)
		if status != "failed" || runErr == "" {
			t.Errorf("review on %s = %s %q, want a failed run with an error", id, status, runErr)
		}
		if forbidden && !strings.Contains(runErr, "Base URL resolves to a forbidden address") {
			t.Errorf("review on %s error = %q, want the forbidden-address message", id, runErr)
		}
		if !forbidden && !strings.Contains(runErr, "302") {
			t.Errorf("review on %s error = %q, want the gateway's 302 as a provider error", id, runErr)
		}
	}
	if got := elsewhere.requests(); len(got) != 0 {
		t.Errorf("the redirect target received %d requests (%s %s), want none", len(got), got[0].Method, got[0].Path)
	}
	if got := len(redirecting.requests()); got < 3 {
		t.Errorf("the redirecting gateway received %d requests, want its model list and its two completions", got)
	}
	if got := waiting.requests(); len(got) != 0 {
		t.Errorf("the listener behind the unspecified address received %d requests, want none (refused at connect time)", len(got))
	}
	texts = append(texts, s.logs.String(), scalar[string](t, s.db.DB, `SELECT coalesce(string_agg(error, ' '), '') FROM agent_runs`))
	ccAbsent(t, "a response, log line or stored error", ccSecrets, texts...)
}

// ---- AC9: an undefined provider (FR9, EC8) ----

// AC9 (FR9, EC8): an agent stored with an undefined custom id fails its review and its eval run
// with "Custom provider custom:ghost is not configured" and the gateway that used to be behind the
// id sees no request. The brief keeps its Phase 5 mapping of an unconfigured provider, 422
// provider_not_configured (spec AC9 says config_error; reported as a deviation).
func TestCustomCalls_UndefinedProviderFailsAsNotConfiguredWithoutARequest(t *testing.T) {
	g := newCCGateway(t, mock.Review)
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	ccDefine(t, s, ccInput("ghost", "Ghost", "openai", g.base(), map[string]any{"api_key": ccKey}))
	reviewer := ccAgent(t, s, "Ghost reviewer", "custom:ghost", "gw-model")
	evaluated := ccAgent(t, s, "Ghost evaluated", "custom:ghost", "gw-model")
	evSuiteCases(t, s, "agent", evaluated, 2)
	// The definition goes away behind the agents' backs (the delete route refuses while they use it).
	execSQL(t, s, `DELETE FROM custom_providers WHERE id = 'ghost'`)
	const want = "Custom provider custom:ghost is not configured"

	t.Run("review", func(t *testing.T) {
		run, status := ccReview(t, s, pr, reviewer)
		if got := ccRunError(t, s, run); status != "failed" || got != want {
			t.Errorf("review = %s %q, want failed %q", status, got, want)
		}
	})
	t.Run("eval run", func(t *testing.T) {
		start := evStart(s, evaluated)
		if start.Status != http.StatusAccepted {
			t.Fatalf("POST eval-runs = %d %s, want 202", start.Status, start.Body)
		}
		done := evAwait(t, s, idOf(t, start))
		sum := evRunOf(t, done)
		if sum["status"] != "failed" || sum["error"] != want || sum["cases_errored"] != float64(2) {
			t.Errorf("eval run = %v, want failed with error %q and both cases errored", sum, want)
		}
		for _, r := range done.Obj(t)["results"].([]any) {
			if res := r.(map[string]any); res["status"] != "error" || res["error"] != want {
				t.Errorf("eval result = %v, want status error with %q", res, want)
			}
		}
	})
	t.Run("brief", func(t *testing.T) {
		r := ccBrief(t, s, pr, "custom:ghost", "gw-model")
		if r.Status != http.StatusUnprocessableEntity || r.errorCode(t) != "provider_not_configured" || !strings.Contains(message(t, r), "custom:ghost") {
			t.Errorf("brief = %d %s, want 422 provider_not_configured naming custom:ghost", r.Status, r.Body)
		}
	})
	if got := g.requests(); len(got) != 0 {
		t.Errorf("the former gateway of custom:ghost received %d requests, want none", len(got))
	}
}

// ---- AC12, AC13: the cost in the ledger (FR12, FR13, EC9, EC10) ----

// ccCosts returns the cost_micros of the ledger entries of a provider by model (every entry has
// the fixed clock's instant, so the order of entries is not a time order); nil is an unknown cost.
func ccCosts(t testing.TB, s *stack, provider string) map[string][]*int64 {
	t.Helper()
	out := map[string][]*int64{}
	for _, e := range lcEntries(t, s, "") {
		if e.Provider == provider {
			out[e.Model] = append(out[e.Model], e.CostMicros)
		}
	}
	return out
}

func ccMicros(v int64) *int64 { return &v }

// sortedCosts orders known costs ascending (they are all known in the callers).
func sortedCosts(in []*int64) []*int64 {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b *int64) int { return int(*a - *b) })
	return out
}

// AC12 (FR12, EC9, EC10): the cost of an attempt is the reported usage.cost (finite, >= 0), else
// the manual price on the reported tokens, else unknown; the price books are never consulted. After
// the manual price changes, the earlier entries are unchanged and the next call uses the new price.
func TestCustomCalls_CostFollowsTheReportedCostThenTheManualPriceElseUnknown(t *testing.T) {
	g := newCCGateway(t, mock.PrBriefLlm)
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	price := func(prompt, completion float64) map[string]any {
		return map[string]any{"promptPerM": prompt, "completionPerM": completion}
	}
	models := func(promptPerM float64) []any {
		return []any{
			map[string]any{"id": "m-reported", "pricing": price(2, 8)},
			map[string]any{"id": "m-priced", "pricing": price(promptPerM, 8)},
			map[string]any{"id": "m-strcost", "pricing": price(2, 8)},
			map[string]any{"id": "m-neg"},
			map[string]any{"id": "m-nousage"},
			map[string]any{"id": "m-estimated", "pricing": price(2, 8)},
		}
	}
	def := ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{"api_key": ccKey, "models": models(2)})
	ccDefine(t, s, def)

	tokens := &ccUsage{In: 1_000_000, Out: 500_000}
	with := func(cost string) *ccUsage { return &ccUsage{In: 1_000_000, Out: 500_000, Cost: cost} }
	cases := []struct {
		model string
		usage *ccUsage
		want  *int64
	}{
		{"m-reported", with("0.0123"), ccMicros(12_300)},  // the reported cost wins over the manual price
		{"m-priced", tokens, ccMicros(6_000_000)},         // 1M x 2/M + 0.5M x 8/M = 6.0
		{"m-strcost", with(`"0.5"`), ccMicros(6_000_000)}, // a string cost is ignored, then the manual price
		{"m-neg", with("-1"), nil},                        // a negative cost is ignored, no manual price
		{"m-nousage", nil, nil},                           // no usage at all: the tokens are an estimate, never priced
		{"m-estimated", nil, nil},                         // a manual price is not applied to estimated tokens (SCR7)
		{"gpt-4.1", tokens, nil},                          // a built-in model name has no price-book price here
	}
	for _, tc := range cases {
		g.usage = tc.usage
		if r := ccBrief(t, s, pr, "custom:synapse", tc.model); r.Status != http.StatusOK {
			t.Fatalf("brief on %s = %d %s, want 200", tc.model, r.Status, r.Body)
		}
	}
	got := ccCosts(t, s, "custom:synapse")
	for _, tc := range cases {
		if diff := cmp.Diff([]*int64{tc.want}, got[tc.model]); diff != "" {
			t.Errorf("cost of %s mismatch (-want +got):\n%s", tc.model, diff)
		}
	}
	// The month spend counts the known costs; the four unknown ones are unpriced (FR13).
	if spent, unpriced := lcSpend(t, s); spent != 12_300+12_000_000 || unpriced != 4 {
		t.Errorf("GET /settings/spend = %d micro-USD, %d unpriced, want %d, 4", spent, unpriced, 12_300+12_000_000)
	}

	// EC10: the prompt price of m-priced rises from 2 to 4; the old entry keeps 6.0, the next call is 4 + 4.
	def["models"] = models(4)
	ccRedefine(t, s, def)
	g.usage = tokens
	if r := ccBrief(t, s, pr, "custom:synapse", "m-priced"); r.Status != http.StatusOK {
		t.Fatalf("brief after the price change = %d %s, want 200", r.Status, r.Body)
	}
	after := ccCosts(t, s, "custom:synapse")
	if diff := cmp.Diff([]*int64{ccMicros(6_000_000), ccMicros(8_000_000)}, sortedCosts(after["m-priced"])); diff != "" {
		t.Errorf("costs of m-priced after the price change mismatch (-want +got):\n%s", diff)
	}
	for _, tc := range cases {
		if tc.model != "m-priced" {
			if diff := cmp.Diff(got[tc.model], after[tc.model]); diff != "" {
				t.Errorf("earlier entry of %s changed with the price (-before +after):\n%s", tc.model, diff)
			}
		}
	}
	// The brief row keeps the cost of its latest call.
	if cost := scalar[float64](t, s.db.DB, `SELECT (json->>'cost_usd')::float8 FROM pr_brief WHERE pr_id = ?`, pr); cost != 8 {
		t.Errorf("stored brief cost_usd = %v, want 8", cost)
	}
}

// AC13 (FR13): a month with three records of unknown cost (two real calls of a custom provider, one
// built-in entry) and priced records reports unpriced_count 3 and the same spent_usd and
// cap_reached as the priced records alone.
func TestCustomCalls_UnpricedCountCoversCustomAndBuiltInRecords(t *testing.T) {
	g := newCCGateway(t, mock.PrBriefLlm)
	g.usage = nil // no usage: both calls are unknown-cost entries
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	ws := workspaceID(t, s)
	ccDefine(t, s, ccInput("synapse", "Synapse", "openai", g.base(), nil))
	requireNoSpend(t, s)

	seedLedgerSpend(t, s, ws, ccNow, 1.25)
	seedLedgerSpend(t, s, ws, ccNow.Add(-time.Hour), 0.75)
	seedLedgerUnpriced(t, s, ws, ccNow) // the built-in record
	for range 2 {
		if r := ccBrief(t, s, pr, "custom:synapse", "m"); r.Status != http.StatusOK {
			t.Fatalf("brief = %d %s, want 200", r.Status, r.Body)
		}
	}
	status := func() map[string]any {
		r := s.get("/settings/spend")
		if r.Status != http.StatusOK {
			t.Fatalf("GET /settings/spend = %d %s, want 200", r.Status, r.Body)
		}
		return r.Obj(t)
	}
	got := status()
	if got["unpriced_count"] != float64(3) || got["spent_usd"] != 2.0 || got["cap_reached"] != false {
		t.Errorf("GET /settings/spend = %v, want unpriced_count 3, spent_usd 2, cap_reached false", got)
	}
	// The unknown costs count as 0 for the cap: a cap of exactly the priced total is reached, one
	// cent above it is not.
	capNow := func(usd float64) map[string]any {
		if put := s.do(http.MethodPut, "/settings", map[string]any{"monthly_spend_cap_usd": usd}); put.Status != http.StatusOK {
			t.Fatalf("PUT /settings cap %v = %d %s, want 200", usd, put.Status, put.Body)
		}
		return status()
	}
	if got := capNow(2.0); got["cap_reached"] != true || got["spent_usd"] != 2.0 || got["unpriced_count"] != float64(3) {
		t.Errorf("GET /settings/spend at a cap of 2 = %v, want cap_reached true, spent_usd 2, unpriced_count 3", got)
	}
	if got := capNow(2.01); got["cap_reached"] != false {
		t.Errorf("GET /settings/spend at a cap of 2.01 = %v, want cap_reached false", got)
	}
}

// ---- AC39-AC41: the structured-output modes (FR33, EC15, EC31, EC32) ----

// ccReplyText answers a request with text in the shape of its protocol (a forced tool call for an
// Anthropic request that has tools).
func ccReplyText(t testing.TB, req ccReq, text string) (ccReply, bool) {
	t.Helper()
	return ccReply{Body: ccAnswerBody(t, req, text, &ccUsage{In: 1200, Out: 180})}, true
}

// ccRejects400 is the answer of a gateway that does not support the structured output of the
// request: a 400 whose message echoes the key.
func ccRejects400() (ccReply, bool) {
	return ccReply{Status: http.StatusBadRequest, Body: `{"error":{"message":"structured output is not supported here; rejected credential ` + ccKey + `"}}`}, true
}

// ccNoStructured fails the test when the request carries any structured-output field.
func ccNoStructured(t testing.TB, what string, r ccReq) {
	t.Helper()
	body := r.obj(t)
	for _, field := range []string{"response_format", "tools", "tool_choice"} {
		if _, has := body[field]; has {
			t.Errorf("%s request carries %s, want none", what, field)
		}
	}
}

// ccSchemaInSystem fails the test unless the system text holds the review schema and the demand
// for exactly one JSON object.
func ccSchemaInSystem(t testing.TB, what string, r ccReq) {
	t.Helper()
	sys := r.system(t)
	for _, want := range []string{"exactly one JSON object", `"verdict"`, `"findings"`} {
		if !strings.Contains(sys, want) {
			t.Errorf("%s system message does not hold %q", what, want)
		}
	}
}

// AC39 (FR33): in json_object mode the gateway sees {"type":"json_object"}, no json_schema and no
// tools, and a system message with the schema; its valid answer is stored as findings.
func TestCustomCalls_JSONObjectModeSendsTheSchemaInThePrompt(t *testing.T) {
	g := newCCGateway(t, mock.Review)
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	ccDefine(t, s, ccInput("jo", "JO", "openai", g.base(), map[string]any{"structured_output": "json_object"}))
	run, status := ccReview(t, s, pr, ccAgent(t, s, "JSON object agent", "custom:jo", "gw-model"))
	if status != "done" {
		t.Fatalf("review = %s (%s), want done", status, ccRunError(t, s, run))
	}
	reqs := g.completions()
	if len(reqs) != 1 {
		t.Fatalf("gateway received %d completions, want 1", len(reqs))
	}
	body := reqs[0].obj(t)
	if diff := cmp.Diff(map[string]any{"type": "json_object"}, body["response_format"]); diff != "" {
		t.Errorf("response_format mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(string(reqs[0].Body), "json_schema") {
		t.Error("the request body mentions json_schema")
	}
	if _, has := body["tools"]; has {
		t.Error("the request carries tools")
	}
	ccSchemaInSystem(t, "json_object", reqs[0])
	if n := ccFindings(t, s, run); n != 2 {
		t.Errorf("stored findings = %d, want the 2 of the gateway's answer", n)
	}
}

// AC40 (FR33, EC31): prompt_only sends no response_format, tools or tool_choice on either protocol
// and puts the schema in the system prompt; an Anthropic answer split over two text blocks is
// joined; prose around a fenced JSON block is extracted; an answer missing a required field is
// re-prompted once with the issues; three invalid answers fail the run after three requests.
func TestCustomCalls_PromptOnlyModeExtractsValidatesAndReprompts(t *testing.T) {
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	valid := ccReviewText(t, "A valid review")
	review := func(g *ccGateway, slug string) (run, status string) {
		t.Helper()
		return ccReview(t, s, pr, ccAgent(t, s, "Prompt only agent "+slug, "custom:"+slug, "gw-model"))
	}

	t.Run("openai: a first answer missing a field is re-prompted once", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		g.handle = func(r ccReq, n int) (ccReply, bool) {
			if !r.completion() {
				return ccReply{}, false
			}
			if n == 1 {
				return ccReplyText(t, r, ccWithout(t, valid, "verdict"))
			}
			return ccReplyText(t, r, valid)
		}
		ccDefine(t, s, ccInput("po-oa", "PO OA", "openai", g.base(), map[string]any{"structured_output": "prompt_only"}))
		run, status := review(g, "po-oa")
		if status != "done" {
			t.Fatalf("review = %s (%s), want done", status, ccRunError(t, s, run))
		}
		reqs := g.completions()
		if len(reqs) != 2 {
			t.Fatalf("gateway received %d completions, want 2 (one re-prompt)", len(reqs))
		}
		for i, r := range reqs {
			ccNoStructured(t, fmt.Sprintf("openai prompt_only #%d", i+1), r)
			ccSchemaInSystem(t, fmt.Sprintf("openai prompt_only #%d", i+1), r)
		}
		if got := reqs[1].lastUser(t); !strings.Contains(got, "did not match the required schema") || !strings.Contains(got, "verdict") {
			t.Errorf("re-prompt = %q, want the schema issues naming the missing field", got)
		}
		if n := ccFindings(t, s, run); n != 2 {
			t.Errorf("stored findings = %d, want 2 from the second answer", n)
		}
	})

	t.Run("openai: prose around a fenced JSON block is extracted", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		g.handle = func(r ccReq, _ int) (ccReply, bool) {
			if !r.completion() {
				return ccReply{}, false
			}
			return ccReplyText(t, r, "Sure, here is my review:\n```json\n"+valid+"\n```\nHope that helps.")
		}
		ccDefine(t, s, ccInput("po-fence", "PO Fence", "openai", g.base(), map[string]any{"structured_output": "prompt_only"}))
		run, status := review(g, "po-fence")
		if status != "done" || len(g.completions()) != 1 || ccFindings(t, s, run) != 2 {
			t.Errorf("review = %s (%s) after %d requests with %d findings, want done after 1 with 2", status, ccRunError(t, s, run), len(g.completions()), ccFindings(t, s, run))
		}
	})

	t.Run("anthropic: two text blocks are joined", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		half := len(valid) / 2
		g.handle = func(r ccReq, _ int) (ccReply, bool) {
			if !r.completion() {
				return ccReply{}, false
			}
			return ccReply{Body: ccTextBlocks(t, &ccUsage{In: 1200, Out: 180}, valid[:half], valid[half:])}, true
		}
		ccDefine(t, s, ccInput("po-an", "PO AN", "anthropic", g.base(), map[string]any{"structured_output": "prompt_only"}))
		run, status := review(g, "po-an")
		if status != "done" {
			t.Fatalf("review = %s (%s), want done", status, ccRunError(t, s, run))
		}
		reqs := g.completions()
		if len(reqs) != 1 || reqs[0].Path != "/v1/messages" {
			t.Fatalf("gateway received %d completions (%v), want 1 at /v1/messages", len(reqs), reqs)
		}
		ccNoStructured(t, "anthropic prompt_only", reqs[0])
		ccSchemaInSystem(t, "anthropic prompt_only", reqs[0])
		if n := ccFindings(t, s, run); n != 2 {
			t.Errorf("stored findings = %d, want 2", n)
		}
	})

	t.Run("openai: three invalid answers fail the run", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		g.handle = func(r ccReq, _ int) (ccReply, bool) {
			if !r.completion() {
				return ccReply{}, false
			}
			return ccReplyText(t, r, ccWithout(t, valid, "summary"))
		}
		ccDefine(t, s, ccInput("po-bad", "PO Bad", "openai", g.base(), map[string]any{"structured_output": "prompt_only"}))
		run, status := review(g, "po-bad")
		msg := ccRunError(t, s, run)
		if status != "failed" || len(g.completions()) != 3 || !strings.Contains(msg, "failed schema validation") || !strings.Contains(msg, "after 3 attempt(s)") {
			t.Errorf("review = %s %q after %d requests, want failed with the schema-validation message after 3", status, msg, len(g.completions()))
		}
	})
}

// AC41 (FR33, EC15, EC32): an OpenAI-protocol provider in strict mode whose gateway answers 2xx to
// /models and 400 to json_schema passes the connection test; the first review fails with the
// scrubbed gateway message and the hint; the gateway never gets a request without json_schema
// (the call is not repeated in another mode); an Anthropic-protocol hint ends with "Prompt only.";
// after a PUT to json_object the next review succeeds; a 400 in json_object mode carries no hint.
func TestCustomCalls_StrictRejectionFailsWithTheHintAndTheModeCanBeSwitched(t *testing.T) {
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	const hintOpenAI = "If this gateway does not support strict structured output, set Structured output of Synapse to JSON object or Prompt only."
	const hintAnthropic = "If this gateway does not support strict structured output, set Structured output of Claude GW to Prompt only."

	t.Run("openai", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		g.handle = func(r ccReq, _ int) (ccReply, bool) {
			if r.completion() {
				if rf, _ := r.obj(t)["response_format"].(map[string]any); rf["type"] == "json_schema" {
					return ccRejects400()
				}
			}
			return ccReply{}, false
		}
		def := ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{"api_key": ccKey})
		ccDefine(t, s, def)
		if res := ccTestConn(t, s, "custom:synapse"); res["ok"] != true {
			t.Errorf("test-connection = %v, want ok:true (no completion is made)", res)
		}
		agent := ccAgent(t, s, "Strict agent", "custom:synapse", "gw-model")
		run, status := ccReview(t, s, pr, agent)
		msg := ccRunError(t, s, run)
		if status != "failed" || !strings.Contains(msg, "[REDACTED]") || !strings.Contains(msg, hintOpenAI) {
			t.Errorf("strict review = %s %q, want failed with the scrubbed gateway message and %q", status, msg, hintOpenAI)
		}
		ccAbsent(t, "the run error", ccSecrets, msg)
		reqs := g.completions()
		if len(reqs) != 1 {
			t.Errorf("gateway received %d completions, want 1 (the 400 is not retried)", len(reqs))
		}
		for _, r := range reqs {
			if rf, _ := r.obj(t)["response_format"].(map[string]any); rf["type"] != "json_schema" {
				t.Errorf("gateway got a request in another mode: response_format %v", rf)
			}
		}

		// The user switches the mode; the next call uses it (EC32).
		def["structured_output"] = "json_object"
		delete(def, "api_key")
		ccRedefine(t, s, def)
		if run, status := ccReview(t, s, pr, agent); status != "done" {
			t.Errorf("review after the switch to json_object = %s (%s), want done", status, ccRunError(t, s, run))
		}
		// A gateway that also rejects json_object: the failure carries no hint.
		g.handle = func(r ccReq, _ int) (ccReply, bool) {
			if r.completion() {
				return ccRejects400()
			}
			return ccReply{}, false
		}
		run, status = ccReview(t, s, pr, agent)
		if msg := ccRunError(t, s, run); status != "failed" || strings.Contains(msg, "If this gateway") {
			t.Errorf("json_object review with a 400 = %s %q, want failed without the hint", status, msg)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		g := newCCGateway(t, mock.Review)
		g.handle = func(r ccReq, _ int) (ccReply, bool) {
			if r.completion() && r.obj(t)["tools"] != nil {
				return ccRejects400()
			}
			return ccReply{}, false
		}
		ccDefine(t, s, ccInput("claude-gw", "Claude GW", "anthropic", g.base(), map[string]any{"api_key": ccKey, "auth_mode": "header"}))
		run, status := ccReview(t, s, pr, ccAgent(t, s, "Strict Anthropic agent", "custom:claude-gw", "gw-model"))
		msg := ccRunError(t, s, run)
		if status != "failed" || !strings.Contains(msg, hintAnthropic) || strings.Contains(msg, "JSON object") {
			t.Errorf("strict anthropic review = %s %q, want failed ending with %q", status, msg, hintAnthropic)
		}
		ccAbsent(t, "the run error", ccSecrets, msg)
		if n := len(g.completions()); n != 1 {
			t.Errorf("gateway received %d completions, want 1", n)
		}
	})
}

// FR33: the brief and the intent use the provider's mode too (the schema is in the system prompt
// and the structured fields are absent), and their answers are stored.
func TestCustomCalls_BriefAndIntentFollowTheProvidersMode(t *testing.T) {
	s := ccStack(t, spec{})
	pr := pr482(t, s)

	brief := newCCGateway(t, mock.PrBriefLlm)
	ccDefine(t, s, ccInput("brief-po", "Brief PO", "openai", brief.base(), map[string]any{"structured_output": "prompt_only"}))
	if r := ccBrief(t, s, pr, "custom:brief-po", "gw-model"); r.Status != http.StatusOK {
		t.Fatalf("brief = %d %s, want 200", r.Status, r.Body)
	}
	reqs := brief.completions()
	if len(reqs) != 1 {
		t.Fatalf("brief gateway received %d completions, want 1", len(reqs))
	}
	ccNoStructured(t, "brief prompt_only", reqs[0])
	if sys := reqs[0].system(t); !strings.Contains(sys, "exactly one JSON object") {
		t.Errorf("brief system message = %q, want the schema block", sys)
	}

	intent := newCCGateway(t, mock.IntentClassification)
	ccDefine(t, s, ccInput("intent-jo", "Intent JO", "openai", intent.base(), map[string]any{"structured_output": "json_object"}))
	setFeatureModel(t, s, "review_intent", "custom:intent-jo", "gw-model")
	if r := s.post("/pulls/"+pr+"/intent/refresh", nil); r.Status != http.StatusOK {
		t.Fatalf("intent refresh = %d %s, want 200", r.Status, r.Body)
	}
	reqs = intent.completions()
	if len(reqs) != 1 {
		t.Fatalf("intent gateway received %d completions, want 1", len(reqs))
	}
	if diff := cmp.Diff(map[string]any{"type": "json_object"}, reqs[0].obj(t)["response_format"]); diff != "" {
		t.Errorf("intent response_format mismatch (-want +got):\n%s", diff)
	}
	if sys := reqs[0].system(t); !strings.Contains(sys, "exactly one JSON object") {
		t.Errorf("intent system message = %q, want the schema block", sys)
	}
}

// ---- the eval run (FR8, FR12) ----

// An eval run on an agent whose provider is custom calls the gateway once per case with the
// provider's auth, and its cases cost what the manual price says for the reported tokens.
func TestCustomCalls_EvalRunCallsTheGatewayAndPricesTheCases(t *testing.T) {
	g := newCCGateway(t, mock.Review)
	g.usage = &ccUsage{In: 1000, Out: 500}
	s := ccStack(t, spec{})
	ccDefine(t, s, ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{
		"api_key": ccKey,
		"models":  []any{map[string]any{"id": "gw-model", "pricing": map[string]any{"promptPerM": 2, "completionPerM": 8}}},
	}))
	agent := ccAgent(t, s, "Eval agent", "custom:synapse", "gw-model")
	cases := evSuiteCases(t, s, "agent", agent, 2)

	start := evStart(s, agent)
	if start.Status != http.StatusAccepted {
		t.Fatalf("POST eval-runs = %d %s, want 202", start.Status, start.Body)
	}
	done := evAwait(t, s, idOf(t, start))
	sum := evRunOf(t, done)
	if sum["status"] != "completed" || sum["cases_errored"] != float64(0) {
		t.Fatalf("eval run = %v, want completed without errors", sum)
	}
	reqs := g.completions()
	if len(reqs) != 2 {
		t.Fatalf("gateway received %d completions, want one per case (2)", len(reqs))
	}
	for _, r := range reqs {
		if got := r.Header.Get("Authorization"); got != "Bearer "+ccKey {
			t.Errorf("eval request Authorization = %q, want Bearer <key>", got)
		}
	}
	// 1000 x 2/M + 500 x 8/M = $0.006 per case.
	for _, r := range done.Obj(t)["results"].([]any) {
		if res := r.(map[string]any); res["cost_usd"] != 0.006 {
			t.Errorf("eval result cost_usd = %v, want 0.006", res["cost_usd"])
		}
	}
	if sum["cost_usd"] != 0.012 {
		t.Errorf("eval run cost_usd = %v, want 0.012", sum["cost_usd"])
	}
	var entries []lcEntry
	for _, e := range lcEntries(t, s, "eval_case") {
		entries = append(entries, e)
		if e.Provider != "custom:synapse" || e.CostMicros == nil || *e.CostMicros != 6000 {
			t.Errorf("eval_case entry = %s cost %v, want custom:synapse at 6000 micro-USD", e.Provider, e.CostMicros)
		}
	}
	lcCheckRefs(t, "eval_case", entries, cases...)
}

// ---- AC18: one log line per attempt, no secrets (NFR6, NFR1) ----

// AC18 (NFR6, NFR1): a review through a custom provider logs exactly one "custom provider request"
// line per attempt - a 500 and then the 200 are two - with provider id, host, operation, status,
// attempt and latency; no log line, response or stored error holds the key, a header value, the
// prompt or the answer.
func TestCustomCalls_OneLogLinePerAttemptAndNoSecretsInTheLog(t *testing.T) {
	g := newCCGateway(t, mock.Review)
	answerText := ccReviewText(t, ccSummary)
	g.handle = func(r ccReq, n int) (ccReply, bool) {
		if !r.completion() {
			return ccReply{}, false
		}
		if n == 1 {
			return ccReply{Status: http.StatusInternalServerError, Body: `{"error":{"message":"upstream failure for ` + ccKey + ` ` + ccTenant + `"}}`}, true
		}
		return ccReplyText(t, r, answerText)
	}
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	ccDefine(t, s, ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{
		"api_key": ccKey, "extra_headers": []any{map[string]any{"name": "X-Tenant", "value": ccTenant}},
	}))
	run, status := ccReview(t, s, pr, ccAgent(t, s, "Log agent", "custom:synapse", "gw-model"))
	if status != "done" {
		t.Fatalf("review = %s (%s), want done after the retry", status, ccRunError(t, s, run))
	}
	if got := len(g.completions()); got != 2 {
		t.Fatalf("gateway received %d completions, want 2 (the 500 and the retry)", got)
	}

	lines := logLines(t, s.logs, "custom provider request")
	if len(lines) != 2 {
		t.Fatalf("log holds %d custom provider request lines, want one per attempt (2): %v", len(lines), lines)
	}
	for i, wantStatus := range []string{"500", "2xx"} {
		l := lines[i]
		latency, _ := l["latency_ms"].(float64)
		if l["provider"] != "custom:synapse" || l["host"] != g.host() || l["operation"] != "complete" ||
			l["status"] != wantStatus || l["attempt"] != float64(i+1) || latency < 0 || l["level"] != "INFO" {
			t.Errorf("log line %d = %v, want INFO provider custom:synapse host %s operation complete status %s attempt %d and a latency", i+1, l, g.host(), wantStatus, i+1)
		}
		if _, has := l["latency_ms"]; !has {
			t.Errorf("log line %d has no latency_ms", i+1)
		}
	}
	secrets := append([]string{ccPrompt, ccSummary}, ccSecrets...)
	ccAbsent(t, "the server log", secrets, s.logs.String())
	traceBody := waitTrace(t, s, run).Body
	ccAbsent(t, "a response", ccSecrets, string(traceBody))
	ccAbsent(t, "a stored error", ccSecrets, scalar[string](t, s.db.DB, `SELECT coalesce(string_agg(error, ' '), '') FROM agent_runs`))
}

// NFR6: a test-connection and a model-list GET each write one "custom provider request" line with
// operation list_models, the status of the gateway's answer and no secret.
func TestCustomCalls_ModelListWritesOneLogLineWithOperationListModels(t *testing.T) {
	g := newCCGateway(t, mock.Review)
	status := http.StatusOK
	g.handle = func(r ccReq, _ int) (ccReply, bool) {
		if r.completion() || status == http.StatusOK {
			return ccReply{}, false
		}
		return ccReply{Status: status, Body: `{"error":{"message":"no list for ` + ccKey + ` ` + ccTenant + `"}}`}, true
	}
	s := ccStack(t, spec{})
	ccDefine(t, s, ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{
		"api_key": ccKey, "extra_headers": []any{map[string]any{"name": "X-Tenant", "value": ccTenant}},
	}))

	want := func(what string, n int, wantStatus string) {
		t.Helper()
		lines := logLines(t, s.logs, "custom provider request")
		if len(lines) != n {
			t.Fatalf("%s: log holds %d custom provider request lines, want %d: %v", what, len(lines), n, lines)
		}
		l := lines[n-1]
		latency, has := l["latency_ms"].(float64)
		if l["provider"] != "custom:synapse" || l["host"] != g.host() || l["operation"] != "list_models" ||
			l["status"] != wantStatus || l["attempt"] != float64(1) || !has || latency < 0 || l["level"] != "INFO" {
			t.Errorf("%s: log line = %v, want INFO provider custom:synapse host %s operation list_models status %s attempt 1 and a latency", what, l, g.host(), wantStatus)
		}
	}

	if res := ccTestConn(t, s, "custom:synapse"); res["ok"] != true {
		t.Fatalf("test-connection = %v, want ok:true", res)
	}
	want("test-connection", 1, "2xx")
	if r := s.get("/providers/custom:synapse/models"); r.Status != http.StatusOK {
		t.Fatalf("GET models = %d %s, want 200", r.Status, r.Body)
	}
	want("GET models", 2, "2xx")

	status = http.StatusNotFound
	if res := ccTestConn(t, s, "custom:synapse"); res["ok"] != true {
		t.Fatalf("test-connection with a 404 list = %v, want ok:true with the warning", res)
	}
	want("test-connection 404", 3, "404")
	status = http.StatusInternalServerError
	if r := s.get("/providers/custom:synapse/models"); r.Status != http.StatusOK {
		t.Fatalf("GET models with a 500 list = %d %s, want 200 (the manual list)", r.Status, r.Body)
	}
	want("GET models 500", 4, "500")

	ccAbsent(t, "the server log", ccSecrets, s.logs.String())
}

// ---- AC15: bundles and workspace exports (FR15, NFR1) ----

// AC15 (FR15, EC16, NFR1): the plugin export and the workspace export of agents, a default model
// and feature models on custom:synapse hold the provider strings and none of the base URL, the auth
// header name, the key, the header value or the manual models; the bundle imported where the
// provider is not defined warns once and installs the agents with that provider; custom:Bad! is a
// 422 invalid_plugin.
func TestCustomCalls_ExportsHoldTheProviderStringsButNoDefinitionOrSecret(t *testing.T) {
	g := newCCGateway(t, mock.Review)
	s := ccStack(t, spec{})
	const manualModel = "MANUAL-MODEL-ONLY-77c1"
	const authHeader = "x-synapse-auth-name"
	ccDefine(t, s, ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{
		"auth_mode": "header", "auth_header": authHeader, "api_key": ccKey,
		"extra_headers": []any{map[string]any{"name": "X-Tenant", "value": ccTenant}},
		"models":        []any{map[string]any{"id": manualModel, "label": "Manual", "pricing": map[string]any{"promptPerM": 1, "completionPerM": 2}}},
	}))
	agentA := ccAgent(t, s, "Exported A", "custom:synapse", "gw-model")
	ccAgent(t, s, "Exported B", "custom:synapse", "other-model")
	choice := map[string]any{"provider": "custom:synapse", "model": "gw-model"}
	if put := s.do(http.MethodPut, "/settings", map[string]any{"default_model": choice, "feature_models": map[string]any{"conventions": choice}}); put.Status != http.StatusOK {
		t.Fatalf("PUT /settings = %d %s, want 200", put.Status, put.Body)
	}
	needles := []string{g.srv.URL, g.host(), authHeader, manualModel, `"Manual"`, "Synapse", ccKey, ccTenant, "X-Tenant", "api_key"}

	// The plugin export.
	exported := s.post("/plugins/export", map[string]any{"agent_ids": []string{agentA}})
	bundle := sdExportBundle(t, exported)
	_, files := sdUnzip(t, exported.Body)
	text := string(files["devdigest-plugin.json"])
	if !strings.Contains(text, `"provider":"custom:synapse"`) {
		t.Errorf("plugin export = %s, want the agent's provider string custom:synapse", text)
	}
	ccAbsent(t, "the plugin export", needles, text)

	// The workspace export keeps the default and feature model choices as strings.
	ws := s.get("/workspace/export")
	if ws.Status != http.StatusOK {
		t.Fatalf("GET /workspace/export = %d %s, want 200", ws.Status, ws.Body)
	}
	if n := strings.Count(string(ws.Body), `"custom:synapse"`); n < 2 {
		t.Errorf("workspace export holds custom:synapse %d times, want the default and the feature model: %s", n, ws.Body)
	}
	ccAbsent(t, "the workspace export", needles, string(ws.Body))

	// Import where the provider is not defined.
	other := ccStack(t, spec{})
	preview := sdPreview(t, other, bundle)
	if preview.Status != http.StatusOK {
		t.Fatalf("dry-run import = %d %s, want 200", preview.Status, preview.Body)
	}
	if diff := cmp.Diff([]any{"Provider custom:synapse is not configured here"}, preview.Obj(t)["warnings"]); diff != "" {
		t.Errorf("dry-run warnings mismatch (-want +got):\n%s", diff)
	}
	sdInstall(t, other, bundle, "exported.zip")
	if n := count(t, other.db.DB, `SELECT count(*) FROM agents WHERE name = 'Exported A' AND provider = 'custom:synapse'`); n != 1 {
		t.Errorf("installed agents on custom:synapse named Exported A = %d, want 1", n)
	}

	// A malformed provider is refused.
	var bad sdDoc
	if err := json.Unmarshal(sdMarshal(t, bundle), &bad); err != nil {
		t.Fatal(err)
	}
	bad["agents"].([]any)[0].(map[string]any)["provider"] = "custom:Bad!"
	if got := sdPreview(t, other, bad); got.Status != http.StatusUnprocessableEntity {
		t.Errorf("import of provider custom:Bad! = %d %s, want 422", got.Status, got.Body)
	} else if code, _ := sdErr(t, got); code != "invalid_plugin" {
		t.Errorf("import of provider custom:Bad! error code = %q, want invalid_plugin", code)
	}
}

// ---- AC17: timeouts, retries, the response cap and the total budget (NFR4) ----

// ccResolver resolves every custom id to one call.
type ccResolver struct{ call llm.CustomCall }

func (r ccResolver) Resolve(context.Context, string, llm.Provider) (llm.CustomCall, error) {
	return r.call, nil
}

// ccTimingClient builds the client of a custom OpenAI-compatible provider the way the server does
// (customAdapters.Build: the default policy of NFR4) over an in-memory gateway, so that the 120 s,
// 30 s and 360 s of the policy elapse on the synctest clock.
func ccTimingClient(t *testing.T, srv *httptest.Server) *llm.Client {
	t.Helper()
	hc := srv.Client() // before srv.URL: the in-memory server has no URL until then
	p := cpdomain.Provider{
		ID: "custom:gw", Name: "GW", Protocol: custom.ProtocolOpenAI, BaseURL: srv.URL + "/v1",
		AuthMode: custom.AuthBearer, StructuredOutput: custom.ModeStrict,
	}
	ad, err := customAdapters{hc: hc}.Build(p, secrets.CustomSecret{Key: ccKey})
	if err != nil {
		t.Fatal(err)
	}
	c := llm.New(llm.Options{
		Keys: noKeyReader{}, Prices: llm.NewPriceBook(llm.PriceBookOptions{}),
		Custom: ccResolver{llm.CustomCall{Adapter: ad, Name: "GW", Protocol: p.Protocol, Host: "gw.test", Mode: custom.ModeStrict, Secrets: []string{ccKey}}},
	})
	t.Cleanup(c.Close)
	return c
}

// ccAsk makes one structured call whose valid answer is {"ok":true}.
func ccAsk(ctx context.Context, c *llm.Client) (llm.Result, error) {
	return c.Structured(ctx, llm.Request{
		Provider: "custom:gw", Model: "m", SchemaName: "Ok",
		Schema:   json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
		Messages: []llm.Message{{Role: llm.User, Content: "answer"}},
	})
}

// ccChat is a chat-completions answer carrying text.
func ccChat(text string) string {
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}})
	return string(raw)
}

// AC17 (NFR4): the policy a custom provider is built with is the built-in OpenAI limits; on a fake
// clock a gateway that hangs times out at 120 s (the model list at 30 s) and is not retried; three
// 500s then a 200 succeed on the fourth request and a fourth 500 gives up; a 17 MiB body fails as a
// provider error; a call that keeps failing slowly never exceeds the 360 s budget.
func TestCustomCalls_TimeoutsRetriesResponseCapAndBudget(t *testing.T) {
	t.Run("the wired policy is the OpenAI limits", func(t *testing.T) {
		for _, protocol := range []custom.Protocol{custom.ProtocolOpenAI, custom.ProtocolAnthropic} {
			p := cpdomain.Provider{ID: "custom:gw", Name: "GW", Protocol: protocol, BaseURL: "https://gw.example/v1", AuthMode: custom.AuthBearer}
			ad, err := newCustomAdapters(nil).Build(p, secrets.CustomSecret{})
			if err != nil {
				t.Fatal(err)
			}
			want := llm.Policy{RequestTimeout: 120 * time.Second, Retries: 3, ListTimeout: 30 * time.Second, ListRetries: 3}
			if diff := cmp.Diff(want, ad.Policy()); diff != "" {
				t.Errorf("%s Policy() mismatch (-want +got):\n%s", protocol, diff)
			}
		}
		if llm.DefaultTotalBudget != 360*time.Second {
			t.Errorf("DefaultTotalBudget = %s, want 360s", llm.DefaultTotalBudget)
		}
	})

	t.Run("a hanging gateway times out at 120 s and the model list at 30 s, without a retry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = io.Copy(io.Discard, r.Body) // the server notices a closed connection once the body is read
				<-r.Context().Done()
			}))
			c := ccTimingClient(t, srv)

			start := time.Now()
			_, err := ccAsk(t.Context(), c)
			if took := time.Since(start); took != 120*time.Second || err == nil || !strings.Contains(err.Error(), "timed out after 120000ms") || hits.Load() != 1 {
				t.Errorf("hanging completion took %s with error %v after %d requests, want 120s, a timeout and 1 request", took, err, hits.Load())
			}

			hits.Store(0)
			start = time.Now()
			_, err = c.ListModelsIn(t.Context(), "ws", "custom:gw")
			if took := time.Since(start); took != 30*time.Second || err == nil || !strings.Contains(err.Error(), "model list timed out after 30s") || hits.Load() != 1 {
				t.Errorf("hanging model list took %s with error %v after %d requests, want 30s, a timeout and 1 request", took, err, hits.Load())
			}
		})
	})

	t.Run("three 500s then a 200 succeed on the fourth request; a fourth 500 gives up", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var hits atomic.Int32
			failures := 3
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if int(hits.Add(1)) <= failures {
					http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusInternalServerError)
					return
				}
				_, _ = io.WriteString(w, ccChat(`{"ok":true}`))
			}))
			c := ccTimingClient(t, srv)

			res, err := ccAsk(t.Context(), c)
			if err != nil || string(res.Data) != `{"ok":true}` || hits.Load() != 4 {
				t.Errorf("call after three 500s = %s, %v after %d requests, want the answer on the 4th request", res.Data, err, hits.Load())
			}

			hits.Store(0)
			failures = 100
			_, err = ccAsk(t.Context(), c)
			if err == nil || !strings.Contains(err.Error(), "500") || hits.Load() != 4 {
				t.Errorf("call with only 500s = %v after %d requests, want a provider error after 4 requests (1 + 3 retries)", err, hits.Load())
			}
		})
	})

	t.Run("a 17 MiB body fails as a provider error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, ccChat(strings.Repeat("a", 17<<20)))
			}))
			c := ccTimingClient(t, srv)
			_, err := ccAsk(t.Context(), c)
			if err == nil || !strings.Contains(err.Error(), "unreadable response") || hits.Load() != 1 {
				t.Errorf("call with a 17 MiB body = %v after %d requests, want an unreadable-response provider error after 1 request", err, hits.Load())
			}
		})
	})

	t.Run("a call never exceeds the 360 s budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select { // 100 s per request, then a 500: four requests would take over 400 s
				case <-time.After(100 * time.Second):
					http.Error(w, "slow failure", http.StatusInternalServerError)
				case <-r.Context().Done():
				}
			}))
			c := ccTimingClient(t, srv)
			start := time.Now()
			_, err := ccAsk(t.Context(), c)
			if took := time.Since(start); took != 360*time.Second || err == nil || !strings.Contains(err.Error(), "exceeded its total budget of 360000ms") {
				t.Errorf("slowly failing call took %s with error %v, want it cut at exactly 360s by the budget", took, err)
			}
		})
	})
}

// ---- fix round r1 (security r1: L1, L2, L4 and the INFO items) ----

// L1: a gateway that echoes a partial copy of the key ("ab...yz") on a rejection leaves no part of
// it in the run error, the trace, the stored errors or the log; a 401 carries a fixed message.
func TestCustomCalls_PartialKeyEchoesNeverReachErrorsTraceOrLogs(t *testing.T) {
	const (
		head = "SYNAPSE-KE"
		tail = "9f3a7c21"
	)
	echo := `{"error":{"message":"Incorrect API key provided: ` + head + `...` + tail + `e5 (header ` + ccTenant[:12] + `)"}}`
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{"401 gets the fixed message", http.StatusUnauthorized, "returned 401: API key rejected by the provider"},
		{"400 is scrubbed of fragments", http.StatusBadRequest, "returned 400: Incorrect API key provided: [REDACTED]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newCCGateway(t, mock.Review)
			g.handle = func(r ccReq, _ int) (ccReply, bool) {
				if r.completion() {
					return ccReply{Status: tc.status, Body: echo}, true
				}
				return ccReply{}, false
			}
			s := ccStack(t, spec{})
			pr := pr482(t, s)
			ccDefine(t, s, ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{
				"api_key": ccKey, "structured_output": "prompt_only",
				"extra_headers": []any{map[string]any{"name": "X-Tenant", "value": ccTenant}},
			}))
			run, status := ccReview(t, s, pr, ccAgent(t, s, "Echo agent", "custom:synapse", "gw-model"))
			msg := ccRunError(t, s, run)
			if status != "failed" || !strings.Contains(msg, tc.want) {
				t.Errorf("review = %s %q, want failed with %q", status, msg, tc.want)
			}
			parts := []string{head, tail, ccTenant[:12]}
			ccAbsent(t, "the run error", parts, msg)
			ccAbsent(t, "the trace", parts, string(waitTrace(t, s, run).Body))
			ccAbsent(t, "the server log", parts, s.logs.String())
			ccAbsent(t, "the stored errors", parts, scalar[string](t, s.db.DB, `SELECT coalesce(string_agg(error, ' '), '') FROM agent_runs`))
		})
	}
}

// L2: hop-by-hop and connection-specific names are refused as extra headers with a 422.
func TestCustomCalls_HopByHopExtraHeadersAreRefused(t *testing.T) {
	s := ccStack(t, spec{})
	for _, name := range []string{"Upgrade", "keep-alive", "Proxy-Connection", "TE", "Trailer"} {
		r := s.post("/settings/custom-providers", ccInput("hop", "Hop", "openai", "https://gateway.example.com/v1", map[string]any{
			"extra_headers": []any{map[string]any{"name": name, "value": "x-value-1"}},
		}))
		if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "/extra_headers/0/name") {
			t.Errorf("POST with extra header %q = %d %s, want 422 at /extra_headers/0/name", name, r.Status, r.Body)
		}
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM custom_providers`); n != 0 {
		t.Errorf("custom_providers rows after the refused creates = %d, want 0", n)
	}
}

// L4: a reported cost of 1e7 USD is unknown (not stored in agent_runs.cost_usd numeric(12,6), not
// in the ledger as a spend) and the run still completes.
func TestCustomCalls_AnAbsurdReportedCostIsUnknownAndTheRunCompletes(t *testing.T) {
	g := newCCGateway(t, mock.Review)
	g.usage = &ccUsage{In: 1200, Out: 180, Cost: "10000000"}
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	ccDefine(t, s, ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{"api_key": ccKey}))
	run, status := ccReview(t, s, pr, ccAgent(t, s, "Costly agent", "custom:synapse", "gw-model"))
	if status != "done" {
		t.Fatalf("review = %s (%s), want done", status, ccRunError(t, s, run))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM agent_runs WHERE id = ? AND cost_usd IS NULL`, run); n != 1 {
		t.Errorf("agent_runs.cost_usd of the run is not NULL, want unknown")
	}
	if diff := cmp.Diff([]*int64{nil}, ccCosts(t, s, "custom:synapse")["gw-model"]); diff != "" {
		t.Errorf("ledger cost mismatch (-want +got):\n%s", diff)
	}
}

// INFO: a transport failure does not put the base URL into the run error, the trace or the log.
func TestCustomCalls_TransportFailureTextHasNoBaseURL(t *testing.T) {
	g := newCCGateway(t, mock.Review)
	s := ccStack(t, spec{})
	pr := pr482(t, s)
	ccDefine(t, s, ccInput("synapse", "Synapse", "openai", g.base(), map[string]any{"api_key": ccKey}))
	agent := ccAgent(t, s, "Down agent", "custom:synapse", "gw-model")
	g.srv.Close()
	run, status := ccReview(t, s, pr, agent)
	msg := ccRunError(t, s, run)
	if status != "failed" || msg == "" {
		t.Fatalf("review = %s %q, want failed with an error", status, msg)
	}
	needles := []string{g.base(), "http://", "/v1/"}
	ccAbsent(t, "the run error", needles, msg)
	ccAbsent(t, "the trace", needles, string(waitTrace(t, s, run).Body))
	ccAbsent(t, "the server log", needles, s.logs.String())
}
