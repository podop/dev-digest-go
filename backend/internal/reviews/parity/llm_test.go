package parity_test

// S23 (AC4041, NFR4005): the structured-output layer against the Node goldens: the strict Review
// schema, the parse and re-prompt texts (schema.json), the JSON extraction, and the provider
// requests, usage and failure texts of the OpenRouter/OpenAI adapters over HTTP (llm.json).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/platform/retry"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

type keyMap map[string]string

func (k keyMap) Get(name string) string { return k[name] }

const testKey = "sk-test-key"

// nilPricer prices nothing, so that the cost of a call is what the provider reported (the Node
// provider has no price book; Go's estimate is the intended extension of FR4036).
type nilPricer struct{}

func (nilPricer) Estimate(string, int, int) *float64 { return nil }

func newClient(t testing.TB, adapters map[llm.Provider]llm.Adapter) *llm.Client {
	t.Helper()
	c := llm.New(llm.Options{
		Keys:     keyMap{"OPENAI_API_KEY": testKey, "OPENROUTER_API_KEY": testKey},
		Adapters: adapters,
		Logger:   slog.New(slog.DiscardHandler),
		Prices:   nilPricer{},
		Retry:    retry.Options{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: func(time.Duration) time.Duration { return 0 }},
	})
	t.Cleanup(c.Close)
	return c
}

// v8Message is the text after "Output was not valid JSON: " up to the line end: V8's JSON.parse
// message, which Go cannot reproduce (README, parity traps).
var v8Message = regexp.MustCompile(`(Output was not valid JSON: )[^\n]*`)

func normJSONError(s string) string { return v8Message.ReplaceAllString(s, "${1}<parse error>") }

// scriptedAdapter answers every attempt with the next scripted output and records the messages.
type scriptedAdapter struct {
	mu      sync.Mutex
	outputs []string
	calls   [][]llm.Message
}

func (a *scriptedAdapter) RequiresKey() bool { return true }
func (a *scriptedAdapter) Policy() llm.Policy {
	return llm.Policy{RequestTimeout: time.Minute, Retries: 0, ListTimeout: time.Minute}
}
func (a *scriptedAdapter) ListModels(context.Context, string) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (a *scriptedAdapter) Complete(_ context.Context, _ string, at llm.Attempt) (llm.AttemptResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, at.Messages)
	out := a.outputs[min(len(a.calls)-1, len(a.outputs)-1)]
	return llm.AttemptResult{Raw: out, Usage: &llm.Usage{TokensIn: 1, TokensOut: 1}}, nil
}

func reviewRequest(schema json.RawMessage) llm.Request {
	return llm.Request{
		Provider: llm.OpenRouter, Model: "m/x", SchemaName: reviewer.ReviewSchemaName, Schema: schema,
		Messages: []llm.Message{{Role: llm.System, Content: "SYSTEM"}, {Role: llm.User, Content: "USER"}},
	}
}

func TestSchemaParity_StrictReviewSchema(t *testing.T) {
	var g struct {
		Strict struct {
			Schema json.RawMessage `json:"schema"`
			Name   string          `json:"name"`
		} `json:"strict"`
	}
	loadGolden(t, "schema", &g)
	if g.Strict.Name != reviewer.ReviewSchemaName {
		t.Errorf("golden schema name = %q, want %q", g.Strict.Name, reviewer.ReviewSchemaName)
	}
	var want any
	if err := json.Unmarshal(g.Strict.Schema, &want); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]json.RawMessage{
		"StrictSchema(ReviewSchemaJSON)": nil,
		"ReviewProviderSchemaJSON":       reviewer.ReviewProviderSchemaJSON,
	} {
		got := raw
		if got == nil {
			var err error
			if got, err = llm.StrictSchema(reviewer.ReviewSchemaJSON); err != nil {
				t.Fatalf("StrictSchema(ReviewSchemaJSON): %v", err)
			}
		}
		var gotV any
		if err := json.Unmarshal(got, &gotV); err != nil {
			t.Fatalf("%s is not JSON: %v", name, err)
		}
		if diff := cmp.Diff(want, gotV); diff != "" {
			t.Errorf("%s differs from toJsonSchema(Review) (-want +got):\n%s", name, diff)
		}
	}
}

// reviewShape keeps what a validated Review answer carries; decoding into it strips unknown keys
// as Zod does. Numbers stay float64: the model may send 1e12.
type reviewShape struct {
	Verdict  string  `json:"verdict"`
	Summary  string  `json:"summary"`
	Score    float64 `json:"score"`
	Findings []struct {
		ID                 string   `json:"id"`
		Severity           string   `json:"severity"`
		Category           string   `json:"category"`
		Title              string   `json:"title"`
		File               string   `json:"file"`
		StartLine          float64  `json:"start_line"`
		EndLine            float64  `json:"end_line"`
		Rationale          string   `json:"rationale"`
		Suggestion         *string  `json:"suggestion"`
		Confidence         float64  `json:"confidence"`
		Kind               *string  `json:"kind"`
		TrifectaComponents []string `json:"trifecta_components"`
		Evidence           []struct {
			Component string  `json:"component"`
			File      string  `json:"file"`
			Line      float64 `json:"line"`
		} `json:"evidence"`
		Skill      *string `json:"skill"`
		OutOfScope *bool   `json:"out_of_scope"`
	} `json:"findings"`
}

func decodeShape(t testing.TB, raw []byte) reviewShape {
	t.Helper()
	var s reviewShape
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return s
}

const jsonTail = "\nReturn ONLY a single valid JSON object matching the schema, no prose."

func TestSchemaParity_ParseAndReprompt(t *testing.T) {
	var g struct {
		Parse []struct {
			Name            string          `json:"name"`
			Raw             string          `json:"raw"`
			OK              bool            `json:"ok"`
			Data            json.RawMessage `json:"data"`
			Error           string          `json:"error"`
			RepromptMessage string          `json:"repromptMessage"`
		} `json:"parse"`
	}
	loadGolden(t, "schema", &g)
	if len(g.Parse) == 0 {
		t.Fatal("schema.json has no parse cases")
	}
	schemas := map[string]json.RawMessage{
		"ReviewSchemaJSON":           reviewer.ReviewSchemaJSON,
		"ReviewValidationSchemaJSON": reviewer.ReviewValidationSchemaJSON,
	}
	for sname, schema := range schemas {
		for _, tc := range g.Parse {
			t.Run(sname+"/"+tc.Name, func(t *testing.T) {
				ad := &scriptedAdapter{outputs: []string{tc.Raw}}
				c := newClient(t, map[llm.Provider]llm.Adapter{llm.OpenRouter: ad})
				req := reviewRequest(schema)
				res, err := c.Structured(t.Context(), req)

				if tc.OK {
					if err != nil {
						t.Fatalf("Structured(%q) error = %v, want a valid review", tc.Raw, err)
					}
					if diff := cmp.Diff(decodeShape(t, tc.Data), decodeShape(t, res.Data)); diff != "" {
						t.Errorf("Structured(%q) data mismatch (-want +got):\n%s", tc.Raw, diff)
					}
					return
				}
				if err == nil {
					t.Fatalf("Structured(%q) succeeded with %s, want a failure", tc.Raw, res.Data)
				}
				// Default: 2 re-prompts → 3 attempts, each answered with the same output.
				if len(ad.calls) != 1+llm.DefaultReprompts {
					t.Fatalf("Structured(%q) made %d attempts, want %d", tc.Raw, len(ad.calls), 1+llm.DefaultReprompts)
				}
				second := ad.calls[1]
				if len(second) != 4 || second[2].Role != llm.Assistant || second[2].Content != tc.Raw || second[3].Role != llm.User {
					t.Fatalf("the re-prompt conversation is %+v, want the originals, the raw answer and one user message", second)
				}
				got := second[3].Content
				if strings.HasPrefix(tc.RepromptMessage, "Output was not valid JSON: ") {
					if !strings.HasPrefix(got, "Output was not valid JSON: ") || !strings.HasSuffix(got, jsonTail) {
						t.Errorf("re-prompt for %q = %q, want the 'Output was not valid JSON: …' form ending in %q", tc.Raw, got, jsonTail)
					}
					if !strings.HasSuffix(tc.RepromptMessage, jsonTail) {
						t.Errorf("golden re-prompt %q does not end in the JSON tail", tc.RepromptMessage)
					}
					return
				}
				if got != tc.RepromptMessage {
					t.Errorf("re-prompt for %q = %q, want %q", tc.Raw, got, tc.RepromptMessage)
				}
			})
		}
	}
}

func TestSchemaParity_ExtractJSON(t *testing.T) {
	var g struct {
		ExtractJSON []struct {
			Text string `json:"text"`
			Out  string `json:"out"`
		} `json:"extractJson"`
	}
	loadGolden(t, "schema", &g)
	if len(g.ExtractJSON) == 0 {
		t.Fatal("schema.json has no extractJson cases")
	}
	// Node's brace scan is not string-aware: a "}" inside a string literal ends the object early
	// and the extraction is not JSON. Go's scan skips strings (internal/llm/extract_test.go pins
	// it: `Sure! {"a":"}"} hope that helps`), so for these two golden cases Go deliberately
	// extracts the whole object where Node extracts a broken prefix.
	goExtracts := map[string]string{
		`{"a":"}"} tail`: `{"a":"}"}`,
		`{"s":"a\"}"}`:   `{"s":"a\"}"}`,
	}
	// extractJSON is unexported: a permissive schema makes Structured return exactly the extracted
	// text when it is JSON, and re-prompt with the parse error when it is not.
	for _, tc := range g.ExtractJSON {
		t.Run(tc.Text, func(t *testing.T) {
			ad := &scriptedAdapter{outputs: []string{tc.Text}}
			c := newClient(t, map[llm.Provider]llm.Adapter{llm.OpenRouter: ad})
			res, err := c.Structured(t.Context(), reviewRequest(json.RawMessage(`{}`)))
			if want, ok := goExtracts[tc.Text]; ok {
				if json.Valid([]byte(tc.Out)) {
					t.Fatalf("the golden extraction %q is valid JSON: the deliberate divergence no longer applies", tc.Out)
				}
				if err != nil || string(res.Data) != want {
					t.Errorf("Structured(%q) = %s, %v; want the string-aware extraction %q", tc.Text, res.Data, err, want)
				}
				return
			}
			if json.Valid([]byte(tc.Out)) {
				if err != nil {
					t.Fatalf("Structured(%q) error = %v, want the extracted %q", tc.Text, err, tc.Out)
				}
				if string(res.Data) != tc.Out {
					t.Errorf("Structured(%q) extracted %q, want %q", tc.Text, res.Data, tc.Out)
				}
				return
			}
			if err == nil {
				t.Fatalf("Structured(%q) = %s, want a JSON failure (Node extracts %q)", tc.Text, res.Data, tc.Out)
			}
			if len(ad.calls) < 2 || !strings.HasPrefix(ad.calls[1][3].Content, "Output was not valid JSON: ") {
				t.Errorf("Structured(%q) did not re-prompt with a JSON error: %+v", tc.Text, ad.calls)
			}
		})
	}
}

type llmUsage struct {
	TokensIn  int      `json:"tokensIn"`
	TokensOut int      `json:"tokensOut"`
	CostUSD   *float64 `json:"costUsd"`
}

type llmRun struct {
	Model    string `json:"model"`
	Requests []struct {
		Path          string         `json:"path"`
		Authorization string         `json:"authorization"`
		Body          map[string]any `json:"body"`
	} `json:"requests"`
	Usage    []llmUsage `json:"usage"`
	Warnings []string   `json:"warnings"`
	Result   *struct {
		Data      json.RawMessage `json:"data"`
		Model     string          `json:"model"`
		TokensIn  int             `json:"tokensIn"`
		TokensOut int             `json:"tokensOut"`
		CostUSD   *float64        `json:"costUsd"`
		Raw       string          `json:"raw"`
		Attempts  int             `json:"attempts"`
	} `json:"result"`
	Error string `json:"error"`
}

// normBody replaces the strict Review response_format by "$strictReview" and normalises the
// V8 parse error of a re-prompt message, as the golden shows them.
func normBody(t testing.TB, body map[string]any, strict any) map[string]any {
	t.Helper()
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = v
	}
	if rf, ok := body["response_format"].(map[string]any); ok {
		js, _ := rf["json_schema"].(map[string]any)
		if rf["type"] == "json_schema" && js != nil && js["name"] == "Review" && js["strict"] == true && cmp.Equal(js["schema"], strict) {
			out["response_format"] = "$strictReview"
		}
	}
	if msgs, ok := body["messages"].([]any); ok {
		norm := make([]any, len(msgs))
		for i, m := range msgs {
			mm := map[string]any{}
			for k, v := range m.(map[string]any) {
				mm[k] = v
			}
			if s, ok := mm["content"].(string); ok && mm["role"] == "user" {
				mm["content"] = normJSONError(s)
			}
			norm[i] = mm
		}
		out["messages"] = norm
	}
	return out
}

func TestLLMParity_ProviderCalls(t *testing.T) {
	var g struct {
		Scenarios []struct {
			Name        string           `json:"name"`
			Provider    string           `json:"provider"`
			Temperature *float64         `json:"temperature"`
			MaxRetries  int              `json:"maxRetries"`
			SessionID   *string          `json:"sessionId"`
			Responses   []map[string]any `json:"responses"`
			Runs        []llmRun         `json:"runs"`
		} `json:"scenarios"`
	}
	loadGolden(t, "llm", &g)
	var schemaGolden struct {
		Strict struct {
			Schema any `json:"schema"`
		} `json:"strict"`
	}
	loadGolden(t, "schema", &schemaGolden)
	strict := schemaGolden.Strict.Schema
	if len(g.Scenarios) == 0 {
		t.Fatal("llm.json has no scenarios")
	}

	for _, sc := range g.Scenarios {
		for _, run := range sc.Runs {
			t.Run(sc.Name+"/"+run.Model, func(t *testing.T) {
				var (
					mu       sync.Mutex
					requests []map[string]any
					auth     []string
					paths    []string
					served   int
				)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					var body map[string]any
					if err := json.Unmarshal(raw, &body); err != nil {
						t.Errorf("request body is not JSON: %v", err)
					}
					mu.Lock()
					requests = append(requests, body)
					auth = append(auth, r.Header.Get("Authorization"))
					paths = append(paths, r.URL.Path)
					i := served
					served++
					mu.Unlock()

					w.Header().Set("Content-Type", "application/json")
					if i >= len(sc.Responses) {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"message":"no scripted response left"}}`)
						return
					}
					r0 := sc.Responses[i]
					payload := r0["raw"]
					if payload == nil {
						payload = map[string]any{
							"id": "gen", "object": "chat.completion", "model": run.Model,
							"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": r0["content"]}}},
						}
						if u, ok := r0["usage"]; ok {
							payload.(map[string]any)["usage"] = u
						}
					}
					_ = json.NewEncoder(w).Encode(payload)
				}))
				defer srv.Close()

				provider := llm.Provider(sc.Provider)
				var ad llm.Adapter
				opts := openaicompat.Options{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()}
				if provider == llm.OpenRouter {
					ad = openaicompat.NewOpenRouter(opts)
				} else {
					ad = openaicompat.NewOpenAI(opts)
				}
				c := newClient(t, map[llm.Provider]llm.Adapter{provider: ad})

				var usage []llmUsage
				req := llm.Request{
					Provider: provider, Model: run.Model, SchemaName: "Review", Schema: reviewer.ReviewSchemaJSON,
					Messages:    []llm.Message{{Role: llm.System, Content: "SYSTEM"}, {Role: llm.User, Content: "USER"}},
					Temperature: sc.Temperature, Reprompts: sc.MaxRetries,
					OnUsage: func(u llm.AttemptUsage) {
						usage = append(usage, llmUsage{TokensIn: u.TokensIn, TokensOut: u.TokensOut, CostUSD: u.CostUSD})
					},
				}
				if req.Reprompts == 0 {
					req.Reprompts = llm.NoReprompts
				}
				if sc.SessionID != nil {
					req.SessionID = *sc.SessionID
				}
				res, err := c.Structured(t.Context(), req)

				mu.Lock()
				defer mu.Unlock()
				if len(requests) != len(run.Requests) {
					t.Fatalf("the provider got %d requests, want %d", len(requests), len(run.Requests))
				}
				for i, want := range run.Requests {
					if paths[i] != want.Path || auth[i] != want.Authorization {
						t.Errorf("request %d: %s with %q, want %s with %q", i, paths[i], auth[i], want.Path, want.Authorization)
					}
					// Key order of the body is not compared: it is JSON for a provider.
					if diff := cmp.Diff(normBody(t, want.Body, strict), normBody(t, requests[i], strict)); diff != "" {
						t.Errorf("request %d body mismatch (-want +got):\n%s", i, diff)
					}
				}
				if diff := cmp.Diff(run.Usage, usage, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("OnUsage reports mismatch (-want +got):\n%s", diff)
				}

				if run.Result == nil {
					if err == nil {
						t.Fatalf("Structured() = %s, want the error %q", res.Data, run.Error)
					}
					wantErr := run.Error
					if sc.Provider == "openai" {
						// The generator drives Node's OpenRouterProvider for every scenario, so its failure
						// text always names OpenRouter; the OpenAI provider names itself (AC4030).
						wantErr = strings.Replace(wantErr, "OpenRouter structured output", "OpenAI structured output", 1)
					}
					if got, want := normJSONError(err.Error()), normJSONError(wantErr); got != want {
						t.Errorf("Structured() error = %q, want %q", got, want)
					}
					return
				}
				if err != nil {
					t.Fatalf("Structured() error = %v, want a result", err)
				}
				want := run.Result
				if diff := cmp.Diff(decodeShape(t, want.Data), decodeShape(t, res.Data)); diff != "" {
					t.Errorf("result data mismatch (-want +got):\n%s", diff)
				}
				if res.Model != want.Model || res.TokensIn != want.TokensIn || res.TokensOut != want.TokensOut ||
					res.Raw != want.Raw || res.Attempts != want.Attempts {
					t.Errorf("result = {model %q in %d out %d attempts %d raw %q}, want {model %q in %d out %d attempts %d raw %q}",
						res.Model, res.TokensIn, res.TokensOut, res.Attempts, res.Raw,
						want.Model, want.TokensIn, want.TokensOut, want.Attempts, want.Raw)
				}
				if diff := cmp.Diff(want.CostUSD, res.CostUSD); diff != "" {
					t.Errorf("result cost mismatch (-want +got):\n%s", diff)
				}
			})
		}
	}
}
