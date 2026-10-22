package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/google/go-cmp/cmp"

	conventionsapp "dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/settings/adapters/providers"
	"dev-digest-go/backend/internal/skills/adapters/urlfetch"
)

// ---- provider stubs ----

// stubCall is one request a provider stub received.
type stubCall struct {
	Method, Path string
	Header       http.Header
	Body         map[string]any // decoded JSON body, nil when there was none
}

// llmStub plays one provider endpoint. reply answers every POST (n is the 1-based count of
// POSTs); GET /models answers models or, when it is nil, a short list.
type llmStub struct {
	srv    *httptest.Server
	reply  func(n int, w http.ResponseWriter, r *http.Request)
	models func(w http.ResponseWriter, r *http.Request)

	mu    sync.Mutex
	calls []stubCall
	posts int
}

func newLLMStub(t testing.TB, reply func(n int, w http.ResponseWriter, r *http.Request)) *llmStub {
	t.Helper()
	l := &llmStub{reply: reply}
	l.srv = httptest.NewServer(http.HandlerFunc(l.serveHTTP))
	t.Cleanup(l.srv.Close)
	return l
}

func (l *llmStub) URL() string { return l.srv.URL }

func (l *llmStub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	call := stubCall{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &call.Body)
	}
	l.mu.Lock()
	l.calls = append(l.calls, call)
	n := 0
	if r.Method == http.MethodPost {
		l.posts++
		n = l.posts
	}
	l.mu.Unlock()

	if r.Method == http.MethodPost {
		l.reply(n, w, r)
		return
	}
	if l.models != nil {
		l.models(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5.4"}]}`)
}

// requests returns the POST requests received so far.
func (l *llmStub) requests() []stubCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []stubCall
	for _, c := range l.calls {
		if c.Method == http.MethodPost {
			out = append(out, c)
		}
	}
	return out
}

// total is the number of requests of any method.
func (l *llmStub) total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

const noCandidates = `{"candidates":[]}`

// openAIAnswer replies as the chat-completions API with content as the model's text.
func openAIAnswer(content string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
		})
	}
}

// anthropicAnswer replies as the messages API with input as the forced tool call's input.
func anthropicAnswer(input string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "tool_use", "input": json.RawMessage(input)}},
			"usage":   map[string]any{"input_tokens": 100, "output_tokens": 20},
		})
	}
}

// openAIError replies with a provider error whose message is msg.
func openAIError(status int, msg string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
	}
}

// hang never answers; it returns when the client gives up.
func hang(_ int, _ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

// fastLLM is the client tuning of the stub tests: static prices (no live price fetch) and a
// near-zero backoff, so a retried request costs milliseconds.
func fastLLM() llm.Options {
	return llm.Options{
		Prices: llm.NewPriceBook(llm.PriceBookOptions{}),
		Retry: retry.Options{
			BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond,
			Jitter: func(time.Duration) time.Duration { return 0 },
		},
	}
}

// startScanStack starts a server over the fixture remote o/r with fastLLM and registers the
// repository; it returns the stack and the repo id.
func startScanStack(t testing.TB, sp spec) (*stack, string) {
	t.Helper()
	remote := t.TempDir()
	makeFilesRemote(t, remote, "o", "r", fixtureFiles)
	sp.remoteDir = remote
	sp.llm = fastLLM()
	s := startStack(t, sp)
	return s, addClonedRepo(t, s, "o", "r")
}

func openAIOnly(l *llmStub) llmURLs {
	closed := "http://127.0.0.1:1"
	return llmURLs{openAI: l.URL(), anthropic: closed, openRouter: closed}
}

// utf16Len is the length in UTF-16 code units, the unit of the prompt log's chars.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// ---- AC268, AC225 ----

// AC268: the OpenAI request of a scan, byte for byte where the spec fixes it.
func TestScan_OpenAIRequestShape(t *testing.T) {
	stub := newLLMStub(t, nil)
	var logs atomic.Pointer[syncBuffer]
	var promptLoggedFirst atomic.Bool
	stub.reply = func(n int, w http.ResponseWriter, r *http.Request) {
		if b := logs.Load(); b != nil && strings.Contains(b.String(), `"prompt_assembled"`) {
			promptLoggedFirst.Store(true)
		}
		openAIAnswer(noCandidates)(n, w, r)
	}
	s, repo := startScanStack(t, spec{llmURLs: openAIOnly(stub), env: map[string]string{"OPENAI_API_KEY": "sk-test-268"}})
	logs.Store(s.logs)

	scan := runScan(t, s, repo)
	if scan["status"] != "done" {
		t.Fatalf("scan = %v, want done", scan)
	}
	calls := stub.requests()
	if len(calls) != 1 {
		t.Fatalf("OpenAI stub got %d POST requests, want 1", len(calls))
	}
	c := calls[0]
	if got := c.Header.Get("Authorization"); got != "Bearer sk-test-268" {
		t.Errorf("Authorization = %q, want the configured key as a bearer token", got)
	}
	if c.Body["model"] != "gpt-5.4" {
		t.Errorf("model = %v, want gpt-5.4", c.Body["model"])
	}
	for _, absent := range []string{"temperature", "max_tokens"} {
		if v, ok := c.Body[absent]; ok {
			t.Errorf("request has %s = %v, want it absent for a gpt-5 model", absent, v)
		}
	}
	if c.Body["max_completion_tokens"] != float64(6000) {
		t.Errorf("max_completion_tokens = %v, want 6000", c.Body["max_completion_tokens"])
	}
	format, _ := c.Body["response_format"].(map[string]any)
	schema, _ := format["json_schema"].(map[string]any)
	if format["type"] != "json_schema" || schema["name"] != "ConventionExtraction" || schema["strict"] != true {
		t.Errorf("response_format = %v, want a strict json_schema named ConventionExtraction", format)
	}

	messages, _ := c.Body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v, want a system and a user message", messages)
	}
	system, user := messages[0].(map[string]any), messages[1].(map[string]any)
	if system["role"] != "system" || system["content"] != domain.ExtractionSystemPrompt {
		t.Errorf("system message differs from the Node prompt: role %v, %d bytes (want %d)", system["role"], len(fmt.Sprint(system["content"])), len(domain.ExtractionSystemPrompt))
	}
	text, _ := user["content"].(string)
	const head = "Repository: o/r\n\nSampled files (config first, then the most central source files):\n\n<untrusted source=\"repository-sample\">\n"
	const tail = "\n</untrusted>\n\nReturn the house conventions as structured output."
	if user["role"] != "user" || !strings.HasPrefix(text, head) || !strings.HasSuffix(text, tail) {
		t.Fatalf("user message does not have the 4 parts: role %v\n%s", user["role"], text)
	}
	sample := strings.TrimSuffix(strings.TrimPrefix(text, head), tail)
	if strings.Contains(sample, "</untrusted>") || !strings.Contains(sample, `<\/untrusted>`) {
		t.Errorf("the sample line with a closing tag was not neutralized to <\\/untrusted>:\n%s", sample)
	}
	if !strings.Contains(sample, "=== FILE: src/app.ts (source) ===") {
		t.Errorf("sample lacks the file block of src/app.ts:\n%s", sample)
	}

	// AC225 (ordering half): the prompt line was already logged when the model was called.
	if !promptLoggedFirst.Load() {
		t.Error("prompt_assembled was not logged before the provider received the request")
	}
}

// AC225: one content-free prompt_assembled line per scan, from the mock provider.
func TestScan_PromptLogIsContentFree(t *testing.T) {
	s, repo := startScanStack(t, spec{config: mockProvider})
	scan := runScan(t, s, repo)
	if scan["status"] != "done" {
		t.Fatalf("scan = %v, want done", scan)
	}

	lines := logLines(t, s.logs, "prompt_assembled")
	if len(lines) != 1 {
		t.Fatalf("prompt_assembled lines = %d, want 1", len(lines))
	}
	l := lines[0]
	if l["evt"] != "prompt_assembled" || l["feature"] != "conventions" || l["correlationId"] != scan["id"] {
		t.Errorf("prompt line = %v, want feature conventions and correlationId %v", l, scan["id"])
	}
	sections, _ := l["sections"].([]any)
	var names []string
	total := 0
	for _, raw := range sections {
		sec := raw.(map[string]any)
		names = append(names, sec["name"].(string))
		total += int(sec["chars"].(float64))
		switch sec["name"] {
		case "system":
			if sec["chars"] != float64(utf16Len(domain.ExtractionSystemPrompt)) || sec["role"] != "system" || sec["trust"] != "trusted" {
				t.Errorf("system section = %v, want the prompt's length, role system, trusted", sec)
			}
		case "repository_sample":
			if sec["items"] != float64(3) || sec["source"] != "repo" || sec["trust"] != "untrusted" || sec["role"] != "user" {
				t.Errorf("repository_sample section = %v, want 3 items from repo, untrusted, role user", sec)
			}
		}
	}
	if diff := cmp.Diff([]string{"system", "task", "repository_sample"}, names); diff != "" {
		t.Errorf("section names mismatch (-want +got):\n%s", diff)
	}
	if l["totalChars"] != float64(total) {
		t.Errorf("totalChars = %v, want the sum of the sections %d", l["totalChars"], total)
	}
	assertNoPromptContent(t, s.logs.String())
}

// assertNoPromptContent fails when a log holds a line of the system prompt or of the fixture
// sample.
func assertNoPromptContent(t testing.TB, logs string) {
	t.Helper()
	var lines []string
	lines = append(lines, strings.Split(domain.ExtractionSystemPrompt, "\n")...)
	for _, content := range fixtureFiles {
		lines = append(lines, strings.Split(content, "\n")...)
	}
	for _, line := range lines {
		if line = strings.TrimSpace(line); len(line) >= 12 && strings.Contains(logs, line) {
			t.Errorf("a log line contains the prompt or sample text %q", line)
		}
	}
}

// ---- AC226 ----

// AC226: the mock replaces every provider, makes no network call, warns once at startup.
func TestMock_ReplacesProvidersWithoutNetwork(t *testing.T) {
	stub := newLLMStub(t, openAIAnswer(noCandidates))
	closed := "http://127.0.0.1:1"
	s := startStack(t, spec{
		config:  mockProvider,
		llmURLs: llmURLs{openAI: stub.URL(), anthropic: stub.URL(), openRouter: closed},
		llm:     fastLLM(),
	})

	resp := s.get("/providers/anthropic/models")
	want := []any{map[string]any{"id": "mock/reviewer", "provider": "anthropic", "label": "Mock reviewer (LLM_PROVIDER_OVERRIDE=mock)"}}
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /providers/anthropic/models = %d %s, want 200", resp.Status, resp.Body)
	}
	if diff := cmp.Diff(want, resp.JSON(t)); diff != "" {
		t.Errorf("models mismatch (-want +got):\n%s", diff)
	}
	if got := s.get("/providers/openrouter/models"); got.Status != http.StatusOK || !strings.Contains(string(got.Body), `"provider":"openrouter"`) {
		t.Errorf("GET /providers/openrouter/models = %d %s, want the mock model for openrouter", got.Status, got.Body)
	}
	if n := stub.total(); n != 0 {
		t.Errorf("the provider stub received %d request(s) in mock mode, want none", n)
	}
	if lines := logLines(t, s.logs, mockWarning); len(lines) != 1 || lines[0]["level"] != "WARN" {
		t.Errorf("mock warning lines = %v, want exactly one WARN line", lines)
	}
}

// AC226: any other override, and an out-of-range mock delay, stop startup.
func TestRun_RejectsBadLLMSwitches(t *testing.T) {
	for _, env := range []map[string]string{
		{"LLM_PROVIDER_OVERRIDE": "other"},
		{"LLM_PROVIDER_OVERRIDE": "mock", "LLM_MOCK_DELAY_MS": "60001"},
		{"LLM_PROVIDER_OVERRIDE": "mock", "LLM_MOCK_DELAY_MS": "soon"},
	} {
		env["DATABASE_URL"] = "postgres://unused@127.0.0.1:1/none"
		env["DEVDIGEST_DATA_DIR"] = t.TempDir()
		var out bytes.Buffer
		err := run(t.Context(), []string{"server"}, func(k string) string { return env[k] }, &out)
		if err == nil || !strings.HasPrefix(err.Error(), "config: ") {
			t.Errorf("run with %v = %v, want a config error", env, err)
		}
	}
}

// ---- AC227 ----

// AC227: the stored feature model decides which provider a scan calls.
func TestScan_FeatureModelPicksTheProvider(t *testing.T) {
	openai := newLLMStub(t, openAIAnswer(noCandidates))
	anthropicStub := newLLMStub(t, anthropicAnswer(noCandidates))
	closed := "http://127.0.0.1:1"
	s, repo := startScanStack(t, spec{
		llmURLs: llmURLs{openAI: openai.URL(), anthropic: anthropicStub.URL(), openRouter: closed},
		env:     map[string]string{"OPENAI_API_KEY": "sk-openai", "ANTHROPIC_API_KEY": "sk-anthropic"},
	})
	setChoice := func(choice any) {
		t.Helper()
		value, err := json.Marshal(map[string]any{"conventions": choice})
		if err != nil {
			t.Fatal(err)
		}
		// Written directly: the bad choice {provider:"bad"} is not accepted by PUT /settings.
		if err := s.db.DB.Exec(`DELETE FROM settings WHERE key = 'feature_models'`).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.db.DB.Exec(`INSERT INTO settings (workspace_id, key, value) SELECT id, 'feature_models', ?::jsonb FROM workspaces LIMIT 1`, string(value)).Error; err != nil {
			t.Fatal(err)
		}
	}
	scanOK := func() {
		t.Helper()
		if scan := runScan(t, s, repo); scan["status"] != "done" {
			t.Fatalf("scan = %v, want done", scan)
		}
	}

	// Setting absent: the registry default, OpenAI gpt-5.4.
	scanOK()
	if calls := openai.requests(); len(calls) != 1 || calls[0].Body["model"] != "gpt-5.4" || anthropicStub.total() != 0 {
		t.Fatalf("default feature model: OpenAI calls %v, Anthropic calls %d; want one gpt-5.4 call and none", calls, anthropicStub.total())
	}

	setChoice(map[string]string{"provider": "anthropic", "model": "claude-haiku-4-5"})
	scanOK()
	calls := anthropicStub.requests()
	if len(calls) != 1 || calls[0].Body["model"] != "claude-haiku-4-5" || calls[0].Header.Get("x-api-key") != "sk-anthropic" {
		t.Fatalf("anthropic choice: Anthropic calls %v, want one claude-haiku-4-5 call with the key", calls)
	}
	if n := len(openai.requests()); n != 1 {
		t.Errorf("OpenAI got %d request(s) after the Anthropic choice, want still 1", n)
	}

	setChoice(map[string]string{"provider": "bad", "model": "x"})
	scanOK()
	if calls := openai.requests(); len(calls) != 2 || calls[1].Body["model"] != "gpt-5.4" {
		t.Errorf("invalid choice: OpenAI calls %v, want a second gpt-5.4 call", calls)
	}
	if n := len(anthropicStub.requests()); n != 1 {
		t.Errorf("Anthropic got %d request(s) after the invalid choice, want still 1", n)
	}
}

// ---- AC272, AC219 ----

// AC272 + AC219: without a key the scan fails and nothing is sent; a key saved through
// test-connection is used by the next scan without a restart.
func TestScan_NoKeyFailsThenSavedKeyIsUsed(t *testing.T) {
	stub := newLLMStub(t, openAIAnswer(noCandidates))
	s, repo := startScanStack(t, spec{
		llmURLs:   openAIOnly(stub),
		providers: providers.Options{OpenAIBaseURL: stub.URL()},
	})

	scan := runScan(t, s, repo)
	if scan["status"] != "failed" || scan["error"] != "OPENAI_API_KEY is not configured" {
		t.Errorf("scan without a key = %v, want failed with OPENAI_API_KEY is not configured", scan)
	}
	jobs := s.waitJobs(conventionsapp.KindExtract, 1)
	if len(jobs) != 1 || jobs[0].Status != "done" || jobs[0].Attempts != 1 {
		t.Errorf("extract job = %+v, want one done job with attempts 1", jobs)
	}
	if n := stub.total(); n != 0 {
		t.Errorf("the provider stub received %d request(s) without a key, want none", n)
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM conventions`); n != 0 {
		t.Errorf("conventions after the failed scan = %d, want 0", n)
	}

	const key = "sk-saved-219"
	resp := s.post("/settings/test-connection", map[string]string{"provider": "openai", "key": key})
	if resp.Status != http.StatusOK || resp.Obj(t)["ok"] != true {
		t.Fatalf("test-connection = %d %s, want ok", resp.Status, resp.Body)
	}
	if scan := runScan(t, s, repo); scan["status"] != "done" {
		t.Fatalf("scan after saving the key = %v, want done", scan)
	}
	calls := stub.requests()
	if len(calls) != 1 || calls[0].Header.Get("Authorization") != "Bearer "+key {
		t.Errorf("provider requests after saving the key = %v, want one with the saved key", calls)
	}
}

// ---- AC273, AC274, AC275 ----

// AC273: an answer that never matches the schema ends the scan after 2 attempts.
func TestScan_SchemaFailureAfterTwoAttempts(t *testing.T) {
	stub := newLLMStub(t, openAIAnswer(`{"candidates":"x"}`))
	s, repo := startScanStack(t, spec{llmURLs: openAIOnly(stub), env: map[string]string{"OPENAI_API_KEY": "sk-273"}})

	scan := runScan(t, s, repo)
	msg, _ := scan["error"].(string)
	const prefix = "OpenAI structured output failed schema validation for ConventionExtraction after 2 attempt(s)"
	if scan["status"] != "failed" || !strings.HasPrefix(msg, prefix) || len([]rune(msg)) > 500 {
		t.Errorf("scan = %v, want failed with %q first and at most 500 characters", scan, prefix)
	}
	if n := len(stub.requests()); n != 2 {
		t.Errorf("the stub got %d requests, want exactly 2", n)
	}
}

// AC274: a provider that never answers ends the scan at the job limit; the job is not retried.
// The limit is injected (2 s); the production value is 300 s (AC303).
func TestScan_TimesOutAtTheJobLimit(t *testing.T) {
	stub := newLLMStub(t, hang)
	s, repo := startScanStack(t, spec{
		llmURLs:        openAIOnly(stub),
		env:            map[string]string{"OPENAI_API_KEY": "sk-274"},
		extractTimeout: 2 * time.Second,
	})

	start := time.Now()
	scan := runScan(t, s, repo)
	took := time.Since(start)
	if scan["status"] != "failed" || scan["error"] != "The scan timed out after 2 s" {
		t.Errorf("scan = %v, want failed with \"The scan timed out after 2 s\"", scan)
	}
	if took > 12*time.Second {
		t.Errorf("the scan left running after %s, want within 12 s of its start", took)
	}
	if n := len(stub.requests()); n < 1 || n > 2 {
		t.Errorf("the stub got %d requests, want 1 or 2", n)
	}
	jobs := s.waitJobs(conventionsapp.KindExtract, 1)
	if len(jobs) != 1 || jobs[0].Attempts != 1 {
		t.Errorf("extract jobs = %+v, want one job run once (not retried)", jobs)
	}
}

// AC275: a key echoed by the provider is scrubbed from the scan, the job row and every log line.
func TestScan_ProviderErrorKeyIsRedacted(t *testing.T) {
	const key = "sk-secret-xyz"
	stub := newLLMStub(t, openAIError(http.StatusBadRequest, "Incorrect API key provided: "+key))
	s, repo := startScanStack(t, spec{llmURLs: openAIOnly(stub), env: map[string]string{"OPENAI_API_KEY": key}})

	scan := runScan(t, s, repo)
	msg, _ := scan["error"].(string)
	if scan["status"] != "failed" || !strings.Contains(msg, "[REDACTED]") || strings.Contains(msg, key) {
		t.Errorf("scan error = %q, want it to hold [REDACTED] and not the key", msg)
	}
	s.waitJobs(conventionsapp.KindExtract, 1)
	for _, table := range []string{"jobs", "convention_scans"} {
		var rows []string
		if err := s.db.DB.Raw(`SELECT to_json(t)::text FROM ` + table + ` t`).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		assertNoSecret(t, "table "+table, strings.Join(rows, "\n"), key)
	}
	logs := s.logs.String()
	assertNoSecret(t, "server log", logs, key)
	if !strings.Contains(logs, "[REDACTED]") {
		t.Error("no log line holds [REDACTED], want the failed-scan line to carry the scrubbed text")
	}
}

// ---- AC276, AC302 ----

// AC276: a scan on the mock provider keeps 3 rules, drops the missing file, costs 0.00042.
func TestScan_MockEndToEnd(t *testing.T) {
	s, repo := startScanStack(t, spec{config: mockProvider})

	scan := runScan(t, s, repo)
	if scan["status"] != "done" || scan["proposed"] != float64(4) || scan["kept"] != float64(3) || scan["cost_usd"] != 0.00042 {
		t.Fatalf("scan = %v, want done, 4 proposed, 3 kept, cost 0.00042", scan)
	}
	wantDropped := []any{map[string]any{
		"rule": "[mock LLM] A rule whose evidence does not exist (dropped by the evidence gate)",
		"path": "src/__mock__/does-not-exist.ts", "reason": "file_not_found",
	}}
	if diff := cmp.Diff(wantDropped, scan["dropped"]); diff != "" {
		t.Errorf("dropped mismatch (-want +got):\n%s", diff)
	}
	conventions := s.get("/repos/" + repo + "/conventions").Obj(t)["conventions"].([]any)
	if len(conventions) != 3 {
		t.Fatalf("conventions = %d, want 3", len(conventions))
	}
	for _, c := range conventions {
		if rule := c.(map[string]any)["rule"].(string); !strings.HasPrefix(rule, "[mock LLM] ") {
			t.Errorf("rule %q does not start with [mock LLM]", rule)
		}
	}
}

// AC302: when every candidate cites a missing file nothing is kept and an accepted rule stays.
func TestScan_AllCandidatesDroppedKeepsAcceptedRules(t *testing.T) {
	missing := `{"candidates":[
		{"category":"naming","rule":"Name things well in this repo","confidence":0.9,"evidence":[{"path":"src/missing-a.ts","start_line":1,"end_line":1,"snippet":"export const a = 1;"}]},
		{"category":"style","rule":"Keep files short in this repo","confidence":0.8,"evidence":[{"path":"src/missing-b.ts","start_line":1,"end_line":1,"snippet":"export const b = 2;"}]}]}`
	stub := newLLMStub(t, openAIAnswer(missing))
	s, repo := startScanStack(t, spec{llmURLs: openAIOnly(stub), env: map[string]string{"OPENAI_API_KEY": "sk-302"}})
	accepted := scalar[string](t, s.db.DB, `INSERT INTO conventions (workspace_id, repo_id, rule, category, status, confidence)
		SELECT workspace_id, id, 'Always return early from guards', 'style', 'accepted', 0.7 FROM repos WHERE id = ? RETURNING id`, repo)

	scan := runScan(t, s, repo)
	if scan["status"] != "done" || scan["kept"] != float64(0) || scan["proposed"] != float64(2) {
		t.Fatalf("scan = %v, want done, 2 proposed, 0 kept", scan)
	}
	for _, d := range scan["dropped"].([]any) {
		if d.(map[string]any)["reason"] != "file_not_found" {
			t.Errorf("dropped item %v, want reason file_not_found", d)
		}
	}
	if n := len(scan["dropped"].([]any)); n != 2 {
		t.Errorf("dropped = %d items, want 2", n)
	}
	var row struct {
		Rule, Status string
	}
	if err := s.db.DB.Table("conventions").Where("id = ?", accepted).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Rule != "Always return early from guards" || row.Status != "accepted" {
		t.Errorf("the accepted rule after the scan = %+v, want it unchanged", row)
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM conventions`); n != 1 {
		t.Errorf("conventions after the scan = %d, want only the accepted one", n)
	}
}

// ---- AC298 ----

// hostResolver resolves every name to one public address, so the importer reaches the TLS stub
// through the injected dialer.
type hostResolver struct{}

func (hostResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

// AC298: after a key is saved, a model listing fails, a scan fails and a URL is imported, no
// key, prompt line, sample line or skill body is in the logs and no key is in rows or responses.
func TestOps_NoSecretsOrContentInLogsRowsOrResponses(t *testing.T) {
	const (
		key       = "sk-live-298-key"
		skillBody = "Skill body line that must stay out of every log 4f9c1d."
	)
	provider := newLLMStub(t, openAIError(http.StatusBadRequest, "Incorrect API key provided: "+key))
	provider.models = func(w http.ResponseWriter, _ *http.Request) {
		openAIError(http.StatusInternalServerError, "upstream failed for "+key)(0, w, nil)
	}

	docs := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = io.WriteString(w, "# Imported\n\n"+skillBody+"\n")
	}))
	t.Cleanup(docs.Close)
	pool := x509.NewCertPool()
	pool.AddCert(docs.Certificate())

	s, repo := startScanStack(t, spec{
		llmURLs:   openAIOnly(provider),
		providers: providers.Options{OpenAIBaseURL: provider.URL()},
		urlfetch: urlfetch.Options{
			Resolver:  hostResolver{},
			TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, docs.Listener.Addr().String())
			},
		},
	})

	var responses []response
	record := func(r response) response { responses = append(responses, r); return r }

	record(s.post("/settings/test-connection", map[string]string{"provider": "openai", "key": key}))
	record(s.get("/settings/secrets-status"))
	record(s.get("/providers/openai/models"))
	scan := runScan(t, s, repo)
	if scan["status"] != "failed" {
		t.Fatalf("scan = %v, want failed against the failing provider", scan)
	}
	record(s.get("/repos/" + repo + "/conventions"))
	preview := record(s.post("/skills/import/preview", map[string]any{"kind": "url", "url": "https://example.com/skills/imported.md"}))
	if preview.Status != http.StatusOK || !strings.Contains(string(preview.Body), skillBody) {
		t.Fatalf("URL import preview = %d %s, want 200 with the document body", preview.Status, preview.Body)
	}
	record(s.post("/skills", map[string]any{"name": "imported", "type": "custom", "body": skillBody, "source": "imported_url"}))
	s.waitJobs(conventionsapp.KindExtract, 1)

	logs := s.logs.String()
	// The scenario really logged its failures; otherwise the checks below would prove nothing.
	if n := len(logLines(t, s.logs, "conventions scan failed")); n != 1 {
		t.Errorf("failed-scan log lines = %d, want 1", n)
	}
	if !strings.Contains(logs, "[REDACTED]") {
		t.Error("no log line holds [REDACTED], want the failures to be logged with the key scrubbed")
	}
	assertNoSecret(t, "server log", logs, key, skillBody)
	assertNoPromptContent(t, logs)

	for i, r := range responses {
		assertNoSecret(t, fmt.Sprintf("response %d (status %d)", i, r.Status), string(r.Body), key)
	}
	for _, table := range []string{"jobs", "convention_scans", "conventions", "settings", "skills", "agents"} {
		var rows []string
		if err := s.db.DB.Raw(`SELECT to_json(t)::text FROM ` + table + ` t`).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		assertNoSecret(t, "table "+table, strings.Join(rows, "\n"), key)
	}
}

// A failing provider listing degrades to [] (spec 7.3); it is the failing-stub half of AC298.
func TestProviders_ModelsListDegradesToEmpty(t *testing.T) {
	stub := newLLMStub(t, openAIAnswer(noCandidates))
	stub.models = func(w http.ResponseWriter, _ *http.Request) {
		openAIError(http.StatusInternalServerError, "down")(0, w, nil)
	}
	s := startStack(t, spec{llmURLs: openAIOnly(stub), llm: fastLLM(), env: map[string]string{"OPENAI_API_KEY": "sk-list"}})
	resp := s.get("/providers/openai/models")
	if resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != "[]" {
		t.Errorf("GET /providers/openai/models with a failing provider = %d %s, want 200 []", resp.Status, resp.Body)
	}
	if n := stub.total(); n < 1 {
		t.Errorf("the provider stub received %d request(s), want the listing to try it", n)
	}
}
