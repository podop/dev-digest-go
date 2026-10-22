package parity_test

// S30 (AC5057, AC5058): the structured-output layer of the Phase 5 features against
// testdata/goldens/schemas.json — the provider-facing (strict) schemas of IntentClassification,
// PrBriefLlm and OnboardingTour, the parse and re-prompt texts for invalid model answers, the mock
// provider's fixtures, and the parameters of the intent call.

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	briefdomain "dev-digest-go/backend/internal/brief/domain"
	intentdomain "dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	onboardingdomain "dev-digest-go/backend/internal/onboarding/domain"
	"dev-digest-go/backend/internal/platform/retry"
)

type keyMap map[string]string

func (k keyMap) Get(name string) string { return k[name] }

// nilPricer prices nothing, so the cost of a call is what the provider reported.
type nilPricer struct{}

func (nilPricer) Estimate(string, int, int) *float64 { return nil }

func newClient(t testing.TB, adapters map[llm.Provider]llm.Adapter) *llm.Client {
	t.Helper()
	c := llm.New(llm.Options{
		Keys:     keyMap{"OPENROUTER_API_KEY": "sk-test-key"},
		Adapters: adapters,
		Logger:   slog.New(slog.DiscardHandler),
		Prices:   nilPricer{},
		Retry:    retry.Options{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: func(time.Duration) time.Duration { return 0 }},
	})
	t.Cleanup(c.Close)
	return c
}

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

// phase5Schemas are the three structured-output schemas of this phase: the golden's schema name,
// the schema an answer is validated against (as Zod validates it) and, where the package keeps
// the provider-facing document, that document.
var phase5Schemas = []struct {
	name       string
	validation json.RawMessage
	provider   json.RawMessage // nil = only llm.StrictSchema(validation) is built
}{
	{"IntentClassification", intentdomain.ClassificationSchemaJSON, nil},
	{"PrBriefLlm", briefdomain.SchemaJSON, nil},
	{"OnboardingTour", onboardingdomain.SchemaJSON, onboardingdomain.ProviderSchemaJSON},
}

func TestSchemasGolden_StrictProviderSchemas(t *testing.T) {
	var g struct {
		Schemas map[string]struct {
			SchemaName string `json:"schemaName"`
			Strict     struct {
				Schema json.RawMessage `json:"schema"`
				Name   string          `json:"name"`
			} `json:"strict"`
		} `json:"schemas"`
	}
	loadGolden(t, "schemas", &g)
	for _, s := range phase5Schemas {
		t.Run(s.name, func(t *testing.T) {
			want, ok := g.Schemas[s.name]
			if !ok {
				t.Fatalf("schemas.json has no %s", s.name)
			}
			if want.Strict.Name != s.name || want.SchemaName != s.name {
				t.Errorf("golden names = %q, %q, want %q", want.SchemaName, want.Strict.Name, s.name)
			}
			wantV := jsonValue(t, want.Strict.Schema)
			strict, err := llm.StrictSchema(s.validation)
			if err != nil {
				t.Fatalf("StrictSchema(%s) error = %v", s.name, err)
			}
			if diff := cmp.Diff(wantV, jsonValue(t, strict)); diff != "" {
				t.Errorf("StrictSchema(%s) differs from toJsonSchema (-want +got):\n%s", s.name, diff)
			}
			if s.provider != nil {
				if diff := cmp.Diff(wantV, jsonValue(t, s.provider)); diff != "" {
					t.Errorf("the provider schema of %s differs from toJsonSchema (-want +got):\n%s", s.name, diff)
				}
			}
		})
	}
}

// stripToSchema drops the keys a schema does not list, as Zod's object parsing does; absent
// optional keys stay absent.
func stripToSchema(schema map[string]any, v any) any {
	if alts, ok := schema["anyOf"].([]any); ok {
		for _, alt := range alts {
			if m, ok := alt.(map[string]any); ok && m["type"] != "null" {
				return stripToSchema(m, v)
			}
		}
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			return x
		}
		out := map[string]any{}
		for k, sub := range props {
			if val, ok := x[k]; ok {
				m, _ := sub.(map[string]any)
				out[k] = stripToSchema(m, val)
			}
		}
		return out
	case []any:
		items, _ := schema["items"].(map[string]any)
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripToSchema(items, e)
		}
		return out
	}
	return v
}

var v8Message = regexp.MustCompile(`(Output was not valid JSON: )[^\n]*`)

func normJSONError(s string) string { return v8Message.ReplaceAllString(s, "${1}<parse error>") }

const jsonTail = "\nReturn ONLY a single valid JSON object matching the schema, no prose."

func TestSchemasGolden_ParseAndReprompt(t *testing.T) {
	var g struct {
		Schemas map[string]struct {
			Parse []struct {
				Name            string          `json:"name"`
				Raw             string          `json:"raw"`
				OK              bool            `json:"ok"`
				Data            json.RawMessage `json:"data"`
				Error           string          `json:"error"`
				RepromptMessage string          `json:"repromptMessage"`
			} `json:"parse"`
		} `json:"schemas"`
	}
	loadGolden(t, "schemas", &g)
	for _, s := range phase5Schemas {
		cases := g.Schemas[s.name].Parse
		if len(cases) == 0 {
			t.Fatalf("schemas.json has no parse cases for %s", s.name)
		}
		var validation map[string]any
		if err := json.Unmarshal(s.validation, &validation); err != nil {
			t.Fatalf("schema of %s is not JSON: %v", s.name, err)
		}
		for _, tc := range cases {
			t.Run(s.name+"/"+tc.Name, func(t *testing.T) {
				ad := &scriptedAdapter{outputs: []string{tc.Raw}}
				c := newClient(t, map[llm.Provider]llm.Adapter{llm.OpenRouter: ad})
				res, err := c.Structured(t.Context(), llm.Request{
					Provider: llm.OpenRouter, Model: "m/x", SchemaName: s.name, Schema: s.validation,
					Messages: []llm.Message{{Role: llm.System, Content: "SYSTEM"}, {Role: llm.User, Content: "USER"}},
				})

				if tc.OK {
					if err != nil {
						t.Fatalf("Structured(%q) error = %v, want a valid answer", tc.Raw, err)
					}
					want := stripToSchema(validation, jsonValue(t, tc.Data))
					got := stripToSchema(validation, jsonValue(t, res.Data))
					if diff := cmp.Diff(want, got); diff != "" {
						t.Errorf("Structured(%q) data mismatch (-want +got):\n%s", tc.Raw, diff)
					}
					return
				}
				if err == nil {
					t.Fatalf("Structured(%q) succeeded with %s, want a failure", tc.Raw, res.Data)
				}
				if len(ad.calls) != 1+llm.DefaultReprompts {
					t.Fatalf("Structured(%q) made %d attempts, want %d", tc.Raw, len(ad.calls), 1+llm.DefaultReprompts)
				}
				second := ad.calls[1]
				if len(second) != 4 || second[2].Role != llm.Assistant || second[2].Content != tc.Raw || second[3].Role != llm.User {
					t.Fatalf("the re-prompt conversation is %+v, want the originals, the raw answer and one user message", second)
				}
				got := second[3].Content
				if strings.HasPrefix(tc.RepromptMessage, "Output was not valid JSON: ") {
					// V8's JSON.parse message cannot be reproduced; its frame can.
					if !strings.HasPrefix(got, "Output was not valid JSON: ") || !strings.HasSuffix(got, jsonTail) {
						t.Errorf("re-prompt for %q = %q, want the 'Output was not valid JSON: …' form ending in %q", tc.Raw, got, jsonTail)
					}
					return
				}
				if got != tc.RepromptMessage {
					t.Errorf("re-prompt for %q =\n%q\nwant\n%q", tc.Raw, got, tc.RepromptMessage)
				}
			})
		}
	}
}

// AC5058: the mock provider's fixtures for the Phase 5 features equal Node's byte for byte, and
// the schemas without a fixture fail with the same text.
func TestSchemasGolden_MockFixtures(t *testing.T) {
	var g struct {
		Mock []struct {
			SchemaName   string `json:"schemaName"`
			UsageReports []struct {
				TokensIn  int     `json:"tokensIn"`
				TokensOut int     `json:"tokensOut"`
				CostUSD   float64 `json:"costUsd"`
			} `json:"usageReports"`
			Result *struct {
				Data      json.RawMessage `json:"data"`
				Model     string          `json:"model"`
				TokensIn  int             `json:"tokensIn"`
				TokensOut int             `json:"tokensOut"`
				CostUSD   float64         `json:"costUsd"`
				Raw       string          `json:"raw"`
				Attempts  int             `json:"attempts"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"mock"`
	}
	loadGolden(t, "schemas", &g)
	validation := map[string]json.RawMessage{}
	for _, s := range phase5Schemas {
		validation[s.name] = s.validation
	}
	var checked int
	for _, tc := range g.Mock {
		// Review and ConventionExtraction answers depend on the prompt (Phase 2 and 4 tests).
		if tc.SchemaName == "Review" || tc.SchemaName == "ConventionExtraction" {
			continue
		}
		checked++
		t.Run(tc.SchemaName, func(t *testing.T) {
			c := newClient(t, map[llm.Provider]llm.Adapter{llm.OpenRouter: mock.New(llm.OpenRouter, mock.Options{})})
			schema, ok := validation[tc.SchemaName]
			if !ok {
				schema = json.RawMessage(`{}`)
			}
			var usage []llm.AttemptUsage
			res, err := c.Structured(t.Context(), llm.Request{
				Provider: llm.OpenRouter, Model: "mock/reviewer", SchemaName: tc.SchemaName, Schema: schema,
				Messages: []llm.Message{{Role: llm.System, Content: "S"}, {Role: llm.User, Content: "U"}},
				OnUsage:  func(u llm.AttemptUsage) { usage = append(usage, u) },
			})
			if tc.Error != nil {
				if err == nil || !strings.Contains(err.Error(), tc.Error.Message) {
					t.Fatalf("Structured(%s) error = %v, want one containing %q", tc.SchemaName, err, tc.Error.Message)
				}
				if len(usage) != len(tc.UsageReports) {
					t.Errorf("Structured(%s) reported %d usages, want %d", tc.SchemaName, len(usage), len(tc.UsageReports))
				}
				return
			}
			if err != nil {
				t.Fatalf("Structured(%s) error = %v", tc.SchemaName, err)
			}
			want := tc.Result
			if res.Raw != want.Raw {
				t.Errorf("Structured(%s) raw =\n%s\nwant\n%s", tc.SchemaName, res.Raw, want.Raw)
			}
			if diff := cmp.Diff(jsonValue(t, want.Data), jsonValue(t, res.Data)); diff != "" {
				t.Errorf("Structured(%s) data mismatch (-want +got):\n%s", tc.SchemaName, diff)
			}
			if res.Model != want.Model || res.TokensIn != want.TokensIn || res.TokensOut != want.TokensOut || res.Attempts != want.Attempts ||
				res.CostUSD == nil || *res.CostUSD != want.CostUSD {
				t.Errorf("Structured(%s) = {model %q in %d out %d attempts %d cost %v}, want {model %q in %d out %d attempts %d cost %v}",
					tc.SchemaName, res.Model, res.TokensIn, res.TokensOut, res.Attempts, res.CostUSD,
					want.Model, want.TokensIn, want.TokensOut, want.Attempts, want.CostUSD)
			}
			if len(usage) != len(tc.UsageReports) {
				t.Fatalf("Structured(%s) reported %d usages, want %d", tc.SchemaName, len(usage), len(tc.UsageReports))
			}
			for i, u := range tc.UsageReports {
				if usage[i].TokensIn != u.TokensIn || usage[i].TokensOut != u.TokensOut || usage[i].CostUSD == nil || *usage[i].CostUSD != u.CostUSD {
					t.Errorf("usage report %d = %+v, want %+v", i, usage[i], u)
				}
			}
		})
	}
	if checked < 4 {
		t.Errorf("checked %d mock fixtures, want IntentClassification, PrBriefLlm, OnboardingTour and Unknown", checked)
	}
}

// The parameters of the intent call equal the ones Node's LlmIntentModel hands to the provider.
func TestSchemasGolden_IntentCallParameters(t *testing.T) {
	var g struct {
		Calls []struct {
			Feature  string `json:"feature"`
			Requests []struct {
				SchemaName       string  `json:"schemaName"`
				Temperature      float64 `json:"temperature"`
				MaxTokens        int     `json:"maxTokens"`
				MaxRetries       int     `json:"maxRetries"`
				HasUsageObserver bool    `json:"hasUsageObserver"`
				HasSessionID     bool    `json:"hasSessionId"`
				Roles            []string
			} `json:"requests"`
		} `json:"calls"`
	}
	loadGolden(t, "schemas", &g)
	var seen int
	for _, c := range g.Calls {
		if c.Feature != "intent" {
			continue
		}
		for _, r := range c.Requests {
			seen++
			if r.SchemaName != intentdomain.SchemaName || r.Temperature != intentdomain.Temperature ||
				r.MaxTokens != intentdomain.MaxOutputTokens || r.MaxRetries != intentdomain.Reprompts {
				t.Errorf("intent call = {%s temperature %v maxTokens %d reprompts %d}, want {%s %v %d %d}",
					intentdomain.SchemaName, intentdomain.Temperature, intentdomain.MaxOutputTokens, intentdomain.Reprompts,
					r.SchemaName, r.Temperature, r.MaxTokens, r.MaxRetries)
			}
		}
	}
	if seen == 0 {
		t.Error("schemas.json holds no intent call")
	}
}
