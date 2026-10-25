package llmcall_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/reviewer/adapters/llmcall"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	"dev-digest-go/backend/internal/reviewer/domain"
)

// fakeClient records the request and answers with fn.
type fakeClient struct {
	req llm.Request
	fn  func(req llm.Request) (llm.Result, error)
}

func (f *fakeClient) Structured(_ context.Context, req llm.Request) (llm.Result, error) {
	f.req = req
	return f.fn(req)
}

type keys map[string]string

func (k keys) Get(name string) string { return k[name] }

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newClient(t *testing.T, adapters map[llm.Provider]llm.Adapter, ks keys, logs io.Writer) *llm.Client {
	t.Helper()
	c := llm.New(llm.Options{
		Keys:     ks,
		Adapters: adapters,
		Logger:   slog.New(slog.NewJSONHandler(logs, nil)),
		Prices:   llm.NewPriceBook(llm.PriceBookOptions{}),
		Retry:    retry.Options{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
	})
	t.Cleanup(c.Close)
	return c
}

func twoSectionCall(chunk domain.ChunkRef) reviewerapp.Call {
	items, truncated := 2, true
	return reviewerapp.Call{
		Messages: []domain.Message{{Role: domain.RoleSystem, Content: "SYS"}, {Role: domain.RoleUser, Content: "USER"}},
		Sections: []domain.SectionMeta{
			{Name: domain.SectionSystem, Source: domain.SourceAgentConfig, Role: domain.RoleSystem, Trust: domain.Trusted, Chars: 3, Tokens: 1},
			{Name: domain.SectionInjectionGuard, Source: domain.SourceEngine, Role: domain.RoleSystem, Trust: domain.Trusted, Chars: 9},
			{Name: domain.SectionPRDescription, Source: domain.SourceAuthor, Role: domain.RoleUser, Trust: domain.Untrusted, Chars: 7, Truncated: &truncated},
			{Name: domain.SectionSkills, Source: domain.SourceCurated, Role: domain.RoleUser, Trust: domain.Trusted, Chars: 5, Items: &items},
		},
		Chunk: chunk,
	}
}

const answer = `{"verdict":"request_changes","summary":"sum","score":55,"findings":[
 {"id":"f1","severity":"CRITICAL","category":"security","title":"T","file":"a.ts","start_line":3,"end_line":4.0,
  "rationale":"R","suggestion":"S","confidence":0.8,"kind":"secret_leak","trifecta_components":null,
  "evidence":null,"skill":"my-skill","out_of_scope":true},
 {"id":"f2","severity":"SUGGESTION","category":"style","title":"U","file":"b.ts","start_line":1,"end_line":1,
  "rationale":"R2","confidence":0.1}]}`

func TestCaller_BuildsTheReviewRequest(t *testing.T) {
	cost := 0.25
	fc := &fakeClient{fn: func(llm.Request) (llm.Result, error) {
		return llm.Result{Data: json.RawMessage(answer), Raw: "RAW", TokensIn: 11, TokensOut: 7, CostUSD: &cost}, nil
	}}
	var usages []domain.Usage
	c := llmcall.New(llmcall.Config{
		Client: fc, Provider: llm.OpenRouter, Model: "m/x", RunID: "run-1", SessionID: "o/n#1:Agent",
		OnUsage: func(u domain.Usage) { usages = append(usages, u) },
	})
	got, err := c.Review(t.Context(), twoSectionCall(domain.ChunkRef{Index: 1, Total: 3}))
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}

	r := fc.req
	if r.Provider != llm.OpenRouter || r.Model != "m/x" || r.SchemaName != "Review" || r.Feature != "review" ||
		r.CorrelationID != "run-1" || r.SessionID != "o/n#1:Agent" || r.Reprompts != 2 {
		t.Errorf("Review() request = %+v", r)
	}
	if r.Temperature == nil || *r.Temperature != 0 {
		t.Errorf("Review() temperature = %v, want 0", r.Temperature)
	}
	if string(r.Schema) != string(domain.ReviewSchemaJSON) {
		t.Error("Review() schema is not domain.ReviewSchemaJSON")
	}
	if diff := cmp.Diff([]llm.Message{{Role: llm.System, Content: "SYS"}, {Role: llm.User, Content: "USER"}}, r.Messages); diff != "" {
		t.Errorf("Review() messages mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(&llm.Chunk{Index: 1, Total: 3}, r.Chunk); diff != "" {
		t.Errorf("Review() chunk mismatch (-want +got):\n%s", diff)
	}
	var roles []string
	for _, s := range r.Sections {
		roles = append(roles, s.Name+"="+s.Role)
	}
	if diff := cmp.Diff([]string{"system=system", "injection_guard=system", "pr_description=user", "skills=user"}, roles); diff != "" {
		t.Errorf("Review() section roles mismatch (-want +got):\n%s", diff)
	}
	if s := r.Sections[2]; s.Truncated == nil || !*s.Truncated || s.Chars != 7 || s.Trust != "untrusted" || s.Source != "author" {
		t.Errorf("Review() pr_description section = %+v", s)
	}
	if s := r.Sections[3]; s.Items == nil || *s.Items != 2 {
		t.Errorf("Review() skills section = %+v, want items 2", s)
	}

	// The usage observer is forwarded and maps the attempt usage.
	r.OnUsage(llm.AttemptUsage{TokensIn: 4, TokensOut: 2, CostUSD: &cost})
	r.OnUsage(llm.AttemptUsage{TokensIn: 1})
	if diff := cmp.Diff([]domain.Usage{{TokensIn: 4, TokensOut: 2, CostUSD: &cost}, {TokensIn: 1}}, usages); diff != "" {
		t.Errorf("OnUsage mismatch (-want +got):\n%s", diff)
	}

	if got.Raw != "RAW" || got.Usage.TokensIn != 11 || got.Usage.TokensOut != 7 || got.Usage.CostUSD != &cost {
		t.Errorf("Review() = raw %q usage %+v, want RAW and 11/7/0.25", got.Raw, got.Usage)
	}
	sug, skill, oos := "S", "my-skill", true
	want := domain.Review{Verdict: domain.VerdictRequestChanges, Summary: "sum", Score: 55, Findings: []domain.Finding{
		{ID: "f1", Severity: domain.SeverityCritical, Category: domain.CategorySecurity, Title: "T", File: "a.ts",
			StartLine: 3, EndLine: 4, Rationale: "R", Suggestion: &sug, Confidence: 0.8, Kind: domain.KindSecretLeak,
			Skill: &skill, OutOfScope: &oos},
		{ID: "f2", Severity: domain.SeveritySuggestion, Category: domain.CategoryStyle, Title: "U", File: "b.ts",
			StartLine: 1, EndLine: 1, Rationale: "R2", Confidence: 0.1},
	}}
	if diff := cmp.Diff(want, got.Review); diff != "" {
		t.Errorf("Review() review mismatch (-want +got):\n%s", diff)
	}
}

func TestCaller_NoOnUsageLeavesTheHookNil(t *testing.T) {
	fc := &fakeClient{fn: func(llm.Request) (llm.Result, error) {
		return llm.Result{Data: json.RawMessage(`{"verdict":"approve","summary":"","score":100,"findings":[]}`)}, nil
	}}
	c := llmcall.New(llmcall.Config{Client: fc, Provider: llm.OpenAI, Model: "m"})
	if _, err := c.Review(t.Context(), twoSectionCall(domain.ChunkRef{Total: 1})); err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if fc.req.OnUsage != nil || fc.req.SessionID != "" {
		t.Errorf("Review() request = %+v, want no OnUsage and no session", fc.req)
	}
}

func TestCaller_ChargeReachesTheRequest(t *testing.T) {
	fc := &fakeClient{fn: func(llm.Request) (llm.Result, error) {
		return llm.Result{Data: json.RawMessage(`{"verdict":"approve","summary":"","score":100,"findings":[]}`)}, nil
	}}
	charge := llm.Charge{WorkspaceID: "ws-1", Kind: llm.KindReviewRun, RefID: "run-1"}
	c := llmcall.New(llmcall.Config{Client: fc, Provider: llm.OpenAI, Model: "m", Charge: &charge})

	var seen []*llm.Charge
	for range 2 {
		if _, err := c.Review(t.Context(), twoSectionCall(domain.ChunkRef{Total: 1})); err != nil {
			t.Fatalf("Review() error = %v", err)
		}
		seen = append(seen, fc.req.Charge)
	}
	for i, got := range seen {
		if diff := cmp.Diff(&llm.Charge{WorkspaceID: "ws-1", Kind: llm.KindReviewRun, RefID: "run-1"}, got); diff != "" {
			t.Errorf("Review() call %d request charge mismatch (-want +got):\n%s", i, diff)
		}
	}

	// Each request owns its copy: neither the caller's value nor an earlier request is shared.
	if seen[0] == &charge || seen[0] == seen[1] {
		t.Error("Review() requests share the Charge pointer, want a copy per request")
	}
	charge.RefID = "changed"
	if seen[0].RefID != "run-1" {
		t.Errorf("request charge RefID = %q after the caller changed its value, want run-1", seen[0].RefID)
	}
}

func TestCaller_NilChargeStaysNil(t *testing.T) {
	fc := &fakeClient{fn: func(llm.Request) (llm.Result, error) {
		return llm.Result{Data: json.RawMessage(`{"verdict":"approve","summary":"","score":100,"findings":[]}`)}, nil
	}}
	c := llmcall.New(llmcall.Config{Client: fc, Provider: llm.OpenAI, Model: "m"})
	if _, err := c.Review(t.Context(), twoSectionCall(domain.ChunkRef{Total: 1})); err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if fc.req.Charge != nil {
		t.Errorf("Review() request charge = %+v, want nil", fc.req.Charge)
	}
}

func TestCaller_ClientErrorPassesThroughUnchanged(t *testing.T) {
	boom := errors.New("OpenAI structured output failed schema validation for Review after 3 attempt(s)")
	fc := &fakeClient{fn: func(llm.Request) (llm.Result, error) { return llm.Result{}, boom }}
	c := llmcall.New(llmcall.Config{Client: fc, Provider: llm.OpenAI, Model: "m"})
	_, err := c.Review(t.Context(), twoSectionCall(domain.ChunkRef{Total: 1}))
	if err != boom {
		t.Errorf("Review() error = %v, want the client's error unchanged", err)
	}
}

func TestNew_PanicsWithoutClient(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New(Config{}) did not panic")
		}
	}()
	llmcall.New(llmcall.Config{})
}

// stubServer is a chat-completions endpoint that records each request body.
type stubServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newStubServer(t *testing.T, content string) *stubServer {
	t.Helper()
	s := &stubServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		c, _ := json.Marshal(content)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":`+string(c)+`}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.001}}`)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubServer) body(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) != 1 {
		t.Fatalf("stub server got %d requests, want 1", len(s.bodies))
	}
	return s.bodies[0]
}

// AC4056: the OpenRouter body carries session_id, usage.include, the strict Review schema equal
// to the Node golden and temperature 0; an OpenAI request has no session_id.
func TestCaller_WireBody(t *testing.T) {
	golden, err := os.ReadFile("../../domain/testdata/review_schema.node.json")
	if err != nil {
		t.Fatalf("read Node golden: %v", err)
	}
	const sessionID = "acme/payments-api#482:Security Reviewer"
	for _, tc := range []struct {
		name     string
		provider llm.Provider
		wantSess bool
	}{
		{"openrouter", llm.OpenRouter, true},
		{"openai", llm.OpenAI, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newStubServer(t, answer)
			ad := map[llm.Provider]llm.Adapter{
				llm.OpenRouter: openaicompat.NewOpenRouter(openaicompat.Options{BaseURL: srv.srv.URL, HTTPClient: srv.srv.Client()}),
				llm.OpenAI:     openaicompat.NewOpenAI(openaicompat.Options{BaseURL: srv.srv.URL, HTTPClient: srv.srv.Client()}),
			}
			logs := &logBuf{}
			client := newClient(t, ad, keys{"OPENROUTER_API_KEY": "or-key", "OPENAI_API_KEY": "oa-key"}, logs)
			var usages []domain.Usage
			c := llmcall.New(llmcall.Config{
				Client: client, Provider: tc.provider, Model: "acme/model", RunID: "run-9", SessionID: sessionID,
				OnUsage: func(u domain.Usage) { usages = append(usages, u) },
			})
			res, err := c.Review(t.Context(), twoSectionCall(domain.ChunkRef{Index: 0, Total: 2}))
			if err != nil {
				t.Fatalf("Review() error = %v", err)
			}
			if len(res.Review.Findings) != 2 || res.Usage.TokensIn != 10 || len(usages) != 1 {
				t.Errorf("Review() = %d findings, usage %+v, %d observer calls", len(res.Review.Findings), res.Usage, len(usages))
			}

			body := srv.body(t)
			if got, ok := body["session_id"]; tc.wantSess != ok || (ok && got != sessionID) {
				t.Errorf("body session_id = %v (present %v), want present=%v %q", got, ok, tc.wantSess, sessionID)
			}
			if _, ok := body["usage"]; ok != (tc.provider == llm.OpenRouter) {
				t.Errorf("body usage present = %v for %s", ok, tc.provider)
			}
			if got, ok := body["temperature"].(float64); !ok || got != 0 {
				t.Errorf("body temperature = %v, want 0", body["temperature"])
			}
			rf := body["response_format"].(map[string]any)["json_schema"].(map[string]any)
			if rf["name"] != "Review" {
				t.Errorf("body schema name = %v, want Review", rf["name"])
			}
			gotSchema, _ := json.Marshal(rf["schema"])
			var wantAny, gotAny any
			if err := json.Unmarshal(golden, &wantAny); err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			_ = json.Unmarshal(gotSchema, &gotAny)
			if diff := cmp.Diff(wantAny, gotAny); diff != "" {
				t.Errorf("body schema differs from the Node golden (-want +got):\n%s", diff)
			}

			// AC4058: one prompt_assembled line with the chunk, the section metadata and no prompt text.
			line := logs.String()
			for _, want := range []string{`"evt":"prompt_assembled"`, `"feature":"review"`, `"correlationId":"run-9"`,
				`"chunk":{"index":0,"total":2}`, `"name":"injection_guard"`, `"role":"system"`, `"items":2`, `"truncated":true`} {
				if !strings.Contains(line, want) {
					t.Errorf("prompt log line lacks %s:\n%s", want, line)
				}
			}
			for _, secret := range []string{"SYS", "USER", "or-key", "oa-key"} {
				if strings.Contains(line, secret) {
					t.Errorf("prompt log line contains %q:\n%s", secret, line)
				}
			}
		})
	}
}

// A real client with the mock provider: the schema accepted by the validator, the fixture decoded.
func TestCaller_MockProviderAnswersTheFixture(t *testing.T) {
	client := newClient(t, map[llm.Provider]llm.Adapter{llm.OpenAI: mock.New(llm.OpenAI, mock.Options{})}, keys{}, io.Discard)
	c := llmcall.New(llmcall.Config{Client: client, Provider: llm.OpenAI, Model: "mock"})
	res, err := c.Review(t.Context(), twoSectionCall(domain.ChunkRef{Total: 1}))
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if res.Review.Verdict != domain.VerdictComment || res.Review.Score != 70 || len(res.Review.Findings) != 2 {
		t.Fatalf("Review() = %+v, want the mock fixture (comment, 70, 2 findings)", res.Review)
	}
	f := res.Review.Findings[0]
	if f.ID != "mock-1" || f.Severity != domain.SeverityWarning || f.Kind != domain.KindFinding || f.File != "src/middleware/ratelimit.ts" || f.StartLine != 7 {
		t.Errorf("Review() first finding = %+v", f)
	}
}
